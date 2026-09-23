# Your Darts – Checkliste für den ersten MVP-Test

Ziel dieses Durchlaufs ist nicht eine fertige Rangliste, sondern der Nachweis, dass die Kette
Extension → Server → Admin steht, und die Aufzeichnung des echten Autodarts-Formats.

## Was beim ersten Test funktionieren wird und was nicht

- **Funktioniert:** Extension sammelt und sendet, Server nimmt an, Dashboard und Admin laufen,
  Boards, Chips, manuelles Einchecken, Freigabe-Queue.
- **Wird vermutlich nicht klappen:** Matches erscheinen in der Rangliste. Der Parser rät das Format.
  Die echten Daten landen unter Admin → „Unerkannte Events“. Das ist erwartet.
- **Nichts geht verloren:** Sobald der Parser an das echte Format angepasst ist, verwandelt
  „Erneut verarbeiten“ die gesammelten Events nachträglich in Matches. Voraussetzung ist
  `MAX_UNPARSED=5000` aus Schritt 1.

## 1. Server starten

Auf deinem PC oder einem Rechner im LAN. Ohne TLS ist für den Test in Ordnung.

```sh
make build
MAX_UNPARSED=5000 ADMIN_PASSWORD=test1234 ./autodarts-stats board add "Wohnzimmer"
MAX_UNPARSED=5000 ADMIN_PASSWORD=test1234 ./autodarts-stats
```

- [ ] API-Key aus der Ausgabe von `board add` notieren. Er wird nur einmal angezeigt.
- [ ] `http://localhost:8080` öffnen. „Your Darts“ mit Icon erscheint, Rangliste ist leer.
- [ ] Admin öffnen, mit dem Passwort anmelden. Board „Wohnzimmer“ wird angezeigt.
- [ ] Läuft der Browser mit Autodarts auf einem anderen Rechner: `http://<IP-des-Servers>:8080`
      ist von dort erreichbar.

## 2. Extension installieren

Chrome oder Firefox ab Version 140.

- [ ] **Chrome:** `chrome://extensions`, Entwicklermodus an, „Entpackte Erweiterung laden“,
      Ordner `extension/` wählen. Bleibt nach einem Neustart installiert.
- [ ] **Firefox:** `about:debugging#/runtime/this-firefox`, „Temporäres Add-on laden“,
      `extension/manifest.json` wählen. Achtung: Nach dem Schließen von Firefox ist die Extension
      weg und muss neu geladen werden. Für dauerhafte Installation siehe unten.
- [ ] Extension-Icon anklicken: Backend-URL, API-Key, Board-Name eintragen, Speichern.
      Die Berechtigungsabfrage für die Backend-URL bestätigen.
- [ ] „Verbindung testen“ zeigt `OK: {"board":"Wohnzimmer"}`.
      Falls nicht: URL ohne Slash am Ende, Key komplett kopiert, Server erreichbar.
- [ ] play.autodarts.com **neu laden**. Ohne Reload ist der Hook noch nicht aktiv.
      Die Extension arbeitet auf `play.autodarts.com` und auf `play.autodarts.io`, beide liefern
      dieselbe App. Gesammelt werden die Antworten von `api.autodarts.com`.

## 3. Ein Testmatch spielen

- [ ] Lokales Spiel mit zwei Gastspielern anlegen, kurze Variante wie 170 oder 301 mit einem Leg.
      Idealerweise beide Namen ohne Umlaute, damit das Vergleichen leichter fällt.
- [ ] Während des Spiels das Extension-Badge beobachten: Die Zahl darauf ist die Queue und sollte
      nach jedem Wurf kurz erscheinen und wieder verschwinden.
- [ ] Extension-Icon anklicken: „Gesendet“ steigt, „Letzter Fehler“ bleibt leer.
- [ ] Im Server-Terminal erscheinen Zeilen mit `POST /api/ingest 200`.
- [ ] Match zu Ende spielen.

## 4. Diagnose in der Extension ablesen

Das Extension-Symbol anklicken. Unter „Diagnose“ steht, wie weit die Daten gekommen sind.
Die Anzeige aktualisiert sich alle zwei Sekunden.

| Anzeige | Bedeutung |
|---|---|
| „Der Hook meldet sich nicht.“ | Das Skript läuft nicht auf der Seite. Tab mit play.autodarts.com offen? Nach dem Installieren neu geladen? In Firefox die Berechtigung erteilen. |
| Alle Zähler auf 0 | Der Hook läuft, sieht aber keine Netzwerkaufrufe. Ein paar Würfe spielen. |
| „an Filter übergeben“ 0 | Autodarts liefert die Daten über einen anderen Weg. |
| „durchgelassen“ 0 | Die Filter werfen alles weg. Haken bei „Alles erfassen (Debug)“ setzen und Seite neu laden. |
| „an Server gesendet“ 0 und „in Queue“ steigt | Der Server nimmt nicht an. Darunter steht der Fehler, meist ein falscher API-Key. |
| „an Server gesendet“ steigt | Alles in Ordnung, weiter zu Schritt 5. |

## 5. Ergebnis prüfen

- [ ] Admin → „Unerkannte Events“ ist gefüllt. Die Spalten Art (`fetch` oder `ws`), URL und Anfang
      zeigen, was Autodarts geliefert hat.
- [ ] Falls unter „Matches“ tatsächlich schon etwas steht: Glückstreffer. Dann prüfen, ob Namen,
      Sieger und Legs stimmen.
- [ ] Falls unter „Unerkannte Events“ **nichts** steht, obwohl gesendet wurde: Extension-Icon
      anklicken, unter „Erweitert“ prüfen, dass die Filter auf den Standardwerten stehen.
      Erster Wert `api\.autodarts\.io/`, zweiter Wert leer.

## 6. Format sichern

- [ ] In „Unerkannte Events“ auf drei bis vier Zeitstempel klicken. Das JSON öffnet sich im neuen Tab.
      Interessant sind: ein Stand mitten im Leg, einer direkt nach dem Leg-Ende, der Endstand nach dem
      Match, und eine `ws`-Nachricht.
- [ ] Jede Datei unter `testdata/` speichern, zum Beispiel `match_running.json`,
      `match_leg_end.json`, `match_finished.json`, `ws_state.json`.
- [ ] Parser anpassen lassen. Danach im Admin „Erneut verarbeiten“ klicken, die Matches tauchen
      rückwirkend auf. Anschließend `./autodarts-stats reprocess`.

## 7. Chip-Flow ohne Leser (optional)

- [ ] Admin → Spieler → „Neuer Spieler“ mit deinem Namen und Haken bei „geschützt“.
- [ ] Admin → Check-ins → „Manuell einchecken“, dich und das Board wählen.
      Es erscheint ein Spielname wie `Tim#4831`.
- [ ] play.autodarts.com neu laden: Unten rechts blendet die Extension den Namen mit „Kopieren“ ein.
- [ ] Ein Match mit genau diesem Namen spielen. Nach dem Parser-Fix zählt es dir direkt.
      Ein Match nur mit „Tim“ landet in der Freigabe.

## Firefox dauerhaft installieren

Ein temporäres Add-on verschwindet beim Beenden von Firefox. Für einen Rechner, der am Board
dauerhaft läuft, gibt es drei Wege:

1. **Chrome oder Chromium benutzen.** Dort bleibt die entpackte Extension installiert. Einfachster Weg.
2. **Extension bei Mozilla signieren lassen.** Kostenloses Konto auf addons.mozilla.org, Upload als
   „unlisted“ (nicht öffentlich gelistet), fertige `.xpi` herunterladen und dauerhaft installieren.
   Dafür `make extension-zip` hochladen.
3. **Firefox Developer Edition oder ESR** mit `about:config` → `xpinstall.signatures.required` auf
   `false`. Im normalen Firefox greift diese Einstellung nicht.

Für den ersten Test reicht das temporäre Add-on. Das Neuladen dauert zwanzig Sekunden.

## Wann Zahlen erscheinen

Die Erfassung läuft fortlaufend, gewertet wird aber gestaffelt:

| Zeitpunkt | Sichtbar |
|---|---|
| Match läuft, erstes Leg | Spieler stehen unter „Spieler“ mit dem Hinweis „läuft“, noch ohne Zahlen |
| Leg beendet | Average, 180er und Checkouts des Legs zählen |
| Match beendet | Sieg und Match zählen, der Spieler erscheint in der Rangliste |

Im Admin stehen Spieler sofort, dort werden alle angelegten Spieler gezeigt, auch ohne Wertung.
Die Rangliste verlangt zusätzlich mindestens 5 Matches, einstellbar über „Min. Matches“.

## Wo die Daten liegen

Die Datenbank ist eine gewöhnliche SQLite-Datei, die beim ersten Start angelegt wird. Sie steckt
**nicht** im Binary, nur das Dashboard ist eingebaut. Ohne `DB_PATH` heißt sie `autodarts-stats.db`
und liegt im Verzeichnis, aus dem der Server gestartet wurde. Startest du ihn aus einem anderen
Verzeichnis, arbeitet er mit einer neuen, leeren Datenbank. Dabei entstehen drei Dateien:
`.db`, `.db-wal` und `.db-shm`. Zum Sichern oder Umziehen den Server stoppen und alle drei kopieren.

## Typische Stolpersteine

- Läuft schon ein alter Server auf Port 8080, startet der neue nicht und beendet sich mit
  „address already in use“. Dann zeigt die Extension einen 401, weil der alte Server eine andere
  Datenbank hat. Prüfen mit `ss -lnt | grep 8080`, alten Prozess mit `pkill -f autodarts-stats` beenden.
- Ob überhaupt je ein gültiger Aufruf ankam, steht im Admin unter „Boards“ in der Spalte
  „Zuletzt gesehen“. Steht dort ein Strich, hat der API-Key noch nie gepasst.
- Der Admin-Login setzt ein Cookie. Über `http://` funktioniert das. Bei Zugriff über verschiedene
  Hostnamen (localhost und IP) musst du dich pro Hostname neu anmelden.
- Chrome legt den Service Worker der Extension nach 30 Sekunden schlafen. Die Queue wird dabei
  gespeichert und per Alarm nachgesendet. Ein paar Sekunden Verzögerung sind normal.
- Firefox fragt Host-Berechtigungen einzeln ab. Falls „Verbindung testen“ scheitert oder nichts
  gesendet wird: `about:addons` → Your Darts Collector → Reiter „Berechtigungen“ und den Zugriff
  auf die Backend-Adresse erlauben.
- Wenn der Server neu gestartet wird, verfallen Admin-Logins, solange `SESSION_SECRET` nicht
  gesetzt ist.
- Extension-Filter bewusst breit: Für den ersten Test wird alles von `api.autodarts.io` gesendet,
  ausgenommen Nutzer- und Auth-Endpunkte. Nach dem Parser-Fix können die Muster in den
  Extension-Optionen wieder enger gesetzt werden.
