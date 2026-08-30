package cli

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/keyolk/dotx/internal/git"
	"github.com/keyolk/dotx/internal/manifest"
	"github.com/keyolk/dotx/internal/secret"
)

func newInitCmd() *cobra.Command {
	var (
		configURL string
		secretURL string
		force     bool
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up dotx on this machine",
		Long: `init prepares a machine, whether it already has dotfiles or nothing at all.

On a machine that already has the config and secret bare repos, it writes a
manifest describing what is there and leaves everything else alone.

On a fresh machine, --clone-config and --clone-secret fetch both repos, check
them out over $HOME, and generate an age identity. The identity's public key is
printed: add it to secrets.recipients from a machine that can already read the
vault, re-save, and this machine can decrypt.

Cloning a private store needs credentials, and on a machine set up this way
those credentials live inside the store being cloned. Break that loop by
putting a token in the clone URL for the first fetch, then resetting the
remote to the plain URL. A token embedded in the URL is redacted from output,
but it does land in shell history.

The chicken-and-egg is real and deliberate: a new machine cannot read secrets
until an existing one grants it access. There is no bootstrap path that does not
involve moving one key by hand, and pretending otherwise means shipping the key
somewhere it should not be.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			path := flagManifest
			if path == "" {
				path = manifest.DefaultPath()
			}
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("manifest already exists at %s (use --force to overwrite)", path)
			}

			configDir := filepath.Join(home, ".config.repo")
			secretDir := filepath.Join(home, ".secret.repo")

			// Both stores are cloned and checked out. Leaving the secret store
			// merely cloned would put its contents -- the vault among them --
			// in the repository but not on disk, so `apply` would fail on a
			// machine that had in fact fetched everything it needed.
			for _, store := range []struct {
				name, url, dir string
			}{
				{"config", configURL, configDir},
				{"secret", secretURL, secretDir},
			} {
				if store.url == "" {
					continue
				}
				fmt.Printf("cloning %s store from %s\n", store.name, redactURL(store.url))
				r, err := git.Clone(store.url, store.dir, home)
				if err != nil {
					return err
				}
				if conflicts, err := r.Checkout(); err != nil {
					fmt.Fprintf(os.Stderr, "\n%s checkout blocked by %d existing file(s):\n",
						store.name, len(conflicts))
					for _, c := range conflicts {
						fmt.Fprintf(os.Stderr, "  %s\n", c)
					}
					return fmt.Errorf("move or remove them, then run: git --git-dir=%s --work-tree=%s checkout",
						store.dir, home)
				}
			}

			identity := filepath.Join(home, ".config", "dotx", "identity.age")
			var pub string
			if _, err := os.Stat(identity); os.IsNotExist(err) {
				pub, err = secret.GenerateIdentity(identity)
				if err != nil {
					return err
				}
			}

			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			body := starterManifest(configDir, secretDir, home, identity, pub)
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				return err
			}

			fmt.Printf("\nmanifest written to %s\n", path)
			if pub != "" {
				fmt.Printf("age identity: %s\npublic key:   %s\n", identity, pub)
			}
			fmt.Println("\nnext:")
			fmt.Println("  dotx doctor            # see what is inconsistent")
			fmt.Println("  dotx status            # see what is untracked")
			fmt.Println("  dotx pkg adopt brew    # capture what is installed")
			if !which("age") && !which("git") {
				fmt.Println("\nwarning: git is not on PATH; install it before using the stores")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&configURL, "clone-config", "", "clone the config store from this URL")
	cmd.Flags().StringVar(&secretURL, "clone-secret", "", "clone the secret store from this URL")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing manifest")
	return cmd
}

// redactURL hides an embedded credential. Bootstrapping a private store often
// means putting a token in the clone URL, and echoing it back would write the
// credential to the terminal and into whatever captures that output.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, hasPassword := u.User.Password(); hasPassword {
		u.User = url.UserPassword(u.User.Username(), "***")
	}
	return u.String()
}

// starterManifest emits a manifest that reflects this machine rather than a
// generic example: the groups below are the ones that were measurably
// mistracked here, so a first `dotx status` reports something true.
func starterManifest(configDir, secretDir, home, identity, pub string) string {
	recipients := ""
	if pub != "" {
		recipients = fmt.Sprintf("%q", pub)
	}

	return fmt.Sprintf(`# dotx manifest
#
# Groups declare which paths belong under version control, as globs. A file
# matching a group is reported the moment it appears, which is the difference
# between this and `+"`git add`"+` on a bare repo: the latter only ever knows
# what someone remembered to add.

[store]
config    = %q
secret    = %q
work_tree = %q

[secrets]
identity   = %q
recipients = [%s]
vault      = ".config/dotx/vault.age"

# --- dotfiles -------------------------------------------------------------

[[dotfiles]]
name    = "shell"
include = [
  ".config/fish/config.fish",
  ".config/fish/functions/**/*.fish",
  ".config/fish/conf.d/**/*.fish",
  ".bashrc",
  ".profile",
]
exclude = [".config/fish/config.fish.bak*", ".config/fish/*.original", ".config/fish/fishfile"]

[[dotfiles]]
name    = "editor"
include = [
  ".vimrc",
  ".vim/*.vim",
  ".vim/autoload/*.vim",   # plug.vim is a symlink into the vim-plug submodule
  ".config/nvim/**/*.lua",
  ".config/nvim/**/*.vim",
]

[[dotfiles]]
name    = "terminal"
include = [".tmux.conf", ".config/ghostty/config", ".config/alacritty/**/*"]

# Everything Claude Code needs to reproduce this machine's behaviour. The old
# setup tracked 41 of 78 hooks and none of the 67 skills or 16 agents.
[[dotfiles]]
name    = "claude"
include = [
  ".claude/CLAUDE.md",
  ".claude/settings.json",
  ".claude/hooks/**/*.py",
  ".claude/hooks/**/*.sh",
  ".claude/skills/**/*",
  ".claude/agents/**/*",
  ".claude/commands/**/*",
  ".claude/rules/**/*",
  ".claude/memory/**/*",
  ".claude/mcp/**/*",
]
exclude = [
  ".claude/**/__pycache__/**",
  ".claude/**/*.pyc",
  ".claude/hooks/**/*.json",     # generated state, not config
  ".claude/**/*.jsonl",          # usage logs
  ".claude/**/.archive-*/**",
  ".claude/**/*.bak",
  ".claude/**/*.bak.*",
  ".claude/**/*.sha256",         # hook integrity markers, regenerated
  # Only the two fixtures that embed fake tokens; the other test files are
  # ordinary config and stay tracked.
  ".claude/hooks/test_bash_command_guard.py",
  ".claude/hooks/test_secret_read_guard.py",
  ".claude/skills-archive/**",
]

# .claude/plugins records which plugins are installed, which is intent worth
# keeping; its cache subtree is not.
[[dotfiles]]
name    = "claude-plugins"
include = [
  ".claude/plugins/config.json",
  ".claude/plugins/installed_plugins.json",
  ".claude/plugins/known_marketplaces.json",
  ".claude/.gitignore",
]

[[dotfiles]]
name    = "scripts"
include = [".local/bin/**/*", ".config/bin/**/*"]
exclude = [
  ".local/bin/__pycache__/**",
  ".local/bin/*.dSYM/**",
  ".local/bin/*.bak",
  ".local/bin/*.bak.*",
  # Vendored shims a third-party installer drops in, not this machine's work.
  ".local/bin/* (kiro-cli-term)",
  ".local/bin/* (qterm)",
  # Compiled binaries belong to the packages section, not here. dotx detects
  # them by magic number and reports them as artifacts, so this list only
  # needs the ones already committed before that check existed -- 30 binaries
  # totalling 509MB, one of which (telepresence, 127MB) exceeded GitHub's hard
  # file-size limit and made the store unpushable.
  ".local/bin/{argx,ccx,ccx-test,cco,dpx,ghx,gcl,kmd,okx,tpx,dotx}",
  ".local/bin/{telepresence,tempo-cli,kubectl-trace,esoctl,vmctl,netpulse}",
  ".local/bin/{herdr,r53-record-collector,narwhal,narwhal-bin,agent-cast}",
  ".local/bin/{ccproxy,k7s,tweb,tweb-tauri,tweb-pane,twebd,cs,netscope}",
  ".local/bin/{envoyscope,browserctl,twm,warp,websocat4,kiro-cli}",
  ".local/bin/{kiro-cli-chat,kiro-cli-term,record-classifier,cship}",
]

[[dotfiles]]
name    = "git"
include = [".gitconfig", ".themes.gitconfig", ".config/pass-git-helper/**/*", ".gitmodules"]
exclude = [".config/pass-git-helper/*.bak.*"]

# Tool configs that live outside the groups above. Each is hand-written config
# that no package manager or generator produces.
# dotx's own manifest. A machine cannot reproduce itself from a manifest the
# store does not carry.
# The manifest is ordinary config -- it names paths, not values.
[[dotfiles]]
name    = "dotx"
include = [".config/dotx/dotx.toml"]

# The vault is ciphertext, so committing it is safe, and only a machine listed
# in secrets.recipients can open it. Without it in a store, a new machine has a
# manifest full of {{ secret }} calls and no way to resolve them.
#
# identity.age -- the private key -- is deliberately in no group at all. It
# moves by hand, or the store becomes a single point of compromise.
[[dotfiles]]
name    = "dotx-vault"
secret  = true
include = [".config/dotx/vault.age"]

[[dotfiles]]
name    = "tools"
# github.spc ships GitHub's own documentation examples in its comments, and
# hooks/test_*.py hold fake tokens as test fixtures. All three are
# credential-shaped without being credentials, and a scanner cannot tell the
# difference from the bytes alone -- so they stay out rather than being
# force-added past the guard.
exclude = [".steampipe/config/github.spc"]
include = [
  ".config/ccx/config.yaml",
  ".config/pet/*.toml",
  ".config/nvim/*.ini",
  ".config/nvim/coc-settings.json",
  ".config/fish/fishfile",
  ".config/coc/extensions/package.json",
  ".obsidian/*.json",
  ".steampipe/config/*.spc",
  ".spin/config",
  ".spin/preprod",
  ".s/config",
  ".aws/config",
  ".aws/cli/data/*",
  ".terraformrc",
  ".less",
  ".lesskey",
  ".wakeup",
  "README.md",
]

# Config files carrying an inline credential live here as templates. The .tmpl
# is committed; the rendered file is not.
[[dotfiles]]
name     = "templated"
template = true
include  = [
  ".config/**/*.tmpl",
  ".claude/**/*.tmpl",
  ".ccproxy/*.tmpl",
  ".aws/**/*.tmpl",
  ".local/bin/**/*.tmpl",
]
# A plugin cache under .ccproxy ships its own .tmpl files that are not this
# machine's config.
# .config is not purely config: coc vendors a whole Go module cache under it,
# and those .tmpl files are a dependency's source, not this machine's setup.
exclude  = [
  ".ccproxy/codex-session/**",
  ".config/coc/**",
  ".config/**/pkg/mod/**",
]

# The rendered outputs of the templates above are deliberately absent from
# every group: .ccproxy/config.json holds a live token and must never reach the
# config store. Only its .tmpl is tracked.

# Material that is secret in whole. Stored in the secret repo.
[[dotfiles]]
name    = "credentials"
secret  = true
include = [
  ".kube/config",
  ".config/hub",
  ".aws/credentials",
  ".aws/amazonq/mcp.json",
  ".ssh/*.pem",
  ".ssh/*.pub",
  ".secret/**/*",
  ".sendbird/*.yaml",
]

# pass(1) keeps each secret in its own .gpg file, so the store must be declared
# as a subtree rather than enumerated.
[[dotfiles]]
name    = "password-store"
secret  = true
include = [".password-store/**/*.gpg", ".password-store/.gpg-id"]

# GPG mixes key material with agent state in one directory, so the whole of
# ~/.gnupg is the wrong unit to track. Only these three things cannot be
# regenerated on a new machine; everything else is either derived, legacy, or
# churns on every gpg invocation.
[[dotfiles]]
name    = "gnupg"
secret  = true
include = [
  ".gnupg/private-keys-v1.d/*.key",
  ".gnupg/pubring.kbx",
  ".gnupg/openpgp-revocs.d/*.rev",
]
exclude = [
  ".gnupg/random_seed",        # rewritten on every gpg run
  ".gnupg/pubring.kbx~",       # gpg's own backup of pubring.kbx
  ".gnupg/pubring.gpg",        # 0 bytes, pre-2.1 legacy
  ".gnupg/secring.gpg",        # 0 bytes, pre-2.1 legacy
  ".gnupg/trustdb.gpg",        # regenerated from the keyring
  ".gnupg/.gpg-v21-migrated",  # a migration marker, not config
]

[[dotfiles]]
name    = "linux-desktop"
os      = ["linux"]
include = [".xprofile", ".config/kime/**/*"]

# --- packages -------------------------------------------------------------
#
# Populate these from what is already installed:
#   dotx pkg adopt brew cask cargo bun mise krew

[packages.brew]
packages = []

[packages.cask]
packages = []

[packages.cargo]
packages = []

[packages.bun]
packages = []

[packages.mise]
packages = []

[packages.krew]
packages = []

[packages.apt]
packages = []

# Binaries no package manager owns. Each records how to reinstall it.
#
# [[packages.brew.binaries]]
# name    = "coder"
# from    = "https://github.com/coder/coder"
# install = "curl -fsSL https://coder.com/install.sh | sh"
# version = "%%s --version"
`, configDir, secretDir, home, identity, recipients)
}
