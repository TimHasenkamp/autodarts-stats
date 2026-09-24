package api

import (
	"errors"
	"net/http"

	"autodarts-stats/internal/tournament"
)

func (s *Server) tournamentError(w http.ResponseWriter, err error) {
	var ie tournament.InputError
	switch {
	case errors.Is(err, tournament.ErrNotFound):
		writeError(w, 404, err.Error())
	case errors.As(err, &ie):
		writeError(w, 400, ie.Error())
	default:
		writeError(w, 500, err.Error())
	}
}

// ---- Public ----

func (s *Server) handleTournaments(w http.ResponseWriter, r *http.Request) {
	list, err := s.Tournaments.List(r.Context())
	if err != nil {
		s.tournamentError(w, err)
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
		s.tournamentError(w, err)
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
		s.tournamentError(w, err)
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
		s.tournamentError(w, err)
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
	s.tournamentOK(w, s.Tournaments.Update(r.Context(), id, in))
}

func (s *Server) adminTournamentAction(action func(r *http.Request, id int64) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r, "id")
		if !ok {
			writeError(w, 400, "ungueltige ID")
			return
		}
		s.tournamentOK(w, action(r, id))
	}
}

func (s *Server) tournamentOK(w http.ResponseWriter, err error) {
	if err != nil {
		s.tournamentError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// adminTournamentResult: {"match_id": n} verknuepft ein erfasstes Match,
// sonst {"winner_id", "legs1", "legs2"} (Legs in der Reihenfolge der Paarung).
func (s *Server) adminTournamentResult(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, 400, "ungueltige ID")
		return
	}
	var body struct {
		MatchID  int64 `json:"match_id"`
		WinnerID int64 `json:"winner_id"`
		Legs1    int   `json:"legs1"`
		Legs2    int   `json:"legs2"`
	}
	if !readJSON(w, r, &body, 4096) {
		return
	}
	key := r.PathValue("key")
	if body.MatchID > 0 {
		s.tournamentOK(w, s.Tournaments.LinkMatch(r.Context(), id, key, body.MatchID))
		return
	}
	s.tournamentOK(w, s.Tournaments.SetResult(r.Context(), id, key, body.WinnerID, body.Legs1, body.Legs2))
}

func (s *Server) adminTournamentCandidates(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, 400, "ungueltige ID")
		return
	}
	list, err := s.Tournaments.Candidates(r.Context(), id, r.PathValue("key"))
	if err != nil {
		s.tournamentError(w, err)
		return
	}
	writeJSON(w, 200, list)
}
