package conformance

import (
	"context"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/go/config"
)

// runMixedCase is spec 0015 D11: a write keeps the spelling the source already
// uses for an existing key, in either mode.
func (s Suite) runMixedCase(t *testing.T) {
	t.Helper()

	parent, _, ok := strings.Cut(s.MixedCaseKey, ".")
	if !ok || strings.ToLower(s.MixedCaseKey) == s.MixedCaseKey {
		t.Fatal("conformance: Suite.MixedCaseKey must be a mixed-case leaf beneath a parent, such as \"Server.Port\"")
	}

	if s.CaseSensitive {
		t.Run("mixed_case_key_is_exact", s.mixedCaseKeyIsExact)

		return
	}

	t.Run("existing_key_written_in_another_case", s.existingKeyWrittenInAnotherCase)
	t.Run("new_key_under_a_mixed_case_parent", func(t *testing.T) { s.newKeyUnderAMixedCaseParent(t, parent) })
}

func (s Suite) mixedCaseStore(t *testing.T) (config.FS, *config.Store) {
	t.Helper()

	fsys := dir(t)
	write(t, fsys, "sample", s.MixedCaseSample)

	return fsys, s.newStore(t, config.WithBackend(config.NewCodecBackend(fsys, "sample", s.Codec)))
}

// exactlyAsWritten re-reads the file in a case-sensitive Store, which is the
// only view that can tell one spelling from another.
func (s Suite) exactlyAsWritten(t *testing.T, fsys config.FS) *config.View {
	t.Helper()

	return newStoreIn(t, true, config.WithBackend(config.NewCodecBackend(fsys, "sample", s.Codec))).View()
}

func (s Suite) existingKeyWrittenInAnotherCase(t *testing.T) {
	fsys, store := s.mixedCaseStore(t)
	other := strings.ToLower(s.MixedCaseKey)

	if _, err := store.Apply(context.Background(), config.Set(other, "edited")); err != nil {
		t.Fatalf("Apply(Set(%q)): %v", other, err)
	}

	written := s.exactlyAsWritten(t, fsys)

	if got := written.GetString(s.MixedCaseKey); got != "edited" {
		t.Errorf("%s = %q after writing %s, want the existing key edited", s.MixedCaseKey, got, other)
	}

	if written.Has(other) {
		t.Errorf("writing %s added it beside %s; the existing key must be edited in its own spelling",
			other, s.MixedCaseKey)
	}
}

func (s Suite) newKeyUnderAMixedCaseParent(t *testing.T, parent string) {
	fsys, store := s.mixedCaseStore(t)
	added := strings.ToLower(parent) + ".conformance_new"

	if _, err := store.Apply(context.Background(), config.Set(added, "added")); err != nil {
		t.Fatalf("Apply(Set(%q)): %v", added, err)
	}

	if got := s.exactlyAsWritten(t, fsys).GetString(parent + ".conformance_new"); got != "added" {
		t.Errorf("%s.conformance_new = %q after writing %s, want it under the parent as the source spells it",
			parent, got, added)
	}
}

func (s Suite) mixedCaseKeyIsExact(t *testing.T) {
	fsys, store := s.mixedCaseStore(t)
	other := strings.ToLower(s.MixedCaseKey)

	if store.View().Has(other) {
		t.Errorf("%s resolves in a case-sensitive Store that defines only %s", other, s.MixedCaseKey)
	}

	if _, err := store.Apply(context.Background(), config.Set(s.MixedCaseKey, "edited")); err != nil {
		t.Fatalf("Apply(Set(%q)): %v", s.MixedCaseKey, err)
	}

	written := s.exactlyAsWritten(t, fsys)

	if got := written.GetString(s.MixedCaseKey); got != "edited" {
		t.Errorf("%s = %q after the write, want %q", s.MixedCaseKey, got, "edited")
	}

	if written.Has(other) {
		t.Errorf("writing %s also defined %s", s.MixedCaseKey, other)
	}
}
