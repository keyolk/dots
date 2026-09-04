// Package cli implements dots's command-line interface.
package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/keyolk/dots/internal/manifest"
)

// Global flags shared by every command.
var (
	flagManifest string
	flagJSON     bool
	flagDryRun   bool
)

// Version is set at build time via -ldflags.
var Version = "dev"

// ExecuteContext runs the root command with a cancellable context.
func ExecuteContext(ctx context.Context) error {
	return newRootCmd().ExecuteContext(ctx)
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "dots",
		Short: "Declarative dotfile, secret and package management",
		Long: `dots manages a machine from one declarative manifest.

A dotfile manifest says which paths belong under version control, as globs
rather than as a list, so a newly written hook or skill is reported the moment
it appears instead of staying invisible until someone remembers to add it.

Secrets live in an age-encrypted vault. A config file that carries one secret
field is committed as a template with a {{ secret "name" }} call where the value
goes, so the file stays reviewable and the value never reaches git.

Packages are declared per source - brew, cargo, bun, go, mise, krew, apt - plus
the binaries that arrive by curl and belong to no package manager at all. dots
reports what is declared but missing and what is installed but undeclared.

Storage stays the two bare repos over $HOME that the config and secret shell
aliases already use, so adopting dots does not invalidate them.`,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	pf := root.PersistentFlags()
	pf.StringVarP(&flagManifest, "manifest", "m", "", "path to dots.toml (default ~/.config/dots/dots.toml)")
	pf.BoolVar(&flagJSON, "json", false, "emit JSON instead of text")

	root.AddCommand(
		newStatusCmd(),
		newAddCmd(),
		newApplyCmd(),
		newSaveCmd(),
		newPullCmd(),
		newPruneCmd(),
		newSecretCmd(),
		newPkgCmd(),
		newConfigCmd(),
		newDoctorCmd(),
		newInitCmd(),
	)
	return root
}

// load reads the manifest, with a message that says how to fix its absence
// rather than only that it is absent.
func load() (*manifest.Manifest, error) {
	m, err := manifest.Load(flagManifest)
	if err != nil {
		if os.IsNotExist(err) || os.IsNotExist(unwrap(err)) {
			return nil, fmt.Errorf("no manifest at %s - run `dots init` to create one",
				manifest.DefaultPath())
		}
		return nil, err
	}
	return m, nil
}

func unwrap(err error) error {
	type unwrapper interface{ Unwrap() error }
	if u, ok := err.(unwrapper); ok {
		return u.Unwrap()
	}
	return err
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}
