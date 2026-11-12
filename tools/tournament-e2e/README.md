# Browser-Test: Turniere ohne Extension

Playwright mit Chromium **ohne** Browser-Extension. Der Test beweist, dass Turniere (Anlegen,
Auslosen, Ergebnisse eintragen, Beamer-Baum, Auto-Zuordnung) komplett ohne Extension laufen.

## Voraussetzungen

```sh
cd tools/tournament-e2e
npm install
npx playwright install chromium
```

Go und Node muessen installiert sein.

## Ausfuehren

```sh
make test-tournament        # aus dem Projektverzeichnis
# oder
tools/tournament-e2e/run.sh
SKIP_WEB_BUILD=1 tools/tournament-e2e/run.sh    # Frontend nicht neu bauen
```

`run.sh` baut das Frontend (`web`, landet per embed im Go-Binary). `global-setup.mjs` baut den
Go-Server, startet ihn mit temporaerer DB auf Port 18200 (`E2E_PORT` aenderbar, 8080 ist gesperrt)
und beendet ihn danach. Admin-Passwort und Board-Key werden automatisch erzeugt.

## Szenarien

1. Admin-UI: Login, Spieler anlegen, Turnier (6 Spieler, Platz 3) anlegen, starten, alle Ergebnisse
   per UI eintragen, Nachruecken pruefen, Sieger. Keine Requests an fremde Hosts.
2. Beamer (`/turniere/{id}/beamer/baum?intervall=5`) in zweitem Kontext ohne Login: Reload-Button,
   automatisches Polling, kein Seitenreload, bei blockierter API bleibt der Stand stehen
   (Badge `bracket-offline`).
3. Lucky-Loser-Turnier (8 Spieler) per API angelegt, per UI durchgespielt.
4. Auto-Zuordnung: Match per `POST /api/ingest` (Fixture aus `testdata/placeholder`) erscheint im Baum.
5. Ersetzen nach Runde 1 (bestehender und neuer Name): Ergebnisse bleiben beim Ersetzten, Ersatz
   spielt bis zum Sieger weiter.
6. Zurueckziehen (Aufgabe): Gegner kommt kampflos weiter, „w.o.“ im Baum und auf dem Beamer;
   danach Rueckgaengig.
7. Einzelergebnis „Aufgabe“ ueber die Ausgang-Auswahl.
8. Nachmelden auf Freilos (6 Spieler): erlaubt, nach gespielter Folgerunde abgelehnt.
9. Tauschen zweier Spieler vor ihren Spielen.
10. Korrektur in Runde 1: Bestaetigungsdialog listet betroffene Paarungen; Abbrechen aendert
    nichts, OK erzwingt.
11. Spieler eines laufenden Turniers im Admin loeschen wird abgelehnt („dort ersetzen“).

Die Tests teilen sich eine DB (eindeutige Spielernamen), daher kein `--repeat-each`; zum
Wiederholen `run.sh` mehrfach starten. Fehlerberichte: `report/`, Traces/Screenshots: `results/`.
