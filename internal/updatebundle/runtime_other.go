//go:build !linux

package updatebundle

import (
	"context"
	"errors"
	"io"
)

func PreflightRelease(context.Context, string, ReleaseIdentity, io.Writer) error {
	return errors.New("release activation preflight requires Linux")
}
