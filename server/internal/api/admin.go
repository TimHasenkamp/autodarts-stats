package api

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"autodarts-stats/internal/ingest"
	"autodarts-stats/internal/names"
)

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.AdminPass == "" {
		writeError(w, http.StatusServiceUnavailable, "ADMIN_PASSWORD ist nicht gesetzt")
		return
	}
	if !s.limiter.allow(s.clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "zu viele Versuche, bitte kurz warten")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body, 4096) {
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Password), []byte(s.AdminPass)) != 1 {
		writeError(w, http.StatusUnauthorized, "falsches Passwort")
		return
	}
	s.sess.set(w, r)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.sess.clear(w)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{"admin": s.sess.authed(r), "configured": s.AdminPass != ""})
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.sess.authed(r) {
			writeError(w, http.StatusUnauthorized, "Login erforderlich")
			return
		}
		next(w, r)
	}
}

type adminPlayer struct {
	ID              int64    `json:"id"`
	DisplayName     string   `json:"display_name"`
	NormalizedName  string   `json:"normalized_name"`
	AutodartsUserID string   `json:"autodarts_user_id,omitempty"`
	Aliases         []string `json:"aliases"`
	Protected       bool     `json:"protected"`
	Chips           int      `json:"chips"`
	Matches         int      `json:"matches"`
	Legs            int      `json:"legs"`
}

func (s *Server) adminPlayers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `SELECT p.id, p.display_name, p.normalized_name, COALESCE(p.autodarts_user_id,''), p.protected,
		(SELECT COUNT(*) FROM player_chips c WHERE c.player_id = p.id),
		(SELECT COUNT(*) FROM match_players mp WHERE mp.player_id = p.id), (SELECT COUNT(*) FROM match_legs l WHERE l.player_id = p.id)
		FROM players p ORDER BY LOWER(p.display_name)`)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []adminPlayer{}
	byID := map[int64]int{}
	for rows.Next() {
		var p adminPlayer
		if err := rows.Scan(&p.ID, &p.DisplayName, &p.NormalizedName, &p.AutodartsUserID, &p.Protected, &p.Chips, &p.Matches, &p.Legs); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		p.Aliases = []string{}
		byID[p.ID] = len(out)
		out = append(out, p)
	}
	ar, err := s.DB.QueryContext(r.Context(), `SELECT player_id, normalized_name FROM player_aliases ORDER BY normalized_name`)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	defer ar.Close()
	for ar.Next() {
		var pid int64
		var a string
		if err := ar.Scan(&pid, &a); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		if i, ok := byID[pid]; ok {
			out[i].Aliases = append(out[i].Aliases, a)
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) adminRename(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, 400, "ungueltige ID")
		return
	}
	var body struct {
		DisplayName string `json:"display_name"`
	}
	if !readJSON(w, r, &body, 4096) {
		return
	}
	name := strings.TrimSpace(body.DisplayName)
	if name == "" {
		writeError(w, 400, "Name fehlt")
		return
	}
	// Nur der Anzeigename aendert sich; normalized_name/Aliase bleiben, damit
	// die Zuordnung beim Ingest stabil bleibt.
	res, err := s.DB.ExecContext(r.Context(), `UPDATE players SET display_name = ? WHERE id = ?`, name, id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, 404, "Spieler nicht gefunden")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// adminMerge verschiebt alle Daten von {id} nach into und loescht {id}.
// Der alte normalisierte Name wird Alias des Zielspielers.
func (s *Server) adminMerge(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, 400, "ungueltige ID")
		return
	}
	var body struct {
		Into int64 `json:"into"`
	}
	if !readJSON(w, r, &body, 4096) {
		return
	}
	if body.Into <= 0 || body.Into == id {
		writeError(w, 400, "Zielspieler ungueltig")
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	defer tx.Rollback()
	var srcNorm string
	var srcUID sql.NullString
	if err := tx.QueryRow(`SELECT normalized_name, autodarts_user_id FROM players WHERE id = ?`, id).Scan(&srcNorm, &srcUID); err != nil {
		writeError(w, 404, "Quellspieler nicht gefunden")
		return
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM players WHERE id = ?`, body.Into).Scan(&n); err != nil || n == 0 {
		writeError(w, 404, "Zielspieler nicht gefunden")
		return
	}
	stmts := []string{
		// Falls beide im selben Match waren, bleiben die Zeilen des Zielspielers.
		`DELETE FROM match_players WHERE player_id = ? AND match_id IN (SELECT match_id FROM match_players WHERE player_id = ?)`,
		`UPDATE match_players SET player_id = ? WHERE player_id = ?`,
		`DELETE FROM match_legs WHERE player_id = ? AND (match_id, set_no, leg_no) IN (SELECT match_id, set_no, leg_no FROM match_legs WHERE player_id = ?)`,
		`UPDATE match_legs SET player_id = ? WHERE player_id = ?`,
		`UPDATE player_aliases SET player_id = ? WHERE player_id = ?`,
		`UPDATE match_overrides SET player_id = ? WHERE player_id = ?`,
		`UPDATE player_chips SET player_id = ? WHERE player_id = ?`,
		`UPDATE checkins SET player_id = ? WHERE player_id = ?`,
		`UPDATE match_pending SET suggested_id = ? WHERE suggested_id = ?`,
		`DELETE FROM tournament_players WHERE player_id = ? AND tournament_id IN (SELECT tournament_id FROM tournament_players WHERE player_id = ?)`,
		`UPDATE tournament_players SET player_id = ? WHERE player_id = ?`,
		`UPDATE tournament_results SET player1_id = ? WHERE player1_id = ?`,
		`UPDATE tournament_results SET player2_id = ? WHERE player2_id = ?`,
		`UPDATE tournament_results SET winner_id = ? WHERE winner_id = ?`,
	}
	args := [][]any{{id, body.Into}, {body.Into, id}, {id, body.Into}, {body.Into, id}, {body.Into, id}, {body.Into, id}, {body.Into, id}, {body.Into, id}, {body.Into, id},
		{id, body.Into}, {body.Into, id}, {body.Into, id}, {body.Into, id}, {body.Into, id}}
	for i, q := range stmts {
		if _, err := tx.Exec(q, args[i]...); err != nil {
			writeError(w, 500, err.Error())
			return
		}
	}
	if _, err := tx.Exec(`DELETE FROM players WHERE id = ?`, id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO player_aliases (normalized_name, player_id) VALUES (?, ?)`, srcNorm, body.Into); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if srcUID.Valid && srcUID.String != "" {
		_, _ = tx.Exec(`UPDATE players SET autodarts_user_id = ? WHERE id = ? AND autodarts_user_id IS NULL`, srcUID.String, body.Into)
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) adminAddAlias(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, 400, "ungueltige ID")
		return
	}
	var body struct {
		Alias string `json:"alias"`
	}
	if !readJSON(w, r, &body, 4096) {
		return
	}
	norm := names.Normalize(body.Alias)
	if norm == "" {
		writeError(w, 400, "Alias fehlt")
		return
	}
	var other int64
	if err := s.DB.QueryRow(`SELECT id FROM players WHERE normalized_name = ? AND id <> ?`, norm, id).Scan(&other); err == nil {
		writeError(w, 409, "Name gehoert bereits einem anderen Spieler; bitte stattdessen zusammenfuehren")
		return
	}
	if _, err := s.DB.ExecContext(r.Context(), `INSERT INTO player_aliases (normalized_name, player_id) VALUES (?, ?)
		ON CONFLICT(normalized_name) DO UPDATE SET player_id = excluded.player_id`, norm, id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"alias": norm})
}

func (s *Server) adminDeleteAlias(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, 400, "ungueltige ID")
		return
	}
	alias := names.Normalize(r.PathValue("alias"))
	var own string
	if err := s.DB.QueryRow(`SELECT normalized_name FROM players WHERE id = ?`, id).Scan(&own); err == nil && own == alias {
		writeError(w, 400, "der Hauptname kann nicht entfernt werden")
		return
	}
	if _, err := s.DB.ExecContext(r.Context(), `DELETE FROM player_aliases WHERE player_id = ? AND normalized_name = ?`, id, alias); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// adminDeletePlayer loescht Spieler inkl. Aliase und match_players/match_legs (CASCADE).
func (s *Server) adminDeletePlayer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, 400, "ungueltige ID")
		return
	}
	res, err := s.DB.ExecContext(r.Context(), `DELETE FROM players WHERE id = ?`, id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, 404, "Spieler nicht gefunden")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

type boardInfo struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at,omitempty"`
	Matches    int    `json:"matches"`
}

func (s *Server) adminBoards(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `SELECT b.id, b.name, b.created_at, COALESCE(b.last_seen_at,''), (SELECT COUNT(*) FROM matches m WHERE m.board_id = b.id) FROM boards b ORDER BY b.name`)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []boardInfo{}
	for rows.Next() {
		var b boardInfo
		if err := rows.Scan(&b.ID, &b.Name, &b.CreatedAt, &b.LastSeenAt, &b.Matches); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		out = append(out, b)
	}
	writeJSON(w, 200, out)
}

func NewAPIKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "adb_" + base64.RawURLEncoding.EncodeToString(b), nil
}

func CreateBoard(db *sql.DB, name string) (int64, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, "", errors.New("Board-Name fehlt")
	}
	key, err := NewAPIKey()
	if err != nil {
		return 0, "", err
	}
	res, err := db.Exec(`INSERT INTO boards (name, api_key_hash) VALUES (?, ?)`, name, ingest.HashKey(key))
	if err != nil {
		return 0, "", err
	}
	id, _ := res.LastInsertId()
	return id, key, nil
}

func (s *Server) adminCreateBoard(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body, 4096) {
		return
	}
	id, key, err := CreateBoard(s.DB, body.Name)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeError(w, 409, "Board-Name existiert bereits")
			return
		}
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": id, "name": strings.TrimSpace(body.Name), "api_key": key})
}

func (s *Server) adminDeleteBoard(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, 400, "ungueltige ID")
		return
	}
	if _, err := s.DB.ExecContext(r.Context(), `DELETE FROM boards WHERE id = ?`, id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

type unparsedInfo struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"`
	URL        string `json:"url"`
	Reason     string `json:"reason"`
	Size       int    `json:"size"`
	Preview    string `json:"preview"`
	ReceivedAt string `json:"received_at"`
}

func (s *Server) adminUnparsed(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id, kind, url, reason, LENGTH(raw_json), SUBSTR(raw_json, 1, 200), received_at FROM unparsed_events ORDER BY id DESC LIMIT 200`)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []unparsedInfo{}
	for rows.Next() {
		var u unparsedInfo
		if err := rows.Scan(&u.ID, &u.Kind, &u.URL, &u.Reason, &u.Size, &u.Preview, &u.ReceivedAt); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		out = append(out, u)
	}
	writeJSON(w, 200, out)
}

func (s *Server) adminUnparsedOne(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, 400, "ungueltige ID")
		return
	}
	var raw string
	if err := s.DB.QueryRowContext(r.Context(), `SELECT raw_json FROM unparsed_events WHERE id = ?`, id).Scan(&raw); err != nil {
		writeError(w, 404, "nicht gefunden")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline; filename=\"unparsed_"+r.PathValue("id")+".json\"")
	w.Write([]byte(raw))
}

func (s *Server) adminClearUnparsed(w http.ResponseWriter, r *http.Request) {
	if _, err := s.DB.ExecContext(r.Context(), `DELETE FROM unparsed_events`); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) adminMatchRaw(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, 400, "ungueltige ID")
		return
	}
	var raw string
	if err := s.DB.QueryRowContext(r.Context(), `SELECT raw_json FROM matches WHERE id = ?`, id).Scan(&raw); err != nil {
		writeError(w, 404, "nicht gefunden")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write([]byte(raw))
}

func (s *Server) adminDeleteMatch(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, 400, "ungueltige ID")
		return
	}
	if _, err := s.DB.ExecContext(r.Context(), `DELETE FROM matches WHERE id = ?`, id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) adminReprocessUnparsed(w http.ResponseWriter, r *http.Request) {
	n, total, err := s.Ingest.ReprocessUnparsed(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]int{"recognized": n, "total": total})
}

func (s *Server) adminReprocess(w http.ResponseWriter, r *http.Request) {
	n, err := s.Ingest.Reprocess(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]int{"matches": n})
}
