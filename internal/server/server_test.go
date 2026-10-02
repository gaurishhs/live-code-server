package server

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gaurishhs/live-code-server/internal/filesystem"
	"github.com/gorilla/websocket"
)

func testServer(t *testing.T, token string) *Server {
	t.Helper()
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "a.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := filesystem.New(d, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{Root: r, Token: token, Origins: []string{"https://viewer.test"}, Logger: log.New(io.Discard, "", 0)})
}

func TestAPIAuthCORSAndLimit(t *testing.T) {
	s := testServer(t, "secret")
	h := httptest.NewServer(s.Handler())
	defer h.Close()
	res, err := http.Get(h.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("auth status %d", res.StatusCode)
	}
	res.Body.Close()
	req, _ := http.NewRequest("GET", h.URL+"/api/health", nil)
	req.Header.Set("Authorization", "Bearer secret")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("health status %d", res.StatusCode)
	}
	res.Body.Close()
	req, _ = http.NewRequest("OPTIONS", h.URL+"/api/health", nil)
	req.Header.Set("Origin", "https://viewer.test")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Header.Get("Access-Control-Allow-Origin") != "https://viewer.test" {
		t.Fatal("CORS origin not allowed")
	}
	res.Body.Close()
	req, _ = http.NewRequest("GET", h.URL+"/api/file?path=a.txt", nil)
	req.Header.Set("Authorization", "Bearer secret")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("size status %d", res.StatusCode)
	}
	res.Body.Close()
}

func TestWebsocketBroadcast(t *testing.T) {
	s := testServer(t, "")
	h := httptest.NewServer(s.Handler())
	defer h.Close()
	c, _, err := websocket.DefaultDialer.Dial("ws"+h.URL[4:]+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	go s.Hub().Broadcast(Event{Type: "modified", Path: "a.txt"})
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	var event Event
	if err := c.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "modified" || event.Path != "a.txt" {
		t.Fatalf("event %#v", event)
	}
}
