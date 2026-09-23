# Projekt: Your Darts (Autodarts Stats Tracker)

Dashboard heißt „Your Darts“, Icon = Symbol aus dem your-admins-Logo (`web/static/favicon.svg`, Extension-Icons daraus gerendert).

Siehe README.md für Aufbau, Befehle und API. Hier nur, was fürs Weiterarbeiten wichtig ist.

## Architektur
- `extension/` – MV3-Extension, reiner Collector (hook.js MAIN world → relay.js → bg.js mit Retry-Queue).
- `server/` – Go 1.22+ stdlib `net/http`, SQLite via `modernc.org/sqlite`, Migrationen unter `internal/db/migrations/`.
  Packages: `names` (Normalisierung), `parser` (Interface + `autodarts/`-Implementierung), `ingest`, `stats`, `api`, `web` (embed).
- `web/` – SvelteKit 2 / Svelte 5 (Runes), adapter-static, Build landet in `server/internal/web/dist/`.

## Entscheidungen
- Laufende Erfassung: Extension sendet jeden Match-Stand. Backend hält den letzten Stand pro Match und archiviert beim
  Leg-Wechsel/Matchende den vorigen Stand als Leg-Endstand (`match_snapshots`) → `match_legs`.
- Wurfstatistiken (Average, 180er, Checkouts) im Leaderboard kommen aus `match_legs` (auch abgebrochene Matches),
  Siege/Matches nur aus `finished` Matches. Average ist dartgewichtet: sum(points)*3/sum(darts).
- Alle Varianten zählen; nur X01 hat Average/Points (`HasScoring`), andere nur Siege/Legs/Darts.
- Mehrere Boards teilen sich die Spieler (Identität = normalisierter Name bzw. Autodarts-User-ID).
- Leaderboard-Standard: min. 5 Matches, im Frontend einstellbar.
- Betrieb im Internet: nur hinter TLS-Reverse-Proxy. Extension nutzt `optional_host_permissions`.
- Frontend auf Deutsch.
- Missbrauchsschutz: `players.protected`. Geschützte Namen zählen nur mit Check-in-Code im Gastnamen (`Tim#4831`,
  `match_overrides` source=checkin) oder passender Autodarts-User-ID; sonst `match_pending` (Freigabe-Queue im Admin).
  Chips (`player_chips`, nur Hash der UID) werden vom Agent (`cmd/autodarts-stats-agent`, Reader-Backends in
  `internal/reader`) am Board eingestempelt → `checkins` mit 4-stelligem Code, TTL `CHECKIN_TTL`.
  Zuordnung ist zum Spielzeitpunkt gültig, damit `reprocess` stabil bleibt; Overrides überleben reprocess.

## Autodarts-Adressen (geprueft am 23.09.2026 im ausgelieferten JS der Live-App)
- Seite: `play.autodarts.com` (und `play.autodarts.io`, gleiche App, gleicher Bundle-Hash)
- REST: `https://api.autodarts.com/{gs,as,bs,us,ds,auth,status}/v0/...`,
  Matchzustand `gs/v0/matches/{id}`, Statistik `as/v0/matches/{id}/stats`
- WebSocket: `wss://play.ws.autodarts.com/ms/v0`, Boards `wss://boards.ws.autodarts.com`
- Kanaele: `autodarts.matches`, `autodarts.boards`, `autodarts.lobbies`, `autodarts.tournaments`, ...
- Bestaetigte Feldnamen: turns, throws, segment, gameWinner, gameScores, matchStats, legStats,
  average, first9Average, checkoutPercent, checkoutsHit, dartsThrown, plus100, plus140, total180, legsWon, cpuPPR, userId
- **Wichtig:** Die Extension muss auf `.com` hoeren. Eine Version, die nur `autodarts.io` gefiltert hat,
  sammelt nichts.
- WebSocket-Protokoll: Client sendet `{type:"subscribe", channel, topic}`, Server antwortet mit
  `{type, channel, topic, data}`. Topics: `<matchId>.state`, `<matchId>.events`, ...
  Beim `.state`-Topic steckt die Match-ID **nur im Topic**, nicht im `data`.
- `set` und `leg` zaehlen **ab 1** (gegen echte Antwort geprueft). Der Wert wird unveraendert uebernommen.
- Stats-Klassen `less60/plus60/plus100/plus140/plus170/total180` sind **disjunkt**, nicht kumulativ.
  `score` und `dartsThrown` aus den Stats haben Vorrang vor eigener Zaehlung aus `turns`.
- `gameFinished` = Leg zu Ende, `finished` = Match zu Ende. Der Ingest archiviert das Leg bei
  `gameFinished`, nicht erst beim Weiterspringen des Leg-Zaehlers.
- Nach dem Laden der Seite kommen fast alle Aktualisierungen per WebSocket, nicht per REST.

## TODO(format)
Das echte Autodarts-JSON ist noch nicht verifiziert. Annahmen stehen kommentiert in
`server/internal/parser/autodarts/autodarts.go`. Echte Responses → `testdata/*.json`, dann Tests und Parser anpassen,
anschließend `autodarts-stats reprocess`. Unerkannte Payloads sind im Admin unter „Unerkannte Events“ einsehbar.

## Tests
`cd server && go test ./...` – Normalisierung, Parser (gegen `testdata/`), Ingest-Idempotenz/Leg-Fluss/Reprocess, HTTP-API inkl. Admin.
`cd web && npm run check` – svelte-check.
