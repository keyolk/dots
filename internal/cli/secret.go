package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/keyolk/dots/internal/secret"
)

func newSecretCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "secret",
		Aliases: []string{"sec"},
		Short:   "Manage the age-encrypted secret vault",
		Long: `secret reads and writes the age vault that templates draw from.

The vault is a single encrypted file rather than one file per secret: a
per-file layout publishes every secret's name through its filename, which is
exactly what a directory of .gpg files does today.`,
	}
	cmd.AddCommand(
		newSecretListCmd(),
		newSecretGetCmd(),
		newSecretSetCmd(),
		newSecretRmCmd(),
		newSecretKeygenCmd(),
	)
	return cmd
}

func openVault() (*secret.Vault, error) {
	m, err := load()
	if err != nil {
		return nil, err
	}
	return secret.Open(vaultPath(m), m.Secrets.Identity, m.Secrets.Recipients)
}

func newSecretListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List secret names",
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := openVault()
			if err != nil {
				return err
			}
			keys := v.Keys()
			if len(keys) == 0 {
				fmt.Println("vault is empty")
				return nil
			}
			for _, k := range keys {
				fmt.Println(k)
			}
			return nil
		},
	}
}

func newSecretGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <name>",
		Short: "Print one secret to stdout",
		Long: `get writes the raw value with no trailing newline, so it composes with
command substitution: export TOKEN=$(dots secret get anthropic/oauth)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := openVault()
			if err != nil {
				return err
			}
			val, err := v.Get(args[0])
			if err != nil {
				return err
			}
			fmt.Print(val)
			// A value printed to a terminal without a newline leaves the shell
			// prompt mid-line; one printed into a pipe must stay exact.
			if term.IsTerminal(int(os.Stdout.Fd())) {
				fmt.Println()
			}
			return nil
		},
	}
}

func newSecretSetCmd() *cobra.Command {
	var fromStdin bool

	cmd := &cobra.Command{
		Use:   "set <name> [value]",
		Short: "Store one secret",
		Long: `set stores a secret. With no value it prompts without echo; with --stdin it
reads the whole of stdin, which is how a multi-line value such as a private key
is stored.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := openVault()
			if err != nil {
				return err
			}

			var val string
			switch {
			case fromStdin:
				b, err := io.ReadAll(os.Stdin)
				if err != nil {
					return err
				}
				val = strings.TrimRight(string(b), "\n")
			case len(args) == 2:
				val = args[1]
			default:
				val, err = promptSecret(fmt.Sprintf("value for %s: ", args[0]))
				if err != nil {
					return err
				}
			}
			if val == "" {
				return errors.New("empty value; use --stdin to store one deliberately")
			}

			v.Set(args[0], val)
			if err := v.Save(); err != nil {
				return err
			}
			fmt.Printf("stored %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&fromStdin, "stdin", false, "read the value from stdin")
	return cmd
}

func newSecretRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove", "delete"},
		Short:   "Remove one secret",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := openVault()
			if err != nil {
				return err
			}
			if _, err := v.Get(args[0]); err != nil {
				return err
			}
			v.Delete(args[0])
			if err := v.Save(); err != nil {
				return err
			}
			fmt.Printf("removed %s\n", args[0])
			return nil
		},
	}
}

func newSecretKeygenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "keygen",
		Short: "Generate the age identity for this machine",
		Long: `keygen writes a new age keypair and prints its public key.

The private key stays on the machine and is never tracked. To give a second
machine access, add its public key to secrets.recipients and re-save the vault.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := load()
			if err != nil {
				return err
			}
			pub, err := secret.GenerateIdentity(m.Secrets.Identity)
			if err != nil {
				return err
			}
			fmt.Printf("identity written to %s\n", m.Secrets.Identity)
			fmt.Printf("public key: %s\n\n", pub)
			fmt.Println("add it to secrets.recipients in the manifest to encrypt to this machine")
			return nil
		},
	}
}

// promptSecret reads a line without echoing it. Falling back to an echoing read
// when stdin is not a terminal keeps the command usable under a pipe.
func promptSecret(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		r := bufio.NewReader(os.Stdin)
		line, err := r.ReadString('\n')
		return strings.TrimRight(line, "\n"), err
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	return string(b), err
}
