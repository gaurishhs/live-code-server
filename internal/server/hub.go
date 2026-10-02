package server

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Event struct {
	Type string `json:"type"`
	Path string `json:"path"`
}
type Hub struct {
	mu      sync.Mutex
	clients map[*websocket.Conn]struct{}
	logger  *log.Logger
	origins []string
}

func NewHub(l *log.Logger, origins ...[]string) *Hub {
	h := &Hub{clients: map[*websocket.Conn]struct{}{}, logger: l}
	if len(origins) > 0 {
		h.origins = origins[0]
	}
	return h
}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: func(req *http.Request) bool {
		origin := req.Header.Get("Origin")
		if origin == "" {
			return true
		}
		for _, allowed := range h.origins {
			if allowed == "*" || allowed == origin {
				return true
			}
		}
		return false
	}}
	c, e := upgrader.Upgrade(w, r, nil)
	if e != nil {
		return
	}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	if h.logger != nil {
		h.logger.Print("INFO  websocket client connected")
	}
	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		h.mu.Unlock()
		c.Close()
		if h.logger != nil {
			h.logger.Print("INFO  websocket client disconnected")
		}
	}()
	c.SetReadLimit(1024)
	for {
		if _, _, e = c.ReadMessage(); e != nil {
			return
		}
	}
}
func (h *Hub) Broadcast(ev Event) {
	b, e := json.Marshal(ev)
	if e != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if e := c.WriteMessage(websocket.TextMessage, b); e != nil {
			c.Close()
			delete(h.clients, c)
		}
	}
}

func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		_ = c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutting down"), time.Now().Add(time.Second))
		_ = c.Close()
		delete(h.clients, c)
	}
}
