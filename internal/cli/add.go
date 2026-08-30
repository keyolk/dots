package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/keyolk/dotx/internal/dotfile"
)

// secretPatterns are the shapes that must never reach the config repo. They are
// checked on add rather than only at review time: this machine already has a
// live example - .ccproxy/config.json holds an sk-ant-oat01 token in the working
// copy while its committed version has an empty string, one careless `config
// add` away from being published.
var secretPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"Anthropic key", regexp.MustCompile(`sk-ant-[a-zA-Z0-9-]{10,}`)},
	{"OpenAI key", regexp.MustCompile(`sk-(proj-)?[A-Za-z0-9]{32,}`)},
	{"GitHub token", regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`)},
	{"GitHub PAT", regexp.MustCompile(`github_pat_[A-Za-z0-9_]{40,}`)},
	{"AWS access key", regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{"Slack token", regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`)},
	{"private key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"Doppler token", regexp.MustCompile(`dp\.(pt|st|sa|scim|audit)\.(?:[A-Za-z0-9_-]+\.)?[A-Za-z0-9_-]{20,}`)},
}

func newAddCmd() *cobra.Command {
	var (
		force  bool
		asTmpl bool
	)

	cmd := &cobra.Command{
		Use:   "add [path...]",
		Short: "Commit untracked declared files into their store",
		Long: `add stages and commits every file the manifest declares but the store has
never seen. With no arguments it adds all of them.

Each file is scanned for credential shapes first. A file that matches is
refused, with the offending line reported, because the config repo is the wrong
place for it - convert it to a template with --template, or move it to a secret
group in the manifest.`,
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

			want := map[string]bool{}
			for _, a := range args {
				want[filepath.Clean(a)] = true
			}

			byStore := map[string][]string{}
			var blocked int

			for _, e := range entries {
				if e.State != dotfile.Untracked {
					continue
				}
				if len(want) > 0 && !want[e.Path] {
					continue
				}
				abs := filepath.Join(m.Store.WorkTree, e.Path)

				// A secret-group file is expected to hold credentials; the scan
				// is only meaningful for the config repo.
				if e.Store == "config" && !asTmpl {
					if hit := scanSecrets(abs); hit != "" {
						fmt.Fprintf(os.Stderr, "refused %s: %s\n", e.Path, hit)
						blocked++
						continue
					}
				}
				byStore[e.Store] = append(byStore[e.Store], e.Path)
			}

			if len(byStore) == 0 {
				if blocked > 0 {
					return fmt.Errorf("%d file(s) refused, nothing added", blocked)
				}
				fmt.Println("nothing to add")
				return nil
			}

			for store, paths := range byStore {
				repo := sc.Repo(store)

				// A path the manifest declares and a .gitignore excludes is a
				// conflict git resolves by failing the whole batch, so one
				// stale rule blocks every unrelated file staged alongside it.
				// Report it and stage the rest rather than failing opaquely.
				ignored, err := repo.Ignored(paths)
				if err != nil {
					return err
				}
				if len(ignored) > 0 {
					skip := make(map[string]bool, len(ignored))
					for _, p := range ignored {
						skip[p] = true
					}
					kept := paths[:0]
					for _, p := range paths {
						if !skip[p] {
							kept = append(kept, p)
						}
					}
					paths = kept

					fmt.Fprintf(os.Stderr,
						"\n%d path(s) are declared in the manifest but excluded by a .gitignore:\n", len(ignored))
					for i, p := range ignored {
						if i == 10 {
							fmt.Fprintf(os.Stderr, "  … %d more\n", len(ignored)-10)
							break
						}
						fmt.Fprintf(os.Stderr, "  %s\n", p)
					}
					fmt.Fprintln(os.Stderr,
						"resolve the conflict: drop the ignore rule, or stop declaring the path")
				}
				if len(paths) == 0 {
					continue
				}

				if err := repo.Add(paths...); err != nil {
					return err
				}
				fmt.Printf("%s: staged %d file(s)\n", store, len(paths))
				for _, p := range paths {
					fmt.Printf("  + %s\n", p)
				}
				if !force {
					continue
				}
				ok, err := repo.Commit(fmt.Sprintf("dotx: add %d file(s)", len(paths)))
				if err != nil {
					return err
				}
				if ok {
					fmt.Printf("%s: committed\n", store)
				}
			}

			if blocked > 0 {
				fmt.Fprintf(os.Stderr, "\n%d file(s) refused for credential content\n", blocked)
			}
			if !force {
				fmt.Println("\nstaged only - run `dotx save` to commit")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "commit", false, "commit immediately instead of leaving changes staged")
	cmd.Flags().BoolVar(&asTmpl, "template", false, "skip the credential scan (the file is a template)")
	return cmd
}

// scanSecrets returns a description of the first credential-shaped match, or
// empty if the file looks clean. Binary files are skipped: a false positive on
// random bytes would block a legitimate add.
func scanSecrets(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil || st.Size() > 4<<20 {
		return ""
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		if strings.IndexByte(text, 0) >= 0 {
			return "" // binary
		}
		for _, p := range secretPatterns {
			if loc := p.re.FindString(text); loc != "" {
				return fmt.Sprintf("%s at line %d (%s…)", p.name, line, safePrefix(loc))
			}
		}
	}
	return ""
}

// safePrefix shows enough of a match to identify it without reprinting the
// credential into a terminal and a scrollback buffer.
func safePrefix(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s[:len(s)/2]
}
