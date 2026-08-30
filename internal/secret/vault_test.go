package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestVault builds an isolated vault with a freshly generated identity.
// HOME is not read by this package, but the identity and vault must not land
// in the developer's real ~/.config either way.
func newTestVault(t *testing.T) (*Vault, string) {
	t.Helper()
	dir := t.TempDir()
	idPath := filepath.Join(dir, "identity.age")
	if _, err := GenerateIdentity(idPath); err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	vaultPath := filepath.Join(dir, "vault.age")
	v, err := Open(vaultPath, idPath, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return v, idPath
}

func TestOpenMissingVaultIsEmptyNotAnError(t *testing.T) {
	// A machine that has an identity but has never stored a secret must be
	// able to run `dots secret set` rather than having to create the file
	// out of band first.
	v, _ := newTestVault(t)
	if got := len(v.Keys()); got != 0 {
		t.Fatalf("fresh vault has %d keys, want 0", got)
	}
}

func TestSetSaveReopenRoundTrips(t *testing.T) {
	v, idPath := newTestVault(t)
	v.Set("anthropic/oauth", "sk-ant-oat01-example")
	if err := v.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened, err := Open(v.path, idPath, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reopened.Get("anthropic/oauth")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "sk-ant-oat01-example" {
		t.Fatalf("Get = %q, want the stored value", got)
	}
}

func TestMultiLineValueSurvivesRoundTrip(t *testing.T) {
	// The line-oriented vault body would silently truncate a PEM block if the
	// escaping were wrong, and the failure would only surface much later as an
	// unusable key.
	key := "-----BEGIN PRIVATE KEY-----\nabc\ndef\n-----END PRIVATE KEY-----"
	v, idPath := newTestVault(t)
	v.Set("tls/key", key)
	if err := v.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened, err := Open(v.path, idPath, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reopened.Get("tls/key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != key {
		t.Fatalf("multi-line value round-tripped as %q", got)
	}
}

func TestBackslashInValueIsNotMangled(t *testing.T) {
	v, idPath := newTestVault(t)
	const val = `C:\path\nnot-a-newline`
	v.Set("windows/path", val)
	if err := v.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reopened, _ := Open(v.path, idPath, nil)
	got, err := reopened.Get("windows/path")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != val {
		t.Fatalf("Get = %q, want %q", got, val)
	}
}

func TestGetMissingKeyReportsNotFound(t *testing.T) {
	v, _ := newTestVault(t)
	if _, err := v.Get("absent"); err == nil {
		t.Fatal("Get on a missing key returned no error")
	} else if !strings.Contains(err.Error(), "absent") {
		t.Fatalf("error %q does not name the missing key", err)
	}
}

func TestDeleteRemovesFromPersistedVault(t *testing.T) {
	v, idPath := newTestVault(t)
	v.Set("gone", "value")
	if err := v.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	v.Delete("gone")
	if err := v.Save(); err != nil {
		t.Fatalf("Save after delete: %v", err)
	}

	reopened, _ := Open(v.path, idPath, nil)
	if _, err := reopened.Get("gone"); err == nil {
		t.Fatal("deleted key still readable after reopen")
	}
}

func TestVaultFileIsNotReadableByOthers(t *testing.T) {
	v, _ := newTestVault(t)
	v.Set("k", "v")
	if err := v.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(v.path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("vault is mode %04o, want no group/other access", perm)
	}
}

func TestVaultFileIsActuallyEncrypted(t *testing.T) {
	// The whole point is that this file can sit in a git repo. If the plaintext
	// key names or values were readable, every other guarantee is moot.
	v, _ := newTestVault(t)
	v.Set("github/token", "ghp_supersecretvalue")
	if err := v.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(v.path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for _, needle := range []string{"github/token", "ghp_supersecretvalue"} {
		if strings.Contains(string(raw), needle) {
			t.Fatalf("plaintext %q found in the encrypted vault", needle)
		}
	}
}

func TestWrongIdentityCannotDecrypt(t *testing.T) {
	v, _ := newTestVault(t)
	v.Set("k", "v")
	if err := v.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	otherDir := t.TempDir()
	otherID := filepath.Join(otherDir, "identity.age")
	if _, err := GenerateIdentity(otherID); err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if _, err := Open(v.path, otherID, nil); err == nil {
		t.Fatal("a foreign identity decrypted the vault")
	}
}

func TestGenerateIdentityRefusesToOverwrite(t *testing.T) {
	// Silently replacing an identity would make every existing vault
	// undecryptable, with no way back.
	dir := t.TempDir()
	p := filepath.Join(dir, "identity.age")
	if _, err := GenerateIdentity(p); err != nil {
		t.Fatalf("first GenerateIdentity: %v", err)
	}
	if _, err := GenerateIdentity(p); err == nil {
		t.Fatal("GenerateIdentity overwrote an existing identity")
	}
}

func TestIdentityFileIsNotReadableByOthers(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "identity.age")
	if _, err := GenerateIdentity(p); err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("identity is mode %04o, want no group/other access", perm)
	}
}

func TestExtraRecipientCanAlsoDecrypt(t *testing.T) {
	// This is the second-machine path: a key that was not used to write the
	// vault must still open it when listed as a recipient.
	dirA, dirB := t.TempDir(), t.TempDir()
	idA := filepath.Join(dirA, "a.age")
	idB := filepath.Join(dirB, "b.age")
	if _, err := GenerateIdentity(idA); err != nil {
		t.Fatalf("GenerateIdentity A: %v", err)
	}
	pubB, err := GenerateIdentity(idB)
	if err != nil {
		t.Fatalf("GenerateIdentity B: %v", err)
	}

	vaultPath := filepath.Join(dirA, "vault.age")
	// Writing machine A must list itself too, or it locks itself out — the
	// same footgun a real recipients list has.
	pubA := publicKeyOf(t, idA)
	v, err := Open(vaultPath, idA, []string{pubA, pubB})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	v.Set("shared", "value")
	if err := v.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	fromB, err := Open(vaultPath, idB, []string{pubA, pubB})
	if err != nil {
		t.Fatalf("Open as B: %v", err)
	}
	if got, err := fromB.Get("shared"); err != nil || got != "value" {
		t.Fatalf("B read %q, %v; want the shared value", got, err)
	}
}

// publicKeyOf reads the public key GenerateIdentity recorded in the comment
// header, which is the same thing a user copies out of `dots secret keygen`.
func publicKeyOf(t *testing.T, identityPath string) string {
	t.Helper()
	b, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if _, pub, ok := strings.Cut(line, "# public key: "); ok {
			return strings.TrimSpace(pub)
		}
	}
	t.Fatalf("no public key comment in %s", identityPath)
	return ""
}
