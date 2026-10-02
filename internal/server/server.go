package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gaurishhs/gor"
	"github.com/gaurishhs/live-code-server/internal/filesystem"
)

type Config struct {
	Root    *filesystem.Root
	Token   string
	Origins []string
	Logger  *log.Logger
	Verbose bool
}
type Server struct {
	cfg     Config
	hub     *Hub
	handler http.Handler
}

func New(c Config) *Server {
	s := &Server{cfg: c, hub: NewHub(c.Logger, c.Origins)}
	r := gor.NewRouter()
	r.Get("/api/health", s.health)
	r.Get("/api/tree", s.tree)
	r.Get("/api/file", s.file)
	r.Handle("/ws", s.auth(http.HandlerFunc(s.hub.ServeHTTP)))
	s.handler = s.cors(s.auth(r))
	if c.Verbose {
		s.handler = s.logRequests(s.handler)
	}
	return s
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Logger != nil {
			s.cfg.Logger.Printf("DEBUG %s %s", r.Method, r.URL.Path)
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) Handler() http.Handler { return s.handler }
func (s *Server) Hub() *Hub             { return s.hub }
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := r.Header.Get("Authorization")
		if r.URL.Path == "/ws" && provided == "" {
			provided = "Bearer " + r.URL.Query().Get("token")
		}
		if s.cfg.Token != "" && r.Method != http.MethodOptions && provided != "Bearer "+s.cfg.Token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			allowed := false
			for _, v := range s.cfg.Origins {
				if v == "*" || v == origin {
					allowed = true
					break
				}
			}
			if allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
				w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
func (s *Server) tree(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	e, err := s.cfg.Root.Tree()
	if err != nil {
		http.Error(w, "unable to read directory", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(e)
}
func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	p := r.URL.Query().Get("path")
	if p == "" {
		http.Error(w, "path is required", 400)
		return
	}
	b, err := s.cfg.Root.Read(filepath.ToSlash(p))
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "outside") || strings.Contains(err.Error(), "not exist") {
			status = http.StatusForbidden
		}
		if strings.Contains(err.Error(), "maximum") {
			status = http.StatusRequestEntityTooLarge
		}
		if errorsIsNotFound(err) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(b)
}
func errorsIsNotFound(err error) bool { return os.IsNotExist(err) }
