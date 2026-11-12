package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

// Eine Panic im Reprocess-Lauf darf den Prozess nicht beenden: der Job gilt
// danach als fehlgeschlagen, running ist zurueckgesetzt, und ein neuer Lauf
// geht wieder (die DB-Verbindung ist frei).
func TestReprocessPanicRecovered(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	e := newHardEnv(t, false)
	e.login(t, "geheim", nil)
	e.ingest(t, "x01_leg1_running.json", "x01_match_finished.json")
	e.ing.ReprocessFinished = func(ctx context.Context, tx *sql.Tx, finished []int64) error {
		panic("boom")
	}
	e.srv.reprocessWait = 5 * time.Second
	code, body := e.do(t, "POST", "/api/admin/reprocess", nil, nil)
	if code != 500 || !bytes.Contains(body, []byte("interner Fehler")) {
		t.Fatalf("erwartet 500: %d %s", code, body)
	}
	code, body = e.do(t, "GET", "/api/admin/reprocess", nil, nil)
	var st reprocessStatus
	if err := json.Unmarshal(body, &st); err != nil || code != 200 {
		t.Fatalf("status: %d %s", code, body)
	}
	if st.Running || st.Error == "" || st.FinishedAt == "" {
		t.Fatalf("status nach panic: %s", body)
	}
	if !strings.Contains(buf.String(), "panic: boom") {
		t.Fatalf("panic nicht geloggt:\n%s", buf.String())
	}
	e.ing.ReprocessFinished = func(ctx context.Context, tx *sql.Tx, finished []int64) error { return nil }
	code, body = e.do(t, "POST", "/api/admin/reprocess", nil, nil)
	if code != 200 || !bytes.Contains(body, []byte(`"matches":1`)) {
		t.Fatalf("lauf nach panic: %d %s", code, body)
	}
}
