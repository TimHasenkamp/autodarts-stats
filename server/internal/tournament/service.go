package tournament

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"strings"
	"time"

	"autodarts-stats/internal/names"
)

var ErrNotFound = errors.New("Turnier nicht gefunden")

// InputError ist ein Fehler in der Eingabe des Admins (HTTP 400).
type InputError string

func (e InputError) Error() string { return string(e) }

const (
	StateDraft    = "draft"
	StateRunning  = "running"
	StateFinished = "finished"
)

// Toleranz zwischen Autodarts-Startzeit und Serverzeit bei der automatischen
// Zuordnung: ein Match zaehlt, wenn es nach dem Freiwerden der Paarung begann.
const clockSkew = time.Minute

const timeFmt = "2006-01-02T15:04:05.000Z"

func fmtTime(t time.Time) string { return t.UTC().Format(timeFmt) }

func parseTime(s string) time.Time {
	t, _ := time.Parse(timeFmt, s)
	return t
}

type Service struct {
	DB  *sql.DB
	Now func() time.Time
	// Shuffle mischt die Auslosung; in Tests austauschbar.
	Shuffle func(ids []int64)
	// IntN liefert eine Zufallszahl in [0,n) fuer den Gegner des Lucky Losers.
	IntN func(n int) int
}

func New(db *sql.DB) *Service {
	return &Service{
		DB:      db,
		Now:     func() time.Time { return time.Now().UTC() },
		Shuffle: func(ids []int64) { rand.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] }) },
		IntN:    rand.IntN,
	}
}

// querier ist *sql.DB oder *sql.Tx. Der Ingest ruft Sync innerhalb seiner
// Transaktion auf; die DB hat nur eine Verbindung, also darf dann nichts
// ausserhalb der Transaktion laufen.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type tournament struct {
	ID          int64
	Name        string
	Status      string
	ThirdPlace  bool
	LuckyLoser  bool
	LLEntry     int
	PlayinMatch int
	PlayinSide  int
	Rules       Rules
	CreatedAt   string
	StartedAt   string
	FinishedAt  string
	Players     []int64 // nach Position
}

func (t *tournament) config() Config {
	return Config{Order: t.Players, ThirdPlace: t.ThirdPlace, LuckyLoser: t.LuckyLoser, LLEntry: t.LLEntry,
		PlayinMatch: t.PlayinMatch, PlayinSide: t.PlayinSide, Rules: t.Rules}
}

func load(ctx context.Context, q querier, id int64) (*tournament, error) {
	t := &tournament{ID: id}
	var rules string
	var started, finished sql.NullString
	err := q.QueryRowContext(ctx, `SELECT name, status, third_place, lucky_loser, ll_entry, playin_match, playin_side, rules_json, created_at, started_at, finished_at
		FROM tournaments WHERE id = ?`, id).Scan(&t.Name, &t.Status, &t.ThirdPlace, &t.LuckyLoser, &t.LLEntry, &t.PlayinMatch, &t.PlayinSide, &rules, &t.CreatedAt, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.StartedAt, t.FinishedAt = started.String, finished.String
	_ = json.Unmarshal([]byte(rules), &t.Rules)
	rows, err := q.QueryContext(ctx, `SELECT player_id FROM tournament_players WHERE tournament_id = ? ORDER BY position, player_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pid int64
		if err := rows.Scan(&pid); err != nil {
			return nil, err
		}
		t.Players = append(t.Players, pid)
	}
	return t, rows.Err()
}

// loadResults liest die Ergebnisse. DecidedAt ist bei verknuepften Matches
// das Matchende, sonst der Zeitpunkt der Eingabe.
func loadResults(ctx context.Context, q querier, id int64) (map[string]*Result, error) {
	rows, err := q.QueryContext(ctx, `SELECT r.match_key, r.player1_id, r.player2_id, r.winner_id, r.legs1, r.legs2, COALESCE(r.match_id,0), r.source, r.warning,
		COALESCE(m.updated_at, r.created_at)
		FROM tournament_results r LEFT JOIN matches m ON m.id = r.match_id WHERE r.tournament_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*Result{}
	for rows.Next() {
		r := &Result{}
		var decided string
		if err := rows.Scan(&r.Key, &r.Player1, &r.Player2, &r.Winner, &r.Legs1, &r.Legs2, &r.MatchID, &r.Source, &r.Warning, &decided); err != nil {
			return nil, err
		}
		r.DecidedAt = parseTime(decided)
		out[r.Key] = r
	}
	return out, rows.Err()
}

func evalTournament(ctx context.Context, q querier, t *tournament) (*evaluation, error) {
	L, err := buildLayout(t.config())
	if err != nil {
		return nil, err
	}
	res, err := loadResults(ctx, q, t.ID)
	if err != nil {
		return nil, err
	}
	return evaluate(L, res, parseTime(t.StartedAt)), nil
}

// ---- Anlegen / Bearbeiten ----

type Input struct {
	Name       string   `json:"name"`
	PlayerIDs  []int64  `json:"player_ids"`
	NewNames   []string `json:"new_names"`
	ThirdPlace bool     `json:"third_place"`
	LuckyLoser bool     `json:"lucky_loser"`
	LLEntry    int      `json:"ll_entry"`
	Rules      Rules    `json:"rules"`
}

func (in *Input) clean() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return InputError("Name fehlt")
	}
	if in.LLEntry < 0 {
		return InputError("Einstiegsrunde ungueltig")
	}
	for i, r := range in.Rules.Rounds {
		in.Rules.Rounds[i] = r.withDefaults()
	}
	in.Rules.LuckyLoser = in.Rules.LuckyLoser.withDefaults()
	return nil
}

// resolvePlayers liefert die Spieler-IDs in Eingabereihenfolge. Neue Namen
// werden einem vorhandenen Spieler (Name oder Alias) zugeordnet oder angelegt.
func resolvePlayers(ctx context.Context, tx *sql.Tx, in Input) ([]int64, error) {
	seen := map[int64]bool{}
	var out []int64
	add := func(id int64) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, id := range in.PlayerIDs {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM players WHERE id = ?`, id).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, InputError(fmt.Sprintf("Spieler #%d gibt es nicht", id))
		}
		add(id)
	}
	for _, raw := range in.NewNames {
		name := strings.TrimSpace(raw)
		norm := names.Normalize(name)
		if norm == "" {
			continue
		}
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM players WHERE normalized_name = ? OR id = (SELECT player_id FROM player_aliases WHERE normalized_name = ?) LIMIT 1`, norm, norm).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			r, err := tx.ExecContext(ctx, `INSERT INTO players (display_name, normalized_name) VALUES (?, ?)`, name, norm)
			if err != nil {
				return nil, err
			}
			id, _ = r.LastInsertId()
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO player_aliases (normalized_name, player_id) VALUES (?, ?)`, norm, id); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		}
		add(id)
	}
	return out, nil
}

func (s *Service) Create(ctx context.Context, in Input) (int64, error) {
	if err := in.clean(); err != nil {
		return 0, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ids, err := resolvePlayers(ctx, tx, in)
	if err != nil {
		return 0, err
	}
	rules, _ := json.Marshal(in.Rules)
	r, err := tx.ExecContext(ctx, `INSERT INTO tournaments (name, third_place, lucky_loser, ll_entry, rules_json, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		in.Name, in.ThirdPlace, in.LuckyLoser, in.LLEntry, string(rules), fmtTime(s.Now()))
	if err != nil {
		return 0, err
	}
	id, _ := r.LastInsertId()
	if err := setPlayers(ctx, tx, id, ids); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func setPlayers(ctx context.Context, tx *sql.Tx, id int64, ids []int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM tournament_players WHERE tournament_id = ?`, id); err != nil {
		return err
	}
	for i, pid := range ids {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tournament_players (tournament_id, player_id, position) VALUES (?, ?, ?)`, id, pid, i); err != nil {
			return err
		}
	}
	return nil
}

// Update aendert ein Turnier. Nach dem Start lassen sich nur noch Name und
// Regeln aendern; Teilnehmer und Modus haengen an der Auslosung.
func (s *Service) Update(ctx context.Context, id int64, in Input) error {
	if err := in.clean(); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	t, err := load(ctx, tx, id)
	if err != nil {
		return err
	}
	rules, _ := json.Marshal(in.Rules)
	if t.Status != StateDraft {
		if _, err := tx.ExecContext(ctx, `UPDATE tournaments SET name = ?, rules_json = ? WHERE id = ?`, in.Name, string(rules), id); err != nil {
			return err
		}
		// Regeln aendern die Warnungen automatisch zugeordneter Ergebnisse.
		if err := s.syncTournament(ctx, tx, id); err != nil {
			return err
		}
		return tx.Commit()
	}
	ids, err := resolvePlayers(ctx, tx, in)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tournaments SET name = ?, third_place = ?, lucky_loser = ?, ll_entry = ?, rules_json = ? WHERE id = ?`,
		in.Name, in.ThirdPlace, in.LuckyLoser, in.LLEntry, string(rules), id); err != nil {
		return err
	}
	if err := setPlayers(ctx, tx, id, ids); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	r, err := s.DB.ExecContext(ctx, `DELETE FROM tournaments WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Start lost aus und startet das Turnier. Redraw lost ein laufendes Turnier
// neu aus, solange noch kein Ergebnis vorliegt.
func (s *Service) Start(ctx context.Context, id int64) error { return s.draw(ctx, id, false) }

func (s *Service) Redraw(ctx context.Context, id int64) error { return s.draw(ctx, id, true) }

func (s *Service) draw(ctx context.Context, id int64, redraw bool) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	t, err := load(ctx, tx, id)
	if err != nil {
		return err
	}
	if redraw {
		if t.Status != StateRunning {
			return InputError("nur ein laufendes Turnier kann neu ausgelost werden")
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tournament_results WHERE tournament_id = ?`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return InputError("es gibt schon Ergebnisse; erst loeschen, dann neu auslosen")
		}
	} else if t.Status != StateDraft {
		return InputError("Turnier laeuft bereits")
	}
	if len(t.Players) < 2 {
		return InputError("mindestens 2 Spieler noetig")
	}
	rounds := RoundCount(len(t.Players))
	if t.LuckyLoser && EntryRound(rounds, t.LLEntry) == 0 {
		return InputError(fmt.Sprintf("Mit %d Spielern gibt es keine Lucky-Loser-Runde vor dieser Runde. Moeglich ab 3 Spielern, Einstieg fruehestens in Runde 2.", len(t.Players)))
	}
	if _, err := buildLayout(t.config()); err != nil {
		return InputError(err.Error())
	}
	order := append([]int64(nil), t.Players...)
	s.Shuffle(order)
	for i, pid := range order {
		if _, err := tx.ExecContext(ctx, `UPDATE tournament_players SET position = ? WHERE tournament_id = ? AND player_id = ?`, i, id, pid); err != nil {
			return err
		}
	}
	playinMatch, playinSide := 0, 0
	if e := EntryRound(rounds, t.LLEntry); t.LuckyLoser && e > 0 {
		playinMatch = s.IntN(1 << (rounds - e))
		playinSide = s.IntN(2)
	}
	started := t.StartedAt
	if !redraw {
		started = fmtTime(s.Now())
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tournaments SET status = ?, started_at = ?, finished_at = NULL, playin_match = ?, playin_side = ? WHERE id = ?`,
		StateRunning, started, playinMatch, playinSide, id); err != nil {
		return err
	}
	if err := s.syncTournament(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- Ergebnisse ----

// SetResult traegt ein Ergebnis von Hand ein (ohne verknuepftes Match).
func (s *Service) SetResult(ctx context.Context, id int64, key string, winner int64, legs1, legs2 int) error {
	return s.withMatch(ctx, id, key, func(tx *sql.Tx, m *evalMatch) error {
		if winner != m.a.player && winner != m.b.player {
			return InputError("Sieger spielt nicht in dieser Paarung")
		}
		if legs1 < 0 || legs2 < 0 {
			return InputError("Legs ungueltig")
		}
		return putResult(ctx, tx, id, key, m.a.player, m.b.player, winner, legs1, legs2, 0, "manual", "", s.Now())
	})
}

// LinkMatch ordnet ein erfasstes Match einer Paarung zu.
func (s *Service) LinkMatch(ctx context.Context, id int64, key string, matchID int64) error {
	return s.withMatch(ctx, id, key, func(tx *sql.Tx, m *evalMatch) error {
		var other string
		err := tx.QueryRowContext(ctx, `SELECT match_key FROM tournament_results WHERE match_id = ? AND NOT (tournament_id = ? AND match_key = ?) LIMIT 1`, matchID, id, key).Scan(&other)
		if err == nil {
			return InputError("dieses Match ist schon einer anderen Paarung zugeordnet")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		mr, err := readMatch(ctx, tx, matchID, m.a.player, m.b.player)
		if err != nil {
			return err
		}
		if mr == nil {
			return InputError("das Match ist nicht beendet oder die beiden Spieler haben es nicht gegeneinander gespielt")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM tournament_ignored WHERE tournament_id = ? AND match_id = ?`, id, matchID); err != nil {
			return err
		}
		return putResult(ctx, tx, id, key, m.a.player, m.b.player, mr.winner, mr.legsA, mr.legsB, matchID, "link", checkRule(m.node.rule, mr), s.Now())
	})
}

// ClearResult entfernt ein Ergebnis. War ein Match verknuepft, wird es fuer
// dieses Turnier nicht erneut automatisch zugeordnet.
func (s *Service) ClearResult(ctx context.Context, id int64, key string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	t, err := load(ctx, tx, id)
	if err != nil {
		return err
	}
	if t.Status == StateDraft {
		return InputError("Turnier ist noch nicht gestartet")
	}
	var matchID sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT match_id FROM tournament_results WHERE tournament_id = ? AND match_key = ?`, id, key).Scan(&matchID)
	if errors.Is(err, sql.ErrNoRows) {
		return InputError("kein Ergebnis vorhanden")
	}
	if err != nil {
		return err
	}
	if matchID.Valid {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO tournament_ignored (tournament_id, match_id) VALUES (?, ?)`, id, matchID.Int64); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tournament_results WHERE tournament_id = ? AND match_key = ?`, id, key); err != nil {
		return err
	}
	if err := s.syncTournament(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

// withMatch prueft, dass die Paarung key feststeht, fuehrt fn aus und
// gleicht das Turnier danach ab.
func (s *Service) withMatch(ctx context.Context, id int64, key string, fn func(tx *sql.Tx, m *evalMatch) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	t, err := load(ctx, tx, id)
	if err != nil {
		return err
	}
	if t.Status == StateDraft {
		return InputError("Turnier ist noch nicht gestartet")
	}
	ev, err := evalTournament(ctx, tx, t)
	if err != nil {
		return err
	}
	m, ok := ev.memo[key]
	if !ok {
		return InputError("unbekannte Paarung")
	}
	if m.status != StatusReady && m.status != StatusDone {
		return InputError("die Paarung steht noch nicht fest")
	}
	if err := fn(tx, m); err != nil {
		return err
	}
	if err := s.syncTournament(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

func putResult(ctx context.Context, q querier, id int64, key string, p1, p2, winner int64, legs1, legs2 int, matchID int64, source, warning string, now time.Time) error {
	var mid any
	if matchID > 0 {
		mid = matchID
	}
	_, err := q.ExecContext(ctx, `INSERT INTO tournament_results (tournament_id, match_key, player1_id, player2_id, winner_id, legs1, legs2, match_id, source, warning, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tournament_id, match_key) DO UPDATE SET player1_id = excluded.player1_id, player2_id = excluded.player2_id, winner_id = excluded.winner_id,
			legs1 = excluded.legs1, legs2 = excluded.legs2, match_id = excluded.match_id, source = excluded.source, warning = excluded.warning, created_at = excluded.created_at`,
		id, key, p1, p2, winner, legs1, legs2, mid, source, warning, fmtTime(now))
	return err
}

// matchResult ist ein erfasstes Match aus Sicht einer Paarung a gegen b.
type matchResult struct {
	winner       int64
	legsA, legsB int
	variant      string
	settings     string
}

// readMatch liest ein beendetes Match, das genau a und b gegeneinander
// gespielt haben (nil, wenn nicht).
func readMatch(ctx context.Context, q querier, matchID, a, b int64) (*matchResult, error) {
	mr := &matchResult{}
	var finished bool
	err := q.QueryRowContext(ctx, `SELECT finished, variant, settings_json FROM matches WHERE id = ?`, matchID).Scan(&finished, &mr.variant, &mr.settings)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !finished {
		return nil, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT player_id, won, legs_won FROM match_players WHERE match_id = ?`, matchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	n := 0
	var sawA, sawB bool
	for rows.Next() {
		var pid int64
		var won bool
		var legs int
		if err := rows.Scan(&pid, &won, &legs); err != nil {
			return nil, err
		}
		n++
		switch pid {
		case a:
			sawA, mr.legsA = true, legs
		case b:
			sawB, mr.legsB = true, legs
		}
		if won {
			mr.winner = pid
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if n != 2 || !sawA || !sawB || mr.winner == 0 {
		return nil, nil
	}
	return mr, nil
}

// checkRule vergleicht ein Match mit den Regeln der Runde. Abweichungen
// verhindern die Zuordnung nicht, sondern werden als Warnung angezeigt.
func checkRule(rule Rule, mr *matchResult) string {
	var w []string
	if rule.Variant != "" && mr.variant != "" && !strings.EqualFold(rule.Variant, mr.variant) {
		w = append(w, fmt.Sprintf("%s statt %s gespielt", mr.variant, rule.Variant))
	}
	if strings.EqualFold(mr.variant, "X01") && rule.BaseScore > 0 {
		var st struct {
			BaseScore int `json:"baseScore"`
		}
		if json.Unmarshal([]byte(mr.settings), &st) == nil && st.BaseScore > 0 && st.BaseScore != rule.BaseScore {
			w = append(w, fmt.Sprintf("%d statt %d gespielt", st.BaseScore, rule.BaseScore))
		}
	}
	legs := mr.legsA
	if mr.legsB > legs {
		legs = mr.legsB
	}
	if rule.FirstTo > 0 && legs != rule.FirstTo {
		w = append(w, fmt.Sprintf("First to %d statt %d gespielt", legs, rule.FirstTo))
	}
	return strings.Join(w, ", ")
}

// ---- Abgleich ----

// MatchFinished ist der Hook fuer den Ingest: ein Match ist beendet (oder
// wurde neu berechnet). Alle laufenden Turniere werden abgeglichen.
func (s *Service) MatchFinished(ctx context.Context, tx *sql.Tx, matchID int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM tournaments WHERE status IN (?, ?) ORDER BY started_at, id`, StateRunning, StateFinished)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	// Ein Fehler in einem Turnier darf den Ingest nicht blockieren (die
	// Extension wuerde endlos neu senden): Turnier zuruecksetzen, loggen, weiter.
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `SAVEPOINT tournament_sync`); err != nil {
			return err
		}
		if err := s.syncTournament(ctx, tx, id); err != nil {
			log.Printf("turnier %d: abgleich nach match %d: %v", id, matchID, err)
			if _, err := tx.ExecContext(ctx, `ROLLBACK TO tournament_sync`); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `RELEASE tournament_sync`); err != nil {
			return err
		}
	}
	return nil
}

// syncTournament bringt ein Turnier auf den aktuellen Stand:
//  1. Ergebnisse verknuepfter Matches aus den Matchdaten auffrischen.
//  2. Spielbereite Paarungen automatisch mit einem passenden Match belegen.
//  3. Verfallene Ergebnisse entfernen, Status (laeuft/beendet) setzen.
func (s *Service) syncTournament(ctx context.Context, q querier, id int64) error {
	t, err := load(ctx, q, id)
	if err != nil {
		return err
	}
	if t.Status == StateDraft {
		return nil
	}
	L, err := buildLayout(t.config())
	if err != nil {
		return err
	}
	if err := refreshLinked(ctx, q, id, L); err != nil {
		return err
	}
	var ev *evaluation
	for range len(L.all) + 1 {
		res, err := loadResults(ctx, q, id)
		if err != nil {
			return err
		}
		ev = evaluate(L, res, parseTime(t.StartedAt))
		assigned := false
		for _, m := range ev.ready() {
			mid, err := findCandidate(ctx, q, id, m)
			if err != nil {
				return err
			}
			if mid == 0 {
				continue
			}
			mr, err := readMatch(ctx, q, mid, m.a.player, m.b.player)
			if err != nil {
				return err
			}
			if mr == nil {
				continue
			}
			if err := putResult(ctx, q, id, m.node.key, m.a.player, m.b.player, mr.winner, mr.legsA, mr.legsB, mid, "auto", checkRule(m.node.rule, mr), s.Now()); err != nil {
				return err
			}
			assigned = true
			break // Ergebnis veraendert den Baum: neu auswerten
		}
		if !assigned {
			break
		}
	}
	used := ev.usedResults()
	for key := range ev.results {
		if !used[key] {
			if _, err := q.ExecContext(ctx, `DELETE FROM tournament_results WHERE tournament_id = ? AND match_key = ?`, id, key); err != nil {
				return err
			}
		}
	}
	if ev.complete() {
		_, err = q.ExecContext(ctx, `UPDATE tournaments SET status = ?, finished_at = COALESCE(finished_at, ?) WHERE id = ?`, StateFinished, fmtTime(s.Now()), id)
	} else {
		_, err = q.ExecContext(ctx, `UPDATE tournaments SET status = ?, finished_at = NULL WHERE id = ?`, StateRunning, id)
	}
	return err
}

// refreshLinked uebernimmt Sieger und Legs verknuepfter Matches neu (z.B.
// nach reprocess). Passt ein Match nicht mehr zur Paarung, bleibt es stehen.
func refreshLinked(ctx context.Context, q querier, id int64, L *layout) error {
	rows, err := q.QueryContext(ctx, `SELECT match_key, player1_id, player2_id, match_id FROM tournament_results WHERE tournament_id = ? AND match_id IS NOT NULL`, id)
	if err != nil {
		return err
	}
	type linked struct {
		key       string
		p1, p2, m int64
	}
	var list []linked
	for rows.Next() {
		var l linked
		if err := rows.Scan(&l.key, &l.p1, &l.p2, &l.m); err != nil {
			rows.Close()
			return err
		}
		list = append(list, l)
	}
	rows.Close()
	for _, l := range list {
		mr, err := readMatch(ctx, q, l.m, l.p1, l.p2)
		if err != nil {
			return err
		}
		if mr == nil {
			continue
		}
		warning := ""
		if nd, ok := L.nodes[l.key]; ok {
			warning = checkRule(nd.rule, mr)
		}
		if _, err := q.ExecContext(ctx, `UPDATE tournament_results SET winner_id = ?, legs1 = ?, legs2 = ?, warning = ? WHERE tournament_id = ? AND match_key = ?`,
			mr.winner, mr.legsA, mr.legsB, warning, id, l.key); err != nil {
			return err
		}
	}
	return nil
}

// findCandidate sucht das erste beendete Match genau dieser beiden Spieler,
// das nach dem Freiwerden der Paarung begonnen hat und noch nirgends
// zugeordnet ist.
func findCandidate(ctx context.Context, q querier, id int64, m *evalMatch) (int64, error) {
	var mid int64
	err := q.QueryRowContext(ctx, `SELECT mt.id FROM matches mt
		WHERE mt.finished = 1 AND mt.played_at >= ?
		  AND (SELECT COUNT(*) FROM match_players mp WHERE mp.match_id = mt.id) = 2
		  AND EXISTS (SELECT 1 FROM match_players mp WHERE mp.match_id = mt.id AND mp.player_id = ?)
		  AND EXISTS (SELECT 1 FROM match_players mp WHERE mp.match_id = mt.id AND mp.player_id = ?)
		  AND NOT EXISTS (SELECT 1 FROM match_pending pe WHERE pe.match_id = mt.id)
		  AND NOT EXISTS (SELECT 1 FROM tournament_results r WHERE r.match_id = mt.id)
		  AND NOT EXISTS (SELECT 1 FROM tournament_ignored i WHERE i.match_id = mt.id AND i.tournament_id = ?)
		ORDER BY mt.played_at, mt.id LIMIT 1`,
		fmtTime(m.readyAt.Add(-clockSkew)), m.a.player, m.b.player, id).Scan(&mid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return mid, err
}

// Candidate ist ein Match, das der Admin einer Paarung zuordnen kann.
type Candidate struct {
	MatchID  int64  `json:"match_id"`
	PlayedAt string `json:"played_at"`
	Variant  string `json:"variant"`
	Score    string `json:"score"`
	Winner   string `json:"winner"`
	LinkedTo string `json:"linked_to,omitempty"` // bereits zugeordnet (Turnier/Paarung)
}

// Candidates listet beendete Matches der beiden Spieler seit Turnierstart.
func (s *Service) Candidates(ctx context.Context, id int64, key string) ([]Candidate, error) {
	t, err := load(ctx, s.DB, id)
	if err != nil {
		return nil, err
	}
	if t.Status == StateDraft {
		return []Candidate{}, nil
	}
	ev, err := evalTournament(ctx, s.DB, t)
	if err != nil {
		return nil, err
	}
	m, ok := ev.memo[key]
	if !ok {
		return nil, InputError("unbekannte Paarung")
	}
	out := []Candidate{}
	if m.a.state != slotKnown || m.b.state != slotKnown {
		return out, nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT mt.id, mt.played_at, mt.variant,
			(SELECT legs_won FROM match_players WHERE match_id = mt.id AND player_id = ?),
			(SELECT legs_won FROM match_players WHERE match_id = mt.id AND player_id = ?),
			COALESCE((SELECT p.display_name FROM match_players mp JOIN players p ON p.id = mp.player_id WHERE mp.match_id = mt.id AND mp.won = 1), ''),
			COALESCE((SELECT t.name || ' · ' || r.match_key FROM tournament_results r JOIN tournaments t ON t.id = r.tournament_id WHERE r.match_id = mt.id LIMIT 1), '')
		FROM matches mt
		WHERE mt.finished = 1 AND mt.played_at >= ?
		  AND (SELECT COUNT(*) FROM match_players mp WHERE mp.match_id = mt.id) = 2
		  AND EXISTS (SELECT 1 FROM match_players mp WHERE mp.match_id = mt.id AND mp.player_id = ?)
		  AND EXISTS (SELECT 1 FROM match_players mp WHERE mp.match_id = mt.id AND mp.player_id = ?)
		ORDER BY mt.played_at DESC LIMIT 20`,
		m.a.player, m.b.player, fmtTime(parseTime(t.StartedAt).Add(-clockSkew)), m.a.player, m.b.player)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c Candidate
		var la, lb int
		if err := rows.Scan(&c.MatchID, &c.PlayedAt, &c.Variant, &la, &lb, &c.Winner, &c.LinkedTo); err != nil {
			return nil, err
		}
		c.Score = fmt.Sprintf("%d:%d", la, lb)
		out = append(out, c)
	}
	return out, rows.Err()
}
