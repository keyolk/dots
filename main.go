// Command dots manages dotfiles, secrets and packages from one manifest.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/keyolk/dots/internal/cli"
)

// version is set at build time via -ldflags. A binary produced by `go install
// module@version` gets no ldflags, so the module version recorded in the
// build info is used instead -- otherwise every installed copy reports "dev".
var version = "dev"

func resolveVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}

func main() {
	cli.Version = resolveVersion()

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
