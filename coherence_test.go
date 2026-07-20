package config_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/phpboyscout/go/config"
)

// TestReadsNeverStraddleAReload hammers two values that are always written
// together while the file changes underneath, and asserts no read ever sees a
// mismatched pair.
//
// This is the guarantee a View exists to provide, and it is the read-side claim
// the documentation makes, so it is measured rather than reasoned about. A
// library that reads from live mutable state fails this: a sequence of reads
// lands either side of a reload and returns a configuration that never existed.
func TestReadsNeverStraddleAReload(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "coherence.yaml")

	// Atomic: write a temp file and rename over the target, so a reader can
	// never observe a half-written file. Without this the test would measure
	// the writer rather than the module.
	write := func(gen int) {
		body := fmt.Sprintf("server:\n  host: host-%d\n  port: %d\n", gen, gen)
		tmp := path + ".tmp"

		if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
			panic(err)
		}

		if err := os.Rename(tmp, path); err != nil {
			panic(err)
		}
	}

	write(0)

	store, err := config.NewStore(context.Background(),
		config.WithFiles(config.OS(), path))
	if err != nil {
		t.Fatal(err)
	}

	stopWatch, err := store.Watch(context.Background(),
		config.WithPollInterval(2*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}

	defer stopWatch()

	var (
		stop atomic.Bool
		wg   sync.WaitGroup
	)

	wg.Add(1)

	go func() {
		defer wg.Done()

		for gen := 1; !stop.Load(); gen++ {
			write(gen)
			time.Sleep(time.Millisecond)
		}
	}()

	var reads, torn int

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		// One view, then two reads through it — the shape real code has.
		view := store.View()

		host := view.GetString("server.host")
		port := view.GetInt("server.port")

		reads++

		if host != fmt.Sprintf("host-%d", port) {
			torn++
		}
	}

	stop.Store(true)
	wg.Wait()

	if torn != 0 {
		t.Errorf("%d of %d reads straddled a reload and saw a configuration "+
			"that never existed", torn, reads)
	}

	if reads < 1000 {
		t.Errorf("only %d reads: the test is not exercising the race it claims to", reads)
	}
}
