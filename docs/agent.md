# Chip-Agent am Board installieren

Der Agent läuft auf dem Linux-Rechner am Dartboard. Er liest NFC-Chips, stempelt den Spieler beim
Your-Darts-Server ein und zeigt den Spielnamen an (z. B. `Tim#4831`), der in Autodarts eingetragen wird.

**Voraussetzungen:** Debian oder Ubuntu (x86_64), angemeldeter Desktop-Benutzer am Board, ein NFC-Leser
und ein API-Key des Boards (im Dashboard unter **Admin → Boards → Neues Board**, wird nur einmal angezeigt).

## 1. Paketquelle eintragen (einmalig)

```sh
curl -fsSL https://timhasenkamp.github.io/autodarts-stats/your-darts.gpg \
  | sudo tee /usr/share/keyrings/your-darts.gpg >/dev/null

echo "deb [arch=amd64 signed-by=/usr/share/keyrings/your-darts.gpg] https://timhasenkamp.github.io/autodarts-stats stable main" \
  | sudo tee /etc/apt/sources.list.d/your-darts.list

sudo apt update
```

Die erste Zeile legt den öffentlichen Schlüssel ab, mit dem die Pakete signiert sind. apt nimmt aus dieser
Quelle nur Pakete an, die mit diesem Schlüssel signiert wurden.

## 2. Installieren

```sh
sudo apt install your-darts-agent
```

Mitinstalliert wird `pcscd` (für PC/SC-Leser) und `libnotify-bin` (Desktop-Benachrichtigungen).
Das Paket legt ab:

| Datei | Zweck |
|---|---|
| `/usr/bin/autodarts-stats-agent` | der Agent |
| `/usr/lib/systemd/user/autodarts-stats-agent.service` | Dienst für den angemeldeten Benutzer |
| `/usr/share/your-darts-agent/agent.env.example` | Vorlage für die Konfiguration |
| `/usr/share/your-darts-agent/99-autodarts-nfc.rules` | udev-Vorlage für Tastatur-Leser |

## 3. Konfigurieren

Als der Benutzer, der am Board angemeldet ist (ohne `sudo`):

```sh
mkdir -p ~/.config
cp /usr/share/your-darts-agent/agent.env.example ~/.config/autodarts-stats-agent.env
nano ~/.config/autodarts-stats-agent.env
```

| Eintrag | Bedeutung |
|---|---|
| `BACKEND_URL` | `https://darts.your-admins.de` |
| `API_KEY` | Key des Boards (`adb_…`) |
| `READER` | `pcsc` für PC/SC-Leser, `evdev:/dev/input/…` für Tastatur-Leser (siehe unten) |
| `NOTIFY` | `auto` (Desktop-Benachrichtigung, wenn vorhanden) oder `none` |
| `TYPE_CMD` | optional: tippt den Spielnamen ins fokussierte Feld, z. B. `xdotool type --delay 30` (Wayland: `wtype`) |

### Welcher Leser?

**PC/SC-Leser** (z. B. ACR122U): `READER=pcsc`. Der Dienst `pcscd` muss laufen:

```sh
sudo systemctl enable --now pcscd
```

**Tastatur-Leser** (USB-HID, tippt die Chip-Nummer wie eine Tastatur): Damit der Agent ohne root lesen darf,
eine udev-Regel anlegen.

```sh
lsusb                                   # Zeile des Lesers suchen, z. B. "ID ffff:0035"
sudo cp /usr/share/your-darts-agent/99-autodarts-nfc.rules /etc/udev/rules.d/
sudo nano /etc/udev/rules.d/99-autodarts-nfc.rules    # idVendor / idProduct eintragen
sudo udevadm control --reload && sudo udevadm trigger
sudo usermod -aG input "$USER"          # danach einmal ab- und wieder anmelden
```

Dann `READER=evdev:/dev/input/autodarts-nfc` eintragen.

## 4. Starten

```sh
systemctl --user enable --now autodarts-stats-agent
```

Der Agent startet damit bei jeder Anmeldung automatisch. Prüfen:

```sh
systemctl --user status autodarts-stats-agent
journalctl --user -u autodarts-stats-agent -f      # Log live, Strg+C beendet
```

Im Log muss `Agent 1.x.y verbunden mit https://darts.your-admins.de` stehen. Chip auflegen: Es erscheint
eine Benachrichtigung mit dem Spielnamen. Ein unbekannter Chip taucht im Dashboard unter
**Admin → Chips → Unbekannte Chips** auf und wird dort einem Spieler zugeordnet.

Nach einer Änderung an der Konfiguration:

```sh
systemctl --user restart autodarts-stats-agent
```

## Updates

Neue Versionen kommen über apt, laufende Agenten werden dabei automatisch neu gestartet:

```sh
sudo apt update && sudo apt upgrade
autodarts-stats-agent -version
```

## Deinstallieren

```sh
systemctl --user disable --now autodarts-stats-agent
sudo apt remove your-darts-agent
# Paketquelle ebenfalls entfernen:
sudo rm /etc/apt/sources.list.d/your-darts.list /usr/share/keyrings/your-darts.gpg
```

Die Konfiguration `~/.config/autodarts-stats-agent.env` bleibt liegen und kann von Hand gelöscht werden.

## Fehlersuche

| Meldung / Verhalten | Ursache |
|---|---|
| Dienst startet nicht, `ConditionPathExists` | `~/.config/autodarts-stats-agent.env` fehlt (Schritt 3) |
| `BACKEND_URL und API_KEY sind Pflicht` | Eintrag in der Konfiguration leer |
| `Backend nicht erreichbar: HTTP 401` | API-Key vertippt oder Board im Admin entfernt |
| `Backend nicht erreichbar: …` (sonstiges) | URL falsch oder Server down; `curl https://darts.your-admins.de/api/health` testen |
| `pcsc: … (laeuft pcscd?)` | `sudo systemctl enable --now pcscd` |
| `pcsc: kein Leser gefunden` | Leser nicht angeschlossen; `pcsc_scan` (Paket `pcsc-tools`) zeigt erkannte Leser |
| `evdev …: permission denied (Rechte? …)` | udev-Regel fehlt oder Benutzer nicht in Gruppe `input` (neu anmelden) |
| `unbekannter Chip` | Chip im Dashboard unter **Admin → Chips** einem Spieler zuordnen |
| Keine Benachrichtigung | kein Desktop angemeldet oder `NOTIFY=none` |
| `apt update`: Signatur ungültig | Schlüssel aus Schritt 1 fehlt oder ist veraltet: Schritt 1 wiederholen |
