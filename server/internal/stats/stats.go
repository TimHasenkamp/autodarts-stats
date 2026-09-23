// Package stats berechnet Aggregate aus match_players und match_legs.
//
// Wurfstatistiken (Average, 180er, Checkouts) kommen aus match_legs, damit
// auch Legs abgebrochener Matches zaehlen. Siege/Matches nur aus beendeten
// Matches.
package stats

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"
)

var ErrNotFound = errors.New("nicht gefunden")

type Filter struct {
	From       time.Time // zero = offen
	To         time.Time // zero = offen (exklusiv)
	Variant    string    // "" = alle
	MinMatches int
	Sort       string // average | wins | count_180 | checkout | matches | win_rate | highest_checkout
	Order      string // asc | desc
}

type PlayerRow struct {
	PlayerID        int64    `json:"player_id"`
	DisplayName     string   `json:"display_name"`
	Matches         int      `json:"matches"`
	Wins            int      `json:"wins"`
	WinRate         *float64 `json:"win_rate"`
	Average         *float64 `json:"average"`
	BestMatchAvg    *float64 `json:"best_match_average"`
	First9Avg       *float64 `json:"first9_avg"`
	CheckoutRate    *float64 `json:"checkout_rate"`
	HighestCheckout int      `json:"highest_checkout"`
	Count180        int      `json:"count_180"`
	Count140Plus    int      `json:"count_140plus"`
	Count100Plus    int      `json:"count_100plus"`
	LegsWon         int      `json:"legs_won"`
	LegsPlayed      int      `json:"legs_played"`
	Darts           int      `json:"darts"`
	LastPlayedAt    string   `json:"last_played_at,omitempty"`
}

type Service struct{ DB *sql.DB }

func New(db *sql.DB) *Service { return &Service{DB: db} }

func fmtTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// filterArgs liefert eine WHERE-Klausel fuer den Alias mt (matches).
func filterClause(f Filter) (string, []any) {
	var parts []string
	var args []any
	if !f.From.IsZero() {
		parts = append(parts, "mt.played_at >= ?")
		args = append(args, fmtTime(f.From))
	}
	if !f.To.IsZero() {
		parts = append(parts, "mt.played_at < ?")
		args = append(args, fmtTime(f.To))
	}
	if f.Variant != "" {
		parts = append(parts, "mt.variant = ?")
		args = append(args, f.Variant)
	}
	if len(parts) == 0 {
		return "1=1", nil
	}
	return strings.Join(parts, " AND "), args
}

const rowsQuery = `
WITH mp AS (
  SELECT mp.player_id,
         COUNT(*) AS matches,
         SUM(mp.won) AS wins,
         MAX(mp.average) AS best_avg,
         AVG(mp.checkout_rate) AS co_rate_avg,
         MAX(mt.played_at) AS last_played
  FROM match_players mp JOIN matches mt ON mt.id = mp.match_id
  WHERE mt.finished = 1 AND %[1]s
  GROUP BY mp.player_id
),
lg AS (
  SELECT l.player_id,
         COUNT(*) AS legs_played,
         SUM(l.won) AS legs_won,
         SUM(l.points) AS points,
         SUM(CASE WHEN l.points IS NOT NULL THEN l.darts ELSE 0 END) AS sdarts,
         SUM(l.darts) AS darts,
         SUM(l.first9_avg * 9) AS f9points,
         SUM(CASE WHEN l.first9_avg IS NOT NULL THEN MIN(l.darts, 9) ELSE 0 END) AS f9darts,
         SUM(l.count_180) AS c180, SUM(l.count_140plus) AS c140, SUM(l.count_100plus) AS c100,
         SUM(l.checkouts_hit) AS co_hit, SUM(l.checkout_attempts) AS co_att,
         MAX(l.checkout) AS hco
  FROM match_legs l JOIN matches mt ON mt.id = l.match_id
  WHERE %[1]s
  GROUP BY l.player_id
)
SELECT p.id, p.display_name,
       COALESCE(mp.matches,0), COALESCE(mp.wins,0), mp.best_avg, mp.co_rate_avg, COALESCE(mp.last_played,''),
       COALESCE(lg.legs_played,0), COALESCE(lg.legs_won,0), lg.points, COALESCE(lg.sdarts,0), COALESCE(lg.darts,0),
       lg.f9points, COALESCE(lg.f9darts,0),
       COALESCE(lg.c180,0), COALESCE(lg.c140,0), COALESCE(lg.c100,0),
       COALESCE(lg.co_hit,0), COALESCE(lg.co_att,0), COALESCE(lg.hco,0)
FROM players p
LEFT JOIN mp ON mp.player_id = p.id
LEFT JOIN lg ON lg.player_id = p.id
WHERE %[2]s
`

func (s *Service) rows(ctx context.Context, f Filter, extraWhere string, extraArgs ...any) ([]PlayerRow, error) {
	clause, args := filterClause(f)
	q := strings.Replace(strings.Replace(rowsQuery, "%[1]s", clause, 2), "%[2]s", extraWhere, 1)
	all := append(append(append([]any{}, args...), args...), extraArgs...)
	rs, err := s.DB.QueryContext(ctx, q, all...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []PlayerRow
	for rs.Next() {
		var r PlayerRow
		var bestAvg, coRateAvg, f9points sql.NullFloat64
		var points sql.NullInt64
		var sdarts, f9darts, coHit, coAtt int
		if err := rs.Scan(&r.PlayerID, &r.DisplayName, &r.Matches, &r.Wins, &bestAvg, &coRateAvg, &r.LastPlayedAt,
			&r.LegsPlayed, &r.LegsWon, &points, &sdarts, &r.Darts, &f9points, &f9darts,
			&r.Count180, &r.Count140Plus, &r.Count100Plus, &coHit, &coAtt, &r.HighestCheckout); err != nil {
			return nil, err
		}
		if r.Matches > 0 {
			wr := float64(r.Wins) / float64(r.Matches)
			r.WinRate = &wr
		}
		if bestAvg.Valid {
			r.BestMatchAvg = &bestAvg.Float64
		}
		if points.Valid && sdarts > 0 {
			avg := float64(points.Int64) * 3 / float64(sdarts)
			r.Average = &avg
		}
		if f9points.Valid && f9darts > 0 {
			f9 := f9points.Float64 / float64(f9darts)
			r.First9Avg = &f9
		}
		switch {
		case coAtt > 0:
			cr := float64(coHit) / float64(coAtt)
			r.CheckoutRate = &cr
		case coRateAvg.Valid:
			r.CheckoutRate = &coRateAvg.Float64
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

func (s *Service) Leaderboard(ctx context.Context, f Filter) ([]PlayerRow, error) {
	rows, err := s.rows(ctx, f, "COALESCE(mp.matches,0) >= ? AND (COALESCE(mp.matches,0) > 0 OR COALESCE(lg.legs_played,0) > 0)", f.MinMatches)
	if err != nil {
		return nil, err
	}
	sortRows(rows, f.Sort, f.Order)
	return rows, nil
}

func deref(p *float64) float64 {
	if p == nil {
		return -1
	}
	return *p
}

func sortRows(rows []PlayerRow, key, order string) {
	less := func(a, b PlayerRow) bool {
		switch key {
		case "wins":
			return a.Wins < b.Wins
		case "win_rate":
			return deref(a.WinRate) < deref(b.WinRate)
		case "matches":
			return a.Matches < b.Matches
		case "count_180":
			return a.Count180 < b.Count180
		case "checkout":
			return deref(a.CheckoutRate) < deref(b.CheckoutRate)
		case "highest_checkout":
			return a.HighestCheckout < b.HighestCheckout
		case "first9":
			return deref(a.First9Avg) < deref(b.First9Avg)
		case "name":
			return strings.ToLower(a.DisplayName) < strings.ToLower(b.DisplayName)
		default:
			return deref(a.Average) < deref(b.Average)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if order == "asc" {
			return less(rows[i], rows[j])
		}
		return less(rows[j], rows[i])
	})
}

type MatchSummary struct {
	MatchID   int64             `json:"match_id"`
	PlayedAt  string            `json:"played_at"`
	Variant   string            `json:"variant"`
	Finished  bool              `json:"finished"`
	BoardName string            `json:"board_name,omitempty"`
	Players   []MatchPlayerInfo `json:"players"`
}

type MatchPlayerInfo struct {
	PlayerID        int64    `json:"player_id"`
	DisplayName     string   `json:"display_name"`
	Won             bool     `json:"won"`
	Average         *float64 `json:"average"`
	First9Avg       *float64 `json:"first9_avg"`
	CheckoutRate    *float64 `json:"checkout_rate"`
	HighestCheckout int      `json:"highest_checkout"`
	Count180        int      `json:"count_180"`
	Count140Plus    int      `json:"count_140plus"`
	Count100Plus    int      `json:"count_100plus"`
	LegsWon         int      `json:"legs_won"`
	LegsPlayed      int      `json:"legs_played"`
}

// matches liefert Matchzusammenfassungen fuer eine Menge von Match-IDs (Query liefert IDs).
func (s *Service) matches(ctx context.Context, idQuery string, args ...any) ([]MatchSummary, error) {
	rs, err := s.DB.QueryContext(ctx, `SELECT mt.id, mt.played_at, mt.variant, mt.finished, COALESCE(b.name,'')
		FROM matches mt LEFT JOIN boards b ON b.id = mt.board_id WHERE mt.id IN (`+idQuery+`) ORDER BY mt.played_at DESC, mt.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	var out []MatchSummary
	idx := map[int64]int{}
	for rs.Next() {
		var m MatchSummary
		if err := rs.Scan(&m.MatchID, &m.PlayedAt, &m.Variant, &m.Finished, &m.BoardName); err != nil {
			rs.Close()
			return nil, err
		}
		idx[m.MatchID] = len(out)
		out = append(out, m)
	}
	rs.Close()
	if len(out) == 0 {
		return []MatchSummary{}, nil
	}
	prs, err := s.DB.QueryContext(ctx, `SELECT mp.match_id, mp.player_id, p.display_name, mp.won, mp.average, mp.first9_avg, mp.checkout_rate, mp.highest_checkout, mp.count_180, mp.count_140plus, mp.count_100plus, mp.legs_won, mp.legs_played
		FROM match_players mp JOIN players p ON p.id = mp.player_id WHERE mp.match_id IN (`+idQuery+`) ORDER BY mp.match_id, mp.player_index`, args...)
	if err != nil {
		return nil, err
	}
	defer prs.Close()
	for prs.Next() {
		var mid int64
		var pi MatchPlayerInfo
		var avg, f9, cr sql.NullFloat64
		if err := prs.Scan(&mid, &pi.PlayerID, &pi.DisplayName, &pi.Won, &avg, &f9, &cr, &pi.HighestCheckout, &pi.Count180, &pi.Count140Plus, &pi.Count100Plus, &pi.LegsWon, &pi.LegsPlayed); err != nil {
			return nil, err
		}
		if avg.Valid {
			pi.Average = &avg.Float64
		}
		if f9.Valid {
			pi.First9Avg = &f9.Float64
		}
		if cr.Valid {
			pi.CheckoutRate = &cr.Float64
		}
		if i, ok := idx[mid]; ok {
			out[i].Players = append(out[i].Players, pi)
		}
	}
	return out, prs.Err()
}

type HistoryPoint struct {
	MatchID  int64    `json:"match_id"`
	PlayedAt string   `json:"played_at"`
	Average  *float64 `json:"average"`
	Won      bool     `json:"won"`
}

type Profile struct {
	Player       PlayerRow      `json:"player"`
	Aliases      []string       `json:"aliases"`
	HasAccount   bool           `json:"has_account"`
	BestLegAvg   *float64       `json:"best_leg_average"`
	BestLegDarts int            `json:"best_leg_darts"` // wenigste Darts fuer ein gewonnenes Leg
	Most180Match int            `json:"most_180_match"`
	Variants     []VariantCount `json:"variants"`
	History      []HistoryPoint `json:"history"`
	Recent       []MatchSummary `json:"recent_matches"`
}

type VariantCount struct {
	Variant string `json:"variant"`
	Matches int    `json:"matches"`
	Wins    int    `json:"wins"`
}

func (s *Service) Profile(ctx context.Context, playerID int64, f Filter) (*Profile, error) {
	rows, err := s.rows(ctx, f, "p.id = ?", playerID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrNotFound
	}
	pr := &Profile{Player: rows[0], Aliases: []string{}, Variants: []VariantCount{}, History: []HistoryPoint{}}
	var uid sql.NullString
	if err := s.DB.QueryRowContext(ctx, `SELECT autodarts_user_id FROM players WHERE id = ?`, playerID).Scan(&uid); err != nil {
		return nil, err
	}
	pr.HasAccount = uid.Valid && uid.String != ""
	ar, err := s.DB.QueryContext(ctx, `SELECT normalized_name FROM player_aliases WHERE player_id = ? ORDER BY normalized_name`, playerID)
	if err != nil {
		return nil, err
	}
	for ar.Next() {
		var a string
		if err := ar.Scan(&a); err != nil {
			ar.Close()
			return nil, err
		}
		pr.Aliases = append(pr.Aliases, a)
	}
	ar.Close()

	clause, args := filterClause(f)
	var bestLeg sql.NullFloat64
	var bestDarts sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, `SELECT MAX(l.average), MIN(CASE WHEN l.won = 1 AND l.points IS NOT NULL THEN l.darts END)
		FROM match_legs l JOIN matches mt ON mt.id = l.match_id WHERE l.player_id = ? AND `+clause, append([]any{playerID}, args...)...).Scan(&bestLeg, &bestDarts); err != nil {
		return nil, err
	}
	if bestLeg.Valid {
		pr.BestLegAvg = &bestLeg.Float64
	}
	if bestDarts.Valid {
		pr.BestLegDarts = int(bestDarts.Int64)
	}
	var most180 sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, `SELECT MAX(mp.count_180) FROM match_players mp JOIN matches mt ON mt.id = mp.match_id WHERE mp.player_id = ? AND `+clause, append([]any{playerID}, args...)...).Scan(&most180); err != nil {
		return nil, err
	}
	pr.Most180Match = int(most180.Int64)

	vr, err := s.DB.QueryContext(ctx, `SELECT mt.variant, COUNT(*), SUM(mp.won) FROM match_players mp JOIN matches mt ON mt.id = mp.match_id
		WHERE mp.player_id = ? AND mt.finished = 1 AND `+clause+` GROUP BY mt.variant ORDER BY COUNT(*) DESC`, append([]any{playerID}, args...)...)
	if err != nil {
		return nil, err
	}
	for vr.Next() {
		var v VariantCount
		if err := vr.Scan(&v.Variant, &v.Matches, &v.Wins); err != nil {
			vr.Close()
			return nil, err
		}
		pr.Variants = append(pr.Variants, v)
	}
	vr.Close()

	hr, err := s.DB.QueryContext(ctx, `SELECT mt.id, mt.played_at, mp.average, mp.won FROM match_players mp JOIN matches mt ON mt.id = mp.match_id
		WHERE mp.player_id = ? AND mt.finished = 1 AND mp.average IS NOT NULL AND `+clause+` ORDER BY mt.played_at DESC LIMIT 100`, append([]any{playerID}, args...)...)
	if err != nil {
		return nil, err
	}
	for hr.Next() {
		var h HistoryPoint
		var avg sql.NullFloat64
		if err := hr.Scan(&h.MatchID, &h.PlayedAt, &avg, &h.Won); err != nil {
			hr.Close()
			return nil, err
		}
		if avg.Valid {
			h.Average = &avg.Float64
		}
		pr.History = append(pr.History, h)
	}
	hr.Close()
	// chronologisch
	for i, j := 0, len(pr.History)-1; i < j; i, j = i+1, j-1 {
		pr.History[i], pr.History[j] = pr.History[j], pr.History[i]
	}

	pr.Recent, err = s.matches(ctx, `SELECT mp.match_id FROM match_players mp JOIN matches mt ON mt.id = mp.match_id WHERE mp.player_id = ? AND `+clause+` ORDER BY mt.played_at DESC LIMIT 20`, append([]any{playerID}, args...)...)
	if err != nil {
		return nil, err
	}
	return pr, nil
}

type H2H struct {
	A       PlayerRow      `json:"a"`
	B       PlayerRow      `json:"b"`
	Matches int            `json:"matches"`
	WinsA   int            `json:"wins_a"`
	WinsB   int            `json:"wins_b"`
	LegsA   int            `json:"legs_a"`
	LegsB   int            `json:"legs_b"`
	AvgA    *float64       `json:"average_a"`
	AvgB    *float64       `json:"average_b"`
	Recent  []MatchSummary `json:"recent_matches"`
}

func (s *Service) HeadToHead(ctx context.Context, a, b int64, f Filter) (*H2H, error) {
	clause, args := filterClause(f)
	// Matches, in denen beide gespielt haben.
	both := `SELECT mt.id FROM matches mt WHERE ` + clause + ` AND EXISTS (SELECT 1 FROM match_players x WHERE x.match_id = mt.id AND x.player_id = ?) AND EXISTS (SELECT 1 FROM match_players y WHERE y.match_id = mt.id AND y.player_id = ?)`
	bothArgs := append(append([]any{}, args...), a, b)

	h := &H2H{}
	var err error
	if err = s.DB.QueryRowContext(ctx, `SELECT display_name FROM players WHERE id = ?`, a).Scan(&h.A.DisplayName); err != nil {
		return nil, ErrNotFound
	}
	if err = s.DB.QueryRowContext(ctx, `SELECT display_name FROM players WHERE id = ?`, b).Scan(&h.B.DisplayName); err != nil {
		return nil, ErrNotFound
	}
	h.A.PlayerID, h.B.PlayerID = a, b

	side := func(pid int64) (wins, legs int, avg *float64, err error) {
		var points sql.NullInt64
		var darts int
		var qargs []any
		for i := 0; i < 4; i++ {
			qargs = append(qargs, pid)
			qargs = append(qargs, bothArgs...)
		}
		err = s.DB.QueryRowContext(ctx, `SELECT
			(SELECT COUNT(*) FROM match_players mp JOIN matches mt ON mt.id = mp.match_id WHERE mp.player_id = ? AND mp.won = 1 AND mt.finished = 1 AND mt.id IN (`+both+`)),
			(SELECT COUNT(*) FROM match_legs l WHERE l.player_id = ? AND l.won = 1 AND l.match_id IN (`+both+`)),
			(SELECT SUM(l.points) FROM match_legs l WHERE l.player_id = ? AND l.match_id IN (`+both+`)),
			(SELECT COALESCE(SUM(CASE WHEN l.points IS NOT NULL THEN l.darts ELSE 0 END),0) FROM match_legs l WHERE l.player_id = ? AND l.match_id IN (`+both+`))`,
			qargs...).Scan(&wins, &legs, &points, &darts)
		if err != nil {
			return
		}
		if points.Valid && darts > 0 {
			v := float64(points.Int64) * 3 / float64(darts)
			avg = &v
		}
		return
	}
	if h.WinsA, h.LegsA, h.AvgA, err = side(a); err != nil {
		return nil, err
	}
	if h.WinsB, h.LegsB, h.AvgB, err = side(b); err != nil {
		return nil, err
	}
	if err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM matches mt WHERE mt.finished = 1 AND mt.id IN (`+both+`)`, bothArgs...).Scan(&h.Matches); err != nil {
		return nil, err
	}
	h.Recent, err = s.matches(ctx, both+` ORDER BY mt.played_at DESC LIMIT 20`, bothArgs...)
	if err != nil {
		return nil, err
	}
	return h, nil
}

type LegRow struct {
	Set          int      `json:"set"`
	Leg          int      `json:"leg"`
	PlayerID     int64    `json:"player_id"`
	DisplayName  string   `json:"display_name"`
	Won          bool     `json:"won"`
	Darts        int      `json:"darts"`
	Average      *float64 `json:"average"`
	First9Avg    *float64 `json:"first9_avg"`
	Checkout     int      `json:"checkout"`
	Count180     int      `json:"count_180"`
	Count140Plus int      `json:"count_140plus"`
	Count100Plus int      `json:"count_100plus"`
}

type PendingSlot struct {
	PlayerIndex int    `json:"player_index"`
	Name        string `json:"name"`
	Reason      string `json:"reason"`
}

type MatchDetail struct {
	MatchSummary
	AutodartsMatchID string        `json:"autodarts_match_id"`
	Settings         string        `json:"settings"`
	Legs             []LegRow      `json:"legs"`
	Pending          []PendingSlot `json:"pending"`
}

func (s *Service) Match(ctx context.Context, id int64) (*MatchDetail, error) {
	ms, err := s.matches(ctx, "?", id)
	if err != nil {
		return nil, err
	}
	if len(ms) == 0 {
		return nil, ErrNotFound
	}
	d := &MatchDetail{MatchSummary: ms[0], Legs: []LegRow{}, Pending: []PendingSlot{}}
	if err := s.DB.QueryRowContext(ctx, `SELECT autodarts_match_id, settings_json FROM matches WHERE id = ?`, id).Scan(&d.AutodartsMatchID, &d.Settings); err != nil {
		return nil, err
	}
	pr, err := s.DB.QueryContext(ctx, `SELECT player_index, name, reason FROM match_pending WHERE match_id = ? ORDER BY player_index`, id)
	if err != nil {
		return nil, err
	}
	for pr.Next() {
		var ps PendingSlot
		if err := pr.Scan(&ps.PlayerIndex, &ps.Name, &ps.Reason); err != nil {
			pr.Close()
			return nil, err
		}
		d.Pending = append(d.Pending, ps)
	}
	pr.Close()
	rs, err := s.DB.QueryContext(ctx, `SELECT l.set_no, l.leg_no, l.player_id, p.display_name, l.won, l.darts, l.average, l.first9_avg, l.checkout, l.count_180, l.count_140plus, l.count_100plus
		FROM match_legs l JOIN players p ON p.id = l.player_id WHERE l.match_id = ? ORDER BY l.set_no, l.leg_no, l.player_id`, id)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	for rs.Next() {
		var l LegRow
		var avg, f9 sql.NullFloat64
		if err := rs.Scan(&l.Set, &l.Leg, &l.PlayerID, &l.DisplayName, &l.Won, &l.Darts, &avg, &f9, &l.Checkout, &l.Count180, &l.Count140Plus, &l.Count100Plus); err != nil {
			return nil, err
		}
		if avg.Valid {
			l.Average = &avg.Float64
		}
		if f9.Valid {
			l.First9Avg = &f9.Float64
		}
		d.Legs = append(d.Legs, l)
	}
	return d, rs.Err()
}

func (s *Service) RecentMatches(ctx context.Context, limit int) ([]MatchSummary, error) {
	return s.matches(ctx, `SELECT id FROM matches ORDER BY played_at DESC LIMIT ?`, limit)
}

type Meta struct {
	Players  int      `json:"players"`
	Matches  int      `json:"matches"`
	Legs     int      `json:"legs"`
	Variants []string `json:"variants"`
}

func (s *Service) Meta(ctx context.Context) (*Meta, error) {
	m := &Meta{Variants: []string{}}
	if err := s.DB.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM players), (SELECT COUNT(*) FROM matches WHERE finished = 1), (SELECT COUNT(*) FROM match_legs WHERE won = 1)`).Scan(&m.Players, &m.Matches, &m.Legs); err != nil {
		return nil, err
	}
	rs, err := s.DB.QueryContext(ctx, `SELECT DISTINCT variant FROM matches WHERE variant <> '' ORDER BY variant`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	for rs.Next() {
		var v string
		if err := rs.Scan(&v); err != nil {
			return nil, err
		}
		m.Variants = append(m.Variants, v)
	}
	return m, nil
}
