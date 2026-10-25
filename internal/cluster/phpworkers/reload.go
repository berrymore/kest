package phpworkers

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"log/slog"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/berrymore/kest/internal/cluster"
)

// watcher resets the workers when the watched files change (reload, K-072):
// it scans the directories every interval, by name, size and modification
// time, and resets once a change has held still for one scan, so that a
// deploy or an editor's save of many files makes one reset. A rewrite that
// keeps a file's size within one tick of the file system's clock (a few
// milliseconds on Linux) after a scan looks like no change.
type watcher struct {
	dirs     []string // absolute
	patterns []string
	interval time.Duration
	maxFiles int
	reset    func(context.Context) error
	logger   *slog.Logger
}

func newWatcher(cfg Reload, configDir string, reset func(context.Context) error, logger *slog.Logger) *watcher {
	if len(cfg.Dirs) == 0 {
		return nil
	}
	w := &watcher{patterns: cfg.Patterns, interval: cfg.Interval.Std(), maxFiles: cfg.MaxFiles, reset: reset,
		logger: logger.With("component", "reload")}
	for _, d := range cfg.Dirs {
		w.dirs = append(w.dirs, resolve(configDir, d))
	}
	return w
}

// errTooManyFiles stops the watcher: a scan found more than max_files.
var errTooManyFiles = errors.New("too many files to watch")

// baseline scans the files as they are when the workers have started; run
// compares with it. It reports false, with the error logged, if watching
// can't start.
func (w *watcher) baseline() (uint64, bool) {
	sum, err := w.scan()
	if err != nil {
		w.logger.Error("not watching files", "err", err)
		return 0, false
	}
	w.logger.Info("watching files for changes", "dirs", w.dirs, "patterns", w.patterns)
	return sum, true
}

// run watches until ctx ends, starting from the baseline last.
func (w *watcher) run(ctx context.Context, last uint64) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	pending := false // a change seen, not reset yet
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		sum, err := w.scan()
		if err != nil {
			w.logger.Error("stopped watching files", "err", err)
			return
		}
		switch {
		case sum != last:
			last, pending = sum, true // wait until the files hold still
		case pending:
			w.logger.Info("files changed, resetting the workers")
			err := w.reset(ctx)
			switch {
			case err == nil:
				pending = false
			case errors.Is(err, cluster.ErrResetRunning):
				// Try again at the next scan.
			case ctx.Err() != nil:
				return
			default:
				pending = false // the next change tries again
				w.logger.Error("reset after a file change failed", "err", err)
			}
		}
	}
}

// scan returns a hash of the names, sizes and modification times of the
// watched files. Unreadable entries count as changes, not errors: they come
// and go while files are being written.
func (w *watcher) scan() (uint64, error) {
	h := fnv.New64a()
	files := 0
	for _, dir := range w.dirs {
		err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				fmt.Fprintf(h, "%s\x00error\x00", p)
				if e != nil && e.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if e.IsDir() {
				if p != dir && strings.HasPrefix(e.Name(), ".") {
					return fs.SkipDir
				}
				return nil
			}
			if !w.matches(e.Name()) {
				return nil
			}
			files++
			if files > w.maxFiles {
				return fmt.Errorf("%w: more than max_files (%d) under %s", errTooManyFiles, w.maxFiles, dir)
			}
			if info, err := e.Info(); err == nil {
				fmt.Fprintf(h, "%s\x00%d\x00%d\x00", p, info.Size(), info.ModTime().UnixNano())
			} else {
				fmt.Fprintf(h, "%s\x00gone\x00", p) // removed during the scan
			}
			return nil
		})
		if err != nil {
			return 0, err
		}
	}
	return h.Sum64(), nil
}

func (w *watcher) matches(name string) bool {
	for _, p := range w.patterns {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}
