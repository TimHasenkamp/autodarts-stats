// Package db oeffnet die SQLite-Datenbank und fuehrt eingebettete Migrationen aus.
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)", path)
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// modernc/sqlite ist nicht fuer viele parallele Writer gedacht.
	d.SetMaxOpenConns(1)
	if err := Migrate(context.Background(), d); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

func Migrate(ctx context.Context, d *sql.DB) error {
	return migrateUpTo(ctx, d, "")
}

// migrateUpTo wendet die Migrationen in Namensreihenfolge an, bei last != ""
// nur bis einschliesslich last (fuer Tests alter Schemastaende).
func migrateUpTo(ctx context.Context, d *sql.DB, last string) error {
	if _, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if last != "" && name > last {
			break
		}
		var n int
		if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			return rollback(tx, fmt.Errorf("migration %s: %w", name, err))
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(name) VALUES (?)`, name); err != nil {
			return rollback(tx, fmt.Errorf("migration %s eintragen: %w", name, err))
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %s commit: %w", name, err)
		}
	}
	return nil
}

// rollback bricht tx ab und haengt einen Rollback-Fehler an err an, statt
// ihn zu verschlucken (eine halb angewandte Migration soll auffallen).
func rollback(tx *sql.Tx, err error) error {
	if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
		return errors.Join(err, fmt.Errorf("rollback: %w", rerr))
	}
	return err
}
