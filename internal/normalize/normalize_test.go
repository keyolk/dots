package normalize

import (
	"bytes"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

const unsorted = `apiVersion: v1
kind: Config
current-context: zeta
clusters:
- cluster:
    server: https://z.example
  name: zeta
- cluster:
    server: https://a.example
  name: alpha
contexts:
- context:
    cluster: zeta
    user: zeta
  name: zeta
- context:
    cluster: alpha
    user: alpha
  name: alpha
users:
- name: zeta
  user: {}
- name: alpha
  user: {}
`

// TestOrderingIsCanonicalised is the whole point: the same entries written in
// a different order must normalise to the same bytes, so a one-line change
// stops reading as a whole-file rewrite.
func TestOrderingIsCanonicalised(t *testing.T) {
	// Same content, the two clusters swapped.
	swapped := strings.Replace(unsorted,
		`- cluster:
    server: https://z.example
  name: zeta
- cluster:
    server: https://a.example
  name: alpha`,
		`- cluster:
    server: https://a.example
  name: alpha
- cluster:
    server: https://z.example
  name: zeta`, 1)
	if swapped == unsorted {
		t.Fatal("the fixture did not actually change")
	}

	a, err := Apply(KubeConfig, []byte(unsorted))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	b, err := Apply(KubeConfig, []byte(swapped))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("reordered input did not normalise to the same bytes:\n%s\n---\n%s", a, b)
	}
}

func TestSortIsByName(t *testing.T) {
	out, err := Apply(KubeConfig, []byte(unsorted))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// The encoder re-indents, so match a name at whatever depth it lands.
	names := regexp.MustCompile(`(?m)^\s*(?:- )?name: (\S+)$`).FindAllStringSubmatch(string(out), -1)
	if len(names) < 2 {
		t.Fatalf("expected several names, got %v", names)
	}
	if names[0][1] != "alpha" {
		t.Fatalf("first cluster is %q, want alpha", names[0][1])
	}
}

// TestRealContentSurvives guards the failure that would matter most: dropping
// or corrupting a cluster while tidying the order.
func TestRealContentSurvives(t *testing.T) {
	out, err := Apply(KubeConfig, []byte(unsorted))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, want := range []string{"https://z.example", "https://a.example", "current-context: zeta"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("%q was lost:\n%s", want, out)
		}
	}
}

// TestGenuineChangeStillDiffers is the other half: normalising must not hide a
// real edit, only the reordering around it.
func TestGenuineChangeStillDiffers(t *testing.T) {
	changed := strings.Replace(unsorted, "current-context: zeta", "current-context: alpha", 1)
	a, _ := Apply(KubeConfig, []byte(unsorted))
	b, _ := Apply(KubeConfig, []byte(changed))
	if bytes.Equal(a, b) {
		t.Fatal("a changed current-context was normalised away")
	}
}

func TestUnparseableInputIsReturnedUnchanged(t *testing.T) {
	// A config that fails to round-trip is worse than a noisy diff, so bad
	// input is passed through rather than mangled.
	junk := []byte("this: is: not: valid: yaml: [\n")
	out, err := Apply(KubeConfig, junk)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !bytes.Equal(out, junk) {
		t.Fatalf("unparseable input was altered: %q", out)
	}
}

func TestNoneIsAPassthrough(t *testing.T) {
	in := []byte("anything at all\n")
	out, err := Apply(None, in)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !bytes.Equal(out, in) {
		t.Fatal("None altered its input")
	}
}

func TestUnknownKindIsAnError(t *testing.T) {
	if _, err := Apply(Kind("nonsense"), []byte("x")); err == nil {
		t.Fatal("an unknown strategy was accepted")
	}
	if Known(Kind("nonsense")) {
		t.Fatal("Known accepted an unimplemented strategy")
	}
	if !Known(KubeConfig) || !Known(None) {
		t.Fatal("Known rejected an implemented strategy")
	}
}

// TestAgainstTheRealKubeconfig is the case this package was written for. It is
// skipped where there is no store to compare against.
func TestAgainstTheRealKubeconfig(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	live, err := os.ReadFile(home + "/.kube/config")
	if err != nil {
		t.Skip("no ~/.kube/config")
	}
	stored, err := exec.Command("git", "--git-dir="+home+"/.config.repo",
		"--work-tree="+home, "show", "HEAD:.kube/config").Output()
	if err != nil || len(stored) == 0 {
		t.Skip("kubeconfig is not in a store here")
	}

	a, err := Apply(KubeConfig, stored)
	if err != nil {
		t.Fatalf("Apply(stored): %v", err)
	}
	b, err := Apply(KubeConfig, live)
	if err != nil {
		t.Fatalf("Apply(live): %v", err)
	}

	// The live file legitimately drifts from the store -- a context switched, a
	// cluster added -- so equality is the wrong assertion; it would fail for
	// reasons that have nothing to do with normalising. What must hold is that
	// normalising is idempotent and order-insensitive on real content.
	if !bytes.Equal(a, mustApply(t, a)) {
		t.Error("normalising the stored kubeconfig twice changed it")
	}
	if !bytes.Equal(b, mustApply(t, b)) {
		t.Error("normalising the live kubeconfig twice changed it")
	}

	// Every entry the store holds must survive normalisation of the live file
	// if it is still there -- the sort must not drop anything.
	for _, name := range names(t, stored) {
		if contains(t, live, name) && !bytes.Contains(b, []byte(name)) {
			t.Errorf("%q was lost while normalising", name)
		}
	}
}

func mustApply(t *testing.T, in []byte) []byte {
	t.Helper()
	out, err := Apply(KubeConfig, in)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return out
}

// names pulls the entry names out of a kubeconfig without parsing YAML.
func names(t *testing.T, content []byte) []string {
	t.Helper()
	var out []string
	for _, m := range regexp.MustCompile(`(?m)^\s*(?:- )?name: (\S+)$`).FindAllSubmatch(content, -1) {
		out = append(out, string(m[1]))
	}
	return out
}

func contains(t *testing.T, content []byte, name string) bool {
	t.Helper()
	return bytes.Contains(content, []byte(name))
}
