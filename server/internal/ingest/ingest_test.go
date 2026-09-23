package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"autodarts-stats/internal/db"
	"autodarts-stats/internal/parser"
	"autodarts-stats/internal/parser/autodarts"
)

const testdata = "../../../testdata/placeholder"

func setup(t *testing.T) (*Service, *sql.DB, int64) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	svc := New(d, []parser.Parser{autodarts.New()}, 10)
	svc.Now = func() time.Time { return time.Date(2026, 9, 20, 18, 0, 0, 0, time.UTC) }
	r, err := d.Exec(`INSERT INTO boards (name, api_key_hash) VALUES ('test', ?)`, HashKey("secret"))
	if err != nil {
		t.Fatal(err)
	}
	bid, _ := r.LastInsertId()
	return svc, d, bid
}

func ev(t *testing.T, name string) Event {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testdata, name))
	if err != nil {
		t.Fatal(err)
	}
	return Event{Kind: "fetch", URL: "https://api.autodarts.io/gs/v0/matches/x", TS: 1758391200000, Body: b}
}

func count(t *testing.T, d *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := d.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAuthBoard(t *testing.T) {
	svc, _, bid := setup(t)
	id, name, err := svc.AuthBoard(context.Background(), "secret")
	if err != nil || id != bid || name != "test" {
		t.Fatalf("auth: %v %d %q", err, id, name)
	}
	if _, _, err := svc.AuthBoard(context.Background(), "falsch"); err != ErrUnauthorized {
		t.Fatalf("expected unauthorized, got %v", err)
	}
	if _, _, err := svc.AuthBoard(context.Background(), ""); err != ErrUnauthorized {
		t.Fatalf("expected unauthorized for empty key, got %v", err)
	}
}

func TestContinuousMatchFlow(t *testing.T) {
	svc, d, bid := setup(t)
	ctx := context.Background()
	seq := []string{"x01_leg1_running.json", "x01_leg1_finished.json", "x01_leg2_running.json", "x01_match_finished.json"}
	for _, f := range seq {
		if _, err := svc.HandleEvent(ctx, bid, ev(t, f)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	if n := count(t, d, `SELECT COUNT(*) FROM matches`); n != 1 {
		t.Fatalf("matches = %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM players`); n != 2 {
		t.Fatalf("players = %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM match_snapshots`); n != 2 {
		t.Fatalf("snapshots = %d (leg1-Ende + Matchende erwartet)", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM match_legs`); n != 4 {
		t.Fatalf("match_legs = %d", n)
	}
	var won, legsWon, legsPlayed, c180, hc int
	var avg float64
	if err := d.QueryRow(`SELECT mp.won, mp.legs_won, mp.legs_played, mp.count_180, mp.highest_checkout, mp.average FROM match_players mp JOIN players p ON p.id = mp.player_id WHERE p.normalized_name = 'tim'`).
		Scan(&won, &legsWon, &legsPlayed, &c180, &hc, &avg); err != nil {
		t.Fatal(err)
	}
	if won != 0 || legsWon != 1 || legsPlayed != 2 || c180 != 1 || hc != 100 {
		t.Errorf("tim: won=%d legsWon=%d legsPlayed=%d 180=%d hc=%d", won, legsWon, legsPlayed, c180, hc)
	}
	if err := d.QueryRow(`SELECT mp.won, mp.legs_won, mp.highest_checkout FROM match_players mp JOIN players p ON p.id = mp.player_id WHERE p.normalized_name = 'juergen mueller'`).
		Scan(&won, &legsWon, &hc); err != nil {
		t.Fatal(err)
	}
	if won != 1 || legsWon != 2 || hc != 161 {
		t.Errorf("juergen: won=%d legsWon=%d hc=%d", won, legsWon, hc)
	}
	if n := count(t, d, `SELECT finished FROM matches`); n != 1 {
		t.Errorf("match not finished")
	}
}

func TestIngestIdempotent(t *testing.T) {
	svc, d, bid := setup(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := svc.HandleEvent(ctx, bid, ev(t, "x01_match_finished.json")); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, d, `SELECT COUNT(*) FROM matches`); n != 1 {
		t.Fatalf("matches = %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM match_players`); n != 2 {
		t.Fatalf("match_players = %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM match_legs`); n != 2 {
		t.Fatalf("match_legs = %d", n)
	}
	// Ein nachtraeglich eintreffender alter Stand darf das Ergebnis nicht veraendern.
	if _, err := svc.HandleEvent(ctx, bid, ev(t, "x01_leg1_running.json")); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT finished FROM matches`); n != 1 {
		t.Fatalf("finished flag lost")
	}
	if n := count(t, d, `SELECT COUNT(*) FROM match_legs`); n != 2 {
		t.Fatalf("match_legs after stale = %d", n)
	}
}

func TestBotsAndAccounts(t *testing.T) {
	svc, d, bid := setup(t)
	if _, err := svc.HandleEvent(context.Background(), bid, ev(t, "ws_match_state.json")); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM players`); n != 2 {
		t.Fatalf("players = %d (Bot muss uebersprungen werden)", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM players WHERE autodarts_user_id = 'user-uuid-anna'`); n != 1 {
		t.Fatalf("account player missing")
	}
}

func TestUnparsedStored(t *testing.T) {
	svc, d, bid := setup(t)
	res, err := svc.HandleEvent(context.Background(), bid, ev(t, "not_a_match.json"))
	if err != nil || res.Recognized {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM unparsed_events`); n != 1 {
		t.Fatalf("unparsed = %d", n)
	}
	for i := 0; i < 15; i++ {
		svc.HandleEvent(context.Background(), bid, ev(t, "not_a_match.json"))
	}
	if n := count(t, d, `SELECT COUNT(*) FROM unparsed_events`); n != 10 {
		t.Fatalf("unparsed cap = %d", n)
	}
}

func TestReprocess(t *testing.T) {
	svc, d, bid := setup(t)
	ctx := context.Background()
	for _, f := range []string{"x01_leg1_finished.json", "x01_leg2_running.json", "x01_match_finished.json"} {
		if _, err := svc.HandleEvent(ctx, bid, ev(t, f)); err != nil {
			t.Fatal(err)
		}
	}
	d.Exec(`UPDATE match_players SET count_180 = 99, won = 0`)
	d.Exec(`DELETE FROM match_legs`)
	n, err := svc.Reprocess(ctx)
	if err != nil || n != 1 {
		t.Fatalf("reprocess: %d %v", n, err)
	}
	if c := count(t, d, `SELECT COUNT(*) FROM match_legs`); c != 4 {
		t.Fatalf("match_legs after reprocess = %d", c)
	}
	if c := count(t, d, `SELECT count_180 FROM match_players mp JOIN players p ON p.id = mp.player_id WHERE p.normalized_name = 'tim'`); c != 1 {
		t.Fatalf("count_180 = %d", c)
	}
	if c := count(t, d, `SELECT SUM(won) FROM match_players`); c != 1 {
		t.Fatalf("winners = %d", c)
	}
}

func TestReprocessUnparsed(t *testing.T) {
	svc, d, bid := setup(t)
	ctx := context.Background()
	// Erst mit einem "kaputten" Parser-Satz einspielen: Event landet als unparsed.
	real := svc.Parsers
	svc.Parsers = nil
	if _, err := svc.HandleEvent(ctx, bid, ev(t, "x01_match_finished.json")); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM unparsed_events`); n != 1 {
		t.Fatalf("unparsed = %d", n)
	}
	svc.Parsers = real
	n, total, err := svc.ReprocessUnparsed(ctx)
	if err != nil || n != 1 || total != 1 {
		t.Fatalf("reprocess unparsed: %d/%d %v", n, total, err)
	}
	if c := count(t, d, `SELECT COUNT(*) FROM matches`); c != 1 {
		t.Fatalf("match not created: %d", c)
	}
	if c := count(t, d, `SELECT COUNT(*) FROM unparsed_events`); c != 0 {
		t.Fatalf("recognized event should be removed: %d", c)
	}
}

// Die Extension schickt den Antworttext als JSON-String. Frueher wurde daraus
// ein doppelt kodiertes Payload, das kein Parser erkannt hat.
func TestHandleEventStringBody(t *testing.T) {
	svc, d, bid := setup(t)
	ctx := context.Background()
	raw, err := os.ReadFile(filepath.Join(testdata, "x01_match_finished.json"))
	if err != nil {
		t.Fatal(err)
	}
	quoted, err := json.Marshal(string(raw)) // so sendet es die Extension
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.HandleEvent(ctx, bid, Event{Kind: "fetch", URL: "https://api.autodarts.io/gs/v0/matches/x", Body: quoted})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Recognized {
		t.Fatalf("String-Payload wurde nicht erkannt: %+v", res)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM matches`); n != 1 {
		t.Fatalf("matches = %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM unparsed_events`); n != 0 {
		t.Fatalf("unparsed = %d", n)
	}
	// Das gespeicherte Roh-JSON muss wieder parsebar sein (fuer reprocess).
	var stored string
	if err := d.QueryRow(`SELECT raw_json FROM matches`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(stored), &obj); err != nil {
		t.Fatalf("gespeichertes raw_json ist kein Objekt: %v", err)
	}
	if obj["id"] != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("falsche Match-ID gespeichert: %v", obj["id"])
	}
	// Und ein identisches Event darf kein zweites Match anlegen.
	if _, err := svc.HandleEvent(ctx, bid, Event{Kind: "fetch", Body: quoted}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM matches`); n != 1 {
		t.Fatalf("idempotenz verletzt: %d", n)
	}
}

// wsEvent verpackt einen Matchzustand so, wie ihn der Autodarts-WebSocket
// liefert: Huelle mit channel/topic, die Match-ID steckt nur im Topic.
func wsEvent(t *testing.T, file, matchID string) Event {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(testdata, file))
	if err != nil {
		t.Fatal(err)
	}
	var inner map[string]any
	if err := json.Unmarshal(raw, &inner); err != nil {
		t.Fatal(err)
	}
	delete(inner, "id")
	env, err := json.Marshal(map[string]any{
		"type": "message", "channel": "autodarts.matches",
		"topic": matchID + ".state", "data": inner,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Die Extension reicht den Text roh weiter, also als JSON-String.
	quoted, _ := json.Marshal(string(env))
	return Event{Kind: "ws", URL: "wss://play.ws.autodarts.com/ms/v0/subscribe", Body: quoted}
}

// Echter Ablauf: einmal REST beim Laden, danach nur noch WebSocket.
func TestFlussRestDannWebsocket(t *testing.T) {
	svc, d, bid := setup(t)
	ctx := context.Background()
	const mid = "11111111-2222-3333-4444-555555555555"

	if _, err := svc.HandleEvent(ctx, bid, ev(t, "x01_leg1_running.json")); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"x01_leg1_finished.json", "x01_leg2_running.json", "x01_match_finished.json"} {
		res, err := svc.HandleEvent(ctx, bid, wsEvent(t, f, mid))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if !res.Recognized || res.MatchID != mid {
			t.Fatalf("%s nicht erkannt: %+v", f, res)
		}
	}
	if n := count(t, d, `SELECT COUNT(*) FROM matches`); n != 1 {
		t.Fatalf("matches = %d (Match-ID aus dem Topic muss dasselbe Match treffen)", n)
	}
	if n := count(t, d, `SELECT finished FROM matches`); n != 1 {
		t.Fatal("Match wurde nie als beendet erkannt")
	}
	if n := count(t, d, `SELECT COUNT(*) FROM match_legs`); n != 4 {
		t.Fatalf("match_legs = %d, erwartet 4 (zwei Legs mal zwei Spieler)", n)
	}
	var legsWon, legsPlayed int
	var avg float64
	if err := d.QueryRow(`SELECT mp.legs_won, mp.legs_played, mp.average FROM match_players mp
		JOIN players p ON p.id = mp.player_id WHERE p.normalized_name = 'juergen mueller'`).Scan(&legsWon, &legsPlayed, &avg); err != nil {
		t.Fatal(err)
	}
	if legsWon != 2 || legsPlayed != 2 {
		t.Errorf("Jürgen: legsWon=%d legsPlayed=%d", legsWon, legsPlayed)
	}
}

// Das erste Leg muss archiviert werden, sobald gameFinished gemeldet wird,
// auch wenn danach kein weiteres Leg mehr kommt.
func TestErstesLegWirdSofortArchiviert(t *testing.T) {
	svc, d, bid := setup(t)
	ctx := context.Background()
	if _, err := svc.HandleEvent(ctx, bid, ev(t, "x01_leg1_running.json")); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM match_legs`); n != 0 {
		t.Fatalf("laufendes Leg darf noch nicht archiviert sein: %d", n)
	}
	res, err := svc.HandleEvent(ctx, bid, ev(t, "x01_leg1_finished.json"))
	if err != nil {
		t.Fatal(err)
	}
	if res.LegsSaved != 1 {
		t.Fatalf("LegsSaved = %d", res.LegsSaved)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM match_legs`); n != 2 {
		t.Fatalf("match_legs = %d, erwartet 2", n)
	}
	if n := count(t, d, `SELECT legs_played FROM match_players mp JOIN players p ON p.id = mp.player_id WHERE p.normalized_name='tim'`); n != 1 {
		t.Fatalf("legs_played = %d", n)
	}
}

// Die echte Antwort eines frisch gestarteten Solo-Matches muss sauber
// durchlaufen: Spieler anlegen, Match als laufend fuehren, kein Leg werten.
func TestEchteAntwortDurchIngest(t *testing.T) {
	svc, d, bid := setup(t)
	ctx := context.Background()
	raw, err := os.ReadFile(filepath.Join(testdata, "..", "match_x01_initial.json"))
	if err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(string(raw)) // so schickt es die Extension
	res, err := svc.HandleEvent(ctx, bid, Event{
		Kind: "fetch",
		URL:  "https://api.autodarts.com/gs/v0/matches/01a0ce02-394e-7b82-8540-3c49e9d8faa4",
		Body: quoted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Recognized || res.Finished || res.LegsSaved != 0 {
		t.Fatalf("Ergebnis: %+v", res)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM matches WHERE finished = 0`); n != 1 {
		t.Fatalf("laufendes Match: %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM match_legs`); n != 0 {
		t.Fatalf("es darf noch kein Leg gewertet sein: %d", n)
	}
	var name, norm, uid string
	if err := d.QueryRow(`SELECT display_name, normalized_name, COALESCE(autodarts_user_id,'') FROM players`).Scan(&name, &norm, &uid); err != nil {
		t.Fatal(err)
	}
	if name != "Testspieler" || norm != "testspieler" || uid != "00000000-0000-4000-8000-000000000001" {
		t.Fatalf("Spieler: %q %q %q", name, norm, uid)
	}
	var variant string
	var set, leg int
	if err := d.QueryRow(`SELECT variant, last_set, last_leg FROM matches`).Scan(&variant, &set, &leg); err != nil {
		t.Fatal(err)
	}
	if variant != "X01" || set != 1 || leg != 1 {
		t.Fatalf("Match: variant=%q set=%d leg=%d", variant, set, leg)
	}
	// Kein Doppelmatch bei erneutem Eintreffen desselben Stands.
	if _, err := svc.HandleEvent(ctx, bid, Event{Kind: "fetch", Body: quoted}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM matches`); n != 1 {
		t.Fatalf("matches = %d", n)
	}
}
