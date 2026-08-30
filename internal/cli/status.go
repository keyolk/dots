package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/keyolk/dots/internal/dotfile"
)

func newStatusCmd() *cobra.Command {
	var (
		all      bool
		groupBy  string
		onlyKind string
	)

	cmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"st"},
		Short:   "Show how the machine differs from the manifest",
		Long: `status compares three views: what the manifest declares, what the git
stores track, and what is on disk.

  M  modified    tracked, and the working copy differs
  ?  untracked   declared and present on disk, but never committed
  D  missing     tracked, but gone from disk
  -  undeclared  tracked, but no manifest group claims it any more

The untracked column is the one a bare repo with status.showUntrackedFiles=no
cannot produce, and it is where new hooks, skills and scripts accumulate.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := load()
			if err != nil {
				return err
			}
			entries, err := dotfile.NewScanner(m, hostname()).Scan()
			if err != nil {
				return err
			}

			if !all {
				kept := entries[:0]
				for _, e := range entries {
					if e.State != dotfile.Clean {
						kept = append(kept, e)
					}
				}
				entries = kept
			}
			if onlyKind != "" {
				kept := entries[:0]
				for _, e := range entries {
					if e.State.String() == onlyKind {
						kept = append(kept, e)
					}
				}
				entries = kept
			}

			if flagJSON {
				return json.NewEncoder(os.Stdout).Encode(entries)
			}
			printStatus(entries, groupBy)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&all, "all", "a", false, "include clean entries")
	cmd.Flags().StringVarP(&groupBy, "group-by", "g", "state", "group output by: state, group, store, none")
	cmd.Flags().StringVar(&onlyKind, "only", "", "show only one state: modified, untracked, missing, undeclared")
	return cmd
}

func printStatus(entries []dotfile.Entry, groupBy string) {
	if len(entries) == 0 {
		fmt.Println("clean - manifest, stores and disk agree")
		return
	}

	if groupBy == "none" {
		for _, e := range entries {
			fmt.Printf(" %s %s\n", e.State.Symbol(), e.Path)
		}
		printSummary(entries)
		return
	}

	buckets := map[string][]dotfile.Entry{}
	for _, e := range entries {
		buckets[bucketKey(e, groupBy)] = append(buckets[bucketKey(e, groupBy)], e)
	}

	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		items := buckets[k]
		fmt.Printf("\n%s (%d)\n", k, len(items))
		// A group with hundreds of entries is a fact to report, not a wall of
		// text to print: the count is the actionable part, the paths are one
		// `--group-by none` away.
		limit := len(items)
		if limit > 20 {
			limit = 20
		}
		for _, e := range items[:limit] {
			fmt.Printf("  %s %s\n", e.State.Symbol(), e.Path)
		}
		if len(items) > limit {
			fmt.Printf("  … %d more\n", len(items)-limit)
		}
	}
	printSummary(entries)
}

func bucketKey(e dotfile.Entry, groupBy string) string {
	switch groupBy {
	case "group":
		if e.Group == "" {
			return "(no group)"
		}
		return e.Group
	case "store":
		if e.Store == "" {
			return "(untracked)"
		}
		return e.Store
	default:
		return e.State.String()
	}
}

func printSummary(entries []dotfile.Entry) {
	counts := map[dotfile.State]int{}
	for _, e := range entries {
		counts[e.State]++
	}
	var parts []string
	for _, s := range []dotfile.State{dotfile.Modified, dotfile.Untracked, dotfile.Missing, dotfile.Undeclared} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
		}
	}
	if len(parts) > 0 {
		fmt.Printf("\n%s\n", strings.Join(parts, ", "))
	}
}
