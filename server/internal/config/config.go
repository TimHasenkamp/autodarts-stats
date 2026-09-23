// Package config liest die Laufzeitkonfiguration aus Umgebungsvariablen.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port          int
	DBPath        string
	AdminPassword string
	SessionSecret []byte
	// MaxUnparsed begrenzt die Anzahl der aufbewahrten, nicht erkannten Payloads.
	MaxUnparsed int
	// TrustProxy: X-Forwarded-For fuer Logging/Cookies beruecksichtigen.
	TrustProxy bool
	// CheckinTTL: Gueltigkeit eines Check-ins (Chip einstempeln).
	CheckinTTL time.Duration
}

func FromEnv() (Config, error) {
	c := Config{
		Port:          8080,
		DBPath:        "autodarts-stats.db",
		AdminPassword: os.Getenv("ADMIN_PASSWORD"),
		MaxUnparsed:   200,
		TrustProxy:    os.Getenv("TRUST_PROXY") == "1",
		CheckinTTL:    4 * time.Hour,
	}
	if v := os.Getenv("CHECKIN_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return c, errors.New("CHECKIN_TTL ist ungueltig (z.B. 4h)")
		}
		c.CheckinTTL = d
	}
	if v := os.Getenv("PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p <= 0 || p > 65535 {
			return c, errors.New("PORT ist ungueltig")
		}
		c.Port = p
	}
	if v := os.Getenv("DB_PATH"); v != "" {
		c.DBPath = v
	}
	if v := os.Getenv("MAX_UNPARSED"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return c, errors.New("MAX_UNPARSED ist ungueltig")
		}
		c.MaxUnparsed = n
	}
	if v := os.Getenv("SESSION_SECRET"); v != "" {
		c.SessionSecret = []byte(v)
	} else {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return c, err
		}
		c.SessionSecret = []byte(hex.EncodeToString(b))
	}
	return c, nil
}
