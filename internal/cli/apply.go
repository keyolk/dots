package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/keyolk/dots/internal/dotfile"
	"github.com/keyolk/dots/internal/manifest"
	"github.com/keyolk/dots/internal/secret"
	"github.com/keyolk/dots/internal/tmpl"
)

func newApplyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Render templates into their live files",
		Long: `apply renders every tracked .tmpl file to its target path, substituting
secrets from the vault.

This is what makes a config file with an embedded credential safe to commit:
.ccproxy/config.json.tmpl is reviewable and version-controlled, while the
rendered .ccproxy/config.json holds the real token, is written 0600, and is
never tracked.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := load()
			if err != nil {
				return err
			}
			entries, err := dotfile.NewScanner(m, hostname()).Scan()
			if err != nil {
				return err
			}

			var templates []dotfile.Entry
			for _, e := range entries {
				if e.Template && e.State != dotfile.Missing {
					templates = append(templates, e)
				}
			}
			if len(templates) == 0 {
				fmt.Println("no templates to render")
				return nil
			}

			// The vault is opened lazily: a manifest whose templates only use
			// {{ .OS }} and {{ env }} should still apply on a machine that has
			// no age identity yet.
			r, err := newRenderer(m)
			if err != nil {
				return err
			}

			var rendered, failed int
			for _, e := range templates {
				src := filepath.Join(m.Store.WorkTree, e.Path)
				dst := filepath.Join(m.Store.WorkTree, tmpl.Target(e.Path))

				if flagDryRun {
					fmt.Printf("would render %s -> %s\n", e.Path, tmpl.Target(e.Path))
					rendered++
					continue
				}
				if err := r.RenderFile(src, dst); err != nil {
					fmt.Fprintf(os.Stderr, "  %s: %v\n", e.Path, err)
					failed++
					continue
				}
				fmt.Printf("  %s -> %s\n", e.Path, tmpl.Target(e.Path))
				rendered++
			}

			fmt.Printf("\nrendered %d template(s)\n", rendered)
			if failed > 0 {
				return fmt.Errorf("%d template(s) failed", failed)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&flagDryRun, "dry-run", "n", false, "show what would be rendered without writing")
	return cmd
}

// newRenderer wires the vault into the template engine, deferring the vault
// open until a template actually calls `secret`.
func newRenderer(m *manifest.Manifest) (*tmpl.Renderer, error) {
	var (
		vault    *secret.Vault
		vaultErr error
		opened   bool
	)
	lookup := func(name string) (string, error) {
		if !opened {
			opened = true
			vault, vaultErr = secret.Open(vaultPath(m), m.Secrets.Identity, m.Secrets.Recipients)
		}
		if vaultErr != nil {
			return "", vaultErr
		}
		return vault.Get(name)
	}
	return tmpl.New(lookup, hostname()), nil
}

func vaultPath(m *manifest.Manifest) string {
	p := m.Secrets.Vault
	if p == "" {
		p = ".config/dots/vault.age"
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(m.Store.WorkTree, p)
}
