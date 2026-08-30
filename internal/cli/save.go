package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/keyolk/dots/internal/dotfile"
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

			byStore := map[string][]string{}
			var blocked int
			for _, e := range entries {
				switch e.State {
				case dotfile.Modified, dotfile.Untracked:
				default:
					continue
				}
				if e.Store == "config" || e.Store == "" {
					abs := m.Store.WorkTree + "/" + e.Path
					if hit := scanSecrets(abs); hit != "" {
						fmt.Fprintf(os.Stderr, "refused %s: %s\n", e.Path, hit)
						blocked++
						continue
					}
				}
				store := e.Store
				if store == "" {
					store = "config"
				}
				byStore[store] = append(byStore[store], e.Path)
			}

			// Paths gone from disk still need their deletion recorded, or the
			// store keeps resurrecting them on the next machine.
			for _, e := range entries {
				if e.State == dotfile.Missing {
					byStore[e.Store] = append(byStore[e.Store], e.Path)
				}
			}

			if len(byStore) == 0 {
				fmt.Println("nothing to save")
				return blockedErr(blocked)
			}
			if message == "" {
				message = "dots: sync"
			}

			for store, paths := range byStore {
				repo := sc.Repo(store)
				// `git add -A` on the listed paths records modifications,
				// additions and deletions in one call.
				args := append([]string{"add", "-A", "--"}, paths...)
				if _, err := repo.Run(args...); err != nil {
					return err
				}
				ok, err := repo.Commit(message)
				if err != nil {
					return err
				}
				if !ok {
					continue
				}
				fmt.Printf("%s: committed %d path(s)\n", store, len(paths))

				if push {
					if err := repo.RunInteractive("push"); err != nil {
						return fmt.Errorf("%s: push: %w", store, err)
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
