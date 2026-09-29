//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"

	"github.com/mhsanaei/3x-ui/v3/internal/updatebundle"
)

func verifyCandidate(ctx context.Context, stage string, identity updatebundle.ReleaseIdentity) error {
	return updatebundle.PreflightRelease(ctx, filepath.Join(stage, "x-ui"), identity, os.Stderr)
}
