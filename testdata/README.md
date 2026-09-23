# testdata

Hier kommen echte Responses aus den DevTools von play.autodarts.io hin.

## Was ich brauche

Die Live-App (Stand 23.09.2026) benutzt diese Adressen:

| Zweck | Adresse |
|---|---|
| Matchzustand | `https://api.autodarts.com/gs/v0/matches/{id}` |
| Match-Statistik | `https://api.autodarts.com/as/v0/matches/{id}/stats` |
| Boards | `https://api.autodarts.com/bs/v0/boards` |
| Live-Events | `wss://play.ws.autodarts.com/ms/v0/subscribe` |

Die Seite selbst ist unter `play.autodarts.com` erreichbar, `play.autodarts.io` liefert dieselbe App.

1. **Match-Zustand per HTTP**: DevTools → Network → Filter `matches` → Response eines
   `GET https://api.autodarts.com/gs/v0/matches/<id>` als `match_x01_running.json`
   speichern. Am besten je ein Stand mitten im Leg, direkt nach einem Leg-Ende und nach Matchende.
2. **WebSocket-Nachrichten**: DevTools → Network → WS → `subscribe` → Messages. Eine Nachricht mit
   `channel` = `autodarts.matches` als `ws_match_state.json` speichern.
3. **Andere Varianten** (Cricket, ATC, ...) als `match_<variante>_*.json`.

Dateinamen sind frei; alles mit Endung `.json` in diesem Ordner wird von den Parser-Tests geladen.
`placeholder/` enthaelt synthetische Beispiele nach dem angenommenen Schema (siehe
`server/internal/parser/autodarts/autodarts.go`, TODO(format)).

Der Server legt ausserdem alles, was kein Parser erkennt, in der Tabelle `unparsed_events` ab
(Admin → „Unerkannte Events“). Das ist der schnellste Weg, das echte Format zu sehen.
