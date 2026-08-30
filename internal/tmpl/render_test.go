package tmpl

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixedSecrets(pairs map[string]string) SecretFunc {
	return func(name string) (string, error) {
		v, ok := pairs[name]
		if !ok {
			return "", errors.New("secret not found: " + name)
		}
		return v, nil
	}
}

func TestSecretIsSubstituted(t *testing.T) {
	r := New(fixedSecrets(map[string]string{"anthropic/oauth": "sk-ant-oat01-real"}), "testhost")
	out, err := r.Render("config.json", []byte(`{"token": "{{ secret "anthropic/oauth" }}"}`))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if string(out) != `{"token": "sk-ant-oat01-real"}` {
		t.Fatalf("rendered %q", out)
	}
}

// TestMissingSecretIsAnError guards the failure mode that would be worst in
// practice: rendering an empty token produces a config that fails much later
// with an unrelated-looking authentication error.
func TestMissingSecretIsAnError(t *testing.T) {
	r := New(fixedSecrets(nil), "testhost")
	if _, err := r.Render("x", []byte(`{{ secret "absent" }}`)); err == nil {
		t.Fatal("a missing secret rendered successfully")
	}
}

func TestRenderWithoutVaultReportsWhy(t *testing.T) {
	r := New(nil, "testhost")
	_, err := r.Render("x", []byte(`{{ secret "k" }}`))
	if err == nil {
		t.Fatal("secret call with no vault rendered successfully")
	}
	if !strings.Contains(err.Error(), "vault") {
		t.Fatalf("error %q does not mention the missing vault", err)
	}
}

func TestMachineFactsAreAvailable(t *testing.T) {
	r := New(nil, "my-laptop")
	out, err := r.Render("x", []byte(`{{ .Host }}/{{ .OS }}`))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.HasPrefix(string(out), "my-laptop/") {
		t.Fatalf("rendered %q, want the host name", out)
	}
}

func TestOSConditionalSelectsABranch(t *testing.T) {
	r := New(nil, "h")
	out, err := r.Render("x", []byte(`{{ if .IsDarwin }}brew{{ else }}apt{{ end }}`))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if s := string(out); s != "brew" && s != "apt" {
		t.Fatalf("rendered %q, want one branch", s)
	}
}

func TestUnknownFieldIsAnError(t *testing.T) {
	// missingkey=error turns a typo into a build-time failure rather than a
	// config file with "<no value>" silently written into it.
	r := New(nil, "h")
	if _, err := r.Render("x", []byte(`{{ .Nonexistent }}`)); err == nil {
		t.Fatal("an unknown field rendered without error")
	}
}

func TestRenderFileWritesSecretBearingOutputAs0600(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "config.json.tmpl")
	dst := filepath.Join(dir, "config.json")
	if err := os.WriteFile(src, []byte(`{"t":"{{ secret "k" }}"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New(fixedSecrets(map[string]string{"k": "value"}), "h")
	if err := r.RenderFile(src, dst); err != nil {
		t.Fatalf("RenderFile: %v", err)
	}

	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("rendered secret-bearing file is mode %04o, want no group/other access", perm)
	}
}

func TestRenderFileKeepsPlainOutputReadable(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "plain.conf.tmpl")
	dst := filepath.Join(dir, "plain.conf")
	if err := os.WriteFile(src, []byte(`os = {{ .OS }}`), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New(nil, "h")
	if err := r.RenderFile(src, dst); err != nil {
		t.Fatalf("RenderFile: %v", err)
	}
	fi, _ := os.Stat(dst)
	if perm := fi.Mode().Perm(); perm != 0o644 {
		t.Fatalf("plain rendered file is mode %04o, want 0644", perm)
	}
}

func TestRenderFilePreservesExecutableBit(t *testing.T) {
	// A rendered wrapper script that loses its +x is a broken command with a
	// confusing error.
	dir := t.TempDir()
	src := filepath.Join(dir, "run.sh.tmpl")
	dst := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(src, []byte("#!/bin/sh\necho {{ .OS }}\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := New(nil, "h").RenderFile(src, dst); err != nil {
		t.Fatalf("RenderFile: %v", err)
	}
	fi, _ := os.Stat(dst)
	if fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("rendered file is mode %04o, want the executable bit kept", fi.Mode().Perm())
	}
}

func TestFailedRenderLeavesNoPartialFile(t *testing.T) {
	// Writing through a temp file plus rename matters here: a half-written
	// config that happens to parse is worse than none at all.
	dir := t.TempDir()
	src := filepath.Join(dir, "bad.tmpl")
	dst := filepath.Join(dir, "bad")
	if err := os.WriteFile(src, []byte(`ok {{ secret "absent" }}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := New(fixedSecrets(nil), "h").RenderFile(src, dst); err == nil {
		t.Fatal("RenderFile succeeded with a missing secret")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("a partial file was left behind after a failed render")
	}
}

func TestTargetStripsSuffix(t *testing.T) {
	if got := Target(".ccproxy/config.json.tmpl"); got != ".ccproxy/config.json" {
		t.Fatalf("Target = %q", got)
	}
	if !IsTemplate("a.tmpl") || IsTemplate("a.json") {
		t.Fatal("IsTemplate misclassified a path")
	}
}

func TestDefaultFillsEmptyValue(t *testing.T) {
	r := New(nil, "h")
	out, err := r.Render("x", []byte(`{{ env "DOTS_UNSET_IN_TEST" | default "fallback" }}`))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if string(out) != "fallback" {
		t.Fatalf("rendered %q, want the fallback", out)
	}
}

func TestShRunsACommand(t *testing.T) {
	r := New(nil, "h")
	out, err := r.Render("x", []byte(`{{ sh "echo hello" }}`))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if string(out) != "hello" {
		t.Fatalf("rendered %q, want trimmed command output", out)
	}
}

func TestQuoteEscapesForEmbedding(t *testing.T) {
	// A secret containing a quote would break the JSON it is rendered into
	// unless it goes through quote.
	r := New(fixedSecrets(map[string]string{"k": `has"quote`}), "h")
	out, err := r.Render("x", []byte(`{{ secret "k" | quote }}`))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if string(out) != `"has\"quote"` {
		t.Fatalf("quote produced %q", out)
	}
}

func TestShFailureIsReported(t *testing.T) {
	// A silently-empty sh result would render a config that looks valid and
	// behaves wrongly.
	r := New(nil, "h")
	if _, err := r.Render("x", []byte(`{{ sh "exit 3" }}`)); err == nil {
		t.Fatal("a failing sh command rendered successfully")
	}
}

func TestMalformedTemplateIsReportedWithItsName(t *testing.T) {
	r := New(nil, "h")
	_, err := r.Render("config.json.tmpl", []byte(`{{ unclosed `))
	if err == nil {
		t.Fatal("a malformed template parsed successfully")
	}
	if !strings.Contains(err.Error(), "config.json.tmpl") {
		t.Fatalf("error %q does not name the template", err)
	}
}

func TestRenderFileOfAMissingSourceFails(t *testing.T) {
	dir := t.TempDir()
	err := New(nil, "h").RenderFile(filepath.Join(dir, "absent.tmpl"), filepath.Join(dir, "out"))
	if err == nil {
		t.Fatal("RenderFile of a missing source succeeded")
	}
}

func TestRenderFileCreatesTheTargetDirectory(t *testing.T) {
	// A template may render into a directory that does not exist yet, e.g. on
	// a machine where the tool has never run.
	dir := t.TempDir()
	src := filepath.Join(dir, "x.tmpl")
	if err := os.WriteFile(src, []byte("os = {{ .OS }}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "nested", "deeper", "x")

	if err := New(nil, "h").RenderFile(src, dst); err != nil {
		t.Fatalf("RenderFile: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("target was not created: %v", err)
	}
}

func TestRenderFileOverwritesAnExistingTarget(t *testing.T) {
	// apply is re-run routinely; the second run must replace the first result
	// rather than fail or append.
	dir := t.TempDir()
	src := filepath.Join(dir, "x.tmpl")
	dst := filepath.Join(dir, "x")
	if err := os.WriteFile(src, []byte("value = {{ .Host }}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("stale content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := New(nil, "first-host").RenderFile(src, dst); err != nil {
		t.Fatalf("RenderFile: %v", err)
	}
	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "stale") {
		t.Fatalf("the stale target survived: %q", body)
	}
}

func TestEnvReadsASetVariable(t *testing.T) {
	t.Setenv("DOTS_TEST_VAR", "from-env")
	out, err := New(nil, "h").Render("x", []byte(`{{ env "DOTS_TEST_VAR" }}`))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if string(out) != "from-env" {
		t.Fatalf("rendered %q", out)
	}
}

func TestHomeAndArchAreExposed(t *testing.T) {
	out, err := New(nil, "h").Render("x", []byte(`{{ .Home }}|{{ .Arch }}`))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	parts := strings.Split(string(out), "|")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		t.Fatalf("rendered %q, want both facts populated", out)
	}
}
