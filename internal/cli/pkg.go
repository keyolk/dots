package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/keyolk/dots/internal/pkgmgr"
	"github.com/keyolk/dots/internal/ui"
)

func newPkgCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pkg",
		Short: "Reconcile installed packages against the manifest",
		Long: `pkg compares the declared package set against what each source reports as
installed, across brew, cask, cargo, bun, go, mise, krew and apt - plus the
binaries that arrive by curl and belong to no source at all.`,
	}
	cmd.AddCommand(newPkgDiffCmd(), newPkgSyncCmd(), newPkgAdoptCmd(), newPkgBinCmd())
	return cmd
}

func newPkgDiffCmd() *cobra.Command {
	var showExtra bool

	cmd := &cobra.Command{
		Use:     "diff",
		Aliases: []string{"status"},
		Short:   "Show declared-but-missing and installed-but-undeclared packages",
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := load()
			if err != nil {
				return err
			}
			diffs := pkgmgr.Reconcile(m)
			if len(diffs) == 0 {
				fmt.Println("no package sources declared")
				return nil
			}

			var totalMissing, totalExtra int
			for _, d := range diffs {
				if !d.Available {
					fmt.Printf("%s %s\n", ui.Heading.Width(8).Render(d.Source),
						ui.Muted.Render("unavailable on this machine"))
					continue
				}
				totalMissing += len(d.Missing)
				totalExtra += len(d.Extra)

				fmt.Printf("%s %s", ui.Heading.Width(8).Render(d.Source),
					ui.OK.Render(fmt.Sprintf("%d managed", d.Managed)))
				if len(d.Missing) > 0 {
					fmt.Printf(", %s", ui.StateMissing.Render(fmt.Sprintf("%d missing", len(d.Missing))))
				}
				if len(d.Extra) > 0 {
					fmt.Printf(", %s", ui.StateUndeclared.Render(fmt.Sprintf("%d undeclared", len(d.Extra))))
				}
				fmt.Println()

				for _, p := range d.Missing {
					fmt.Printf("    %s %s\n", ui.StateMissing.Render("-"), p)
				}
				if showExtra {
					for _, p := range d.Extra {
						fmt.Printf("    %s %s\n", ui.StateUndeclared.Render("+"), p)
					}
				}
			}

			fmt.Println()
			if totalMissing > 0 {
				fmt.Printf("%d package(s) missing - run `dots pkg sync`\n", totalMissing)
			}
			if totalExtra > 0 && !showExtra {
				fmt.Printf("%d package(s) installed but undeclared - see `dots pkg diff --extra`, adopt with `dots pkg adopt`\n", totalExtra)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&showExtra, "extra", "e", false, "list installed-but-undeclared packages")
	return cmd
}

func newPkgSyncCmd() *cobra.Command {
	var only string

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Install every declared package that is missing",
		Long: `sync installs what the manifest declares and the machine lacks.

It never removes anything. An installed-but-undeclared package is a fact to
reconcile deliberately, not something a sync should delete out from under a
running system.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := load()
			if err != nil {
				return err
			}

			var installed, failed int
			for _, d := range pkgmgr.Reconcile(m) {
				if !d.Available || len(d.Missing) == 0 {
					continue
				}
				if only != "" && d.Source != only {
					continue
				}
				for _, pkg := range d.Missing {
					argv := pkgmgr.InstallCmd(d.Source, m.Packages[d.Source], pkg)
					if argv == nil {
						fmt.Fprintf(os.Stderr, "%s: no install command for %s\n", d.Source, pkg)
						failed++
						continue
					}
					if flagDryRun {
						fmt.Printf("would run: %s\n", strings.Join(argv, " "))
						continue
					}
					fmt.Printf("==> %s\n", strings.Join(argv, " "))
					c := exec.Command(argv[0], argv[1:]...)
					c.Stdout, c.Stderr = os.Stdout, os.Stderr
					if err := c.Run(); err != nil {
						fmt.Fprintf(os.Stderr, "%s: %s failed: %v\n", d.Source, pkg, err)
						failed++
						continue
					}
					installed++
				}
			}

			fmt.Printf("\ninstalled %d package(s)\n", installed)
			if failed > 0 {
				return fmt.Errorf("%d package(s) failed", failed)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&flagDryRun, "dry-run", "n", false, "print the install commands without running them")
	cmd.Flags().StringVar(&only, "source", "", "restrict to one source")
	return cmd
}

func newPkgAdoptCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "adopt [source...]",
		Short: "Print manifest entries for installed-but-undeclared packages",
		Long: `adopt emits the TOML to paste into the manifest for everything currently
installed that is not yet declared.

It prints rather than edits: 371 undeclared packages on a long-lived machine is
not a list to accept blindly, and the point of the manifest is that its contents
were chosen.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := load()
			if err != nil {
				return err
			}
			want := map[string]bool{}
			for _, a := range args {
				want[a] = true
			}

			for _, d := range pkgmgr.Reconcile(m) {
				if !d.Available || len(d.Extra) == 0 {
					continue
				}
				if len(want) > 0 && !want[d.Source] {
					continue
				}
				fmt.Printf("\n[packages.%s]\npackages = [\n", d.Source)
				for _, p := range d.Extra {
					fmt.Printf("  %q,\n", p)
				}
				fmt.Println("]")
			}
			return nil
		},
	}
}

func newPkgBinCmd() *cobra.Command {
	var dir string

	cmd := &cobra.Command{
		Use:   "bin",
		Short: "Check declared binaries and find undeclared ones",
		Long: `bin covers the executables no package manager can account for: the ones
that arrived from a curl one-liner or a release tarball.

A declared binary records the command that reinstalls it, so a fresh machine
reproduces it instead of leaving a file whose origin nobody remembers.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := load()
			if err != nil {
				return err
			}

			states := pkgmgr.CheckBinaries(m)
			var missing int
			if len(states) > 0 {
				fmt.Printf("declared binaries (%d)\n", len(states))
				for _, s := range states {
					mark := ui.OK.Width(8).Render("ok")
					if !s.Present {
						mark = ui.Fail.Width(8).Render("MISSING")
						missing++
					}
					fmt.Printf("  %s %-20s %s\n", mark, s.Name, ui.Muted.Render(s.Version))
					if !s.Present && s.Install != "" {
						fmt.Println(ui.Fix.Render("           install: " + s.Install))
					}
				}
			}

			if dir == "" {
				home, _ := os.UserHomeDir()
				dir = filepath.Join(home, ".local", "bin")
			}
			undeclared, err := pkgmgr.UndeclaredBinaries(m, dir)
			if err != nil {
				return err
			}
			if len(undeclared) > 0 {
				fmt.Printf("\nundeclared in %s (%d)\n", dir, len(undeclared))
				for _, u := range undeclared {
					fmt.Printf("  %s %s\n", ui.StateUntracked.Render("?"), u)
				}
				fmt.Println("\ndeclare each with a [[packages.<source>.binaries]] entry recording where it came from")
			}

			if missing > 0 {
				return fmt.Errorf("%d declared binary/binaries missing", missing)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "directory to scan (default ~/.local/bin)")
	return cmd
}
