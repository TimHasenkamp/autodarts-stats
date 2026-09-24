package tournament

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type Summary struct {
	ID         int64       `json:"id"`
	Name       string      `json:"name"`
	Status     string      `json:"status"`
	CreatedAt  string      `json:"created_at"`
	StartedAt  string      `json:"started_at,omitempty"`
	FinishedAt string      `json:"finished_at,omitempty"`
	Players    int         `json:"players"`
	Champion   *PlayerName `json:"champion,omitempty"`
}

type PlayerName struct {
	PlayerID    int64  `json:"player_id"`
	DisplayName string `json:"display_name"`
}

type Side struct {
	PlayerID    int64  `json:"player_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	// Placeholder beschreibt einen offenen Platz ("Sieger Spiel 3", "Freilos").
	Placeholder string `json:"placeholder,omitempty"`
	Legs        *int   `json:"legs,omitempty"`
	Won         bool   `json:"won"`
}

type MatchView struct {
	Key     string `json:"key"`
	No      int    `json:"no"`
	Bracket string `json:"bracket"`
	Round   int    `json:"round"`
	Status  string `json:"status"`
	Rule    Rule   `json:"rule"`
	A       Side   `json:"a"`
	B       Side   `json:"b"`
	MatchID int64  `json:"match_id,omitempty"`
	Source  string `json:"source,omitempty"`
	Warning string `json:"warning,omitempty"`
	// Target beschreibt, wohin der Sieger kommt (nur beim Zusatzspiel).
	Target string `json:"target,omitempty"`
}

type RoundView struct {
	Name    string      `json:"name"`
	Rule    Rule        `json:"rule"`
	Matches []MatchView `json:"matches"`
}

type Participant struct {
	PlayerID    int64  `json:"player_id"`
	DisplayName string `json:"display_name"`
	Placement
}

type View struct {
	Summary
	ThirdPlace  bool          `json:"third_place"`
	LuckyLoser  bool          `json:"lucky_loser"`
	LLEntry     int           `json:"ll_entry"`
	LLEntryName string        `json:"ll_entry_name,omitempty"`
	Rules       Rules         `json:"rules"`
	RoundNames  []string      `json:"round_names"` // Hauptrunden fuer die aktuelle Teilnehmerzahl
	Main        []RoundView   `json:"main"`
	LuckyRounds []RoundView   `json:"lucky_rounds"`
	Playin      *MatchView    `json:"playin,omitempty"`
	Third       *MatchView    `json:"third,omitempty"`
	Next        []MatchView   `json:"next"` // spielbereite Paarungen
	Standings   []Participant `json:"standings"`
	Stats       *Stats        `json:"stats"`
}

func playerNames(ctx context.Context, q querier, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(ids) == 0 {
		return out, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := q.QueryContext(ctx, `SELECT id, display_name FROM players WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n string
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func (s *Service) List(ctx context.Context) ([]Summary, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM tournaments ORDER BY COALESCE(started_at, created_at) DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := []Summary{}
	for _, id := range ids {
		t, err := load(ctx, s.DB, id)
		if err != nil {
			return nil, err
		}
		sum, _, err := s.summary(ctx, t)
		if err != nil {
			return nil, err
		}
		out = append(out, sum)
	}
	return out, nil
}

func (s *Service) summary(ctx context.Context, t *tournament) (Summary, *evaluation, error) {
	sum := Summary{ID: t.ID, Name: t.Name, Status: t.Status, CreatedAt: t.CreatedAt, StartedAt: t.StartedAt, FinishedAt: t.FinishedAt, Players: len(t.Players)}
	if t.Status == StateDraft {
		return sum, nil, nil
	}
	ev, err := evalTournament(ctx, s.DB, t)
	if err != nil {
		return sum, nil, err
	}
	if w := ev.final().winner; w.state == slotKnown {
		names, err := playerNames(ctx, s.DB, []int64{w.player})
		if err != nil {
			return sum, nil, err
		}
		sum.Champion = &PlayerName{PlayerID: w.player, DisplayName: names[w.player]}
	}
	return sum, ev, nil
}

func (s *Service) Get(ctx context.Context, id int64) (*View, error) {
	t, err := load(ctx, s.DB, id)
	if err != nil {
		return nil, err
	}
	sum, ev, err := s.summary(ctx, t)
	if err != nil {
		return nil, err
	}
	names, err := playerNames(ctx, s.DB, t.Players)
	if err != nil {
		return nil, err
	}
	v := &View{Summary: sum, ThirdPlace: t.ThirdPlace, LuckyLoser: t.LuckyLoser, LLEntry: t.LLEntry, Rules: t.Rules,
		RoundNames: []string{}, Main: []RoundView{}, LuckyRounds: []RoundView{}, Next: []MatchView{}, Standings: []Participant{}}
	rounds := RoundCount(len(t.Players))
	for r := 1; r <= rounds; r++ {
		v.RoundNames = append(v.RoundNames, RoundName(rounds, r))
	}
	if e := EntryRound(rounds, t.LLEntry); t.LuckyLoser && e > 0 {
		v.LLEntryName = RoundName(rounds, e)
	}
	if ev == nil {
		for _, pid := range t.Players {
			v.Standings = append(v.Standings, Participant{PlayerID: pid, DisplayName: names[pid], Placement: Placement{Label: "angemeldet"}})
		}
		return v, nil
	}
	L := ev.L
	mv := func(nd *node) MatchView { return matchView(ev, nd, names) }
	for r, round := range L.main {
		rv := RoundView{Name: RoundName(L.rounds, r+1), Rule: round[0].rule}
		for _, nd := range round {
			rv.Matches = append(rv.Matches, mv(nd))
		}
		v.Main = append(v.Main, rv)
	}
	for r, round := range L.ll {
		rv := RoundView{Name: fmt.Sprintf("LL-Runde %d", r+1), Rule: round[0].rule}
		if r == len(L.ll)-1 {
			rv.Name = "LL-Finale"
		}
		for _, nd := range round {
			rv.Matches = append(rv.Matches, mv(nd))
		}
		v.LuckyRounds = append(v.LuckyRounds, rv)
	}
	if L.playin != nil {
		p := mv(L.playin)
		p.Target = RoundName(L.rounds, L.entry)
		v.Playin = &p
	}
	if L.third != nil {
		th := mv(L.third)
		v.Third = &th
	}
	for _, m := range ev.ready() {
		v.Next = append(v.Next, mv(m.node))
	}
	pl := ev.placements(t.Players)
	for _, pid := range ranking(t.Players, pl) {
		v.Standings = append(v.Standings, Participant{PlayerID: pid, DisplayName: names[pid], Placement: pl[pid]})
	}
	v.Stats, err = s.stats(ctx, t.ID, ev, names)
	if err != nil {
		return nil, err
	}
	return v, nil
}

func matchView(ev *evaluation, nd *node, names map[int64]string) MatchView {
	m := ev.memo[nd.key]
	out := MatchView{Key: nd.key, No: nd.no, Bracket: nd.bracket, Round: nd.round, Status: m.status, Rule: nd.rule}
	side := func(src source, sl slot) Side {
		sd := Side{}
		switch sl.state {
		case slotKnown:
			sd.PlayerID, sd.DisplayName = sl.player, names[sl.player]
		case slotNone:
			sd.Placeholder = "Freilos"
			if src.kind != srcBye {
				sd.Placeholder = "–"
			}
		default:
			sd.Placeholder = placeholder(ev.L, src)
		}
		return sd
	}
	out.A, out.B = side(nd.a, m.a), side(nd.b, m.b)
	if r := m.result; r != nil {
		la, lb := r.Legs1, r.Legs2
		if r.Player1 != m.a.player {
			la, lb = lb, la
		}
		out.A.Legs, out.B.Legs = &la, &lb
		out.A.Won, out.B.Won = r.Winner == m.a.player, r.Winner == m.b.player
		out.MatchID, out.Source, out.Warning = r.MatchID, r.Source, r.Warning
	}
	if m.status == StatusWalkover {
		out.A.Won, out.B.Won = m.a.state == slotKnown, m.b.state == slotKnown
	}
	return out
}

func placeholder(L *layout, src source) string {
	nd := L.nodes[src.key]
	if nd == nil {
		return "offen"
	}
	switch {
	case nd.bracket == BracketPlayin:
		return "Sieger Zusatzspiel"
	case src.kind == srcWinner && nd.bracket == BracketLL && nd.key == L.ll[len(L.ll)-1][0].key:
		return "Lucky Loser"
	case src.kind == srcWinner:
		return fmt.Sprintf("Sieger Spiel %d", nd.no)
	default:
		return fmt.Sprintf("Verlierer Spiel %d", nd.no)
	}
}

// ---- Statistik ----

type StatRow struct {
	PlayerID        int64    `json:"player_id"`
	DisplayName     string   `json:"display_name"`
	Matches         int      `json:"matches"`
	Wins            int      `json:"wins"`
	LegsWon         int      `json:"legs_won"`
	LegsLost        int      `json:"legs_lost"`
	Average         *float64 `json:"average"`
	BestMatchAvg    *float64 `json:"best_match_average"`
	Count180        int      `json:"count_180"`
	Count140Plus    int      `json:"count_140plus"`
	Count100Plus    int      `json:"count_100plus"`
	HighestCheckout int      `json:"highest_checkout"`
}

type Leader struct {
	PlayerID    int64   `json:"player_id"`
	DisplayName string  `json:"display_name"`
	Value       float64 `json:"value"`
}

type Stats struct {
	Rows             []StatRow `json:"rows"`
	BestAverage      *Leader   `json:"best_average"`
	BestMatchAverage *Leader   `json:"best_match_average"`
	Most180          *Leader   `json:"most_180"`
	HighestCheckout  *Leader   `json:"highest_checkout"`
}

// stats: Siege und Legs aus den Turnierergebnissen (auch von Hand
// eingetragene), Wurfstatistik aus den verknuepften Matches.
func (s *Service) stats(ctx context.Context, id int64, ev *evaluation, names map[int64]string) (*Stats, error) {
	rows := map[int64]*StatRow{}
	get := func(pid int64) *StatRow {
		r, ok := rows[pid]
		if !ok {
			r = &StatRow{PlayerID: pid, DisplayName: names[pid]}
			rows[pid] = r
		}
		return r
	}
	for _, nd := range ev.L.all {
		m := ev.memo[nd.key]
		if m.result == nil {
			continue
		}
		res := m.result
		for _, p := range []struct {
			pid        int64
			legs, lost int
		}{{res.Player1, res.Legs1, res.Legs2}, {res.Player2, res.Legs2, res.Legs1}} {
			r := get(p.pid)
			r.Matches++
			r.LegsWon += p.legs
			r.LegsLost += p.lost
			if res.Winner == p.pid {
				r.Wins++
			}
		}
	}
	// Nur Matches, deren Ergebnis im aktuellen Baum gilt.
	var mids []any
	for _, m := range ev.memo {
		if m.result != nil && m.result.MatchID > 0 {
			mids = append(mids, m.result.MatchID)
		}
	}
	if len(mids) > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(mids)), ",")
		lr, err := s.DB.QueryContext(ctx, `SELECT l.player_id, SUM(l.points), SUM(CASE WHEN l.points IS NOT NULL THEN l.darts ELSE 0 END),
			SUM(l.count_180), SUM(l.count_140plus), SUM(l.count_100plus), MAX(l.checkout)
			FROM match_legs l WHERE l.match_id IN (`+ph+`) GROUP BY l.player_id`, mids...)
		if err != nil {
			return nil, err
		}
		for lr.Next() {
			var pid int64
			var points sql.NullInt64
			var darts int
			var c180, c140, c100, hco int
			if err := lr.Scan(&pid, &points, &darts, &c180, &c140, &c100, &hco); err != nil {
				lr.Close()
				return nil, err
			}
			if _, ok := names[pid]; !ok {
				continue
			}
			r := get(pid)
			if points.Valid && darts > 0 {
				avg := float64(points.Int64) * 3 / float64(darts)
				r.Average = &avg
			}
			r.Count180, r.Count140Plus, r.Count100Plus, r.HighestCheckout = c180, c140, c100, hco
		}
		lr.Close()
		mr, err := s.DB.QueryContext(ctx, `SELECT player_id, MAX(average) FROM match_players WHERE match_id IN (`+ph+`) AND average IS NOT NULL GROUP BY player_id`, mids...)
		if err != nil {
			return nil, err
		}
		for mr.Next() {
			var pid int64
			var best float64
			if err := mr.Scan(&pid, &best); err != nil {
				mr.Close()
				return nil, err
			}
			if _, ok := names[pid]; ok {
				b := best
				get(pid).BestMatchAvg = &b
			}
		}
		mr.Close()
	}
	st := &Stats{Rows: []StatRow{}}
	lead := func(cur **Leader, r *StatRow, v float64) {
		if v > 0 && (*cur == nil || v > (*cur).Value) {
			*cur = &Leader{PlayerID: r.PlayerID, DisplayName: r.DisplayName, Value: v}
		}
	}
	for _, pid := range ev.L.orderPlayers() {
		r, ok := rows[pid]
		if !ok {
			continue
		}
		st.Rows = append(st.Rows, *r)
		if r.Average != nil {
			lead(&st.BestAverage, r, *r.Average)
		}
		if r.BestMatchAvg != nil {
			lead(&st.BestMatchAverage, r, *r.BestMatchAvg)
		}
		lead(&st.Most180, r, float64(r.Count180))
		lead(&st.HighestCheckout, r, float64(r.HighestCheckout))
	}
	return st, nil
}

// orderPlayers liefert die Spieler in Auslosungsreihenfolge.
func (L *layout) orderPlayers() []int64 {
	var out []int64
	for _, nd := range L.main[0] {
		for _, s := range []source{nd.a, nd.b} {
			if s.kind == srcPlayer {
				out = append(out, s.player)
			}
		}
	}
	return out
}

// ---- Spielerprofil ----

type PlayerEntry struct {
	TournamentID int64  `json:"tournament_id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	StartedAt    string `json:"started_at,omitempty"`
	Players      int    `json:"players"`
	Placement
	Matches int `json:"matches"`
	Wins    int `json:"wins"`
}

type PlayerHistory struct {
	Tournaments []PlayerEntry `json:"tournaments"`
	Wins        int           `json:"wins"`   // Turniersiege
	Podium      int           `json:"podium"` // Platz 1-3
}

// ForPlayer listet die gestarteten Turniere eines Spielers, neueste zuerst.
func (s *Service) ForPlayer(ctx context.Context, playerID int64) (*PlayerHistory, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT t.id FROM tournaments t JOIN tournament_players tp ON tp.tournament_id = t.id
		WHERE tp.player_id = ? AND t.status <> ? ORDER BY t.started_at DESC, t.id DESC`, playerID, StateDraft)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	h := &PlayerHistory{Tournaments: []PlayerEntry{}}
	for _, id := range ids {
		t, err := load(ctx, s.DB, id)
		if err != nil {
			return nil, err
		}
		ev, err := evalTournament(ctx, s.DB, t)
		if err != nil {
			return nil, err
		}
		e := PlayerEntry{TournamentID: id, Name: t.Name, Status: t.Status, StartedAt: t.StartedAt, Players: len(t.Players),
			Placement: ev.placements([]int64{playerID})[playerID]}
		for _, m := range ev.memo {
			if r := m.result; r != nil && (r.Player1 == playerID || r.Player2 == playerID) {
				e.Matches++
				if r.Winner == playerID {
					e.Wins++
				}
			}
		}
		if e.Place == 1 {
			h.Wins++
		}
		if e.Place >= 1 && e.Place <= 3 {
			h.Podium++
		}
		h.Tournaments = append(h.Tournaments, e)
	}
	return h, nil
}
