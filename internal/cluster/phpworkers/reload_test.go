package phpworkers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/berrymore/kest/internal/cluster"
	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/logging"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// watch runs a watcher on dir (1s interval, the default) until the test ends;
// call it in a synctest bubble.
func watch(t *testing.T, dir string, maxFiles int, reset func(context.Context) error) {
	t.Helper()
	cfg := DefaultConfig().Reload
	cfg.Dirs, cfg.MaxFiles = []string{dir}, maxFiles
	w := newWatcher(cfg, "", reset, logging.Discard())
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func TestWatcher(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "src", "a.php"), "<?php // 1")
		var resets atomic.Int32
		watch(t, dir, 100, func(context.Context) error { resets.Add(1); return nil })
		synctest.Wait()

		step := func(what string, want int32) {
			t.Helper()
			time.Sleep(time.Second)
			synctest.Wait()
			if got := resets.Load(); got != want {
				t.Fatalf("%s: %d resets, want %d", what, got, want)
			}
		}
		step("nothing changed", 0)

		writeFile(t, filepath.Join(dir, "src", "a.php"), "<?php // two")
		step("a change, seen once", 0)
		step("the change held still", 1)

		writeFile(t, filepath.Join(dir, "notes.txt"), "not watched")
		writeFile(t, filepath.Join(dir, ".git", "x.php"), "hidden directory")
		step("files that don't count", 1)
		step("still nothing", 1)

		// A burst over several scans makes one reset, after it ends.
		for i := range 3 {
			writeFile(t, filepath.Join(dir, "src", "b.php"), "<?php // "+string(rune('a'+i)))
			step("during the burst", 1)
		}
		step("after the burst", 2)

		if err := os.Remove(filepath.Join(dir, "src", "b.php")); err != nil {
			t.Fatal(err)
		}
		step("a removed file, seen once", 2)
		step("a removed file", 3)
	})
}

func TestWatcherRetriesARunningReset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "a.php"), "1")
		var calls atomic.Int32
		watch(t, dir, 100, func(context.Context) error {
			if calls.Add(1) == 1 {
				return cluster.ErrResetRunning
			}
			return nil
		})
		synctest.Wait()
		writeFile(t, filepath.Join(dir, "a.php"), "22")
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if got := calls.Load(); got != 2 {
			t.Errorf("%d reset calls, want 2: one refused while another ran, then one done", got)
		}
	})
}

func TestWatcherMaxFiles(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range []string{"a.php", "b.php", "c.php"} {
			writeFile(t, filepath.Join(dir, name), name)
		}
		var resets atomic.Int32
		watch(t, dir, 2, func(context.Context) error { resets.Add(1); return nil })
		synctest.Wait() // the watcher gave up on its first scan
		writeFile(t, filepath.Join(dir, "a.php"), "changed")
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if resets.Load() != 0 {
			t.Error("a watcher over max_files reset the workers")
		}
	})
}

func TestReloadConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Command = []string{"php"}
	cfg.Reload = Reload{Dirs: []string{"src"}, Patterns: []string{"[", ""}, Interval: config.Duration(time.Millisecond)}
	err := cfg.Validate()
	for _, want := range []string{`reload.patterns[0]: invalid pattern "["`, `reload.patterns[1]: invalid pattern ""`,
		"reload.interval: must be at least 10ms, got 1ms", "reload.max_files: must be positive, got 0"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Validate = %v, want %s", err, want)
		}
	}
	if w := newWatcher(DefaultConfig().Reload, "/srv", nil, logging.Discard()); w != nil {
		t.Error("a watcher without reload.dirs")
	}
	cfg.Reload = DefaultConfig().Reload
	cfg.Reload.Dirs = []string{"src", "/abs"}
	if w := newWatcher(cfg.Reload, "/srv/app", nil, logging.Discard()); w.dirs[0] != "/srv/app/src" || w.dirs[1] != "/abs" {
		t.Errorf("dirs = %v, want them relative to the config file", w.dirs)
	}
}
