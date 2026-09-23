//go:build pcsc

package reader

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ebfe/scard"
)

// pcscReader liest die UID per "Get Data" APDU (FF CA 00 00 00), das bei
// ACR122U und den meisten PC/SC-Lesern fuer ISO-14443-Karten funktioniert.
type pcscReader struct {
	ctx    *scard.Context
	reader string
}

func openPCSC(name string) (Reader, error) {
	c, err := scard.EstablishContext()
	if err != nil {
		return nil, fmt.Errorf("pcsc: %w (laeuft pcscd?)", err)
	}
	readers, err := c.ListReaders()
	if err != nil || len(readers) == 0 {
		c.Release()
		return nil, errors.New("pcsc: kein Leser gefunden")
	}
	sel := readers[0]
	if name != "" {
		sel = ""
		for _, r := range readers {
			if strings.Contains(strings.ToLower(r), strings.ToLower(name)) {
				sel = r
				break
			}
		}
		if sel == "" {
			c.Release()
			return nil, fmt.Errorf("pcsc: Leser %q nicht gefunden, vorhanden: %v", name, readers)
		}
	}
	return &pcscReader{ctx: c, reader: sel}, nil
}

func (p *pcscReader) Read(ctx context.Context) (string, error) {
	states := []scard.ReaderState{{Reader: p.reader, CurrentState: scard.StateUnaware}}
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := p.ctx.GetStatusChange(states, 500*time.Millisecond); err != nil && !errors.Is(err, scard.ErrTimeout) {
			return "", err
		}
		states[0].CurrentState = states[0].EventState
		if states[0].EventState&scard.StatePresent == 0 {
			continue
		}
		card, err := p.ctx.Connect(p.reader, scard.ShareShared, scard.ProtocolAny)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		rsp, err := card.Transmit([]byte{0xFF, 0xCA, 0x00, 0x00, 0x00})
		card.Disconnect(scard.LeaveCard)
		if err != nil || len(rsp) < 2 || rsp[len(rsp)-2] != 0x90 {
			// Auf Entfernen der Karte warten, sonst Endlosschleife.
			p.waitRemoved(ctx, states)
			continue
		}
		uid := fmt.Sprintf("%x", rsp[:len(rsp)-2])
		p.waitRemoved(ctx, states)
		return uid, nil
	}
}

func (p *pcscReader) waitRemoved(ctx context.Context, states []scard.ReaderState) {
	for ctx.Err() == nil {
		if err := p.ctx.GetStatusChange(states, 500*time.Millisecond); err != nil && !errors.Is(err, scard.ErrTimeout) {
			return
		}
		states[0].CurrentState = states[0].EventState
		if states[0].EventState&scard.StatePresent == 0 {
			return
		}
	}
}

func (p *pcscReader) Close() error { return p.ctx.Release() }
