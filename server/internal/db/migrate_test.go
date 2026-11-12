package db

// Migrationstest: Eine Datenbank im Stand nach 003 mit realistischen Daten
// wird auf den aktuellen Stand gehoben. Daten, Fremdschluessel, Indizes und
// die Turnieransicht muessen danach stimmen.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"autodarts-stats/internal/tournament"
)

// openRaw oeffnet die DB wie Open, aber ohne Migrationen.
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)", path)
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	d.SetMaxOpenConns(1)
	t.Cleanup(func() { d.Close() })
	return d
}

func mustExec(t *testing.T, d *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", strings.SplitN(strings.TrimSpace(q), "\n", 2)[0], err)
	}
}

// seed fuegt dieselben Daten in eine DB im Stand 003 oder neuer ein (nur
// Spalten, die es schon in 003 gibt).
//
// Spieler: Anna, Ben, Carl (Turnier), Dora (nur ein offenes Match).
// Matches: 1 und 2 beendet (Turnierspiele), 3 nicht beendet.
// Turnier 1 (beendet, 3 Spieler): M1-0 Anna gegen Freilos, M1-1 Ben 3:1 Carl,
// Finale M2-0 Anna 3:2 Ben -> Siegerin Anna.
// Turnier 2: Entwurf mit Anna und Dora, ohne Ergebnisse.
func seed(t *testing.T, d *sql.DB) {
	t.Helper()
	mustExec(t, d, `INSERT INTO boards (id, name, api_key_hash) VALUES (1, 'Board 1', 'hash1')`)
	for i, n := range []string{"Anna", "Ben", "Carl", "Dora"} {
		mustExec(t, d, `INSERT INTO players (id, display_name, normalized_name, created_at) VALUES (?, ?, ?, '2026-09-01T10:00:00.000Z')`,
			i+1, n, strings.ToLower(n))
	}
	mustExec(t, d, `INSERT INTO player_aliases (normalized_name, player_id) VALUES ('anni', 1)`)
	matches := []struct {
		id              int
		finished        int
		played, updated string
		p1, p2, l1, l2  int
		avg1, avg2      float64
	}{
		{1, 1, "2026-09-24T18:05:00.000Z", "2026-09-24T18:20:00.000Z", 2, 3, 3, 1, 61.5, 48.2},
		{2, 1, "2026-09-24T18:30:00.000Z", "2026-09-24T18:55:00.000Z", 1, 2, 3, 2, 72.25, 66.0},
		{3, 0, "2026-09-24T19:00:00.000Z", "2026-09-24T19:04:00.000Z", 4, 1, 0, 1, 40.0, 55.0},
	}
	for _, m := range matches {
		mustExec(t, d, `INSERT INTO matches (id, autodarts_match_id, board_id, played_at, variant, settings_json, finished, last_set, last_leg, raw_json, received_at, updated_at)
			VALUES (?, ?, 1, ?, 'X01', '{"baseScore":501}', ?, 1, ?, '{}', ?, ?)`,
			m.id, fmt.Sprintf("ad-%d", m.id), m.played, m.finished, m.l1+m.l2, m.played, m.updated)
		for idx, p := range []struct {
			pid, legs, other int
			avg              float64
		}{{m.p1, m.l1, m.l2, m.avg1}, {m.p2, m.l2, m.l1, m.avg2}} {
			won := 0
			if m.finished == 1 && p.legs > p.other {
				won = 1
			}
			mustExec(t, d, `INSERT INTO match_players (match_id, player_id, player_index, won, average, legs_won, legs_played, darts_thrown, count_180)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, m.id, p.pid, idx, won, p.avg, p.legs, m.l1+m.l2, 15*(m.l1+m.l2), idx)
			for leg := 1; leg <= m.l1+m.l2; leg++ {
				mustExec(t, d, `INSERT INTO match_legs (match_id, set_no, leg_no, player_id, won, darts, points, checkout, count_180)
					VALUES (?, 1, ?, ?, 0, 15, ?, ?, ?)`, m.id, leg, p.pid, int(p.avg*5), 40+leg, idx)
			}
		}
		mustExec(t, d, `INSERT INTO match_snapshots (match_id, set_no, leg_no, raw_json, received_at) VALUES (?, 1, 1, '{}', ?)`, m.id, m.played)
	}
	mustExec(t, d, `INSERT INTO unparsed_events (board_id, kind, url, reason, raw_json, received_at) VALUES (1, 'ws', 'wss://x', 'unbekannt', '{}', '2026-09-24T18:00:00.000Z')`)

	mustExec(t, d, `INSERT INTO tournaments (id, name, status, rules_json, created_at, started_at, finished_at)
		VALUES (1, 'Herbstcup', 'finished', '{}', '2026-09-24T17:00:00.000Z', '2026-09-24T18:00:00.000Z', '2026-09-24T18:55:00.000Z')`)
	for pos, pid := range []int{1, 2, 3} {
		mustExec(t, d, `INSERT INTO tournament_players (tournament_id, player_id, position) VALUES (1, ?, ?)`, pid, pos)
	}
	mustExec(t, d, `INSERT INTO tournament_results (tournament_id, match_key, player1_id, player2_id, winner_id, legs1, legs2, match_id, source, warning, created_at)
		VALUES (1, 'M1-1', 2, 3, 2, 3, 1, 1, 'auto', '', '2026-09-24T18:20:01.000Z')`)
	mustExec(t, d, `INSERT INTO tournament_results (tournament_id, match_key, player1_id, player2_id, winner_id, legs1, legs2, match_id, source, warning, created_at)
		VALUES (1, 'M2-0', 1, 2, 1, 3, 2, 2, 'link', 'Hinweis', '2026-09-24T18:56:00.000Z')`)
	mustExec(t, d, `INSERT INTO tournament_ignored (tournament_id, match_id) VALUES (1, 3)`)

	mustExec(t, d, `INSERT INTO tournaments (id, name, status, created_at) VALUES (2, 'Entwurf', 'draft', '2026-09-25T10:00:00.000Z')`)
	mustExec(t, d, `INSERT INTO tournament_players (tournament_id, player_id, position) VALUES (2, 1, 0), (2, 4, 1)`)
}

// dump liest eine Tabelle als sortierte Textzeilen (nur die angegebenen Spalten).
func dump(t *testing.T, d *sql.DB, table, cols string) []string {
	t.Helper()
	rows, err := d.Query(`SELECT ` + cols + ` FROM ` + table + ` ORDER BY ` + cols)
	if err != nil {
		t.Fatalf("%s: %v", table, err)
	}
	defer rows.Close()
	ct, _ := rows.ColumnTypes()
	var out []string
	for rows.Next() {
		vals := make([]any, len(ct))
		ptrs := make([]any, len(ct))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		parts := make([]string, len(vals))
		for i, v := range vals {
			parts[i] = fmt.Sprint(v)
		}
		out = append(out, strings.Join(parts, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// tables: Tabelle -> Spalten, die es schon in 003 gab.
var tables = map[string]string{
	"boards":             "id, name, api_key_hash",
	"players":            "id, display_name, normalized_name, autodarts_user_id, created_at",
	"player_aliases":     "normalized_name, player_id",
	"matches":            "id, autodarts_match_id, board_id, played_at, variant, settings_json, finished, last_set, last_leg, raw_json, received_at, updated_at",
	"match_snapshots":    "id, match_id, set_no, leg_no, raw_json, received_at",
	"match_players":      "match_id, player_id, player_index, won, average, legs_won, legs_played, darts_thrown, count_180",
	"match_legs":         "match_id, set_no, leg_no, player_id, won, darts, points, checkout, count_180",
	"unparsed_events":    "id, board_id, kind, url, reason, raw_json, received_at",
	"tournaments":        "id, name, status, third_place, lucky_loser, ll_entry, playin_match, playin_side, rules_json, created_at, started_at, finished_at",
	"tournament_players": "tournament_id, player_id, position",
	"tournament_results": "tournament_id, match_key, player1_id, player2_id, winner_id, legs1, legs2, match_id, source, warning, created_at",
	"tournament_ignored": "tournament_id, match_id",
}

func TestMigrateFrom003(t *testing.T) {
	ctx := context.Background()
	d := openRaw(t, filepath.Join(t.TempDir(), "old.db"))
	if err := migrateUpTo(ctx, d, "003_tournaments.sql"); err != nil {
		t.Fatal(err)
	}
	var applied int
	if err := d.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil || applied != 3 {
		t.Fatalf("bis 003 migriert: %d Migrationen (%v)", applied, err)
	}
	seed(t, d)
	before := map[string][]string{}
	for tbl, cols := range tables {
		before[tbl] = dump(t, d, tbl, cols)
	}

	if err := Migrate(ctx, d); err != nil {
		t.Fatalf("migration: %v", err)
	}
	// Erneutes Migrieren ist ein No-op.
	if err := Migrate(ctx, d); err != nil {
		t.Fatalf("zweite migration: %v", err)
	}

	// Zeilenzahl und Inhalte erhalten.
	for tbl, cols := range tables {
		after := dump(t, d, tbl, cols)
		if !reflect.DeepEqual(before[tbl], after) {
			t.Errorf("%s veraendert:\nvorher  %q\nnachher %q", tbl, before[tbl], after)
		}
	}
	if len(before["tournament_players"]) != 5 || len(before["tournament_results"]) != 2 || len(before["matches"]) != 3 {
		t.Fatalf("testdaten unvollstaendig: %v", before)
	}

	// Neue Spalten mit Standardwerten.
	if got := dump(t, d, "tournament_players", "tournament_id, player_id, bye_slot, withdrawn_at, withdraw_reason"); !reflect.DeepEqual(got, []string{
		"1|1|<nil>|<nil>|", "1|2|<nil>|<nil>|", "1|3|<nil>|<nil>|", "2|1|<nil>|<nil>|", "2|4|<nil>|<nil>|"}) {
		t.Errorf("tournament_players neue spalten: %q", got)
	}
	var badOutcome int
	if err := d.QueryRow(`SELECT COUNT(*) FROM tournament_results WHERE outcome <> ''`).Scan(&badOutcome); err != nil || badOutcome != 0 {
		t.Errorf("outcome: %d (%v)", badOutcome, err)
	}

	// finished_at: Backfill nur fuer beendete Matches, mit dem letzten Stand.
	if got := dump(t, d, "matches", "id, finished, finished_at"); !reflect.DeepEqual(got, []string{
		"1|1|2026-09-24T18:20:00.000Z", "2|1|2026-09-24T18:55:00.000Z", "3|0|<nil>"}) {
		t.Errorf("finished_at: %q", got)
	}

	// Fremdschluessel konsistent.
	fk, err := d.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	var violations []string
	for fk.Next() {
		var tbl, parent string
		var rowid, idx sql.NullInt64
		if err := fk.Scan(&tbl, &rowid, &parent, &idx); err != nil {
			t.Fatal(err)
		}
		violations = append(violations, fmt.Sprintf("%s#%d -> %s", tbl, rowid.Int64, parent))
	}
	if err := fk.Err(); err != nil {
		t.Fatal(err)
	}
	fk.Close()
	if len(violations) > 0 {
		t.Errorf("foreign_key_check: %v", violations)
	}

	// Indizes vorhanden.
	for _, idx := range []string{
		"idx_tournament_players_player", "idx_tournament_results_match",
		"idx_tournament_former_player", "idx_tournament_former_replaced",
		"idx_matches_played_at", "idx_match_players_player", "idx_match_legs_player",
	} {
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s fehlt (%v)", idx, err)
		}
	}

	// Turnieransicht: gleicher Baum und gleiche Sieger wie bei einer
	// frisch migrierten DB mit denselben Daten.
	fresh := openRaw(t, filepath.Join(t.TempDir(), "fresh.db"))
	if err := Migrate(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	seed(t, fresh)
	mustExec(t, fresh, `UPDATE matches SET finished_at = updated_at WHERE finished = 1`)
	for _, id := range []int64{1, 2} {
		got, err := tournament.New(d).Get(ctx, id)
		if err != nil {
			t.Fatalf("turnier %d nach migration: %v", id, err)
		}
		want, err := tournament.New(fresh).Get(ctx, id)
		if err != nil {
			t.Fatalf("turnier %d frisch: %v", id, err)
		}
		gj, _ := json.Marshal(got)
		wj, _ := json.Marshal(want)
		if string(gj) != string(wj) {
			t.Errorf("turnier %d weicht ab:\nmigriert %s\nfrisch   %s", id, gj, wj)
		}
	}
	v, err := tournament.New(d).Get(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if v.Champion == nil || v.Champion.PlayerID != 1 || v.Champion.DisplayName != "Anna" {
		t.Errorf("sieger: %+v", v.Champion)
	}
	if len(v.Main) != 2 || len(v.Main[0].Matches) != 2 || len(v.Main[1].Matches) != 1 {
		t.Fatalf("baum: %+v", v.Main)
	}
	bye, semi, final := v.Main[0].Matches[0], v.Main[0].Matches[1], v.Main[1].Matches[0]
	if bye.Status != tournament.StatusWalkover || bye.A.PlayerID != 1 || bye.B.Placeholder != "Freilos" {
		t.Errorf("freilos-spiel: %+v", bye)
	}
	if winnerID(semi) != 2 || semi.MatchID != 1 {
		t.Errorf("M1-1: %+v", semi)
	}
	if winnerID(final) != 1 || final.MatchID != 2 || final.Warning != "Hinweis" {
		t.Errorf("finale: %+v", final)
	}
	if v.Stats == nil || len(v.Stats.Rows) != 3 {
		t.Errorf("statistik: %+v", v.Stats)
	}

	// RESTRICT: Spieler aus einem Turnier lassen sich nicht loeschen ...
	for _, pid := range []int{1, 2, 3} {
		if _, err := d.Exec(`DELETE FROM players WHERE id = ?`, pid); err == nil {
			t.Errorf("spieler %d trotz Turnier geloescht", pid)
		}
	}
	// ... Dora steht nur im Entwurf: auch das sperrt (die API raeumt vorher auf).
	if _, err := d.Exec(`DELETE FROM players WHERE id = 4`); err == nil {
		t.Error("spieler 4 trotz Entwurf geloescht")
	}
	// Nach dem Entfernen aus dem Entwurf geht es, Match-Daten kaskadieren.
	mustExec(t, d, `DELETE FROM tournament_players WHERE player_id = 4`)
	mustExec(t, d, `DELETE FROM players WHERE id = 4`)
	var left int
	if err := d.QueryRow(`SELECT COUNT(*) FROM match_players WHERE player_id = 4`).Scan(&left); err != nil || left != 0 {
		t.Errorf("match_players von spieler 4: %d (%v)", left, err)
	}
	// Ein Turnier zu loeschen kaskadiert weiterhin.
	mustExec(t, d, `DELETE FROM tournaments WHERE id = 1`)
	for _, tbl := range []string{"tournament_players", "tournament_results", "tournament_ignored"} {
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM ` + tbl + ` WHERE tournament_id = 1`).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s nach Turnier-Loeschen: %d (%v)", tbl, n, err)
		}
	}
}

func winnerID(m tournament.MatchView) int64 {
	switch {
	case m.A.Won && !m.B.Won:
		return m.A.PlayerID
	case m.B.Won && !m.A.Won:
		return m.B.PlayerID
	}
	return 0
}
