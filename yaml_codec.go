package config

import (
	"bytes"
	"fmt"
	"io"
	"strings"

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
// The judgement is this module's; the detection is yamldoc's. It reports what
// it cannot round-trip safely, and refusing is the policy applied to that
// report.
func (YAMLCodec) Check(path string, src []byte) error {
	doc, err := yamldoc.Parse(src)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrBackendParse, path, err)
	}

	if unsupported := doc.Unsupported(); len(unsupported) > 0 {
		return fmt.Errorf("%w: %s: %s", ErrBackendUnsafe, path, unsupported[0])
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
// styles survive. Nothing here decodes values from that tree: the
// documents-versus-values boundary is what keeps the two YAML parsers from
// disagreeing about types.
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
		//
		// Seeding with an empty flow mapping instead looks tidier and is not:
		// yamldoc re-emits in the style it found, so every subsequent key is
		// written in flow style too, and anything nested comes back as YAML
		// that does not parse. A created file should look like one a person
		// would have written.
		seeded, remaining, err := seedDocument(path, source, edits)
		if err != nil {
			return nil, err
		}

		source, edits = seeded, remaining
	}

	doc, err := yamldoc.Parse(source)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrBackendParse, path, err)
	}

	if unsupported := doc.Unsupported(); len(unsupported) > 0 {
		return nil, fmt.Errorf("%w: %s: %s", ErrBackendUnsafe, path, unsupported[0])
	}

	docs := doc.Documents()

	for _, edit := range edits {
		if err := applyOne(path, docs, edit); err != nil {
			return nil, err
		}
	}

	out, err := doc.Bytes()
	if err != nil {
		return nil, fmt.Errorf("config: rendering %s: %w", path, err)
	}

	return out, nil
}

// applyOne applies a single edit to the document it addresses.
func applyOne(path string, docs []*yamldoc.Document, edit Edit) error {
	if edit.Document >= len(docs) {
		return fmt.Errorf("%w: %s has %d document(s), edit addressed document %d",
			ErrInternal, path, len(docs), edit.Document)
	}

	target := docs[edit.Document]
	addressed := documentPath(target, edit.Path)

	if !edit.Remove {
		if err := target.Set(addressed, edit.Value); err != nil {
			return fmt.Errorf("config: setting %s in %s: %w", edit.Path, path, err)
		}

		return nil
	}

	if err := target.Remove(addressed); err != nil {
		// Removing something already absent reaches the desired end state, so
		// it is not worth failing a batch over.
		if errors.Is(err, yamldoc.ErrNotFound) {
			return nil
		}

		return fmt.Errorf("config: removing %s from %s: %w", edit.Path, path, err)
	}

	return nil
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

	doc, err := yamldoc.Parse(source)
	if err != nil {
		// Leave a genuinely malformed file to the parse error below, which
		// names the problem properly.
		return false
	}

	docs := doc.Documents()
	if len(docs) == 0 {
		return true
	}

	_, ok := docs[0].Keys("")

	return !ok
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

// documentPath renders a config path in the spelling the document already uses.
//
// Keys are matched case-insensitively everywhere else in the module, so a
// caller may address server.port as Server.Port and routing will resolve it to
// the layer that defines it. The document layer has no such rule: it matches
// literally, so handing it the caller's spelling wrote a second, differently
// cased block beside the real one — leaving the file holding both and the
// original value untouched.
//
// Each segment is therefore resolved against the keys actually present. A
// segment that matches nothing is a key being created, and takes the module's
// normalised form.
func documentPath(doc *yamldoc.Document, path string) string {
	segs := splitPath(path)
	if segs == nil {
		return path
	}

	resolved := make([]string, 0, len(segs))

	for _, seg := range segs {
		resolved = append(resolved, documentKey(doc, strings.Join(resolved, "."), seg))
	}

	return strings.Join(resolved, ".")
}

// documentKey returns the document's own spelling of a key, or the normalised
// form when the document does not have it.
func documentKey(doc *yamldoc.Document, parent, seg string) string {
	keys, ok := doc.Keys(parent)
	if !ok {
		return seg
	}

	for _, k := range keys {
		if normaliseKey(k) == seg {
			return k
		}
	}

	return seg
}
