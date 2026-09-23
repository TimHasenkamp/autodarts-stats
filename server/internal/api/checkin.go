package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"autodarts-stats/internal/ingest"
)

// ---- Board-Endpunkte (API-Key) ----

type checkinRequest struct {
	UID string `json:"uid"`
}

// handleCheckin: Chip am Board eingestempelt (vom Agent aufgerufen).
func (s *Server) handleCheckin(w http.ResponseWriter, r *http.Request) {
	boardID, _, err := s.Ingest.AuthBoard(r.Context(), bearer(r))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "API-Key ungueltig")
		return
	}
	var req checkinRequest
	if !readJSON(w, r, &req, 4096) {
		return
	}
	if ingest.NormalizeUID(req.UID) == "" {
		writeError(w, 400, "uid fehlt")
		return
	}
	ci, err := s.Ingest.CheckinByChip(r.Context(), boardID, req.UID, s.CheckinTTL)
	if errors.Is(err, ingest.ErrUnknownChip) {
		writeJSON(w, 404, map[string]any{"error": "Chip ist keinem Spieler zugeordnet. Im Admin unter Chips zuordnen.", "registered": false})
		return
	}
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, ci)
}

// handleBoardCheckins: aktive Check-ins dieses Boards (Extension-Overlay, Agent).
func (s *Server) handleBoardCheckins(w http.ResponseWriter, r *http.Request) {
	boardID, _, err := s.Ingest.AuthBoard(r.Context(), bearer(r))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "API-Key ungueltig")
		return
	}
	list, err := s.Ingest.ActiveCheckins(r.Context(), boardID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, list)
}

// ---- Admin ----

func (s *Server) adminProtect(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, 400, "ungueltige ID")
		return
	}
	var body struct {
		Protected bool `json:"protected"`
	}
	if !readJSON(w, r, &body, 1024) {
		return
	}
	if _, err := s.DB.ExecContext(r.Context(), `UPDATE players SET protected = ? WHERE id = ?`, body.Protected, id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) adminCreatePlayer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DisplayName string `json:"display_name"`
		Protected   bool   `json:"protected"`
	}
	if !readJSON(w, r, &body, 1024) {
		return
	}
	id, err := s.Ingest.CreatePlayer(r.Context(), body.DisplayName, body.Protected)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeError(w, 409, "Name existiert bereits")
			return
		}
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]int64{"id": id})
}

type chipInfo struct {
	ID          int64  `json:"id"`
	PlayerID    int64  `json:"player_id"`
	DisplayName string `json:"display_name"`
	Label       string `json:"label"`
	CreatedAt   string `json:"created_at"`
	LastUsedAt  string `json:"last_used_at,omitempty"`
}

type unknownChip struct {
	UIDHash   string `json:"uid_hash"`
	UIDHint   string `json:"uid_hint"`
	BoardName string `json:"board_name,omitempty"`
	FirstSeen string `json:"first_seen_at"`
	LastSeen  string `json:"last_seen_at"`
	SeenCount int    `json:"seen_count"`
}

func (s *Server) adminChips(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `SELECT c.id, c.player_id, p.display_name, c.label, c.created_at, COALESCE(c.last_used_at,'')
		FROM player_chips c JOIN players p ON p.id = c.player_id ORDER BY p.display_name, c.id`)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	known := []chipInfo{}
	for rows.Next() {
		var c chipInfo
		if err := rows.Scan(&c.ID, &c.PlayerID, &c.DisplayName, &c.Label, &c.CreatedAt, &c.LastUsedAt); err != nil {
			rows.Close()
			writeError(w, 500, err.Error())
			return
		}
		known = append(known, c)
	}
	rows.Close()
	urows, err := s.DB.QueryContext(r.Context(), `SELECT u.uid_hash, u.uid_hint, COALESCE(b.name,''), u.first_seen_at, u.last_seen_at, u.seen_count
		FROM unknown_chips u LEFT JOIN boards b ON b.id = u.board_id ORDER BY u.last_seen_at DESC`)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	defer urows.Close()
	unknown := []unknownChip{}
	for urows.Next() {
		var u unknownChip
		if err := urows.Scan(&u.UIDHash, &u.UIDHint, &u.BoardName, &u.FirstSeen, &u.LastSeen, &u.SeenCount); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		unknown = append(unknown, u)
	}
	writeJSON(w, 200, map[string]any{"chips": known, "unknown": unknown})
}

// adminRegisterChip bindet einen Chip (uid_hash aus der Unbekannt-Liste oder
// uid im Klartext) an einen bestehenden oder neuen Spieler.
func (s *Server) adminRegisterChip(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UIDHash  string `json:"uid_hash"`
		UID      string `json:"uid"`
		PlayerID int64  `json:"player_id"`
		NewName  string `json:"new_name"`
		Label    string `json:"label"`
	}
	if !readJSON(w, r, &body, 4096) {
		return
	}
	h := body.UIDHash
	if h == "" && body.UID != "" {
		h = ingest.ChipHash(body.UID)
	}
	if h == "" {
		writeError(w, 400, "uid_hash oder uid fehlt")
		return
	}
	pid := body.PlayerID
	if pid == 0 {
		if strings.TrimSpace(body.NewName) == "" {
			writeError(w, 400, "player_id oder new_name angeben")
			return
		}
		id, err := s.Ingest.CreatePlayer(r.Context(), body.NewName, true)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				writeError(w, 409, "Name existiert bereits, bitte bestehenden Spieler waehlen")
				return
			}
			writeError(w, 400, err.Error())
			return
		}
		pid = id
	}
	if err := s.Ingest.RegisterChip(r.Context(), h, pid, body.Label); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeError(w, 409, "Chip ist bereits registriert")
			return
		}
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]int64{"player_id": pid})
}

func (s *Server) adminDeleteChip(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, 400, "ungueltige ID")
		return
	}
	if _, err := s.DB.ExecContext(r.Context(), `DELETE FROM player_chips WHERE id = ?`, id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) adminDeleteUnknownChip(w http.ResponseWriter, r *http.Request) {
	if _, err := s.DB.ExecContext(r.Context(), `DELETE FROM unknown_chips WHERE uid_hash = ?`, r.PathValue("hash")); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) adminCheckins(w http.ResponseWriter, r *http.Request) {
	list, err := s.Ingest.ActiveCheckins(r.Context(), 0)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, list)
}

// adminManualCheckin: Check-in ohne Chip (z.B. Leser kaputt).
func (s *Server) adminManualCheckin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PlayerID int64 `json:"player_id"`
		BoardID  int64 `json:"board_id"`
	}
	if !readJSON(w, r, &body, 1024) {
		return
	}
	if body.PlayerID <= 0 || body.BoardID <= 0 {
		writeError(w, 400, "player_id und board_id angeben")
		return
	}
	ci, err := s.Ingest.CheckinPlayer(r.Context(), body.BoardID, body.PlayerID, s.CheckinTTL)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, ci)
}

func (s *Server) adminEndCheckin(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, 400, "ungueltige ID")
		return
	}
	if err := s.Ingest.EndCheckin(r.Context(), id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

type pendingInfo struct {
	MatchID       int64  `json:"match_id"`
	PlayerIndex   int    `json:"player_index"`
	Name          string `json:"name"`
	UserID        string `json:"user_id,omitempty"`
	SuggestedID   int64  `json:"suggested_id,omitempty"`
	SuggestedName string `json:"suggested_name,omitempty"`
	Reason        string `json:"reason"`
	CreatedAt     string `json:"created_at"`
	PlayedAt      string `json:"played_at"`
	Variant       string `json:"variant"`
	BoardName     string `json:"board_name,omitempty"`
	Opponents     string `json:"opponents"`
	Finished      bool   `json:"finished"`
}

func (s *Server) adminPending(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `SELECT mp.match_id, mp.player_index, mp.name, mp.user_id, COALESCE(mp.suggested_id,0), COALESCE(sp.display_name,''), mp.reason, mp.created_at,
		m.played_at, m.variant, COALESCE(b.name,''), m.finished,
		COALESCE((SELECT GROUP_CONCAT(p.display_name, ', ') FROM match_players x JOIN players p ON p.id = x.player_id WHERE x.match_id = m.id), '')
		FROM match_pending mp JOIN matches m ON m.id = mp.match_id
		LEFT JOIN players sp ON sp.id = mp.suggested_id LEFT JOIN boards b ON b.id = m.board_id
		ORDER BY m.played_at DESC, mp.player_index`)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []pendingInfo{}
	for rows.Next() {
		var p pendingInfo
		if err := rows.Scan(&p.MatchID, &p.PlayerIndex, &p.Name, &p.UserID, &p.SuggestedID, &p.SuggestedName, &p.Reason, &p.CreatedAt,
			&p.PlayedAt, &p.Variant, &p.BoardName, &p.Finished, &p.Opponents); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		out = append(out, p)
	}
	writeJSON(w, 200, out)
}

// adminResolvePending: {"player_id": 3} zuordnen, {"ignore": true} verwerfen.
func (s *Server) adminResolvePending(w http.ResponseWriter, r *http.Request) {
	matchID, ok := pathID(r, "match")
	idx, err := strconv.Atoi(r.PathValue("index"))
	if !ok || err != nil || idx < 0 {
		writeError(w, 400, "ungueltige Angaben")
		return
	}
	var body struct {
		PlayerID int64 `json:"player_id"`
		Ignore   bool  `json:"ignore"`
	}
	if !readJSON(w, r, &body, 1024) {
		return
	}
	if !body.Ignore && body.PlayerID <= 0 {
		writeError(w, 400, "player_id oder ignore angeben")
		return
	}
	if body.Ignore {
		body.PlayerID = 0
	}
	if err := s.Ingest.ResolvePending(r.Context(), matchID, idx, body.PlayerID); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// adminReassign: bestehenden Match-Slot einem anderen Spieler zuordnen oder
// ignorieren (nachtraegliches Aufraeumen).
func (s *Server) adminReassign(w http.ResponseWriter, r *http.Request) {
	s.adminResolvePending(w, r)
}
