package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const sessionCookie = "adstats_session"
const sessionTTL = 30 * 24 * time.Hour

type sessions struct {
	secret []byte
	secure bool
}

func (s sessions) sign(exp int64) string {
	msg := strconv.FormatInt(exp, 10)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(msg))
	return msg + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s sessions) valid(token string) bool {
	i := strings.IndexByte(token, '.')
	if i < 0 {
		return false
	}
	exp, err := strconv.ParseInt(token[:i], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.sign(exp))) == 1
}

func (s sessions) set(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: s.sign(time.Now().Add(sessionTTL).Unix()), Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.secure || r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		MaxAge: int(sessionTTL.Seconds()),
	})
}

func (s sessions) clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
}

func (s sessions) authed(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	return err == nil && s.valid(c.Value)
}

// loginLimiter bremst Passwort-Raten pro IP. allow reserviert den Versuch
// atomar (pending), damit ein paralleler Burst nicht mehr als maxFails
// Versuche durchlaesst; fail wandelt die Reservierung in einen Fehlversuch
// um, reset (erfolgreicher Login) vergisst Fehlversuche und Reservierung.
// Erfolgreiche Logins sperren also nicht. Reservierungen ohne Abschluss
// (z. B. ungueltiges JSON) zaehlen bis zum Ablauf des Fensters mit.
// Veraltete Eintraege werden beim Zugriff und periodisch entfernt, die Maps
// sind auf maxEntries begrenzt (dann faellt der aelteste Eintrag heraus).
type loginLimiter struct {
	mu         sync.Mutex
	fails      map[string][]time.Time
	pending    map[string][]time.Time
	window     time.Duration
	maxFails   int
	maxEntries int
	lastSweep  time.Time
	now        func() time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{fails: map[string][]time.Time{}, pending: map[string][]time.Time{}, window: time.Minute, maxFails: 5, maxEntries: 10000, now: time.Now}
}

// pruneMapLocked entfernt abgelaufene Zeitpunkte der IP; ist nichts mehr
// uebrig, verschwindet der Eintrag ganz.
func (l *loginLimiter) pruneMapLocked(m map[string][]time.Time, ip string, now time.Time) []time.Time {
	list := m[ip]
	keep := list[:0]
	for _, t := range list {
		if now.Sub(t) < l.window {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(m, ip)
		return nil
	}
	m[ip] = keep
	return keep
}

func (l *loginLimiter) pruneLocked(ip string, now time.Time) []time.Time {
	return l.pruneMapLocked(l.fails, ip, now)
}

func (l *loginLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < l.window && len(l.fails) < l.maxEntries && len(l.pending) < l.maxEntries {
		return
	}
	l.lastSweep = now
	for ip := range l.fails {
		l.pruneMapLocked(l.fails, ip, now)
	}
	for ip := range l.pending {
		l.pruneMapLocked(l.pending, ip, now)
	}
}

// allow meldet, ob die IP es (noch) versuchen darf, und reserviert den
// Versuch in derselben Sperre (Fehlversuche + offene Reservierungen).
func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweepLocked(now)
	fails := l.pruneMapLocked(l.fails, ip, now)
	pend := l.pruneMapLocked(l.pending, ip, now)
	if len(fails)+len(pend) >= l.maxFails {
		return false
	}
	if pend == nil && len(l.pending) >= l.maxEntries {
		evictOldestLocked(l.pending)
	}
	l.pending[ip] = append(pend, now)
	return true
}

// takePendingLocked loest die aelteste Reservierung der IP ein.
func (l *loginLimiter) takePendingLocked(ip string, now time.Time) {
	pend := l.pruneMapLocked(l.pending, ip, now)
	if len(pend) <= 1 {
		delete(l.pending, ip)
		return
	}
	l.pending[ip] = pend[1:]
}

// fail verbucht einen Fehlversuch (und loest die Reservierung aus allow ein).
func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweepLocked(now)
	l.takePendingLocked(ip, now)
	if _, ok := l.fails[ip]; !ok && len(l.fails) >= l.maxEntries {
		evictOldestLocked(l.fails)
	}
	l.fails[ip] = append(l.pruneLocked(ip, now), now)
}

// reset vergisst die Fehlversuche der IP (nach erfolgreichem Login) und
// loest die Reservierung dieses Versuchs ein.
func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ip)
	l.takePendingLocked(ip, l.now())
}

func evictOldestLocked(m map[string][]time.Time) {
	var oldestIP string
	var oldest time.Time
	for ip, list := range m {
		last := list[len(list)-1]
		if oldestIP == "" || last.Before(oldest) {
			oldestIP, oldest = ip, last
		}
	}
	delete(m, oldestIP)
}

func (l *loginLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.fails)
}
