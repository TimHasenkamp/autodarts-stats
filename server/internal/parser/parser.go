// Package parser definiert das Format-unabhaengige Zwischenmodell eines
// Match-Zustands und das Interface, das konkrete Parser implementieren.
package parser

import (
	"encoding/json"
	"errors"
	"time"
)

// ErrNotRecognized meldet ein Parser, wenn ihm das Payload nichts sagt.
var ErrNotRecognized = errors.New("payload nicht erkannt")

type PlayerRef struct {
	Index  int
	Name   string
	UserID string // leer bei Gastspielern
	IsBot  bool
}

// PlayerStats sind Wurfstatistiken eines Spielers, entweder fuer ein Leg
// oder kumuliert fuer das Match. Pointer = "nicht bekannt".
type PlayerStats struct {
	Points           int // erzielte Punkte (fuer gewichteten Average)
	Darts            int
	Average          *float64
	First9Avg        *float64
	CheckoutRate     *float64
	CheckoutsHit     int
	CheckoutAttempts int
	HighestCheckout  int
	Count180         int
	Count140Plus     int
	Count100Plus     int
	LegsWon          int
}

// State ist ein Snapshot eines laufenden oder beendeten Matches.
type State struct {
	MatchID   string
	Variant   string
	Settings  json.RawMessage
	StartedAt time.Time
	Finished  bool
	Set       int // 1-basiert
	Leg       int // 1-basiert
	Players   []PlayerRef
	// Winner ist der Index des Matchgewinners, -1 wenn unbekannt/offen.
	Winner int
	// LegWinner ist der Index des Gewinners des aktuellen Legs, -1 wenn offen.
	LegWinner int
	// HasScoring: Variante mit Punkte-Average (X01). Sonst bleiben
	// Average-Felder leer und nur Siege/Darts werden gezaehlt.
	HasScoring bool
	// MatchStats und LegStats sind indexgleich mit Players; koennen nil sein.
	MatchStats []PlayerStats
	LegStats   []PlayerStats
}

type Parser interface {
	Name() string
	// Parse liefert ErrNotRecognized, wenn das Payload kein Matchzustand
	// dieses Formats ist. kind ist "fetch" oder "ws".
	Parse(kind, url string, body []byte) (*State, error)
}

// ParseAny probiert alle Parser der Reihe nach.
func ParseAny(parsers []Parser, kind, url string, body []byte) (*State, string, error) {
	var lastErr error = ErrNotRecognized
	for _, p := range parsers {
		st, err := p.Parse(kind, url, body)
		if err == nil {
			return st, p.Name(), nil
		}
		if !errors.Is(err, ErrNotRecognized) {
			lastErr = err
		}
	}
	return nil, "", lastErr
}
