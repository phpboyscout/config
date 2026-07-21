package conformance

import (
	"fmt"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/go/config"
)

// The codecs below are a trivial flat key=value format, correct by construction,
// used only to exercise the suite itself: every subtest must run and pass for a
// codec that behaves. A real format's conformance run is the adapter's own test.

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

type editableLineCodec struct{ lineCodec }

func (editableLineCodec) Check(string, []byte) error { return nil }

func (editableLineCodec) Empty() []byte { return nil }

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

func TestRun_EditingCodec(t *testing.T) {
	t.Parallel()

	Run(t, Suite{
		Codec:      editableLineCodec{},
		Sample:     []byte("level=info\nregion=eu\n"),
		Defines:    map[string]string{"level": "info", "region": "eu"},
		WriteKey:   "host",
		WriteValue: "localhost",
	})
}

func TestRun_ReadOnlyCodec(t *testing.T) {
	t.Parallel()

	Run(t, Suite{
		Codec:   lineCodec{},
		Sample:  []byte("level=info\nregion=eu\n"),
		Defines: map[string]string{"level": "info", "region": "eu"},
	})
}
