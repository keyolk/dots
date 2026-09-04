package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/keyolk/dots/internal/dotfile"
	"github.com/keyolk/dots/internal/ui"
)

func newPullCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Bring in what another machine pushed",
		Long: `pull fetches the store and applies it to $HOME.

This is the half of the cycle that save does not cover: save sends this
machine's changes out, pull brings other machines' changes in. Running it at
the start of a session is what keeps two machines from editing the same file
from different starting points.

Local edits are protected. If a tracked file has uncommitted changes, pull
stops and says which -- committing them first is almost always what you want,
since the alternative is losing them. --force discards them instead.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := load()
			if err != nil {
				return err
			}
			sc := dotfile.NewScanner(m, hostname())
			repo := sc.Repo()
			if !repo.Exists() {
				return fmt.Errorf("no store at %s", m.Store.Config)
			}

			// Uncommitted work is checked before fetching: a pull that is
			// going to refuse should not have changed anything first.
			if !force {
				entries, err := sc.Scan()
				if err != nil {
					return err
				}
				var dirty []string
				for _, e := range entries {
					if e.State == dotfile.Modified {
						dirty = append(dirty, e.Path)
					}
				}
				if len(dirty) > 0 {
					fmt.Fprintf(os.Stderr, "%s\n",
						ui.Warn.Render(fmt.Sprintf(
							"%d tracked file(s) have uncommitted changes:", len(dirty))))
					for i, p := range dirty {
						if i == 10 {
							fmt.Fprintln(os.Stderr,
								ui.Muted.Render(fmt.Sprintf("  … %d more", len(dirty)-10)))
							break
						}
						fmt.Fprintf(os.Stderr, "  %s %s\n", ui.StateModified.Render("M"), p)
					}
					return fmt.Errorf("commit them with `dots save`, or pull --force to discard")
				}
			}

			// --force promises to discard local edits, so it has to actually
			// do it: git merge refuses to overwrite a dirty work tree no
			// matter what this command intended.
			if force {
				if _, err := repo.Run("checkout", "--", "."); err != nil {
					return fmt.Errorf("discarding local changes: %w", err)
				}
			}

			before, err := repo.Run("rev-parse", "HEAD")
			if err != nil {
				return err
			}

			fmt.Println(ui.Muted.Render("fetching…"))
			if _, err := repo.Run("fetch", "origin"); err != nil {
				return err
			}

			branch, err := repo.Run("rev-parse", "--abbrev-ref", "HEAD")
			if err != nil {
				return err
			}
			branch = strings.TrimSpace(branch)
			upstream := "origin/" + branch

			incoming, err := repo.Run("rev-list", "--count", branch+".."+upstream)
			if err != nil {
				return fmt.Errorf("no %s to pull from; is the remote set? see `dots config`", upstream)
			}
			if strings.TrimSpace(incoming) == "0" {
				fmt.Println("already up to date")
				return nil
			}

			// A merge would need a commit message and could conflict; a
			// fast-forward is the only shape that makes sense for a store one
			// machine writes at a time. Anything else is a real divergence and
			// should be looked at, not merged blind.
			if _, err := repo.Run("merge", "--ff-only", upstream); err != nil {
				return fmt.Errorf(
					"local and %s have diverged; resolve it with git: %w", upstream, err)
			}

			if _, err := repo.Checkout(); err != nil {
				return fmt.Errorf("fetched, but the checkout failed: %w", err)
			}

			after, _ := repo.Run("rev-parse", "HEAD")
			changed, _ := repo.Run("diff", "--name-only",
				strings.TrimSpace(before), strings.TrimSpace(after))

			var files []string
			for _, l := range strings.Split(changed, "\n") {
				if l = strings.TrimSpace(l); l != "" {
					files = append(files, l)
				}
			}
			fmt.Printf("%s %s commit(s), %d file(s)\n",
				ui.OK.Render("pulled"), strings.TrimSpace(incoming), len(files))
			for i, f := range files {
				if i == 20 {
					fmt.Println(ui.Muted.Render(fmt.Sprintf("  … %d more", len(files)-20)))
					break
				}
				fmt.Printf("  %s\n", f)
			}

			// A template whose secret changed upstream renders differently
			// now, and the rendered file is not tracked -- so nothing else
			// would tell you it is stale.
			for _, f := range files {
				if strings.HasSuffix(f, ".tmpl") {
					fmt.Println(ui.Fix.Render("\na template changed — run `dots apply`"))
					break
				}
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false,
		"discard uncommitted changes to tracked files")
	return cmd
}
