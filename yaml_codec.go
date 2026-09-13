package config

import (
	"bytes"
	"fmt"
	"io"

	"gitlab.com/phpboyscout/go/errors"
	"gitlab.com/phpboyscout/go/yamldoc"
	yaml "go.yaml.in/yaml/v3"
)

// NewFileBackend returns a backend reading YAML from a path on the given
// filesystem.
//
// YAML is the module's built-in format: it is the default, and the
// comment-preserving write path — the module's headline feature — is built on
// yamldoc. Every other format ships as a sibling module supplying its own codec
// to [NewCodecBackend]; this is that call with the YAML codec.
func NewFileBackend(filesystem FS, path string) Backend {
	return NewCodecBackend(filesystem, path, YAMLCodec{})
}

// YAMLCodec reads and edits YAML configuration. It is what [NewFileBackend] and
// [WithFiles] use, and it is exported so a backend adapter can reuse it as a
// value decoder — a Consul or etcd key whose value is a YAML document, decoded
// into a subtree via that adapter's WithValueCodec option. It is a [Codec] (and
// an [EditingCodec]); the sibling format adapters export theirs as `Codec`, but
// the plain name is the interface here, so this one carries the format.
//
// It reads the file twice, deliberately. Values are decoded by the YAML value
// parser, while document structure — comments, positions, and whether the file
// can be edited safely at all — comes from yamldoc. The two disagree about
// scalar types (`8080` decodes as int in one and uint64 in the other, and large
// integers survive in one and are destroyed in the other), so the boundary
// between documents and values must not be crossed. Values never come from
// yamldoc; documents never come from the value parser.
type YAMLCodec struct{}

// PreservesComments reports that YAML edits retain comments and formatting,
// which is what backs the file backend's PreservesComments capability. It
// satisfies [CommentPreservingCodec].
func (YAMLCodec) PreservesComments() bool { return true }

// Decode decodes every YAML document in a source into its own map.
//
// An empty document is a nil entry rather than an omission, so the documents
// that follow it keep their index — which is how routing, precedence and
// provenance treat documents and files uniformly, and it fixes a defect in the
// incumbent, which reads the first document of a multi-document file and
// silently discards the rest.
func (YAMLCodec) Decode(path string, src []byte) ([]map[string]any, error) {
	dec := yaml.NewDecoder(bytes.NewReader(src))

	var docs []map[string]any

	for index := 0; ; index++ {
		var values map[string]any

		err := dec.Decode(&values)
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("%w: %s document %d: %w", ErrBackendParse, path, index, err)
		}

		docs = append(docs, values)
	}

	return docs, nil
}

// Check reports whether a source can be edited without risking corruption.
//
// The judgement is this module's; the detection is yamldoc's. Bytes that are
// not YAML fail to parse. A document that parses but does not mean anything
// under its schema, such as one with a dangling alias or two keys that are the
// same value, is reported by validation, and every edit that depended on the
// broken part would be refused; refusing the whole file up front is the policy
// applied to that report.
func (YAMLCodec) Check(path string, src []byte) error {
	file, err := yamldoc.Parse(src, yamldoc.Options{})
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrBackendParse, path, err)
	}

	if _, err := file.Snapshot().Validate(yamldoc.ValidationOptions{}); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrBackendUnsafe, path, err)
	}

	return nil
}

// Empty returns the content of a new, empty YAML document.
//
// A YAML file created from nothing needs no preamble — an empty file is a valid
// empty document. The create-a-file path seeds a mapping to edit into from
// within Apply, so nothing here has to.
func (YAMLCodec) Empty() []byte { return nil }

// Apply edits the document tree and re-emits it.
//
// Editing goes through yamldoc so comments, key order, quoting and block
// styles survive, and every byte outside the edits comes back exactly. The
// batch is one transaction: a failure anywhere in it leaves nothing applied.
// Nothing here decodes values from that tree: the documents-versus-values
// boundary is what keeps the two YAML parsers from disagreeing about types.
func (YAMLCodec) Apply(path string, src []byte, edits []Edit) ([]byte, error) {
	source := src

	if needsSeed(source) {
		// The file has no mapping to edit into: it is absent, empty, or holds
		// only comments, blank lines or a bare document marker. Commenting a
		// config file out entirely is an ordinary thing to do, and it must not
		// make the file permanently unwritable.
		//
		// Whatever is already there is kept and the first key is rendered
		// beneath it, so a commented-out header survives being written to.
		seeded, remaining, err := seedDocument(path, source, edits)
		if err != nil {
			return nil, err
		}

		source, edits = seeded, remaining
	}

	file, err := yamldoc.Parse(source, yamldoc.Options{})
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrBackendParse, path, err)
	}

	if _, err := file.Snapshot().Validate(yamldoc.ValidationOptions{}); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrBackendUnsafe, path, err)
	}

	err = file.Edit(func(tx *yamldoc.Transaction) error {
		for _, edit := range edits {
			if err := applyOne(tx, path, edit); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	out, err := file.Snapshot().Bytes()
	if err != nil {
		return nil, fmt.Errorf("config: rendering %s: %w", path, err)
	}

	return out, nil
}

// applyOne applies a single edit to the document it addresses.
//
// Every command produces a new working revision, so the document is selected
// afresh from the transaction's snapshot for each edit.
func applyOne(tx *yamldoc.Transaction, path string, edit Edit) error {
	snapshot, err := tx.Snapshot()
	if err != nil {
		return err
	}

	docs, err := snapshot.Documents()
	if err != nil {
		return err
	}

	if edit.Document >= len(docs) {
		return fmt.Errorf("%w: %s has %d document(s), edit addressed document %d",
			ErrInternal, path, len(docs), edit.Document)
	}

	root, err := docs[edit.Document].Root()
	if err != nil {
		return err
	}

	if !edit.Remove {
		if err := setPath(tx, root, edit.Path, edit.Value); err != nil {
			return fmt.Errorf("config: setting %s in %s: %w", edit.Path, path, err)
		}

		return nil
	}

	if err := removePath(tx, root, edit.Path); err != nil {
		return fmt.Errorf("config: removing %s from %s: %w", edit.Path, path, err)
	}

	return nil
}

// setPath writes value at a dotted path under root, creating missing mapping
// ancestors, each addressed in the spelling the document already uses.
func setPath(tx *yamldoc.Transaction, root yamldoc.Node, path string, value any) error {
	segs := splitPath(path)
	if segs == nil {
		return fmt.Errorf("%w: %q", ErrInvalidTarget, path)
	}

	container := root

	for i, seg := range segs[:len(segs)-1] {
		child, err := container.Get(documentStep(container, seg))
		if errors.Is(err, yamldoc.ErrNotFound) {
			nested, err := nestedMapping(segs[i+1:], value)
			if err != nil {
				return err
			}

			return tx.Set(container, documentStep(container, seg), nested)
		}

		if err != nil {
			return err
		}

		container = child
	}

	return tx.Set(container, documentStep(container, segs[len(segs)-1]), value)
}

// removePath removes the entry at a dotted path under root. Removing something
// already absent reaches the desired end state, so it is not worth failing a
// batch over.
func removePath(tx *yamldoc.Transaction, root yamldoc.Node, path string) error {
	segs := splitPath(path)
	if segs == nil {
		return fmt.Errorf("%w: %q", ErrInvalidTarget, path)
	}

	container := root

	for _, seg := range segs[:len(segs)-1] {
		child, err := container.Get(documentStep(container, seg))
		if errors.Is(err, yamldoc.ErrNotFound) {
			return nil
		}

		if err != nil {
			return err
		}

		container = child
	}

	return tx.RemoveIfPresent(container, documentStep(container, segs[len(segs)-1]))
}

// nestedMapping wraps value in one mapping per remaining segment so a single
// Set creates the whole missing branch, keys in the module's normalised form.
func nestedMapping(segs []string, value any) (any, error) {
	for i := len(segs) - 1; i >= 0; i-- {
		key, err := yamldoc.StringKey(segs[i])
		if err != nil {
			return nil, err
		}

		value = yamldoc.MappingInput{{Key: key, Value: value}}
	}

	return value, nil
}

// seedDocument renders the first assignment as a block-style document and
// returns it with the edits still to be applied.
//
// Creating a file is the one case where there is nothing to preserve, so the
// value layer may render it. Every later edit goes back through yamldoc, which
// is what keeps the boundary meaningful: the document layer owns editing, and
// this owns only the moment before a document exists.
func seedDocument(path string, original []byte, edits []Edit) ([]byte, []Edit, error) {
	for _, edit := range edits {
		if edit.Document != 0 {
			// Creating a file produces one document. Addressing a later one is
			// a request that cannot be met, and saying so names the caller's
			// mistake rather than reporting an internal invariant violation.
			return nil, nil, fmt.Errorf(
				"%w: %s does not exist, so it cannot be created with document %d",
				ErrInvalidTarget, path, edit.Document)
		}
	}

	// A placeholder rather than the first real value. The document layer has
	// nothing to edit into until a mapping exists, and rendering a user's value
	// here to create one would mean two emitters writing the same kind of
	// content — this one, and yamldoc for every key after it. They do not agree
	// about quoting or escaping, and the guarantee that invisible and
	// bidirectional characters are escaped is a property of yamldoc's emitter,
	// so a value written by this path would sit outside it.
	//
	// The placeholder carries no user data, so nothing that matters is rendered
	// here. Every real value is written by yamldoc, into the mapping this
	// creates, and the placeholder is removed before anything is emitted.
	// Separated by a blank line, so whatever was already in the file reads as a
	// section comment rather than the placeholder's own. A head comment
	// directly above a key is removed with it — the rule that makes deletion
	// tidy — and without the blank line the file's header would go with the
	// placeholder.
	seeded := append(preamble(original), '\n')
	seeded = append(seeded, []byte(seedKey+": null\n")...)

	return seeded, append(edits, Remove(seedKey).asEdit()), nil
}

// seedKey is the placeholder a created file is given so the document layer has
// a mapping to edit into. It is removed in the same pass, so it never reaches
// disk.
const seedKey = "x-config-seed"

// needsSeed reports whether a source has no mapping for an edit to land in.
//
// Byte-emptiness is not the same question. A file holding only comments, blank
// lines or a bare document marker parses successfully and yields no mapping, so
// yamldoc has nothing to set into and every write to it would fail.
func needsSeed(source []byte) bool {
	if len(source) == 0 {
		return true
	}

	file, err := yamldoc.Parse(source, yamldoc.Options{})
	if err != nil {
		// Leave a genuinely malformed file to the parse error below, which
		// names the problem properly.
		return false
	}

	docs, err := file.Snapshot().Documents()
	if err != nil || len(docs) == 0 {
		return true
	}

	root, err := docs[0].Root()
	if err != nil {
		return true
	}

	info, err := root.Syntax()

	return err != nil || info.Kind != yamldoc.KindMapping
}

// preamble returns the source with a trailing newline guaranteed, so rendered
// content appended after it starts on its own line.
func preamble(original []byte) []byte {
	if len(original) == 0 {
		return nil
	}

	out := make([]byte, 0, len(original)+1)
	out = append(out, original...)

	if out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}

	return out
}

// documentStep addresses one segment of a config path in the spelling the
// document already uses.
//
// Keys are matched case-insensitively everywhere else in the module, so a
// caller may address server.port as Server.Port and routing will resolve it to
// the layer that defines it. The document layer has no such rule: it matches
// literally, so handing it the caller's spelling would write a second,
// differently cased block beside the real one and leave the original value
// untouched.
//
// The segment is therefore resolved against the keys actually present. A
// segment that matches nothing is a key being created, and takes the module's
// normalised form.
func documentStep(container yamldoc.Node, seg string) yamldoc.Step {
	entries, err := container.Entries()
	if err != nil {
		return yamldoc.StringStep(seg)
	}

	for _, entry := range entries {
		key, err := entry.Key.String()
		if err == nil && normaliseKey(key) == seg {
			return yamldoc.StringStep(key)
		}
	}

	return yamldoc.StringStep(seg)
}
