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

// loginLimiter: einfache Bremse gegen Passwort-Raten (pro IP).
type loginLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{hits: map[string][]time.Time{}} }

func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	var keep []time.Time
	for _, t := range l.hits[ip] {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	if len(keep) >= 5 {
		l.hits[ip] = keep
		return false
	}
	l.hits[ip] = append(keep, now)
	return true
}
