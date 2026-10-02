package distribution

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"runtime/debug"
	"strings"
)

// PanelSourceRevision is injected independently of the panel's release channel.
// An unstamped engineering binary can report info but cannot pass package verification.
var PanelSourceRevision string

func CurrentTarget() Target {
	target := Target{OS: runtime.GOOS, Arch: runtime.GOARCH}
	if runtime.GOARCH == "arm" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, s := range info.Settings {
				if s.Key == "GOARM" {
					target.ARM = strings.Split(s.Value, ",")[0]
				}
			}
		}
	}
	return target
}

// RunCommand never loads service environment files, opens a database or starts
// a core instance. main dispatches it before the regular service CLI setup.
func RunCommand(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 1 && args[0] == "info" {
		return json.NewEncoder(output).Encode(BinaryReport{FormatVersion: 1, APIVersion: 1, Compatibility: "traffic-control-v1", SourceRevision: PanelSourceRevision, Target: CurrentTarget(), GoVersion: runtime.Version()})
	}
	if len(args) == 2 && args[0] == "verify" {
		m, err := Verify(ctx, args[1])
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(m)
	}
	if len(args) == 3 && args[0] == "stage" {
		root, err := ExtractArchive(ctx, args[1], args[2])
		if err != nil {
			return err
		}
		m, err := Verify(ctx, root)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(m)
	}
	if len(args) == 4 && args[0] == "promote" {
		p, err := Promote(ctx, args[1], args[2], args[3])
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(p)
	}
	return errors.New("usage: x-ui package info | x-ui package verify DIRECTORY | x-ui package stage ARCHIVE NEW_DIRECTORY | x-ui package promote CANDIDATE INSTALLED PREVIOUS")
}
