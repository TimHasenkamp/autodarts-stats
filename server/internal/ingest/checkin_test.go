package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// stateWith baut aus dem Placeholder-Match einen Stand mit anderen Spielernamen.
func stateWith(t *testing.T, file string, names [2]string, userIDs [2]string) Event {
	t.Helper()
	e := ev(t, file)
	var m map[string]any
	if err := json.Unmarshal(e.Body, &m); err != nil {
		t.Fatal(err)
	}
	players := m["players"].([]any)
	for i := range 2 {
		p := players[i].(map[string]any)
		p["name"] = names[i]
		p["userId"] = userIDs[i]
	}
	b, _ := json.Marshal(m)
	e.Body = b
	return e
}

func TestSplitToken(t *testing.T) {
	cases := map[string][2]string{
		"Tim#4831":   {"Tim", "4831"},
		"Tim #4831":  {"Tim", "4831"},
		"Tim# 4831 ": {"Tim", "4831"},
		"Tim":        {"Tim", ""},
		"Tim#12":     {"Tim#12", ""},
		"#4831":      {"", "4831"},
		"C#Dev":      {"C#Dev", ""},
	}
	for in, want := range cases {
		b, tok := splitToken(in)
		if b != want[0] || tok != want[1] {
			t.Errorf("splitToken(%q) = %q,%q want %q,%q", in, b, tok, want[0], want[1])
		}
	}
}

func TestProtectedNameGoesPending(t *testing.T) {
	svc, d, bid := setup(t)
	ctx := context.Background()
	tim, err := svc.CreatePlayer(ctx, "Tim", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.HandleEvent(ctx, bid, ev(t, "x01_match_finished.json")); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM match_players WHERE player_id = ?`, tim); n != 0 {
		t.Fatalf("protected player got attributed: %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM match_pending WHERE suggested_id = ?`, tim); n != 1 {
		t.Fatalf("pending = %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM match_players`); n != 1 {
		t.Fatalf("opponent should still count: %d", n)
	}
	// Freigabe -> jetzt zugeordnet, Legs auch.
	if err := svc.ResolvePending(ctx, 1, 0, tim); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM match_players WHERE player_id = ?`, tim); n != 1 {
		t.Fatalf("after approve: %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM match_legs WHERE player_id = ?`, tim); n != 1 {
		t.Fatalf("legs after approve: %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM match_pending`); n != 0 {
		t.Fatalf("pending after approve: %d", n)
	}
	// Reprocess behaelt die Zuordnung.
	if _, err := svc.Reprocess(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM match_players WHERE player_id = ?`, tim); n != 1 {
		t.Fatalf("after reprocess: %d", n)
	}
}

func TestRejectPending(t *testing.T) {
	svc, d, bid := setup(t)
	ctx := context.Background()
	svc.CreatePlayer(ctx, "Tim", true)
	svc.HandleEvent(ctx, bid, ev(t, "x01_match_finished.json"))
	if err := svc.ResolvePending(ctx, 1, 0, 0); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM match_players`); n != 1 {
		t.Fatalf("ignored slot must not count: %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM players`); n != 2 {
		t.Fatalf("no new player expected: %d", n)
	}
}

func TestCheckinTokenAttribution(t *testing.T) {
	svc, d, bid := setup(t)
	ctx := context.Background()
	tim, _ := svc.CreatePlayer(ctx, "Tim", true)
	if err := svc.RegisterChip(ctx, ChipHash("04:A1:B2:C3"), tim, "Firmenchip"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CheckinByChip(ctx, bid, "deadbeef", 4*time.Hour); !errors.Is(err, ErrUnknownChip) {
		t.Fatalf("expected unknown chip, got %v", err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM unknown_chips`); n != 1 {
		t.Fatalf("unknown chip not recorded")
	}
	// Placeholder-Match spielt um 18:00; Check-in davor (Now = 18:00 im Test).
	svc.Now = func() time.Time { return time.Date(2026, 9, 20, 17, 30, 0, 0, time.UTC) }
	ci, err := svc.CheckinByChip(ctx, bid, "04a1b2c3", 4*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if ci.PlayerID != tim || !strings.HasPrefix(ci.GameName, "Tim#") || len(ci.Token) != 4 {
		t.Fatalf("checkin: %+v", ci)
	}
	// Erneut einstempeln: gleicher Code, verlaengert.
	ci2, err := svc.CheckinByChip(ctx, bid, "04A1B2C3", 4*time.Hour)
	if err != nil || ci2.Token != ci.Token || ci2.ID != ci.ID {
		t.Fatalf("renew: %+v %v", ci2, err)
	}
	active, _ := svc.ActiveCheckins(ctx, bid)
	if len(active) != 1 {
		t.Fatalf("active = %d", len(active))
	}

	svc.Now = func() time.Time { return time.Date(2026, 9, 20, 18, 30, 0, 0, time.UTC) }
	svc.HandleEvent(ctx, bid, stateWith(t, "x01_leg1_finished.json", [2]string{ci.GameName, "Anna"}, [2]string{"", ""}))
	svc.HandleEvent(ctx, bid, stateWith(t, "x01_match_finished.json", [2]string{ci.GameName, "Anna"}, [2]string{"", ""}))
	if n := count(t, d, `SELECT COUNT(*) FROM match_players WHERE player_id = ?`, tim); n != 1 {
		t.Fatalf("token attribution failed: %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM match_legs WHERE player_id = ?`, tim); n != 2 {
		t.Fatalf("legs = %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM players WHERE normalized_name LIKE 'tim#%'`); n != 0 {
		t.Fatalf("token name must not become a player")
	}
	if n := count(t, d, `SELECT COUNT(*) FROM match_overrides WHERE source = 'checkin'`); n != 1 {
		t.Fatalf("override missing")
	}
	// Falscher Code -> pending, nicht zugeordnet.
	e := stateWith(t, "x01_match_finished.json", [2]string{"Tim#0000", "Anna"}, [2]string{"", ""})
	e.Body = []byte(strings.Replace(string(e.Body), "11111111-2222-3333-4444-555555555555", "22222222-2222-3333-4444-555555555555", 1))
	svc.HandleEvent(ctx, bid, e)
	if n := count(t, d, `SELECT COUNT(*) FROM match_pending WHERE reason LIKE 'Code%'`); n != 1 {
		t.Fatalf("bad token should be pending")
	}
	// Nach Ablauf: reprocess behaelt die Zuordnung (Override).
	svc.Now = func() time.Time { return time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC) }
	if _, err := svc.Reprocess(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM match_players WHERE player_id = ?`, tim); n != 1 {
		t.Fatalf("after reprocess: %d", n)
	}
}

func TestForeignAccountDoesNotHijackProtected(t *testing.T) {
	svc, d, bid := setup(t)
	ctx := context.Background()
	tim, _ := svc.CreatePlayer(ctx, "Tim", true)
	svc.HandleEvent(ctx, bid, stateWith(t, "x01_match_finished.json", [2]string{"Tim", "Anna"}, [2]string{"user-fremd", ""}))
	if n := count(t, d, `SELECT COUNT(*) FROM players WHERE autodarts_user_id = 'user-fremd'`); n != 0 {
		t.Fatalf("foreign account must not be attached/created automatically")
	}
	if n := count(t, d, `SELECT COUNT(*) FROM match_pending WHERE suggested_id = ? AND user_id = 'user-fremd'`, tim); n != 1 {
		t.Fatalf("expected pending with user id")
	}
	// Freigabe uebernimmt die User-ID, danach zaehlt der Account direkt.
	if err := svc.ResolvePending(ctx, 1, 0, tim); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM players WHERE id = ? AND autodarts_user_id = 'user-fremd'`, tim); n != 1 {
		t.Fatalf("user id not attached on approve")
	}
	e := stateWith(t, "x01_match_finished.json", [2]string{"Tim", "Anna"}, [2]string{"user-fremd", ""})
	e.Body = []byte(strings.Replace(string(e.Body), "11111111-2222-3333-4444-555555555555", "33333333-2222-3333-4444-555555555555", 1))
	svc.HandleEvent(ctx, bid, e)
	if n := count(t, d, `SELECT COUNT(*) FROM match_players WHERE player_id = ?`, tim); n != 2 {
		t.Fatalf("account should now attribute directly: %d", n)
	}
}

func TestForeignAccountSameNameUnprotected(t *testing.T) {
	svc, d, bid := setup(t)
	ctx := context.Background()
	// Gast "Tim" spielt, dann Account A mit Namen Tim -> haengt sich an (unprotected).
	svc.HandleEvent(ctx, bid, ev(t, "x01_match_finished.json"))
	e := stateWith(t, "x01_match_finished.json", [2]string{"Tim", "Anna"}, [2]string{"user-a", ""})
	e.Body = []byte(strings.Replace(string(e.Body), "1111", "4444", 1))
	svc.HandleEvent(ctx, bid, e)
	if n := count(t, d, `SELECT COUNT(*) FROM players WHERE normalized_name = 'tim' AND autodarts_user_id = 'user-a'`); n != 1 {
		t.Fatalf("account should attach to guest player")
	}
	// Zweiter Account mit Namen Tim -> eigener Spieler "Tim (2)".
	e = stateWith(t, "x01_match_finished.json", [2]string{"Tim", "Anna"}, [2]string{"user-b", ""})
	e.Body = []byte(strings.Replace(string(e.Body), "1111", "5555", 1))
	svc.HandleEvent(ctx, bid, e)
	if n := count(t, d, `SELECT COUNT(*) FROM players WHERE normalized_name = 'tim (2)' AND autodarts_user_id = 'user-b'`); n != 1 {
		t.Fatalf("second account should get its own player")
	}
}
