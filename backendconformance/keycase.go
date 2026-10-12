package backendconformance

import (
	"context"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/go/config"
)

// runMixedCase is spec 0015 D11: a write keeps the spelling the backend
// already uses for an existing key, in either mode.
func (s Suite) runMixedCase(t *testing.T) {
	t.Helper()

	parent, _, ok := strings.Cut(s.MixedCaseKey, ".")
	if !ok || strings.ToLower(s.MixedCaseKey) == s.MixedCaseKey {
		t.Fatal("backendconformance: Suite.MixedCaseKey must be a mixed-case leaf beneath a parent, such as \"Server.Port\"")
	}

	if s.CaseSensitive {
		t.Run("mixed_case_key_is_exact", s.mixedCaseKeyIsExact)

		return
	}

	t.Run("existing_key_written_in_another_case", s.existingKeyWrittenInAnotherCase)

	if !s.BoundedKeySpace {
		t.Run("new_key_under_a_mixed_case_parent", func(t *testing.T) { s.newKeyUnderAMixedCaseParent(t, parent) })
	}
}

func (s Suite) mixedCaseStore(t *testing.T) (*config.Store, Control) {
	t.Helper()

	backend, ctrl := s.newBackend(t, s.MixedCaseSeed)

	return s.newStore(t, config.WithBackend(backend)), ctrl
}

// exactlyAsWritten re-reads the backing store in a case-sensitive Store, which
// is the only view that can tell one spelling from another.
func exactlyAsWritten(t *testing.T, ctrl Control) *config.View {
	t.Helper()

	return newStoreIn(t, true, config.WithBackend(ctrl.Reopen(t))).View()
}

func (s Suite) apply(t *testing.T, store *config.Store, key, value string) {
	t.Helper()

	if _, err := store.Apply(context.Background(), s.writeChange(t, store, key, value)); err != nil {
		t.Fatalf("Apply(Set(%q)): %v", key, err)
	}
}

func (s Suite) existingKeyWrittenInAnotherCase(t *testing.T) {
	store, ctrl := s.mixedCaseStore(t)
	other := strings.ToLower(s.MixedCaseKey)

	s.apply(t, store, other, "edited")

	written := exactlyAsWritten(t, ctrl)

	if got := written.GetString(s.MixedCaseKey); got != "edited" {
		t.Errorf("%s = %q after writing %s, want the existing key edited", s.MixedCaseKey, got, other)
	}

	if written.Has(other) {
		t.Errorf("writing %s added it beside %s; the existing key must be edited in its own spelling",
			other, s.MixedCaseKey)
	}
}

func (s Suite) newKeyUnderAMixedCaseParent(t *testing.T, parent string) {
	store, ctrl := s.mixedCaseStore(t)
	added := strings.ToLower(parent) + ".conformance_new"

	s.apply(t, store, added, "added")

	if got := exactlyAsWritten(t, ctrl).GetString(parent + ".conformance_new"); got != "added" {
		t.Errorf("%s.conformance_new = %q after writing %s, want it under the parent as the backend spells it",
			parent, got, added)
	}
}

func (s Suite) mixedCaseKeyIsExact(t *testing.T) {
	store, ctrl := s.mixedCaseStore(t)
	other := strings.ToLower(s.MixedCaseKey)

	if store.View().Has(other) {
		t.Errorf("%s resolves in a case-sensitive Store that defines only %s", other, s.MixedCaseKey)
	}

	s.apply(t, store, s.MixedCaseKey, "edited")

	written := exactlyAsWritten(t, ctrl)

	if got := written.GetString(s.MixedCaseKey); got != "edited" {
		t.Errorf("%s = %q after the write, want %q", s.MixedCaseKey, got, "edited")
	}

	if written.Has(other) {
		t.Errorf("writing %s also defined %s", s.MixedCaseKey, other)
	}
}
