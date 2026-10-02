// packageverify is a static installation helper. It is a short-lived CLI, not a
// runtime/data-plane service, and needs neither Go nor Python on the destination.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mhsanaei/3x-ui/v3/internal/distribution"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := distribution.RunCommand(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
