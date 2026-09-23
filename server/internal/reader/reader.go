// Package reader liefert Chip-Seriennummern (UIDs) von verschiedenen
// NFC-/RFID-Lesern am Board-Client.
//
// Backends:
//   - stdin            : UID pro Zeile eintippen (Test)
//   - evdev:/dev/input/eventN : USB-Leser im Tastatur-Modus (tippt UID + Enter);
//     wird exklusiv gegriffen, damit nichts im Browser landet
//   - pcsc             : PC/SC-Leser (ACR122U u.a.), Build-Tag "pcsc" noetig
package reader

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

type Reader interface {
	// Read blockiert bis ein Chip gelesen wurde und liefert dessen UID (Hex).
	Read(ctx context.Context) (string, error)
	Close() error
}

// Open parst die READER-Angabe.
func Open(spec string) (Reader, error) {
	switch {
	case spec == "" || spec == "stdin":
		return &lineReader{r: bufio.NewReader(os.Stdin), name: "stdin"}, nil
	case strings.HasPrefix(spec, "evdev:"):
		return openEvdev(strings.TrimPrefix(spec, "evdev:"))
	case strings.HasPrefix(spec, "file:"):
		f, err := os.Open(strings.TrimPrefix(spec, "file:"))
		if err != nil {
			return nil, err
		}
		return &lineReader{r: bufio.NewReader(f), c: f, name: spec}, nil
	case spec == "pcsc" || strings.HasPrefix(spec, "pcsc:"):
		return openPCSC(strings.TrimPrefix(strings.TrimPrefix(spec, "pcsc"), ":"))
	}
	return nil, fmt.Errorf("unbekannter Reader %q (stdin | evdev:/dev/input/eventN | pcsc)", spec)
}

type lineReader struct {
	r    *bufio.Reader
	c    io.Closer
	name string
}

func (l *lineReader) Read(ctx context.Context) (string, error) {
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		s, err := l.r.ReadString('\n')
		ch <- res{s, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ch:
		if r.err != nil && r.s == "" {
			return "", r.err
		}
		return strings.TrimSpace(r.s), nil
	}
}

func (l *lineReader) Close() error {
	if l.c != nil {
		return l.c.Close()
	}
	return nil
}
