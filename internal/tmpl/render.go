// Package tmpl renders config files that carry secrets inline.
//
// The problem it solves: .ccproxy/config.json is 95% ordinary config and one
// OAuth token. A file-level config/secret split cannot express that — the file
// either leaks the token or the config stops being reviewable. Here the
// committed artifact is `.ccproxy/config.json.tmpl` with a {{ secret ... }}
// call where the token goes, and the rendered file never enters git.
package tmpl

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
)

// Suffix marks a tracked file as a template. Rendering strips it.
const Suffix = ".tmpl"

// SecretFunc resolves a secret name to its value.
type SecretFunc func(name string) (string, error)

// Renderer renders templates against a machine's facts and secret vault.
type Renderer struct {
	secret SecretFunc
	data   map[string]any
}

// New builds a renderer. host is passed in rather than read here so callers
// can override it for testing and for per-host manifests.
func New(secret SecretFunc, host string) *Renderer {
	return &Renderer{
		secret: secret,
		data: map[string]any{
			"OS":       runtime.GOOS,
			"Arch":     runtime.GOARCH,
			"Host":     host,
			"Home":     home(),
			"IsDarwin": runtime.GOOS == "darwin",
			"IsLinux":  runtime.GOOS == "linux",
		},
	}
}

// IsTemplate reports whether a tracked path is a template.
func IsTemplate(path string) bool { return strings.HasSuffix(path, Suffix) }

// Target returns the rendered path for a template path.
func Target(path string) string { return strings.TrimSuffix(path, Suffix) }

// Render executes one template body.
func (r *Renderer) Render(name string, body []byte) ([]byte, error) {
	t, err := template.New(name).
		Option("missingkey=error").
		Funcs(r.funcs()).
		Parse(string(body))
	if err != nil {
		return nil, fmt.Errorf("parse template %s: %w", name, err)
	}
	var out bytes.Buffer
	if err := t.Execute(&out, r.data); err != nil {
		return nil, fmt.Errorf("render %s: %w", name, err)
	}
	return out.Bytes(), nil
}

// RenderFile renders src and writes it to dst with mode, atomically.
//
// The rendered file is written 0600 when it contained a secret and 0644
// otherwise: a rendered config that embeds a token must not stay world-readable
// just because its template was.
func (r *Renderer) RenderFile(src, dst string) error {
	body, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	hadSecret := bytes.Contains(body, []byte("secret "))
	out, err := r.Render(filepath.Base(src), body)
	if err != nil {
		return err
	}

	mode := os.FileMode(0o644)
	if hadSecret {
		mode = 0o600
	}
	if st, err := os.Stat(src); err == nil && st.Mode().Perm()&0o111 != 0 {
		mode |= 0o111
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".dots-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}

func (r *Renderer) funcs() template.FuncMap {
	return template.FuncMap{
		// secret pulls from the age vault. A missing secret is a hard error:
		// silently rendering an empty token produces a config that fails at
		// runtime with a far less obvious message.
		"secret": func(name string) (string, error) {
			if r.secret == nil {
				return "", fmt.Errorf("secret %q requested but no vault is open", name)
			}
			return r.secret(name)
		},
		// env reads an environment variable, empty if unset.
		"env": os.Getenv,
		// sh runs a command and returns trimmed stdout. For values that only a
		// local tool knows (a keychain lookup, a hostname-derived path).
		"sh": func(cmdline string) (string, error) {
			out, err := exec.Command("sh", "-c", cmdline).Output()
			if err != nil {
				return "", fmt.Errorf("sh %q: %w", cmdline, err)
			}
			return strings.TrimRight(string(out), "\n"), nil
		},
		"default": func(fallback, val string) string {
			if val == "" {
				return fallback
			}
			return val
		},
		"quote": func(s string) string { return fmt.Sprintf("%q", s) },
	}
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}
