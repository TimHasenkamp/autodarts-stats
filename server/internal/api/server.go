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
	"sync"
	"time"

	"autodarts-stats/internal/ingest"
	"autodarts-stats/internal/stats"
	"autodarts-stats/internal/tournament"
)

type Server struct {
	DB          *sql.DB
	Ingest      *ingest.Service
	Stats       *stats.Service
	Tournaments *tournament.Service
	AdminPass   string
	TrustProxy  bool
	CheckinTTL  time.Duration
	sess        sessions
	limiter     *loginLimiter
	static      http.Handler
	xffWarn     sync.Once
	reprocess   reprocessJob
	// reprocessWait: so lange wartet POST /api/admin/reprocess auf das
	// Ergebnis, danach 202 und der Job laeuft im Hintergrund weiter
	// (deutlich unter dem WriteTimeout des HTTP-Servers).
	reprocessWait time.Duration
}

type Options struct {
	DB            *sql.DB
	Ingest        *ingest.Service
	Stats         *stats.Service
	Tournaments   *tournament.Service
	AdminPassword string
	SessionSecret []byte
	TrustProxy    bool
	CheckinTTL    time.Duration
	Static        http.Handler
}

func New(o Options) *Server {
	return &Server{
		DB: o.DB, Ingest: o.Ingest, Stats: o.Stats, Tournaments: o.Tournaments, AdminPass: o.AdminPassword, TrustProxy: o.TrustProxy, CheckinTTL: o.CheckinTTL,
		sess: sessions{secret: o.SessionSecret, secure: false}, limiter: newLoginLimiter(), static: o.Static,
		reprocessWait: 40 * time.Second,
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
	mux.HandleFunc("GET /api/tournaments", s.handleTournaments)
	mux.HandleFunc("GET /api/tournaments/{id}", s.handleTournament)
	mux.HandleFunc("GET /api/players/{id}/tournaments", s.handlePlayerTournaments)

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
	admin("GET /api/admin/reprocess", s.adminReprocessStatus)
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
	admin("POST /api/admin/tournaments", s.adminCreateTournament)
	admin("PUT /api/admin/tournaments/{id}", s.adminUpdateTournament)
	admin("DELETE /api/admin/tournaments/{id}", s.adminTournamentAction(func(r *http.Request, id int64) error { return s.Tournaments.Delete(r.Context(), id) }))
	admin("POST /api/admin/tournaments/{id}/start", s.adminTournamentAction(func(r *http.Request, id int64) error { return s.Tournaments.Start(r.Context(), id) }))
	admin("POST /api/admin/tournaments/{id}/redraw", s.adminTournamentAction(func(r *http.Request, id int64) error { return s.Tournaments.Redraw(r.Context(), id) }))
	admin("POST /api/admin/tournaments/{id}/results/{key}", s.adminTournamentResult)
	admin("DELETE /api/admin/tournaments/{id}/results/{key}", s.adminTournamentAction(func(r *http.Request, id int64) error {
		return s.Tournaments.ClearResult(r.Context(), id, r.PathValue("key"), forced(r))
	}))
	admin("GET /api/admin/tournaments/{id}/candidates/{key}", s.adminTournamentCandidates)
	admin("POST /api/admin/tournaments/{id}/players/{pid}/replace", s.adminTournamentReplace)
	admin("POST /api/admin/tournaments/{id}/players/{pid}/swap", s.adminTournamentSwap)
	admin("POST /api/admin/tournaments/{id}/players/{pid}/withdraw", s.adminTournamentWithdraw)
	admin("DELETE /api/admin/tournaments/{id}/players/{pid}/withdraw", s.adminTournamentUndoWithdraw)
	admin("POST /api/admin/tournaments/{id}/late/{key}", s.adminTournamentLate)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeError(w, 404, "unbekannter Endpunkt") })
	if s.static != nil {
		mux.Handle("/", s.static)
	}
	return s.logging(mux)
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// Global, damit auch Rohdaten-Endpunkte (adminUnparsedOne,
		// adminMatchRaw) nie als HTML o.ae. interpretiert werden.
		w.Header().Set("X-Content-Type-Options", "nosniff")
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

// clientIP liefert die IP fuer das Login-Rate-Limit.
//
// Mit TRUST_PROXY zaehlt das letzte Element von X-Forwarded-For: das haengt
// der eigene Reverse-Proxy an (nginx: $proxy_add_x_forwarded_for, Caddy
// ersetzt den Header ohne trusted_proxies ganz). Alles links davon kommt vom
// Client und ist frei faelschbar. Genau ein Proxy davor wird vorausgesetzt.
// X-Real-IP wird bewusst ignoriert: Caddy setzt es nicht und reicht einen
// vom Client geschickten Wert unveraendert durch.
func (s *Server) clientIP(r *http.Request) string {
	xff := r.Header.Values("X-Forwarded-For")
	if s.TrustProxy {
		if ip := lastForwarded(xff); ip != "" {
			return ip
		}
	} else if len(xff) > 0 {
		s.xffWarn.Do(func() {
			log.Printf("WARNUNG: Anfrage mit X-Forwarded-For, aber TRUST_PROXY ist aus; alle Clients teilen sich das Login-Limit des Proxys. Hinter einem Reverse-Proxy TRUST_PROXY=1 setzen.")
		})
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// lastForwarded liefert das letzte gueltige Element aus (ggf. mehreren)
// X-Forwarded-For-Headern.
func lastForwarded(values []string) string {
	if len(values) == 0 {
		return ""
	}
	parts := strings.Split(values[len(values)-1], ",")
	ip := strings.TrimSpace(parts[len(parts)-1])
	if net.ParseIP(ip) == nil {
		return ""
	}
	return ip
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
		internalError(w, r, err)
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
	// Dauerhaft unbrauchbare Events (ingest.IsPermanent) werden pro Event
	// gemeldet und bestaetigt, sonst sendet die Extension den Batch endlos
	// neu. Bei einem voruebergehenden Fehler (SQLITE_BUSY, I/O, Kontext ...)
	// bricht der Batch mit 503 ab: Die Extension behaelt ihn in der Queue und
	// sendet ihn spaeter komplett erneut. Die bis dahin verarbeiteten Events
	// sind idempotent, die Reihenfolge der Staende bleibt erhalten.
	results := make([]ingest.Result, 0, len(req.Events))
	for i, ev := range req.Events {
		res, err := s.Ingest.HandleEvent(r.Context(), boardID, ev)
		if err != nil {
			if !ingest.IsPermanent(err) {
				log.Printf("ingest: board %d event %d/%d (%q %q) voruebergehend fehlgeschlagen, batch wird wiederholt: %v",
					boardID, i, len(req.Events), ev.Kind, ev.URL, err)
				w.Header().Set("Retry-After", "30")
				writeError(w, http.StatusServiceUnavailable, "voruebergehender fehler, bitte erneut senden")
				return
			}
			log.Printf("ingest: board %d event %d (%q %q) verworfen: %v", boardID, i, ev.Kind, ev.URL, err)
			res = ingest.Result{Error: "event ungueltig"}
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
		internalError(w, r, err)
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
		internalError(w, r, err)
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
		internalError(w, r, err)
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
		internalError(w, r, err)
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
		internalError(w, r, err)
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
		internalError(w, r, err)
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
		internalError(w, r, err)
		return
	}
	writeJSON(w, 200, m)
}
