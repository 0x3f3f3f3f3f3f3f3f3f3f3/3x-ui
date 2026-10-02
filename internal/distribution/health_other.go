//go:build !linux

package distribution

import "errors"

func processIdentity(int, string) (string, error) {
	return "", errors.New("paired runtime activation checking currently requires Linux")
}
