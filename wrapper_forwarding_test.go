package config

import (
	"testing"
	"time"
)

// The optional interfaces the Store asks a backend about, checked through each
// decorator. Every one behaves the same absent as delegated to nothing, so a
// wrapper implements them all and forwards. go/config#14.

var decorators = map[string]func(Backend) Backend{
	"Filtered":    func(b Backend) Backend { return Filtered(b, Deny("unrelated.*")) },
	"Constrained": func(b Backend) Backend { return Constrained(b, Forbid("unrelated.*")) },
}

func TestDecorators_ForwardWatchReporting(t *testing.T) {
	t.Parallel()

	for name, wrap := range decorators {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			file := NewFileBackend(NewMemFS(), "/app.yaml").(*editingBackend)
			wrapped := wrap(file)

			reporter, ok := wrapped.(WatchErrorReporter)
			if !ok {
				t.Fatal("the wrapper hides WatchErrorReporter")
			}

			reporter.SetWatchErrorHandler(func(error) {})

			if file.onWatchError == nil {
				t.Error("SetWatchErrorHandler did not reach the wrapped backend")
			}

			paths, ok := wrapped.(WatchPathReporter)
			if !ok {
				t.Fatal("the wrapper hides WatchPathReporter")
			}

			gotPath, gotOK := paths.WatchPath()
			wantPath, wantOK := file.WatchPath()

			if gotPath != wantPath || gotOK != wantOK {
				t.Errorf("WatchPath = (%q, %v), want (%q, %v)", gotPath, gotOK, wantPath, wantOK)
			}
		})
	}
}

func TestDecorators_ForwardKindAndPollHint(t *testing.T) {
	t.Parallel()

	for name, wrap := range decorators {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if d, ok := wrap(stubKinded{}).(SourceKindDeclarer); !ok || d.SourceKind() != SourceKind("remote") {
				t.Error("the wrapper does not forward SourceKind")
			}

			if h, ok := wrap(stubHinter{}).(PollIntervalHinter); !ok || h.PollInterval() != time.Minute {
				t.Error("the wrapper does not forward PollInterval")
			}
		})
	}
}
