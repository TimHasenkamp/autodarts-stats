// autodarts-stats: Server (Standard), reprocess und Board-Verwaltung.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"autodarts-stats/internal/api"
	"autodarts-stats/internal/config"
	"autodarts-stats/internal/db"
	"autodarts-stats/internal/ingest"
	"autodarts-stats/internal/parser"
	"autodarts-stats/internal/parser/autodarts"
	"autodarts-stats/internal/stats"
	"autodarts-stats/internal/tournament"
	"autodarts-stats/internal/web"
)

func usage() {
	fmt.Fprintln(os.Stderr, `Verwendung:
  autodarts-stats [serve]           Server starten (PORT, DB_PATH, ADMIN_PASSWORD, SESSION_SECRET, TRUST_PROXY)
  autodarts-stats reprocess         Aggregate aus Rohdaten neu berechnen
  autodarts-stats reprocess-unparsed  Unerkannte Events erneut durch den Parser schicken
  autodarts-stats board add <name>  Board anlegen, API-Key wird einmalig ausgegeben
  autodarts-stats board list
  autodarts-stats board revoke <name>`)
}

func main() {
	cfg, err := config.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}
	d, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer d.Close()
	parsers := []parser.Parser{autodarts.New()}
	ing := ingest.New(d, parsers, cfg.MaxUnparsed)
	tour := tournament.New(d)
	ing.MatchFinished = tour.MatchFinished

	switch cmd {
	case "serve":
		serve(cfg, ing, tour)
	case "reprocess":
		n, err := ing.Reprocess(context.Background())
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%d Matches neu berechnet\n", n)
	case "reprocess-unparsed":
		n, total, err := ing.ReprocessUnparsed(context.Background())
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%d von %d unerkannten Events jetzt erkannt\n", n, total)
	case "board":
		if len(args) == 0 {
			usage()
			os.Exit(2)
		}
		switch args[0] {
		case "add":
			if len(args) < 2 {
				usage()
				os.Exit(2)
			}
			id, key, err := api.CreateBoard(d, strings.Join(args[1:], " "))
			if err != nil {
				log.Fatal(err)
			}
			fmt.Printf("Board #%d angelegt.\nAPI-Key (nur jetzt sichtbar): %s\n", id, key)
		case "list":
			rows, err := d.Query(`SELECT id, name, created_at, COALESCE(last_seen_at,'-') FROM boards ORDER BY id`)
			if err != nil {
				log.Fatal(err)
			}
			defer rows.Close()
			for rows.Next() {
				var id int64
				var name, created, seen string
				if err := rows.Scan(&id, &name, &created, &seen); err != nil {
					log.Fatal(err)
				}
				fmt.Printf("#%d\t%s\tangelegt %s\tzuletzt %s\n", id, name, created, seen)
			}
		case "revoke":
			if len(args) < 2 {
				usage()
				os.Exit(2)
			}
			res, err := d.Exec(`DELETE FROM boards WHERE name = ?`, strings.Join(args[1:], " "))
			if err != nil {
				log.Fatal(err)
			}
			n, _ := res.RowsAffected()
			fmt.Printf("%d Board(s) entfernt\n", n)
		default:
			usage()
			os.Exit(2)
		}
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func serve(cfg config.Config, ing *ingest.Service, tour *tournament.Service) {
	srv := api.New(api.Options{
		DB: ing.DB, Ingest: ing, Stats: stats.New(ing.DB), Tournaments: tour,
		AdminPassword: cfg.AdminPassword, SessionSecret: cfg.SessionSecret, TrustProxy: cfg.TrustProxy,
		CheckinTTL: cfg.CheckinTTL, Static: web.Handler(),
	})
	if cfg.AdminPassword == "" {
		log.Println("WARNUNG: ADMIN_PASSWORD nicht gesetzt, Admin-Login ist deaktiviert")
	}
	hs := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		log.Printf("autodarts-stats lauscht auf %s (DB: %s)", hs.Addr, cfg.DBPath)
		if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = hs.Shutdown(ctx)
}
