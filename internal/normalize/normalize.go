// Package normalize removes churn from files a tool rewrites on every run.
//
// Some config files are owned by a program that reorders them freely.
// ~/.kube/config is the case this exists for: kubectl rewrites it whenever a
// context is touched, and the 70 clusters, 71 contexts and 70 users come back
// in a different order each time. The result is a 1951-line diff for what is
// almost always a one-line change — enough noise that a real edit disappears
// into it.
//
// Normalising sorts those lists by name before comparing or committing, so the
// diff shows what actually changed. It is a display and storage concern only:
// the file on disk is left alone, since kubectl owns it.
package normalize

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Kind names a normalisation strategy a manifest group can ask for.
type Kind string

const (
	// None leaves content untouched.
	None Kind = ""
	// KubeConfig sorts a kubeconfig's clusters, contexts and users by name.
	KubeConfig Kind = "kubeconfig"
)

// Known reports whether a manifest names a strategy this package implements.
func Known(k Kind) bool {
	switch k {
	case None, KubeConfig:
		return true
	}
	return false
}

// Apply returns the normalised form of content. An input the strategy cannot
// parse is returned unchanged rather than mangled: a config file that fails to
// round-trip is worse than a noisy diff.
func Apply(k Kind, content []byte) ([]byte, error) {
	switch k {
	case None:
		return content, nil
	case KubeConfig:
		return kubeconfig(content)
	}
	return nil, fmt.Errorf("unknown normalisation %q", k)
}

// kubeconfig sorts the three list fields kubectl reorders.
func kubeconfig(content []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return content, nil //nolint:nilerr // unparseable input is left as-is
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return content, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return content, nil
	}

	// A yaml.Node tree is used rather than a map so that comments, key order
	// and scalar style survive; only the three sequences move.
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, val := root.Content[i], root.Content[i+1]
		switch key.Value {
		case "clusters", "contexts", "users":
			if val.Kind == yaml.SequenceNode {
				sortByName(val)
			}
		}
	}

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return content, nil //nolint:nilerr // fall back to the original
	}
	if err := enc.Close(); err != nil {
		return content, nil //nolint:nilerr
	}
	return out.Bytes(), nil
}

// sortByName orders a sequence of mappings by their "name" field, using a
// stable insertion sort so entries without a name keep their relative order.
func sortByName(seq *yaml.Node) {
	name := func(n *yaml.Node) string {
		if n.Kind != yaml.MappingNode {
			return ""
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == "name" {
				return n.Content[i+1].Value
			}
		}
		return ""
	}
	items := seq.Content
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && name(items[j]) < name(items[j-1]); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}
