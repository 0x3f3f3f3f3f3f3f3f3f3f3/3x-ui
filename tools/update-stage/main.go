// Command update-stage validates and stages a downloaded release archive.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/mhsanaei/3x-ui/v3/internal/updatebundle"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout)
	stop()
	if errors.Is(err, flag.ErrHelp) {
		fmt.Println("usage: update-stage --archive FILE --sha256 HEX --parent DIRECTORY [--release-commit SHA --release-tag TAG --release-platform PLATFORM]")
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("update-stage", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	archive := flags.String("archive", "", "downloaded archive")
	sum := flags.String("sha256", "", "expected compressed archive SHA256")
	parent := flags.String("parent", "", "existing staging parent directory")
	commit := flags.String("release-commit", "", "expected full release commit")
	tag := flags.String("release-tag", "", "expected release tag")
	platform := flags.String("release-platform", "", "expected Linux release platform")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *archive == "" || *sum == "" || *parent == "" || flags.NArg() != 0 {
		return errors.New("required: --archive FILE --sha256 HEX --parent DIRECTORY")
	}
	verifyRelease := *commit != "" || *tag != "" || *platform != ""
	if verifyRelease && (*commit == "" || *tag == "" || *platform == "") {
		return errors.New("release verification requires commit, tag and platform together")
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
	if verifyRelease {
		identity := updatebundle.ReleaseIdentity{Repository: updatebundle.ReleaseRepository, Commit: *commit, Tag: *tag, Platform: *platform}
		if _, err := updatebundle.VerifyManifest(ctx, filepath.Join(stage, "x-ui"), identity); err != nil {
			return errors.Join(err, os.RemoveAll(stage))
		}
	}
	if _, err := fmt.Fprintln(out, stage); err != nil {
		return errors.Join(err, os.RemoveAll(stage))
	}
	return nil
}
