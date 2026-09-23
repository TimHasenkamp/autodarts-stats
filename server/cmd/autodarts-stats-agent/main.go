// autodarts-stats-agent laeuft auf dem Linux-Client am Board: liest Chips
// vom NFC-Leser, stempelt sie beim Server ein und zeigt den Spielnamen
// ("Tim#4831") an, der in Autodarts eingetragen wird.
//
// Konfiguration ueber Env oder Flags:
//
//	BACKEND_URL  https://darts.example.org
//	API_KEY      Board-API-Key
//	READER       stdin | evdev:/dev/input/by-id/usb-...-event-kbd | pcsc[:Name]
//	NOTIFY       auto | notify-send | none
//	TYPE_CMD     optional: Befehl, der den Spielnamen ins fokussierte Feld tippt,
//	             z.B. "xdotool type --delay 30" oder "wtype"
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"autodarts-stats/internal/reader"
)

type checkinResp struct {
	DisplayName string `json:"display_name"`
	GameName    string `json:"game_name"`
	Token       string `json:"token"`
	ExpiresAt   string `json:"expires_at"`
	Error       string `json:"error"`
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	backend := flag.String("backend", env("BACKEND_URL", ""), "Backend-URL")
	apiKey := flag.String("key", env("API_KEY", ""), "Board-API-Key")
	readerSpec := flag.String("reader", env("READER", "stdin"), "Leser: stdin | evdev:/dev/input/eventN | pcsc[:Name]")
	notify := flag.String("notify", env("NOTIFY", "auto"), "Benachrichtigung: auto | notify-send | none")
	typeCmd := flag.String("type-cmd", env("TYPE_CMD", ""), "Befehl, der den Spielnamen tippt (Name wird als letztes Argument angehaengt)")
	debounce := flag.Duration("debounce", 3*time.Second, "gleicher Chip innerhalb dieser Zeit wird ignoriert")
	flag.Parse()

	if *backend == "" || *apiKey == "" {
		fmt.Fprintln(os.Stderr, "BACKEND_URL und API_KEY sind Pflicht")
		os.Exit(2)
	}
	*backend = strings.TrimRight(*backend, "/")

	rd, err := reader.Open(*readerSpec)
	if err != nil {
		log.Fatal(err)
	}
	defer rd.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if _, err := exec.LookPath("notify-send"); err != nil && *notify == "auto" {
		*notify = "none"
	}
	client := &http.Client{Timeout: 10 * time.Second}
	if err := ping(ctx, client, *backend, *apiKey); err != nil {
		log.Printf("WARNUNG: Backend nicht erreichbar: %v", err)
	} else {
		log.Printf("verbunden mit %s, Leser %s", *backend, *readerSpec)
	}

	var lastUID string
	var lastAt time.Time
	for ctx.Err() == nil {
		uid, err := rd.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			if err == io.EOF {
				break
			}
			log.Printf("Leser: %v", err)
			time.Sleep(time.Second)
			continue
		}
		uid = strings.TrimSpace(uid)
		if uid == "" {
			continue
		}
		if uid == lastUID && time.Since(lastAt) < *debounce {
			continue
		}
		lastUID, lastAt = uid, time.Now()

		res, status, err := checkin(ctx, client, *backend, *apiKey, uid)
		switch {
		case err != nil:
			log.Printf("Check-in fehlgeschlagen: %v", err)
			say(*notify, "Your Darts Check-in", "Server nicht erreichbar: "+err.Error(), true)
		case status == 404:
			log.Printf("unbekannter Chip (%s)", mask(uid))
			say(*notify, "Unbekannter Chip", "Bitte im Admin unter „Chips“ einem Spieler zuordnen.", true)
		case status != 200:
			log.Printf("Server: %d %s", status, res.Error)
			say(*notify, "Your Darts Check-in", fmt.Sprintf("Fehler %d: %s", status, res.Error), true)
		default:
			until := res.ExpiresAt
			if t, err := time.Parse("2006-01-02T15:04:05.000Z", res.ExpiresAt); err == nil {
				until = t.Local().Format("15:04")
			}
			log.Printf("eingecheckt: %s (bis %s)", res.GameName, until)
			say(*notify, "Eingecheckt: "+res.GameName, "Diesen Namen in Autodarts eintragen. Gültig bis "+until+".", false)
			if *typeCmd != "" {
				typeName(*typeCmd, res.GameName)
			}
		}
	}
}

func ping(ctx context.Context, c *http.Client, backend, key string) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", backend+"/api/ingest/ping", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return nil
}

func checkin(ctx context.Context, c *http.Client, backend, key, uid string) (checkinResp, int, error) {
	body, _ := json.Marshal(map[string]string{"uid": uid})
	req, _ := http.NewRequestWithContext(ctx, "POST", backend+"/api/checkin", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.Do(req)
	if err != nil {
		return checkinResp{}, 0, err
	}
	defer res.Body.Close()
	var out checkinResp
	_ = json.NewDecoder(res.Body).Decode(&out)
	return out, res.StatusCode, nil
}

func say(mode, title, text string, isErr bool) {
	if mode == "none" {
		return
	}
	args := []string{"-a", "Your Darts", "-t", "8000"}
	if isErr {
		args = append(args, "-u", "critical")
	}
	args = append(args, title, text)
	if err := exec.Command("notify-send", args...).Run(); err != nil {
		log.Printf("notify-send: %v", err)
	}
}

func typeName(cmd, name string) {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return
	}
	parts = append(parts, name)
	if out, err := exec.Command(parts[0], parts[1:]...).CombinedOutput(); err != nil {
		log.Printf("type-cmd: %v %s", err, strings.TrimSpace(string(out)))
	}
}

func mask(uid string) string {
	if len(uid) <= 4 {
		return "…"
	}
	return "…" + uid[len(uid)-4:]
}
