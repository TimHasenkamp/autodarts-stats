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
	if st.Set != 1 || st.Leg != 1 || st.Finished || st.LegWinner != 0 || st.Winner != -1 {
		t.Fatalf("unexpected leg info: %+v", st)
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
	if !st.Finished || st.Winner != 1 || st.LegWinner != 1 || st.Leg != 2 {
		t.Fatalf("finished state: %+v", st)
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
