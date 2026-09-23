//go:build !pcsc

package reader

import "errors"

func openPCSC(string) (Reader, error) {
	return nil, errors.New("PC/SC-Unterstuetzung fehlt: Agent mit 'go build -tags pcsc' bauen (braucht libpcsclite-dev)")
}
