//go:build !linux

package main

import (
	"errors"
	"os"
)

func openArchive(string) (*os.File, error) {
	return nil, errors.New("the release staging command requires Linux")
}
