package hc

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/haorui/agentSpace/skills/hello-center"
)

// Server is the Hello Center process: one business layer behind HTTP and MCP.
type Server struct {
	cfg      Config
	st       *store
	hub      *hub
	log      *slog.Logger
	joinLim  *slidingLimiter
	loginLim *slidingLimiter
	writeLim *slidingLimiter
	skillMD  string
}

// Open prepares directories, the database, and the first administrator.
func Open(cfg Config) (*Server, error) {
	cfg = cfg.normalized()
	if strings.TrimSpace(cfg.AdminPassword) == "" || len(cfg.AdminPassword) < 8 {
		return nil, errors.New("ADMIN_PASSWORD must be at least 8 characters")
	}
	user, ok := trimMax(cfg.AdminUsername, 64)
	if !ok {
		return nil, errors.New("ADMIN_USERNAME is invalid")
	}
	cfg.AdminUsername = user
	if cfg.AssetDir == "" {
		return nil, errors.New("asset directory is required")
	}
	if err := ensureDir(cfg.AssetDir); err != nil {
		return nil, err
	}
	st, version, err := openStore(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:      cfg,
		st:       st,
		hub:      newHub(),
		log:      cfg.Logger,
		joinLim:  newLimiter(cfg.JoinRatePerMinute, time.Minute),
		loginLim: newLimiter(cfg.LoginRatePerMinute, time.Minute),
		writeLim: newLimiter(cfg.WriteRatePerMinute, time.Minute),
		skillMD:  skill.Markdown,
	}
	if err := s.ensureAdmin(context.Background()); err != nil {
		st.close()
		return nil, err
	}
	s.log.Info("redis ready", "version", version, "addr", cfg.RedisAddr, "db", cfg.RedisDB, "prefix", cfg.KeyPrefix)
	if cfg.AdminPassword == "change-me-please" {
		s.log.Warn("ADMIN_PASSWORD is still the example value; change it before sharing this service")
	}
	return s, nil
}

// Close releases the Redis client. Stored data stays in Redis.
func (s *Server) Close() error {
	if s == nil || s.st == nil {
		return nil
	}
	err := s.st.close()
	s.st = nil
	return err
}

// DeletePrefix removes only this process's key prefix. Tests use it so a shared Redis is not flushed.
func (s *Server) DeletePrefix(ctx context.Context) error {
	if s == nil || s.st == nil {
		return nil
	}
	return s.st.deletePrefix(ctx)
}

type slidingLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string][]time.Time
}

func newLimiter(limit int, window time.Duration) *slidingLimiter {
	return &slidingLimiter{limit: limit, window: window, hits: map[string][]time.Time{}}
}

func (l *slidingLimiter) allow(key string) bool {
	if l == nil || l.limit < 0 {
		return true
	}
	if l.limit == 0 {
		return false
	}
	now := time.Now()
	cutoff := now.Add(-l.window)
	l.mu.Lock()
	defer l.mu.Unlock()
	prev := l.hits[key]
	kept := make([]time.Time, 0, len(prev)+1)
	for _, t := range prev {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}

type hub struct {
	mu   sync.Mutex
	next uint64
	subs map[uint64]hubSub
}

type hubSub struct {
	spaceID string
	admin   bool
	rooms   map[string]bool
	ch      chan Event
}

func newHub() *hub {
	return &hub{subs: map[uint64]hubSub{}}
}

func (h *hub) add(spaceID string, admin bool, rooms map[string]bool) (uint64, chan Event) {
	ch := make(chan Event, 32)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	id := h.next
	h.subs[id] = hubSub{spaceID: spaceID, admin: admin, rooms: rooms, ch: ch}
	return id, ch
}

func (h *hub) remove(id uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, id)
}

func (h *hub) publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, sub := range h.subs {
		if sub.spaceID != ev.SpaceID {
			continue
		}
		if ev.Visibility == "room" && (sub.admin || !sub.rooms[ev.RoomID]) {
			continue
		}
		select {
		case sub.ch <- ev:
		default:
		}
	}
}

// Handler is the HTTP API, invitation pages, MCP endpoint, and web console.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	return s.middleware(mux)
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' blob:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		path := r.URL.Path
		if strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/i/") || path == "/mcp" || path == "/openapi.yaml" {
			w.Header().Set("Cache-Control", "no-store")
		}
		for _, key := range []string{"access_token", "token", "invite_code"} {
			if r.URL.Query().Has(key) {
				s.writeErr(w, badRequest("credentials must not be passed in the query string"))
				return
			}
		}
		if s.needsCSRF(r) && r.Header.Get("X-Hello-Center") != "1" {
			s.writeErr(w, forbidden("missing X-Hello-Center header"))
			return
		}
		if s.isWrite(r) && !strings.HasSuffix(r.URL.Path, "/join") && !strings.HasSuffix(r.URL.Path, "/admin/login") {
			if !s.writeLim.allow(clientIP(r)) {
				w.Header().Set("Retry-After", "60")
				s.writeErr(w, rateLimited("too many requests"))
				return
			}
		}
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r)
		s.log.Info("request",
			"method", r.Method,
			"path", redactPath(r.URL.Path),
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

func (s *Server) needsCSRF(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return false
	}
	if r.Header.Get("Authorization") != "" {
		return false
	}
	if _, err := r.Cookie("hc_session"); err == nil {
		return true
	}
	// Login is a cookie-setting mutation and must also opt in.
	return strings.HasSuffix(r.URL.Path, "/admin/login")
}

func (s *Server) isWrite(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
		return strings.HasPrefix(r.URL.Path, "/api/")
	default:
		return false
	}
}

func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	return host
}

func redactPath(path string) string {
	if strings.HasPrefix(path, "/i/") {
		rest := strings.TrimPrefix(path, "/i/")
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			return "/i/:invite/" + rest[i+1:]
		}
		return "/i/:invite"
	}
	const marker = "/invitations/"
	if i := strings.Index(path, marker); i >= 0 {
		rest := path[i+len(marker):]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			return path[:i] + marker + ":invite/" + rest[j+1:]
		}
		return path[:i] + marker + ":invite"
	}
	return path
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.status = code
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.status = http.StatusOK
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
