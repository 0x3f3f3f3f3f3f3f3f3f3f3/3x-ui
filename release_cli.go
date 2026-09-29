package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/routedbridge"
	"github.com/mhsanaei/3x-ui/v3/internal/updatebundle"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func runReleaseCommand(args []string, out io.Writer) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "release-info":
		return true, writeReleaseInfo(args[1:], out)
	case "prepare-update":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return true, prepareInstalledUpdate(ctx, args[1:], out)
	case "verify-release":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return true, verifyRelease(ctx, args[1:], out)
	default:
		return false, nil
	}
}

func prepareInstalledUpdate(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 0 {
		return errors.New("prepare-update does not accept arguments")
	}
	if runtime.GOOS != "linux" {
		return errors.New("installed updater preparation requires Linux")
	}
	info, err := config.GetReleaseInfo()
	if err != nil {
		return err
	}
	if info.Modified || info.Commit == "" {
		return errors.New("updater requires an unmodified panel with a known release source")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	path, err := updatebundle.PrepareInstalledUpdater(ctx, filepath.Dir(executable), os.TempDir(), info.Commit, info.Platform)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, path); err != nil {
		return errors.Join(err, os.Remove(path))
	}
	return nil
}

func writeReleaseInfo(args []string, out io.Writer) error {
	if len(args) != 0 {
		return errors.New("release-info does not accept arguments")
	}
	info, err := config.GetReleaseInfo()
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(info)
}

func verifyRelease(ctx context.Context, args []string, out io.Writer) error {
	if runtime.GOOS != "linux" {
		return errors.New("release activation preflight requires Linux")
	}
	flags := flag.NewFlagSet("verify-release", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	directory := flags.String("directory", "", "staged x-ui directory")
	commit := flags.String("commit", "", "selected full source commit")
	tag := flags.String("tag", "", "selected release tag")
	platform := flags.String("platform", "", "selected release platform")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *directory == "" || *commit == "" || *tag == "" || *platform == "" || flags.NArg() != 0 {
		return errors.New("required: --directory DIRECTORY --commit SHA --tag TAG --platform PLATFORM")
	}
	info, err := config.GetReleaseInfo()
	if err != nil {
		return err
	}
	if info.Modified || info.Commit != *commit || info.Platform != *platform {
		return errors.New("compiled panel source/platform differs from the selected unmodified release")
	}
	identity := updatebundle.ReleaseIdentity{Repository: info.Repository, Commit: *commit, Tag: *tag, Platform: *platform}
	manifest, err := updatebundle.VerifyManifest(ctx, *directory, identity)
	if err != nil {
		return err
	}
	if manifest.PolicyABI != info.PolicyABI || manifest.RoutingABI != info.RoutingABI {
		return errors.New("compiled panel policy/routing ABI differs from the release manifest")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	self, err := os.Stat(executable)
	if err != nil {
		return err
	}
	candidate, err := os.Stat(filepath.Join(*directory, "x-ui"))
	if err != nil {
		return err
	}
	if !os.SameFile(self, candidate) {
		return errors.New("verify-release must execute the panel inside the staged bundle")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := probeReleaseCore(ctx, *directory); err != nil {
		return fmt.Errorf("managed routing verification failed: %w", err)
	}
	_, err = fmt.Fprintln(out, "managed routing verified")
	return err
}

// probeReleaseCore runs only in the standalone verification command, before service initialization.
func probeReleaseCore(ctx context.Context, directory string) (resultErr error) {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	temporary, err := os.MkdirTemp("", ".3x-ui-release-probe-")
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, os.RemoveAll(temporary)) }()
	if err := os.Setenv("XUI_BIN_FOLDER", filepath.Join(directory, "bin")); err != nil {
		return err
	}
	if err := os.Setenv("XUI_LOG_FOLDER", filepath.Join(temporary, "log")); err != nil {
		return err
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	address, err := netip.ParseAddrPort(listener.Addr().String())
	if err != nil {
		return err
	}
	bridge, err := routedbridge.NewManaged("release-preflight", address, []routedbridge.ClientBinding{{PolicyID: uuid.NewString(), Email: "release-preflight"}})
	if err != nil {
		return err
	}
	cfg := &xray.Config{
		LogConfig:       []byte(`{"loglevel":"none"}`),
		OutboundConfigs: []byte(`[{"tag":"deny","protocol":"blackhole"}]`),
	}
	if err := bridge.Apply(cfg); err != nil {
		return err
	}
	if err := listener.Close(); err != nil {
		return err
	}
	process := xray.NewTestProcess(cfg, filepath.Join(temporary, "config.json"))
	defer func() {
		if process.IsRunning() {
			resultErr = errors.Join(resultErr, process.Stop())
		}
	}()
	if err := process.Start(); err != nil {
		return err
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !process.IsRunning() {
			return fmt.Errorf("probe core exited: %s", process.GetResult())
		}
		conn, err := (&net.Dialer{Timeout: 50 * time.Millisecond}).DialContext(ctx, "tcp4", address.String())
		if err == nil {
			conn.Close()
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	if err := bridge.Check(ctx); err != nil {
		return err
	}
	if !process.IsRunning() {
		return errors.New("probe core exited after authentication")
	}
	return nil
}
