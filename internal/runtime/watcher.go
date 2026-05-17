package runtime

import (
	"context"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/felixgeelhaar/scry/internal/auth"
	"github.com/felixgeelhaar/scry/internal/obs"
)

// debounceWindow batches rapid editor save events (vim, vscode, sed
// all emit multiple writes per save). Reapply at most once per
// window so we don't tear down + re-introspect twice in a row.
const debounceWindow = 500 * time.Millisecond

// WatchServers tails the servers.yml path and calls mgr.Replace
// whenever it changes. Blocks until ctx cancels. Errors during
// reload are logged but never propagated — the watcher should never
// kill the process. A failed reload keeps the previous state
// active.
//
// Watches the *directory* (not the file) because most editors
// replace-on-save: the original inode is deleted + a new file
// rename'd in, and a file-level fsnotify watch loses its target.
// Filter on the leaf name inside the loop instead.
func WatchServers(ctx context.Context, path string, mgr *Manager) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer func() { _ = w.Close() }()

	dir := filepath.Dir(path)
	leaf := filepath.Base(path)
	if err := w.Add(dir); err != nil {
		return err
	}
	obs.L.Info().
		Str("event", "watcher.started").
		Str("path", path).
		Msg("watching servers.yml for changes")

	var timer *time.Timer
	for {
		select {
		case <-ctx.Done():
			obs.L.Info().Str("event", "watcher.stopped").Msg("watcher stopped")
			return nil
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			obs.L.Warn().Str("event", "watcher.error").Err(err).Msg("watcher error (continuing)")
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if filepath.Base(ev.Name) != leaf {
				continue
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			// Debounce: reset the timer on every event;
			// applyOnce fires only after debounceWindow passes
			// with no further events.
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(debounceWindow, func() {
				applyOnce(ctx, path, mgr)
			})
		}
	}
}

// applyOnce reloads servers.yml and applies it to the Manager. Any
// errors are logged + swallowed — the running process keeps its
// previous configuration alive on bad input.
func applyOnce(ctx context.Context, path string, mgr *Manager) {
	s, err := auth.Load(path)
	if err != nil {
		obs.L.Error().
			Str("event", "watcher.reload_failed").
			Str("path", path).
			Err(err).
			Msg("could not reload servers.yml; keeping previous state")
		return
	}
	if err := mgr.Replace(ctx, s); err != nil {
		obs.L.Warn().
			Str("event", "watcher.reload_partial").
			Err(err).
			Msg("hot-reload completed with some errors; partial state applied")
		return
	}
	obs.L.Info().
		Str("event", "watcher.reload_ok").
		Int("servers", len(mgr.List())).
		Strs("server_names", mgr.List()).
		Msg("hot-reload applied")
}
