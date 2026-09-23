// Package autodarts parst Match-Zustaende von play.autodarts.io.
//
// Die Feldnamen wurden am 2026-09-23 gegen das ausgelieferte JavaScript der
// Live-App abgeglichen und dort bestaetigt: turns, throws, segment,
// gameWinner, gameScores, matchStats, legStats sowie in den Stats average,
// first9Average, checkoutPercent, checkoutsHit, dartsThrown, plus100,
// plus140, total180, legsWon.
//
// Adressen der Live-App (Stand 2026-09-23):
//
//	https://api.autodarts.com/gs/v0/matches/{id}      Matchzustand
//	https://api.autodarts.com/as/v0/matches/{id}/stats
//	https://api.autodarts.com/bs/v0/boards            Boards
//	wss://play.ws.autodarts.com/ms/v0/subscribe       Live-Events
//	Kanal autodarts.matches, Topic {matchId}.state
//
// Ebenfalls im JS der App bestaetigt:
//
//   - set und leg zaehlen ab 1. Ein frisches Match hat set:1, leg:1.
//   - gameFinished meldet das Ende eines Legs, finished das Ende des Matches.
//   - gameWinner ist der Leg-Gewinner, winner der Match-Gewinner, -1 = offen.
//   - Der WebSocket verpackt alles als {type, channel, topic, data}. Beim
//     Topic "<matchId>.state" steckt die Match-ID nur im Topic, nicht im data.
//   - turns tragen playerId (nicht den Index), players tragen id, name,
//     userId und bei Bots cpuPPR.
//
// Gegen eine echte Antwort geprueft, siehe testdata/match_x01_initial.json:
//
//   - stats[] hat pro Spieler matchStats, setStats und legStats.
//   - Darin sind less60, plus60, plus100, plus140, plus170 und total180
//     disjunkte Klassen (unter 60, 60-99, 100-139, 140-169, 170-179, genau
//     180), keine kumulativen Zaehler.
//   - score ist die Summe der erzielten Punkte, dartsThrown die Zahl der
//     Darts. Beide haben Vorrang vor der eigenen Zaehlung aus turns.
//   - turns tragen throws als Array. Die laufende Runde hat ein leeres Array
//     und darf nicht als volle Aufnahme zaehlen.
//
// TODO(format): Ein Stand mitten im Leg, ein Leg-Ende und ein Matchende
// fehlen noch als echte Beispiele. Der Parser ist bewusst tolerant: fehlende
// Felder fuehren zu Nullwerten, nicht zu Fehlern.
package autodarts

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"autodarts-stats/internal/parser"
)

type Parser struct{}

func New() *Parser { return &Parser{} }

func (Parser) Name() string { return "autodarts" }

// wsEnvelope: Nachrichten des Autodarts-WebSockets sind in channel/topic/data
// verpackt. TODO(format): pruefen.
type wsEnvelope struct {
	Channel string          `json:"channel"`
	Topic   string          `json:"topic"`
	Data    json.RawMessage `json:"data"`
}

// rawMatch ist das angenommene Matchobjekt. TODO(format): pruefen.
type rawMatch struct {
	ID         string          `json:"id"`
	Variant    string          `json:"variant"`
	Type       string          `json:"type"`
	CreatedAt  string          `json:"createdAt"`
	Finished   bool            `json:"finished"`
	Settings   json.RawMessage `json:"settings"`
	Set        int             `json:"set"`
	Leg        int             `json:"leg"`
	Player     int             `json:"player"`
	Winner     *int            `json:"winner"`
	GameWinner *int            `json:"gameWinner"`
	// gameFinished meldet das Ende des aktuellen Legs (nicht des Matches).
	GameFinished bool            `json:"gameFinished"`
	Players      []rawPlayer     `json:"players"`
	Scores       []rawScore      `json:"scores"`
	Turns        []rawTurn       `json:"turns"`
	Stats        []rawStatsEntry `json:"stats"`
}

type rawPlayer struct {
	ID     string   `json:"id"`
	Index  *int     `json:"index"`
	Name   string   `json:"name"`
	UserID string   `json:"userId"`
	CPUPPR *float64 `json:"cpuPPR"`
	User   *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"user"`
}

type rawScore struct {
	Legs int `json:"legs"`
	Sets int `json:"sets"`
}

type rawTurn struct {
	PlayerID string `json:"playerId"`
	Player   *int   `json:"player"`
	Round    int    `json:"round"`
	Score    int    `json:"score"`
	Points   int    `json:"points"`
	Busted   bool   `json:"busted"`
	Throws   []struct {
		Segment struct {
			Name       string `json:"name"`
			Number     int    `json:"number"`
			Multiplier int    `json:"multiplier"`
		} `json:"segment"`
	} `json:"throws"`
}

// rawStatsEntry: entweder flache Felder oder in matchStats/legStats/setStats
// verschachtelt. TODO(format): pruefen, welche Variante die API liefert.
type rawStatsEntry struct {
	rawStatsFields
	MatchStats *rawStatsFields `json:"matchStats"`
	LegStats   *rawStatsFields `json:"legStats"`
}

// rawStatsFields bildet die Stats der Autodarts-Antwort ab.
// less60, plus60, plus100, plus140, plus170 und total180 sind disjunkte
// Klassen (unter 60, 60-99, 100-139, 140-169, 170-179, genau 180). Intern
// wird kumulativ gezaehlt, deshalb werden sie unten aufaddiert.
type rawStatsFields struct {
	Average         *float64 `json:"average"`
	First9Average   *float64 `json:"first9Average"`
	CheckoutPercent *float64 `json:"checkoutPercent"`
	CheckoutsHit    *int     `json:"checkoutsHit"`
	Checkouts       *int     `json:"checkouts"`
	CheckoutPoints  *int     `json:"checkoutPoints"`
	DartsThrown     *int     `json:"dartsThrown"`
	Score           *int     `json:"score"`
	Less60          *int     `json:"less60"`
	Plus60          *int     `json:"plus60"`
	Plus100         *int     `json:"plus100"`
	Plus140         *int     `json:"plus140"`
	Plus170         *int     `json:"plus170"`
	Total180        *int     `json:"total180"`
}

func (p Parser) Parse(kind, url string, body []byte) (*parser.State, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return nil, parser.ErrNotRecognized
	}
	// Der WebSocket verpackt den Zustand als {type, channel, topic, data}.
	// Die Match-ID steckt dabei im Topic ("<matchId>.state"), nicht im Payload.
	var topicID string
	var env wsEnvelope
	if err := json.Unmarshal(body, &env); err == nil && len(env.Data) > 0 && env.Channel != "" {
		if !strings.Contains(env.Channel, "match") {
			return nil, parser.ErrNotRecognized
		}
		if i := strings.IndexByte(env.Topic, '.'); i > 0 {
			topicID = env.Topic[:i]
		}
		body = env.Data
	}
	var m rawMatch
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, parser.ErrNotRecognized
	}
	if m.ID == "" {
		m.ID = topicID
	}
	if m.ID == "" || len(m.Players) == 0 || (m.Variant == "" && m.Turns == nil && m.Stats == nil) {
		return nil, parser.ErrNotRecognized
	}
	return convert(&m), nil
}

func convert(m *rawMatch) *parser.State {
	// set und leg zaehlt Autodarts ab 1 (gegen eine echte Antwort geprueft,
	// siehe testdata/match_x01_initial.json). Der Wert wird unveraendert
	// uebernommen. Wichtig: nicht auf 1 hochsetzen, sonst waeren ein Stand
	// mit 0 und einer mit 1 nicht unterscheidbar.
	st := &parser.State{
		MatchID:     m.ID,
		Variant:     m.Variant,
		Settings:    m.Settings,
		Finished:    m.Finished,
		Set:         m.Set,
		Leg:         m.Leg,
		Winner:      -1,
		LegWinner:   -1,
		LegFinished: m.GameFinished,
	}
	if len(st.Settings) == 0 {
		st.Settings = json.RawMessage("{}")
	}
	if t, err := time.Parse(time.RFC3339Nano, m.CreatedAt); err == nil {
		st.StartedAt = t.UTC()
	}
	if m.Winner != nil {
		st.Winner = *m.Winner
	}
	if m.GameWinner != nil {
		st.LegWinner = *m.GameWinner
		if *m.GameWinner >= 0 {
			st.LegFinished = true
		}
	}
	// TODO(format): Variantennamen pruefen ("X01" angenommen).
	st.HasScoring = strings.EqualFold(m.Variant, "X01")

	byParticipant := map[string]int{}
	for i, rp := range m.Players {
		ref := parser.PlayerRef{Index: i, Name: strings.TrimSpace(rp.Name), UserID: rp.UserID, IsBot: rp.CPUPPR != nil}
		if rp.Index != nil {
			ref.Index = *rp.Index
		}
		if ref.UserID == "" && rp.User != nil {
			ref.UserID = rp.User.ID
			if ref.Name == "" {
				ref.Name = rp.User.Name
			}
		}
		if rp.ID != "" {
			byParticipant[rp.ID] = ref.Index
		}
		st.Players = append(st.Players, ref)
	}
	n := len(st.Players)

	// Leg-Statistik aus den Turns des aktuellen Legs berechnen.
	legStats := make([]parser.PlayerStats, n)
	legHasTurns := false
	first9 := make([][2]int, n) // points, darts der ersten 9 Darts
	for _, t := range m.Turns {
		idx := -1
		if t.Player != nil {
			idx = *t.Player
		} else if i, ok := byParticipant[t.PlayerID]; ok {
			idx = i
		}
		if idx < 0 || idx >= n {
			continue
		}
		legHasTurns = true
		// Die laufende Runde hat noch keine Wuerfe. Sie darf nicht als volle
		// Aufnahme zaehlen, sonst ist der Average zu niedrig. Nur wenn das
		// Feld ganz fehlt, wird von drei Darts ausgegangen.
		darts := len(t.Throws)
		if t.Throws == nil && (t.Score != 0 || t.Busted) {
			darts = 3
		}
		score := t.Score
		if t.Busted {
			score = 0
		}
		ls := &legStats[idx]
		ls.Darts += darts
		ls.Points += score
		if first9[idx][1] < 9 {
			first9[idx][0] += score
			first9[idx][1] += darts
		}
		switch {
		case score == 180:
			ls.Count180++
			ls.Count140Plus++
			ls.Count100Plus++
		case score >= 140:
			ls.Count140Plus++
			ls.Count100Plus++
		case score >= 100:
			ls.Count100Plus++
		}
	}
	if st.HasScoring && legHasTurns {
		for i := range legStats {
			ls := &legStats[i]
			if ls.Darts > 0 {
				avg := float64(ls.Points) * 3 / float64(ls.Darts)
				ls.Average = &avg
			}
			if first9[i][1] > 0 {
				f9 := float64(first9[i][0]) * 3 / float64(first9[i][1])
				ls.First9Avg = &f9
			}
		}
	}
	// Checkout des Leg-Gewinners = Score des letzten Turns.
	if st.LegWinner >= 0 && st.LegWinner < n && st.HasScoring {
		for i := len(m.Turns) - 1; i >= 0; i-- {
			t := m.Turns[i]
			idx := -1
			if t.Player != nil {
				idx = *t.Player
			} else if j, ok := byParticipant[t.PlayerID]; ok {
				idx = j
			}
			if idx == st.LegWinner {
				legStats[idx].HighestCheckout = t.Score
				legStats[idx].CheckoutsHit = 1
				break
			}
		}
	}

	matchStats := make([]parser.PlayerStats, n)
	hasMatchStats := false
	for i, se := range m.Stats {
		if i >= n {
			break
		}
		mf := se.rawStatsFields
		if se.MatchStats != nil {
			mf = *se.MatchStats
		}
		hasMatchStats = true
		matchStats[i] = fromFields(mf)
		if se.LegStats != nil {
			lf := fromFields(*se.LegStats)
			// Von der API gelieferte Leg-Werte haben Vorrang vor der Berechnung,
			// eigene Zaehlungen (180er, Checkout) bleiben erhalten wenn API nichts liefert.
			if lf.Average != nil {
				legStats[i].Average = lf.Average
			}
			if lf.First9Avg != nil {
				legStats[i].First9Avg = lf.First9Avg
			}
			// Autodarts liefert dartsThrown und score selbst. Diese Werte
			// haben Vorrang vor der eigenen Zaehlung aus den Runden.
			if lf.Darts > 0 {
				legStats[i].Darts = lf.Darts
			}
			if lf.Points > 0 {
				legStats[i].Points = lf.Points
			}
			if lf.CheckoutAttempts > 0 {
				legStats[i].CheckoutAttempts = lf.CheckoutAttempts
				legStats[i].CheckoutsHit = lf.CheckoutsHit
			}
			if !legHasTurns {
				legStats[i].Count180 = lf.Count180
				legStats[i].Count140Plus = lf.Count140Plus
				legStats[i].Count100Plus = lf.Count100Plus
			}
		}
	}
	for i, sc := range m.Scores {
		if i < n {
			matchStats[i].LegsWon = sc.Legs
			hasMatchStats = true
		}
	}
	if hasMatchStats {
		st.MatchStats = matchStats
	}
	st.LegStats = legStats
	return st
}

func fromFields(f rawStatsFields) parser.PlayerStats {
	ps := parser.PlayerStats{Average: f.Average, First9Avg: f.First9Average}
	if f.CheckoutPercent != nil {
		v := *f.CheckoutPercent
		if v > 1 {
			v = v / 100 // TODO(format): Prozent oder Anteil?
		}
		ps.CheckoutRate = &v
	}
	if f.CheckoutsHit != nil {
		ps.CheckoutsHit = *f.CheckoutsHit
	}
	if f.Checkouts != nil {
		ps.CheckoutAttempts = *f.Checkouts
	}
	if f.CheckoutPoints != nil {
		ps.HighestCheckout = *f.CheckoutPoints
	}
	if f.DartsThrown != nil {
		ps.Darts = *f.DartsThrown
	}
	if f.Score != nil {
		ps.Points = *f.Score
	}
	// Disjunkte Klassen zu kumulativen Zaehlern aufaddieren.
	n := func(p *int) int {
		if p == nil {
			return 0
		}
		return *p
	}
	ps.Count180 = n(f.Total180)
	ps.Count140Plus = n(f.Plus140) + n(f.Plus170) + ps.Count180
	ps.Count100Plus = n(f.Plus100) + ps.Count140Plus
	return ps
}
