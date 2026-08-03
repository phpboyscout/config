// Package conformance is a reusable test suite that a config codec adapter runs
// against its own codec to prove it behaves like a first-class backend.
//
// The point is the D3 trap. The file machinery a codec plugs into — reading,
// fingerprinting for conflict detection, staging, atomic rename, rollback,
// watching — lives in the core and is shared, so an adapter cannot reimplement
// it wrongly. This suite is what confirms, per adapter, that a real source in
// the adapter's format takes part in merge, precedence, provenance and conflict
// detection exactly as the built-in YAML one does, rather than trusting it to.
//
// An adapter writes one test:
//
//	func TestConformance(t *testing.T) {
//		conformance.Run(t, conformance.Suite{
//			Codec:      jsonfile.Codec{},
//			Sample:     []byte(`{"server":{"port":"9090"}}`),
//			Defines:    map[string]string{"server.port": "9090"},
//			WriteKey:   "server.host", WriteValue: "localhost",
//		})
//	}
//
// It uses the standard library testing package and nothing else — deliberately
// no testify — so an adapter that runs it takes on no assertion-library
// dependency it would otherwise avoid.
package conformance

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/go/errors"

	"gitlab.com/phpboyscout/go/config"
)

// Suite describes a codec and the minimal sample of its format the assertions
// need. Read-only codecs supply the first four fields; an EditingCodec supplies
// WriteKey and WriteValue as well.
type Suite struct {
	// Codec is the codec under test. When it also satisfies
	// [config.EditingCodec], the editing assertions run.
	Codec config.Codec

	// Sample is a valid single-document source in the codec's format.
	Sample []byte

	// Defines is the keys Sample exposes and the value each reads back as
	// through a View, so the suite can assert decoding, merge and provenance
	// without knowing the format. At least one entry is required.
	Defines map[string]string

	// WriteKey and WriteValue are a key the codec can set and the value it is
	// set to — required only for an [config.EditingCodec]. The value must read
	// back after the write.
	WriteKey   string
	WriteValue string
}

// Run executes the whole suite against s, one named subtest per contract, so a
// failing adapter sees exactly which one it breaks.
//
// Every codec is checked for the capability split, per-key merge with another
// format, tolerance of an absent source, and provenance naming the source. A
// read-only codec additionally has its layer confirmed skipped by write routing.
// An [config.EditingCodec] instead has its write round-trip, its no-edit no-op,
// and — the trap the seam exists to make unmissable — its refusal of a change
// that landed between load and write with [config.ErrConflict] checked, using
// WriteKey and WriteValue.
func Run(t *testing.T, s Suite) {
	t.Helper()

	if len(s.Defines) == 0 {
		t.Fatal("conformance: Suite.Defines needs at least one key the Sample exposes")
	}

	if len(s.Sample) == 0 {
		t.Fatal("conformance: Suite.Sample must be a valid source in the codec's format")
	}

	t.Run("capability_split", s.capabilitySplit)
	t.Run("decodes_and_merges", s.decodesAndMerges)
	t.Run("absent_source_tolerated", s.absentSourceTolerated)
	t.Run("provenance_names_source", s.provenanceNamesSource)

	if ec, ok := s.Codec.(config.EditingCodec); ok {
		if s.WriteKey == "" {
			t.Fatal("conformance: Suite.WriteKey and WriteValue are required for an EditingCodec")
		}

		t.Run("write_round_trips", s.writeRoundTrips)
		t.Run("no_edit_is_a_noop", func(t *testing.T) { s.noEditIsANoop(t, ec) })
		t.Run("conflict_detected", func(t *testing.T) { s.conflictDetected(t, ec) })

		return
	}

	t.Run("read_only_skipped_by_routing", s.readOnlySkippedByRouting)
}

// capabilitySplit is D5: the backend produced is a write target exactly when the
// codec can edit, so routing can trust the type system rather than a flag.
func (s Suite) capabilitySplit(t *testing.T) {
	backend := config.NewCodecBackend(config.OS(), "sample", s.Codec)

	_, editing := s.Codec.(config.EditingCodec)
	_, writable := backend.(config.WritableBackend)

	if editing != writable {
		t.Errorf("codec is EditingCodec=%v but backend is WritableBackend=%v; they must agree",
			editing, writable)
	}
}

// decodesAndMerges is D7: the codec's values merge per-key with a layer of
// another format, and precedence is the order the sources were added.
func (s Suite) decodesAndMerges(t *testing.T) {
	fsys := dir(t)
	shared := s.sortedKeys()[0]

	base := nestedYAML(shared, "from-base") + "conformance_base_only: present\n"
	write(t, fsys, "base.yaml", []byte(base))
	write(t, fsys, "sample", s.Sample)

	store := newStore(t,
		config.WithBackend(config.NewFileBackend(fsys, "base.yaml")),        // lower precedence
		config.WithBackend(config.NewCodecBackend(fsys, "sample", s.Codec)), // higher precedence
	)
	view := store.View()

	// The codec layer was added last, so it wins the shared key.
	if got := view.GetString(shared); got != s.Defines[shared] {
		t.Errorf("%s = %q, want %q from the codec layer", shared, got, s.Defines[shared])
	}

	// The base-only key survives the merge — merge is per-key, not layer-replace.
	if got := view.GetString("conformance_base_only"); got != "present" {
		t.Errorf("conformance_base_only = %q, want the base to survive per-key merge", got)
	}

	// Every declared key resolves to its value.
	for key, want := range s.Defines {
		if got := view.GetString(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// absentSourceTolerated confirms a missing codec source is tolerated for an
// optional layer, exactly as a missing overlay file is: the store builds and the
// layers beneath stand.
func (s Suite) absentSourceTolerated(t *testing.T) {
	fsys := dir(t)
	key := s.sortedKeys()[0]

	write(t, fsys, "base.yaml", []byte(nestedYAML(key, "from-base")))

	store := newStore(t,
		config.WithBackend(config.NewFileBackend(fsys, "base.yaml")),
		config.WithBackend(config.NewCodecBackend(fsys, "does-not-exist", s.Codec)),
	)

	if got := store.View().GetString(key); got != "from-base" {
		t.Errorf("%s = %q, want the base to stand when the codec source is absent", key, got)
	}
}

// provenanceNamesSource confirms the codec's layers carry the source's identity,
// so Origin can answer "which file supplied this".
func (s Suite) provenanceNamesSource(t *testing.T) {
	fsys := dir(t)
	key := s.sortedKeys()[0]

	write(t, fsys, "sample", s.Sample)

	store := newStore(t, config.WithBackend(config.NewCodecBackend(fsys, "sample", s.Codec)))

	src, ok := store.View().Origin(key)
	if !ok {
		t.Fatalf("Origin(%q) reported no source", key)
	}

	if src.Name != "sample" {
		t.Errorf("Origin(%q).Name = %q, want %q", key, src.Name, "sample")
	}
}

// readOnlySkippedByRouting is D10/D18: a write to a key a read-only layer defines
// lands in the writable layer beneath and is reported shadowed, not applied to
// the read-only source and not failed.
func (s Suite) readOnlySkippedByRouting(t *testing.T) {
	fsys := dir(t)
	key := s.sortedKeys()[0]

	write(t, fsys, "base.yaml", []byte(nestedYAML(key, "from-base")))
	write(t, fsys, "sample", s.Sample)

	store := newStore(t,
		config.WithBackend(config.NewFileBackend(fsys, "base.yaml")),
		config.WithBackend(config.NewCodecBackend(fsys, "sample", s.Codec)),
	)

	plan, err := store.Plan(config.Set(key, "new"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	targets := plan.Targets()
	if len(targets) != 1 || targets[0].Name != "base.yaml" {
		t.Errorf("targets = %v, want the write routed to base.yaml (read-only codec skipped)", targets)
	}

	if plan.Effective() {
		t.Error("a write to a key the read-only layer defines should be shadowed, not effective")
	}
}

// writeRoundTrips confirms an editing codec receives writes end to end: the value
// is served, and a fresh read of the file gets it back.
func (s Suite) writeRoundTrips(t *testing.T) {
	fsys := dir(t)
	write(t, fsys, "sample", s.Sample)

	store := newStore(t, config.WithBackend(config.NewCodecBackend(fsys, "sample", s.Codec)))

	if _, err := store.Apply(context.Background(), config.Set(s.WriteKey, s.WriteValue)); err != nil {
		t.Fatalf("Apply(Set(%q, %q)): %v", s.WriteKey, s.WriteValue, err)
	}

	if got := store.View().GetString(s.WriteKey); got != s.WriteValue {
		t.Errorf("%s = %q after write, want %q", s.WriteKey, got, s.WriteValue)
	}

	// A fresh store over the same file must read the written value, or the write
	// did not reach disk through the codec.
	fresh := newStore(t, config.WithBackend(config.NewCodecBackend(fsys, "sample", s.Codec)))
	if got := fresh.View().GetString(s.WriteKey); got != s.WriteValue {
		t.Errorf("a fresh read of the file has %s = %q, want %q — the write did not reach disk",
			s.WriteKey, got, s.WriteValue)
	}
}

// noEditIsANoop confirms Apply with no edits does not change the values the
// source exposes. Byte-identity is a per-format fidelity claim; this is the
// value-level no-op every editing codec must honour.
func (s Suite) noEditIsANoop(t *testing.T, ec config.EditingCodec) {
	out, err := ec.Apply("sample", s.Sample, nil)
	if err != nil {
		t.Fatalf("Apply with no edits: %v", err)
	}

	fsys := dir(t)
	write(t, fsys, "after", out)

	store := newStore(t, config.WithBackend(config.NewCodecBackend(fsys, "after", s.Codec)))

	for key, want := range s.Defines {
		if got := store.View().GetString(key); got != want {
			t.Errorf("after a no-edit Apply, %s = %q, want %q — the round-trip changed a value",
				key, got, want)
		}
	}
}

// conflictDetected is the D3 trap, asserted: a change landing between load and
// write is refused with ErrConflict, and the rejected write leaves the source
// byte-identical to the foreign content.
func (s Suite) conflictDetected(t *testing.T, ec config.EditingCodec) {
	fsys := dir(t)
	write(t, fsys, "sample", s.Sample)

	store := newStore(t, config.WithBackend(config.NewCodecBackend(fsys, "sample", s.Codec)))

	// A foreign change: valid, different content produced by the codec itself,
	// so it is guaranteed to be a source the codec accepts.
	foreign, err := ec.Apply("sample", s.Sample, []config.Edit{{Path: s.WriteKey, Value: s.WriteValue}})
	if err != nil {
		t.Fatalf("producing foreign content via Apply: %v", err)
	}

	if bytes.Equal(foreign, s.Sample) {
		t.Skipf("setting %q does not change the sample, so a foreign change cannot be simulated", s.WriteKey)
	}

	write(t, fsys, "sample", foreign)

	_, err = store.Apply(context.Background(), config.Set(s.WriteKey, "something-else"))
	if !errors.Is(err, config.ErrConflict) {
		t.Errorf("Apply after a foreign change = %v, want ErrConflict — the fingerprint must be "+
			"taken at load, not at write", err)
	}

	// The rejected write must have left the foreign content untouched.
	got, err := fsys.ReadFile("sample")
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, foreign) {
		t.Error("a rejected write changed the source; it must leave it byte-identical")
	}
}

// --- helpers -------------------------------------------------------------

func (s Suite) sortedKeys() []string {
	keys := make([]string, 0, len(s.Defines))
	for key := range s.Defines {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

// nestedYAML renders a dotted key and a scalar value as block-style YAML, so a
// suite-controlled base can define the same key path the codec's sample does.
func nestedYAML(key, value string) string {
	var b strings.Builder

	segs := strings.Split(key, ".")
	for i, seg := range segs {
		b.WriteString(strings.Repeat("  ", i))
		b.WriteString(seg)

		if i == len(segs)-1 {
			b.WriteString(": ")
			b.WriteString(value)
			b.WriteString("\n")

			break
		}

		b.WriteString(":\n")
	}

	return b.String()
}

func dir(t *testing.T) config.FS {
	t.Helper()

	fsys, err := config.Dir(t.TempDir())
	if err != nil {
		t.Fatalf("config.Dir: %v", err)
	}

	return fsys
}

func newStore(t *testing.T, opts ...config.StoreOption) *config.Store {
	t.Helper()

	store, err := config.NewStore(context.Background(), opts...)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	return store
}

// fileMode is what the suite writes sample sources with — owner-only, matching
// the core's own configuration file mode.
const fileMode = 0o600

func write(t *testing.T, fsys config.FS, name string, data []byte) {
	t.Helper()

	if err := fsys.WriteFile(name, data, fileMode); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}
