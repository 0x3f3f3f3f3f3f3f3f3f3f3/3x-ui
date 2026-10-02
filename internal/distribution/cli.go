package distribution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
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
	if len(args) == 3 && args[0] == "health" {
		return WaitRuntimeHealth(ctx, args[1], args[2])
	}
	if len(args) >= 3 && args[0] == "with-lock" {
		return RunLocked(ctx, args[1], args[2:], output)
	}
	if len(args) == 1 && args[0] == "info" {
		return json.NewEncoder(output).Encode(BinaryReport{FormatVersion: 1, APIVersion: 1, Compatibility: "traffic-control-v1", SourceRevision: PanelSourceRevision, Target: CurrentTarget(), GoVersion: runtime.Version()})
	}
	if len(args) == 2 && (args[0] == "verify" || args[0] == "verify-incoming") {
		verify := Verify
		if args[0] == "verify-incoming" {
			verify = VerifyIncoming
		}
		m, err := verify(ctx, args[1])
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(m)
	}
	if len(args) == 2 && args[0] == "recovery-work" {
		work, err := RecoveryWork(args[1])
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, work)
		return err
	}
	if len(args) == 2 && args[0] == "recovery-previous" {
		if _, err := RecoveryWork(args[1]); err != nil {
			return err
		}
		installed, err := filepath.Abs(args[1])
		if err != nil {
			return err
		}
		p, err := readPending(installed)
		if err != nil || p == nil {
			return err
		}
		_, err = fmt.Fprintln(output, p.Previous)
		return err
	}
	if len(args) == 3 && args[0] == "stage" {
		root, err := ExtractArchive(ctx, args[1], args[2])
		if err != nil {
			return err
		}
		m, err := VerifyIncoming(ctx, root)
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
	if len(args) == 3 && args[0] == "rollback" {
		p, err := Rollback(ctx, args[1], args[2])
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(p)
	}
	if len(args) == 3 && args[0] == "complete" {
		p, err := CompletePromotion(ctx, args[1], args[2])
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(p)
	}
	if len(args) == 2 && (args[0] == "recover" || args[0] == "complete-offline") {
		var p *Promotion
		var err error
		if args[0] == "recover" {
			p, err = Recover(ctx, args[1])
		} else {
			p, err = CompleteOfflinePromotion(ctx, args[1])
		}
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(p)
	}
	return errors.New("usage: x-ui package info | verify DIRECTORY | verify-incoming DIRECTORY | stage ARCHIVE NEW_DIRECTORY | promote CANDIDATE INSTALLED PREVIOUS | recovery-work INSTALLED | recovery-previous INSTALLED | recover INSTALLED | health INSTALLED HEALTH_FILE | complete INSTALLED HEALTH_FILE | complete-offline INSTALLED | rollback INSTALLED FAILED | with-lock INSTALLED COMMAND [ARGS...]")
}
