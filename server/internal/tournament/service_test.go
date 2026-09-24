package tournament

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"autodarts-stats/internal/db"
)

type fixture struct {
	t   *testing.T
	d   *sql.DB
	svc *Service
	ctx context.Context
	now time.Time
	seq int
}

func setup(t *testing.T) *fixture {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	f := &fixture{t: t, d: d, ctx: context.Background(), now: time.Date(2026, 9, 24, 18, 0, 0, 0, time.UTC)}
	f.svc = New(d)
	f.svc.Now = func() time.Time { return f.now }
	f.svc.Shuffle = func([]int64) {} // Reihenfolge der Anmeldung
	f.svc.IntN = func(int) int { return 0 }
	return f
}

func (f *fixture) player(name string) int64 {
	f.t.Helper()
	var id int64
	if err := f.d.QueryRow(`SELECT id FROM players WHERE display_name = ?`, name).Scan(&id); err != nil {
		f.t.Fatalf("spieler %s: %v", name, err)
	}
	return id
}

// match legt ein beendetes Match an und ruft den Ingest-Hook auf. Es
// beginnt jetzt und endet 10 Minuten spaeter; die Uhr laeuft mit.
func (f *fixture) match(a, b string, legsA, legsB int, baseScore int) int64 {
	f.t.Helper()
	f.seq++
	start := f.now
	f.now = f.now.Add(10 * time.Minute)
	settings := `{"baseScore":` + strconv.Itoa(baseScore) + `}`
	r, err := f.d.Exec(`INSERT INTO matches (autodarts_match_id, played_at, variant, settings_json, finished, raw_json, received_at, updated_at) VALUES (?, ?, 'X01', ?, 1, '{}', ?, ?)`,
		"m"+strconv.Itoa(f.seq), fmtTime(start), settings, fmtTime(f.now), fmtTime(f.now))
	if err != nil {
		f.t.Fatal(err)
	}
	mid, _ := r.LastInsertId()
	for i, p := range []struct {
		name string
		legs int
		won  bool
	}{{a, legsA, legsA > legsB}, {b, legsB, legsB > legsA}} {
		if _, err := f.d.Exec(`INSERT INTO match_players (match_id, player_id, player_index, won, legs_won) VALUES (?, ?, ?, ?, ?)`, mid, f.player(p.name), i, p.won, p.legs); err != nil {
			f.t.Fatal(err)
		}
	}
	tx, err := f.d.Begin()
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.svc.MatchFinished(f.ctx, tx, mid); err != nil {
		f.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		f.t.Fatal(err)
	}
	return mid
}

func (f *fixture) view(id int64) *View {
	f.t.Helper()
	v, err := f.svc.Get(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}

func findMatch(v *View, key string) MatchView {
	all := append([]MatchView{}, v.Next...)
	for _, r := range append(v.Main, v.LuckyRounds...) {
		all = append(all, r.Matches...)
	}
	if v.Third != nil {
		all = append(all, *v.Third)
	}
	if v.Playin != nil {
		all = append(all, *v.Playin)
	}
	for _, m := range all {
		if m.Key == key {
			return m
		}
	}
	return MatchView{}
}

func TestTournamentFlow(t *testing.T) {
	f := setup(t)
	rules := Rules{Rounds: []Rule{{Variant: "X01", BaseScore: 501, FirstTo: 3}, {Variant: "X01", BaseScore: 501, FirstTo: 2}}}
	id, err := f.svc.Create(f.ctx, Input{Name: "Herbstcup", NewNames: []string{"Anna", "Ben", "Carl", "Dora"}, ThirdPlace: true, Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	// Vor dem Start: Ergebnisse gehen nicht.
	if err := f.svc.SetResult(f.ctx, id, "M1-0", f.player("Anna"), 2, 0); err == nil {
		t.Fatal("Ergebnis vor dem Start akzeptiert")
	}
	f.match("Anna", "Ben", 2, 0, 501) // vor dem Start gespielt: zaehlt nicht
	if err := f.svc.Start(f.ctx, id); err != nil {
		t.Fatal(err)
	}
	v := f.view(id)
	if v.Status != StateRunning || len(v.Next) != 2 {
		t.Fatalf("status=%s offen=%d", v.Status, len(v.Next))
	}
	if m := findMatch(v, "M1-0"); m.Status != StatusReady {
		t.Fatalf("M1-0 = %s (Match vor dem Start darf nicht zaehlen)", m.Status)
	}

	// Nicht angesetzte Paarung: bleibt normales Match.
	casual := f.match("Anna", "Carl", 2, 1, 501)
	// Angesetzte Paarung: wird automatisch zugeordnet.
	f.match("Anna", "Ben", 2, 1, 501)
	v = f.view(id)
	m := findMatch(v, "M1-0")
	if m.Status != StatusDone || !m.A.Won || *m.A.Legs != 2 || m.Source != "auto" || m.Warning != "" {
		t.Fatalf("M1-0 nicht automatisch zugeordnet: %+v", m)
	}
	f.match("Carl", "Dora", 1, 2, 301) // falsche Startpunktzahl -> Warnung
	v = f.view(id)
	if m := findMatch(v, "M1-1"); m.Status != StatusDone || !strings.Contains(m.Warning, "301 statt 501") {
		t.Fatalf("M1-1: %+v", m)
	}
	// Das Freizeitmatch Anna-Carl lag vor dem Freiwerden des Finales.
	if m := findMatch(v, "M2-0"); m.Status != StatusReady || m.A.DisplayName != "Anna" || m.B.DisplayName != "Dora" {
		t.Fatalf("Finale: %+v", m)
	}

	// Platz 3 von Hand.
	if err := f.svc.SetResult(f.ctx, id, "T", f.player("Carl"), 2, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetResult(f.ctx, id, "T", f.player("Anna"), 2, 0); err == nil {
		t.Fatal("Sieger ausserhalb der Paarung akzeptiert")
	}
	final := f.match("Dora", "Anna", 3, 1, 501)
	v = f.view(id)
	if v.Status != StateFinished || v.Champion == nil || v.Champion.DisplayName != "Dora" {
		t.Fatalf("Turnier nicht beendet: %s %+v", v.Status, v.Champion)
	}
	places := map[string]int{}
	for _, p := range v.Standings {
		places[p.DisplayName] = p.Place
	}
	if places["Dora"] != 1 || places["Anna"] != 2 || places["Carl"] != 3 || places["Ben"] != 4 {
		t.Fatalf("Platzierungen: %v", places)
	}
	if v.Standings[0].DisplayName != "Dora" {
		t.Fatalf("Reihenfolge: %+v", v.Standings)
	}

	// Neu auslosen geht nicht mehr.
	if err := f.svc.Redraw(f.ctx, id); err == nil {
		t.Fatal("Neu auslosen trotz Ergebnissen")
	}

	// Zuordnung des Finales loesen: bleibt geloest, Turnier laeuft wieder.
	if err := f.svc.ClearResult(f.ctx, id, "M2-0"); err != nil {
		t.Fatal(err)
	}
	f.match("Anna", "Ben", 2, 0, 501) // irgendein weiteres Match loest einen Abgleich aus
	v = f.view(id)
	if v.Status != StateRunning || findMatch(v, "M2-0").Status != StatusReady {
		t.Fatalf("Finale nach Loesen: %s / %s", v.Status, findMatch(v, "M2-0").Status)
	}
	// Admin verknuepft das Finale wieder von Hand.
	cands, err := f.svc.Candidates(f.ctx, id, "M2-0")
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].MatchID != final {
		t.Fatalf("Kandidaten: %+v", cands)
	}
	if err := f.svc.LinkMatch(f.ctx, id, "M2-0", casual); err == nil {
		t.Fatal("Match anderer Spieler verknuepft")
	}
	if err := f.svc.LinkMatch(f.ctx, id, "M2-0", final); err != nil {
		t.Fatal(err)
	}
	v = f.view(id)
	if m := findMatch(v, "M2-0"); m.Status != StatusDone || m.Source != "link" || m.Warning != "" {
		t.Fatalf("Finale verknuepft: %+v", m)
	}
	if v.Status != StateFinished {
		t.Fatalf("status %s", v.Status)
	}

	// Korrektur in Runde 1: Ben statt Anna. Finale und dessen Ergebnis verfallen.
	if err := f.svc.SetResult(f.ctx, id, "M1-0", f.player("Ben"), 2, 1); err != nil {
		t.Fatal(err)
	}
	v = f.view(id)
	if m := findMatch(v, "M2-0"); m.Status != StatusReady || m.A.DisplayName != "Ben" {
		t.Fatalf("Finale nach Korrektur: %+v", m)
	}
	var n int
	f.d.QueryRow(`SELECT COUNT(*) FROM tournament_results WHERE tournament_id = ? AND match_key = 'M2-0'`, id).Scan(&n)
	if n != 0 {
		t.Fatal("verfallenes Finalergebnis nicht entfernt")
	}

	// Spielerprofil.
	h, err := f.svc.ForPlayer(f.ctx, f.player("Carl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Tournaments) != 1 || h.Tournaments[0].Place != 0 || h.Tournaments[0].Matches != 1 {
		t.Fatalf("Profil Carl: %+v", h)
	}
}

func TestStartValidation(t *testing.T) {
	f := setup(t)
	id, err := f.svc.Create(f.ctx, Input{Name: "Mini", NewNames: []string{"A", "B"}, LuckyLoser: true, LLEntry: 0})
	if err != nil {
		t.Fatal(err)
	}
	var ie InputError
	if err := f.svc.Start(f.ctx, id); !errors.As(err, &ie) {
		t.Fatalf("2 Spieler mit Lucky Loser: %v", err)
	}
	// Nach dem Umstellen geht es.
	if err := f.svc.Update(f.ctx, id, Input{Name: "Mini", NewNames: []string{"A", "B", "C"}, LuckyLoser: true, LLEntry: 0}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Start(f.ctx, id); err != nil {
		t.Fatal(err)
	}
	v := f.view(id)
	if v.Playin == nil || v.LLEntryName != "Finale" || len(v.Standings) != 3 {
		t.Fatalf("view: %+v", v)
	}
	// Nach dem Start aendern sich Teilnehmer nicht mehr.
	if err := f.svc.Update(f.ctx, id, Input{Name: "Mini 2", NewNames: []string{"A", "B"}}); err != nil {
		t.Fatal(err)
	}
	v = f.view(id)
	if v.Name != "Mini 2" || v.Players != 3 || !v.LuckyLoser {
		t.Fatalf("nach Update: %+v", v.Summary)
	}
}
