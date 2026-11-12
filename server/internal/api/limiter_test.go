package api

import (
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Ein paralleler Burst darf nicht mehr Versuche durchlassen als maxFails:
// allow reserviert den Versuch in derselben Sperre, in der es zaehlt.
func TestLoginLimiterConcurrentBurst(t *testing.T) {
	l := newLoginLimiter()
	const n = 20
	var allowed atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if l.allow("203.0.113.1") {
				allowed.Add(1)
				// Passwortpruefung dauert: erst danach wird der Fehlversuch verbucht.
				time.Sleep(time.Millisecond)
				l.fail("203.0.113.1")
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := allowed.Load(); got > int32(l.maxFails) {
		t.Fatalf("%d von %d parallelen Versuchen durchgelassen, erlaubt sind %d", got, n, l.maxFails)
	}
	if l.allow("203.0.113.1") {
		t.Fatal("nach dem Burst nicht gesperrt")
	}
}

// Reservierungen ohne Abschluss (Abbruch vor der Passwortpruefung) zaehlen
// nur bis zum Ablauf des Fensters; ein Erfolg loest seine Reservierung ein.
func TestLoginLimiterPendingExpires(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	l := newLoginLimiter()
	l.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		if !l.allow("10.1.1.1") {
			t.Fatalf("versuch %d gesperrt", i)
		}
	}
	if l.allow("10.1.1.1") {
		t.Fatal("offene Reservierungen zaehlen nicht")
	}
	now = now.Add(2 * time.Minute)
	if !l.allow("10.1.1.1") {
		t.Fatal("abgelaufene Reservierungen sperren noch")
	}
	l.reset("10.1.1.1")
	for i := 0; i < 5; i++ {
		if !l.allow("10.1.1.1") {
			t.Fatalf("nach Erfolg: versuch %d gesperrt", i)
		}
		l.reset("10.1.1.1")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.pending) != 0 || len(l.fails) != 0 {
		t.Fatalf("reste: pending=%v fails=%v", l.pending, l.fails)
	}
}

// Dasselbe ueber HTTP: 20 parallele Fehlversuche, hoechstens 5 erreichen
// die Passwortpruefung (401), der Rest bekommt 429.
func TestLoginConcurrentBurstHTTP(t *testing.T) {
	e := newHardEnv(t, false)
	const n = 20
	var unauthorized, limited, other atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			resp, err := http.Post(e.ts.URL+"/api/admin/login", "application/json", strings.NewReader(`{"password":"falsch"}`))
			if err != nil {
				other.Add(1)
				return
			}
			resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusUnauthorized:
				unauthorized.Add(1)
			case http.StatusTooManyRequests:
				limited.Add(1)
			default:
				other.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if other.Load() != 0 {
		t.Fatalf("%d unerwartete Antworten", other.Load())
	}
	if u := unauthorized.Load(); u > 5 {
		t.Fatalf("%d Fehlversuche durchgelassen, erlaubt sind 5", u)
	}
	if unauthorized.Load()+limited.Load() != n {
		t.Fatalf("401=%d 429=%d", unauthorized.Load(), limited.Load())
	}
	if code := e.login(t, "geheim", nil); code != 429 {
		t.Fatalf("nach dem Burst: %d", code)
	}
}
