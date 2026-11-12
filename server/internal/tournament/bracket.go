// Package tournament verwaltet K.-o.-Turniere lokaler Spieler.
//
// Gespeichert werden nur Eingaben: Teilnehmer mit Auslosung, Einstellungen und
// Ergebnisse pro Paarung. Der Baum selbst wird bei jedem Zugriff daraus
// berechnet (layout + evaluation). Korrigiert der Admin ein Ergebnis weiter
// vorne, verfallen dadurch spaetere Ergebnisse, deren Paarung nicht mehr passt.
//
// Lucky-Loser-Runde: Wer vor der Einstiegsrunde E im Hauptbaum verliert, spielt
// in einer eigenen K.-o.-Runde weiter. Die Verlierer spaeterer Hauptrunden
// steigen dort nach und nach ein (wie in der Verliererrunde eines Doppel-K.-o.).
// Der Sieger spielt vor Runde E ein Zusatzspiel gegen einen zugelosten
// Qualifikanten; wer gewinnt, nimmt dessen Platz in Runde E ein.
package tournament

import (
	"fmt"
	"sort"
	"time"
)

const (
	BracketMain   = "main"
	BracketLL     = "ll"
	BracketPlayin = "playin"
	BracketThird  = "third"
)

// Rule sind die Spielregeln einer Runde. FirstTo = benoetigte Legs zum Sieg.
type Rule struct {
	Variant   string `json:"variant"`
	BaseScore int    `json:"base_score"`
	FirstTo   int    `json:"first_to"`
}

var defaultRule = Rule{Variant: "X01", BaseScore: 501, FirstTo: 2}

func (r Rule) withDefaults() Rule {
	if r.Variant == "" {
		r.Variant = defaultRule.Variant
	}
	if r.BaseScore <= 0 && r.Variant == "X01" {
		r.BaseScore = defaultRule.BaseScore
	}
	if r.FirstTo <= 0 {
		r.FirstTo = defaultRule.FirstTo
	}
	return r
}

// Rules: Rounds[0] gilt fuers Finale, [1] fuers Halbfinale usw. Fehlt eine
// fruehe Runde, gilt die fruehste angegebene. Das Spiel um Platz 3 spielt nach
// den Halbfinal-Regeln, das Zusatzspiel nach denen der Runde vor dem Einstieg.
type Rules struct {
	Rounds     []Rule `json:"rounds"`
	LuckyLoser Rule   `json:"lucky_loser"`
}

func (r Rules) forDistance(d int) Rule {
	if len(r.Rounds) == 0 {
		return defaultRule
	}
	if d >= len(r.Rounds) {
		d = len(r.Rounds) - 1
	}
	return r.Rounds[d].withDefaults()
}

// Config ist alles, was den Baum eines gestarteten Turniers festlegt.
type Config struct {
	Order []int64 // Spieler in ausgeloster Reihenfolge
	// Late: Nachmelder. Spiel in Runde 1 (Index) -> Spieler, der dort den
	// Freilos-Platz einnimmt. Die Freilos-Verteilung haengt nur an len(Order).
	Late        map[int]int64
	ThirdPlace  bool
	LuckyLoser  bool
	LLEntry     int // Einstiegsrunde als Abstand zum Finale
	PlayinMatch int // Spiel in der Einstiegsrunde, dessen Platz ausgelost wurde
	PlayinSide  int // 0 = oben, 1 = unten
	Rules       Rules
}

// RoundCount liefert die Zahl der Hauptrunden fuer n Spieler.
func RoundCount(n int) int {
	r, s := 0, 1
	for s < n {
		s *= 2
		r++
	}
	return r
}

// EntryRound liefert die Hauptrunde (1-basiert), vor der der Lucky Loser
// einsteigt, oder 0, wenn das bei dieser Rundenzahl nicht geht. Frueheste
// Einstiegsrunde ist Runde 2, sonst gaebe es keine Verlierer.
func EntryRound(rounds, distance int) int {
	e := rounds - distance
	if distance < 0 || e < 2 {
		return 0
	}
	return e
}

// RoundName benennt Hauptrunde r (1-basiert) bei insgesamt rounds Runden.
func RoundName(rounds, r int) string {
	switch rounds - r {
	case 0:
		return "Finale"
	case 1:
		return "Halbfinale"
	case 2:
		return "Viertelfinale"
	case 3:
		return "Achtelfinale"
	}
	return fmt.Sprintf("Runde %d", r)
}

type srcKind int

const (
	srcBye srcKind = iota
	srcPlayer
	srcWinner
	srcLoser
)

type source struct {
	kind   srcKind
	player int64
	seat   int // nur srcPlayer: Platz im Baum (Spiel i in Runde 1: 2i oben, 2i+1 unten)
	key    string
}

func winnerOf(key string) source { return source{kind: srcWinner, key: key} }
func loserOf(key string) source  { return source{kind: srcLoser, key: key} }

type node struct {
	key     string
	bracket string
	round   int // 1-basiert innerhalb der Gruppe
	index   int
	no      int // laufende Spielnummer fuer die Anzeige
	a, b    source
	rule    Rule
}

type layout struct {
	size, rounds int
	entry        int // Einstiegsrunde des Lucky Losers, 0 = ohne
	main         [][]*node
	ll           [][]*node
	playin       *node
	third        *node
	nodes        map[string]*node
	all          []*node
	seatPlayer   map[int]int64 // aktueller Spieler je Platz
	seatOf       map[int64]int
	byeMatch     map[int]bool // Spiele in Runde 1 mit Freilos (laut Auslosung)
}

func mkey(prefix string, round, index int) string {
	return fmt.Sprintf("%s%d-%d", prefix, round, index)
}

func buildLayout(cfg Config) (*layout, error) {
	n := len(cfg.Order)
	if n < 2 {
		return nil, fmt.Errorf("mindestens 2 Spieler noetig")
	}
	R := RoundCount(n)
	S := 1 << R
	L := &layout{size: S, rounds: R, nodes: map[string]*node{}, main: make([][]*node, R),
		seatPlayer: map[int]int64{}, seatOf: map[int64]int{}, byeMatch: map[int]bool{}}

	// Runde 1: Freilose gleichmaessig ueber den Baum verteilen. Da es weniger
	// Freilose als Spiele gibt, trifft nie Freilos auf Freilos.
	half := S / 2
	byes := S - n
	isBye := make([]bool, half)
	for j := 0; j < byes; j++ {
		isBye[j*half/byes] = true
	}
	for i := range cfg.Late {
		if i < 0 || i >= half || !isBye[i] {
			return nil, fmt.Errorf("Nachmelder auf Spiel %d: dort gibt es kein Freilos", i+1)
		}
	}
	seat := func(s int, pid int64) source {
		L.seatPlayer[s] = pid
		L.seatOf[pid] = s
		return source{kind: srcPlayer, player: pid, seat: s}
	}
	p := 0
	for i := 0; i < half; i++ {
		nd := &node{key: mkey("M", 1, i), bracket: BracketMain, round: 1, index: i}
		nd.a = seat(2*i, cfg.Order[p])
		p++
		if isBye[i] {
			L.byeMatch[i] = true
			nd.b = source{kind: srcBye}
			if pid := cfg.Late[i]; pid != 0 {
				nd.b = seat(2*i+1, pid)
			}
		} else {
			nd.b = seat(2*i+1, cfg.Order[p])
			p++
		}
		L.main[0] = append(L.main[0], nd)
	}
	for r := 2; r <= R; r++ {
		for i := 0; i < S>>r; i++ {
			L.main[r-1] = append(L.main[r-1], &node{key: mkey("M", r, i), bracket: BracketMain, round: r, index: i,
				a: winnerOf(mkey("M", r-1, 2*i)), b: winnerOf(mkey("M", r-1, 2*i+1))})
		}
	}
	for r, round := range L.main {
		rule := cfg.Rules.forDistance(R - 1 - r)
		for _, nd := range round {
			nd.rule = rule
		}
	}

	if cfg.LuckyLoser {
		E := EntryRound(R, cfg.LLEntry)
		if E == 0 {
			return nil, fmt.Errorf("Lucky-Loser-Einstieg passt nicht zu %d Spielern", n)
		}
		if E == R && cfg.ThirdPlace {
			return nil, fmt.Errorf("Spiel um Platz 3 geht nicht, wenn der Lucky Loser erst im Finale einsteigt: die Halbfinal-Verlierer spielen dann die Lucky-Loser-Runde")
		}
		L.entry = E
		llRule := cfg.Rules.LuckyLoser.withDefaults()
		var prev []string
		addRound := func(pairs [][2]source) {
			r := len(L.ll) + 1
			var round []*node
			prev = prev[:0:0]
			for i, pr := range pairs {
				nd := &node{key: mkey("L", r, i), bracket: BracketLL, round: r, index: i, a: pr[0], b: pr[1], rule: llRule}
				round = append(round, nd)
				prev = append(prev, nd.key)
			}
			L.ll = append(L.ll, round)
		}
		reduce := func() {
			var pairs [][2]source
			for i := 0; i+1 < len(prev); i += 2 {
				pairs = append(pairs, [2]source{winnerOf(prev[i]), winnerOf(prev[i+1])})
			}
			addRound(pairs)
		}
		// Verlierer der ersten Runde spielen untereinander.
		var pairs [][2]source
		for i := 0; i < half/2; i++ {
			pairs = append(pairs, [2]source{loserOf(mkey("M", 1, 2*i)), loserOf(mkey("M", 1, 2*i+1))})
		}
		addRound(pairs)
		// Verlierer der Runden 2..E-1 steigen ein. Gespiegelte Zuordnung,
		// damit sich zwei Spieler nicht gleich wieder begegnen.
		for k := 2; k <= E-1; k++ {
			if k > 2 {
				reduce()
			}
			m := len(L.main[k-1])
			pairs = nil
			for i := range prev {
				pairs = append(pairs, [2]source{winnerOf(prev[i]), loserOf(mkey("M", k, m-1-i))})
			}
			addRound(pairs)
		}
		for len(prev) > 1 {
			reduce()
		}
		champion := prev[0]

		target := L.main[E-1]
		mi := cfg.PlayinMatch
		if mi < 0 || mi >= len(target) {
			mi = 0
		}
		tn := target[mi]
		L.playin = &node{key: "P", bracket: BracketPlayin, round: 1, a: winnerOf(champion), rule: cfg.Rules.forDistance(R - E + 1)}
		if cfg.PlayinSide == 1 {
			L.playin.b, tn.b = tn.b, winnerOf("P")
		} else {
			L.playin.b, tn.a = tn.a, winnerOf("P")
		}
	}

	if cfg.ThirdPlace && R >= 2 {
		L.third = &node{key: "T", bracket: BracketThird, round: 1,
			a: loserOf(mkey("M", R-1, 0)), b: loserOf(mkey("M", R-1, 1)), rule: cfg.Rules.forDistance(1)}
	}

	for _, round := range L.main {
		L.all = append(L.all, round...)
	}
	for _, round := range L.ll {
		L.all = append(L.all, round...)
	}
	if L.playin != nil {
		L.all = append(L.all, L.playin)
	}
	if L.third != nil {
		L.all = append(L.all, L.third)
	}
	for i, nd := range L.all {
		nd.no = i + 1
		L.nodes[nd.key] = nd
	}
	return L, nil
}

// ---- Auswertung ----

type slotState int

const (
	slotPending slotState = iota // steht noch nicht fest
	slotKnown
	slotNone // bleibt leer (Freilos)
)

// slot ist eine Seite einer Paarung. seat ist der Platz im Baum, ueber den der
// Spieler hineingekommen ist; player der Spieler, der dort spielt bzw. spielte.
type slot struct {
	state  slotState
	player int64
	seat   int
}

// Result ist ein gespeichertes Ergebnis einer Paarung.
type Result struct {
	Key       string
	Player1   int64
	Player2   int64
	Winner    int64
	Legs1     int
	Legs2     int
	MatchID   int64
	Source    string
	Warning   string
	Outcome   string // "" = regulaer, sonst OutcomeRetired / OutcomeDQ
	DecidedAt time.Time
}

// Ausgang einer Paarung, die nicht regulaer zu Ende gespielt wurde.
const (
	OutcomeRetired   = "retired"   // Aufgabe des Verlierers
	OutcomeDQ        = "dq"        // Disqualifikation des Verlierers
	OutcomeWithdrawn = "withdrawn" // kampflos nach Rueckzug (berechnet, nie gespeichert)
)

// Status einer Paarung.
const (
	StatusWait     = "wait"     // Spieler stehen noch nicht fest
	StatusReady    = "ready"    // kann gespielt werden
	StatusDone     = "done"     // Ergebnis liegt vor
	StatusWalkover = "walkover" // Freilos, Spieler kommt kampflos weiter
	StatusEmpty    = "empty"    // bleibt leer
	StatusForfeit  = "forfeit"  // kampflos entschieden: ein Spieler hat sich zurueckgezogen
)

type withdrawal struct {
	At     time.Time
	Reason string
}

// roster sind die Teilnehmeraenderungen nach dem Start: fruehere Spieler je
// Platz (Ersatzspieler) und Rueckzuege. Gespielte Ergebnisse eines frueheren
// Spielers zaehlen fuer seinen Platz weiter; offene Paarungen spielt der
// aktuelle Spieler.
type roster struct {
	former    map[int][]int64
	withdrawn map[int64]withdrawal
}

type evalMatch struct {
	node      *node
	a, b      slot
	status    string
	winner    slot
	loser     slot
	result    *Result
	readyAt   time.Time
	decidedAt time.Time
}

type evaluation struct {
	L       *layout
	results map[string]*Result
	started time.Time
	ro      *roster
	memo    map[string]*evalMatch
}

func evaluate(L *layout, results map[string]*Result, started time.Time) *evaluation {
	return evaluateWith(L, results, started, nil)
}

func evaluateWith(L *layout, results map[string]*Result, started time.Time, ro *roster) *evaluation {
	if ro == nil {
		ro = &roster{}
	}
	ev := &evaluation{L: L, results: results, started: started, ro: ro, memo: map[string]*evalMatch{}}
	for _, nd := range L.all {
		ev.match(nd.key)
	}
	return ev
}

func (ev *evaluation) current(seat int) slot {
	return slot{state: slotKnown, seat: seat, player: ev.L.seatPlayer[seat]}
}

// inSeat: pid spielt bzw. spielte auf diesem Platz.
func (ev *evaluation) inSeat(pid int64, seat int) bool {
	if ev.L.seatPlayer[seat] == pid {
		return true
	}
	for _, p := range ev.ro.former[seat] {
		if p == pid {
			return true
		}
	}
	return false
}

func (ev *evaluation) withdrawn(s slot) (withdrawal, bool) {
	if s.state != slotKnown {
		return withdrawal{}, false
	}
	w, ok := ev.ro.withdrawn[s.player]
	return w, ok
}

// fit prueft, ob ein Ergebnis zur Paarung der Plaetze sa/sb gehoert, und
// liefert die Spieler in der Reihenfolge der Paarung.
func (ev *evaluation) fit(r *Result, sa, sb int) (pa, pb int64, ok bool) {
	if r == nil || (r.Winner != r.Player1 && r.Winner != r.Player2) {
		return 0, 0, false
	}
	switch {
	case ev.inSeat(r.Player1, sa) && ev.inSeat(r.Player2, sb):
		return r.Player1, r.Player2, true
	case ev.inSeat(r.Player2, sa) && ev.inSeat(r.Player1, sb):
		return r.Player2, r.Player1, true
	}
	return 0, 0, false
}

func (ev *evaluation) resolve(s source) (slot, time.Time) {
	switch s.kind {
	case srcPlayer:
		return ev.current(s.seat), time.Time{}
	case srcWinner:
		m := ev.match(s.key)
		return m.winner, m.decidedAt
	case srcLoser:
		m := ev.match(s.key)
		return m.loser, m.decidedAt
	}
	return slot{state: slotNone}, time.Time{}
}

func later(ts ...time.Time) time.Time {
	var out time.Time
	for _, t := range ts {
		if t.After(out) {
			out = t
		}
	}
	return out
}

func (ev *evaluation) match(key string) *evalMatch {
	if m, ok := ev.memo[key]; ok {
		return m
	}
	nd := ev.L.nodes[key]
	m := &evalMatch{node: nd}
	var ta, tb time.Time
	m.a, ta = ev.resolve(nd.a)
	m.b, tb = ev.resolve(nd.b)
	m.readyAt = later(ev.started, ta, tb)
	pending := slot{state: slotPending}
	none := slot{state: slotNone}
	switch {
	case m.a.state == slotNone && m.b.state == slotNone:
		m.status, m.winner, m.loser, m.decidedAt = StatusEmpty, none, none, m.readyAt
	case m.a.state == slotNone || m.b.state == slotNone:
		other := m.a
		if other.state == slotNone {
			other = m.b
		}
		m.loser = none
		if w, ok := ev.withdrawn(other); ok {
			// Zurueckgezogen und Freilos: niemand kommt weiter.
			m.status, m.winner, m.decidedAt = StatusEmpty, none, later(m.readyAt, w.At)
		} else if other.state == slotKnown {
			m.status, m.winner, m.decidedAt = StatusWalkover, other, m.readyAt
		} else {
			m.status, m.winner = StatusWait, pending
		}
	case m.a.state == slotPending || m.b.state == slotPending:
		m.status, m.winner, m.loser = StatusWait, pending, pending
	default:
		r := ev.results[key]
		wa, isA := ev.withdrawn(m.a)
		wb, isB := ev.withdrawn(m.b)
		if pa, pb, ok := ev.fit(r, m.a.seat, m.b.seat); ok {
			// Gespielt: das Ergebnis gilt, auch wenn auf dem Platz inzwischen
			// ein Ersatzspieler steht oder der Spieler zurueckgezogen hat.
			m.status, m.result, m.decidedAt = StatusDone, r, r.DecidedAt
			m.a.player, m.b.player = pa, pb
			if r.Winner == pa {
				m.winner, m.loser = ev.current(m.a.seat), ev.current(m.b.seat)
			} else {
				m.winner, m.loser = ev.current(m.b.seat), ev.current(m.a.seat)
			}
		} else if isA || isB {
			// Kampflos: wer zurueckgezogen hat, verliert die Paarung.
			m.status, m.decidedAt = StatusForfeit, m.readyAt
			if isA {
				m.decidedAt = later(m.decidedAt, wa.At)
			}
			if isB {
				m.decidedAt = later(m.decidedAt, wb.At)
			}
			switch {
			case isA && isB:
				m.winner, m.loser = none, none
			case isA:
				m.winner, m.loser = m.b, m.a
			default:
				m.winner, m.loser = m.a, m.b
			}
		} else {
			m.status, m.winner, m.loser = StatusReady, pending, pending
		}
	}
	ev.memo[key] = m
	return m
}

// ready liefert die spielbereiten Paarungen in Baumreihenfolge.
func (ev *evaluation) ready() []*evalMatch {
	var out []*evalMatch
	for _, nd := range ev.L.all {
		if m := ev.memo[nd.key]; m.status == StatusReady {
			out = append(out, m)
		}
	}
	return out
}

// usedResults sind die Schluessel aller Ergebnisse, die zur aktuellen
// Paarung passen. Alle anderen sind verfallen.
func (ev *evaluation) usedResults() map[string]bool {
	out := map[string]bool{}
	for k, m := range ev.memo {
		if m.result != nil {
			out[k] = true
		}
	}
	return out
}

func (ev *evaluation) final() *evalMatch {
	return ev.memo[mkey("M", ev.L.rounds, 0)]
}

// complete: Sieger steht fest (oder bleibt aus, weil beide Finalisten
// zurueckgezogen haben) und ggf. auch Platz 3.
func (ev *evaluation) complete() bool {
	if ev.final().winner.state == slotPending {
		return false
	}
	if ev.L.third != nil {
		t := ev.memo["T"]
		return t.winner.state != slotPending
	}
	return true
}

// Placement ist das Abschneiden eines Spielers. Place 0 = noch im Turnier.
type Placement struct {
	Place      int    `json:"place"`
	Label      string `json:"label"`
	LuckyLoser bool   `json:"lucky_loser"` // hat in der Lucky-Loser-Runde gespielt
	Out        bool   `json:"out"`
	// Withdrawn: zurueckgezogen (OutcomeRetired oder OutcomeDQ).
	Withdrawn string `json:"withdrawn,omitempty"`
	// Replaced: durch einen Ersatzspieler ersetzt.
	Replaced bool `json:"replaced,omitempty"`
}

// seatPlacements berechnet das Abschneiden je Platz. Massgeblich ist die
// spaeteste Niederlage im Hauptbaum bzw. im Zusatzspiel. Verlierer der Runde k
// teilen sich Platz 2^(R-k)+1.
func (ev *evaluation) seatPlacements() map[int]Placement {
	L := ev.L
	out := map[int]Placement{}
	stage := map[int]int{} // spaeteste Niederlage, doppelt gezaehlt (Zusatzspiel = 2E-1)
	terminal := map[int]bool{}
	inLL := map[int]bool{}
	lose := func(seat int, st int, final bool) {
		if st > stage[seat] {
			stage[seat] = st
		}
		if final {
			terminal[seat] = true
		}
	}
	for _, nd := range L.all {
		m := ev.memo[nd.key]
		if nd.bracket == BracketLL {
			for _, s := range []slot{m.a, m.b} {
				if s.state == slotKnown {
					inLL[s.seat] = true
				}
			}
		}
		var losers []int
		switch m.status {
		case StatusDone:
			losers = append(losers, m.loser.seat)
		case StatusForfeit:
			for _, s := range []slot{m.a, m.b} {
				if _, ok := ev.withdrawn(s); ok {
					losers = append(losers, s.seat)
				}
			}
		}
		for _, lo := range losers {
			switch nd.bracket {
			case BracketMain:
				// Mit Lucky Loser geht es vor der Einstiegsrunde weiter, mit Platz 3 nach dem Halbfinale.
				final := !(L.entry > 0 && nd.round < L.entry) && !(L.third != nil && nd.round == L.rounds-1)
				lose(lo, 2*nd.round, final)
			case BracketLL:
				terminal[lo] = true
			case BracketPlayin:
				lose(lo, 2*L.entry-1, true)
			case BracketThird:
				terminal[lo] = true
			}
		}
	}
	champ := ev.final().winner
	var tm *evalMatch
	if L.third != nil {
		tm = ev.memo["T"]
		if tm.status != StatusDone && tm.status != StatusForfeit {
			tm = nil
		}
	}
	for seat := range L.seatPlayer {
		pl := Placement{LuckyLoser: inLL[seat]}
		st := stage[seat]
		switch {
		case champ.state == slotKnown && champ.seat == seat:
			pl.Place, pl.Label, pl.Out = 1, "Sieger", true
		case tm != nil && tm.winner.state == slotKnown && tm.winner.seat == seat:
			pl.Place, pl.Label, pl.Out = 3, "Platz 3", true
		case tm != nil && tm.loser.state == slotKnown && tm.loser.seat == seat:
			pl.Place, pl.Label, pl.Out = 4, "Platz 4", true
		case terminal[seat] || (st > 0 && ev.complete()):
			pl.Out = true
			if st%2 == 1 { // Zusatzspiel verloren: zaehlt wie Aus in der Runde davor
				pl.Place = L.size>>(L.entry-1) + 1
				pl.Label = "Zusatzspiel"
			} else if st > 0 {
				k := st / 2
				pl.Place = L.size>>k + 1
				pl.Label = RoundName(L.rounds, k)
			}
		default:
			pl.Label = "im Turnier"
		}
		out[seat] = pl
	}
	return out
}

// WithdrawnLabel benennt einen Rueckzug.
func WithdrawnLabel(reason string) string {
	if reason == OutcomeDQ {
		return "disqualifiziert"
	}
	return "zurückgezogen"
}

// placements liefert das Abschneiden je Spieler. Ein ersetzter Spieler ist
// ausgeschieden ohne Platz, ein zurueckgezogener behaelt den Platz seiner
// (kampflosen) Niederlage.
func (ev *evaluation) placements(players []int64) map[int64]Placement {
	sp := ev.seatPlacements()
	out := map[int64]Placement{}
	for _, pid := range players {
		if seat, ok := ev.L.seatOf[pid]; ok {
			pl := sp[seat]
			if w, ok := ev.ro.withdrawn[pid]; ok && pl.Place != 1 {
				pl.Withdrawn, pl.Out = w.Reason, true
				pl.Label = WithdrawnLabel(w.Reason)
				if pl.Place > 0 {
					pl.Label += " (" + sp[seat].Label + ")"
				}
			}
			out[pid] = pl
			continue
		}
		pl := Placement{Label: "im Turnier"}
		for _, list := range ev.ro.former {
			for _, p := range list {
				if p == pid {
					pl = Placement{Label: "ersetzt", Out: true, Replaced: true}
				}
			}
		}
		out[pid] = pl
	}
	return out
}

// ranking sortiert Spieler nach Platzierung: noch dabei zuerst, dann nach
// Platz, zuletzt Ausgeschiedene ohne Platz (ersetzt, zurueckgezogen).
func ranking(players []int64, pl map[int64]Placement) []int64 {
	rank := func(p Placement) int {
		switch {
		case p.Place > 0:
			return p.Place
		case !p.Out:
			return 0
		}
		return 1 << 30
	}
	out := append([]int64(nil), players...)
	sort.SliceStable(out, func(i, j int) bool { return rank(pl[out[i]]) < rank(pl[out[j]]) })
	return out
}
