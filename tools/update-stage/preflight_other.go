//go:build !linux

package main

import (
	"context"
	"errors"

	"github.com/mhsanaei/3x-ui/v3/internal/updatebundle"
)

func verifyCandidate(context.Context, string, updatebundle.ReleaseIdentity) error {
	return errors.New("release activation preflight requires Linux")
}
