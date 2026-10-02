package watcher

import (
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gaurishhs/live-code-server/internal/filesystem"
	"github.com/gaurishhs/live-code-server/internal/server"
	"github.com/gorilla/websocket"
)

func TestChangeBroadcastAndIgnore(t *testing.T) {
	d := t.TempDir()
	os.Mkdir(filepath.Join(d, "ignored"), 0755)
	r, e := filesystem.New(d, []string{"ignored"}, 1024)
	if e != nil {
		t.Fatal(e)
	}
	hub := server.NewHub(log.New(os.Stderr, "", 0))
	ws := httptest.NewServer(http.HandlerFunc(hub.ServeHTTP))
	defer ws.Close()
	conn, _, e := websocket.DefaultDialer.Dial("ws"+ws.URL[4:], nil)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	w, e := New(r, hub, 100*time.Millisecond)
	if e != nil {
		t.Fatal(e)
	}
	defer w.Close()
	done := make(chan struct{})
	go w.Run(done)
	defer close(done)
	os.WriteFile(filepath.Join(d, "a.txt"), []byte("one"), 0644)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var ev server.Event
	if e = conn.ReadJSON(&ev); e != nil {
		t.Fatal(e)
	}
	if ev.Path != "a.txt" || (ev.Type != "created" && ev.Type != "modified") {
		t.Fatalf("event %#v", ev)
	}
	os.WriteFile(filepath.Join(d, "ignored", "secret"), []byte("x"), 0644)
	_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	if e = conn.ReadJSON(&ev); e == nil {
		t.Fatalf("unexpected ignored event %#v", ev)
	}
}
