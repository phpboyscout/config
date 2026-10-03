package config

import (
	"context"
	"testing"

	"gitlab.com/phpboyscout/go/errors"
)

// A load or write that passed the lock-free check before Close, and only then
// took the lock, must still refuse. The race between the two cannot be forced
// through the public API, so the guard is driven directly. Spec 0013 D3.
func TestLockedPathsRecheckClosed(t *testing.T) {
	t.Parallel()

	s, err := NewStore(context.Background(), WithReaders(NamedSource{Name: "defaults", Content: []byte("a: 1\n")}))
	if err != nil {
		t.Fatal(err)
	}

	s.life.closed.Store(true)

	if _, _, err := s.reload(context.Background()); !errors.Is(err, ErrStoreClosed) {
		t.Errorf("reload under the lock: want ErrStoreClosed, got %v", err)
	}

	if _, _, err := s.apply(context.Background(), []Change{Set("a", 2)}); !errors.Is(err, ErrStoreClosed) {
		t.Errorf("apply under the lock: want ErrStoreClosed, got %v", err)
	}
}
