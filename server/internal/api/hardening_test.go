package api

// Tests fuer die Haertung aus dem Go-Review: Client-IP hinter Proxy,
// Login-Limiter, generische 500er, Fehler pro Ingest-Event, nosniff,
// Reprocess im Hintergrund, Schutz beim Merge.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

type hardEnv struct {
	*env
	db  *sql.DB
	srv *Server
	ing *ingest.Service
}

func newHardEnv(t *testing.T, trustProxy bool) *hardEnv {
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
	ing.MatchFinished = tour.MatchFinished
	ing.ReprocessFinished = tour.MatchesReprocessed
	srv := New(Options{DB: d, Ingest: ing, Stats: stats.New(d), Tournaments: tour, AdminPassword: "geheim",
		SessionSecret: []byte("s3cret"), CheckinTTL: 4 * time.Hour, TrustProxy: trustProxy})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &hardEnv{env: &env{ts: ts, key: key, c: &http.Client{Jar: &cookieJar{}}}, db: d, srv: srv, ing: ing}
}

func (e *hardEnv) login(t *testing.T, pw string, headers map[string]string) int {
	t.Helper()
	code, _ := e.do(t, "POST", "/api/admin/login", map[string]string{"password": pw}, headers)
	return code
}

func TestClientIP(t *testing.T) {
	req := func(remote string, xff ...string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		for _, v := range xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		return r
	}
	trusted := &Server{TrustProxy: true}
	direct := &Server{}
	cases := []struct {
		name string
		s    *Server
		r    *http.Request
		want string
	}{
		{"proxy haengt an, Client faelscht links", trusted, req("10.0.0.1:5000", "1.2.3.4, 203.0.113.9"), "203.0.113.9"},
		{"mehrere Header: letzter zaehlt", trusted, req("10.0.0.1:5000", "1.2.3.4", "6.6.6.6, 203.0.113.9"), "203.0.113.9"},
		{"nur Proxy-Eintrag", trusted, req("10.0.0.1:5000", "203.0.113.9"), "203.0.113.9"},
		{"Muell im letzten Element -> RemoteAddr", trusted, req("10.0.0.1:5000", "1.2.3.4, kaputt"), "10.0.0.1"},
		{"ohne Header -> RemoteAddr", trusted, req("10.0.0.1:5000"), "10.0.0.1"},
		{"X-Real-IP wird ignoriert", trusted, func() *http.Request {
			r := req("10.0.0.1:5000", "203.0.113.9")
			r.Header.Set("X-Real-IP", "1.2.3.4")
			return r
		}(), "203.0.113.9"},
		{"ohne TRUST_PROXY zaehlt nur RemoteAddr", direct, req("10.0.0.1:5000", "1.2.3.4"), "10.0.0.1"},
	}
	for _, c := range cases {
		if got := c.s.clientIP(c.r); got != c.want {
			t.Errorf("%s: %q, erwartet %q", c.name, got, c.want)
		}
	}
}

// Ein Angreifer hinter dem Proxy kann das Limit nicht umgehen, indem er das
// erste XFF-Element bei jedem Versuch aendert.
func TestLoginXFFSpoofing(t *testing.T) {
	e := newHardEnv(t, true)
	for i := 0; i < 5; i++ {
		h := map[string]string{"X-Forwarded-For": fmt.Sprintf("198.51.100.%d, 203.0.113.7", i)}
		if code := e.login(t, "falsch", h); code != 401 {
			t.Fatalf("versuch %d: %d", i, code)
		}
	}
	if code := e.login(t, "falsch", map[string]string{"X-Forwarded-For": "198.51.100.99, 203.0.113.7"}); code != 429 {
		t.Fatalf("gefaelschtes erstes XFF-Element umgeht das Limit: %d", code)
	}
	// Ein anderer echter Client (anderer Proxy-Eintrag) ist nicht betroffen.
	if code := e.login(t, "geheim", map[string]string{"X-Forwarded-For": "203.0.113.8"}); code != 200 {
		t.Fatalf("anderer Client gesperrt: %d", code)
	}
}

func TestSuccessfulLoginsDoNotLock(t *testing.T) {
	e := newHardEnv(t, false)
	for i := 0; i < 12; i++ {
		if code := e.login(t, "geheim", nil); code != 200 {
			t.Fatalf("login %d: %d", i, code)
		}
	}
	// 4 Fehlversuche, Erfolg setzt zurueck, danach wieder 4 erlaubt.
	for round := 0; round < 3; round++ {
		for i := 0; i < 4; i++ {
			if code := e.login(t, "falsch", nil); code != 401 {
				t.Fatalf("runde %d fehlversuch %d: %d", round, i, code)
			}
		}
		if code := e.login(t, "geheim", nil); code != 200 {
			t.Fatalf("runde %d: erfolgreicher login gesperrt: %d", round, code)
		}
	}
	// 5 Fehlversuche sperren weiterhin (auch das richtige Passwort).
	for i := 0; i < 5; i++ {
		e.login(t, "falsch", nil)
	}
	if code := e.login(t, "geheim", nil); code != 429 {
		t.Fatalf("nach 5 fehlversuchen: %d", code)
	}
}

func TestLoginLimiterCleanup(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	l := newLoginLimiter()
	l.now = func() time.Time { return now }
	for i := 0; i < 100; i++ {
		l.fail(fmt.Sprintf("10.0.%d.%d", i/256, i%256))
	}
	if n := l.size(); n != 100 {
		t.Fatalf("eintraege: %d", n)
	}
	// allow auf eine IP mit nur abgelaufenen Eintraegen loescht diese sofort.
	now = now.Add(2 * time.Minute)
	if !l.allow("10.0.0.1") {
		t.Fatal("abgelaufene fehlversuche sperren noch")
	}
	// ... und der periodische Durchlauf raeumt alle anderen mit ab.
	if n := l.size(); n != 0 {
		t.Fatalf("nach ablauf noch %d eintraege", n)
	}
	// reset entfernt den Eintrag.
	l.fail("1.1.1.1")
	l.reset("1.1.1.1")
	if n := l.size(); n != 0 {
		t.Fatalf("reset: %d", n)
	}
	// Obergrenze: aelteste fliegen raus.
	l.maxEntries = 3
	for i := 0; i < 10; i++ {
		now = now.Add(time.Second)
		l.fail(fmt.Sprintf("192.0.2.%d", i))
	}
	if n := l.size(); n > 3 {
		t.Fatalf("obergrenze ueberschritten: %d", n)
	}
	if _, ok := l.fails["192.0.2.9"]; !ok {
		t.Fatal("juengster eintrag fehlt")
	}
}

func TestXFFWarningOnce(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	e := newHardEnv(t, false)
	for i := 0; i < 3; i++ {
		e.login(t, "geheim", map[string]string{"X-Forwarded-For": "1.2.3.4"})
	}
	if n := strings.Count(buf.String(), "TRUST_PROXY ist aus"); n != 1 {
		t.Fatalf("warnung %d-mal statt einmal:\n%s", n, buf.String())
	}
}

func TestInternalErrorIsGeneric(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	e := newHardEnv(t, false)
	if code := e.login(t, "geheim", nil); code != 200 {
		t.Fatal(code)
	}
	if _, err := e.db.Exec(`DROP TABLE unparsed_events`); err != nil {
		t.Fatal(err)
	}
	code, body := e.do(t, "GET", "/api/admin/unparsed", nil, nil)
	if code != 500 {
		t.Fatalf("erwartet 500, got %d %s", code, body)
	}
	var eb errorBody
	if err := json.Unmarshal(body, &eb); err != nil || eb.Error != "interner Fehler" {
		t.Fatalf("body nicht generisch: %s", body)
	}
	if bytes.Contains(body, []byte("unparsed_events")) || bytes.Contains(body, []byte("no such table")) {
		t.Fatalf("interne details nach aussen: %s", body)
	}
	if !strings.Contains(buf.String(), "no such table") {
		t.Fatalf("fehler nicht geloggt:\n%s", buf.String())
	}
}

func TestNoSniffHeader(t *testing.T) {
	e := newHardEnv(t, false)
	e.login(t, "geheim", nil)
	if _, err := e.db.Exec(`INSERT INTO unparsed_events (kind, url, reason, raw_json, received_at) VALUES ('fetch','x','r','<script>alert(1)</script>','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/admin/unparsed/1", "/api/health"} {
		req, _ := http.NewRequest("GET", e.ts.URL+path, nil)
		res, err := e.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 || res.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: %d nosniff=%q", path, res.StatusCode, res.Header.Get("X-Content-Type-Options"))
		}
	}
}

// ingestBatch liest die Testdateien; "kaputt" ist ein unerkanntes Payload.
func ingestBatch(t *testing.T, files ...string) []ingest.Event {
	t.Helper()
	var evs []ingest.Event
	for _, f := range files {
		var b []byte
		if f == "kaputt" {
			b = []byte(`{"nichts":1}`)
		} else {
			var err error
			if b, err = os.ReadFile(filepath.Join(testdata, f)); err != nil {
				t.Fatal(err)
			}
		}
		evs = append(evs, ingest.Event{Kind: "fetch", URL: "https://api.autodarts.io/gs/v0/matches/x", TS: 1, Body: b})
	}
	return evs
}

func (e *hardEnv) postBatch(t *testing.T, evs []ingest.Event) (int, []byte, []ingest.Result) {
	t.Helper()
	code, body := e.do(t, "POST", "/api/ingest", map[string]any{"board": "Test", "events": evs}, map[string]string{"Authorization": "Bearer " + e.key})
	var out struct {
		Results []ingest.Result `json:"results"`
	}
	if code == 200 {
		if err := json.Unmarshal(body, &out); err != nil || len(out.Results) != len(evs) {
			t.Fatalf("antwort: %s", body)
		}
	}
	return code, body, out.Results
}

// rowCounts zaehlt alle Tabellen, in denen ein doppelt verarbeitetes Event
// Spuren hinterlassen wuerde.
func (e *hardEnv) rowCounts(t *testing.T) map[string]int {
	t.Helper()
	m := map[string]int{}
	for _, tbl := range []string{"matches", "match_snapshots", "match_legs", "match_players", "players", "unparsed_events"} {
		var n int
		if err := e.db.QueryRow(`SELECT COUNT(*) FROM ` + tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		m[tbl] = n
	}
	return m
}

// Ein voruebergehender Serverfehler darf kein Event verlieren: 503 fuer den
// Batch, die Extension sendet ihn erneut, und das erzeugt keine Duplikate.
func TestIngestTransientErrorRetries(t *testing.T) {
	log.SetOutput(&bytes.Buffer{})
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	evs := ingestBatch(t, "x01_leg1_running.json", "x01_match_finished.json", "kaputt")

	// Referenz: derselbe Batch einmal fehlerfrei.
	ref := newHardEnv(t, false)
	if code, body, _ := ref.postBatch(t, evs); code != 200 {
		t.Fatalf("referenz: %d %s", code, body)
	}
	want := ref.rowCounts(t)

	e := newHardEnv(t, false)
	orig := e.ing.MatchFinished
	fail := true
	e.ing.MatchFinished = func(ctx context.Context, tx *sql.Tx, matchID int64) error {
		if fail {
			return errors.New("database is locked (5) geheimes detail")
		}
		return orig(ctx, tx, matchID)
	}
	code, body, _ := e.postBatch(t, evs)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("erwartet 503, bekommen %d %s", code, body)
	}
	if bytes.Contains(body, []byte("geheimes detail")) {
		t.Fatalf("interner fehler nach aussen: %s", body)
	}
	var finished int
	e.db.QueryRow(`SELECT COUNT(*) FROM matches WHERE finished = 1`).Scan(&finished)
	if finished != 0 {
		t.Fatal("fehlgeschlagenes Event wurde teilweise gespeichert")
	}

	// Retry (wie die Extension: derselbe Batch, hier zweimal) -> wie Referenz.
	fail = false
	for i := 0; i < 2; i++ {
		code, body, res := e.postBatch(t, evs)
		if code != 200 {
			t.Fatalf("retry %d: %d %s", i, code, body)
		}
		for j, r := range res {
			if r.Error != "" {
				t.Fatalf("retry %d event %d: %+v", i, j, r)
			}
		}
		if !res[1].Recognized || !res[1].Finished || res[2].Recognized {
			t.Fatalf("retry %d: %+v", i, res)
		}
		got := e.rowCounts(t)
		for k, v := range want {
			if got[k] != v {
				t.Fatalf("retry %d: %s = %d, erwartet %d (alle: %v / %v)", i, k, got[k], v, got, want)
			}
		}
	}
}

// Ein Kontextabbruch zaehlt ebenfalls als voruebergehend.
func TestIngestContextErrorIsTransient(t *testing.T) {
	log.SetOutput(&bytes.Buffer{})
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	e := newHardEnv(t, false)
	e.ing.MatchFinished = func(ctx context.Context, tx *sql.Tx, matchID int64) error {
		return fmt.Errorf("hook: %w", context.DeadlineExceeded)
	}
	if code, body, _ := e.postBatch(t, ingestBatch(t, "x01_match_finished.json")); code != http.StatusServiceUnavailable {
		t.Fatalf("erwartet 503: %d %s", code, body)
	}
}

// Dauerhaft unbrauchbare Events werden pro Event gemeldet, als unerkannt
// abgelegt und blockieren den Batch nicht (200).
func TestIngestPermanentErrorPerEvent(t *testing.T) {
	log.SetOutput(&bytes.Buffer{})
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	e := newHardEnv(t, false)
	// Inhaltsbedingter SQLite-Fehler (SQLITE_CONSTRAINT_TRIGGER), der beim
	// Wiederholen genauso auftritt: beendete Matches lassen sich nicht speichern.
	for _, op := range []string{"INSERT", "UPDATE"} {
		if _, err := e.db.Exec(`CREATE TRIGGER kein_finale_` + op + ` BEFORE ` + op + ` ON matches WHEN NEW.finished = 1
			BEGIN SELECT RAISE(ABORT, 'geheimes detail'); END`); err != nil {
			t.Fatal(err)
		}
	}
	evs := ingestBatch(t, "x01_match_finished.json", "x01_leg1_running.json", "kaputt")
	for i := 0; i < 2; i++ {
		code, body, res := e.postBatch(t, evs)
		if code != 200 {
			t.Fatalf("lauf %d: %d %s", i, code, body)
		}
		if res[0].Error == "" || res[0].Recognized {
			t.Fatalf("lauf %d event 0 ohne fehler: %+v", i, res[0])
		}
		if res[1].Error != "" || !res[1].Recognized {
			t.Fatalf("lauf %d event 1: %+v", i, res[1])
		}
		if res[2].Error != "" || res[2].Recognized {
			t.Fatalf("lauf %d event 2 (unerkannt) wurde nicht weiterverarbeitet: %+v", i, res[2])
		}
		if bytes.Contains(body, []byte("geheimes detail")) {
			t.Fatalf("interner fehler nach aussen: %s", body)
		}
	}
	var failed, all int
	e.db.QueryRow(`SELECT COUNT(*) FROM unparsed_events WHERE reason LIKE 'verarbeitung fehlgeschlagen:%'`).Scan(&failed)
	e.db.QueryRow(`SELECT COUNT(*) FROM unparsed_events`).Scan(&all)
	if failed != 1 || all != 2 {
		t.Fatalf("unparsed: %d fehlgeschlagen, %d gesamt (erwartet 1/2, ohne Duplikate)", failed, all)
	}

	// Sentinel aus dem Hook: ebenfalls dauerhaft.
	e2 := newHardEnv(t, false)
	e2.ing.MatchFinished = func(ctx context.Context, tx *sql.Tx, matchID int64) error {
		return fmt.Errorf("turnier: %w", ingest.ErrInvalidEvent)
	}
	code, body, res := e2.postBatch(t, ingestBatch(t, "x01_match_finished.json"))
	if code != 200 || res[0].Error == "" {
		t.Fatalf("sentinel: %d %s", code, body)
	}
}

func TestReprocessBackground(t *testing.T) {
	e := newHardEnv(t, false)
	e.login(t, "geheim", nil)
	e.ingest(t, "x01_leg1_running.json", "x01_match_finished.json")
	release := make(chan struct{})
	e.ing.ReprocessFinished = func(ctx context.Context, tx *sql.Tx, finished []int64) error {
		<-release
		return nil
	}
	e.srv.reprocessWait = 50 * time.Millisecond
	code, body := e.do(t, "POST", "/api/admin/reprocess", nil, nil)
	if code != 202 || !bytes.Contains(body, []byte(`"running":true`)) {
		t.Fatalf("erwartet 202 running: %d %s", code, body)
	}
	// Ein zweiter Start haengt sich an den laufenden Job.
	if code, body := e.do(t, "POST", "/api/admin/reprocess", nil, nil); code != 202 {
		t.Fatalf("zweiter start: %d %s", code, body)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, body = e.do(t, "GET", "/api/admin/reprocess", nil, nil)
		if code != 200 {
			t.Fatalf("status: %d %s", code, body)
		}
		var st reprocessStatus
		json.Unmarshal(body, &st)
		if !st.Running {
			if st.Matches != 1 || st.Error != "" || st.FinishedAt == "" {
				t.Fatalf("status nach ende: %s", body)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reprocess wird nicht fertig")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Schnell genug: wie bisher direkt 200 mit matches.
	e.srv.reprocessWait = 10 * time.Second
	code, body = e.do(t, "POST", "/api/admin/reprocess", nil, nil)
	if code != 200 || !bytes.Contains(body, []byte(`"matches":1`)) {
		t.Fatalf("synchron: %d %s", code, body)
	}
}

func TestMergeKeepsProtection(t *testing.T) {
	e := newHardEnv(t, false)
	e.login(t, "geheim", nil)
	ids := map[string]int64{}
	for name, prot := range map[string]bool{"Tim": true, "Timmy": false} {
		code, body := e.do(t, "POST", "/api/admin/players", map[string]any{"display_name": name, "protected": prot}, nil)
		if code != 201 {
			t.Fatalf("anlegen %s: %d %s", name, code, body)
		}
		var r struct{ ID int64 }
		json.Unmarshal(body, &r)
		ids[name] = r.ID
	}
	if code, body := e.do(t, "POST", fmt.Sprintf("/api/admin/players/%d/merge", ids["Tim"]), map[string]int64{"into": ids["Timmy"]}, nil); code != 200 {
		t.Fatalf("merge: %d %s", code, body)
	}
	var prot bool
	if err := e.db.QueryRow(`SELECT protected FROM players WHERE id = ?`, ids["Timmy"]).Scan(&prot); err != nil || !prot {
		t.Fatalf("zielspieler nicht geschuetzt: %v %v", prot, err)
	}
	// Name fehlt: 400 mit verstaendlicher Meldung, kein 500.
	code, body := e.do(t, "POST", "/api/admin/players", map[string]any{"display_name": "  "}, nil)
	if code != 400 || !bytes.Contains(body, []byte("Name fehlt")) {
		t.Fatalf("leerer name: %d %s", code, body)
	}
}
