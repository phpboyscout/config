package config_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/go/config"
)

// The codecs below are a deliberately-not-real format — one document of
// `key=value` lines — so the core's own suite exercises the codec seam without
// acquiring a parser dependency. If the seam extracted the right thing, a
// consumer's codec built only against the public API reads, routes, writes and
// detects conflicts exactly as the built-in YAML one does.

// lineCodec is the read-only half: it can decode, and nothing else.
type lineCodec struct{}

func (lineCodec) Decode(path string, src []byte) ([]map[string]any, error) {
	values := map[string]any{}

	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%w: %s: line %q has no '='", config.ErrBackendParse, path, line)
		}

		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}

	return []map[string]any{values}, nil
}

// editableLineCodec adds the editing methods, and nothing about how it is
// constructed changes — which is the writer-shaped-hole shape a real adapter
// grows into.
type editableLineCodec struct{ lineCodec }

func (editableLineCodec) Check(string, []byte) error { return nil }

func (editableLineCodec) Empty() []byte { return nil }

// Apply re-emits the file with the edits folded in, keeping existing keys in
// their original order and appending new ones — enough structure preservation
// to prove the write path is wired, not a real editor.
func (editableLineCodec) Apply(_ string, src []byte, edits []config.Edit) ([]byte, error) {
	order := []string{}
	values := map[string]string{}

	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		key, value, _ := strings.Cut(line, "=")
		key = strings.TrimSpace(key)

		if _, seen := values[key]; !seen {
			order = append(order, key)
		}

		values[key] = strings.TrimSpace(value)
	}

	for _, edit := range edits {
		if edit.Remove {
			delete(values, edit.Path)

			continue
		}

		if _, seen := values[edit.Path]; !seen {
			order = append(order, edit.Path)
		}

		values[edit.Path] = fmt.Sprint(edit.Value)
	}

	var b strings.Builder

	for _, key := range order {
		value, ok := values[key]
		if !ok {
			continue
		}

		fmt.Fprintf(&b, "%s=%s\n", key, value)
	}

	return []byte(b.String()), nil
}

// TestCodecSeam_CapabilitySplit is D5: the type produced reflects the codec's
// capability, so routing and Plan can trust the type system rather than a flag.
func TestCodecSeam_CapabilitySplit(t *testing.T) {
	t.Parallel()

	readOnly := config.NewCodecBackend(config.OS(), "/app.conf", lineCodec{})
	if _, ok := readOnly.(config.WritableBackend); ok {
		t.Error("a read-only codec must not produce a writable backend")
	}

	editing := config.NewCodecBackend(config.OS(), "/app.conf", editableLineCodec{})
	if _, ok := editing.(config.WritableBackend); !ok {
		t.Error("an editing codec must produce a writable backend")
	}
}

// readOverConf builds a Store with a read-only conf layer over a writable YAML
// base, and returns the Store and the two paths.
func readOverConf(t *testing.T) (store *config.Store, base, conf string) {
	t.Helper()

	dir := t.TempDir()
	base = dir + "/base.yaml"
	conf = dir + "/app.conf"

	if err := config.OS().WriteFile(base, []byte("shared: fromyaml\nonlyyaml: y\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := config.OS().WriteFile(conf, []byte("shared=fromconf\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := config.NewStore(context.Background(),
		config.WithBackend(config.NewFileBackend(config.OS(), base)),               // writable, lower precedence
		config.WithBackend(config.NewCodecBackend(config.OS(), conf, lineCodec{})), // read-only, higher precedence
	)
	if err != nil {
		t.Fatal(err)
	}

	return store, base, conf
}

// TestCodecSeam_ParticipatesAsAReadLayer proves a non-YAML read-only layer takes
// part in precedence, merge and provenance like any other layer.
func TestCodecSeam_ParticipatesAsAReadLayer(t *testing.T) {
	t.Parallel()

	store, _, conf := readOverConf(t)
	view := store.View()

	// Precedence: the read-only layer was added last, so it wins.
	if got := view.GetString("shared"); got != "fromconf" {
		t.Errorf("shared = %q, want \"fromconf\" from the read-only layer", got)
	}

	// Merge is per-key, so the YAML layer still supplies what the conf omits.
	if got := view.GetString("onlyyaml"); got != "y" {
		t.Errorf("onlyyaml = %q, want the YAML layer to survive the merge", got)
	}

	// Provenance names the conf source.
	if src, ok := view.Origin("shared"); !ok || src.Name != conf {
		t.Errorf("origin = %q (ok=%v), want the conf path %q", src.Name, ok, conf)
	}
}

// TestCodecSeam_ReadOnlyIsSkippedByRouting proves a write to a key the read-only
// layer defines lands in the writable layer beneath and is reported shadowed
// rather than failing (D10, D18).
func TestCodecSeam_ReadOnlyIsSkippedByRouting(t *testing.T) {
	t.Parallel()

	store, base, _ := readOverConf(t)

	plan, err := store.Plan(config.Set("shared", "new"))
	if err != nil {
		t.Fatal(err)
	}

	targets := plan.Targets()
	if len(targets) != 1 || targets[0].Name != base {
		t.Errorf("targets = %v, want the write routed to %q", targets, base)
	}

	if plan.Effective() {
		t.Error("the write should be reported shadowed by the read-only conf layer, not effective")
	}
}

// TestCodecSeam_EditingCodecReceivesWrites proves the extracted write machinery
// drives a non-YAML editing codec end to end: the value is served and reaches
// disk through the codec's Apply.
func TestCodecSeam_EditingCodecReceivesWrites(t *testing.T) {
	t.Parallel()

	conf := t.TempDir() + "/app.conf"
	if err := config.OS().WriteFile(conf, []byte("level=info\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := config.NewStore(context.Background(),
		config.WithBackend(config.NewCodecBackend(config.OS(), conf, editableLineCodec{})))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.Apply(context.Background(), config.Set("level", "debug")); err != nil {
		t.Fatal(err)
	}

	if got := store.View().GetString("level"); got != "debug" {
		t.Errorf("view = %q, want \"debug\"", got)
	}

	data, err := config.OS().ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.TrimSpace(string(data)); got != "level=debug" {
		t.Errorf("file = %q, want the codec to have written \"level=debug\"", got)
	}
}

// TestCodecSeam_DetectsConcurrentChange is the D3 trap the seam exists to make
// once: a change landing between Load and the write is refused with ErrConflict,
// and the source is left untouched — for any editing codec, not just YAML.
func TestCodecSeam_DetectsConcurrentChange(t *testing.T) {
	t.Parallel()

	conf := t.TempDir() + "/app.conf"
	if err := config.OS().WriteFile(conf, []byte("level=info\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := config.NewStore(context.Background(),
		config.WithBackend(config.NewCodecBackend(config.OS(), conf, editableLineCodec{})))
	if err != nil {
		t.Fatal(err)
	}

	// Something else changes the file after the Store read it.
	if err := config.OS().WriteFile(conf, []byte("level=info\nother=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = store.Apply(context.Background(), config.Set("level", "debug"))
	if !errors.Is(err, config.ErrConflict) {
		t.Errorf("err = %v, want ErrConflict — a stale write was accepted", err)
	}

	// And nothing was written over the foreign change.
	data, err := config.OS().ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(data), "other=x") {
		t.Errorf("file = %q, want the foreign change \"other=x\" intact", string(data))
	}
}
