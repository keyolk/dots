// Package secret implements dots's secret backend: an age-encrypted key-value
// vault plus the identity that opens it.
//
// The vault is a single encrypted file rather than one file per secret. A
// per-secret layout leaks the shape of the secret set through filenames — the
// existing .password-store does exactly that, publishing every account name in
// the clear — and makes an atomic multi-secret rotation impossible.
package secret

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"filippo.io/age"
)

// ErrNotFound is returned by Get when a key is absent from the vault.
var ErrNotFound = errors.New("secret not found")

// Vault is a decrypted key-value set together with the material needed to seal
// it again.
type Vault struct {
	path       string
	identities []age.Identity
	recipients []age.Recipient

	values map[string]string
}

// Open decrypts the vault at path. A missing file is not an error: it yields an
// empty vault, so `dots secret set` works on a machine that has never had one.
func Open(path, identityFile string, recipientStrings []string) (*Vault, error) {
	ids, err := loadIdentities(identityFile)
	if err != nil {
		return nil, err
	}
	recips, err := parseRecipients(recipientStrings)
	if err != nil {
		return nil, err
	}
	// With no explicit recipients, encrypt back to the identity's own public
	// key. A single-machine setup then needs no recipient bookkeeping at all.
	if len(recips) == 0 {
		for _, id := range ids {
			if x, ok := id.(*age.X25519Identity); ok {
				recips = append(recips, x.Recipient())
			}
		}
	}
	if len(recips) == 0 {
		return nil, errors.New("no age recipients: set secrets.recipients or use an X25519 identity")
	}

	v := &Vault{path: path, identities: ids, recipients: recips, values: map[string]string{}}

	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open vault: %w", err)
	}
	defer f.Close()

	r, err := age.Decrypt(f, ids...)
	if err != nil {
		return nil, fmt.Errorf("decrypt vault %s: %w", path, err)
	}
	if err := v.parse(r); err != nil {
		return nil, err
	}
	return v, nil
}

// parse reads the plaintext vault body: `key = value`, one per line, value
// newline-escaped. Comments and blanks are skipped.
func (v *Vault) parse(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v.values[strings.TrimSpace(k)] = unescape(strings.TrimSpace(val))
	}
	return sc.Err()
}

// Get returns one secret.
func (v *Vault) Get(key string) (string, error) {
	val, ok := v.values[key]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return val, nil
}

// Set stores one secret in memory. Call Save to persist.
func (v *Vault) Set(key, val string) { v.values[key] = val }

// Delete removes one secret from memory. Call Save to persist.
func (v *Vault) Delete(key string) { delete(v.values, key) }

// Keys returns the secret names, sorted. Values are never returned in bulk —
// listing names is a routine operation, dumping values is not.
func (v *Vault) Keys() []string {
	out := make([]string, 0, len(v.values))
	for k := range v.values {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Save re-encrypts the vault to all recipients and writes it atomically.
func (v *Vault) Save() error {
	var plain bytes.Buffer
	plain.WriteString("# dots vault - age encrypted, do not edit directly\n")
	for _, k := range v.Keys() {
		fmt.Fprintf(&plain, "%s = %s\n", k, escape(v.values[k]))
	}

	if err := os.MkdirAll(filepath.Dir(v.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(v.path), ".vault-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	w, err := age.Encrypt(tmp, v.recipients...)
	if err != nil {
		tmp.Close()
		return fmt.Errorf("encrypt vault: %w", err)
	}
	if _, err := w.Write(plain.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	// The age writer must be closed before the file: closing it flushes the
	// final chunk and the payload MAC. Closing the file first would truncate
	// the ciphertext into something that decrypts as corrupt.
	if err := w.Close(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, v.path)
}

func loadIdentities(path string) ([]age.Identity, error) {
	if path == "" {
		return nil, errors.New("no age identity configured: set secrets.identity")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open age identity %s: %w", path, err)
	}
	defer f.Close()

	ids, err := age.ParseIdentities(f)
	if err != nil {
		return nil, fmt.Errorf("parse age identity %s: %w", path, err)
	}
	return ids, nil
}

func parseRecipients(specs []string) ([]age.Recipient, error) {
	var out []age.Recipient
	for _, s := range specs {
		s = strings.TrimSpace(s)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		r, err := age.ParseX25519Recipient(s)
		if err != nil {
			return nil, fmt.Errorf("parse recipient %q: %w", s, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// escape/unescape keep multi-line values (PEM blocks, JSON) on one line so the
// vault body stays a simple line-oriented format.
func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case '\\':
				b.WriteByte('\\')
			default:
				b.WriteByte('\\')
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// GenerateIdentity writes a fresh age keypair to path and returns the public
// key. Used by bootstrap on a machine with no key yet.
func GenerateIdentity(path string) (string, error) {
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("identity already exists at %s", path)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	body := fmt.Sprintf("# created by dots\n# public key: %s\n%s\n",
		id.Recipient(), id)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", err
	}
	return id.Recipient().String(), nil
}
