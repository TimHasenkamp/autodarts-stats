package ingest

import (
	"context"
	"errors"
)

// ErrInvalidEvent kennzeichnet Events, die dauerhaft nicht verarbeitet werden
// koennen: Erneutes Senden wuerde genauso scheitern. Die API meldet sie pro
// Event und bestaetigt den Batch trotzdem, damit die Extension nicht endlos
// wiederholt. Alle anderen Fehler gelten als voruebergehend (z.B.
// SQLITE_BUSY, I/O, abgebrochener Kontext) und fuehren zu 503 + Retry.
var ErrInvalidEvent = errors.New("event dauerhaft nicht verarbeitbar")

// invalidEvent verpackt einen Fehler als dauerhaft (errors.Is(err,
// ErrInvalidEvent)), ohne die Ursache zu verlieren.
type invalidEvent struct{ err error }

func (e *invalidEvent) Error() string        { return "event ungueltig: " + e.err.Error() }
func (e *invalidEvent) Unwrap() error        { return e.err }
func (e *invalidEvent) Is(target error) bool { return target == ErrInvalidEvent }

// Invalid markiert err als dauerhaften Fehler.
func Invalid(err error) error {
	if err == nil || errors.Is(err, ErrInvalidEvent) {
		return err
	}
	return &invalidEvent{err: err}
}

// Primaere SQLite-Ergebniscodes (stabil, siehe sqlite.org/rescode.html), die
// deterministisch vom Inhalt abhaengen und beim Wiederholen gleich ausfallen.
const (
	sqliteTooBig     = 18
	sqliteConstraint = 19
	sqliteMismatch   = 20
	sqliteRange      = 25
)

// IsPermanent meldet, ob err ein dauerhafter, inhaltsbedingter Fehler ist.
// Im Zweifel false: Ein unnoetiger Retry kostet nichts (Ingest ist
// idempotent), ein verworfenes Event dagegen Daten.
func IsPermanent(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, ErrInvalidEvent) {
		return true
	}
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		switch coded.Code() & 0xff { // erweiterte Codes auf den primaeren reduzieren
		case sqliteTooBig, sqliteConstraint, sqliteMismatch, sqliteRange:
			return true
		}
	}
	return false
}
