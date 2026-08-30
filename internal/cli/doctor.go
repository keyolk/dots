package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/keyolk/dots/internal/dotfile"
	"github.com/keyolk/dots/internal/git"
	"github.com/keyolk/dots/internal/manifest"
	"github.com/keyolk/dots/internal/pkgmgr"
	"github.com/keyolk/dots/internal/secret"
)

// check is one diagnostic with a fix the user can act on.
type check struct {
	name   string
	status string // ok, warn, fail
	detail string
	fix    string
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose the machine's configuration state",
		Long: `doctor reports whether the manifest, the stores, the vault and the package
sources are consistent, and what to run for each thing that is not.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var checks []check

			m, err := manifest.Load(flagManifest)
			if err != nil {
				printChecks([]check{{
					name:   "manifest",
					status: "fail",
					detail: err.Error(),
					fix:    "dots init",
				}})
				return fmt.Errorf("no usable manifest")
			}
			checks = append(checks, check{
				name: "manifest", status: "ok",
				detail: fmt.Sprintf("%s (%d dotfile groups, %d package sources)",
					m.Path, len(m.Dotfiles), len(m.Packages)),
			})

			checks = append(checks, checkStores(m)...)
			checks = append(checks, checkVault(m))
			checks = append(checks, checkDotfiles(m))
			checks = append(checks, checkPackages(m))
			checks = append(checks, checkPath())

			printChecks(checks)

			for _, c := range checks {
				if c.status == "fail" {
					return fmt.Errorf("doctor found problems")
				}
			}
			return nil
		},
	}
}

func checkStores(m *manifest.Manifest) []check {
	var out []check
	for _, s := range []struct{ name, dir string }{
		{"store/config", m.Store.Config},
		{"store/secret", m.Store.Secret},
	} {
		if s.dir == "" {
			out = append(out, check{s.name, "warn", "not configured", "set store." + strings.TrimPrefix(s.name, "store/") + " in the manifest"})
			continue
		}
		r := git.New(s.dir, m.Store.WorkTree)
		if !r.Exists() {
			out = append(out, check{s.name, "fail", s.dir + " does not exist",
				"dots init --clone <url>"})
			continue
		}
		files, err := r.LsFiles()
		if err != nil {
			out = append(out, check{s.name, "fail", err.Error(), ""})
			continue
		}
		out = append(out, check{s.name, "ok", fmt.Sprintf("%d tracked path(s)", len(files)), ""})
	}
	return out
}

func checkVault(m *manifest.Manifest) check {
	if m.Secrets.Identity == "" {
		return check{"vault", "warn", "no age identity configured", "dots secret keygen"}
	}
	if _, err := os.Stat(m.Secrets.Identity); err != nil {
		return check{"vault", "fail", "identity missing: " + m.Secrets.Identity,
			"dots secret keygen, or restore the key from your password manager"}
	}
	// A world-readable private key is a real finding, not a style note.
	if fi, err := os.Stat(m.Secrets.Identity); err == nil && fi.Mode().Perm()&0o077 != 0 {
		return check{"vault", "fail",
			fmt.Sprintf("identity %s is mode %04o", m.Secrets.Identity, fi.Mode().Perm()),
			"chmod 600 " + m.Secrets.Identity}
	}
	v, err := secret.Open(vaultPath(m), m.Secrets.Identity, m.Secrets.Recipients)
	if err != nil {
		return check{"vault", "fail", err.Error(), ""}
	}
	return check{"vault", "ok", fmt.Sprintf("%d secret(s)", len(v.Keys())), ""}
}

func checkDotfiles(m *manifest.Manifest) check {
	entries, err := dotfile.NewScanner(m, hostname()).Scan()
	if err != nil {
		return check{"dotfiles", "fail", err.Error(), ""}
	}
	counts := map[dotfile.State]int{}
	for _, e := range entries {
		counts[e.State]++
	}
	dirty := counts[dotfile.Modified] + counts[dotfile.Untracked] + counts[dotfile.Missing]
	if dirty == 0 {
		return check{"dotfiles", "ok", fmt.Sprintf("%d path(s) clean", counts[dotfile.Clean]), ""}
	}
	return check{"dotfiles", "warn",
		fmt.Sprintf("%d modified, %d untracked, %d missing, %d undeclared",
			counts[dotfile.Modified], counts[dotfile.Untracked],
			counts[dotfile.Missing], counts[dotfile.Undeclared]),
		"dots status, then dots save"}
}

func checkPackages(m *manifest.Manifest) check {
	var missing, unavailable int
	for _, d := range pkgmgr.Reconcile(m) {
		if !d.Available {
			unavailable++
			continue
		}
		missing += len(d.Missing)
	}
	if missing == 0 {
		return check{"packages", "ok", "all declared packages installed", ""}
	}
	return check{"packages", "warn",
		fmt.Sprintf("%d declared package(s) not installed", missing),
		"dots pkg sync"}
}

// checkPath catches the failure that makes a fresh machine look broken in a way
// that has nothing to do with dots: everything installs correctly into
// ~/.local/bin and none of it is on PATH.
func checkPath() check {
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".local", "bin")
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == want {
			return check{"path", "ok", want + " is on PATH", ""}
		}
	}
	return check{"path", "warn", want + " is not on PATH",
		"add it in your shell config"}
}

func printChecks(checks []check) {
	for _, c := range checks {
		fmt.Printf("%s %-14s %s\n", mark(c.status), c.name, c.detail)
		if c.fix != "" {
			fmt.Printf("               → %s\n", c.fix)
		}
	}
}

func mark(status string) string {
	switch status {
	case "ok":
		return "ok  "
	case "warn":
		return "warn"
	default:
		return "FAIL"
	}
}

// which reports whether an executable is on PATH, for bootstrap decisions.
func which(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
