// Command update-stage validates and stages a downloaded release archive.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/updatebundle"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout)
	stop()
	if errors.Is(err, flag.ErrHelp) {
		fmt.Println("usage: update-stage --parent DIRECTORY [--preflight] (--download --release-platform PLATFORM [--release-tag TAG] | --archive FILE --sha256 HEX [--release-commit SHA --release-tag TAG --release-platform PLATFORM])")
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	return runWithClient(ctx, args, out, &http.Client{Timeout: 5 * time.Minute})
}

func runWithClient(ctx context.Context, args []string, out io.Writer, client *http.Client) error {
	flags := flag.NewFlagSet("update-stage", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	archive := flags.String("archive", "", "downloaded archive")
	sum := flags.String("sha256", "", "expected compressed archive SHA256")
	parent := flags.String("parent", "", "existing staging parent directory")
	commit := flags.String("release-commit", "", "expected full release commit")
	tag := flags.String("release-tag", "", "expected release tag")
	platform := flags.String("release-platform", "", "expected Linux release platform")
	download := flags.Bool("download", false, "select and download a verified release from the managed fork")
	preflight := flags.Bool("preflight", false, "run the verified candidate's managed core preflight")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if runtime.GOOS != "linux" {
		return errors.New("the release staging command requires Linux")
	}
	if *parent == "" || flags.NArg() != 0 {
		return errors.New("required: --parent DIRECTORY and no positional arguments")
	}
	if *download {
		if *archive != "" || *sum != "" || *commit != "" || *platform == "" {
			return errors.New("download requires --release-platform and excludes --archive, --sha256 and --release-commit")
		}
		stage, identity, err := updatebundle.DownloadRelease(ctx, client, *tag, *platform, *parent)
		if err != nil {
			return err
		}
		return publishStage(ctx, out, stage, identity, *preflight)
	}
	if *archive == "" || *sum == "" {
		return errors.New("required: --archive FILE --sha256 HEX --parent DIRECTORY")
	}
	verifyRelease := *commit != "" || *tag != "" || *platform != ""
	if verifyRelease && (*commit == "" || *tag == "" || *platform == "") {
		return errors.New("release verification requires commit, tag and platform together")
	}
	if *preflight && !verifyRelease {
		return errors.New("preflight requires a verified release identity")
	}
	f, err := openArchive(*archive)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("archive must be a regular file")
	}
	stage, err := updatebundle.Stage(ctx, f, *parent, *sum)
	if err != nil {
		return err
	}
	identity := updatebundle.ReleaseIdentity{Repository: updatebundle.ReleaseRepository, Commit: *commit, Tag: *tag, Platform: *platform}
	if verifyRelease {
		if _, err := updatebundle.VerifyManifest(ctx, filepath.Join(stage, "x-ui"), identity); err != nil {
			return errors.Join(err, os.RemoveAll(stage))
		}
	}
	return publishStage(ctx, out, stage, identity, *preflight)
}

func publishStage(ctx context.Context, out io.Writer, stage string, identity updatebundle.ReleaseIdentity, preflight bool) error {
	if preflight {
		if err := verifyCandidate(ctx, stage, identity); err != nil {
			return errors.Join(err, os.RemoveAll(stage))
		}
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, os.RemoveAll(stage))
	}
	if _, err := fmt.Fprintln(out, stage); err != nil {
		return errors.Join(err, os.RemoveAll(stage))
	}
	return nil
}
