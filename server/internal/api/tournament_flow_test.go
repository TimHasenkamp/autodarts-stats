package api

// Ende-zu-Ende-Tests fuer Turniere ueber die echte HTTP-API (httptest, echte
// SQLite-Temp-DB). Szenarien: A manuell bis zum Sieger, B Optionen, C Korrektur
// und Redraw, D automatische Zuordnung ueber den Ingest, E Fehlerfaelle.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"autodarts-stats/internal/db"
	"autodarts-stats/internal/ingest"
	"autodarts-stats/internal/parser"
	"autodarts-stats/internal/parser/autodarts"
	"autodarts-stats/internal/stats"
	"autodarts-stats/internal/tournament"
)

type flow struct {
	*env
	t    *testing.T
	db   *sql.DB
	tour *tournament.Service
}

// newFlow baut einen Server wie newEnv, aber mit fester Auslosung
// (Reihenfolge der Anmeldung) und angemeldetem Admin. Turniere starten am
// 20.09.2026 17:00 UTC.
func newFlow(t *testing.T) *flow {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	_, key, err := CreateBoard(d, "Test")
	if err != nil {
		t.Fatal(err)
	}
	ing := ingest.New(d, []parser.Parser{autodarts.New()}, 50)
	tour := tournament.New(d)
	tour.Now = func() time.Time { return time.Date(2026, 9, 20, 17, 0, 0, 0, time.UTC) }
	tour.Shuffle = func([]int64) {}
	tour.IntN = func(int) int { return 0 }
	ing.MatchFinished = tour.MatchFinished
	ing.ReprocessFinished = tour.MatchesReprocessed
	srv := New(Options{DB: d, Ingest: ing, Stats: stats.New(d), Tournaments: tour, AdminPassword: "geheim", SessionSecret: []byte("s3cret"), CheckinTTL: 4 * time.Hour})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	f := &flow{env: &env{ts: ts, key: key, c: &http.Client{Jar: &cookieJar{}}}, t: t, db: d, tour: tour}
	if code, _ := f.do(t, "POST", "/api/admin/login", map[string]string{"password": "geheim"}, nil); code != 200 {
		t.Fatalf("login: %d", code)
	}
	return f
}

func (f *flow) expect(want int, method, path string, body any) []byte {
	f.t.Helper()
	code, out := f.do(f.t, method, path, body, nil)
	if code != want {
		f.t.Fatalf("%s %s: %d (erwartet %d): %s", method, path, code, want, out)
	}
	return out
}

// players legt Spieler "Spieler 01".. ueber die Admin-API an.
func (f *flow) players(n int) []int64 {
	f.t.Helper()
	ids := make([]int64, n)
	for i := range ids {
		out := f.expect(201, "POST", "/api/admin/players", map[string]string{"display_name": fmt.Sprintf("Spieler %02d", i+1)})
		var r struct {
			ID int64 `json:"id"`
		}
		json.Unmarshal(out, &r)
		ids[i] = r.ID
	}
	return ids
}

func (f *flow) namedPlayers(names ...string) []int64 {
	f.t.Helper()
	ids := make([]int64, len(names))
	for i, n := range names {
		out := f.expect(201, "POST", "/api/admin/players", map[string]string{"display_name": n})
		var r struct {
			ID int64 `json:"id"`
		}
		json.Unmarshal(out, &r)
		ids[i] = r.ID
	}
	return ids
}

type tin = map[string]any

func (f *flow) create(in tin) int64 {
	f.t.Helper()
	out := f.expect(201, "POST", "/api/admin/tournaments", in)
	var r struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal(out, &r)
	if r.ID == 0 {
		f.t.Fatalf("keine ID: %s", out)
	}
	return r.ID
}

func (f *flow) start(id int64) { f.t.Helper(); f.expect(200, "POST", tpath(id, "/start"), nil) }

func tpath(id int64, suffix string) string {
	return fmt.Sprintf("/api/admin/tournaments/%d%s", id, suffix)
}

func (f *flow) view(id int64) *tournament.View {
	f.t.Helper()
	out := f.expect(200, "GET", fmt.Sprintf("/api/tournaments/%d", id), nil)
	v := &tournament.View{}
	if err := json.Unmarshal(out, v); err != nil {
		f.t.Fatalf("view: %v: %s", err, out)
	}
	return v
}

func (f *flow) setResult(id int64, key string, winner int64, l1, l2 int) (int, []byte) {
	return f.do(f.t, "POST", tpath(id, "/results/"+key), map[string]any{"winner_id": winner, "legs1": l1, "legs2": l2}, nil)
}

// resultFor traegt fuer die Paarung m einen Sieger von Hand ein.
func (f *flow) resultFor(id int64, m tournament.MatchView, winner int64) {
	f.t.Helper()
	l1, l2 := 2, 0
	if winner == m.B.PlayerID {
		l1, l2 = 1, 2
	}
	if code, out := f.setResult(id, m.Key, winner, l1, l2); code != 200 {
		f.t.Fatalf("ergebnis %s: %d %s", m.Key, code, out)
	}
}

func pickLow(a, b int64) int64 { return min(a, b) }
func pickHigh(a, b int64) int64 {
	return max(a, b)
}

// playOut spielt alle spielbereiten Paarungen manuell, bis das Turnier
// beendet ist.
func (f *flow) playOut(id int64, pick func(a, b int64) int64) *tournament.View {
	f.t.Helper()
	for i := 0; i < 200; i++ {
		v := f.view(id)
		if v.Status == tournament.StateFinished {
			return v
		}
		if len(v.Next) == 0 {
			f.t.Fatalf("Turnier haengt: Status %s, keine spielbereite Paarung", v.Status)
		}
		m := v.Next[0]
		if m.A.PlayerID == 0 || m.B.PlayerID == 0 {
			f.t.Fatalf("spielbereite Paarung %s ohne zwei Spieler: %+v", m.Key, m)
		}
		f.resultFor(id, m, pick(m.A.PlayerID, m.B.PlayerID))
	}
	f.t.Fatal("Turnier wird nicht fertig")
	return nil
}

func winnerOfView(m tournament.MatchView) int64 {
	switch {
	case m.A.Won:
		return m.A.PlayerID
	case m.B.Won:
		return m.B.PlayerID
	}
	return 0
}

// checkMain prueft den Hauptbaum: jede Runde besteht aus den Siegern der
// Vorrunde an der richtigen Stelle, Freilose sind Walkover, der Sieger der
// Wahlfunktion ist Champion.
func checkMain(t *testing.T, v *tournament.View, ids []int64, want int64) {
	t.Helper()
	n := len(ids)
	if v.Status != tournament.StateFinished {
		t.Fatalf("Status %s", v.Status)
	}
	if v.Champion == nil || v.Champion.PlayerID != want {
		t.Fatalf("Sieger %+v, erwartet %d", v.Champion, want)
	}
	if len(v.Main) != tournament.RoundCount(n) {
		t.Fatalf("%d Runden, erwartet %d", len(v.Main), tournament.RoundCount(n))
	}
	done, walkover := 0, 0
	for r, round := range v.Main {
		for i, m := range round.Matches {
			switch m.Status {
			case tournament.StatusDone:
				done++
				if winnerOfView(m) == 0 || m.A.PlayerID == 0 || m.B.PlayerID == 0 {
					t.Errorf("%s: Done ohne Sieger/Spieler: %+v", m.Key, m)
				}
			case tournament.StatusWalkover:
				walkover++
				if winnerOfView(m) == 0 {
					t.Errorf("%s: Walkover ohne Sieger", m.Key)
				}
			default:
				t.Errorf("%s: Status %s im fertigen Turnier", m.Key, m.Status)
			}
			if r > 0 {
				prev := v.Main[r-1].Matches
				if m.A.PlayerID != winnerOfView(prev[2*i]) || m.B.PlayerID != winnerOfView(prev[2*i+1]) {
					t.Errorf("%s: Spieler %d/%d, erwartet Sieger %d/%d", m.Key, m.A.PlayerID, m.B.PlayerID, winnerOfView(prev[2*i]), winnerOfView(prev[2*i+1]))
				}
			}
		}
	}
	size := 1 << tournament.RoundCount(n)
	if done != n-1 || walkover != size-n {
		t.Errorf("n=%d: %d gespielt, %d Freilose; erwartet %d/%d", n, done, walkover, n-1, size-n)
	}
	if fin := v.Main[len(v.Main)-1].Matches[0]; winnerOfView(fin) != want {
		t.Errorf("Finalsieger %d", winnerOfView(fin))
	}
	if len(v.Standings) != n || v.Standings[0].PlayerID != want || v.Standings[0].Place != 1 {
		t.Errorf("Rangliste: %+v", v.Standings)
	}
	if len(v.Next) != 0 {
		t.Errorf("fertiges Turnier mit offenen Paarungen: %d", len(v.Next))
	}
}

func idsOf(ids []int64) tin { return tin{"name": "Cup", "player_ids": ids} }

func with(base tin, kv ...any) tin {
	out := tin{}
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

// ---- A: manuell bis zum Sieger ----

func TestTournamentFlowManual(t *testing.T) {
	for _, n := range []int{2, 3, 4, 5, 7, 8, 11, 16} {
		for _, strat := range []string{"niedrigste ID", "hoechste ID"} {
			t.Run(fmt.Sprintf("n=%d/%s", n, strat), func(t *testing.T) {
				f := newFlow(t)
				ids := f.players(n)
				id := f.create(tin{"name": "Cup", "player_ids": ids})
				if v := f.view(id); v.Status != tournament.StateDraft || len(v.Standings) != n {
					t.Fatalf("Entwurf: %s %d", v.Status, len(v.Standings))
				}
				// Vor dem Start kann nichts eingetragen werden.
				if code, _ := f.setResult(id, "M1-0", ids[0], 2, 0); code != 400 {
					t.Fatalf("Ergebnis im Entwurf: %d", code)
				}
				f.start(id)
				v := f.view(id)
				if v.Status != tournament.StateRunning || v.Champion != nil {
					t.Fatalf("nach Start: %s %+v", v.Status, v.Champion)
				}
				pick, want := pickLow, slices.Min(ids)
				if strat == "hoechste ID" {
					pick, want = pickHigh, slices.Max(ids)
				}
				fin := f.playOut(id, pick)
				checkMain(t, fin, ids, want)

				// Oeffentliche Liste und Spielerprofil zeigen den Sieger.
				var list []tournament.Summary
				json.Unmarshal(f.expect(200, "GET", "/api/tournaments", nil), &list)
				if len(list) != 1 || list[0].Champion == nil || list[0].Champion.PlayerID != want || list[0].Status != "finished" {
					t.Errorf("Liste: %+v", list)
				}
				var h tournament.PlayerHistory
				json.Unmarshal(f.expect(200, "GET", fmt.Sprintf("/api/players/%d/tournaments", want), nil), &h)
				if h.Wins != 1 || h.Podium != 1 {
					t.Errorf("Profil des Siegers: %+v", h)
				}
				// Eingetragene Ergebnisse bleiben nach erneutem Laden stabil.
				if again := f.view(id); again.Champion == nil || again.Champion.PlayerID != want {
					t.Errorf("Sieger nicht stabil")
				}
			})
		}
	}
}

// Die Runde rueckt erst nach, wenn beide Vorspiele entschieden sind.
func TestTournamentFlowAdvancesStepwise(t *testing.T) {
	f := newFlow(t)
	ids := f.players(4)
	id := f.create(idsOf(ids))
	f.start(id)
	v := f.view(id)
	if len(v.Next) != 2 || v.Main[1].Matches[0].Status != tournament.StatusWait {
		t.Fatalf("Start: Next=%d final=%s", len(v.Next), v.Main[1].Matches[0].Status)
	}
	if v.Main[1].Matches[0].A.Placeholder == "" {
		t.Errorf("Finale ohne Platzhalter")
	}
	f.resultFor(id, v.Main[0].Matches[0], ids[1])
	v = f.view(id)
	fin := v.Main[1].Matches[0]
	if fin.Status != tournament.StatusWait || fin.A.PlayerID != ids[1] || fin.B.PlayerID != 0 {
		t.Fatalf("Finale nach Spiel 1: %+v", fin)
	}
	f.resultFor(id, v.Main[0].Matches[1], ids[2])
	v = f.view(id)
	fin = v.Main[1].Matches[0]
	if fin.Status != tournament.StatusReady || fin.A.PlayerID != ids[1] || fin.B.PlayerID != ids[2] {
		t.Fatalf("Finale nach Spiel 2: %+v", fin)
	}
	f.resultFor(id, fin, ids[2])
	v = f.view(id)
	if v.Status != "finished" || v.Champion.PlayerID != ids[2] {
		t.Fatalf("Ende: %s %+v", v.Status, v.Champion)
	}
}

// Teilnehmer ueber new_names und ein Mix mit vorhandenen Spielern.
func TestTournamentFlowNewNames(t *testing.T) {
	f := newFlow(t)
	ids := f.namedPlayers("Anna")
	id := f.create(tin{"name": "Mix", "player_ids": ids, "new_names": []string{"Berta", "  Carl  ", "anna", ""}})
	f.start(id)
	v := f.view(id)
	if len(v.Standings) != 3 {
		t.Fatalf("Teilnehmer: %+v", v.Standings)
	}
	fin := f.playOut(id, pickLow)
	if fin.Champion.PlayerID != ids[0] {
		t.Errorf("Sieger %+v", fin.Champion)
	}
}

// ---- B: Optionen ----

func TestTournamentFlowThirdPlace(t *testing.T) {
	for _, n := range []int{4, 5, 8, 11} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			f := newFlow(t)
			ids := f.players(n)
			id := f.create(with(idsOf(ids), "name", "P3", "third_place", true))
			f.start(id)
			v := f.view(id)
			if v.Third == nil || v.Third.Status != tournament.StatusWait {
				t.Fatalf("Platz-3-Spiel vor den Halbfinals: %+v", v.Third)
			}
			// Mit dem letzten Halbfinale ist Platz 3 spielbar, das Turnier aber nicht fertig.
			for i := 0; i < 100; i++ {
				v = f.view(id)
				if v.Third.Status == tournament.StatusReady && v.Main[len(v.Main)-1].Matches[0].Status == tournament.StatusReady {
					break
				}
				f.resultFor(id, v.Next[0], pickLow(v.Next[0].A.PlayerID, v.Next[0].B.PlayerID))
			}
			if v.Third.Status != tournament.StatusReady {
				t.Fatalf("Platz 3 nicht spielbereit")
			}
			// Finale allein beendet das Turnier nicht.
			f.resultFor(id, v.Main[len(v.Main)-1].Matches[0], ids[0])
			if v := f.view(id); v.Status != tournament.StateRunning {
				t.Fatalf("Turnier ohne Platz 3 beendet: %s", v.Status)
			}
			// Die beiden Halbfinal-Verlierer; hoeherer ID gewinnt Platz 3.
			a, b := v.Third.A.PlayerID, v.Third.B.PlayerID
			f.resultFor(id, *v.Third, pickHigh(a, b))
			v = f.view(id)
			if v.Status != tournament.StateFinished || v.Champion.PlayerID != ids[0] {
				t.Fatalf("Ende: %s %+v", v.Status, v.Champion)
			}
			places := map[int]int64{}
			for _, s := range v.Standings {
				places[s.Place] = s.PlayerID
			}
			if places[1] != ids[0] || places[3] != pickHigh(a, b) || places[4] != pickLow(a, b) {
				t.Errorf("Platzierungen: %v", places)
			}
		})
	}
}

func TestTournamentFlowLuckyLoser(t *testing.T) {
	cases := []struct {
		n, entry int
		third    bool
	}{
		{8, 1, false}, {8, 1, true}, {8, 0, false},
		{16, 0, false}, {16, 1, false}, {16, 2, false}, {16, 2, true}, {16, 1, true},
		{5, 1, false}, {11, 1, false}, {11, 2, false}, {11, 0, false},
	}
	for _, c := range cases {
		for _, strat := range []string{"niedrig", "hoch"} {
			t.Run(fmt.Sprintf("n=%d/entry=%d/p3=%v/%s", c.n, c.entry, c.third, strat), func(t *testing.T) {
				f := newFlow(t)
				ids := f.players(c.n)
				id := f.create(with(idsOf(ids), "name", "LL", "lucky_loser", true, "ll_entry", c.entry, "third_place", c.third))
				if c.third && c.entry == 0 {
					// schliesst sich aus: sauber abgelehnt
					if code, out := f.do(t, "POST", tpath(id, "/start"), nil, nil); code != 400 {
						t.Fatalf("Start: %d %s", code, out)
					}
					if v := f.view(id); v.Status != tournament.StateDraft {
						t.Fatalf("Status %s", v.Status)
					}
					return
				}
				f.start(id)
				v := f.view(id)
				if v.Playin == nil || len(v.LuckyRounds) == 0 || v.LLEntryName == "" || v.Playin.Target != v.LLEntryName {
					t.Fatalf("LL-Struktur fehlt: playin=%v rounds=%d entry=%q", v.Playin, len(v.LuckyRounds), v.LLEntryName)
				}
				pick, want := pickLow, slices.Min(ids)
				if strat == "hoch" {
					pick, want = pickHigh, slices.Max(ids)
				}
				fin := f.playOut(id, pick)
				if fin.Champion == nil || fin.Champion.PlayerID != want {
					t.Fatalf("Sieger %+v, erwartet %d", fin.Champion, want)
				}
				for _, rd := range fin.LuckyRounds {
					for _, m := range rd.Matches {
						if m.Status != tournament.StatusDone && m.Status != tournament.StatusWalkover && m.Status != tournament.StatusEmpty {
							t.Errorf("LL %s: %s", m.Key, m.Status)
						}
					}
				}
				if fin.Playin.Status != tournament.StatusDone {
					t.Errorf("Zusatzspiel: %s", fin.Playin.Status)
				}
				if c.third && (fin.Third == nil || fin.Third.Status != tournament.StatusDone) {
					t.Errorf("Platz 3: %+v", fin.Third)
				}
				// Das Zusatzspiel steht im Hauptbaum am Zielplatz.
				if fin.Standings[0].PlayerID != want {
					t.Errorf("Rangliste: %+v", fin.Standings[0])
				}
			})
		}
	}
}

func TestTournamentFlowOptionConflicts(t *testing.T) {
	cases := []struct {
		name  string
		n     int
		in    tin
		start bool // true: Ablehnung darf auch erst beim Start kommen
	}{
		{"Platz 3 + LL-Einstieg im Finale", 8, tin{"third_place": true, "lucky_loser": true, "ll_entry": 0}, true},
		{"LL mit 2 Spielern", 2, tin{"lucky_loser": true, "ll_entry": 1}, true},
		{"LL mit 4 Spielern, Einstieg im Halbfinale", 4, tin{"lucky_loser": true, "ll_entry": 1}, true},
		{"LL mit 8 Spielern, Einstieg im Achtelfinale", 8, tin{"lucky_loser": true, "ll_entry": 2}, true},
		{"LL mit sehr grossem Abstand", 8, tin{"lucky_loser": true, "ll_entry": 99}, true},
		{"LL negativer Abstand", 8, tin{"lucky_loser": true, "ll_entry": -1}, true},
		{"nur 1 Spieler", 1, tin{}, true},
		{"kein Spieler", 0, tin{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFlow(t)
			ids := f.players(c.n)
			in := with(c.in, "name", "X", "player_ids", ids)
			code, out := f.do(t, "POST", "/api/admin/tournaments", in, nil)
			if code == 400 {
				return
			}
			if code != 201 {
				t.Fatalf("Create: %d %s", code, out)
			}
			var r struct {
				ID int64 `json:"id"`
			}
			json.Unmarshal(out, &r)
			if code, out := f.do(t, "POST", tpath(r.ID, "/start"), nil, nil); code != 400 {
				t.Fatalf("Start: %d %s (erwartet 400)", code, out)
			}
			if v := f.view(r.ID); v.Status != tournament.StateDraft {
				t.Fatalf("Turnier trotz Fehler gestartet: %s", v.Status)
			}
		})
	}
}

// Alle gueltigen Kombinationen aus Optionen und Teilnehmerzahl starten ohne Fehler.
func TestTournamentFlowOptionMatrix(t *testing.T) {
	for n := 2; n <= 17; n++ {
		for entry := 0; entry <= 3; entry++ {
			for _, third := range []bool{false, true} {
				for _, ll := range []bool{false, true} {
					rounds := tournament.RoundCount(n)
					valid := true
					if ll {
						e := tournament.EntryRound(rounds, entry)
						valid = e > 0 && !(e == rounds && third)
					} else if entry != 0 {
						continue
					}
					if n == 2 && third {
						// Platz 3 gibt es bei 2 Spielern nicht; muss aber nicht scheitern.
					}
					f := newFlow(t)
					ids := f.players(n)
					// M7: unmoegliche Kombinationen lehnt schon das Anlegen ab.
					code, out := f.do(t, "POST", "/api/admin/tournaments", with(idsOf(ids), "name", "M", "third_place", third, "lucky_loser", ll, "ll_entry", entry), nil)
					if code == 400 && !valid {
						continue
					}
					if code != 201 {
						t.Fatalf("n=%d ll=%v entry=%d third=%v: Create %d %s", n, ll, entry, third, code, out)
					}
					var cr struct {
						ID int64 `json:"id"`
					}
					json.Unmarshal(out, &cr)
					id := cr.ID
					code, out = f.do(t, "POST", tpath(id, "/start"), nil, nil)
					if valid && code != 200 {
						t.Fatalf("n=%d ll=%v entry=%d third=%v: Start %d %s", n, ll, entry, third, code, out)
					}
					if !valid && code != 400 {
						t.Fatalf("n=%d ll=%v entry=%d third=%v: Start %d %s (erwartet 400)", n, ll, entry, third, code, out)
					}
					if valid {
						fin := f.playOut(id, pickLow)
						if fin.Champion == nil || fin.Champion.PlayerID != slices.Min(ids) {
							t.Fatalf("n=%d ll=%v entry=%d third=%v: Sieger %+v", n, ll, entry, third, fin.Champion)
						}
					}
				}
			}
		}
	}
}

// ---- C: Korrektur und Redraw ----

// playRounds spielt ein 8er-Turnier (IDs aufsteigend) komplett, niedrige ID gewinnt.
func setup8(t *testing.T) (*flow, int64, []int64) {
	f := newFlow(t)
	ids := f.players(8)
	id := f.create(tin{"name": "Korrektur", "player_ids": ids})
	f.start(id)
	f.playOut(id, pickLow)
	return f, id, ids
}

func TestTournamentFlowCorrectionChangesWinner(t *testing.T) {
	f, id, ids := setup8(t)
	// Spieler 1 verliert Spiel 1 doch gegen Spieler 2. Ohne force: 409 mit den
	// Paarungen, deren Ergebnisse verfallen (M4); nichts wird veraendert.
	code, out := f.setResult(id, "M1-0", ids[1], 0, 2)
	var conflict struct {
		Affected []tournament.Affected `json:"affected"`
	}
	json.Unmarshal(out, &conflict)
	if code != 409 || len(conflict.Affected) != 2 || conflict.Affected[0].Key != "M2-0" || conflict.Affected[1].Key != "M3-0" {
		t.Fatalf("Korrektur ohne force: %d %s", code, out)
	}
	if v := f.view(id); v.Status != tournament.StateFinished || winnerOfView(v.Main[0].Matches[0]) != ids[0] {
		t.Fatalf("409 hat trotzdem geaendert: %s", v.Status)
	}
	if code, out := f.do(t, "POST", tpath(id, "/results/M1-0?force=1"), map[string]any{"winner_id": ids[1], "legs1": 0, "legs2": 2}, nil); code != 200 {
		t.Fatalf("Korrektur: %d %s", code, out)
	}
	v := f.view(id)
	if v.Status != tournament.StateRunning || v.Champion != nil {
		t.Fatalf("nach Korrektur: %s %+v", v.Status, v.Champion)
	}
	m := func(r, i int) tournament.MatchView { return v.Main[r].Matches[i] }
	if winnerOfView(m(0, 0)) != ids[1] || m(0, 0).Status != tournament.StatusDone {
		t.Errorf("M1-0: %+v", m(0, 0))
	}
	// Das Halbfinal-Ergebnis mit der alten Paarung (1 gegen 3) ist verfallen.
	if m(1, 0).Status != tournament.StatusReady || m(1, 0).A.PlayerID != ids[1] || m(1, 0).B.PlayerID != ids[2] {
		t.Errorf("M2-0: %+v", m(1, 0))
	}
	if m(1, 0).A.Legs != nil {
		t.Errorf("verfallenes Ergebnis noch sichtbar")
	}
	if m(2, 0).Status != tournament.StatusWait || m(2, 0).A.PlayerID != 0 {
		t.Errorf("Finale: %+v", m(2, 0))
	}
	// Unabhaengige Aeste bleiben stehen.
	for _, k := range [][2]int{{0, 1}, {0, 2}, {0, 3}, {1, 1}} {
		if m(k[0], k[1]).Status != tournament.StatusDone {
			t.Errorf("%s verloren: %s", m(k[0], k[1]).Key, m(k[0], k[1]).Status)
		}
	}
	if len(v.Next) != 1 || v.Next[0].Key != "M2-0" {
		t.Errorf("Next: %+v", v.Next)
	}
	// Das Turnier laesst sich zu Ende spielen; Spieler 2 hat nur noch Spieler 1 hinter sich gelassen.
	fin := f.playOut(id, pickLow)
	if fin.Champion.PlayerID != ids[1] {
		t.Errorf("Sieger %+v, erwartet %d", fin.Champion, ids[1])
	}
	// Spieler 1 ist nach Spiel 1 draussen.
	for _, s := range fin.Standings {
		if s.PlayerID == ids[0] && s.Place <= 4 {
			t.Errorf("Spieler 1 platziert: %+v", s)
		}
	}
}

func TestTournamentFlowCorrectionSameWinnerKeepsTree(t *testing.T) {
	f, id, ids := setup8(t)
	// Nur die Legs aendern: nichts darf verfallen.
	if code, out := f.setResult(id, "M1-0", ids[0], 2, 1); code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	v := f.view(id)
	if v.Status != tournament.StateFinished || v.Champion.PlayerID != ids[0] {
		t.Fatalf("%s %+v", v.Status, v.Champion)
	}
	if l := v.Main[0].Matches[0].B.Legs; l == nil || *l != 1 {
		t.Errorf("Legs nicht uebernommen: %+v", v.Main[0].Matches[0])
	}
}

func TestTournamentFlowDeleteResult(t *testing.T) {
	f, id, ids := setup8(t)
	// Ergebnis der frueheren Runde loeschen: alles, was davon abhaengt, verfaellt.
	// Das braucht eine Bestaetigung (M4).
	f.expect(409, "DELETE", tpath(id, "/results/M1-1"), nil)
	f.expect(200, "DELETE", tpath(id, "/results/M1-1?force=1"), nil)
	v := f.view(id)
	if v.Status != tournament.StateRunning {
		t.Fatalf("Status %s", v.Status)
	}
	st := func(r, i int) string { return v.Main[r].Matches[i].Status }
	if st(0, 1) != tournament.StatusReady || st(1, 0) != tournament.StatusWait || st(2, 0) != tournament.StatusWait {
		t.Errorf("Status nach Loeschen: %s %s %s", st(0, 1), st(1, 0), st(2, 0))
	}
	if st(0, 0) != tournament.StatusDone || st(1, 1) != tournament.StatusDone {
		t.Errorf("unabhaengige Aeste: %s %s", st(0, 0), st(1, 1))
	}
	// Zweites Loeschen desselben Ergebnisses: sauberer 400.
	if code, _ := f.do(t, "DELETE", tpath(id, "/results/M1-1"), nil, nil); code != 400 {
		t.Errorf("doppeltes Loeschen: %d", code)
	}
	// Spiel 2 diesmal mit anderem Sieger neu eintragen; die Runde danach ist offen.
	f.resultFor(id, v.Main[0].Matches[1], ids[3])
	v = f.view(id)
	if v.Main[1].Matches[0].Status != tournament.StatusReady || v.Main[1].Matches[0].B.PlayerID != ids[3] {
		t.Fatalf("Halbfinale: %+v", v.Main[1].Matches[0])
	}
	fin := f.playOut(id, pickLow)
	if fin.Champion.PlayerID != ids[0] {
		t.Errorf("Sieger %+v", fin.Champion)
	}
	// Alle Ergebnisse nacheinander (spaet nach frueh) loeschen: Turnier steht wieder am Anfang.
	for _, k := range []string{"M3-0", "M2-0", "M2-1", "M1-0", "M1-1", "M1-2", "M1-3"} {
		code, out := f.do(t, "DELETE", tpath(id, "/results/"+k), nil, nil)
		if code != 200 && code != 400 { // 400: durch fruehere Loeschung schon verfallen
			t.Fatalf("DELETE %s: %d %s", k, code, out)
		}
	}
	v = f.view(id)
	if v.Status != tournament.StateRunning || len(v.Next) != 4 || v.Champion != nil {
		t.Errorf("nach Komplett-Loeschung: %s next=%d", v.Status, len(v.Next))
	}
}

func TestTournamentFlowRedraw(t *testing.T) {
	f := newFlow(t)
	ids := f.players(4)
	id := f.create(idsOf(ids))
	// Redraw im Entwurf nicht moeglich.
	if code, _ := f.do(t, "POST", tpath(id, "/redraw"), nil, nil); code != 400 {
		t.Errorf("Redraw im Entwurf: %d", code)
	}
	f.start(id)
	// Zweiter Start nicht moeglich.
	if code, _ := f.do(t, "POST", tpath(id, "/start"), nil, nil); code != 400 {
		t.Errorf("zweiter Start: %d", code)
	}
	v := f.view(id)
	started := v.StartedAt
	if v.Main[0].Matches[0].A.PlayerID != ids[0] {
		t.Fatalf("Auslosung: %+v", v.Main[0].Matches[0])
	}
	// Neu auslosen (umgekehrte Reihenfolge).
	f.tour.Shuffle = func(o []int64) { slices.Reverse(o) }
	f.expect(200, "POST", tpath(id, "/redraw"), nil)
	v = f.view(id)
	if v.Main[0].Matches[0].A.PlayerID != ids[3] || v.Main[0].Matches[0].B.PlayerID != ids[2] || v.Status != tournament.StateRunning {
		t.Fatalf("nach Redraw: %+v", v.Main[0].Matches[0])
	}
	if v.StartedAt != started {
		t.Errorf("Startzeit veraendert")
	}
	// Mit Ergebnis: Redraw abgelehnt, nach Loeschen wieder moeglich.
	f.resultFor(id, v.Main[0].Matches[0], ids[3])
	if code, _ := f.do(t, "POST", tpath(id, "/redraw"), nil, nil); code != 400 {
		t.Errorf("Redraw mit Ergebnis: %d", code)
	}
	f.expect(200, "DELETE", tpath(id, "/results/M1-0"), nil)
	f.expect(200, "POST", tpath(id, "/redraw"), nil)
	fin := f.playOut(id, pickHigh)
	if fin.Champion.PlayerID != ids[3] {
		t.Errorf("Sieger %+v", fin.Champion)
	}
	// Beendetes Turnier laesst sich nicht neu auslosen.
	if code, _ := f.do(t, "POST", tpath(id, "/redraw"), nil, nil); code != 400 {
		t.Errorf("Redraw nach Ende: %d", code)
	}
}

// Nach dem Start lassen sich Teilnehmer nicht mehr aendern, Name schon.
func TestTournamentFlowUpdateAfterStart(t *testing.T) {
	f := newFlow(t)
	ids := f.players(4)
	id := f.create(with(idsOf(ids), "name", "Alt"))
	f.start(id)
	f.expect(200, "PUT", tpath(id, ""), with(idsOf(ids[:2]), "name", "Neu"))
	v := f.view(id)
	if v.Name != "Neu" || len(v.Standings) != 4 {
		t.Errorf("%s %d", v.Name, len(v.Standings))
	}
	fin := f.playOut(id, pickLow)
	if fin.Champion.PlayerID != ids[0] {
		t.Errorf("Sieger %+v", fin.Champion)
	}
	f.expect(200, "DELETE", tpath(id, ""), nil)
	if code, _ := f.do(t, "GET", fmt.Sprintf("/api/tournaments/%d", id), nil, nil); code != 404 {
		t.Errorf("geloescht: %d", code)
	}
}

// ---- D: automatische Zuordnung ueber den Ingest ----

// matchJSON baut aus dem Testmatch ein neues Match: Spieler A verliert 1:2,
// Spieler B gewinnt.
func matchJSON(t *testing.T, id, a, b, createdAt string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(testdata, "x01_match_finished.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range [][2]string{
		{"11111111-2222-3333-4444-555555555555", id},
		{`"name": "Tim"`, `"name": "` + a + `"`},
		{`"name": "Jürgen Müller"`, `"name": "` + b + `"`},
		{"2026-09-20T18:00:00.000Z", createdAt},
	} {
		if !bytes.Contains(raw, []byte(r[0])) {
			t.Fatalf("Testdaten enthalten %q nicht", r[0])
		}
		raw = bytes.ReplaceAll(raw, []byte(r[0]), []byte(r[1]))
	}
	return raw
}

func (f *flow) ingestRaw(raws ...[]byte) {
	f.t.Helper()
	var evs []ingest.Event
	for _, r := range raws {
		evs = append(evs, ingest.Event{Kind: "fetch", URL: "https://api.autodarts.com/gs/v0/matches/x", TS: 1, Body: r})
	}
	code, out := f.do(f.t, "POST", "/api/ingest", map[string]any{"board": "Test", "events": evs}, map[string]string{"Authorization": "Bearer " + f.key})
	if code != 200 {
		f.t.Fatalf("ingest: %d %s", code, out)
	}
}

func (f *flow) matchDBID(autodartsID string) int64 {
	f.t.Helper()
	var id int64
	if err := f.db.QueryRow(`SELECT id FROM matches WHERE autodarts_match_id = ?`, autodartsID).Scan(&id); err != nil {
		f.t.Fatalf("match %s: %v", autodartsID, err)
	}
	return id
}

const tim, juergen = "Tim", "Jürgen Müller"

func TestTournamentFlowAutoAssign(t *testing.T) {
	cases := []struct {
		name      string
		createdAt string
		want      bool
	}{
		{"nach dem Start", "2026-09-20T18:00:00.000Z", true},
		{"knapp vor dem Start (Toleranz)", "2026-09-20T16:59:30.000Z", true},
		{"deutlich vor dem Start", "2026-09-20T16:58:00.000Z", false},
		{"Stunden vor dem Start", "2026-09-20T10:00:00.000Z", false},
		{"Tage vor dem Start", "2026-09-01T10:00:00.000Z", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFlow(t)
			f.namedPlayers(tim, juergen)
			id := f.create(tin{"name": "Duell", "new_names": []string{tim, juergen}})
			f.start(id)
			f.ingestRaw(matchJSON(t, "aaaaaaaa-0000-0000-0000-000000000001", tim, juergen, c.createdAt))
			v := f.view(id)
			m := v.Main[0].Matches[0]
			if !c.want {
				if m.Status != tournament.StatusReady || v.Status != tournament.StateRunning || m.MatchID != 0 {
					t.Fatalf("faelschlich zugeordnet: %+v", m)
				}
				// Reprocess aendert daran nichts.
				f.expect(200, "POST", "/api/admin/reprocess", nil)
				if m := f.view(id).Main[0].Matches[0]; m.Status != tournament.StatusReady {
					t.Fatalf("nach Reprocess zugeordnet: %+v", m)
				}
				return
			}
			dbID := f.matchDBID("aaaaaaaa-0000-0000-0000-000000000001")
			if m.Status != tournament.StatusDone || m.Source != "auto" || m.MatchID != dbID || winnerOfView(m) != m.B.PlayerID {
				t.Fatalf("nicht zugeordnet: %+v", m)
			}
			if v.Status != tournament.StateFinished || v.Champion == nil || v.Champion.DisplayName != juergen {
				t.Fatalf("Sieger: %s %+v", v.Status, v.Champion)
			}
			if *m.A.Legs != 1 || *m.B.Legs != 2 {
				t.Errorf("Legs %d:%d", *m.A.Legs, *m.B.Legs)
			}
			if v.Stats == nil || len(v.Stats.Rows) != 2 {
				t.Errorf("Statistik: %+v", v.Stats)
			}
			// Reprocess laesst die Zuordnung stabil (zweimal).
			for i := 0; i < 2; i++ {
				f.expect(200, "POST", "/api/admin/reprocess", nil)
				v2 := f.view(id)
				m2 := v2.Main[0].Matches[0]
				if m2.Status != tournament.StatusDone || m2.Source != "auto" || v2.Champion == nil || v2.Champion.DisplayName != juergen {
					t.Fatalf("nach Reprocess %d: %+v / %+v", i+1, m2, v2.Champion)
				}
				if *m2.A.Legs != 1 || *m2.B.Legs != 2 {
					t.Errorf("Legs nach Reprocess %d:%d", *m2.A.Legs, *m2.B.Legs)
				}
			}
			// Nochmaliges Einspielen derselben Daten (Retry der Extension) aendert nichts.
			f.ingestRaw(matchJSON(t, "aaaaaaaa-0000-0000-0000-000000000001", tim, juergen, c.createdAt))
			if v2 := f.view(id); v2.Champion == nil || v2.Main[0].Matches[0].Status != tournament.StatusDone {
				t.Fatalf("nach Doppel-Ingest: %+v", v2.Champion)
			}
			// Admin loescht die Zuordnung: das Match wird nicht erneut automatisch zugeordnet.
			f.expect(200, "DELETE", tpath(id, "/results/M1-0"), nil)
			f.expect(200, "POST", "/api/admin/reprocess", nil)
			if v2 := f.view(id); v2.Status != tournament.StateRunning || v2.Main[0].Matches[0].Status != tournament.StatusReady {
				t.Fatalf("Zuordnung kam nach Loeschen zurueck: %+v", v2.Main[0].Matches[0])
			}
		})
	}
}

// Ein Match vor dem Freiwerden einer spaeteren Paarung zaehlt nicht, ein
// spaeteres schon; Reprocess haelt die Zuordnung. Gemischt mit manuellen Ergebnissen.
func TestTournamentFlowAutoAssignLaterRound(t *testing.T) {
	f := newFlow(t)
	p := f.namedPlayers(tim, juergen, "Anna", "Berta")
	id := f.create(tin{"name": "Cup", "player_ids": p})
	f.start(id)
	// Spiel 1: Tim gegen Juergen (Juergen gewinnt), automatisch.
	f.ingestRaw(matchJSON(t, "bbbbbbbb-0000-0000-0000-000000000001", tim, juergen, "2026-09-20T18:00:00.000Z"))
	v := f.view(id)
	if v.Main[0].Matches[0].Source != "auto" || winnerOfView(v.Main[0].Matches[0]) != p[1] {
		t.Fatalf("Spiel 1: %+v", v.Main[0].Matches[0])
	}
	// Spiel 2 von Hand: Anna gewinnt.
	f.resultFor(id, v.Main[0].Matches[1], p[2])
	v = f.view(id)
	fin := v.Main[1].Matches[0]
	if fin.Status != tournament.StatusReady || fin.A.PlayerID != p[1] || fin.B.PlayerID != p[2] {
		t.Fatalf("Finale: %+v", fin)
	}
	// Ein Match Juergen gegen Anna, das VOR dem Freiwerden des Finales gespielt wurde, zaehlt nicht.
	f.ingestRaw(matchJSON(t, "bbbbbbbb-0000-0000-0000-000000000002", "Anna", juergen, "2026-09-20T19:00:00.000Z"))
	if fin = f.view(id).Main[1].Matches[0]; fin.Status != tournament.StatusReady {
		t.Fatalf("zu frueh gespieltes Match zugeordnet: %+v", fin)
	}
	// Ein Match danach schon (Anna verliert, Juergen gewinnt).
	now := time.Now().UTC().Add(time.Minute).Format("2006-01-02T15:04:05.000Z")
	f.ingestRaw(matchJSON(t, "bbbbbbbb-0000-0000-0000-000000000003", "Anna", juergen, now))
	v = f.view(id)
	fin = v.Main[1].Matches[0]
	if fin.Status != tournament.StatusDone || fin.Source != "auto" || fin.MatchID != f.matchDBID("bbbbbbbb-0000-0000-0000-000000000003") {
		t.Fatalf("Finale nicht korrekt zugeordnet: %+v", fin)
	}
	if v.Status != tournament.StateFinished || v.Champion.PlayerID != p[1] {
		t.Fatalf("Ende: %s %+v", v.Status, v.Champion)
	}
	// Reprocess: gleiche Zuordnung, gleicher Sieger.
	f.expect(200, "POST", "/api/admin/reprocess", nil)
	v = f.view(id)
	fin = v.Main[1].Matches[0]
	if v.Champion == nil || v.Champion.PlayerID != p[1] || fin.Source != "auto" || fin.MatchID != f.matchDBID("bbbbbbbb-0000-0000-0000-000000000003") {
		t.Fatalf("nach Reprocess: %s %+v %+v", v.Status, v.Champion, fin)
	}
	if v.Main[0].Matches[0].Source != "auto" || v.Main[0].Matches[1].Source != "manual" {
		t.Errorf("Quellen: %q %q", v.Main[0].Matches[0].Source, v.Main[0].Matches[1].Source)
	}
	// Das Zurueckziehen des ersten Ergebnisses laesst das auto-zugeordnete Finale
	// verfallen (mit Bestaetigung, M4).
	f.expect(409, "DELETE", tpath(id, "/results/M1-0"), nil)
	f.expect(200, "DELETE", tpath(id, "/results/M1-0?force=1"), nil)
	v = f.view(id)
	if v.Status != tournament.StateRunning || v.Main[1].Matches[0].Status != tournament.StatusWait {
		t.Fatalf("nach Loeschen: %s %+v", v.Status, v.Main[1].Matches[0])
	}
}

// Ein Match zwischen Spielern, die nicht die offene Paarung bilden, wird ignoriert;
// ein Match mit dritten Spielern ebenso.
func TestTournamentFlowAutoAssignIgnoresOthers(t *testing.T) {
	f := newFlow(t)
	p := f.namedPlayers(tim, juergen, "Anna", "Berta")
	id := f.create(tin{"name": "Cup", "player_ids": p})
	f.start(id)
	// Tim gegen Anna: im Baum noch keine Paarung.
	f.ingestRaw(matchJSON(t, "cccccccc-0000-0000-0000-000000000001", tim, "Anna", "2026-09-20T18:00:00.000Z"))
	// Unbekannter Dritter gegen Tim.
	f.ingestRaw(matchJSON(t, "cccccccc-0000-0000-0000-000000000002", tim, "Fremder", "2026-09-20T18:05:00.000Z"))
	v := f.view(id)
	if len(v.Next) != 2 || v.Main[0].Matches[0].MatchID != 0 || v.Main[0].Matches[1].MatchID != 0 {
		t.Fatalf("falsche Zuordnung: %+v", v.Next)
	}
	// Ein Admin kann das passende Match aber von Hand verknuepfen (Kandidatenliste + match_id).
	f.ingestRaw(matchJSON(t, "cccccccc-0000-0000-0000-000000000003", "Berta", "Anna", "2026-09-20T18:10:00.000Z"))
	// Anna gegen Berta ist jetzt (M1-1) die Paarung Anna-Berta -> automatisch zugeordnet. Pruefen.
	v = f.view(id)
	if m := v.Main[0].Matches[1]; m.Source != "auto" || m.MatchID != f.matchDBID("cccccccc-0000-0000-0000-000000000003") {
		t.Fatalf("M1-1 sollte auto sein: %+v", m)
	}
}

// Manuelles Verknuepfen ueber match_id inklusive Fehlerfaellen.
func TestTournamentFlowLinkMatch(t *testing.T) {
	f := newFlow(t)
	p := f.namedPlayers(tim, juergen, "Anna", "Berta")
	id := f.create(tin{"name": "Cup", "player_ids": p})
	f.start(id)
	// Match vor dem Start gespielt: nicht automatisch, aber vom Admin verknuepfbar? (Kandidaten zeigen nur ab Start.)
	f.ingestRaw(matchJSON(t, "dddddddd-0000-0000-0000-000000000001", tim, juergen, "2026-09-20T18:00:00.000Z"))
	// Das wurde automatisch zugeordnet. Verknuepfen mit anderer Paarung muss scheitern.
	mid := f.matchDBID("dddddddd-0000-0000-0000-000000000001")
	if code, out := f.do(t, "POST", tpath(id, "/results/M1-1"), map[string]any{"match_id": mid}, nil); code != 400 {
		t.Errorf("fremdes Match verknuepft: %d %s", code, out)
	}
	if code, out := f.do(t, "POST", tpath(id, "/results/M1-1"), map[string]any{"match_id": 99999}, nil); code != 400 {
		t.Errorf("unbekanntes Match: %d %s", code, out)
	}
	// Kandidaten fuer eine bereits belegte Paarung: ein Eintrag mit Zuordnung.
	var cands []tournament.Candidate
	json.Unmarshal(f.expect(200, "GET", tpath(id, "/candidates/M1-0"), nil), &cands)
	if len(cands) != 1 || cands[0].MatchID != mid || cands[0].LinkedTo == "" {
		t.Errorf("Kandidaten: %+v", cands)
	}
	// Unbekannte Paarung bei Kandidaten: 400, kein 500.
	if code, _ := f.do(t, "GET", tpath(id, "/candidates/Z9-9"), nil, nil); code != 400 {
		t.Errorf("Kandidaten unbekannte Paarung: %d", code)
	}
	// Loeschen und von Hand verknuepfen (Admin-Entscheidung gilt).
	f.expect(200, "DELETE", tpath(id, "/results/M1-0"), nil)
	f.expect(200, "POST", tpath(id, "/results/M1-0"), map[string]any{"match_id": mid})
	m := f.view(id).Main[0].Matches[0]
	if m.Source != "link" || m.MatchID != mid || winnerOfView(m) != p[1] {
		t.Errorf("Link: %+v", m)
	}
}

// ---- E: Fehlerfaelle ----

func TestTournamentFlowErrors(t *testing.T) {
	f := newFlow(t)
	ids := f.players(5) // 5 Spieler: Freilose in Runde 1
	id := f.create(tin{"name": "Fehler", "player_ids": ids})
	draft := f.create(tin{"name": "Entwurf", "player_ids": ids})
	f.start(id)
	v := f.view(id)
	var bye, ready tournament.MatchView
	for _, m := range v.Main[0].Matches {
		if m.Status == tournament.StatusWalkover && bye.Key == "" {
			bye = m
		}
		if m.Status == tournament.StatusReady && ready.Key == "" {
			ready = m
		}
	}
	if bye.Key == "" || ready.Key == "" {
		t.Fatalf("Testaufbau: bye=%q ready=%q", bye.Key, ready.Key)
	}
	outsider := f.namedPlayers("Fremder")[0]
	third := ready.A.PlayerID
	_ = third
	other := ids[0]
	for _, p := range ids {
		if p != ready.A.PlayerID && p != ready.B.PlayerID {
			other = p
			break
		}
	}
	anon := &env{ts: f.ts, c: &http.Client{}}
	res := func(key string) string { return tpath(id, "/results/"+key) }
	good := map[string]any{"winner_id": ready.A.PlayerID, "legs1": 2, "legs2": 0}

	cases := []struct {
		name   string
		client *env
		method string
		path   string
		body   any
		want   int
	}{
		{"ohne Session: Ergebnis", anon, "POST", res(ready.Key), good, 401},
		{"ohne Session: loeschen", anon, "DELETE", res(ready.Key), nil, 401},
		{"ohne Session: Start", anon, "POST", tpath(draft, "/start"), nil, 401},
		{"ohne Session: Redraw", anon, "POST", tpath(id, "/redraw"), nil, 401},
		{"ohne Session: anlegen", anon, "POST", "/api/admin/tournaments", tin{"name": "x"}, 401},
		{"ohne Session: aendern", anon, "PUT", tpath(id, ""), tin{"name": "x"}, 401},
		{"ohne Session: Turnier loeschen", anon, "DELETE", tpath(id, ""), nil, 401},
		{"ohne Session: Kandidaten", anon, "GET", tpath(id, "/candidates/"+ready.Key), nil, 401},
		{"unbekannte Paarung", f.env, "POST", res("M9-9"), good, 400},
		{"Muell als Paarung", f.env, "POST", res("foo"), good, 400},
		{"Paarung noch nicht bespielbar", f.env, "POST", res("M3-0"), tin{"winner_id": ids[0], "legs1": 2, "legs2": 0}, 400},
		{"Freilos-Paarung", f.env, "POST", res(bye.Key), tin{"winner_id": bye.A.PlayerID, "legs1": 2, "legs2": 0}, 400},
		{"Sieger spielt nicht mit", f.env, "POST", res(ready.Key), tin{"winner_id": other, "legs1": 2, "legs2": 0}, 400},
		{"Sieger nicht im Turnier", f.env, "POST", res(ready.Key), tin{"winner_id": outsider, "legs1": 2, "legs2": 0}, 400},
		{"Sieger fehlt", f.env, "POST", res(ready.Key), tin{"legs1": 2, "legs2": 0}, 400},
		{"Sieger negativ", f.env, "POST", res(ready.Key), tin{"winner_id": -3, "legs1": 2, "legs2": 0}, 400},
		{"negative Legs", f.env, "POST", res(ready.Key), tin{"winner_id": ready.A.PlayerID, "legs1": -1, "legs2": 0}, 400},
		{"kaputtes JSON-Feld", f.env, "POST", res(ready.Key), tin{"winner_id": "abc"}, 400},
		{"match_id unbekannt", f.env, "POST", res(ready.Key), tin{"match_id": 424242}, 400},
		{"Ergebnis im Entwurf", f.env, "POST", tpath(draft, "/results/M1-0"), good, 400},
		{"loeschen im Entwurf", f.env, "DELETE", tpath(draft, "/results/M1-0"), nil, 400},
		{"loeschen ohne Ergebnis", f.env, "DELETE", res(ready.Key), nil, 400},
		{"Redraw im Entwurf", f.env, "POST", tpath(draft, "/redraw"), nil, 400},
		{"Start doppelt", f.env, "POST", tpath(id, "/start"), nil, 400},
		{"Turnier unbekannt: Ergebnis", f.env, "POST", tpath(9999, "/results/M1-0"), good, 404},
		{"Turnier unbekannt: loeschen", f.env, "DELETE", tpath(9999, "/results/M1-0"), nil, 404},
		{"Turnier unbekannt: Start", f.env, "POST", tpath(9999, "/start"), nil, 404},
		{"Turnier unbekannt: Redraw", f.env, "POST", tpath(9999, "/redraw"), nil, 404},
		{"Turnier unbekannt: Kandidaten", f.env, "GET", tpath(9999, "/candidates/M1-0"), nil, 404},
		{"Turnier unbekannt: aendern", f.env, "PUT", tpath(9999, ""), tin{"name": "x"}, 404},
		{"Turnier unbekannt: loeschen", f.env, "DELETE", tpath(9999, ""), nil, 404},
		{"Turnier unbekannt: lesen", f.env, "GET", "/api/tournaments/9999", nil, 404},
		{"ID keine Zahl: lesen", f.env, "GET", "/api/tournaments/abc", nil, 400},
		{"ID keine Zahl: Ergebnis", f.env, "POST", "/api/admin/tournaments/abc/results/M1-0", good, 400},
		{"ID keine Zahl: Start", f.env, "POST", "/api/admin/tournaments/abc/start", nil, 400},
		{"Name fehlt", f.env, "POST", "/api/admin/tournaments", tin{"name": "  ", "player_ids": ids}, 400},
		{"Spieler existiert nicht", f.env, "POST", "/api/admin/tournaments", tin{"name": "x", "player_ids": []int64{ids[0], 77777}}, 400},
		{"Spielerprofil unbekannt", f.env, "GET", "/api/players/99999/tournaments", nil, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out := c.client.do(t, c.method, c.path, c.body, nil)
			if code != c.want {
				t.Fatalf("%s %s: %d (erwartet %d): %s", c.method, c.path, code, c.want, out)
			}
		})
	}

	// Rohes, kaputtes JSON und leere Koerper.
	for name, raw := range map[string]string{"kein JSON": "{nope", "leer": "", "Array": "[1,2]"} {
		t.Run("Body "+name, func(t *testing.T) {
			req, _ := http.NewRequest("POST", f.ts.URL+res(ready.Key), strings.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			resp, err := f.c.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode < 400 || resp.StatusCode >= 500 {
				t.Fatalf("Status %d", resp.StatusCode)
			}
		})
	}

	// Nach all den Fehlern ist das Turnier unveraendert.
	after := f.view(id)
	if after.Status != tournament.StateRunning || len(after.Next) != len(v.Next) {
		t.Fatalf("Turnier veraendert: %s next=%d (vorher %d)", after.Status, len(after.Next), len(v.Next))
	}
	for _, rd := range after.Main {
		for _, m := range rd.Matches {
			if m.Status == tournament.StatusDone {
				t.Errorf("%s ohne Auftrag erledigt", m.Key)
			}
		}
	}
	// Der gueltige Eintrag funktioniert weiterhin.
	if code, out := f.setResult(id, ready.Key, ready.A.PlayerID, 2, 0); code != 200 {
		t.Fatalf("gueltiges Ergebnis: %d %s", code, out)
	}
}

// Kein Ergebnis im beendeten Turnier fuer eine nicht mehr relevante Paarung,
// und Fehler in der Reihenfolge (Finale vor Halbfinale).
func TestTournamentFlowOutOfOrder(t *testing.T) {
	f := newFlow(t)
	ids := f.players(4)
	id := f.create(idsOf(ids))
	f.start(id)
	if code, out := f.setResult(id, "M2-0", ids[0], 2, 0); code != 400 {
		t.Fatalf("Finale vor Halbfinale: %d %s", code, out)
	}
	if code, out := f.setResult(id, "M1-0", ids[2], 2, 0); code != 400 {
		t.Fatalf("falsches Paar: %d %s", code, out)
	}
	f.playOut(id, pickLow)
	// Fertiges Turnier: ein Ergebnis darf weiter korrigiert werden, ein falscher Sieger nicht.
	if code, out := f.setResult(id, "M2-0", ids[3], 2, 0); code != 400 {
		t.Fatalf("falscher Finalist: %d %s", code, out)
	}
	if code, out := f.setResult(id, "M2-0", ids[2], 1, 2); code != 200 { // M7: Sieger (B) braucht mehr Legs
		t.Fatalf("Finale korrigieren: %d %s", code, out)
	}
	if v := f.view(id); v.Champion.PlayerID != ids[2] {
		t.Errorf("Sieger %+v", v.Champion)
	}
}

// ---- F: Teilnehmer im laufenden Turnier aendern ----

func allMatches(v *tournament.View) []tournament.MatchView {
	var out []tournament.MatchView
	for _, r := range append(append([]tournament.RoundView{}, v.Main...), v.LuckyRounds...) {
		out = append(out, r.Matches...)
	}
	if v.Playin != nil {
		out = append(out, *v.Playin)
	}
	if v.Third != nil {
		out = append(out, *v.Third)
	}
	return out
}

func matchByKey(t *testing.T, v *tournament.View, key string) tournament.MatchView {
	t.Helper()
	for _, m := range allMatches(v) {
		if m.Key == key {
			return m
		}
	}
	t.Fatalf("Paarung %s fehlt", key)
	return tournament.MatchView{}
}

func standing(t *testing.T, v *tournament.View, pid int64) tournament.Participant {
	t.Helper()
	for _, p := range v.Standings {
		if p.PlayerID == pid {
			return p
		}
	}
	t.Fatalf("Spieler %d fehlt in der Rangliste", pid)
	return tournament.Participant{}
}

func (f *flow) playerID(name string) int64 {
	f.t.Helper()
	var id int64
	if err := f.db.QueryRow(`SELECT id FROM players WHERE display_name = ?`, name).Scan(&id); err != nil {
		f.t.Fatalf("spieler %s: %v", name, err)
	}
	return id
}

func ppath(id, pid int64, action string) string {
	return tpath(id, fmt.Sprintf("/players/%d/%s", pid, action))
}

// playSteps traegt n spielbereite Paarungen ein (niedrige ID gewinnt).
func (f *flow) playSteps(id int64, n int) {
	f.t.Helper()
	for i := 0; i < n; i++ {
		v := f.view(id)
		if len(v.Next) == 0 {
			f.t.Fatalf("keine offene Paarung nach %d Schritten", i)
		}
		m := v.Next[0]
		f.resultFor(id, m, pickLow(m.A.PlayerID, m.B.PlayerID))
	}
}

// Wechsel in jeder Runde: frueher gespielte Ergebnisse bleiben dem ersetzten
// Spieler, offene Paarungen spielt der Ersatz, nichts verfaellt.
func TestTournamentFlowReplaceEachRound(t *testing.T) {
	cases := []struct {
		name  string
		steps int    // vorher eingetragene Ergebnisse
		key   string // Paarung, deren Spieler A ersetzt wird
	}{
		{"Runde 1", 0, "M1-0"},
		{"Halbfinale", 4, "M2-0"},
		{"Finale", 6, "M3-0"},
		{"Platz 3", 6, "T"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFlow(t)
			ids := f.players(8)
			id := f.create(with(idsOf(ids), "third_place", true))
			f.start(id)
			f.playSteps(id, c.steps)
			before := f.view(id)
			m := matchByKey(t, before, c.key)
			if m.Status != tournament.StatusReady {
				t.Fatalf("%s nicht spielbereit: %s", c.key, m.Status)
			}
			x := m.A.PlayerID
			var done []tournament.MatchView
			for _, mm := range allMatches(before) {
				if mm.Status == tournament.StatusDone {
					done = append(done, mm)
				}
			}
			// Ersatz darf nicht schon im Turnier stehen.
			if code, out := f.do(t, "POST", ppath(id, x, "replace"), map[string]any{"player_id": m.B.PlayerID}, nil); code != 400 {
				t.Fatalf("Ersatz aus dem Turnier: %d %s", code, out)
			}
			f.expect(200, "POST", ppath(id, x, "replace"), map[string]any{"new_name": "Ersatz"})
			y := f.playerID("Ersatz")
			v := f.view(id)
			if got := matchByKey(t, v, c.key); got.A.PlayerID != y || got.Status != tournament.StatusReady {
				t.Fatalf("offene Paarung nach Wechsel: %+v", got)
			}
			// Gespielte Ergebnisse bleiben unveraendert (inkl. X).
			for _, d := range done {
				got := matchByKey(t, v, d.Key)
				if got.Status != tournament.StatusDone || got.A.PlayerID != d.A.PlayerID || got.B.PlayerID != d.B.PlayerID || winnerOfView(got) != winnerOfView(d) {
					t.Errorf("%s nach Wechsel veraendert: %+v -> %+v", d.Key, d, got)
				}
			}
			if s := standing(t, v, x); !s.Replaced || s.ReplacedBy == nil || s.ReplacedBy.PlayerID != y {
				t.Errorf("Ersetzter: %+v", s)
			}
			// Der ersetzte Spieler kann nicht nochmal ersetzt werden.
			if code, _ := f.do(t, "POST", ppath(id, x, "replace"), map[string]any{"new_name": "Noch einer"}, nil); code != 400 {
				t.Errorf("ersetzten Spieler erneut ersetzt: %d", code)
			}
			f.expect(200, "POST", "/api/admin/reprocess", nil)
			// pickHigh: der Ersatz hat die hoechste ID und gewinnt ab dem Wechsel alles.
			fin := f.playOut(id, pickHigh)
			if fin.Status != tournament.StateFinished || fin.Champion == nil {
				t.Fatalf("Ende: %s %+v", fin.Status, fin.Champion)
			}
			for _, d := range done {
				if got := matchByKey(t, fin, d.Key); got.Status != tournament.StatusDone || winnerOfView(got) != winnerOfView(d) {
					t.Errorf("%s am Ende veraendert", d.Key)
				}
			}
			if got := matchByKey(t, fin, c.key); winnerOfView(got) != y {
				t.Errorf("%s: Ersatz hat nicht gespielt: %+v", c.key, got)
			}
			if s := standing(t, fin, y); s.Place == 0 {
				t.Errorf("Ersatz ohne Platz: %+v", s)
			}
			if s := standing(t, fin, x); s.Place != 0 || !s.Out {
				t.Errorf("Ersetzter mit Platz: %+v", s)
			}
			if c.key != "T" && fin.Champion.PlayerID != y {
				t.Errorf("Sieger %+v, erwartet Ersatz %d", fin.Champion, y)
			}
		})
	}
}

// Wechsel in der Lucky-Loser-Runde und Wechsel zurueck.
func TestTournamentFlowReplaceLuckyLoser(t *testing.T) {
	f := newFlow(t)
	ids := f.players(8)
	id := f.create(with(idsOf(ids), "lucky_loser", true, "ll_entry", 1))
	f.start(id)
	f.playSteps(id, 4) // Runde 1
	v := f.view(id)
	l := matchByKey(t, v, "L1-0")
	if l.Status != tournament.StatusReady {
		t.Fatalf("L1-0: %+v", l)
	}
	x := l.A.PlayerID
	f.expect(200, "POST", ppath(id, x, "replace"), map[string]any{"new_name": "LL-Ersatz"})
	y := f.playerID("LL-Ersatz")
	v = f.view(id)
	if got := matchByKey(t, v, "L1-0"); got.A.PlayerID != y {
		t.Fatalf("L1-0 nach Wechsel: %+v", got)
	}
	// Die Runde-1-Niederlage bleibt X zugeordnet.
	found := false
	for _, m := range v.Main[0].Matches {
		if (m.A.PlayerID == x || m.B.PlayerID == x) && m.Status == tournament.StatusDone {
			found = true
		}
	}
	if !found {
		t.Errorf("Runde-1-Ergebnis von X fehlt")
	}
	// Zurueckwechseln auf X ist erlaubt (gleicher Platz).
	f.expect(200, "POST", ppath(id, y, "replace"), map[string]any{"player_id": x})
	if got := matchByKey(t, f.view(id), "L1-0"); got.A.PlayerID != x {
		t.Fatalf("Zurueckwechseln: %+v", got)
	}
	fin := f.playOut(id, pickLow)
	if fin.Status != tournament.StateFinished || fin.Playin.Status != tournament.StatusDone {
		t.Fatalf("Ende: %s", fin.Status)
	}
}

func TestTournamentFlowSwap(t *testing.T) {
	f := newFlow(t)
	ids := f.players(4)
	id := f.create(idsOf(ids))
	f.start(id)
	f.expect(200, "POST", ppath(id, ids[0], "swap"), map[string]any{"with": ids[3]})
	v := f.view(id)
	if m := v.Main[0].Matches[0]; m.A.PlayerID != ids[3] || m.B.PlayerID != ids[1] {
		t.Fatalf("M1-0 nach Tausch: %+v", m)
	}
	if m := v.Main[0].Matches[1]; m.B.PlayerID != ids[0] {
		t.Fatalf("M1-1 nach Tausch: %+v", m)
	}
	f.resultFor(id, v.Main[0].Matches[0], ids[3])
	// Wer gespielt hat, tauscht nicht mehr; Unsinn wird abgelehnt.
	for _, c := range []struct{ a, b int64 }{{ids[3], ids[0]}, {ids[0], ids[1]}, {ids[2], ids[2]}, {ids[2], 99999}} {
		if code, out := f.do(t, "POST", ppath(id, c.a, "swap"), map[string]any{"with": c.b}, nil); code != 400 {
			t.Errorf("Tausch %d/%d: %d %s", c.a, c.b, code, out)
		}
	}
	// Die beiden, die noch nicht gespielt haben, schon.
	f.expect(200, "POST", ppath(id, ids[0], "swap"), map[string]any{"with": ids[2]})
	if m := f.view(id).Main[0].Matches[1]; m.A.PlayerID != ids[0] || m.B.PlayerID != ids[2] {
		t.Fatalf("zweiter Tausch: %+v", m)
	}
	fin := f.playOut(id, pickLow)
	if fin.Champion.PlayerID != ids[0] {
		t.Errorf("Sieger %+v", fin.Champion)
	}
}

// Zurueckziehen vor dem ersten Spiel mit Lucky Loser und Platz 3: alle
// Paarungen des Spielers gehen kampflos verloren, das Turnier laeuft durch.
func TestTournamentFlowWithdrawChain(t *testing.T) {
	f := newFlow(t)
	ids := f.players(8)
	id := f.create(with(idsOf(ids), "lucky_loser", true, "ll_entry", 1, "third_place", true))
	f.start(id)
	if code, out := f.do(t, "POST", ppath(id, ids[0], "withdraw"), map[string]any{"reason": "foo"}, nil); code != 400 {
		t.Fatalf("Grund foo: %d %s", code, out)
	}
	f.expect(200, "POST", ppath(id, ids[0], "withdraw"), map[string]any{"reason": "retired"})
	f.expect(400, "POST", ppath(id, ids[0], "withdraw"), map[string]any{})
	v := f.view(id)
	m := v.Main[0].Matches[0]
	if m.Status != tournament.StatusForfeit || m.Outcome != tournament.OutcomeWithdrawn || !m.B.Won || m.A.Withdrawn != "retired" || m.A.Legs != nil {
		t.Fatalf("M1-0 nach Rueckzug: %+v", m)
	}
	// Kein Ergebnis fuer eine kampflos entschiedene Paarung.
	if code, _ := f.setResult(id, "M1-0", ids[0], 2, 0); code != 400 {
		t.Errorf("Ergebnis trotz Rueckzug: %d", code)
	}
	// Ersetzen eines Zurueckgezogenen: erst rueckgaengig machen.
	if code, _ := f.do(t, "POST", ppath(id, ids[0], "replace"), map[string]any{"new_name": "X"}, nil); code != 400 {
		t.Errorf("Ersetzen trotz Rueckzug: %d", code)
	}
	fin := f.playOut(id, pickLow)
	if fin.Status != tournament.StateFinished || fin.Champion == nil || fin.Champion.PlayerID != ids[1] {
		t.Fatalf("Ende: %s %+v", fin.Status, fin.Champion)
	}
	if l := matchByKey(t, fin, "L1-0"); l.Status != tournament.StatusForfeit || winnerOfView(l) == ids[0] || winnerOfView(l) == 0 {
		t.Errorf("LL-Paarung des Zurueckgezogenen: %+v", l)
	}
	s := standing(t, fin, ids[0])
	if !s.Out || s.Withdrawn != "retired" || !strings.Contains(s.Label, "zurückgezogen") {
		t.Errorf("Rangliste: %+v", s)
	}
	if fin.Standings[0].PlayerID != ids[1] {
		t.Errorf("Reihenfolge: %+v", fin.Standings)
	}
	// Rueckgaengig geht nicht mehr: spaetere Ergebnisse beruhen darauf.
	code, out := f.do(t, "DELETE", ppath(id, ids[0], "withdraw"), nil, nil)
	if code != 409 || !strings.Contains(string(out), "affected") {
		t.Fatalf("Rueckgaengig mit Folgeergebnissen: %d %s", code, out)
	}
	f.expect(200, "POST", "/api/admin/reprocess", nil)
	if v := f.view(id); v.Status != tournament.StateFinished || v.Champion.PlayerID != ids[1] {
		t.Errorf("nach Reprocess: %s", v.Status)
	}
}

// Rueckzug eines Finalisten: Finale kampflos, Platz 3 wird noch gespielt.
func TestTournamentFlowWithdrawFinalist(t *testing.T) {
	f := newFlow(t)
	ids := f.players(4)
	id := f.create(with(idsOf(ids), "third_place", true))
	f.start(id)
	f.playSteps(id, 2)
	f.expect(200, "POST", ppath(id, ids[0], "withdraw"), map[string]any{"reason": "dq"})
	v := f.view(id)
	fin := v.Main[1].Matches[0]
	if fin.Status != tournament.StatusForfeit || winnerOfView(fin) != ids[2] {
		t.Fatalf("Finale: %+v", fin)
	}
	if v.Status != tournament.StateRunning || v.Third.Status != tournament.StatusReady {
		t.Fatalf("Platz 3 offen erwartet: %s %+v", v.Status, v.Third)
	}
	// Ohne Folgeergebnis laesst sich der Rueckzug zuruecknehmen ...
	f.expect(200, "DELETE", ppath(id, ids[0], "withdraw"), nil)
	if v := f.view(id); v.Main[1].Matches[0].Status != tournament.StatusReady {
		t.Fatalf("nach Rueckgaengig: %+v", v.Main[1].Matches[0])
	}
	f.expect(400, "DELETE", ppath(id, ids[0], "withdraw"), nil)
	f.expect(200, "POST", ppath(id, ids[0], "withdraw"), map[string]any{"reason": "dq"})
	v = f.view(id)
	f.resultFor(id, *v.Third, v.Third.A.PlayerID)
	v = f.view(id)
	if v.Status != tournament.StateFinished || v.Champion.PlayerID != ids[2] {
		t.Fatalf("Ende: %s %+v", v.Status, v.Champion)
	}
	if s := standing(t, v, ids[0]); s.Place != 2 || !strings.Contains(s.Label, "disqualifiziert") {
		t.Errorf("Finalist: %+v", s)
	}
	// Ausgeschiedene koennen nicht zurueckziehen.
	if code, _ := f.do(t, "POST", ppath(id, ids[1], "withdraw"), map[string]any{}, nil); code != 400 {
		t.Errorf("Rueckzug eines Ausgeschiedenen: %d", code)
	}
}

// Aufgabe/Disqualifikation als Einzelergebnis einer Paarung.
func TestTournamentFlowRetiredResult(t *testing.T) {
	f := newFlow(t)
	ids := f.players(4)
	id := f.create(idsOf(ids))
	f.start(id)
	// Aufgabe bei 0:1 aus Sicht des Siegers: ohne Leg-Regel erlaubt.
	f.expect(200, "POST", tpath(id, "/results/M1-0"), map[string]any{"winner_id": ids[0], "legs1": 0, "legs2": 1, "outcome": "retired"})
	f.expect(200, "POST", tpath(id, "/results/M1-1"), map[string]any{"winner_id": ids[3], "outcome": "dq"})
	f.expect(400, "POST", tpath(id, "/results/M2-0"), map[string]any{"winner_id": ids[0], "outcome": "foo"})
	v := f.view(id)
	a, b := v.Main[0].Matches[0], v.Main[0].Matches[1]
	if a.Outcome != "retired" || a.A.Legs == nil || *a.B.Legs != 1 || winnerOfView(a) != ids[0] {
		t.Errorf("Aufgabe: %+v", a)
	}
	if b.Outcome != "dq" || b.A.Legs != nil || winnerOfView(b) != ids[3] {
		t.Errorf("DQ ohne kuenstliche Legs: %+v", b)
	}
	if fin := v.Main[1].Matches[0]; fin.Status != tournament.StatusReady || fin.A.PlayerID != ids[0] || fin.B.PlayerID != ids[3] {
		t.Errorf("Finale: %+v", fin)
	}
}

func TestTournamentFlowLateEntry(t *testing.T) {
	f := newFlow(t)
	ids := f.players(5) // 8 Plaetze, Freilose in M1-0, M1-1, M1-2
	id := f.create(idsOf(ids))
	f.start(id)
	v := f.view(id)
	for _, k := range []string{"M1-0", "M1-1", "M1-2"} {
		if m := matchByKey(t, v, k); m.Status != tournament.StatusWalkover || m.B.Placeholder != "Freilos" {
			t.Fatalf("Testaufbau %s: %+v", k, m)
		}
	}
	// M2-0 (Freilos-Empfaenger aus M1-0 und M1-1) spielen.
	m20 := matchByKey(t, v, "M2-0")
	f.resultFor(id, m20, m20.A.PlayerID)
	code, out := f.do(t, "POST", tpath(id, "/late/M1-0"), map[string]any{"new_name": "Zu spaet"}, nil)
	if code != 400 || !strings.Contains(string(out), "schon gespielt") {
		t.Fatalf("Nachmelden nach gespielter Folgerunde: %d %s", code, out)
	}
	for _, c := range []struct {
		key  string
		body map[string]any
	}{
		{"M1-3", map[string]any{"new_name": "Kein Freilos"}},
		{"M9-9", map[string]any{"new_name": "Unbekannt"}},
		{"M1-2", map[string]any{"player_id": ids[0]}},
		{"M1-2", map[string]any{}},
	} {
		if code, out := f.do(t, "POST", tpath(id, "/late/"+c.key), c.body, nil); code != 400 {
			t.Errorf("Nachmelden %s %v: %d %s", c.key, c.body, code, out)
		}
	}
	f.expect(200, "POST", tpath(id, "/late/M1-2"), map[string]any{"new_name": "Nachmelder"})
	late := f.playerID("Nachmelder")
	v = f.view(id)
	if m := matchByKey(t, v, "M1-2"); m.Status != tournament.StatusReady || m.B.PlayerID != late {
		t.Fatalf("M1-2 nach Nachmelden: %+v", m)
	}
	if v.Players != 6 {
		t.Errorf("Teilnehmer: %d", v.Players)
	}
	f.expect(400, "POST", tpath(id, "/late/M1-2"), map[string]any{"new_name": "Noch einer"})
	fin := f.playOut(id, pickHigh)
	if fin.Champion.PlayerID != late {
		t.Errorf("Sieger %+v, erwartet Nachmelder", fin.Champion)
	}
	if winnerOfView(matchByKey(t, fin, "M2-0")) != m20.A.PlayerID {
		t.Errorf("M2-0 veraendert")
	}
}

// Nach einem Wechsel ordnet der Ingest Matches des Ersatzspielers zu, nicht
// mehr die des ersetzten; reprocess haelt die Zuordnung.
func TestTournamentFlowAutoAssignAfterReplace(t *testing.T) {
	f := newFlow(t)
	p := f.namedPlayers(tim, juergen, "Anna", "Berta")
	id := f.create(tin{"name": "Cup", "player_ids": p})
	f.start(id)
	f.expect(200, "POST", ppath(id, p[0], "replace"), map[string]any{"new_name": "Carl"})
	carl := f.playerID("Carl")
	f.ingestRaw(matchJSON(t, "eeeeeeee-0000-0000-0000-000000000001", tim, juergen, "2026-09-20T18:00:00.000Z"))
	if m := f.view(id).Main[0].Matches[0]; m.Status != tournament.StatusReady || m.A.PlayerID != carl {
		t.Fatalf("Match des Ersetzten zugeordnet: %+v", m)
	}
	f.ingestRaw(matchJSON(t, "eeeeeeee-0000-0000-0000-000000000002", "Carl", juergen, "2026-09-20T18:10:00.000Z"))
	mid := f.matchDBID("eeeeeeee-0000-0000-0000-000000000002")
	check := func(when string) {
		t.Helper()
		m := f.view(id).Main[0].Matches[0]
		if m.Status != tournament.StatusDone || m.Source != "auto" || m.MatchID != mid || m.A.PlayerID != carl || winnerOfView(m) != p[1] {
			t.Fatalf("%s: %+v", when, m)
		}
	}
	check("nach Ingest")
	for i := 0; i < 2; i++ {
		f.expect(200, "POST", "/api/admin/reprocess", nil)
		check("nach Reprocess")
	}
	// Spaeterer Wechsel des Siegers: sein gespieltes Ergebnis bleibt, das Finale spielt der Ersatz.
	f.resultFor(id, f.view(id).Main[0].Matches[1], p[2])
	f.expect(200, "POST", ppath(id, p[1], "replace"), map[string]any{"new_name": "Dora"})
	f.expect(200, "POST", "/api/admin/reprocess", nil)
	m := f.view(id).Main[0].Matches[0]
	if m.Status != tournament.StatusDone || m.MatchID != mid || m.B.PlayerID != p[1] || winnerOfView(m) != p[1] {
		t.Fatalf("nach zweitem Wechsel und Reprocess: %+v", m)
	}
	if fin := f.view(id).Main[1].Matches[0]; fin.A.PlayerID != f.playerID("Dora") || fin.Status != tournament.StatusReady {
		t.Fatalf("Finale: %+v", fin)
	}
}

// H1: Spieler in gestarteten Turnieren lassen sich nicht loeschen oder
// zusammenfuehren; aus Entwuerfen werden sie ausgetragen.
func TestTournamentFlowPlayerDeleteProtection(t *testing.T) {
	f := newFlow(t)
	ids := f.players(7)
	free := f.namedPlayers("Frei")[0]
	id := f.create(with(idsOf(ids[:4]), "name", "Laeuft"))
	draft := f.create(with(idsOf([]int64{ids[5], ids[6], free}), "name", "Entwurf"))
	f.start(id)
	f.playSteps(id, 1)
	f.expect(200, "POST", ppath(id, ids[2], "replace"), map[string]any{"player_id": ids[4]})
	for _, pid := range []int64{ids[0], ids[2], ids[4]} { // Teilnehmer, ersetzt, Ersatz
		code, out := f.do(t, "DELETE", fmt.Sprintf("/api/admin/players/%d", pid), nil, nil)
		if code != 409 || !strings.Contains(string(out), "Laeuft") || !strings.Contains(string(out), "ersetzen") {
			t.Errorf("Loeschen %d: %d %s", pid, code, out)
		}
		code, out = f.do(t, "POST", fmt.Sprintf("/api/admin/players/%d/merge", pid), map[string]any{"into": ids[6]}, nil)
		if code != 409 {
			t.Errorf("Merge %d: %d %s", pid, code, out)
		}
	}
	// Beide im selben Entwurf: 409.
	if code, out := f.do(t, "POST", fmt.Sprintf("/api/admin/players/%d/merge", ids[5]), map[string]any{"into": free}, nil); code != 409 {
		t.Errorf("Merge im selben Turnier: %d %s", code, out)
	}
	// Nur im Entwurf: loeschen geht und traegt aus.
	f.expect(200, "DELETE", fmt.Sprintf("/api/admin/players/%d", ids[5]), nil)
	if v := f.view(draft); v.Players != 2 {
		t.Errorf("Entwurf nach Loeschen: %d", v.Players)
	}
	// Merge in einen Turnierspieler hinein ist erlaubt.
	extra := f.namedPlayers("Doppelt")[0]
	f.expect(200, "POST", fmt.Sprintf("/api/admin/players/%d/merge", extra), map[string]any{"into": ids[0]})
	// Die Datenbank schuetzt zusaetzlich (RESTRICT).
	if _, err := f.db.Exec(`DELETE FROM players WHERE id = ?`, ids[0]); err == nil {
		t.Errorf("RESTRICT greift nicht")
	}
	if v := f.view(id); v.Players != 4 {
		t.Errorf("Turnier veraendert: %d", v.Players)
	}
}

// H2: Ein beendetes Match ohne Sieger blockiert die Paarung nicht.
func TestTournamentFlowAutoAssignSkipsBrokenMatch(t *testing.T) {
	f := newFlow(t)
	p := f.namedPlayers(tim, juergen)
	id := f.create(tin{"name": "Duell", "player_ids": p})
	f.start(id)
	r, err := f.db.Exec(`INSERT INTO matches (autodarts_match_id, played_at, variant, settings_json, finished, raw_json, received_at, updated_at, finished_at)
		VALUES ('kaputt', '2026-09-20T17:30:00.000Z', 'X01', '{}', 1, '{}', '2026-09-20T17:40:00.000Z', '2026-09-20T17:40:00.000Z', '2026-09-20T17:40:00.000Z')`)
	if err != nil {
		t.Fatal(err)
	}
	broken, _ := r.LastInsertId()
	for i, pid := range p {
		if _, err := f.db.Exec(`INSERT INTO match_players (match_id, player_id, player_index, won, legs_won) VALUES (?, ?, ?, 0, 1)`, broken, pid, i); err != nil {
			t.Fatal(err)
		}
	}
	f.ingestRaw(matchJSON(t, "ffffffff-0000-0000-0000-000000000001", tim, juergen, "2026-09-20T18:00:00.000Z"))
	m := f.view(id).Main[0].Matches[0]
	if m.Status != tournament.StatusDone || m.MatchID != f.matchDBID("ffffffff-0000-0000-0000-000000000001") {
		t.Fatalf("echtes Match nicht zugeordnet: %+v", m)
	}
	// M3: Matchende stabil, auch wenn derselbe Stand erneut kommt.
	var fin1, fin2 string
	f.db.QueryRow(`SELECT finished_at FROM matches WHERE id = ?`, m.MatchID).Scan(&fin1)
	time.Sleep(5 * time.Millisecond)
	f.ingestRaw(matchJSON(t, "ffffffff-0000-0000-0000-000000000001", tim, juergen, "2026-09-20T18:00:00.000Z"))
	f.db.QueryRow(`SELECT finished_at FROM matches WHERE id = ?`, m.MatchID).Scan(&fin2)
	if fin1 == "" || fin1 != fin2 {
		t.Errorf("finished_at: %q -> %q", fin1, fin2)
	}
}

// M4: Ein verknuepftes Match, das durch ein manuelles Ergebnis ersetzt wird,
// kommt nicht automatisch zurueck.
func TestTournamentFlowManualReplacesLinked(t *testing.T) {
	f := newFlow(t)
	p := f.namedPlayers(tim, juergen)
	id := f.create(tin{"name": "Duell", "player_ids": p})
	f.start(id)
	f.ingestRaw(matchJSON(t, "abababab-0000-0000-0000-000000000001", tim, juergen, "2026-09-20T18:00:00.000Z"))
	mid := f.matchDBID("abababab-0000-0000-0000-000000000001")
	if m := f.view(id).Main[0].Matches[0]; m.MatchID != mid {
		t.Fatalf("nicht zugeordnet: %+v", m)
	}
	f.expect(200, "POST", tpath(id, "/results/M1-0"), map[string]any{"winner_id": p[0], "legs1": 3, "legs2": 1})
	var n int
	f.db.QueryRow(`SELECT COUNT(*) FROM tournament_ignored WHERE tournament_id = ? AND match_id = ?`, id, mid).Scan(&n)
	if n != 1 {
		t.Errorf("ersetztes Match nicht in tournament_ignored")
	}
	f.expect(200, "POST", "/api/admin/reprocess", nil)
	if m := f.view(id).Main[0].Matches[0]; m.Source != "manual" || winnerOfView(m) != p[0] || m.MatchID != 0 {
		t.Errorf("nach Reprocess: %+v", m)
	}
}

// M7: Validierung beim Anlegen, Aendern und Eintragen.
func TestTournamentFlowValidation(t *testing.T) {
	f := newFlow(t)
	ids := f.players(4)
	rules := func(ft int) tin { return tin{"rounds": []tin{{"variant": "X01", "base_score": 501, "first_to": ft}}} }
	for name, in := range map[string]tin{
		"First to 100":        with(idsOf(ids), "rules", rules(100)),
		"First to negativ":    with(idsOf(ids), "rules", rules(-1)),
		"Name zu lang":        with(idsOf(ids), "name", strings.Repeat("x", 101)),
		"LL mit 2 Spielern":   with(idsOf(ids[:2]), "lucky_loser", true, "ll_entry", 0),
		"Platz 3 + LL-Finale": with(idsOf(ids), "lucky_loser", true, "ll_entry", 0, "third_place", true),
		"Einstieg riesig":     with(idsOf(ids), "lucky_loser", true, "ll_entry", 50),
	} {
		if code, out := f.do(t, "POST", "/api/admin/tournaments", in, nil); code != 400 {
			t.Errorf("%s: %d %s", name, code, out)
		}
	}
	id := f.create(with(idsOf(ids), "rules", rules(99)))
	if code, _ := f.do(t, "PUT", tpath(id, ""), with(idsOf(ids), "rules", rules(0), "lucky_loser", true, "ll_entry", 1), nil); code != 400 {
		t.Errorf("Aendern auf unmoegliche Kombination: %d", code)
	}
	f.start(id)
	if code, _ := f.do(t, "PUT", tpath(id, ""), with(idsOf(ids), "rules", rules(500)), nil); code != 400 {
		t.Errorf("Aendern nach Start mit First to 500: %d", code)
	}
	for name, l := range map[string][2]int{"weniger Legs": {1, 2}, "gleich viele": {2, 2}, "zu viele": {100, 0}} {
		if code, out := f.setResult(id, "M1-0", ids[0], l[0], l[1]); code != 400 {
			t.Errorf("%s: %d %s", name, code, out)
		}
	}
	if code, out := f.setResult(id, "M1-0", ids[0], 99, 98); code != 200 {
		t.Errorf("gueltig: %d %s", code, out)
	}
}
