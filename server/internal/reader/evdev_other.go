//go:build !linux

package reader

import "errors"

func openEvdev(string) (Reader, error) { return nil, errors.New("evdev gibt es nur unter Linux") }
