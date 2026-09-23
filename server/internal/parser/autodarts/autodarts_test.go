package autodarts

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"autodarts-stats/internal/parser"
)

const testdata = "../../../../testdata"

func load(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testdata, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPlaceholderLegFinished(t *testing.T) {
	st, err := New().Parse("fetch", "https://api.autodarts.io/gs/v0/matches/x", load(t, "placeholder/x01_leg1_finished.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.MatchID != "11111111-2222-3333-4444-555555555555" || st.Variant != "X01" || !st.HasScoring {
		t.Fatalf("unexpected head: %+v", st)
	}
	// Autodarts zaehlt ab 0, intern ab 1.
	if st.Set != 1 || st.Leg != 1 || st.Finished || st.LegWinner != 0 || st.Winner != -1 || !st.LegFinished {
		t.Fatalf("unexpected leg info: set=%d leg=%d finished=%v legWinner=%d legFinished=%v",
			st.Set, st.Leg, st.Finished, st.LegWinner, st.LegFinished)
	}
	if len(st.Players) != 2 || st.Players[1].Name != "Jürgen Müller" || st.Players[0].IsBot {
		t.Fatalf("players: %+v", st.Players)
	}
	a := st.LegStats[0]
	if a.Darts != 11 || a.Points != 501 || a.Count180 != 1 || a.Count140Plus != 2 || a.Count100Plus != 3 || a.HighestCheckout != 100 {
		t.Errorf("leg stats A: %+v", a)
	}
	if a.Average == nil || *a.Average < 136.5 || *a.Average > 136.7 {
		t.Errorf("avg A: %v", a.Average)
	}
	if a.First9Avg == nil || *a.First9Avg < 133.6 || *a.First9Avg > 133.8 {
		t.Errorf("first9 A: %v", a.First9Avg)
	}
	b := st.LegStats[1]
	if b.Darts != 9 || b.Points != 240 || b.HighestCheckout != 0 || b.Count100Plus != 1 {
		t.Errorf("leg stats B: %+v", b)
	}
	if st.MatchStats[0].LegsWon != 1 || st.MatchStats[0].Count180 != 1 || st.MatchStats[1].Darts != 9 {
		t.Errorf("match stats: %+v", st.MatchStats)
	}
}

func TestPlaceholderFinished(t *testing.T) {
	st, err := New().Parse("fetch", "", load(t, "placeholder/x01_match_finished.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !st.Finished || st.Winner != 1 || st.LegWinner != 1 || st.Leg != 2 || !st.LegFinished {
		t.Fatalf("finished state: set=%d leg=%d winner=%d legWinner=%d", st.Set, st.Leg, st.Winner, st.LegWinner)
	}
	if st.LegStats[1].HighestCheckout != 161 || st.LegStats[1].Points != 501 || st.LegStats[1].Darts != 15 {
		t.Errorf("checkout: %+v", st.LegStats[1])
	}
	if st.MatchStats[0].CheckoutRate == nil || *st.MatchStats[0].CheckoutRate != 0.5 {
		t.Errorf("checkout rate: %+v", st.MatchStats[0])
	}
}

func TestWebSocketEnvelope(t *testing.T) {
	st, err := New().Parse("ws", "wss://api.autodarts.io/ms/v0/subscribe", load(t, "placeholder/ws_match_state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Players) != 3 || !st.Players[1].IsBot || st.Players[2].UserID != "user-uuid-anna" {
		t.Fatalf("players: %+v", st.Players)
	}
}

func TestNotRecognized(t *testing.T) {
	for _, body := range [][]byte{load(t, "placeholder/not_a_match.json"), []byte("[]"), []byte(""), []byte("kein json")} {
		_, err := New().Parse("fetch", "", body)
		if !errors.Is(err, parser.ErrNotRecognized) {
			t.Errorf("expected ErrNotRecognized, got %v", err)
		}
	}
}

// TestRealTestdata laeuft ueber echte Responses in testdata/*.json, sobald
// welche vorhanden sind. TODO(format): Erwartungen ergaenzen.
func TestRealTestdata(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(testdata, "*.json"))
	if len(files) == 0 {
		t.Skip("keine echten Responses unter testdata/")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		st, err := New().Parse("fetch", "", b)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
			continue
		}
		if st.MatchID == "" || len(st.Players) == 0 {
			t.Errorf("%s: leerer Zustand %+v", filepath.Base(f), st)
		}
	}
}

// Der WebSocket liefert den Matchzustand unter dem Topic "<matchId>.state".
// Das Payload selbst enthaelt dann keine id, sie muss aus dem Topic kommen.
func TestMatchIDAusTopic(t *testing.T) {
	st, err := New().Parse("ws", "wss://play.ws.autodarts.com/ms/v0/subscribe", load(t, "placeholder/ws_state_ohne_id.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.MatchID != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("Match-ID nicht aus dem Topic uebernommen: %q", st.MatchID)
	}
	if !st.Finished || st.Winner != 1 || !st.LegFinished {
		t.Fatalf("Zustand: finished=%v winner=%d legFinished=%v", st.Finished, st.Winner, st.LegFinished)
	}
	if len(st.Players) != 2 || st.LegStats[1].HighestCheckout != 161 {
		t.Fatalf("Spieler/Stats: %+v", st.Players)
	}
}

// Das erste und das zweite Leg muessen unterscheidbar sein, sonst wird der
// Endstand des ersten Legs nie archiviert.
func TestErstesUndZweitesLegUnterscheidbar(t *testing.T) {
	a, err := New().Parse("fetch", "", load(t, "placeholder/x01_leg1_running.json"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := New().Parse("fetch", "", load(t, "placeholder/x01_leg2_running.json"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Leg == b.Leg {
		t.Fatalf("Leg 1 und Leg 2 haben denselben Zaehler: %d", a.Leg)
	}
	if a.Leg != 1 || b.Leg != 2 {
		t.Fatalf("erwartet 1 und 2, bekommen %d und %d", a.Leg, b.Leg)
	}
}

// Echte Antwort von api.autodarts.com/gs/v0/matches/{id}, Kennungen ersetzt.
// Haelt die geprueften Eigenschaften des Formats fest.
func TestEchteAntwortInitial(t *testing.T) {
	st, err := New().Parse("fetch", "https://api.autodarts.com/gs/v0/matches/01a0ce02-394e-7b82-8540-3c49e9d8faa4", load(t, "match_x01_initial.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.MatchID != "01a0ce02-394e-7b82-8540-3c49e9d8faa4" || st.Variant != "X01" || !st.HasScoring {
		t.Fatalf("Kopf: id=%q variant=%q scoring=%v", st.MatchID, st.Variant, st.HasScoring)
	}
	// Autodarts zaehlt ab 1, ein frisches Match steht auf set=1, leg=1.
	if st.Set != 1 || st.Leg != 1 {
		t.Errorf("set=%d leg=%d, erwartet 1 und 1", st.Set, st.Leg)
	}
	if st.Finished || st.LegFinished || st.Winner != -1 || st.LegWinner != -1 {
		t.Errorf("frisches Match darf nicht beendet sein: %+v", st)
	}
	if len(st.Players) != 1 {
		t.Fatalf("Spieler: %d, erwartet 1 (Solo-Match)", len(st.Players))
	}
	p := st.Players[0]
	if p.Name != "Testspieler" || p.UserID != "00000000-0000-4000-8000-000000000001" || p.IsBot {
		t.Errorf("Spieler: %+v", p)
	}
	if st.StartedAt.IsZero() {
		t.Error("createdAt wurde nicht gelesen")
	}
	if len(st.LegStats) != 1 || st.LegStats[0].Darts != 0 || st.LegStats[0].Count180 != 0 {
		t.Errorf("LegStats: %+v", st.LegStats)
	}
}

// Die Klassen less60/plus60/plus100/plus140/plus170/total180 sind disjunkt
// und muessen kumulativ verrechnet werden.
func TestStatsKlassenKumulativ(t *testing.T) {
	got := fromFields(rawStatsFields{
		Plus100:  ptr(3),
		Plus140:  ptr(2),
		Plus170:  ptr(1),
		Total180: ptr(4),
		Score:    ptr(1234),
	})
	if got.Count180 != 4 {
		t.Errorf("180er: %d", got.Count180)
	}
	if got.Count140Plus != 7 {
		t.Errorf("140+: %d, erwartet 2+1+4", got.Count140Plus)
	}
	if got.Count100Plus != 10 {
		t.Errorf("100+: %d, erwartet 3+2+1+4", got.Count100Plus)
	}
	if got.Points != 1234 {
		t.Errorf("Punkte: %d", got.Points)
	}
}

func ptr(i int) *int { return &i }
