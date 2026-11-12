package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"autodarts-stats/internal/tournament"
)

// conflictBody: 409 mit den Paarungen, deren Ergebnisse verfallen wuerden.
type conflictBody struct {
	Error     string                `json:"error"`
	Affected  []tournament.Affected `json:"affected"`
	Forceable bool                  `json:"forceable"`
}

func (s *Server) tournamentError(w http.ResponseWriter, r *http.Request, err error) {
	var ie tournament.InputError
	var ce *tournament.ConflictError
	switch {
	case errors.Is(err, tournament.ErrNotFound):
		writeError(w, 404, err.Error())
	case errors.As(err, &ie):
		writeError(w, 400, ie.Error())
	case errors.As(err, &ce):
		writeJSON(w, 409, conflictBody{Error: ce.Msg, Affected: ce.Affected, Forceable: ce.Forceable})
	default:
		internalError(w, r, err)
	}
}

// forced: ?force=1 bestaetigt, dass spaetere Ergebnisse verfallen.
func forced(r *http.Request) bool {
	v := r.URL.Query().Get("force")
	return v == "1" || v == "true"
}

// ---- Public ----

func (s *Server) handleTournaments(w http.ResponseWriter, r *http.Request) {
	list, err := s.Tournaments.List(r.Context())
	if err != nil {
		s.tournamentError(w, r, err)
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) handleTournament(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, 400, "ungueltige ID")
		return
	}
	v, err := s.Tournaments.Get(r.Context(), id)
	if err != nil {
		s.tournamentError(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) handlePlayerTournaments(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, 400, "ungueltige ID")
		return
	}
	h, err := s.Tournaments.ForPlayer(r.Context(), id)
	if err != nil {
		s.tournamentError(w, r, err)
		return
	}
	writeJSON(w, 200, h)
}

// ---- Admin ----

func (s *Server) adminCreateTournament(w http.ResponseWriter, r *http.Request) {
	var in tournament.Input
	if !readJSON(w, r, &in, 64<<10) {
		return
	}
	id, err := s.Tournaments.Create(r.Context(), in)
	if err != nil {
		s.tournamentError(w, r, err)
		return
	}
	writeJSON(w, 201, map[string]int64{"id": id})
}

func (s *Server) adminUpdateTournament(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, 400, "ungueltige ID")
		return
	}
	var in tournament.Input
	if !readJSON(w, r, &in, 64<<10) {
		return
	}
	s.tournamentOK(w, r, s.Tournaments.Update(r.Context(), id, in))
}

func (s *Server) adminTournamentAction(action func(r *http.Request, id int64) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r, "id")
		if !ok {
			writeError(w, 400, "ungueltige ID")
			return
		}
		s.tournamentOK(w, r, action(r, id))
	}
}

func (s *Server) tournamentOK(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		s.tournamentError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// adminTournamentResult: {"match_id": n} verknuepft ein erfasstes Match,
// sonst {"winner_id", "legs1", "legs2"} (Legs in der Reihenfolge der Paarung),
// optional "outcome": "retired" (Aufgabe) | "dq" (Disqualifikation) des
// Verlierers. Verfallen dadurch spaetere Ergebnisse, braucht es ?force=1.
func (s *Server) adminTournamentResult(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, 400, "ungueltige ID")
		return
	}
	var body struct {
		MatchID  int64  `json:"match_id"`
		WinnerID int64  `json:"winner_id"`
		Legs1    int    `json:"legs1"`
		Legs2    int    `json:"legs2"`
		Outcome  string `json:"outcome"`
	}
	if !readJSON(w, r, &body, 4096) {
		return
	}
	key := r.PathValue("key")
	if body.MatchID > 0 {
		s.tournamentOK(w, r, s.Tournaments.LinkMatch(r.Context(), id, key, body.MatchID, forced(r)))
		return
	}
	s.tournamentOK(w, r, s.Tournaments.SetResult(r.Context(), id, key, body.WinnerID, body.Legs1, body.Legs2,
		tournament.ResultOpts{Outcome: body.Outcome, Force: forced(r)}))
}

func (s *Server) adminTournamentCandidates(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, 400, "ungueltige ID")
		return
	}
	list, err := s.Tournaments.Candidates(r.Context(), id, r.PathValue("key"))
	if err != nil {
		s.tournamentError(w, r, err)
		return
	}
	writeJSON(w, 200, list)
}

// ---- Teilnehmer im laufenden Turnier ----

// tournamentPlayer liest Turnier-ID und Spieler-ID aus dem Pfad.
func tournamentPlayer(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	id, ok := pathID(r, "id")
	pid, ok2 := pathID(r, "pid")
	if !ok || !ok2 {
		writeError(w, 400, "ungueltige ID")
		return 0, 0, false
	}
	return id, pid, true
}

// newPlayerBody: vorhandener Spieler (player_id) oder neuer Name (new_name).
type newPlayerBody struct {
	PlayerID int64  `json:"player_id"`
	NewName  string `json:"new_name"`
}

// adminTournamentReplace: Ersatzspieler fuer {pid}.
func (s *Server) adminTournamentReplace(w http.ResponseWriter, r *http.Request) {
	id, pid, ok := tournamentPlayer(w, r)
	if !ok {
		return
	}
	var body newPlayerBody
	if !readJSON(w, r, &body, 4096) {
		return
	}
	s.tournamentOK(w, r, s.Tournaments.Replace(r.Context(), id, pid, body.PlayerID, body.NewName))
}

// adminTournamentSwap: {"with": andere Spieler-ID} tauscht die Plaetze.
func (s *Server) adminTournamentSwap(w http.ResponseWriter, r *http.Request) {
	id, pid, ok := tournamentPlayer(w, r)
	if !ok {
		return
	}
	var body struct {
		With int64 `json:"with"`
	}
	if !readJSON(w, r, &body, 4096) {
		return
	}
	s.tournamentOK(w, r, s.Tournaments.Swap(r.Context(), id, pid, body.With))
}

// adminTournamentWithdraw: {"reason": "retired" | "dq"} zieht {pid} zurueck.
func (s *Server) adminTournamentWithdraw(w http.ResponseWriter, r *http.Request) {
	id, pid, ok := tournamentPlayer(w, r)
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if !readJSON(w, r, &body, 4096) {
		return
	}
	s.tournamentOK(w, r, s.Tournaments.Withdraw(r.Context(), id, pid, body.Reason))
}

func (s *Server) adminTournamentUndoWithdraw(w http.ResponseWriter, r *http.Request) {
	id, pid, ok := tournamentPlayer(w, r)
	if !ok {
		return
	}
	s.tournamentOK(w, r, s.Tournaments.UndoWithdraw(r.Context(), id, pid))
}

// adminTournamentLate: Nachmelder auf das Freilos der Paarung {key}.
func (s *Server) adminTournamentLate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, 400, "ungueltige ID")
		return
	}
	var body newPlayerBody
	if !readJSON(w, r, &body, 4096) {
		return
	}
	s.tournamentOK(w, r, s.Tournaments.LateEntry(r.Context(), id, r.PathValue("key"), body.PlayerID, body.NewName))
}

// ---- Schutz fuer Spieler in Turnieren (Loeschen/Zusammenfuehren) ----

// tournamentOf liefert den Namen eines gestarteten Turniers, in dem pid steht
// oder stand (Teilnehmer, ersetzter Spieler oder Ergebnis); "" wenn keins.
func tournamentOf(ctx context.Context, tx *sql.Tx, pid int64) (string, error) {
	var name string
	err := tx.QueryRowContext(ctx, `SELECT t.name FROM tournaments t WHERE t.status <> 'draft' AND (
			EXISTS (SELECT 1 FROM tournament_players tp WHERE tp.tournament_id = t.id AND tp.player_id = ?1)
			OR EXISTS (SELECT 1 FROM tournament_former tf WHERE tf.tournament_id = t.id AND (tf.player_id = ?1 OR tf.replaced_by = ?1))
			OR EXISTS (SELECT 1 FROM tournament_results r WHERE r.tournament_id = t.id AND ?1 IN (r.player1_id, r.player2_id, r.winner_id)))
		ORDER BY t.id LIMIT 1`, pid).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}

// sharedTournament liefert ein Turnier (auch Entwurf), in dem a und b beide stehen.
func sharedTournament(ctx context.Context, tx *sql.Tx, a, b int64) (string, error) {
	var name string
	err := tx.QueryRowContext(ctx, `SELECT t.name FROM tournaments t
		JOIN tournament_players x ON x.tournament_id = t.id AND x.player_id = ?
		JOIN tournament_players y ON y.tournament_id = t.id AND y.player_id = ?
		ORDER BY t.id LIMIT 1`, a, b).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}
