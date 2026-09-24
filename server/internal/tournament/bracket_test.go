package tournament

import (
	"fmt"
	"testing"
	"time"
)

func players(n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(i + 1)
	}
	return out
}

var t0 = time.Date(2026, 9, 24, 18, 0, 0, 0, time.UTC)

// play spielt den Baum durch; pick bestimmt den Sieger einer Paarung.
// Prueft unterwegs, dass kein Spieler gleichzeitig in zwei offenen Paarungen steht.
func play(t *testing.T, L *layout, pick func(a, b int64) int64) *evaluation {
	t.Helper()
	res := map[string]*Result{}
	clock := t0
	for step := 0; ; step++ {
		if step > 1000 {
			t.Fatal("Turnier endet nicht")
		}
		ev := evaluate(L, res, t0)
		ready := ev.ready()
		if len(ready) == 0 {
			return ev
		}
		busy := map[int64]string{}
		for _, m := range ready {
			for _, p := range []int64{m.a.player, m.b.player} {
				if k, ok := busy[p]; ok {
					t.Fatalf("Spieler %d steht gleichzeitig in %s und %s", p, k, m.node.key)
				}
				busy[p] = m.node.key
			}
			if m.a.player == m.b.player {
				t.Fatalf("%s: Spieler %d gegen sich selbst", m.node.key, m.a.player)
			}
		}
		m := ready[0]
		clock = clock.Add(time.Minute)
		res[m.node.key] = &Result{Key: m.node.key, Player1: m.a.player, Player2: m.b.player, Winner: pick(m.a.player, m.b.player), Legs1: 2, Legs2: 1, DecidedAt: clock}
	}
}

func lower(a, b int64) int64 { return min(a, b) }
func higher(a, b int64) int64 {
	return max(a, b)
}

func TestLayoutByes(t *testing.T) {
	L, err := buildLayout(Config{Order: players(11)})
	if err != nil {
		t.Fatal(err)
	}
	if L.size != 16 || L.rounds != 4 || len(L.main[0]) != 8 {
		t.Fatalf("size=%d rounds=%d r1=%d", L.size, L.rounds, len(L.main[0]))
	}
	byes, seen := 0, map[int64]bool{}
	for _, nd := range L.main[0] {
		if nd.a.kind == srcBye {
			t.Fatalf("%s: Freilos oben", nd.key)
		}
		seen[nd.a.player] = true
		if nd.b.kind == srcBye {
			byes++
		} else {
			seen[nd.b.player] = true
		}
	}
	if byes != 5 || len(seen) != 11 {
		t.Fatalf("byes=%d spieler=%d", byes, len(seen))
	}
	ev := evaluate(L, nil, t0)
	ready := 0
	for _, m := range ev.ready() {
		if m.node.round == 1 {
			ready++
		}
	}
	if ready != 3 {
		t.Fatalf("spielbereit in Runde 1 = %d, erwartet 3", ready)
	}
}

func TestRoundNames(t *testing.T) {
	got := []string{RoundName(4, 1), RoundName(4, 2), RoundName(4, 3), RoundName(4, 4), RoundName(6, 1)}
	want := []string{"Achtelfinale", "Viertelfinale", "Halbfinale", "Finale", "Runde 1"}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%d: %q statt %q", i, got[i], want[i])
		}
	}
}

// Alle Kombinationen aus Teilnehmerzahl, Lucky-Loser-Einstieg, Platz 3 und
// Gegner des Lucky Losers muessen sauber zu Ende laufen.
func TestPlayThroughAllShapes(t *testing.T) {
	for n := 2; n <= 20; n++ {
		R := RoundCount(n)
		for _, third := range []bool{false, true} {
			entries := []int{-1}
			for d := 0; EntryRound(R, d) > 0; d++ {
				entries = append(entries, d)
			}
			for _, d := range entries {
				for side := 0; side < 2; side++ {
					for _, pick := range []func(a, b int64) int64{lower, higher} {
						if third && d == 0 {
							if _, err := buildLayout(Config{Order: players(n), ThirdPlace: true, LuckyLoser: true, LLEntry: 0}); err == nil {
								t.Fatalf("n=%d: Platz 3 mit Lucky-Loser-Einstieg im Finale muesste abgelehnt werden", n)
							}
							continue
						}
						cfg := Config{Order: players(n), ThirdPlace: third, LuckyLoser: d >= 0, LLEntry: d, PlayinSide: side}
						if d >= 0 {
							cfg.PlayinMatch = (n * 7) % (1 << (R - EntryRound(R, d)))
						}
						name := fmt.Sprintf("n=%d third=%v ll=%d side=%d", n, third, d, side)
						L, err := buildLayout(cfg)
						if err != nil {
							t.Fatalf("%s: %v", name, err)
						}
						ev := play(t, L, pick)
						if !ev.complete() {
							t.Fatalf("%s: nicht abgeschlossen", name)
						}
						pl := ev.placements(cfg.Order)
						count := map[int]int{}
						for _, pid := range cfg.Order {
							p := pl[pid]
							if !p.Out || p.Place == 0 {
								t.Fatalf("%s: Spieler %d ohne Platzierung: %+v", name, pid, p)
							}
							count[p.Place]++
						}
						if count[1] != 1 {
							t.Fatalf("%s: %d Sieger", name, count[1])
						}
						if n >= 2 && count[2] != 1 {
							t.Fatalf("%s: %d Zweite", name, count[2])
						}
						if third && n >= 4 && (count[3] != 1 || count[4] != 1) {
							t.Fatalf("%s: Platz 3/4 = %d/%d", name, count[3], count[4])
						}
					}
				}
			}
		}
	}
}

func TestLuckyLoserReturns(t *testing.T) {
	// 16 Spieler, Einstieg im Halbfinale (Runde 3). Verlierer der Runden 1 und 2
	// spielen die Lucky-Loser-Runde; der Sieger trifft im Zusatzspiel auf den
	// ausgelosten Halbfinalisten (Spiel 1, unten).
	cfg := Config{Order: players(16), LuckyLoser: true, LLEntry: 1, PlayinMatch: 1, PlayinSide: 1, ThirdPlace: true}
	L, err := buildLayout(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if L.entry != 3 || len(L.ll) != 4 {
		t.Fatalf("entry=%d ll-runden=%d", L.entry, len(L.ll))
	}
	// 8 Erstrunden-Verlierer: 4 Spiele; 4 Sieger gegen 4 Zweitrunden-Verlierer; dann 2, 1.
	for i, want := range []int{4, 4, 2, 1} {
		if len(L.ll[i]) != want {
			t.Fatalf("LL-Runde %d: %d Spiele, erwartet %d", i+1, len(L.ll[i]), want)
		}
	}
	if L.main[2][1].b.kind != srcWinner || L.main[2][1].b.key != "P" {
		t.Fatalf("Halbfinale 2 unten sollte der Sieger des Zusatzspiels sein: %+v", L.main[2][1].b)
	}
	ev := play(t, L, higher)
	p := ev.memo["P"]
	if p.status != StatusDone {
		t.Fatalf("Zusatzspiel: %s", p.status)
	}
	ll := p.a.player
	if ll == 0 {
		t.Fatal("kein Lucky Loser")
	}
	pl := ev.placements(cfg.Order)
	if !pl[ll].LuckyLoser {
		t.Fatalf("Lucky Loser %d nicht markiert", ll)
	}
	if ev.final().winner.player != 16 {
		t.Fatalf("Sieger %d", ev.final().winner.player)
	}
}

func TestCorrectionInvalidatesLaterResults(t *testing.T) {
	L, _ := buildLayout(Config{Order: players(4)})
	res := map[string]*Result{
		"M1-0": {Key: "M1-0", Player1: 1, Player2: 2, Winner: 1, DecidedAt: t0.Add(time.Minute)},
		"M1-1": {Key: "M1-1", Player1: 3, Player2: 4, Winner: 3, DecidedAt: t0.Add(2 * time.Minute)},
		"M2-0": {Key: "M2-0", Player1: 1, Player2: 3, Winner: 3, DecidedAt: t0.Add(3 * time.Minute)},
	}
	ev := evaluate(L, res, t0)
	if !ev.complete() || ev.final().winner.player != 3 {
		t.Fatal("Finale sollte entschieden sein")
	}
	if !ev.memo["M2-0"].readyAt.Equal(t0.Add(2 * time.Minute)) {
		t.Fatalf("readyAt = %v", ev.memo["M2-0"].readyAt)
	}
	res["M1-0"].Winner = 2 // Korrektur: 2 hat gewonnen
	ev = evaluate(L, res, t0)
	if ev.complete() || ev.memo["M2-0"].status != StatusReady {
		t.Fatalf("Finale sollte wieder offen sein: %s", ev.memo["M2-0"].status)
	}
	if ev.usedResults()["M2-0"] {
		t.Fatal("altes Finalergebnis gilt noch")
	}
}
