// Package ingest nimmt Rohdaten der Extension entgegen, speichert sie und
// leitet daraus Match-, Spieler- und Leg-Statistiken ab.
//
// Ablauf pro Event:
//  1. Parser erkennen das Payload (sonst -> unparsed_events).
//  2. matches.raw_json haelt immer den letzten Stand des Matches.
//  3. Wechselt Set/Leg oder endet das Match, wird der vorige Stand als
//     Leg-Endstand in match_snapshots archiviert und daraus match_legs gefuellt.
//  4. match_players wird aus dem aktuellen Stand fortgeschrieben.
package ingest

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"autodarts-stats/internal/names"
	"autodarts-stats/internal/parser"
)

var ErrUnauthorized = errors.New("ungueltiger API-Key")

type Event struct {
	Kind string          `json:"kind"` // "fetch" | "ws"
	URL  string          `json:"url"`
	TS   int64           `json:"ts"` // Unix-Millisekunden (Client)
	Body json.RawMessage `json:"body"`
}

type Result struct {
	Recognized bool   `json:"recognized"`
	Parser     string `json:"parser,omitempty"`
	MatchID    string `json:"match_id,omitempty"`
	LegsSaved  int    `json:"legs_saved"`
	Finished   bool   `json:"finished"`
}

// matchCtx buendelt, was die Spielerzuordnung ueber ein Match wissen muss.
type matchCtx struct {
	matchID  int64
	boardID  int64
	playedAt time.Time
}

type Service struct {
	DB          *sql.DB
	Parsers     []parser.Parser
	MaxUnparsed int
	Now         func() time.Time
}

func New(db *sql.DB, parsers []parser.Parser, maxUnparsed int) *Service {
	return &Service{DB: db, Parsers: parsers, MaxUnparsed: maxUnparsed, Now: func() time.Time { return time.Now().UTC() }}
}

func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// AuthBoard prueft den API-Key und liefert die Board-ID.
func (s *Service) AuthBoard(ctx context.Context, key string) (int64, string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return 0, "", ErrUnauthorized
	}
	h := HashKey(key)
	var id int64
	var name, stored string
	err := s.DB.QueryRowContext(ctx, `SELECT id, name, api_key_hash FROM boards WHERE api_key_hash = ?`, h).Scan(&id, &name, &stored)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && subtle.ConstantTimeCompare([]byte(stored), []byte(h)) != 1) {
		return 0, "", ErrUnauthorized
	}
	if err != nil {
		return 0, "", err
	}
	_, _ = s.DB.ExecContext(ctx, `UPDATE boards SET last_seen_at = ? WHERE id = ?`, ts(s.Now()), id)
	return id, name, nil
}

func ts(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// normalizeBody akzeptiert das Payload in beiden Formen: direkt als
// JSON-Objekt oder als JSON-Zeichenkette. Die Extension reicht den
// Antworttext roh weiter und verpackt ihn damit als String.
func normalizeBody(b json.RawMessage) json.RawMessage {
	t := json.RawMessage(strings.TrimSpace(string(b)))
	if len(t) > 0 && t[0] == '"' {
		var inner string
		if err := json.Unmarshal(t, &inner); err == nil {
			return json.RawMessage(strings.TrimSpace(inner))
		}
	}
	return t
}

// HandleEvent verarbeitet ein einzelnes Event idempotent.
func (s *Service) HandleEvent(ctx context.Context, boardID int64, ev Event) (Result, error) {
	ev.Body = normalizeBody(ev.Body)
	st, pname, err := parser.ParseAny(s.Parsers, ev.Kind, ev.URL, ev.Body)
	if err != nil {
		reason := err.Error()
		if perr := s.storeUnparsed(ctx, boardID, ev, reason); perr != nil {
			return Result{}, perr
		}
		return Result{Recognized: false}, nil
	}
	res := Result{Recognized: true, Parser: pname, MatchID: st.MatchID, Finished: st.Finished}
	now := s.Now()
	if st.StartedAt.IsZero() {
		if ev.TS > 0 {
			st.StartedAt = time.UnixMilli(ev.TS).UTC()
		} else {
			st.StartedAt = now
		}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	var (
		matchID          int64
		prevRaw          string
		prevSet, prevLeg int
		prevFinished     bool
		playedAtStr      string
		exists           = true
	)
	err = tx.QueryRowContext(ctx, `SELECT id, raw_json, last_set, last_leg, finished, played_at FROM matches WHERE autodarts_match_id = ?`, st.MatchID).
		Scan(&matchID, &prevRaw, &prevSet, &prevLeg, &prevFinished, &playedAtStr)
	if errors.Is(err, sql.ErrNoRows) {
		exists = false
	} else if err != nil {
		return res, err
	}

	if !exists {
		r, err := tx.ExecContext(ctx, `INSERT INTO matches (autodarts_match_id, board_id, played_at, variant, settings_json, finished, last_set, last_leg, raw_json, received_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			st.MatchID, boardID, ts(st.StartedAt), st.Variant, string(st.Settings), st.Finished, st.Set, st.Leg, string(ev.Body), ts(now), ts(now))
		if err != nil {
			return res, err
		}
		matchID, _ = r.LastInsertId()
		playedAtStr = ts(st.StartedAt)
	} else {
		// Aeltere oder identische Staende nach Matchende ignorieren.
		if prevFinished && !st.Finished {
			return res, tx.Commit()
		}
		if (st.Set < prevSet) || (st.Set == prevSet && st.Leg < prevLeg) {
			return res, tx.Commit()
		}
		legChanged := st.Set != prevSet || st.Leg != prevLeg
		if legChanged && !prevFinished {
			// Endstand des vorigen Legs archivieren.
			n, err := s.archiveLeg(ctx, tx, mcFrom(matchID, boardID, playedAtStr), prevSet, prevLeg, prevRaw, now)
			if err != nil {
				return res, err
			}
			res.LegsSaved += n
		}
		if _, err := tx.ExecContext(ctx, `UPDATE matches SET raw_json = ?, last_set = ?, last_leg = ?, finished = ?, variant = ?, settings_json = ?, updated_at = ?, board_id = COALESCE(board_id, ?) WHERE id = ?`,
			string(ev.Body), st.Set, st.Leg, st.Finished, st.Variant, string(st.Settings), ts(now), boardID, matchID); err != nil {
			return res, err
		}
	}

	mc := mcFrom(matchID, boardID, playedAtStr)
	if st.Finished {
		n, err := s.archiveLeg(ctx, tx, mc, st.Set, st.Leg, string(ev.Body), now)
		if err != nil {
			return res, err
		}
		res.LegsSaved += n
	}
	if err := s.applyMatchState(ctx, tx, mc, st); err != nil {
		return res, err
	}
	return res, tx.Commit()
}

func mcFrom(matchID, boardID int64, playedAt string) matchCtx {
	t, _ := time.Parse("2006-01-02T15:04:05.000Z", playedAt)
	return matchCtx{matchID: matchID, boardID: boardID, playedAt: t}
}

// archiveLeg speichert den Leg-Endstand und leitet match_legs daraus ab.
// Liefert 1, wenn ein Leg mit Gewinner verbucht wurde.
func (s *Service) archiveLeg(ctx context.Context, tx *sql.Tx, mc matchCtx, set, leg int, raw string, now time.Time) (int, error) {
	if _, err := tx.ExecContext(ctx, `INSERT INTO match_snapshots (match_id, set_no, leg_no, raw_json, received_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(match_id, set_no, leg_no) DO UPDATE SET raw_json = excluded.raw_json, received_at = excluded.received_at`,
		mc.matchID, set, leg, raw, ts(now)); err != nil {
		return 0, err
	}
	st, _, err := parser.ParseAny(s.Parsers, "", "", []byte(raw))
	if err != nil {
		return 0, nil
	}
	return s.applyLegState(ctx, tx, mc, set, leg, st)
}

func (s *Service) applyLegState(ctx context.Context, tx *sql.Tx, mc matchCtx, set, leg int, st *parser.State) (int, error) {
	matchID := mc.matchID
	if st.LegWinner < 0 || st.LegWinner >= len(st.Players) {
		return 0, nil // Leg ohne Gewinner (abgebrochen / Snapshot verpasst)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM match_legs WHERE match_id = ? AND set_no = ? AND leg_no = ?`, matchID, set, leg); err != nil {
		return 0, err
	}
	seen := map[int64]bool{}
	for i, p := range st.Players {
		pid, ok, err := s.resolvePlayer(ctx, tx, mc, p)
		if err != nil {
			return 0, err
		}
		if !ok || seen[pid] {
			continue // Bots, wartende Slots bzw. zwei Slots, die (nach Merge) derselbe Spieler sind
		}
		seen[pid] = true
		var ls parser.PlayerStats
		if i < len(st.LegStats) {
			ls = st.LegStats[i]
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO match_legs (match_id, set_no, leg_no, player_id, won, darts, points, average, first9_avg, checkout, checkouts_hit, checkout_attempts, count_180, count_140plus, count_100plus)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			matchID, set, leg, pid, p.Index == st.LegWinner, ls.Darts, nullPoints(st.HasScoring, ls.Points), ls.Average, ls.First9Avg, ls.HighestCheckout, ls.CheckoutsHit, ls.CheckoutAttempts, ls.Count180, ls.Count140Plus, ls.Count100Plus); err != nil {
			return 0, err
		}
	}
	return 1, nil
}

func nullPoints(scoring bool, points int) any {
	if !scoring {
		return nil
	}
	return points
}

// applyMatchState schreibt match_players aus dem aktuellen Stand fort.
// Legs/Checkouts/180er kommen aus match_legs, damit sie konsistent bleiben.
func (s *Service) applyMatchState(ctx context.Context, tx *sql.Tx, mc matchCtx, st *parser.State) error {
	matchID := mc.matchID
	seen := map[int64]bool{}
	for i, p := range st.Players {
		pid, ok, err := s.resolvePlayer(ctx, tx, mc, p)
		if err != nil {
			return err
		}
		if !ok || seen[pid] {
			continue
		}
		seen[pid] = true
		var ms parser.PlayerStats
		if i < len(st.MatchStats) {
			ms = st.MatchStats[i]
		}
		won := st.Finished && p.Index == st.Winner
		if _, err := tx.ExecContext(ctx, `INSERT INTO match_players (match_id, player_id, player_index, won, average, first9_avg, checkout_rate, checkouts_hit, checkout_attempts, darts_thrown, legs_won)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(match_id, player_id) DO UPDATE SET player_index = excluded.player_index, won = excluded.won, average = excluded.average, first9_avg = excluded.first9_avg,
				checkout_rate = excluded.checkout_rate, checkouts_hit = excluded.checkouts_hit, checkout_attempts = excluded.checkout_attempts, darts_thrown = excluded.darts_thrown, legs_won = excluded.legs_won`,
			matchID, pid, p.Index, won, ms.Average, ms.First9Avg, ms.CheckoutRate, ms.CheckoutsHit, ms.CheckoutAttempts, ms.Darts, ms.LegsWon); err != nil {
			return err
		}
	}
	// Aus Legs abgeleitete Felder nachziehen.
	_, err := tx.ExecContext(ctx, `UPDATE match_players SET
		legs_played = (SELECT COUNT(*) FROM match_legs l WHERE l.match_id = match_players.match_id AND l.player_id = match_players.player_id),
		legs_won = MAX(legs_won, (SELECT COUNT(*) FROM match_legs l WHERE l.match_id = match_players.match_id AND l.player_id = match_players.player_id AND l.won = 1)),
		highest_checkout = COALESCE((SELECT MAX(checkout) FROM match_legs l WHERE l.match_id = match_players.match_id AND l.player_id = match_players.player_id), 0),
		count_180 = COALESCE((SELECT SUM(count_180) FROM match_legs l WHERE l.match_id = match_players.match_id AND l.player_id = match_players.player_id), 0),
		count_140plus = COALESCE((SELECT SUM(count_140plus) FROM match_legs l WHERE l.match_id = match_players.match_id AND l.player_id = match_players.player_id), 0),
		count_100plus = COALESCE((SELECT SUM(count_100plus) FROM match_legs l WHERE l.match_id = match_players.match_id AND l.player_id = match_players.player_id), 0)
		WHERE match_id = ?`, matchID)
	return err
}

// resolvePlayer ordnet einen Match-Slot einer Spieler-ID zu (ok=false: Slot
// wird nicht gewertet, z.B. Bot oder wartet auf Freigabe).
//
// Reihenfolge:
//  1. manuelle/Check-in-Zuordnung (match_overrides)
//  2. Name mit Code "Tim#4831" -> aktiver Check-in am Board, sonst Freigabe-Queue
//  3. Autodarts-User-ID
//  4. normalisierter Name bzw. Alias; geschuetzte Spieler ohne Code -> Freigabe-Queue
//  5. neuen Spieler anlegen
func (s *Service) resolvePlayer(ctx context.Context, tx *sql.Tx, mc matchCtx, p parser.PlayerRef) (int64, bool, error) {
	if p.IsBot {
		return 0, false, nil
	}
	var ov sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT player_id FROM match_overrides WHERE match_id = ? AND player_index = ?`, mc.matchID, p.Index).Scan(&ov)
	if err == nil {
		if ov.Valid {
			return ov.Int64, true, nil
		}
		return 0, false, nil // Slot bewusst ignoriert
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}

	base, token := splitToken(p.Name)
	norm := names.Normalize(base)
	if norm == "" && p.UserID == "" {
		return 0, false, nil
	}

	if token != "" {
		pid, ok, err := s.lookupToken(ctx, tx, mc.boardID, token, mc.playedAt)
		if err != nil {
			return 0, false, err
		}
		if ok {
			if err := setOverride(ctx, tx, mc.matchID, p.Index, pid, "checkin", s.Now()); err != nil {
				return 0, false, err
			}
			return pid, true, nil
		}
		return s.pending(ctx, tx, mc, p, base, 0, "Code ungueltig oder abgelaufen")
	}

	var id int64
	if p.UserID != "" {
		err := tx.QueryRowContext(ctx, `SELECT id FROM players WHERE autodarts_user_id = ?`, p.UserID).Scan(&id)
		if err == nil {
			return id, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, false, err
		}
	}

	if norm != "" {
		var candidate int64
		var protected bool
		var candUID sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT p.id, p.protected, p.autodarts_user_id FROM players p
			WHERE p.id = (SELECT player_id FROM player_aliases WHERE normalized_name = ?) OR p.normalized_name = ? LIMIT 1`, norm, norm).
			Scan(&candidate, &protected, &candUID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, false, err
		}
		if err == nil {
			switch {
			case protected:
				return s.pending(ctx, tx, mc, p, base, candidate, "geschuetzter Name ohne Check-in-Code")
			case p.UserID != "" && candUID.Valid && candUID.String != "" && candUID.String != p.UserID:
				// Name gehoert einem anderen Account: eigener Spieler (unten).
			default:
				if p.UserID != "" {
					_, _ = tx.ExecContext(ctx, `UPDATE players SET autodarts_user_id = ? WHERE id = ? AND autodarts_user_id IS NULL AND NOT EXISTS (SELECT 1 FROM players WHERE autodarts_user_id = ?)`, p.UserID, candidate, p.UserID)
				}
				return candidate, true, nil
			}
		}
	}

	name := strings.TrimSpace(base)
	if norm == "" {
		name = "Spieler " + p.UserID[:min(8, len(p.UserID))]
		norm = names.Normalize(name)
	}
	// Freien normalisierten Namen finden (Kollision mit fremdem Account).
	for i := 2; ; i++ {
		var c int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM players WHERE normalized_name = ?`, norm).Scan(&c); err != nil {
			return 0, false, err
		}
		if c == 0 {
			break
		}
		name = fmt.Sprintf("%s (%d)", strings.TrimSpace(base), i)
		norm = names.Normalize(name)
	}
	var uid any
	if p.UserID != "" {
		uid = p.UserID
	}
	r, err := tx.ExecContext(ctx, `INSERT INTO players (display_name, normalized_name, autodarts_user_id) VALUES (?, ?, ?)`, name, norm, uid)
	if err != nil {
		return 0, false, fmt.Errorf("spieler anlegen %q: %w", name, err)
	}
	id, _ = r.LastInsertId()
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO player_aliases (normalized_name, player_id) VALUES (?, ?)`, norm, id); err != nil {
		return 0, false, err
	}
	return id, true, nil
}

func setOverride(ctx context.Context, tx *sql.Tx, matchID int64, index int, playerID int64, source string, now time.Time) error {
	var pid any
	if playerID > 0 {
		pid = playerID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO match_overrides (match_id, player_index, player_id, source, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(match_id, player_index) DO UPDATE SET player_id = excluded.player_id, source = excluded.source, created_at = excluded.created_at`,
		matchID, index, pid, source, ts(now)); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM match_pending WHERE match_id = ? AND player_index = ?`, matchID, index)
	return err
}

func (s *Service) pending(ctx context.Context, tx *sql.Tx, mc matchCtx, p parser.PlayerRef, base string, suggested int64, reason string) (int64, bool, error) {
	var sug any
	if suggested > 0 {
		sug = suggested
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO match_pending (match_id, player_index, name, user_id, suggested_id, reason, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(match_id, player_index) DO UPDATE SET name = excluded.name, user_id = excluded.user_id, suggested_id = excluded.suggested_id, reason = excluded.reason`,
		mc.matchID, p.Index, strings.TrimSpace(p.Name), p.UserID, sug, reason, ts(s.Now()))
	return 0, false, err
}

// ResolvePending ordnet einen wartenden Slot zu (playerID 0 = ignorieren)
// und berechnet das Match neu. Bringt der Slot eine Autodarts-User-ID mit und
// der Zielspieler hat noch keine, wird sie uebernommen.
func (s *Service) ResolvePending(ctx context.Context, matchID int64, index int, playerID int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var userID string
	_ = tx.QueryRowContext(ctx, `SELECT user_id FROM match_pending WHERE match_id = ? AND player_index = ?`, matchID, index).Scan(&userID)
	if err := setOverride(ctx, tx, matchID, index, playerID, "admin", s.Now()); err != nil {
		return err
	}
	if playerID > 0 && userID != "" {
		_, _ = tx.ExecContext(ctx, `UPDATE players SET autodarts_user_id = ? WHERE id = ? AND autodarts_user_id IS NULL AND NOT EXISTS (SELECT 1 FROM players WHERE autodarts_user_id = ?)`, userID, playerID, userID)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.ReprocessMatch(ctx, matchID)
}

func (s *Service) storeUnparsed(ctx context.Context, boardID int64, ev Event, reason string) error {
	if s.MaxUnparsed <= 0 {
		return nil
	}
	body := ev.Body
	if len(body) > 512*1024 {
		body = body[:512*1024]
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO unparsed_events (board_id, kind, url, reason, raw_json, received_at) VALUES (?, ?, ?, ?, ?, ?)`,
		boardID, ev.Kind, ev.URL, reason, string(body), ts(s.Now())); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM unparsed_events WHERE id NOT IN (SELECT id FROM unparsed_events ORDER BY id DESC LIMIT ?)`, s.MaxUnparsed)
	return err
}

// ReprocessUnparsed schickt gespeicherte unerkannte Events erneut durch die
// Parser (nach einer Parser-Anpassung). Erkannte werden geloescht.
func (s *Service) ReprocessUnparsed(ctx context.Context) (recognized, total int, err error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, COALESCE(board_id,0), kind, url, raw_json FROM unparsed_events ORDER BY id`)
	if err != nil {
		return 0, 0, err
	}
	type row struct {
		id, board int64
		kind, url string
		raw       string
	}
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.board, &r.kind, &r.url, &r.raw); err != nil {
			rows.Close()
			return 0, 0, err
		}
		list = append(list, r)
	}
	rows.Close()
	saveMax := s.MaxUnparsed
	s.MaxUnparsed = 0 // waehrenddessen nichts erneut als unparsed speichern
	defer func() { s.MaxUnparsed = saveMax }()
	for _, r := range list {
		res, err := s.HandleEvent(ctx, r.board, Event{Kind: r.kind, URL: r.url, Body: json.RawMessage(r.raw)})
		if err != nil {
			return recognized, len(list), fmt.Errorf("event %d: %w", r.id, err)
		}
		if res.Recognized {
			recognized++
			if _, err := s.DB.ExecContext(ctx, `DELETE FROM unparsed_events WHERE id = ?`, r.id); err != nil {
				return recognized, len(list), err
			}
		}
	}
	return recognized, len(list), nil
}

// Reprocess berechnet match_players und match_legs aller Matches aus den
// gespeicherten Rohdaten neu (z.B. nach Parser-Aenderungen).
func (s *Service) Reprocess(ctx context.Context) (int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM matches ORDER BY id`)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if err := s.ReprocessMatch(ctx, id); err != nil {
			return 0, fmt.Errorf("match %d: %w", id, err)
		}
	}
	return len(ids), nil
}

// ReprocessMatch berechnet ein einzelnes Match aus seinen Rohdaten neu.
func (s *Service) ReprocessMatch(ctx context.Context, matchID int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw, playedAt string
	var boardID sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT raw_json, board_id, played_at FROM matches WHERE id = ?`, matchID).Scan(&raw, &boardID, &playedAt); err != nil {
		return err
	}
	mc := mcFrom(matchID, boardID.Int64, playedAt)
	if _, err := tx.ExecContext(ctx, `DELETE FROM match_pending WHERE match_id = ?`, matchID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM match_legs WHERE match_id = ?`, matchID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM match_players WHERE match_id = ?`, matchID); err != nil {
		return err
	}
	snaps, err := tx.QueryContext(ctx, `SELECT set_no, leg_no, raw_json FROM match_snapshots WHERE match_id = ? ORDER BY set_no, leg_no`, matchID)
	if err != nil {
		return err
	}
	type snap struct {
		set, leg int
		raw      string
	}
	var list []snap
	for snaps.Next() {
		var sn snap
		if err := snaps.Scan(&sn.set, &sn.leg, &sn.raw); err != nil {
			snaps.Close()
			return err
		}
		list = append(list, sn)
	}
	snaps.Close()
	for _, sn := range list {
		st, _, err := parser.ParseAny(s.Parsers, "", "", []byte(sn.raw))
		if err != nil {
			continue
		}
		if _, err := s.applyLegState(ctx, tx, mc, sn.set, sn.leg, st); err != nil {
			return err
		}
	}
	st, _, err := parser.ParseAny(s.Parsers, "", "", []byte(raw))
	if err != nil {
		return tx.Commit() // Rohdaten nicht mehr lesbar: Match bleibt ohne Aggregate.
	}
	if _, err := tx.ExecContext(ctx, `UPDATE matches SET variant = ?, settings_json = ?, finished = ?, last_set = ?, last_leg = ? WHERE id = ?`,
		st.Variant, string(st.Settings), st.Finished, st.Set, st.Leg, matchID); err != nil {
		return err
	}
	if err := s.applyMatchState(ctx, tx, mc, st); err != nil {
		return err
	}
	return tx.Commit()
}
