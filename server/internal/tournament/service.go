package tournament

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"autodarts-stats/internal/names"
)

var ErrNotFound = errors.New("Turnier nicht gefunden")

// InputError ist ein Fehler in der Eingabe des Admins (HTTP 400).
type InputError string

func (e InputError) Error() string { return string(e) }

// Affected ist eine Paarung, deren Ergebnis durch eine Aenderung verfiele.
type Affected struct {
	Key   string `json:"key"`
	No    int    `json:"no"`
	Label string `json:"label"`
}

// ConflictError: Die Aenderung wuerde Ergebnisse anderer Paarungen verwerfen
// (HTTP 409). Bei Ergebnis-Korrekturen laesst sie sich mit force bestaetigen.
type ConflictError struct {
	Msg      string
	Affected []Affected
	// Forceable: mit ?force=1 erlaubt.
	Forceable bool
}

func (e *ConflictError) Error() string { return e.Msg }

const (
	StateDraft    = "draft"
	StateRunning  = "running"
	StateFinished = "finished"
)

// Obergrenzen fuer Eingaben.
const (
	maxNameLen  = 100
	maxPlayers  = 256
	maxRuleRows = 12
	maxFirstTo  = 99
	maxBase     = 10000
	maxLLEntry  = 8
	maxLegs     = 99
)

// Toleranz zwischen Autodarts-Startzeit und Serverzeit bei der automatischen
// Zuordnung: ein Match zaehlt, wenn es nach dem Freiwerden der Paarung begann.
const clockSkew = time.Minute

const timeFmt = "2006-01-02T15:04:05.000Z"

func fmtTime(t time.Time) string { return t.UTC().Format(timeFmt) }

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(timeFmt, s)
	if err != nil {
		// Andere Formate (z.B. ohne Millisekunden) noch akzeptieren.
		if t2, err2 := time.Parse(time.RFC3339Nano, s); err2 == nil {
			return t2
		}
		log.Printf("turnier: zeitstempel %q nicht lesbar: %v", s, err)
	}
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

type formerRow struct {
	seat       int
	player     int64
	replacedBy int64
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
	Players     []int64       // ausgeloste Spieler nach Position
	Late        map[int]int64 // Nachmelder: Spiel in Runde 1 -> Spieler
	Former      []formerRow
	Withdrawn   map[int64]withdrawal
}

func (t *tournament) config() Config {
	return Config{Order: t.Players, Late: t.Late, ThirdPlace: t.ThirdPlace, LuckyLoser: t.LuckyLoser, LLEntry: t.LLEntry,
		PlayinMatch: t.PlayinMatch, PlayinSide: t.PlayinSide, Rules: t.Rules}
}

func (t *tournament) roster() *roster {
	ro := &roster{former: map[int][]int64{}, withdrawn: t.Withdrawn}
	for _, f := range t.Former {
		ro.former[f.seat] = append(ro.former[f.seat], f.player)
	}
	return ro
}

// current: alle aktuell gemeldeten Spieler (Auslosung, dann Nachmelder).
func (t *tournament) current() []int64 {
	out := append([]int64(nil), t.Players...)
	idx := make([]int, 0, len(t.Late))
	for i := range t.Late {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		out = append(out, t.Late[i])
	}
	return out
}

// everyone: aktuelle und fruehere (ersetzte) Spieler.
func (t *tournament) everyone() []int64 {
	out := t.current()
	seen := map[int64]bool{}
	for _, p := range out {
		seen[p] = true
	}
	for _, f := range t.Former {
		if !seen[f.player] {
			seen[f.player] = true
			out = append(out, f.player)
		}
	}
	return out
}

// involved: pid steht (oder stand) in diesem Turnier.
func (t *tournament) involved(pid int64) bool {
	for _, p := range t.everyone() {
		if p == pid {
			return true
		}
	}
	return false
}

func load(ctx context.Context, q querier, id int64) (*tournament, error) {
	t := &tournament{ID: id, Late: map[int]int64{}, Withdrawn: map[int64]withdrawal{}}
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
	if err := json.Unmarshal([]byte(rules), &t.Rules); err != nil {
		log.Printf("turnier %d: regeln nicht lesbar, es gelten die Standardregeln: %v", id, err)
	}
	rows, err := q.QueryContext(ctx, `SELECT player_id, bye_slot, withdrawn_at, withdraw_reason FROM tournament_players WHERE tournament_id = ? ORDER BY position, player_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pid int64
		var bye sql.NullInt64
		var wAt sql.NullString
		var reason string
		if err := rows.Scan(&pid, &bye, &wAt, &reason); err != nil {
			return nil, err
		}
		if bye.Valid {
			t.Late[int(bye.Int64)] = pid
		} else {
			t.Players = append(t.Players, pid)
		}
		if wAt.Valid {
			t.Withdrawn[pid] = withdrawal{At: parseTime(wAt.String), Reason: reason}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	fr, err := q.QueryContext(ctx, `SELECT seat, player_id, replaced_by FROM tournament_former WHERE tournament_id = ? ORDER BY created_at, player_id`, id)
	if err != nil {
		return nil, err
	}
	defer fr.Close()
	for fr.Next() {
		var f formerRow
		if err := fr.Scan(&f.seat, &f.player, &f.replacedBy); err != nil {
			return nil, err
		}
		t.Former = append(t.Former, f)
	}
	return t, fr.Err()
}

// loadResults liest die Ergebnisse. DecidedAt ist bei verknuepften Matches
// das Matchende (finished_at, aeltere Matches: updated_at), sonst der
// Zeitpunkt der Eingabe.
func loadResults(ctx context.Context, q querier, id int64) (map[string]*Result, error) {
	rows, err := q.QueryContext(ctx, `SELECT r.match_key, r.player1_id, r.player2_id, r.winner_id, r.legs1, r.legs2, COALESCE(r.match_id,0), r.source, r.warning, r.outcome,
		COALESCE(m.finished_at, m.updated_at, r.created_at)
		FROM tournament_results r LEFT JOIN matches m ON m.id = r.match_id WHERE r.tournament_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*Result{}
	for rows.Next() {
		r := &Result{}
		var decided string
		if err := rows.Scan(&r.Key, &r.Player1, &r.Player2, &r.Winner, &r.Legs1, &r.Legs2, &r.MatchID, &r.Source, &r.Warning, &r.Outcome, &decided); err != nil {
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
	return evaluateWith(L, res, parseTime(t.StartedAt), t.roster()), nil
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

func checkRuleInput(r Rule, what string) error {
	if r.FirstTo < 0 || r.FirstTo > maxFirstTo {
		return InputError(fmt.Sprintf("%s: First to muss zwischen 1 und %d liegen", what, maxFirstTo))
	}
	if r.BaseScore < 0 || r.BaseScore > maxBase {
		return InputError(fmt.Sprintf("%s: Startpunktzahl ungueltig", what))
	}
	if utf8.RuneCountInString(r.Variant) > 40 {
		return InputError(fmt.Sprintf("%s: Spielvariante zu lang", what))
	}
	return nil
}

func (in *Input) clean() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return InputError("Name fehlt")
	}
	if utf8.RuneCountInString(in.Name) > maxNameLen {
		return InputError(fmt.Sprintf("Name zu lang (hoechstens %d Zeichen)", maxNameLen))
	}
	if in.LLEntry < 0 || in.LLEntry > maxLLEntry {
		return InputError("Einstiegsrunde ungueltig")
	}
	if len(in.PlayerIDs)+len(in.NewNames) > maxPlayers {
		return InputError(fmt.Sprintf("hoechstens %d Teilnehmer", maxPlayers))
	}
	if len(in.Rules.Rounds) > maxRuleRows {
		return InputError("zu viele Rundenregeln")
	}
	for i, r := range in.Rules.Rounds {
		if err := checkRuleInput(r, fmt.Sprintf("Regel %d", i+1)); err != nil {
			return err
		}
		in.Rules.Rounds[i] = r.withDefaults()
	}
	if err := checkRuleInput(in.Rules.LuckyLoser, "Lucky-Loser-Regel"); err != nil {
		return err
	}
	in.Rules.LuckyLoser = in.Rules.LuckyLoser.withDefaults()
	if in.LuckyLoser && in.ThirdPlace && in.LLEntry == 0 {
		return InputError("Spiel um Platz 3 geht nicht, wenn der Lucky Loser erst im Finale einsteigt")
	}
	return nil
}

// checkOptions probiert die Optionen mit n Spielern am echten Baum aus.
func checkOptions(n int, third, ll bool, entry int) error {
	if n < 2 {
		return nil // Entwurf mit zu wenigen Spielern: Start lehnt ab
	}
	rounds := RoundCount(n)
	if ll && EntryRound(rounds, entry) == 0 {
		return InputError(fmt.Sprintf("Mit %d Spielern gibt es keine Lucky-Loser-Runde vor dieser Runde. Moeglich ab 3 Spielern, Einstieg fruehestens in Runde 2.", n))
	}
	order := make([]int64, n)
	for i := range order {
		order[i] = int64(i + 1)
	}
	if _, err := buildLayout(Config{Order: order, ThirdPlace: third, LuckyLoser: ll, LLEntry: entry}); err != nil {
		return InputError(err.Error())
	}
	return nil
}

// resolveName ordnet einen Namen einem vorhandenen Spieler (Name oder Alias)
// zu oder legt ihn an. 0 bei leerem Namen.
func resolveName(ctx context.Context, tx *sql.Tx, raw string) (int64, error) {
	name := strings.TrimSpace(raw)
	norm := names.Normalize(name)
	if norm == "" {
		return 0, nil
	}
	if utf8.RuneCountInString(name) > maxNameLen {
		return 0, InputError("Spielername zu lang")
	}
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM players WHERE normalized_name = ? OR id = (SELECT player_id FROM player_aliases WHERE normalized_name = ?) LIMIT 1`, norm, norm).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		r, err := tx.ExecContext(ctx, `INSERT INTO players (display_name, normalized_name) VALUES (?, ?)`, name, norm)
		if err != nil {
			return 0, err
		}
		id, _ = r.LastInsertId()
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO player_aliases (normalized_name, player_id) VALUES (?, ?)`, norm, id); err != nil {
			return 0, err
		}
	} else if err != nil {
		return 0, err
	}
	return id, nil
}

func playerExists(ctx context.Context, q querier, id int64) error {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM players WHERE id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return InputError(fmt.Sprintf("Spieler #%d gibt es nicht", id))
	}
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
		if err := playerExists(ctx, tx, id); err != nil {
			return nil, err
		}
		add(id)
	}
	for _, raw := range in.NewNames {
		id, err := resolveName(ctx, tx, raw)
		if err != nil {
			return nil, err
		}
		if id != 0 {
			add(id)
		}
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
	if err := checkOptions(len(ids), in.ThirdPlace, in.LuckyLoser, in.LLEntry); err != nil {
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
// Regeln aendern; Teilnehmer und Modus haengen an der Auslosung (Teilnehmer
// aendern dann nur die eigenen Aktionen Ersetzen/Tauschen/Zurueckziehen/Nachmelden).
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
	if err := checkOptions(len(ids), in.ThirdPlace, in.LuckyLoser, in.LLEntry); err != nil {
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
		if len(t.Withdrawn) > 0 {
			return InputError("es gibt Rueckzuege; erst rueckgaengig machen, dann neu auslosen")
		}
		// Ohne Ergebnisse haben fruehere Spieler nichts gespielt; Nachmelder
		// werden normale Teilnehmer der neuen Auslosung.
		if _, err := tx.ExecContext(ctx, `DELETE FROM tournament_former WHERE tournament_id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tournament_players SET bye_slot = NULL WHERE tournament_id = ?`, id); err != nil {
			return err
		}
		if t, err = load(ctx, tx, id); err != nil {
			return err
		}
	} else if t.Status != StateDraft {
		return InputError("Turnier laeuft bereits")
	}
	if len(t.Players) < 2 {
		return InputError("mindestens 2 Spieler noetig")
	}
	if err := checkOptions(len(t.Players), t.ThirdPlace, t.LuckyLoser, t.LLEntry); err != nil {
		return err
	}
	if _, err := buildLayout(t.config()); err != nil {
		return InputError(err.Error())
	}
	rounds := RoundCount(len(t.Players))
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

// ---- Aenderungen mit Folgen-Pruefung ----

type guardMode int

const (
	// guardForce: verfallende Folgeergebnisse nur mit force (sonst 409).
	guardForce guardMode = iota
	// guardConflict: Folgeergebnisse duerfen nicht verfallen (409).
	guardConflict
	// guardInput: Folgeergebnisse duerfen nicht verfallen (400).
	guardInput
)

type guard struct {
	mode  guardMode
	force bool
	skip  string // die geaenderte Paarung selbst zaehlt nicht
	msg   string
}

// fallen listet Ergebnisse, die vorher galten und nachher nicht mehr.
func fallen(ctx context.Context, q querier, before, after *evaluation, skip string) ([]Affected, error) {
	var lost []*evalMatch
	var ids []int64
	for _, nd := range before.L.all {
		if nd.key == skip {
			continue
		}
		m := before.memo[nd.key]
		if m.result == nil {
			continue
		}
		if am := after.memo[nd.key]; am != nil && am.result != nil {
			continue
		}
		lost = append(lost, m)
		ids = append(ids, m.result.Player1, m.result.Player2)
	}
	if len(lost) == 0 {
		return nil, nil
	}
	nm, err := playerNames(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Affected, 0, len(lost))
	for _, m := range lost {
		out = append(out, Affected{Key: m.node.key, No: m.node.no,
			Label: fmt.Sprintf("Spiel %d: %s – %s", m.node.no, nm[m.result.Player1], nm[m.result.Player2])})
	}
	return out, nil
}

// mutate fuehrt fn in einer Transaktion auf einem gestarteten Turnier aus,
// prueft, welche Ergebnisse dadurch verfallen, und gleicht danach ab.
func (s *Service) mutate(ctx context.Context, id int64, g guard, fn func(tx *sql.Tx, t *tournament, ev *evaluation) error) error {
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
	if err := fn(tx, t, ev); err != nil {
		return err
	}
	t2, err := load(ctx, tx, id)
	if err != nil {
		return err
	}
	ev2, err := evalTournament(ctx, tx, t2)
	if err != nil {
		return InputError(err.Error())
	}
	lost, err := fallen(ctx, tx, ev, ev2, g.skip)
	if err != nil {
		return err
	}
	if len(lost) > 0 {
		switch {
		case g.mode == guardInput:
			return InputError(g.msg)
		case g.mode == guardConflict:
			return &ConflictError{Msg: g.msg, Affected: lost}
		case !g.force:
			return &ConflictError{Msg: "Dadurch verfallen spaetere Ergebnisse. Bitte bestaetigen (force=1).", Affected: lost, Forceable: true}
		}
	}
	if err := s.syncTournament(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- Ergebnisse ----

// ResultOpts ergaenzt ein von Hand eingetragenes Ergebnis.
type ResultOpts struct {
	// Outcome: "" regulaer, OutcomeRetired (Aufgabe) oder OutcomeDQ
	// (Disqualifikation) des Verlierers. Dann gelten keine Leg-Regeln.
	Outcome string
	// Force bestaetigt, dass spaetere Ergebnisse verfallen.
	Force bool
}

func readyOrDone(m *evalMatch) error {
	switch m.status {
	case StatusReady, StatusDone:
		return nil
	case StatusForfeit:
		return InputError("die Paarung ist durch Rueckzug kampflos entschieden")
	}
	return InputError("die Paarung steht noch nicht fest")
}

func matchOf(ev *evaluation, key string) (*evalMatch, error) {
	m, ok := ev.memo[key]
	if !ok {
		return nil, InputError("unbekannte Paarung")
	}
	return m, readyOrDone(m)
}

// ignoreReplaced merkt ein bisher verknuepftes Match als geloest vor, wenn
// das Ergebnis der Paarung durch ein anderes ersetzt wird.
func ignoreReplaced(ctx context.Context, tx *sql.Tx, id int64, key string, newMatch int64) error {
	var old sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT match_id FROM tournament_results WHERE tournament_id = ? AND match_key = ?`, id, key).Scan(&old)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if old.Valid && old.Int64 != newMatch {
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO tournament_ignored (tournament_id, match_id) VALUES (?, ?)`, id, old.Int64)
	}
	return err
}

// SetResult traegt ein Ergebnis von Hand ein (ohne verknuepftes Match).
func (s *Service) SetResult(ctx context.Context, id int64, key string, winner int64, legs1, legs2 int, opt ResultOpts) error {
	switch opt.Outcome {
	case "", OutcomeRetired, OutcomeDQ:
	default:
		return InputError("Ausgang ungueltig (erlaubt: retired, dq)")
	}
	return s.mutate(ctx, id, guard{mode: guardForce, force: opt.Force, skip: key}, func(tx *sql.Tx, t *tournament, ev *evaluation) error {
		m, err := matchOf(ev, key)
		if err != nil {
			return err
		}
		if winner != m.a.player && winner != m.b.player {
			return InputError("Sieger spielt nicht in dieser Paarung")
		}
		if legs1 < 0 || legs2 < 0 || legs1 > maxLegs || legs2 > maxLegs {
			return InputError("Legs ungueltig")
		}
		if opt.Outcome == "" {
			wl, ll := legs1, legs2
			if winner == m.b.player {
				wl, ll = legs2, legs1
			}
			if wl <= ll {
				return InputError("der Sieger muss mehr Legs haben (sonst Aufgabe/Disqualifikation waehlen)")
			}
		}
		if err := ignoreReplaced(ctx, tx, id, key, 0); err != nil {
			return err
		}
		return putResult(ctx, tx, id, key, m.a.player, m.b.player, winner, legs1, legs2, 0, "manual", "", opt.Outcome, s.Now())
	})
}

// LinkMatch ordnet ein erfasstes Match einer Paarung zu.
func (s *Service) LinkMatch(ctx context.Context, id int64, key string, matchID int64, force bool) error {
	return s.mutate(ctx, id, guard{mode: guardForce, force: force, skip: key}, func(tx *sql.Tx, t *tournament, ev *evaluation) error {
		m, err := matchOf(ev, key)
		if err != nil {
			return err
		}
		var other string
		err = tx.QueryRowContext(ctx, `SELECT match_key FROM tournament_results WHERE match_id = ? AND NOT (tournament_id = ? AND match_key = ?) LIMIT 1`, matchID, id, key).Scan(&other)
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
		if err := ignoreReplaced(ctx, tx, id, key, matchID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM tournament_ignored WHERE tournament_id = ? AND match_id = ?`, id, matchID); err != nil {
			return err
		}
		return putResult(ctx, tx, id, key, m.a.player, m.b.player, mr.winner, mr.legsA, mr.legsB, matchID, "link", checkRule(m.node.rule, mr), "", s.Now())
	})
}

// ClearResult entfernt ein Ergebnis. War ein Match verknuepft, wird es fuer
// dieses Turnier nicht erneut automatisch zugeordnet.
func (s *Service) ClearResult(ctx context.Context, id int64, key string, force bool) error {
	return s.mutate(ctx, id, guard{mode: guardForce, force: force, skip: key}, func(tx *sql.Tx, t *tournament, ev *evaluation) error {
		var matchID sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT match_id FROM tournament_results WHERE tournament_id = ? AND match_key = ?`, id, key).Scan(&matchID)
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
		_, err = tx.ExecContext(ctx, `DELETE FROM tournament_results WHERE tournament_id = ? AND match_key = ?`, id, key)
		return err
	})
}

func putResult(ctx context.Context, q querier, id int64, key string, p1, p2, winner int64, legs1, legs2 int, matchID int64, source, warning, outcome string, now time.Time) error {
	var mid any
	if matchID > 0 {
		mid = matchID
	}
	_, err := q.ExecContext(ctx, `INSERT INTO tournament_results (tournament_id, match_key, player1_id, player2_id, winner_id, legs1, legs2, match_id, source, warning, outcome, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tournament_id, match_key) DO UPDATE SET player1_id = excluded.player1_id, player2_id = excluded.player2_id, winner_id = excluded.winner_id,
			legs1 = excluded.legs1, legs2 = excluded.legs2, match_id = excluded.match_id, source = excluded.source, warning = excluded.warning,
			outcome = excluded.outcome, created_at = excluded.created_at`,
		id, key, p1, p2, winner, legs1, legs2, mid, source, warning, outcome, fmtTime(now))
	return err
}

// ---- Teilnehmer im laufenden Turnier ----

// activeSeat prueft, dass pid aktuell im Turnier spielt (nicht ersetzt, nicht
// ausgeschieden), und liefert seinen Platz.
func activeSeat(t *tournament, ev *evaluation, pid int64) (int, error) {
	seat, ok := ev.L.seatOf[pid]
	if !ok {
		if t.involved(pid) {
			return 0, InputError("der Spieler wurde bereits ersetzt")
		}
		return 0, InputError("der Spieler spielt nicht in diesem Turnier")
	}
	return seat, nil
}

func stillIn(ev *evaluation, pid int64) error {
	if pl := ev.placements([]int64{pid})[pid]; pl.Out {
		return InputError("der Spieler ist bereits ausgeschieden")
	}
	return nil
}

// newcomer bestimmt den neuen Spieler (vorhanden oder neuer Name) und prueft,
// dass er noch nicht im Turnier steht. seat >= 0 erlaubt einen frueheren
// Spieler genau dieses Platzes (Wechsel zuruecknehmen).
func newcomer(ctx context.Context, tx *sql.Tx, t *tournament, playerID int64, name string, seat int) (int64, error) {
	pid := playerID
	if pid > 0 {
		if err := playerExists(ctx, tx, pid); err != nil {
			return 0, err
		}
	} else {
		var err error
		if pid, err = resolveName(ctx, tx, name); err != nil {
			return 0, err
		}
		if pid == 0 {
			return 0, InputError("neuer Spieler fehlt (player_id oder new_name)")
		}
	}
	for _, p := range t.current() {
		if p == pid {
			return 0, InputError("der neue Spieler steht schon im Turnier")
		}
	}
	for _, f := range t.Former {
		if f.player == pid && f.seat != seat {
			return 0, InputError("der neue Spieler hat schon auf einem anderen Platz in diesem Turnier gespielt")
		}
	}
	return pid, nil
}

// Replace setzt auf den Platz von pid einen Ersatzspieler. Gespielte
// Ergebnisse von pid bleiben ihm zugeordnet und zaehlen fuer den Platz;
// offene und kuenftige Paarungen spielt der Ersatz.
func (s *Service) Replace(ctx context.Context, id, pid, newID int64, newName string) error {
	g := guard{mode: guardConflict, msg: "Ersetzen nicht moeglich: Ergebnisse wuerden verfallen"}
	return s.mutate(ctx, id, g, func(tx *sql.Tx, t *tournament, ev *evaluation) error {
		seat, err := activeSeat(t, ev, pid)
		if err != nil {
			return err
		}
		if _, ok := t.Withdrawn[pid]; ok {
			return InputError("der Spieler hat sich zurueckgezogen; erst den Rueckzug rueckgaengig machen")
		}
		if err := stillIn(ev, pid); err != nil {
			return err
		}
		np, err := newcomer(ctx, tx, t, newID, newName, seat)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tournament_players SET player_id = ? WHERE tournament_id = ? AND player_id = ?`, np, id, pid); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM tournament_former WHERE tournament_id = ? AND seat = ? AND player_id = ?`, id, seat, np); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO tournament_former (tournament_id, seat, player_id, replaced_by, created_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(tournament_id, seat, player_id) DO UPDATE SET replaced_by = excluded.replaced_by, created_at = excluded.created_at`,
			id, seat, pid, np, fmtTime(s.Now()))
		return err
	})
}

// Swap tauscht die Plaetze zweier Teilnehmer, die beide noch nicht gespielt haben.
func (s *Service) Swap(ctx context.Context, id, p1, p2 int64) error {
	g := guard{mode: guardConflict, msg: "Tauschen nicht moeglich: Ergebnisse wuerden verfallen"}
	return s.mutate(ctx, id, g, func(tx *sql.Tx, t *tournament, ev *evaluation) error {
		if p1 == p2 {
			return InputError("zwei verschiedene Spieler waehlen")
		}
		for _, p := range []int64{p1, p2} {
			seat, err := activeSeat(t, ev, p)
			if err != nil {
				return err
			}
			if _, ok := t.Withdrawn[p]; ok {
				return InputError("ein zurueckgezogener Spieler kann nicht tauschen")
			}
			if len(ev.ro.former[seat]) > 0 {
				return InputError("auf diesem Platz wurde schon gewechselt; Tausch nur, solange beide noch nicht gespielt haben")
			}
			for _, r := range ev.results {
				if r.Player1 == p || r.Player2 == p {
					return InputError("Tausch nur, solange beide noch nicht gespielt haben")
				}
			}
		}
		type pos struct {
			position int
			bye      sql.NullInt64
		}
		var a, b pos
		for _, x := range []struct {
			p   int64
			dst *pos
		}{{p1, &a}, {p2, &b}} {
			if err := tx.QueryRowContext(ctx, `SELECT position, bye_slot FROM tournament_players WHERE tournament_id = ? AND player_id = ?`, id, x.p).Scan(&x.dst.position, &x.dst.bye); err != nil {
				return err
			}
		}
		for _, x := range []struct {
			p int64
			v pos
		}{{p1, b}, {p2, a}} {
			var bye any
			if x.v.bye.Valid {
				bye = x.v.bye.Int64
			}
			if _, err := tx.ExecContext(ctx, `UPDATE tournament_players SET position = ?, bye_slot = ? WHERE tournament_id = ? AND player_id = ?`, x.v.position, bye, id, x.p); err != nil {
				return err
			}
		}
		return nil
	})
}

// Withdraw zieht pid zurueck: seine offene und alle kuenftigen Paarungen
// gehen kampflos an den Gegner. reason: OutcomeRetired (Standard) oder OutcomeDQ.
func (s *Service) Withdraw(ctx context.Context, id, pid int64, reason string) error {
	if reason == "" {
		reason = OutcomeRetired
	}
	if reason != OutcomeRetired && reason != OutcomeDQ {
		return InputError("Grund ungueltig (erlaubt: retired, dq)")
	}
	g := guard{mode: guardConflict, msg: "Zurueckziehen nicht moeglich: Ergebnisse wuerden verfallen"}
	return s.mutate(ctx, id, g, func(tx *sql.Tx, t *tournament, ev *evaluation) error {
		if _, err := activeSeat(t, ev, pid); err != nil {
			return err
		}
		if _, ok := t.Withdrawn[pid]; ok {
			return InputError("der Spieler hat sich bereits zurueckgezogen")
		}
		if err := stillIn(ev, pid); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE tournament_players SET withdrawn_at = ?, withdraw_reason = ? WHERE tournament_id = ? AND player_id = ?`,
			fmtTime(s.Now()), reason, id, pid)
		return err
	})
}

// UndoWithdraw nimmt einen Rueckzug zurueck, solange keine spaeteren
// Ergebnisse auf den kampflosen Entscheidungen beruhen.
func (s *Service) UndoWithdraw(ctx context.Context, id, pid int64) error {
	g := guard{mode: guardConflict, msg: "Rueckgaengig nicht moeglich: spaetere Ergebnisse beruhen auf dem Rueckzug. Erst diese loeschen."}
	return s.mutate(ctx, id, g, func(tx *sql.Tx, t *tournament, ev *evaluation) error {
		if _, ok := t.Withdrawn[pid]; !ok {
			return InputError("der Spieler hat sich nicht zurueckgezogen")
		}
		_, err := tx.ExecContext(ctx, `UPDATE tournament_players SET withdrawn_at = NULL, withdraw_reason = '' WHERE tournament_id = ? AND player_id = ?`, id, pid)
		return err
	})
}

// LateEntry setzt einen Nachmelder auf das Freilos der Paarung key (Runde 1),
// solange der Freilos-Empfaenger in der naechsten Runde noch nicht gespielt hat.
func (s *Service) LateEntry(ctx context.Context, id int64, key string, newID int64, newName string) error {
	g := guard{mode: guardInput, msg: "Nachmelden nicht mehr moeglich: davon haengen schon Ergebnisse ab"}
	return s.mutate(ctx, id, g, func(tx *sql.Tx, t *tournament, ev *evaluation) error {
		nd, ok := ev.L.nodes[key]
		if !ok {
			return InputError("unbekannte Paarung")
		}
		if nd.bracket != BracketMain || nd.round != 1 || !ev.L.byeMatch[nd.index] || t.Late[nd.index] != 0 {
			return InputError("diese Paarung hat kein freies Freilos")
		}
		if ev.L.rounds >= 2 {
			next := ev.memo[mkey("M", 2, nd.index/2)]
			if next.result != nil {
				return InputError("Nachmelden nicht mehr moeglich: der Freilos-Empfaenger hat in der naechsten Runde schon gespielt")
			}
		}
		np, err := newcomer(ctx, tx, t, newID, newName, -1)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO tournament_players (tournament_id, player_id, position, bye_slot) VALUES (?, ?, ?, ?)`,
			id, np, 100000+nd.index, nd.index)
		return err
	})
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

// MatchesReprocessed ist der Hook fuer einen kompletten Reprocess: alle
// Turniere werden einmal abgeglichen statt einmal pro Match. Die Zuordnung
// haengt nicht an einzelnen Match-IDs (syncTournament sucht selbst die
// passenden Matches), daher genuegt ein Durchlauf.
func (s *Service) MatchesReprocessed(ctx context.Context, tx *sql.Tx, finished []int64) error {
	last := int64(0)
	if len(finished) > 0 {
		last = finished[len(finished)-1]
	}
	return s.MatchFinished(ctx, tx, last)
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
	ro := t.roster()
	var ev *evaluation
	for range len(L.all) + 1 {
		res, err := loadResults(ctx, q, id)
		if err != nil {
			return err
		}
		ev = evaluateWith(L, res, parseTime(t.StartedAt), ro)
		assigned := false
		for _, m := range ev.ready() {
			mid, mr, err := findCandidate(ctx, q, id, m)
			if err != nil {
				return err
			}
			if mr == nil {
				continue
			}
			if err := putResult(ctx, q, id, m.node.key, m.a.player, m.b.player, mr.winner, mr.legsA, mr.legsB, mid, "auto", checkRule(m.node.rule, mr), "", s.Now()); err != nil {
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

// findCandidate sucht das erste brauchbare beendete Match genau dieser beiden
// Spieler, das nach dem Freiwerden der Paarung begonnen hat und noch nirgends
// zugeordnet ist. Unbrauchbare Matches (z.B. beendet, aber ohne Sieger)
// werden uebersprungen, damit sie die Paarung nicht dauerhaft blockieren.
func findCandidate(ctx context.Context, q querier, id int64, m *evalMatch) (int64, *matchResult, error) {
	rows, err := q.QueryContext(ctx, `SELECT mt.id FROM matches mt
		WHERE mt.finished = 1 AND mt.played_at >= ?
		  AND (SELECT COUNT(*) FROM match_players mp WHERE mp.match_id = mt.id) = 2
		  AND EXISTS (SELECT 1 FROM match_players mp WHERE mp.match_id = mt.id AND mp.player_id = ?)
		  AND EXISTS (SELECT 1 FROM match_players mp WHERE mp.match_id = mt.id AND mp.player_id = ?)
		  AND EXISTS (SELECT 1 FROM match_players mp WHERE mp.match_id = mt.id AND mp.won = 1)
		  AND NOT EXISTS (SELECT 1 FROM match_pending pe WHERE pe.match_id = mt.id)
		  AND NOT EXISTS (SELECT 1 FROM tournament_results r WHERE r.match_id = mt.id)
		  AND NOT EXISTS (SELECT 1 FROM tournament_ignored i WHERE i.match_id = mt.id AND i.tournament_id = ?)
		ORDER BY mt.played_at, mt.id`,
		fmtTime(m.readyAt.Add(-clockSkew)), m.a.player, m.b.player, id)
	if err != nil {
		return 0, nil, err
	}
	var ids []int64
	for rows.Next() {
		var mid int64
		if err := rows.Scan(&mid); err != nil {
			rows.Close()
			return 0, nil, err
		}
		ids = append(ids, mid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	for _, mid := range ids {
		mr, err := readMatch(ctx, q, mid, m.a.player, m.b.player)
		if err != nil {
			return 0, nil, err
		}
		if mr != nil {
			return mid, mr, nil
		}
	}
	return 0, nil, nil
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
	if m.a.state != slotKnown || m.b.state != slotKnown || m.status == StatusForfeit {
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
