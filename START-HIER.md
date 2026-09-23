# Your Darts – Paket für den Board-Rechner

Alles fertig gebaut. Auf dem Rechner werden weder Go noch Node gebraucht.

## 1. Auspacken und Binary wählen

```sh
mkdir -p ~/your-darts && cd ~/your-darts
# (Inhalt dieses Pakets hierher entpacken)
uname -m        # x86_64 -> amd64,  aarch64 -> arm64
```

Passendes Binary an den Platz kopieren und ausführbar machen:

```sh
cp bin/autodarts-stats-linux-amd64 autodarts-stats          # oder -arm64
chmod +x autodarts-stats
```

Das ist alles, was du für den Test brauchst. Den zweiten Binary im Ordner `bin/`,
den Chip-Agenten, lässt du vorerst liegen. Er wird erst mit einem NFC-Leser gebraucht,
siehe ganz unten.

## 2. Board anlegen und Server starten

```sh
export DB_PATH=~/your-darts/your-darts.db
export ADMIN_PASSWORD=test1234
export MAX_UNPARSED=5000        # wichtig für den ersten Test, hebt alle Rohdaten auf

./autodarts-stats board add "Wohnzimmer"    # API-Key notieren, wird nur einmal angezeigt
./autodarts-stats                            # läuft auf http://localhost:8080
```

Der Server muss laufen, solange gespielt wird. Fürs Erste reicht ein offenes Terminal.

## 3. Extension laden

### Firefox (ab Version 140)

1. `about:debugging#/runtime/this-firefox` in die Adresszeile eingeben
2. „Temporäres Add-on laden…“ anklicken
3. Im Dateidialog entweder `~/your-darts/autodarts-stats-extension.zip` auswählen
   oder `~/your-darts/extension/manifest.json`. Beides funktioniert.

Die Extension erscheint dann unter „Temporäre Erweiterungen“. **Wichtig:** Beim Beenden von Firefox
wird sie entfernt. Nach jedem Firefox-Start sind diese drei Schritte zu wiederholen. Für den Test
ist das in Ordnung, dauerhafte Installation siehe unten.

### Chrome oder Chromium

1. `chrome://extensions` öffnen
2. Oben rechts den Entwicklermodus einschalten
3. „Entpackte Erweiterung laden“ anklicken und den Ordner `~/your-darts/extension` auswählen

Hier bleibt die Extension nach einem Neustart installiert.

### Danach in beiden Browsern gleich

4. Extension-Symbol anklicken (in Firefox ggf. erst auf das Puzzleteil in der Symbolleiste)
5. Eintragen: Backend-URL `http://localhost:8080`, API-Key aus Schritt 2, Board-Name `Wohnzimmer`
6. Speichern, die Nachfrage nach der Berechtigung bestätigen
7. „Verbindung testen“ muss `OK: {"board":"Wohnzimmer"}` anzeigen
8. **play.autodarts.com neu laden**, sonst ist der Hook nicht aktiv
   (`play.autodarts.io` geht genauso, es ist dieselbe App)

Den Ordner `~/your-darts/extension` nicht verschieben oder löschen, der Browser merkt sich den Pfad.

### Wenn Firefox nichts sendet

Firefox fragt Host-Berechtigungen strenger ab als Chrome. Dann `about:addons` öffnen, den Eintrag
„Your Darts Collector“ anklicken, Reiter „Berechtigungen“, und den Zugriff auf
`http://localhost:8080` erlauben. Danach play.autodarts.com neu laden.

### Firefox dauerhaft installieren

Drei Wege, falls das Neuladen nach jedem Start nervt:

1. **Chromium zusätzlich installieren** und die Extension nur dort laden. Einfachster Weg.
2. **Signieren lassen:** kostenloses Konto auf addons.mozilla.org. Vorher die Extension packen,
   die `manifest.json` muss dabei direkt im Zip liegen:
   ```sh
   cd ~/your-darts/extension && zip -r ../your-darts-extension.zip .
   ```
   Diese Datei als „unlisted“ hochladen, die signierte `.xpi` herunterladen und per
   `about:addons` → Zahnrad → „Add-on aus Datei installieren“ dauerhaft einrichten.
3. **Firefox Developer Edition oder ESR** mit `about:config` → `xpinstall.signatures.required`
   auf `false`. Im normalen Firefox wirkt diese Einstellung nicht.

## 4. Testen

Weiter mit `docs/mvp-test-checkliste.md`. Kurzfassung: ein Match spielen, dann im Admin unter
„Unerkannte Events“ nachsehen. Dass dort etwas steht und die Rangliste leer bleibt, ist beim ersten
Durchlauf normal. Der Parser kennt das echte Autodarts-Format noch nicht.

Drei bis vier dieser JSON-Dateien speichern und zurückschicken, dann wird der Parser angepasst.
Danach genügt im Admin „Erneut verarbeiten“ und die Matches erscheinen rückwirkend.

## Chip-Agent: jetzt noch nicht nötig

Im Paket liegt neben dem Server ein zweites Programm, `autodarts-stats-agent`. Es ist **kein**
Teil der Erfassung und muss für den Test **nicht** laufen. Die Matches kommen allein über die
Browser-Extension herein.

Der Agent hat nur eine Aufgabe: Er liest einen NFC-Chip am Board-Rechner und meldet ihn beim
Server an. Der Server gibt dann einen Spielnamen mit Code zurück, etwa `Tim#4831`. Wer diesen
Namen in Autodarts einträgt, bekommt das Match sicher zugeordnet. Das schützt geschützte Spieler
davor, dass jemand unter ihrem Namen spielt.

Ohne angeschlossenen Leser bringt er nichts. Sobald einer da ist:

```sh
cp bin/autodarts-stats-agent-linux-amd64 autodarts-stats-agent    # oder -arm64
chmod +x autodarts-stats-agent
BACKEND_URL=http://localhost:8080 API_KEY=adb_... READER=stdin ./autodarts-stats-agent
```

Mit `READER=stdin` kannst du eine Chipnummer von Hand eintippen und den Ablauf ausprobieren.
Welche Leser unterstützt werden und wie der Agent als Dienst läuft, steht in `README.md`.

Den Ablauf kannst du auch ganz ohne Agent testen: im Admin unter „Check-ins“ gibt es
„Manuell einchecken“.

## Dashboard vom Handy

`http://<IP-des-Board-Rechners>:8080` im gleichen WLAN. Die IP zeigt `ip addr`.
Am Handy einmal separat im Admin anmelden, das Cookie gilt pro Adresse.

## Was sonst noch im Paket liegt

| Pfad | Inhalt |
|---|---|
| `bin/` | Server und Chip-Agent für amd64 und arm64 |
| `extension/` | Browser-Extension als Ordner, für Chrome |
| `autodarts-stats-extension.zip` | dieselbe Extension gepackt, für Firefox und zum Signieren |
| `docs/` | Test-Checkliste |
| `server/`, `web/` | Quellcode, falls doch selbst gebaut werden soll (`make build`) |
| `deploy/` | systemd-Units, Caddy-Beispiel, docker-compose |
| `testdata/` | Platzhalter-Beispiele des angenommenen JSON-Formats |

| `tools/` | Browser-Test der Extension (`make test-extension`) |
