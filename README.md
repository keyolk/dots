# dots

Declarative dotfile, secret and package management for a machine you actually
use.

```
dots status                      # what differs from the manifest
dots add                         # commit declared files the store never saw
dots save -M "message"           # stage and commit everything declared
dots prune                       # untrack what the manifest no longer declares
dots apply                       # render templates, substituting secrets
dots secret set <name>           # store a secret in the age vault
dots pkg diff                    # declared vs installed, per source
dots pkg sync                    # install what is declared and missing
dots pkg bin                     # account for curl-installed binaries
dots doctor                      # what is inconsistent, and what to run
dots init                        # set up a machine, fresh or existing
```

## Why

The bare-repo-over-`$HOME` pattern — `alias config "git --git-dir=~/.config.repo
--work-tree=$HOME"` — has one failure mode that compounds silently. Because
`status.showUntrackedFiles` must be `no` (otherwise `git status` reports the
entire home directory), the repo only ever reports changes to files it already
tracks. A file you never explicitly added is invisible forever.

Measured on the machine this was built for:

| location | tracked | actual |
|---|---|---|
| `~/.local/bin` | 42 | 123 |
| `~/.claude/hooks` | 41 | 78 |
| `~/.claude/skills` | 0 | 67 |
| `~/.claude/agents` | 0 | 16 |
| `~/.claude/rules` | 0 | 11 |

The `.gitignore` even listed `skills/` and `commands/` under "Keep These". They
had never been added. Nothing reported that, because nothing could.

Three more problems come from the same root:

- **Secrets are file-granular.** `.ccproxy/config.json` is 95% ordinary config
  and one OAuth token. A config-repo/secret-repo split cannot express that: the
  committed copy had `"token": ""` while the working copy held a live
  `sk-ant-oat01-…`, one careless `config add` from being published.
- **Noise crowds out signal.** 2110 of 2274 tracked paths were `.spin/`
  watchman cookies. Real config was 164 files in a 21MB repo.
- **Packages are not recorded at all.** 316 brew formulae, 31 casks, 20 cargo
  crates, 374 bun globals, 5 mise tools, ~60 binaries in `~/.local/bin` with no
  recorded origin — and no Brewfile. None of it reproducible.

## The manifest

One `dots.toml` declares intent. Everything else compares intent against
reality.

```toml
[store]
config    = "~/.config.repo"     # the existing bare repos, unchanged
secret    = "~/.secret.repo"
work_tree = "~"

[secrets]
identity   = "~/.config/dots/identity.age"
recipients = ["age1qmsxc455ljqp0..."]
vault      = ".config/dots/vault.age"

[[dotfiles]]
name    = "claude"
include = [".claude/hooks/**/*.py", ".claude/skills/**/*", ".claude/agents/**/*"]
exclude = [".claude/**/__pycache__/**", ".claude/**/.archive-*/**"]
```

Paths are declared as **globs, not as a list**. A hook written tomorrow matches
`.claude/hooks/**/*.py` and shows up in `dots status` immediately. That is the
whole difference from `git add`.

Groups carry conditions and roles:

```toml
[[dotfiles]]
name = "linux-desktop"
os   = ["linux"]              # or host = ["work-laptop"]

[[dotfiles]]
name   = "credentials"
secret = true                 # goes to the secret store, not config

[[dotfiles]]
name     = "templated"
template = true               # rendered on apply, not used as-is
```

The first group to claim a path wins, so a narrow group listed before a broad
one is how you carve out an exception.

### Rooted patterns are required

`**/*.tmpl` is rejected. It has no literal prefix, so it would walk all of
`$HOME` — on a real machine that is over a million files across `Library`,
`src`, `.cargo` and `.rustup`. Write `.config/**/*.tmpl`. The check turns a
status that never finishes into one that takes 0.7s.

## What status reports

```
  M  modified    tracked, and the working copy differs
  ?  untracked   declared and present on disk, but never committed
  D  missing     tracked, but gone from disk
  -  undeclared  tracked, but no manifest group claims it any more
  S  submodule   a gitlink; no file glob can match a commit pointer
  ~  inactive    declared, but by a group this machine's os/host excludes
```

`untracked` is the one a bare repo with `status.showUntrackedFiles=no` cannot
produce, and it is where new hooks, skills and scripts accumulate.

`submodule` and `inactive` exist so `prune` cannot destroy things a file glob
was never going to match. A gitlink is a commit pointer, so tpm, fisherman and
vim-plug would otherwise read as undeclared; and a `.xprofile` declared by a
linux-only group is not a leftover on a Mac — untracking it there would delete
the other machine's config.

## Secrets

An age-encrypted key-value vault, not a directory of encrypted files. A
per-file layout publishes every secret's *name* through its filename — which is
exactly what a `.password-store` tree does today, listing every account you hold
in the clear.

```
dots secret keygen                       # generate this machine's identity
dots secret set github/token             # prompts, no echo
dots secret set tls/key --stdin < key.pem
dots secret get github/token             # raw value, composes with $(...)
dots secret list                         # names only, never values in bulk
```

### Why this does not replace `pass`

The two coexist deliberately, because they answer different questions.

| | holds | read by |
|---|---|---|
| `pass` (GPG) | interactive secrets — what a shell function pulls with `$(pass ...)` | you, at a prompt |
| dots vault (age) | values that get rendered into config files | `dots apply` |

Using age here does not mean `pass` should move to age too, because `pass`
cannot: it hardcodes `GPG="gpg"` (falling back to `gpg2`) and calls `$GPG -e` /
`$GPG -d` directly at 86 sites. No `PASSWORD_STORE_*` variable selects a
backend. That is precisely why `passage` exists as a *fork* rather than a
plugin — and the fork is not in Homebrew, and `pass-git-helper` does not
support it.

Meanwhile `pass` is load-bearing here in ways a swap would break: 33 call sites
in `config.fish`, and `.gitconfig` wires `pass-git-helper` as the git credential
helper that dispatches a different GitHub token per org. Switching would take
out `git push` to buy nothing dots needs.

The GPG key already lives in the secret store, so a new machine already has a
path to restore it. Moving two keys instead of one is not the cost worth
optimising.

Adding a second machine means adding its public key to `recipients` and
re-saving from a machine that can already decrypt. There is no way around
moving one key by hand, and any tool that claims otherwise is shipping your key
somewhere it should not be.

## Pruning what should never have been tracked

`prune` is the inverse of `add`: it untracks paths the store still carries but
no manifest group claims. Files are only untracked, never deleted.

```
dots prune -n                    # list what would be untracked
dots prune --store secret        # one store at a time
dots prune -y --commit           # do it
```

The two stores are pruned separately by design. A config store accumulates
generated junk that is safe to drop, while an undeclared path in the *secret*
store may be real key material that simply has not been declared yet — on this
machine that was 67 `.password-store` entries, five SSH keys and the `.secret`
tree, none of which should have been untracked. Declare first, prune second.

### GPG as the worked example

`~/.gnupg` is the wrong unit to track: GPG mixes key material with agent state
in one directory. Of ten tracked paths, six were noise —

| path | why it was dropped |
|---|---|
| `random_seed` | rewritten on every `gpg` run, so it was *modified* in every status |
| `pubring.kbx~` | gpg's own backup of `pubring.kbx` |
| `pubring.gpg`, `secring.gpg` | 0 bytes, pre-2.1 legacy |
| `trustdb.gpg` | regenerated from the keyring |
| `.gpg-v21-migrated` | a migration marker |

What survives is what cannot be regenerated: `private-keys-v1.d/*.key`,
`pubring.kbx`, and the revocation certificate. The manifest states this as a
group rather than leaving it to whoever last ran `git add`.

## Templates

The answer to "this config file has one secret field in it".

`.ccproxy/config.json.tmpl` is committed and reviewable:

```json
{ "token": "{{ secret "anthropic/ccproxy-oauth" }}", "weight": 30 }
```

`dots apply` renders `.ccproxy/config.json` with the real value, at mode `0600`,
and never tracks it. A missing secret is a hard error — rendering an empty token
produces a config that fails much later with an unrelated-looking auth error.

Available in templates: `{{ secret "name" }}`, `{{ env "VAR" }}`,
`{{ sh "command" }}`, `{{ .OS }}`, `{{ .Arch }}`, `{{ .Host }}`, `{{ .Home }}`,
`{{ if .IsDarwin }}`, `{{ .IsLinux }}`, `default`, `quote`.

## The credential guard

`dots add` and `dots save` refuse any file bound for the **config** store that
matches a credential shape — Anthropic, OpenAI, GitHub, AWS, Slack, Doppler
tokens and PEM private keys. The report names the file and line and prints only
a 12-character prefix: a guard that leaks the secret while refusing it has
defeated itself.

Files in a `secret = true` group skip the check, since holding credentials is
their job.

The guard reads bytes, not intent, so a test fixture holding a deliberately
fake token and a config file quoting the vendor's own documentation example
both trip it. Exclude those in the manifest rather than forcing them past the
guard — the alternative is teaching yourself to ignore it.

`add` also reports paths the manifest declares and a `.gitignore` excludes.
git resolves that conflict by failing the entire `git add`, so one stale ignore
rule silently blocks every unrelated file staged alongside it — on this machine
a rule labelled "Agent State (auto-generated)" was hiding 16 hand-written
subagent definitions.

## Packages

Every source behind one interface, because the answer is not "pick one package
manager" — it is "make all of them enumerable".

```toml
[packages.brew]
packages = ["age", "jq"]
darwin   = ["mas"]          # OS-specific additions

[packages.apt]
packages = ["build-essential"]

[[packages.brew.binaries]]
name    = "coder"
from    = "https://github.com/coder/coder"
install = "curl -fsSL https://coder.com/install.sh | sh"
version = "%s --version"
```

Built-in sources: `brew`, `cask`, `cargo`, `go`, `bun`, `mise`, `krew`, `apt`.
Each is parsed to bare package names — cargo's `crate vX.Y.Z:` headers, bun's
tree drawing with `@`-pinned versions, mise's columns, krew's header row. Any
other source works by declaring `list` and `install` commands.

`binaries` covers what no package manager can enumerate: the executable that
arrived from a `curl | sh` and is otherwise indistinguishable from one you
compiled. Declaring it records how to reproduce it.

```
dots pkg diff              # per source: managed, missing, undeclared
dots pkg diff --extra      # name the undeclared ones
dots pkg adopt brew        # emit TOML for what is installed but undeclared
dots pkg sync -n           # print install commands without running them
dots pkg bin               # declared binaries + undeclared ones in ~/.local/bin
```

`sync` **never removes anything.** An installed-but-undeclared package is
something to reconcile deliberately, not something a sync should delete out from
under a running system.

A source whose list command fails is reported *unavailable*, not "everything is
missing" — otherwise `sync` would act on a lie and reinstall the world.

## Bootstrap

On a machine that has nothing:

```
# 1. git and dots must exist first; everything else comes from the stores.
dots init --clone-config https://github.com/you/config.git \
          --clone-secret https://github.com/you/secret.git

# 2. init prints this machine's new age public key. From a machine that can
#    already decrypt, append it to secrets.recipients and re-save the vault:
#      dots secret set <any-existing-key> "$(dots secret get <any-existing-key>)"
#    then push. Until that happens, step 4 fails by design.

dots doctor          # what is inconsistent
dots pkg sync        # install the declared packages
dots apply           # render templates once the vault is readable
```

What each store carries, and what it deliberately does not:

| | in a store | why |
|---|---|---|
| `dots.toml` | config | names paths, not values |
| `vault.age` | secret | ciphertext; useless without a listed key |
| `identity.age` | **neither** | the private key; moving it by hand is the point |
| `.ccproxy/config.json` | **neither** | rendered output, holds the live token |
| `.local/bin/*` compiled | **neither** | rebuild from source, don't commit Mach-O |

### Two things a fresh machine needs before dots can help

Both were found by running the bootstrap into an empty `$HOME` rather than by
reasoning about it.

**Cloning a private store needs credentials that live in the store.** Here the
git credential helper is `pass-git-helper`, which reads `~/.password-store`,
which is decrypted by a GPG key — and all three are inside the secret repo you
are trying to clone. `git clone` into a clean `$HOME` fails with *"could not
read Username for 'https://github.com'"*. Break the loop with a token in the
clone URL for the first fetch:

```
TOKEN=<a personal access token>
dots init --clone-config "https://user:$TOKEN@github.com/you/config.git" \
          --clone-secret "https://user:$TOKEN@github.com/you/secret.git"
git --git-dir=~/.config.repo --work-tree=$HOME remote set-url origin \
    https://github.com/you/config.git      # drop the token afterwards
```

**Reading the vault needs a key an existing machine must grant.** A clone into
an empty `$HOME` checks out 442 files across both stores — including the vault,
the GPG keyring and the SSH keys — generates an age key, and then reports:

```
ok   store/config   339 tracked path(s)
ok   store/secret   109 tracked path(s)
FAIL vault          identity did not match any of the recipients
ok   dotfiles       444 path(s) clean
```

That is the design working, and `doctor` names the single remaining step: add
the new machine's public key to `secrets.recipients` from a machine that can
already decrypt, re-save, push.

Neither loop can be closed by the tool. One token and one key move by hand, and
a tool that claimed otherwise would be shipping your credentials somewhere they
should not be.

`init` generates an age identity and prints its public key. Grant it access from
a machine that can already read the vault, then `apply` works.

Checkout refuses to overwrite existing files and names every conflict, rather
than clobbering a home directory that already had content.

On a machine that already has the bare repos, plain `dots init` writes a
manifest describing what is there and changes nothing else.

## Adopting it

The two bare repos stay exactly as they are — same history, same remotes, and
the `config` and `secret` shell aliases keep working. dots layers on top rather
than migrating anything, so nothing has to be moved to try it.

```
dots init
dots status --group-by group    # see what was never tracked
dots pkg adopt brew cargo bun   # capture what is installed
```

## Build

```
make build
make install       # to ~/.local/bin/dots
make test
```
