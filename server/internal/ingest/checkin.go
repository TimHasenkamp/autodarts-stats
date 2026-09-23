package ingest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"autodarts-stats/internal/names"
)

var ErrUnknownChip = errors.New("unbekannter Chip")

// NormalizeUID vereinheitlicht Chip-Seriennummern (Hex, ohne Trenner, klein).
func NormalizeUID(uid string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(uid)) {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func ChipHash(uid string) string { return HashKey("chip:" + NormalizeUID(uid)) }

type Checkin struct {
	ID          int64  `json:"id"`
	PlayerID    int64  `json:"player_id"`
	DisplayName string `json:"display_name"`
	BoardID     int64  `json:"board_id"`
	BoardName   string `json:"board_name,omitempty"`
	Token       string `json:"token"`
	// GameName ist der Name, der in Autodarts eingetragen wird: "Tim#4831".
	GameName  string `json:"game_name"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
}

func gameName(display, token string) string { return display + "#" + token }

// CheckinByChip stempelt einen Chip an einem Board ein. Ein aktiver Check-in
// desselben Spielers am selben Board wird verlaengert (Code bleibt gleich).
func (s *Service) CheckinByChip(ctx context.Context, boardID int64, uid string, ttl time.Duration) (*Checkin, error) {
	h := ChipHash(uid)
	now := s.Now()
	var playerID int64
	err := s.DB.QueryRowContext(ctx, `SELECT player_id FROM player_chips WHERE uid_hash = ?`, h).Scan(&playerID)
	if errors.Is(err, sql.ErrNoRows) {
		hint := NormalizeUID(uid)
		if len(hint) > 4 {
			hint = "…" + hint[len(hint)-4:]
		}
		_, _ = s.DB.ExecContext(ctx, `INSERT INTO unknown_chips (uid_hash, uid_hint, board_id, first_seen_at, last_seen_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(uid_hash) DO UPDATE SET last_seen_at = excluded.last_seen_at, board_id = excluded.board_id, seen_count = seen_count + 1`,
			h, hint, boardID, ts(now), ts(now))
		return nil, ErrUnknownChip
	}
	if err != nil {
		return nil, err
	}
	_, _ = s.DB.ExecContext(ctx, `UPDATE player_chips SET last_used_at = ? WHERE uid_hash = ?`, ts(now), h)
	return s.CheckinPlayer(ctx, boardID, playerID, ttl)
}

// CheckinPlayer legt einen Check-in an oder verlaengert den aktiven.
func (s *Service) CheckinPlayer(ctx context.Context, boardID, playerID int64, ttl time.Duration) (*Checkin, error) {
	now := s.Now()
	exp := now.Add(ttl)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id int64
	var token string
	err = tx.QueryRowContext(ctx, `SELECT id, token FROM checkins WHERE player_id = ? AND board_id = ? AND expires_at > ? ORDER BY id DESC LIMIT 1`, playerID, boardID, ts(now)).Scan(&id, &token)
	switch {
	case err == nil:
		if _, err := tx.ExecContext(ctx, `UPDATE checkins SET expires_at = ? WHERE id = ?`, ts(exp), id); err != nil {
			return nil, err
		}
	case errors.Is(err, sql.ErrNoRows):
		token, err = s.freshToken(ctx, tx, boardID, now)
		if err != nil {
			return nil, err
		}
		r, err := tx.ExecContext(ctx, `INSERT INTO checkins (player_id, board_id, token, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`, playerID, boardID, token, ts(now), ts(exp))
		if err != nil {
			return nil, err
		}
		id, _ = r.LastInsertId()
	default:
		return nil, err
	}
	// Aufraeumen: abgelaufene Check-ins aelter als 30 Tage.
	_, _ = tx.ExecContext(ctx, `DELETE FROM checkins WHERE expires_at < ?`, ts(now.Add(-30*24*time.Hour)))
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.checkinByID(ctx, id)
}

func (s *Service) freshToken(ctx context.Context, tx *sql.Tx, boardID int64, now time.Time) (string, error) {
	for i := 0; i < 50; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(9000))
		if err != nil {
			return "", err
		}
		token := fmt.Sprintf("%04d", n.Int64()+1000)
		var c int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM checkins WHERE board_id = ? AND token = ? AND expires_at > ?`, boardID, token, ts(now.Add(-24*time.Hour))).Scan(&c); err != nil {
			return "", err
		}
		if c == 0 {
			return token, nil
		}
	}
	return "", errors.New("kein freier Code")
}

const checkinSelect = `SELECT c.id, c.player_id, p.display_name, c.board_id, b.name, c.token, c.created_at, c.expires_at
	FROM checkins c JOIN players p ON p.id = c.player_id JOIN boards b ON b.id = c.board_id`

func scanCheckin(row interface{ Scan(...any) error }) (*Checkin, error) {
	var c Checkin
	if err := row.Scan(&c.ID, &c.PlayerID, &c.DisplayName, &c.BoardID, &c.BoardName, &c.Token, &c.CreatedAt, &c.ExpiresAt); err != nil {
		return nil, err
	}
	c.GameName = gameName(c.DisplayName, c.Token)
	return &c, nil
}

func (s *Service) checkinByID(ctx context.Context, id int64) (*Checkin, error) {
	return scanCheckin(s.DB.QueryRowContext(ctx, checkinSelect+` WHERE c.id = ?`, id))
}

// ActiveCheckins liefert aktive Check-ins, optional nur fuer ein Board (0 = alle).
func (s *Service) ActiveCheckins(ctx context.Context, boardID int64) ([]Checkin, error) {
	q := checkinSelect + ` WHERE c.expires_at > ?`
	args := []any{ts(s.Now())}
	if boardID > 0 {
		q += ` AND c.board_id = ?`
		args = append(args, boardID)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY c.created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Checkin{}
	for rows.Next() {
		c, err := scanCheckin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Service) EndCheckin(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE checkins SET expires_at = ? WHERE id = ?`, ts(s.Now()), id)
	return err
}

// RegisterChip bindet einen (bisher unbekannten) Chip an einen Spieler.
func (s *Service) RegisterChip(ctx context.Context, uidHash string, playerID int64, label string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO player_chips (player_id, uid_hash, label, created_at) VALUES (?, ?, ?, ?)`, playerID, uidHash, label, ts(s.Now())); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM unknown_chips WHERE uid_hash = ?`, uidHash); err != nil {
		return err
	}
	return tx.Commit()
}

var tokenRe = regexp.MustCompile(`^(.*?)\s*#\s*(\d{4})\s*$`)

// splitToken trennt "Tim#4831" in ("Tim", "4831"). Ohne Code: (name, "").
func splitToken(name string) (string, string) {
	m := tokenRe.FindStringSubmatch(strings.TrimSpace(name))
	if m == nil {
		return strings.TrimSpace(name), ""
	}
	return strings.TrimSpace(m[1]), m[2]
}

// lookupToken sucht einen aktiven Check-in zu Board+Code. Die Spielzeit
// (playedAt) zaehlt, nicht der Verarbeitungszeitpunkt, damit reprocess
// spaeter dasselbe Ergebnis liefert.
func (s *Service) lookupToken(ctx context.Context, tx *sql.Tx, boardID int64, token string, playedAt time.Time) (int64, bool, error) {
	var pid int64
	err := tx.QueryRowContext(ctx, `SELECT player_id FROM checkins WHERE board_id = ? AND token = ? AND created_at <= ? AND expires_at > ? ORDER BY id DESC LIMIT 1`,
		boardID, token, ts(playedAt.Add(10*time.Minute)), ts(playedAt)).Scan(&pid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return pid, err == nil, err
}

// CreatePlayer legt einen Spieler manuell an (Admin, z.B. bei Chip-Registrierung).
func (s *Service) CreatePlayer(ctx context.Context, displayName string, protected bool) (int64, error) {
	name := strings.TrimSpace(displayName)
	norm := names.Normalize(name)
	if norm == "" {
		return 0, errors.New("Name fehlt")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `INSERT INTO players (display_name, normalized_name, protected) VALUES (?, ?, ?)`, name, norm, protected)
	if err != nil {
		return 0, err
	}
	id, _ := r.LastInsertId()
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO player_aliases (normalized_name, player_id) VALUES (?, ?)`, norm, id); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}
