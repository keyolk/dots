package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/keyolk/dots/internal/dotfile"
	"github.com/keyolk/dots/internal/git"
	"github.com/keyolk/dots/internal/ui"
)

func newPushCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Send this machine's commits to the remote",
		Long: `push sends the store's commits to origin.

save --push covers the common case, but only when it made a commit: commits
already sitting on the branch -- the ones doctor warns about -- have no way
out through save. This is that way out, and the other half of pull.

It fetches first, so the reasons a push would be rejected are named here
rather than coming back as a git error: a diverged branch is reported as
something to pull, not as a non-fast-forward. Declared files that were never
committed are reported too, since a push that leaves them behind looks
exactly like one that carried them.`,
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

			branch, err := repo.Run("rev-parse", "--abbrev-ref", "HEAD")
			if err != nil {
				return fmt.Errorf("the store has no commits yet: %w", err)
			}
			branch = strings.TrimSpace(branch)
			if branch == "HEAD" {
				return fmt.Errorf("the store is on a detached HEAD; check out a branch first")
			}
			if _, err := repo.Run("remote", "get-url", "origin"); err != nil {
				return fmt.Errorf(
					"the store has no origin remote; `dots config` shows where it points")
			}

			// Reported before anything else: "nothing to push" on a machine
			// holding uncommitted work is the misleading case, not the
			// informative one.
			if n := uncommitted(sc); n > 0 {
				fmt.Fprintf(os.Stderr, "%s %s\n",
					ui.Warn.Render(fmt.Sprintf("%d declared file(s) not committed", n)),
					ui.Muted.Render("- `dots save` to include them"))
			}

			fmt.Println(ui.Muted.Render("fetching…"))
			if _, err := repo.Run("fetch", "origin"); err != nil {
				return err
			}

			upstream := "origin/" + branch
			// A branch the remote has never seen has no upstream to subtract,
			// so every commit on it is outgoing and nothing can be incoming.
			first := false
			outgoing, err := count(repo, upstream+".."+branch)
			if err != nil {
				first = true
				if outgoing, err = count(repo, branch); err != nil {
					return err
				}
			}
			incoming := 0
			if !first {
				if incoming, err = count(repo, branch+".."+upstream); err != nil {
					return err
				}
			}

			switch {
			case outgoing > 0 && incoming > 0:
				return fmt.Errorf(
					"local and %s have diverged (%d ahead, %d behind); `dots pull` first",
					upstream, outgoing, incoming)
			case outgoing == 0 && incoming > 0:
				fmt.Printf("nothing to push; %s\n",
					ui.Fix.Render(fmt.Sprintf("%d commit(s) to pull - run `dots pull`", incoming)))
				return nil
			case outgoing == 0:
				fmt.Println("nothing to push")
				return nil
			}

			spec := upstream + ".." + branch
			if first {
				spec = branch
			}
			// --no-color because a `color.ui = always` in the user's gitconfig
			// colours even a pipe, and those codes interleave with the style
			// applied here into unreadable output.
			if log, err := repo.Run("log", "--oneline", "--no-decorate", "--no-color",
				"-n", "20", spec); err == nil {
				for _, l := range strings.Split(strings.TrimSpace(log), "\n") {
					if l != "" {
						fmt.Printf("  %s\n", ui.Muted.Render(l))
					}
				}
				if outgoing > 20 {
					fmt.Println(ui.Muted.Render(fmt.Sprintf("  … %d more", outgoing-20)))
				}
			}

			if flagDryRun {
				fmt.Printf("%s %d commit(s) to %s\n",
					ui.Muted.Render("would push"), outgoing, upstream)
				return nil
			}

			// -u on the first push, so doctor and config can compare against a
			// tracking ref from then on.
			pushArgs := []string{"push", "origin", branch}
			if first {
				pushArgs = []string{"push", "-u", "origin", branch}
			}
			if err := repo.RunInteractive(pushArgs...); err != nil {
				return fmt.Errorf("push: %w", err)
			}
			fmt.Printf("%s %d commit(s) to %s\n",
				ui.OK.Render("pushed"), outgoing, upstream)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&flagDryRun, "dry-run", "n", false,
		"list the commits that would be pushed without pushing")
	return cmd
}

// count returns how many commits the rev range holds.
func count(repo *git.Repo, spec string) (int, error) {
	out, err := repo.Run("rev-list", "--count", spec)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("counting %s: %w", spec, err)
	}
	return n, nil
}

// uncommitted counts declared paths a push would leave behind. A scan failure
// is reported as zero rather than as an error: it is an advisory, and failing
// the push over it would be worse than staying quiet.
func uncommitted(sc *dotfile.Scanner) int {
	entries, err := sc.Scan()
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		switch e.State {
		case dotfile.Modified, dotfile.Untracked, dotfile.Missing:
			n++
		}
	}
	return n
}
