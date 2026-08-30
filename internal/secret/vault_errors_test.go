package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenWithNoIdentityConfiguredExplainsWhatToDo(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "v.age"), "", nil)
	if err == nil {
		t.Fatal("Open with no identity succeeded")
	}
	if !strings.Contains(err.Error(), "secrets.identity") {
		t.Fatalf("error %q does not name the setting to fix", err)
	}
}

func TestOpenWithAMissingIdentityFileNamesThePath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.age")
	_, err := Open(filepath.Join(t.TempDir(), "v.age"), missing, nil)
	if err == nil {
		t.Fatal("Open with a missing identity succeeded")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("error %q does not name the missing file", err)
	}
}

func TestOpenWithAMalformedIdentityFails(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.age")
	if err := os.WriteFile(p, []byte("this is not an age key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(t.TempDir(), "v.age"), p, nil); err == nil {
		t.Fatal("Open with a malformed identity succeeded")
	}
}

func TestOpenRejectsAMalformedRecipient(t *testing.T) {
	// A typo in secrets.recipients must fail loudly at open rather than
	// silently dropping a machine from the recipient set on the next save.
	dir := t.TempDir()
	id := filepath.Join(dir, "identity.age")
	if _, err := GenerateIdentity(id); err != nil {
		t.Fatal(err)
	}
	_, err := Open(filepath.Join(dir, "v.age"), id, []string{"age1-not-a-real-key"})
	if err == nil {
		t.Fatal("a malformed recipient was accepted")
	}
	if !strings.Contains(err.Error(), "recipient") {
		t.Fatalf("error %q does not identify the bad recipient", err)
	}
}

func TestOpenIgnoresBlankAndCommentedRecipients(t *testing.T) {
	// A recipients list edited by hand accumulates comments and blank entries;
	// neither should be treated as a key.
	dir := t.TempDir()
	id := filepath.Join(dir, "identity.age")
	pub, err := GenerateIdentity(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(dir, "v.age"), id, []string{"", "  ", "# a comment", pub}); err != nil {
		t.Fatalf("Open with padded recipients: %v", err)
	}
}

func TestOpenOnACorruptVaultFails(t *testing.T) {
	// Truncated or garbage ciphertext must not be read as an empty vault, or a
	// subsequent save would silently destroy every secret.
	dir := t.TempDir()
	id := filepath.Join(dir, "identity.age")
	if _, err := GenerateIdentity(id); err != nil {
		t.Fatal(err)
	}
	vault := filepath.Join(dir, "v.age")
	if err := os.WriteFile(vault, []byte("not age ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(vault, id, nil); err == nil {
		t.Fatal("a corrupt vault opened successfully")
	}
}

func TestParseSkipsCommentsAndMalformedLines(t *testing.T) {
	v := &Vault{values: map[string]string{}}
	body := strings.NewReader(strings.Join([]string{
		"# a comment",
		"",
		"   ",
		"no-equals-sign-here",
		"good = value",
		"  spaced   =   trimmed  ",
	}, "\n"))
	if err := v.parse(body); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(v.values) != 2 {
		t.Fatalf("parsed %v, want only the two well-formed entries", v.values)
	}
	if v.values["spaced"] != "trimmed" {
		t.Fatalf("whitespace was not trimmed: %q", v.values["spaced"])
	}
}

func TestSaveCreatesTheVaultDirectory(t *testing.T) {
	// The vault path may point into a directory that does not exist yet on a
	// fresh machine.
	dir := t.TempDir()
	id := filepath.Join(dir, "identity.age")
	if _, err := GenerateIdentity(id); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "a", "b", "vault.age")

	v, err := Open(nested, id, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	v.Set("k", "value")
	if err := v.Save(); err != nil {
		t.Fatalf("Save into a missing directory: %v", err)
	}
	if _, err := os.Stat(nested); err != nil {
		t.Fatalf("vault was not created: %v", err)
	}
}

func TestSaveLeavesNoTemporaryFilesBehind(t *testing.T) {
	// The write goes through a temp file plus rename; leaking those into the
	// work tree would show up as untracked noise in every status.
	dir := t.TempDir()
	id := filepath.Join(dir, "identity.age")
	if _, err := GenerateIdentity(id); err != nil {
		t.Fatal(err)
	}
	v, err := Open(filepath.Join(dir, "vault.age"), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	v.Set("k", "v")
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}

	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".vault-") {
			t.Fatalf("temporary file %s left behind", e.Name())
		}
	}
}

func TestKeysAreSortedForStableDiffs(t *testing.T) {
	// The vault body is written in key order so re-saving an unchanged vault
	// produces a byte-identical plaintext, rather than a spurious change.
	v := &Vault{values: map[string]string{"z": "1", "a": "2", "m": "3"}}
	got := v.Keys()
	if len(got) != 3 || got[0] != "a" || got[1] != "m" || got[2] != "z" {
		t.Fatalf("Keys = %v, want sorted", got)
	}
}

func TestDeleteOfAnAbsentKeyIsHarmless(t *testing.T) {
	v := &Vault{values: map[string]string{"a": "1"}}
	v.Delete("not-there")
	if len(v.values) != 1 {
		t.Fatal("Delete of an absent key changed the vault")
	}
}

func TestGenerateIdentityRecordsThePublicKeyInTheFile(t *testing.T) {
	// The public key is what the user copies to grant another machine access,
	// so it has to survive in the file rather than only in the command output.
	p := filepath.Join(t.TempDir(), "identity.age")
	pub, err := GenerateIdentity(p)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), pub) {
		t.Fatal("the public key is not recorded in the identity file")
	}
}

func TestGenerateIdentityFailsOnAnUnwritablePath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "readonly")
	if err := os.MkdirAll(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700) //nolint:errcheck // best-effort cleanup

	if _, err := GenerateIdentity(filepath.Join(dir, "sub", "id.age")); err == nil {
		t.Skip("running as a user that bypasses directory permissions")
	}
}

func TestUnescapeLeavesUnknownEscapesIntact(t *testing.T) {
	// A value containing \t must round-trip as the literal two characters,
	// not be silently reinterpreted or dropped.
	if got := unescape(`a\tb`); got != `a\tb` {
		t.Fatalf("unescape = %q, want the escape preserved", got)
	}
	if got := unescape(`trailing\`); got != `trailing\` {
		t.Fatalf("unescape of a trailing backslash = %q", got)
	}
}

func TestEscapeUnescapeRoundTripsAwkwardValues(t *testing.T) {
	values := []string{
		"plain",
		"with = equals",
		"with\nnewline",
		`with\backslash`,
		`ends with backslash\`,
		"# looks like a comment",
		"",
	}
	for _, v := range values {
		if got := unescape(escape(v)); got != v {
			t.Fatalf("round trip of %q produced %q", v, got)
		}
	}
}
