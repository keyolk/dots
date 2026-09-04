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
	"github.com/keyolk/dots/internal/ui"
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
	if m.Store.Config == "" {
		return append(out, check{"store", "warn", "not configured",
			"set store.config in the manifest"})
	}
	r := git.New(m.Store.Config, m.Store.WorkTree)
	if !r.Exists() {
		return append(out, check{"store", "fail", m.Store.Config + " does not exist",
			"dots init --clone-config <url>"})
	}
	files, err := r.LsFiles()
	if err != nil {
		return append(out, check{"store", "fail", err.Error(), ""})
	}
	out = append(out, check{"store", "ok", fmt.Sprintf("%d tracked path(s)", len(files)), ""})

	// A commit that never left the machine protects nothing. --push on save is
	// opt-in, so without this the gap is silent: the store looks healthy while
	// every other machine is behind.
	if n, _ := r.Unpushed(); n > 0 {
		out = append(out, check{"remote", "warn",
			fmt.Sprintf("%d commit(s) not pushed", n),
			"dots save --push, or: config push"})
	} else if n == 0 {
		// Name the remote rather than saying "origin": which repository this
		// machine is pointed at is the thing worth confirming at a glance.
		remote := "origin"
		if url, err := r.Run("remote", "get-url", "origin"); err == nil {
			remote = strings.TrimSpace(url)
		}
		out = append(out, check{"remote", "ok", "up to date with " + remote, ""})
	}

	// A manifest carried over from the two-store layout still names a second
	// repo. Say so rather than ignoring it silently, since its contents are no
	// longer being tracked by anything.
	if m.Store.Secret != "" {
		out = append(out, check{"store/secret", "warn",
			"a second store is configured but no longer used",
			"merge its contents into store.config, then drop store.secret"})
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
		// Padding has to happen inside the style: %-14s counts the ANSI escape
		// bytes as characters, so a styled name pushes the detail column out
		// of alignment by however long the escape sequence is.
		fmt.Printf("%s %s %s\n", mark(c.status), ui.Heading.Width(14).Render(c.name), c.detail)
		if c.fix != "" {
			fmt.Println(ui.Fix.Render("               → " + c.fix))
		}
	}
}

// mark renders the verdict. The width is fixed at four columns so the names
// line up whichever verdicts a run produces.
func mark(status string) string {
	switch status {
	case "ok":
		return ui.OK.Render("ok  ")
	case "warn":
		return ui.Warn.Render("warn")
	default:
		return ui.Fail.Render("FAIL")
	}
}

// which reports whether an executable is on PATH, for bootstrap decisions.
func which(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
