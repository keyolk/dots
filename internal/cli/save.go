package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/keyolk/dots/internal/dotfile"
	"github.com/keyolk/dots/internal/ui"
)

func newSaveCmd() *cobra.Command {
	var (
		message string
		push    bool
	)

	cmd := &cobra.Command{
		Use:   "save [message]",
		Short: "Stage and commit every declared change in both stores",
		Long: `save commits the modified and untracked files the manifest declares, to
whichever store owns them, in one step.

Files carrying credential shapes are refused before staging, the same check
add applies.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				message = strings.Join(args, " ")
			}
			m, err := load()
			if err != nil {
				return err
			}
			sc := dotfile.NewScanner(m, hostname())
			entries, err := sc.Scan()
			if err != nil {
				return err
			}

			var paths []string
			var blocked int
			for _, e := range entries {
				switch e.State {
				case dotfile.Modified, dotfile.Untracked:
				default:
					continue
				}
				// A secret-group file holds credentials by design, so the scan
				// would only refuse the files that most need tracking.
				if e.Store != "secret" {
					abs := filepath.Join(m.Store.WorkTree, e.Path)
					if hit := scanSecrets(abs); hit != "" {
						fmt.Fprintf(os.Stderr, "%s %s: %s\n",
							ui.Refused.Render("refused"), e.Path, ui.Muted.Render(hit))
						blocked++
						continue
					}
				}
				paths = append(paths, e.Path)
			}

			// Paths gone from disk still need their deletion recorded, or the
			// store keeps resurrecting them on the next machine.
			for _, e := range entries {
				if e.State == dotfile.Missing {
					paths = append(paths, e.Path)
				}
			}

			if len(paths) == 0 {
				fmt.Println("nothing to save")
				return blockedErr(blocked)
			}
			if message == "" {
				message = "dots: sync"
			}

			repo := sc.Repo()
			// `git add -A` on the listed paths records modifications, additions
			// and deletions in one call.
			addArgs := append([]string{"add", "-A", "--"}, paths...)
			if _, err := repo.Run(addArgs...); err != nil {
				return err
			}
			ok, err := repo.Commit(message)
			if err != nil {
				return err
			}
			if ok {
				fmt.Printf("%s %d path(s)\n", ui.OK.Render("committed"), len(paths))
				if push {
					if err := repo.RunInteractive("push"); err != nil {
						return fmt.Errorf("push: %w", err)
					}
				}
			}
			return blockedErr(blocked)
		},
	}

	cmd.Flags().StringVarP(&message, "message", "M", "", "commit message")
	cmd.Flags().BoolVar(&push, "push", false, "push after committing")
	return cmd
}

func blockedErr(n int) error {
	if n > 0 {
		return fmt.Errorf("%d file(s) refused for credential content", n)
	}
	return nil
}
