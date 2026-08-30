// Command dots manages dotfiles, secrets and packages from one manifest.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/keyolk/dots/internal/cli"
)

// version is set at build time via -ldflags.
var version = "dev"

func main() {
	cli.Version = version

	// Ctrl+C during a package sync or a vault write should unwind rather than
	// leave a half-installed source or a truncated ciphertext.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cli.ExecuteContext(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "dots: "+err.Error())
		os.Exit(1)
	}
}
