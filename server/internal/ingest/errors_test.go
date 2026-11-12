package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// codedErr ahmt *sqlite.Error (Methode Code()) nach.
type codedErr int

func (c codedErr) Error() string { return fmt.Sprintf("sqlite %d", int(c)) }
func (c codedErr) Code() int     { return int(c) }

func TestIsPermanent(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unbekannt", errors.New("x"), false},
		{"canceled", fmt.Errorf("a: %w", context.Canceled), false},
		{"deadline", context.DeadlineExceeded, false},
		{"sentinel", fmt.Errorf("a: %w", ErrInvalidEvent), true},
		{"Invalid()", Invalid(errors.New("x")), true},
		{"locked_text", errors.New("database is locked (5) (SQLITE_BUSY)"), false},
		{"busy_wrapped", fmt.Errorf("spieler anlegen: %w", codedErr(5)), false},
		{"busy_snapshot", codedErr(5 | 2<<8), false},
		{"busy", codedErr(5), false},
		{"locked", codedErr(6), false},
		{"ioerr", codedErr(10), false},
		{"ioerr_read", codedErr(10 | 1<<8), false},
		{"ioerr_write", fmt.Errorf("commit: %w", codedErr(10|3<<8)), false},
		{"canceled_but_invalid_wrapped", fmt.Errorf("%w: %w", ErrInvalidEvent, context.Canceled), false},
		{"full", codedErr(13), false},
		{"constraint", codedErr(19), true},
		{"constraint_unique", codedErr(2067), true},
		{"constraint_trigger", fmt.Errorf("w: %w", codedErr(1811)), true},
		{"mismatch", codedErr(20), true},
		{"toobig", codedErr(18), true},
	}
	for _, c := range cases {
		if got := IsPermanent(c.err); got != c.want {
			t.Errorf("%s: IsPermanent(%v) = %v, erwartet %v", c.name, c.err, got, c.want)
		}
	}
	var c codedErr
	if !errors.As(Invalid(codedErr(5)), &c) || c != 5 {
		t.Error("Invalid muss die Ursache erhalten")
	}
}

// Echter SQLITE_BUSY aus dem Treiber: eine zweite Verbindung schreibt,
// waehrend die erste eine Schreibtransaktion haelt.
func TestIsPermanentRealBusy(t *testing.T) {
	// Der Treiber "sqlite" ist ueber das db-Paket registriert.
	path := filepath.Join(t.TempDir(), "busy.db")
	a, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := a.Exec(`CREATE TABLE x (v INTEGER)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := a.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `ROLLBACK`)
	_, err = b.Exec(`INSERT INTO x VALUES (1)`)
	if err == nil {
		t.Fatal("erwartet SQLITE_BUSY")
	}
	if IsPermanent(err) {
		t.Fatalf("busy als dauerhaft eingestuft: %v", err)
	}
}
