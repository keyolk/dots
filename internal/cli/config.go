package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/keyolk/dots/internal/dotfile"
	"github.com/keyolk/dots/internal/git"
	"github.com/keyolk/dots/internal/manifest"
	"github.com/keyolk/dots/internal/ui"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show where dots is pointed and what it tracks",
		Long: `config prints the settings dots is running on: which repository it reads
and writes, where that repository pushes to, which key opens the vault, and
what each group covers.

Answering "where is this actually pointed?" took a git incantation before this
existed. Every value it prints comes from the manifest, so the file it names
is also the file to edit.`,
	}
	cmd.AddCommand(newConfigShowCmd(), newConfigGroupsCmd(), newConfigPathCmd())

	// Bare `dots config` is the common case.
	cmd.RunE = newConfigShowCmd().RunE
	return cmd
}

// configView is the JSON shape, and the thing the text output renders.
type configView struct {
	Manifest string       `json:"manifest"`
	Store    storeView    `json:"store"`
	Secrets  secretsView  `json:"secrets"`
	Groups   []groupView  `json:"groups"`
	Packages []packageRow `json:"packages"`
}

type storeView struct {
	GitDir   string `json:"git_dir"`
	WorkTree string `json:"work_tree"`
	Remote   string `json:"remote,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Tracked  int    `json:"tracked"`
	Unpushed int    `json:"unpushed"`
}

type secretsView struct {
	Identity   string   `json:"identity"`
	Vault      string   `json:"vault"`
	Recipients []string `json:"recipients"`
	HasKey     bool     `json:"has_key"`
}

type groupView struct {
	Name      string `json:"name"`
	Patterns  int    `json:"patterns"`
	Matched   int    `json:"matched"`
	Secret    bool   `json:"secret,omitempty"`
	Template  bool   `json:"template,omitempty"`
	Normalize string `json:"normalize,omitempty"`
	OS        string `json:"os,omitempty"`
	Host      string `json:"host,omitempty"`
}

type packageRow struct {
	Source   string `json:"source"`
	Declared int    `json:"declared"`
	Binaries int    `json:"binaries"`
}

func newConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the effective configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := load()
			if err != nil {
				return err
			}
			v := buildConfigView(m)

			if flagJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(v)
			}
			printConfigView(v)
			return nil
		},
	}
}

func buildConfigView(m *manifest.Manifest) configView {
	v := configView{
		Manifest: m.Path,
		Store: storeView{
			GitDir:   m.Store.Config,
			WorkTree: m.Store.WorkTree,
			Unpushed: -1,
		},
		Secrets: secretsView{
			Identity:   m.Secrets.Identity,
			Vault:      m.Secrets.Vault,
			Recipients: m.Secrets.Recipients,
		},
	}

	repo := git.New(m.Store.Config, m.Store.WorkTree)
	if repo.Exists() {
		if out, err := repo.Run("remote", "get-url", "origin"); err == nil {
			v.Store.Remote = strings.TrimSpace(out)
		}
		if out, err := repo.Run("rev-parse", "--abbrev-ref", "HEAD"); err == nil {
			v.Store.Branch = strings.TrimSpace(out)
		}
		if files, err := repo.LsFiles(); err == nil {
			v.Store.Tracked = len(files)
		}
		v.Store.Unpushed, _ = repo.Unpushed()
	}

	if m.Secrets.Identity != "" {
		if _, err := os.Stat(m.Secrets.Identity); err == nil {
			v.Secrets.HasKey = true
		}
	}

	// Group rows report how many paths each one currently matches, which is
	// what turns a list of globs into something you can sanity-check.
	counts := groupMatchCounts(m)
	for _, g := range m.Dotfiles {
		row := groupView{
			Name:      g.Name,
			Patterns:  len(g.Include),
			Matched:   counts[g.Name],
			Secret:    g.Secret,
			Template:  g.Template,
			Normalize: g.Normalize,
		}
		if len(g.OS) > 0 {
			row.OS = strings.Join(g.OS, ",")
		}
		if len(g.Host) > 0 {
			row.Host = strings.Join(g.Host, ",")
		}
		v.Groups = append(v.Groups, row)
	}

	names := make([]string, 0, len(m.Packages))
	for name := range m.Packages {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := m.Packages[name]
		v.Packages = append(v.Packages, packageRow{
			Source:   name,
			Declared: len(p.Resolved()),
			Binaries: len(p.Binaries),
		})
	}
	return v
}

// groupMatchCounts asks the scanner how many paths each group claims. A group
// matching zero paths is usually a typo in a glob, and the count is the
// cheapest way to see it.
func groupMatchCounts(m *manifest.Manifest) map[string]int {
	counts := map[string]int{}
	entries, err := dotfile.NewScanner(m, hostname()).Scan()
	if err != nil {
		return counts
	}
	for _, e := range entries {
		if e.Group != "" {
			counts[e.Group]++
		}
	}
	return counts
}

func printConfigView(v configView) {
	fmt.Printf("%s %s\n", ui.Heading.Width(12).Render("manifest"), v.Manifest)
	fmt.Println()

	fmt.Println(ui.Heading.Render("store"))
	kv("repo", v.Store.GitDir)
	kv("work tree", v.Store.WorkTree)
	if v.Store.Remote != "" {
		kv("remote", v.Store.Remote)
	} else {
		fmt.Printf("  %s %s\n", ui.Muted.Width(12).Render("remote"),
			ui.Warn.Render("none configured"))
	}
	if v.Store.Branch != "" {
		kv("branch", v.Store.Branch)
	}
	kv("tracked", fmt.Sprintf("%d path(s)", v.Store.Tracked))
	switch {
	case v.Store.Unpushed > 0:
		fmt.Printf("  %s %s\n", ui.Muted.Width(12).Render("unpushed"),
			ui.Warn.Render(fmt.Sprintf("%d commit(s)", v.Store.Unpushed)))
	case v.Store.Unpushed == 0:
		kv("unpushed", "none")
	default:
		fmt.Printf("  %s %s\n", ui.Muted.Width(12).Render("unpushed"),
			ui.Muted.Render("unknown (no remote-tracking ref)"))
	}
	fmt.Println()

	fmt.Println(ui.Heading.Render("secrets"))
	kv("vault", v.Secrets.Vault)
	if v.Secrets.HasKey {
		kv("identity", v.Secrets.Identity)
	} else {
		fmt.Printf("  %s %s\n", ui.Muted.Width(12).Render("identity"),
			ui.Warn.Render(v.Secrets.Identity+" (missing)"))
	}
	if len(v.Secrets.Recipients) == 0 {
		fmt.Printf("  %s %s\n", ui.Muted.Width(12).Render("recipients"),
			ui.Muted.Render("none listed; the vault opens only with this key"))
	} else {
		kv("recipients", fmt.Sprintf("%d key(s)", len(v.Secrets.Recipients)))
	}
	fmt.Println()

	fmt.Println(ui.Heading.Render("groups"))
	for _, g := range v.Groups {
		var flags []string
		if g.Secret {
			flags = append(flags, "secret")
		}
		if g.Template {
			flags = append(flags, "template")
		}
		if g.Normalize != "" {
			flags = append(flags, "normalize="+g.Normalize)
		}
		if g.OS != "" {
			flags = append(flags, "os="+g.OS)
		}
		if g.Host != "" {
			flags = append(flags, "host="+g.Host)
		}

		matched := fmt.Sprintf("%4d matched", g.Matched)
		// Zero matches on an active group means a glob is not finding what it
		// was written for -- worth seeing without reading the manifest.
		if g.Matched == 0 && g.OS == "" && g.Host == "" {
			matched = ui.Warn.Render(matched)
		}
		fmt.Printf("  %s %s  %s %s\n",
			ui.Heading.Width(16).Render(g.Name),
			matched,
			ui.Muted.Render(fmt.Sprintf("%2d pattern(s)", g.Patterns)),
			ui.Muted.Render(strings.Join(flags, " ")))
	}
	fmt.Println()

	fmt.Println(ui.Heading.Render("packages"))
	for _, p := range v.Packages {
		extra := ""
		if p.Binaries > 0 {
			extra = ui.Muted.Render(fmt.Sprintf("  + %d binary/binaries", p.Binaries))
		}
		fmt.Printf("  %s %4d declared%s\n",
			ui.Heading.Width(16).Render(p.Source), p.Declared, extra)
	}

	fmt.Printf("\n%s\n", ui.Muted.Render("edit: "+v.Manifest))
}

func kv(k, val string) {
	fmt.Printf("  %s %s\n", ui.Muted.Width(12).Render(k), val)
}

func newConfigGroupsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "groups [name]",
		Short: "Show the patterns a group declares",
		Long: `groups prints the include and exclude globs of every group, or of one
named group. This is the answer to "why is this file not showing up?" -- if no
pattern covers it, nothing will report it.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := load()
			if err != nil {
				return err
			}
			want := ""
			if len(args) == 1 {
				want = args[0]
			}

			found := false
			for _, g := range m.Dotfiles {
				if want != "" && g.Name != want {
					continue
				}
				found = true
				fmt.Printf("\n%s\n", ui.Heading.Render(g.Name))
				for _, p := range g.Include {
					fmt.Printf("  %s %s\n", ui.StateUntracked.Render("+"), p)
				}
				for _, p := range g.Exclude {
					fmt.Printf("  %s %s\n", ui.StateUndeclared.Render("-"), p)
				}
			}
			if !found {
				return fmt.Errorf("no group named %q; see `dots config` for the list", want)
			}
			return nil
		},
	}
}

func newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the manifest path, for scripting",
		Long: `path prints just the manifest's location, so it composes:

    $EDITOR "$(dots config path)"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := manifest.Load(flagManifest)
			if err != nil {
				// Even when the manifest is missing, saying where it would go
				// is more useful than an error alone.
				fmt.Println(defaultManifestPath())
				return nil
			}
			fmt.Println(m.Path)
			return nil
		},
	}
}

func defaultManifestPath() string {
	if flagManifest != "" {
		return flagManifest
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "dots.toml"
	}
	return filepath.Join(home, ".config", "dots", "dots.toml")
}
