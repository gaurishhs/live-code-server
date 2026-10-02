package watcher

import (
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/gaurishhs/live-code-server/internal/filesystem"
	"github.com/gaurishhs/live-code-server/internal/server"
)

type queued struct {
	event string
	at    time.Time
}
type Watcher struct {
	w        *fsnotify.Watcher
	root     *filesystem.Root
	hub      *server.Hub
	debounce time.Duration
	logger   *log.Logger
}

func New(root *filesystem.Root, hub *server.Hub, debounce time.Duration, loggers ...*log.Logger) (*Watcher, error) {
	w, e := fsnotify.NewWatcher()
	if e != nil {
		return nil, e
	}
	x := &Watcher{w: w, root: root, hub: hub, debounce: debounce}
	if len(loggers) > 0 {
		x.logger = loggers[0]
	}
	if e = x.addTree(root.Path); e != nil {
		w.Close()
		return nil, e
	}
	return x, nil
}
func (x *Watcher) addTree(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			return nil
		}
		if p != x.root.Path {
			rel, _ := filepath.Rel(x.root.Path, p)
			if x.root.IsIgnored(rel) {
				return filepath.SkipDir
			}
		}
		return x.w.Add(p)
	})
}
func (x *Watcher) Close() error { return x.w.Close() }
func (x *Watcher) Run(done <-chan struct{}) {
	pending := map[string]queued{}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case ev, ok := <-x.w.Events:
			if !ok {
				return
			}
			rel, e := filepath.Rel(x.root.Path, ev.Name)
			if e != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || x.root.IsIgnored(rel) {
				continue
			}
			kind := ""
			switch {
			case ev.Op&fsnotify.Create != 0:
				kind = "created"
			case ev.Op&fsnotify.Write != 0:
				kind = "modified"
			case ev.Op&fsnotify.Rename != 0:
				kind = "renamed"
			case ev.Op&fsnotify.Remove != 0:
				kind = "deleted"
			}
			if kind != "" {
				p := filepath.ToSlash(rel)
				pending[p] = queued{kind, time.Now()}
			}
			if ev.Op&fsnotify.Create != 0 {
				if info, e := os.Stat(ev.Name); e == nil && info.IsDir() {
					_ = x.addTree(ev.Name)
				}
			}
		case <-ticker.C:
			now := time.Now()
			for p, q := range pending {
				if now.Sub(q.at) >= x.debounce {
					if x.logger != nil {
						x.logger.Printf("INFO  file changed: %s", p)
					}
					x.hub.Broadcast(server.Event{Type: q.event, Path: p})
					delete(pending, p)
				}
			}
		case <-x.w.Errors:
		}
	}
}
