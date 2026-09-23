# Your Darts

Dashboard für die Darts-Statistik der Firma (Autodarts-Boards). Icon und Name nach [your-admins.de](https://www.your-admins.de).

Erfasst Statistiken lokaler Autodarts-Spieler (Gastnamen ohne Account) über viele Matches hinweg.
Ein Go-Binary (Backend + eingebettetes SvelteKit-Frontend) plus eine Browser-Extension als Collector.

```
extension/   Chrome/Firefox-Extension (MV3), reicht Rohdaten weiter
server/      Go-Backend: Ingest, Verarbeitung, API, Frontend-Auslieferung; cmd/autodarts-stats-agent = Chip-Agent
web/         SvelteKit-Frontend (adapter-static), wird ins Binary eingebettet
testdata/    Beispiel-Responses von Autodarts (echte bitte hier ablegen!)
deploy/      systemd-Unit, Caddyfile, docker-compose
docs/        Checklisten und Notizen (docs/mvp-test-checkliste.md)
```

Für einen Rechner ohne Go/Node: `make package` erzeugt `your-darts-paket.zip` mit fertigen
Linux-Binaries (amd64 + arm64), Extension und Doku. Anleitung darin: `START-HIER.md`.

## Schnellstart

```sh
make build                    # Frontend + Binary  -> ./autodarts-stats
ADMIN_PASSWORD=geheim ./autodarts-stats board add "Wohnzimmer"   # API-Key notieren!
ADMIN_PASSWORD=geheim ./autodarts-stats                          # Server auf :8080
```

Dann `make extension-zip` bzw. den Ordner `extension/` als entpackte Extension laden
(Chrome: `chrome://extensions` → Entwicklermodus → „Entpackte Erweiterung laden“;
Firefox ab 140: `about:debugging` → „Temporäres Add-on laden“ → `manifest.json`, überlebt aber
keinen Neustart von Firefox; dauerhaft siehe `docs/mvp-test-checkliste.md`).
In den Extension-Optionen Backend-URL, API-Key und Board-Name eintragen, „Verbindung testen“.

## Konfiguration (Env)

| Variable         | Standard              | Bedeutung |
|------------------|-----------------------|-----------|
| `PORT`           | `8080`                | HTTP-Port |
| `DB_PATH`        | `autodarts-stats.db`  | SQLite-Datei (WAL) |
| `ADMIN_PASSWORD` | leer = Admin aus      | Passwort für den Admin-Bereich |
| `SESSION_SECRET` | zufällig pro Start    | HMAC-Secret für Admin-Cookies; setzen, damit Logins Neustarts überleben |
| `TRUST_PROXY`    | `0`                   | `1`, wenn hinter Reverse-Proxy (X-Forwarded-For fürs Login-Rate-Limit) |
| `MAX_UNPARSED`   | `200`                 | Wie viele unerkannte Payloads zum Debuggen aufbewahrt werden |
| `CHECKIN_TTL`    | `4h`                  | Gültigkeit eines Chip-Check-ins |

## Befehle

```
autodarts-stats [serve]          Server
autodarts-stats reprocess        Aggregate aus Rohdaten neu berechnen (nach Parser-Änderungen)
autodarts-stats reprocess-unparsed  Unerkannte Events erneut durch den Parser schicken (auch im Admin)
autodarts-stats board add NAME   Board + API-Key anlegen
autodarts-stats board list
autodarts-stats board revoke NAME
```

## Wie die Erfassung funktioniert

1. `hook.js` (MAIN world) wrappt `fetch`/`XMLHttpRequest`/`WebSocket` auf play.autodarts.com
   (und play.autodarts.io) und reicht JSON-Antworten von `api.autodarts.com` sowie Nachrichten
   von `wss://play.ws.autodarts.com` per `postMessage` weiter.
2. `relay.js` filtert nach konfigurierbaren URL-/Kanal-Mustern und schickt an den Service Worker.
3. `bg.js` puffert in einer persistenten Queue (max. 500), sendet Batches an `POST /api/ingest`
   und wiederholt bei Fehlern per Alarm.
4. Das Backend hält pro Match den **letzten Stand** (`matches.raw_json`). Wechselt das Leg oder endet
   das Match, wird der vorige Stand als **Leg-Endstand** archiviert (`match_snapshots`) und daraus
   `match_legs` berechnet. So zählen Legs auch aus abgebrochenen Matches. `match_players` wird laufend
   fortgeschrieben; Siege/Matches zählen erst bei `finished`.
5. Was kein Parser erkennt, landet in `unparsed_events` (Admin → „Unerkannte Events“).

Spieleridentität: Bots werden ignoriert. Autodarts-Accounts über ihre User-ID, Gäste über den
normalisierten Namen (trim, lowercase, Umlaute → ae/oe/ue/ss, Diakritika entfernt). Kein Fuzzy-Matching;
Zusammenführen/Aliase über den Admin-Bereich.

## Chip-Check-in: Namen vor Missbrauch schützen

Wer am Board einen Gastnamen eintippt, ist für Autodarts einfach dieser Spieler. Damit niemand
unter deinem Namen deine Statistik verhunzt, gibt es **geschützte Spieler**:

1. Im Admin einen Spieler „schützen“ (oder beim Zuordnen eines Chips neu anlegen, dann ist er automatisch geschützt).
2. Am Board-Client läuft `autodarts-stats-agent` mit einem NFC-Leser. Chip vorhalten → der Server
   vergibt einen Session-Code, z.B. **`Tim#4831`** (Standard 4 h gültig, `CHECKIN_TTL`).
3. Der Name mit Code wird in Autodarts als Gastname eingetragen. Die Extension blendet die aktiven
   Check-ins mit „Kopieren“-Button auf play.autodarts.com ein; der Agent kann ihn optional auch
   direkt tippen (`TYPE_CMD`).
4. Matches mit gültigem Code zählen sofort. Matches unter einem geschützten Namen **ohne** Code,
   mit falschem Code oder von einem fremden Autodarts-Account mit gleichem Namen landen in der
   **Freigabe-Queue** (Admin) und zählen erst nach Zuordnung. Dort kann man auch verwerfen oder
   einem anderen Spieler zuordnen. Bestehende Matches lassen sich ebenso nachträglich umhängen.
5. Unbekannte Chips erscheinen im Admin unter „Chips“ und werden dort einem Spieler zugeordnet
   (oder ein neuer Spieler wird angelegt). Gespeichert wird nur ein Hash der Chip-Seriennummer.

Ungeschützte Spieler verhalten sich wie bisher (Name = Identität).

### Agent einrichten (Linux-Client am Board)

```sh
make build-agent                # Tastatur-Leser (evdev) oder stdin
make build-agent TAGS=pcsc      # zusätzlich PC/SC-Leser wie ACR122U (braucht libpcsclite-dev, pcscd)
BACKEND_URL=https://darts.example.org API_KEY=adb_... READER=stdin ./autodarts-stats-agent   # Test: UID eintippen
```

* **Tastatur-Emulations-Leser** (günstige USB-RFID-Leser, tippen die UID + Enter):
  `READER=evdev:/dev/input/by-id/usb-…-event-kbd`. Der Agent greift das Gerät exklusiv, damit die UID
  nicht im Browser landet. Rechte: `deploy/99-autodarts-nfc.rules` oder Nutzer in Gruppe `input`.
* **PC/SC-Leser** (ACR122U u.a.): `pcscd` installieren, `READER=pcsc`.
* Benachrichtigung per `notify-send`, Dienst als User-Unit: `deploy/autodarts-stats-agent.service`.
* Ohne Leser geht es auch: Admin → Check-ins → „Manuell einchecken“.

Der Code ist am Board für Anwesende sichtbar. Wer ihn abschreibt, kann ihn bis zum Ablauf (Standard 4 h)
am selben Board verwenden. Für Kollegen-Statistiken reicht das; gegen gezielte Manipulation hilft die
Freigabe-Queue plus nachträgliches Umhängen.

## Adressen der Autodarts-App

Geprüft am 23.09.2026 im ausgelieferten JavaScript der Live-App:

| Zweck | Adresse |
|---|---|
| Seite | `play.autodarts.com`, `play.autodarts.io` (gleiche App) |
| Matchzustand | `https://api.autodarts.com/gs/v0/matches/{id}` |
| Match-Statistik | `https://api.autodarts.com/as/v0/matches/{id}/stats` |
| Boards | `https://api.autodarts.com/bs/v0/boards` |
| Live-Events | `wss://play.ws.autodarts.com/ms/v0/subscribe`, Kanal `autodarts.matches` |

Eine Extension, die nur auf `autodarts.io` hört, sammelt nichts. Die Standardfilter in den
Extension-Optionen decken beide Domains ab.

## TODO(format): echtes Autodarts-JSON

Der Parser in `server/internal/parser/autodarts/` basiert auf einem **angenommenen** Schema
(siehe `testdata/README.md`). Für den ersten Test den Server mit `MAX_UNPARSED=5000` starten, damit
alle Payloads aufgehoben werden. Echte Responses unter `testdata/` ablegen, Parser anpassen,
dann Admin → „Unerkannte Events“ → „Erneut verarbeiten“ (oder `autodarts-stats reprocess-unparsed`)
und anschließend `autodarts-stats reprocess`.

## API (öffentlich)

```
GET /api/meta
GET /api/leaderboard?sort=average|wins|win_rate|count_180|checkout|highest_checkout|first9|matches|name
                     &order=desc|asc&from=YYYY-MM-DD&to=YYYY-MM-DD&variant=X01&min_matches=5
GET /api/players                 GET /api/players/{id}?from=&to=&variant=
GET /api/h2h?a={id}&b={id}       GET /api/matches?limit=30      GET /api/matches/{id}
```

Board (Bearer API-Key): `POST /api/checkin {"uid"}` und `GET /api/checkins`.

Admin (Cookie-Session nach `POST /api/admin/login {"password"}`): Spieler anlegen/umbenennen/mergen/schützen,
Aliase, löschen, Boards, Chips, Check-ins, Freigabe-Queue (`GET /api/admin/pending`,
`POST /api/admin/pending/{match}/{index}`), Slots umhängen (`POST /api/admin/matches/{match}/slots/{index}`),
unerkannte Events, `POST /api/admin/reprocess`.

## Deployment

* **Docker:** `docker build -t autodarts-stats .` bzw. `deploy/docker-compose.yml`. Daten liegen in `/data`.
* **systemd:** `deploy/autodarts-stats.service`, Binary nach `/usr/local/bin`, Secrets in `/etc/autodarts-stats.env`.
* **TLS:** Der Server spricht nur HTTP. Im Internet unbedingt einen Reverse-Proxy mit TLS davor
  (`deploy/Caddyfile.example`), sonst gehen API-Key und Admin-Passwort im Klartext über die Leitung.
  Die Extension fragt beim Speichern die Berechtigung für die Backend-URL an.

## Entwicklung

```sh
cd server && go test ./...           # Backend-Tests (Normalisierung, Parser, Ingest, API)
cd web && npm run dev                 # Frontend mit Proxy auf :8080
make test
```
