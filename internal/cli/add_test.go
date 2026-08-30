package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestScanSecretsCatchesRealCredentialShapes uses the shapes actually found on
// this machine, including the .ccproxy OAuth token that sits one careless
// `config add` away from being committed.
func TestScanSecretsCatchesRealCredentialShapes(t *testing.T) {
	cases := map[string]string{
		"anthropic oauth": `{"token": "sk-ant-oat01-AbCdEfGhIjKlMnOpQrStUv"}`,
		"github classic":  `oauth_token: ghp_0FAKE0FAKE0FAKE0FAKE0FAKE0FAKE0FAKE0`,
		"github pat":      `token = github_pat_11ABCDEFG0abcdefghijklmnopqrstuvwxyz0123456789ABCD`,
		"aws key":         `aws_access_key_id = AKIAIOSFODNN7EXAMPLE`,
		"slack bot":       `SLACK_TOKEN=xoxb-123456789012-abcdefghijkl`,
		"private key":     "-----BEGIN OPENSSH PRIVATE KEY-----\nbody\n",
		"doppler":         `DOPPLER_TOKEN=dp.st.dev.aBcDeFgHiJkLmNoPqRsTuVwXyZ012345`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if got := scanSecrets(writeTemp(t, body)); got == "" {
				t.Fatalf("credential not detected in %q", body)
			}
		})
	}
}

func TestScanSecretsReportsLineNumber(t *testing.T) {
	body := "line one\nline two\ntoken: ghp_0FAKE0FAKE0FAKE0FAKE0FAKE0FAKE0FAKE0\n"
	got := scanSecrets(writeTemp(t, body))
	if !strings.Contains(got, "line 3") {
		t.Fatalf("report %q does not name the offending line", got)
	}
}

// TestScanSecretsDoesNotPrintTheWholeCredential matters because the report goes
// to a terminal and into scrollback: a guard that leaks the secret while
// refusing it has defeated itself.
func TestScanSecretsDoesNotPrintTheWholeCredential(t *testing.T) {
	const token = "ghp_0FAKE0FAKE0FAKE0FAKE0FAKE0FAKE0FAKE0"
	got := scanSecrets(writeTemp(t, "token: "+token))
	if strings.Contains(got, token) {
		t.Fatalf("the full credential appears in the report: %q", got)
	}
}

func TestScanSecretsAllowsOrdinaryConfig(t *testing.T) {
	clean := []string{
		"set -x EDITOR nvim\n",
		`{"model": "claude-opus-5", "context_window": 272000}`,
		"# a comment mentioning a github token in prose\n",
		`base_url = "https://claude-3p.delight.ai"`,
	}
	for _, body := range clean {
		if got := scanSecrets(writeTemp(t, body)); got != "" {
			t.Fatalf("false positive on %q: %s", body, got)
		}
	}
}

func TestScanSecretsSkipsBinaryFiles(t *testing.T) {
	// ~/.local/bin holds ~60 compiled binaries. Scanning them for text patterns
	// would produce noise at best and block a legitimate add at worst.
	p := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(p, []byte{0x7f, 'E', 'L', 'F', 0x00, 0x01, 0x02}, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := scanSecrets(p); got != "" {
		t.Fatalf("binary file flagged: %s", got)
	}
}

func TestScanSecretsHandlesMissingFile(t *testing.T) {
	// A path that vanished between scan and add must not panic the command.
	if got := scanSecrets(filepath.Join(t.TempDir(), "absent")); got != "" {
		t.Fatalf("missing file produced a finding: %s", got)
	}
}

func TestSafePrefixNeverReturnsTheWholeInput(t *testing.T) {
	for _, in := range []string{"short", "sk-ant-oat01-abcdefghijklmnop"} {
		if got := safePrefix(in); got == in && len(in) > 1 {
			t.Fatalf("safePrefix(%q) returned the whole value", in)
		}
	}
}
