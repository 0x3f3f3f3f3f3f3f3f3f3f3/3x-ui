// Command release-manifest records source identity and hashes for a Linux bundle.
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
		fmt.Println("usage: release-manifest --directory DIRECTORY --commit SHA --tag TAG --platform PLATFORM")
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("release-manifest", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	directory := flags.String("directory", "", "assembled x-ui bundle directory")
	commit := flags.String("commit", "", "full source commit")
	tag := flags.String("tag", "", "release tag")
	platform := flags.String("platform", "", "Linux release platform")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *directory == "" || flags.NArg() != 0 {
		return errors.New("required: --directory DIRECTORY --commit SHA --tag TAG --platform PLATFORM")
	}
	identity := updatebundle.ReleaseIdentity{Repository: updatebundle.ReleaseRepository, Commit: *commit, Tag: *tag, Platform: *platform}
	manifest, err := updatebundle.BuildManifest(ctx, *directory, identity)
	if err != nil {
		return err
	}
	if err := updatebundle.WriteManifest(*directory, manifest); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, filepath.Join(*directory, updatebundle.ManifestName))
	return err
}
