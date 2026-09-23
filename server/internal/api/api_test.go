package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"autodarts-stats/internal/db"
	"autodarts-stats/internal/ingest"
	"autodarts-stats/internal/parser"
	"autodarts-stats/internal/parser/autodarts"
	"autodarts-stats/internal/stats"
)

const testdata = "../../../testdata/placeholder"

type env struct {
	ts  *httptest.Server
	key string
	c   *http.Client
}

func newEnv(t *testing.T) *env {
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
	srv := New(Options{DB: d, Ingest: ing, Stats: stats.New(d), AdminPassword: "geheim", SessionSecret: []byte("s3cret"), CheckinTTL: 4 * time.Hour})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar := &cookieJar{}
	return &env{ts: ts, key: key, c: &http.Client{Jar: jar}}
}

type urlT = url.URL

type cookieJar struct{ cookies []*http.Cookie }

func (j *cookieJar) SetCookies(_ *urlT, cs []*http.Cookie) { j.cookies = cs }
func (j *cookieJar) Cookies(_ *urlT) []*http.Cookie        { return j.cookies }

func (e *env) do(t *testing.T, method, path string, body any, headers map[string]string) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := e.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out bytes.Buffer
	out.ReadFrom(res.Body)
	return res.StatusCode, out.Bytes()
}

func (e *env) ingest(t *testing.T, files ...string) {
	t.Helper()
	var evs []ingest.Event
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(testdata, f))
		if err != nil {
			t.Fatal(err)
		}
		evs = append(evs, ingest.Event{Kind: "fetch", URL: "https://api.autodarts.io/gs/v0/matches/x", TS: 1, Body: b})
	}
	code, body := e.do(t, "POST", "/api/ingest", map[string]any{"board": "Test", "events": evs}, map[string]string{"Authorization": "Bearer " + e.key})
	if code != 200 {
		t.Fatalf("ingest: %d %s", code, body)
	}
}

func TestIngestAuth(t *testing.T) {
	e := newEnv(t)
	code, _ := e.do(t, "POST", "/api/ingest", map[string]any{"events": []any{}}, map[string]string{"Authorization": "Bearer falsch"})
	if code != 401 {
		t.Fatalf("expected 401, got %d", code)
	}
	code, _ = e.do(t, "POST", "/api/ingest", map[string]any{"events": []any{}}, nil)
	if code != 401 {
		t.Fatalf("expected 401 without key, got %d", code)
	}
	code, body := e.do(t, "GET", "/api/ingest/ping", nil, map[string]string{"Authorization": "Bearer " + e.key})
	if code != 200 || !bytes.Contains(body, []byte(`"Test"`)) {
		t.Fatalf("ping: %d %s", code, body)
	}
}

func TestPublicAPI(t *testing.T) {
	e := newEnv(t)
	e.ingest(t, "x01_leg1_running.json", "x01_leg1_finished.json", "x01_leg2_running.json", "x01_match_finished.json")

	code, body := e.do(t, "GET", "/api/leaderboard?min_matches=1&sort=average", nil, nil)
	if code != 200 {
		t.Fatalf("leaderboard: %d %s", code, body)
	}
	var rows []stats.PlayerRow
	json.Unmarshal(body, &rows)
	if len(rows) != 2 || rows[0].DisplayName != "Tim" || rows[0].Average == nil {
		t.Fatalf("leaderboard rows: %s", body)
	}
	// Tim: Leg1 501/11 Darts, Leg2 400/12 Darts -> 901*3/23 = 117.5
	if *rows[0].Average < 117.4 || *rows[0].Average > 117.6 {
		t.Errorf("tim average = %v", *rows[0].Average)
	}
	if rows[1].Wins != 1 || rows[1].HighestCheckout != 161 {
		t.Errorf("juergen: %+v", rows[1])
	}
	code, body = e.do(t, "GET", "/api/leaderboard", nil, nil)
	json.Unmarshal(body, &rows)
	if code != 200 || len(rows) != 0 {
		t.Errorf("default min_matches=5 should hide players: %s", body)
	}

	code, body = e.do(t, "GET", "/api/players/1", nil, nil)
	if code != 200 {
		t.Fatalf("profile: %d %s", code, body)
	}
	var prof stats.Profile
	json.Unmarshal(body, &prof)
	if prof.Player.Matches != 1 || len(prof.History) != 1 || len(prof.Recent) != 1 || prof.BestLegDarts != 11 {
		t.Errorf("profile: %+v", prof)
	}
	code, body = e.do(t, "GET", "/api/h2h?a=1&b=2", nil, nil)
	if code != 200 {
		t.Fatalf("h2h: %d %s", code, body)
	}
	var h stats.H2H
	json.Unmarshal(body, &h)
	if h.Matches != 1 || h.WinsB != 1 || h.LegsA != 1 || h.LegsB != 1 || h.AvgA == nil {
		t.Errorf("h2h: %+v", h)
	}
	code, body = e.do(t, "GET", "/api/matches/1", nil, nil)
	var md stats.MatchDetail
	json.Unmarshal(body, &md)
	if code != 200 || len(md.Legs) != 4 || len(md.Players) != 2 {
		t.Errorf("match detail: %d %s", code, body)
	}
	code, _ = e.do(t, "GET", "/api/players/999", nil, nil)
	if code != 404 {
		t.Errorf("expected 404, got %d", code)
	}
}

func TestAdminFlow(t *testing.T) {
	e := newEnv(t)
	e.ingest(t, "x01_match_finished.json")
	code, _ := e.do(t, "GET", "/api/admin/players", nil, nil)
	if code != 401 {
		t.Fatalf("expected 401, got %d", code)
	}
	code, _ = e.do(t, "POST", "/api/admin/login", map[string]string{"password": "falsch"}, nil)
	if code != 401 {
		t.Fatalf("expected 401, got %d", code)
	}
	code, _ = e.do(t, "POST", "/api/admin/login", map[string]string{"password": "geheim"}, nil)
	if code != 200 {
		t.Fatalf("login failed: %d", code)
	}
	code, body := e.do(t, "GET", "/api/admin/players", nil, nil)
	if code != 200 {
		t.Fatalf("players: %d %s", code, body)
	}
	var players []adminPlayer
	json.Unmarshal(body, &players)
	if len(players) != 2 {
		t.Fatalf("players: %s", body)
	}
	// Alias hinzufuegen, dann Ingest mit dem Alias-Namen ordnet zu.
	code, _ = e.do(t, "POST", "/api/admin/players/1/aliases", map[string]string{"alias": "Timmy"}, nil)
	if code != 200 {
		t.Fatalf("alias: %d", code)
	}
	code, _ = e.do(t, "POST", "/api/admin/players/1/rename", map[string]string{"display_name": "Tim H."}, nil)
	if code != 200 {
		t.Fatalf("rename: %d", code)
	}
	// Merge 2 -> 1
	code, body = e.do(t, "POST", "/api/admin/players/2/merge", map[string]int64{"into": 1}, nil)
	if code != 200 {
		t.Fatalf("merge: %d %s", code, body)
	}
	code, body = e.do(t, "GET", "/api/admin/players", nil, nil)
	json.Unmarshal(body, &players)
	if len(players) != 1 || players[0].DisplayName != "Tim H." || len(players[0].Aliases) != 3 {
		t.Fatalf("after merge: %s", body)
	}
	// Boards
	code, body = e.do(t, "POST", "/api/admin/boards", map[string]string{"name": "Keller"}, nil)
	if code != 201 || !bytes.Contains(body, []byte("adb_")) {
		t.Fatalf("board: %d %s", code, body)
	}
	code, body = e.do(t, "POST", "/api/admin/reprocess", nil, nil)
	if code != 200 {
		t.Fatalf("reprocess: %d %s", code, body)
	}
	// Loeschen
	code, _ = e.do(t, "DELETE", "/api/admin/players/1", nil, nil)
	if code != 200 {
		t.Fatalf("delete: %d", code)
	}
	code, body = e.do(t, "GET", "/api/players", nil, nil)
	if code != 200 || string(bytes.TrimSpace(body)) != "[]" {
		t.Fatalf("players after delete: %s", body)
	}
	code, _ = e.do(t, "POST", "/api/admin/logout", nil, nil)
	code, _ = e.do(t, "GET", "/api/admin/players", nil, nil)
	if code != 401 {
		t.Fatalf("expected 401 after logout, got %d", code)
	}
}

func TestCheckinFlow(t *testing.T) {
	e := newEnv(t)
	auth := map[string]string{"Authorization": "Bearer " + e.key}
	// Unbekannter Chip -> 404 + Unbekannt-Liste
	code, body := e.do(t, "POST", "/api/checkin", map[string]string{"uid": "04:AA:BB:CC"}, auth)
	if code != 404 {
		t.Fatalf("unknown chip: %d %s", code, body)
	}
	code, _ = e.do(t, "POST", "/api/checkin", map[string]string{"uid": "04aabbcc"}, map[string]string{"Authorization": "Bearer falsch"})
	if code != 401 {
		t.Fatalf("expected 401, got %d", code)
	}
	e.do(t, "POST", "/api/admin/login", map[string]string{"password": "geheim"}, nil)
	code, body = e.do(t, "GET", "/api/admin/chips", nil, nil)
	var chips struct {
		Chips   []chipInfo    `json:"chips"`
		Unknown []unknownChip `json:"unknown"`
	}
	json.Unmarshal(body, &chips)
	if code != 200 || len(chips.Unknown) != 1 || len(chips.Chips) != 0 {
		t.Fatalf("chips: %d %s", code, body)
	}
	// Chip einem neuen, geschuetzten Spieler zuordnen
	code, body = e.do(t, "POST", "/api/admin/chips", map[string]any{"uid_hash": chips.Unknown[0].UIDHash, "new_name": "Tim", "label": "Firmenchip"}, nil)
	if code != 201 {
		t.Fatalf("register: %d %s", code, body)
	}
	var reg struct {
		PlayerID int64 `json:"player_id"`
	}
	json.Unmarshal(body, &reg)
	// Einstempeln
	code, body = e.do(t, "POST", "/api/checkin", map[string]string{"uid": "04 AA BB CC"}, auth)
	if code != 200 {
		t.Fatalf("checkin: %d %s", code, body)
	}
	var ci ingest.Checkin
	json.Unmarshal(body, &ci)
	if ci.PlayerID != reg.PlayerID || !bytes.HasPrefix([]byte(ci.GameName), []byte("Tim#")) {
		t.Fatalf("checkin: %+v", ci)
	}
	code, body = e.do(t, "GET", "/api/checkins", nil, auth)
	var list []ingest.Checkin
	json.Unmarshal(body, &list)
	if code != 200 || len(list) != 1 || list[0].Token != ci.Token {
		t.Fatalf("board checkins: %d %s", code, body)
	}
	// Match mit Code -> zugeordnet; Match ohne Code -> Freigabe-Queue
	raw, _ := os.ReadFile(filepath.Join(testdata, "x01_match_finished.json"))
	withCode := bytes.Replace(raw, []byte(`"name": "Tim"`), []byte(`"name": "`+ci.GameName+`"`), 1)
	noCode := bytes.Replace(raw, []byte("11111111-2222"), []byte("99999999-2222"), 1)
	// played_at 2026-09-20 liegt vor dem Check-in (heute) -> Code muss zum Spielzeitpunkt gelten.
	// Daher createdAt auf jetzt setzen.
	nowISO := []byte(time.Now().UTC().Format(time.RFC3339))
	withCode = bytes.Replace(withCode, []byte("2026-09-20T18:00:00.000Z"), nowISO, 1)
	evs := []ingest.Event{{Kind: "fetch", Body: withCode}, {Kind: "fetch", Body: noCode}}
	code, body = e.do(t, "POST", "/api/ingest", map[string]any{"events": evs}, auth)
	if code != 200 {
		t.Fatalf("ingest: %d %s", code, body)
	}
	code, body = e.do(t, "GET", "/api/admin/pending", nil, nil)
	var pend []pendingInfo
	json.Unmarshal(body, &pend)
	if code != 200 || len(pend) != 1 || pend[0].SuggestedID != reg.PlayerID || pend[0].Name != "Tim" {
		t.Fatalf("pending: %d %s", code, body)
	}
	code, body = e.do(t, "GET", "/api/players/"+strconv.FormatInt(reg.PlayerID, 10), nil, nil)
	var prof stats.Profile
	json.Unmarshal(body, &prof)
	if prof.Player.Matches != 1 {
		t.Fatalf("expected exactly the coded match to count: %+v", prof.Player)
	}
	code, body = e.do(t, "GET", "/api/matches/"+strconv.FormatInt(pend[0].MatchID, 10), nil, nil)
	var md stats.MatchDetail
	json.Unmarshal(body, &md)
	if len(md.Pending) != 1 {
		t.Fatalf("match detail pending: %s", body)
	}
	// Freigeben
	code, body = e.do(t, "POST", "/api/admin/pending/"+strconv.FormatInt(pend[0].MatchID, 10)+"/0", map[string]any{"player_id": reg.PlayerID}, nil)
	if code != 200 {
		t.Fatalf("resolve: %d %s", code, body)
	}
	code, body = e.do(t, "GET", "/api/players/"+strconv.FormatInt(reg.PlayerID, 10), nil, nil)
	json.Unmarshal(body, &prof)
	if prof.Player.Matches != 2 {
		t.Fatalf("after approve: %+v", prof.Player)
	}
	// Nachtraeglich umhaengen: Slot 0 des ersten Matches ignorieren
	code, body = e.do(t, "POST", "/api/admin/matches/"+strconv.FormatInt(pend[0].MatchID, 10)+"/slots/0", map[string]any{"ignore": true}, nil)
	if code != 200 {
		t.Fatalf("reassign: %d %s", code, body)
	}
	code, body = e.do(t, "GET", "/api/players/"+strconv.FormatInt(reg.PlayerID, 10), nil, nil)
	json.Unmarshal(body, &prof)
	if prof.Player.Matches != 1 {
		t.Fatalf("after ignore: %+v", prof.Player)
	}
	// Check-in beenden
	code, _ = e.do(t, "DELETE", "/api/admin/checkins/"+strconv.FormatInt(ci.ID, 10), nil, nil)
	if code != 200 {
		t.Fatalf("end checkin: %d", code)
	}
	_, body = e.do(t, "GET", "/api/checkins", nil, auth)
	if string(bytes.TrimSpace(body)) != "[]" {
		t.Fatalf("checkins after end: %s", body)
	}
	// Protect-Toggle
	code, _ = e.do(t, "POST", "/api/admin/players/"+strconv.FormatInt(reg.PlayerID, 10)+"/protect", map[string]bool{"protected": false}, nil)
	if code != 200 {
		t.Fatalf("protect: %d", code)
	}
}
