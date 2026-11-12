package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

// ReprocessUnparsed darf s.MaxUnparsed nicht veraendern: parallel
// eingehende unerkannte Events muessen weiter gespeichert werden, und mit
// -race darf kein Datenrennen gemeldet werden.
func TestReprocessUnparsedNoRace(t *testing.T) {
	svc, d, bid := setup(t)
	svc.MaxUnparsed = 100
	ctx := context.Background()
	garbage := func(i int) Event {
		return Event{Kind: "fetch", URL: "https://x/" + fmt.Sprint(i), Body: json.RawMessage(fmt.Sprintf(`{"unbekannt":%d}`, i))}
	}
	for i := 0; i < 5; i++ {
		if _, err := svc.HandleEvent(ctx, bid, garbage(i)); err != nil {
			t.Fatal(err)
		}
	}
	const parallel = 20
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < parallel; i++ {
			if _, err := svc.HandleEvent(ctx, bid, garbage(100+i)); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			if _, _, err := svc.ReprocessUnparsed(ctx); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	wg.Wait()
	if svc.MaxUnparsed != 100 {
		t.Fatalf("MaxUnparsed veraendert: %d", svc.MaxUnparsed)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM unparsed_events`); n != 5+parallel {
		t.Fatalf("unparsed = %d, erwartet %d (parallele Events verloren?)", n, 5+parallel)
	}
}

// Reprocess ruft mit ReprocessFinished den Turnier-Hook einmal am Ende statt
// einmal pro Match; ohne bleibt das alte Verhalten. Einzelne Neuberechnungen
// rufen weiterhin MatchFinished.
func TestReprocessBatchHook(t *testing.T) {
	svc, d, bid := setup(t)
	ctx := context.Background()
	for _, f := range []string{"x01_leg1_finished.json", "x01_leg2_running.json", "x01_match_finished.json"} {
		if _, err := svc.HandleEvent(ctx, bid, ev(t, f)); err != nil {
			t.Fatal(err)
		}
	}
	// Zwei weitere beendete Matches als Kopie.
	for i := 0; i < 2; i++ {
		if _, err := d.Exec(`INSERT INTO matches (autodarts_match_id, board_id, played_at, variant, settings_json, finished, last_set, last_leg, raw_json, received_at, updated_at)
			SELECT autodarts_match_id || ?, board_id, played_at, variant, settings_json, finished, last_set, last_leg, raw_json, received_at, updated_at FROM matches WHERE id = 1`, fmt.Sprint("-copy", i)); err != nil {
			t.Fatal(err)
		}
	}
	perMatch := 0
	svc.MatchFinished = func(ctx context.Context, tx *sql.Tx, matchID int64) error { perMatch++; return nil }

	// Ohne Batch-Hook: einmal pro beendetem Match.
	if n, err := svc.Reprocess(ctx); err != nil || n != 3 {
		t.Fatalf("reprocess: %d %v", n, err)
	}
	if perMatch != 3 {
		t.Fatalf("MatchFinished ohne batch: %d", perMatch)
	}

	perMatch = 0
	var batches [][]int64
	svc.ReprocessFinished = func(ctx context.Context, tx *sql.Tx, finished []int64) error {
		var c int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM match_players`).Scan(&c); err != nil || c == 0 {
			t.Errorf("hook sieht die neu berechneten daten nicht: %d %v", c, err)
		}
		batches = append(batches, append([]int64(nil), finished...))
		return nil
	}
	if n, err := svc.Reprocess(ctx); err != nil || n != 3 {
		t.Fatalf("reprocess: %d %v", n, err)
	}
	if perMatch != 0 || len(batches) != 1 || fmt.Sprint(batches[0]) != "[1 2 3]" {
		t.Fatalf("perMatch=%d batches=%v", perMatch, batches)
	}
	if err := svc.ReprocessMatch(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if perMatch != 1 || len(batches) != 1 {
		t.Fatalf("ReprocessMatch: perMatch=%d batches=%d", perMatch, len(batches))
	}
}
