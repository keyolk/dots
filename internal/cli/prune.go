package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/keyolk/dots/internal/dotfile"
)

func newPruneCmd() *cobra.Command {
	var (
		commit bool
		yes    bool
		only   string
	)

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Untrack files the manifest no longer declares",
		Long: `prune removes undeclared paths from their store without deleting them from
disk.

An undeclared path is one the store still tracks but no manifest group claims:
churning state that was committed once and now shows up as modified forever,
generated files, or leftovers from an earlier layout. On the machine this was
built for that was 2110 watchman cookies under .spin and a .gnupg/random_seed
that gpg rewrites on every invocation.

Files are only untracked, never removed from the filesystem. Re-declaring a
path in the manifest and running dots add puts it back.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := load()
			if err != nil {
				return err
			}
			sc := dotfile.NewScanner(m, hostname())
			entries, err := sc.Scan()
			if err != nil {
				return err
			}

			byStore := map[string][]string{}
			for _, e := range entries {
				if e.State != dotfile.Undeclared {
					continue
				}
				// The two stores hold very different risk: config accumulates
				// generated junk, while secret holds material that is merely
				// not declared yet. Pruning them separately is the norm.
				if only != "" && e.Store != only {
					continue
				}
				byStore[e.Store] = append(byStore[e.Store], e.Path)
			}
			if len(byStore) == 0 {
				if only != "" {
					fmt.Printf("nothing to prune in the %s store\n", only)
					return nil
				}
				fmt.Println("nothing to prune - every tracked path is declared")
				return nil
			}

			total := 0
			for store, paths := range byStore {
				total += len(paths)
				fmt.Printf("\n%s: %d undeclared path(s)\n", store, len(paths))
				for i, p := range paths {
					if i == 15 {
						fmt.Printf("  … %d more\n", len(paths)-15)
						break
					}
					fmt.Printf("  - %s\n", p)
				}
			}

			if flagDryRun {
				fmt.Printf("\nwould untrack %d path(s); files stay on disk\n", total)
				return nil
			}
			// Untracking is recorded in git history and is recoverable, but it
			// still rewrites the index for thousands of paths, so it is not
			// something to do on a stray keypress.
			if !yes && !confirm(fmt.Sprintf("\nuntrack %d path(s)? files stay on disk [y/N] ", total)) {
				fmt.Println("aborted")
				return nil
			}

			for store, paths := range byStore {
				repo := sc.Repo(store)
				// git rm takes the paths as arguments, and a store with
				// thousands of undeclared entries would overflow ARG_MAX in one
				// call.
				for _, batch := range chunk(paths, 500) {
					if err := repo.Remove(batch...); err != nil {
						return err
					}
				}
				fmt.Printf("%s: untracked %d path(s)\n", store, len(paths))

				if !commit {
					continue
				}
				ok, err := repo.Commit(fmt.Sprintf("dots: untrack %d undeclared path(s)", len(paths)))
				if err != nil {
					return err
				}
				if ok {
					fmt.Printf("%s: committed\n", store)
				}
			}

			if !commit {
				fmt.Println("\nstaged only - run `dots save` to commit")
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&flagDryRun, "dry-run", "n", false, "list what would be untracked without doing it")
	cmd.Flags().BoolVar(&commit, "commit", false, "commit immediately instead of leaving changes staged")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	cmd.Flags().StringVar(&only, "store", "", "restrict to one store: config or secret")
	return cmd
}

// confirm asks for a yes on the terminal. A non-interactive stdin answers no,
// so a piped or scripted invocation cannot untrack thousands of paths by
// accident; such callers pass --yes deliberately.
func confirm(prompt string) bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	fmt.Print(prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

func chunk(xs []string, size int) [][]string {
	var out [][]string
	for len(xs) > size {
		out = append(out, xs[:size])
		xs = xs[size:]
	}
	if len(xs) > 0 {
		out = append(out, xs)
	}
	return out
}
