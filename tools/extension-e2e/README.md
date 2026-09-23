# End-to-End-Test der Extension

Startet ein echtes Chromium mit geladener Extension, fälscht `play.autodarts.io` und
`api.autodarts.io` und prüft, ob ein Match bis in die Datenbank durchläuft.

Dieser Test hat den Fehler gefunden, dass die Extension das Payload als JSON-Zeichenkette
schickt, der Server aber ein Objekt erwartet hat. Reine Go-Tests konnten das nicht sehen,
weil dort die Anfrage von Hand gebaut wurde.

## Voraussetzungen

```sh
cd tools/extension-e2e
npm install
npx playwright install chromium
```

## Ausführen

```sh
make test-extension          # aus dem Projektverzeichnis
# oder
tools/extension-e2e/run.sh
```

Das Skript baut nichts. Vorher `make build-server` laufen lassen, damit `./autodarts-stats`
aktuell ist. Es startet einen eigenen Server auf Port 18100 mit einer temporären Datenbank
und beendet ihn wieder. Port 8080 wird nicht angefasst.

Ausgabe bei Erfolg endet mit `ERGEBNIS: OK` und zeigt die erkannten Spieler.
