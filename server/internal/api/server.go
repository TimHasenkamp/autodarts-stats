// Package api verdrahtet HTTP-Routen: Ingest, oeffentliche Stats-API, Admin.
package api

import (
	"database/sql"
	"errors"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"autodarts-stats/internal/ingest"
	"autodarts-stats/internal/stats"
)

type Server struct {
	DB         *sql.DB
	Ingest     *ingest.Service
	Stats      *stats.Service
	AdminPass  string
	TrustProxy bool
	CheckinTTL time.Duration
	sess       sessions
	limiter    *loginLimiter
	static     http.Handler
}

type Options struct {
	DB            *sql.DB
	Ingest        *ingest.Service
	Stats         *stats.Service
	AdminPassword string
	SessionSecret []byte
	TrustProxy    bool
	CheckinTTL    time.Duration
	Static        http.Handler
}

func New(o Options) *Server {
	return &Server{
		DB: o.DB, Ingest: o.Ingest, Stats: o.Stats, AdminPass: o.AdminPassword, TrustProxy: o.TrustProxy, CheckinTTL: o.CheckinTTL,
		sess: sessions{secret: o.SessionSecret, secure: false}, limiter: newLoginLimiter(), static: o.Static,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })

	mux.HandleFunc("POST /api/ingest", s.handleIngest)
	mux.HandleFunc("GET /api/ingest/ping", s.handleIngestPing)
	mux.HandleFunc("POST /api/checkin", s.handleCheckin)
	mux.HandleFunc("GET /api/checkins", s.handleBoardCheckins)

	mux.HandleFunc("GET /api/meta", s.handleMeta)
	mux.HandleFunc("GET /api/leaderboard", s.handleLeaderboard)
	mux.HandleFunc("GET /api/players", s.handlePlayers)
	mux.HandleFunc("GET /api/players/{id}", s.handlePlayer)
	mux.HandleFunc("GET /api/h2h", s.handleH2H)
	mux.HandleFunc("GET /api/matches", s.handleMatches)
	mux.HandleFunc("GET /api/matches/{id}", s.handleMatch)

	mux.HandleFunc("POST /api/admin/login", s.handleLogin)
	mux.HandleFunc("POST /api/admin/logout", s.handleLogout)
	mux.HandleFunc("GET /api/admin/me", s.handleMe)
	admin := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, s.requireAdmin(h)) }
	admin("GET /api/admin/players", s.adminPlayers)
	admin("POST /api/admin/players/{id}/rename", s.adminRename)
	admin("POST /api/admin/players/{id}/merge", s.adminMerge)
	admin("POST /api/admin/players/{id}/aliases", s.adminAddAlias)
	admin("DELETE /api/admin/players/{id}/aliases/{alias}", s.adminDeleteAlias)
	admin("DELETE /api/admin/players/{id}", s.adminDeletePlayer)
	admin("GET /api/admin/boards", s.adminBoards)
	admin("POST /api/admin/boards", s.adminCreateBoard)
	admin("DELETE /api/admin/boards/{id}", s.adminDeleteBoard)
	admin("GET /api/admin/unparsed", s.adminUnparsed)
	admin("GET /api/admin/unparsed/{id}", s.adminUnparsedOne)
	admin("DELETE /api/admin/unparsed", s.adminClearUnparsed)
	admin("GET /api/admin/matches/{id}/raw", s.adminMatchRaw)
	admin("DELETE /api/admin/matches/{id}", s.adminDeleteMatch)
	admin("POST /api/admin/reprocess", s.adminReprocess)
	admin("POST /api/admin/unparsed/reprocess", s.adminReprocessUnparsed)
	admin("POST /api/admin/players", s.adminCreatePlayer)
	admin("POST /api/admin/players/{id}/protect", s.adminProtect)
	admin("GET /api/admin/chips", s.adminChips)
	admin("POST /api/admin/chips", s.adminRegisterChip)
	admin("DELETE /api/admin/chips/{id}", s.adminDeleteChip)
	admin("DELETE /api/admin/chips/unknown/{hash}", s.adminDeleteUnknownChip)
	admin("GET /api/admin/checkins", s.adminCheckins)
	admin("POST /api/admin/checkins", s.adminManualCheckin)
	admin("DELETE /api/admin/checkins/{id}", s.adminEndCheckin)
	admin("GET /api/admin/pending", s.adminPending)
	admin("POST /api/admin/pending/{match}/{index}", s.adminResolvePending)
	admin("POST /api/admin/matches/{match}/slots/{index}", s.adminReassign)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeError(w, 404, "unbekannter Endpunkt") })
	if s.static != nil {
		mux.Handle("/", s.static)
	}
	return s.logging(mux)
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, rw.status, time.Since(start).Round(time.Millisecond))
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }

func (s *Server) clientIP(r *http.Request) string {
	if s.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---- Ingest ----

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

type ingestRequest struct {
	Board  string         `json:"board"`
	Events []ingest.Event `json:"events"`
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	boardID, _, err := s.Ingest.AuthBoard(r.Context(), bearer(r))
	if err != nil {
		if errors.Is(err, ingest.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "API-Key ungueltig")
			return
		}
		writeError(w, 500, err.Error())
		return
	}
	var req ingestRequest
	if !readJSON(w, r, &req, 64<<20) {
		return
	}
	if len(req.Events) == 0 {
		writeJSON(w, 200, map[string]any{"results": []ingest.Result{}})
		return
	}
	if len(req.Events) > 500 {
		writeError(w, http.StatusRequestEntityTooLarge, "zu viele Events")
		return
	}
	results := make([]ingest.Result, 0, len(req.Events))
	for _, ev := range req.Events {
		res, err := s.Ingest.HandleEvent(r.Context(), boardID, ev)
		if err != nil {
			log.Printf("ingest: %v", err)
			writeError(w, 500, "Verarbeitung fehlgeschlagen: "+err.Error())
			return
		}
		results = append(results, res)
	}
	writeJSON(w, 200, map[string]any{"results": results})
}

func (s *Server) handleIngestPing(w http.ResponseWriter, r *http.Request) {
	_, name, err := s.Ingest.AuthBoard(r.Context(), bearer(r))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "API-Key ungueltig")
		return
	}
	writeJSON(w, 200, map[string]string{"board": name})
}

// ---- Public ----

func parseFilter(r *http.Request) stats.Filter {
	q := r.URL.Query()
	f := stats.Filter{Variant: q.Get("variant"), Sort: q.Get("sort"), Order: q.Get("order"), MinMatches: 0}
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			f.From = t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			f.To = t.Add(24 * time.Hour)
		}
	}
	if v := q.Get("min_matches"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			f.MinMatches = n
		}
	}
	return f
}

func pathID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id, err == nil && id > 0
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	m, err := s.Stats.Meta(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, m)
}

func (s *Server) handleLeaderboard(w http.ResponseWriter, r *http.Request) {
	f := parseFilter(r)
	if r.URL.Query().Get("min_matches") == "" {
		f.MinMatches = 5
	}
	rows, err := s.Stats.Leaderboard(r.Context(), f)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if rows == nil {
		rows = []stats.PlayerRow{}
	}
	writeJSON(w, 200, rows)
}

func (s *Server) handlePlayers(w http.ResponseWriter, r *http.Request) {
	f := parseFilter(r)
	f.MinMatches = 0
	if f.Sort == "" {
		f.Sort = "name"
		f.Order = "asc"
	}
	rows, err := s.Stats.Players(r.Context(), f)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if rows == nil {
		rows = []stats.PlayerRow{}
	}
	writeJSON(w, 200, rows)
}

func (s *Server) handlePlayer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, 400, "ungueltige ID")
		return
	}
	p, err := s.Stats.Profile(r.Context(), id, parseFilter(r))
	if errors.Is(err, stats.ErrNotFound) {
		writeError(w, 404, "Spieler nicht gefunden")
		return
	}
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) handleH2H(w http.ResponseWriter, r *http.Request) {
	a, errA := strconv.ParseInt(r.URL.Query().Get("a"), 10, 64)
	b, errB := strconv.ParseInt(r.URL.Query().Get("b"), 10, 64)
	if errA != nil || errB != nil || a == b {
		writeError(w, 400, "a und b muessen verschiedene Spieler-IDs sein")
		return
	}
	h, err := s.Stats.HeadToHead(r.Context(), a, b, parseFilter(r))
	if errors.Is(err, stats.ErrNotFound) {
		writeError(w, 404, "Spieler nicht gefunden")
		return
	}
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, h)
}

func (s *Server) handleMatches(w http.ResponseWriter, r *http.Request) {
	limit := 30
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	ms, err := s.Stats.RecentMatches(r.Context(), limit)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, ms)
}

func (s *Server) handleMatch(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, 400, "ungueltige ID")
		return
	}
	m, err := s.Stats.Match(r.Context(), id)
	if errors.Is(err, stats.ErrNotFound) {
		writeError(w, 404, "Match nicht gefunden")
		return
	}
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, m)
}
