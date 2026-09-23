//go:build linux

package reader

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// Linux input_event (64-bit): timeval(16) + type(2) + code(2) + value(4).
const eventSize = 24

const (
	evKey      = 1
	keyEnter   = 28
	keyKPEnter = 96
	eviocgrab  = 0x40044590 // _IOW('E', 0x90, int)
)

// Keycodes eines US-Layouts, wie sie Tastatur-Emulations-Leser senden.
var keymap = map[uint16]byte{
	2: '1', 3: '2', 4: '3', 5: '4', 6: '5', 7: '6', 8: '7', 9: '8', 10: '9', 11: '0',
	16: 'q', 17: 'w', 18: 'e', 19: 'r', 20: 't', 21: 'y', 22: 'u', 23: 'i', 24: 'o', 25: 'p',
	30: 'a', 31: 's', 32: 'd', 33: 'f', 34: 'g', 35: 'h', 36: 'j', 37: 'k', 38: 'l',
	44: 'z', 45: 'x', 46: 'c', 47: 'v', 48: 'b', 49: 'n', 50: 'm',
	71: '7', 72: '8', 73: '9', 75: '4', 76: '5', 77: '6', 79: '1', 80: '2', 81: '3', 82: '0',
	12: '-', 57: ' ',
}

type evdevReader struct {
	f   *os.File
	buf []byte
}

func openEvdev(path string) (Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("evdev %s: %w (Rechte? Gruppe 'input' oder udev-Regel)", path, err)
	}
	// Exklusiv greifen: der Leser soll nicht in den Browser tippen.
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), eviocgrab, 1); errno != 0 {
		f.Close()
		return nil, fmt.Errorf("evdev grab %s: %v", path, errno)
	}
	return &evdevReader{f: f, buf: make([]byte, eventSize*64)}, nil
}

func (e *evdevReader) Read(ctx context.Context) (string, error) {
	var sb strings.Builder
	type res struct {
		n   int
		err error
	}
	for {
		ch := make(chan res, 1)
		go func() {
			n, err := e.f.Read(e.buf)
			ch <- res{n, err}
		}()
		var r res
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case r = <-ch:
		}
		if r.err != nil {
			return "", r.err
		}
		for off := 0; off+eventSize <= r.n; off += eventSize {
			typ := binary.LittleEndian.Uint16(e.buf[off+16:])
			code := binary.LittleEndian.Uint16(e.buf[off+18:])
			val := int32(binary.LittleEndian.Uint32(e.buf[off+20:]))
			if typ != evKey || val != 1 {
				continue
			}
			if code == keyEnter || code == keyKPEnter {
				if sb.Len() > 0 {
					return sb.String(), nil
				}
				continue
			}
			if c, ok := keymap[code]; ok {
				sb.WriteByte(c)
			}
		}
	}
}

func (e *evdevReader) Close() error {
	syscall.Syscall(syscall.SYS_IOCTL, e.f.Fd(), eviocgrab, 0)
	return e.f.Close()
}

var _ = unsafe.Sizeof(0)
