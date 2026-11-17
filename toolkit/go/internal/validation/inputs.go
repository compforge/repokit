package validation

import (
	"crypto/sha256"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	cg "github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go/internal/analysis"
	"github.com/compforge/repokit/toolkit/go/internal/project"
)

// CheckInputDeclaration extends the owning Component inputs independently of
// affected-file selection. Components and Paths add explicit cross-Component inputs.
// Witnesses are caller-selected external/ignored receipt files, never their contents.
// DisableReuse permits real checks in environments without trustworthy receipts.
type CheckInputDeclaration struct {
	Components   []string `json:"components,omitempty"`
	Paths        []string `json:"paths,omitempty"`
	Witnesses    []string `json:"witnesses,omitempty"`
	DisableReuse bool     `json:"disableReuse,omitempty"`
}

// inputEntry records content, not source text. Version entries remain in the
// complete manifest and are ignored only when comparing reusable evidence.
type inputEntry struct {
	Kind    string `json:"kind"`
	Digest  string `json:"digest"`
	Version bool   `json:"version,omitempty"`
}
type inputManifest struct {
	linkInputs    map[string][]string   // Derived from the live capture; history stores the selected paths and their digests.
	SchemaVersion int                   `json:"schemaVersion"`
	Checkout      string                `json:"checkout"`
	Snapshot      string                `json:"snapshot"`
	Files         map[string]inputEntry `json:"files"`
}

// +spec=One round hashes each captured file once, independently of check count
// +link=component://docs/validation.md
func captureInputs(captured *analysis.CapturedSnapshot) inputManifest {
	m := inputManifest{SchemaVersion: 1, Checkout: captured.Report.Checkout, Snapshot: captured.Report.Snapshot, Files: map[string]inputEntry{}}
	put := func(path, kind string, data []byte) {
		document := cg.Document{Path: path, Gitlink: captured.Contents.Modules[path], Symlink: captured.Contents.Links[path]}
		version := (kind == "file" || kind == "opaque") && slices.Contains(project.FileTags(document), cg.VersionTag)
		m.Files[path] = inputEntry{Kind: kind, Digest: fmt.Sprintf("%x", sha256.Sum256(data)), Version: version}
	}
	for path, data := range captured.Contents.Files {
		put(path, "file", data)
	}
	for kind, entries := range map[string]map[string]string{"symlink": captured.Contents.Links, "gitlink": captured.Contents.Modules, "opaque": captured.Contents.Opaque} {
		for path, value := range entries {
			put(path, kind, []byte(value))
		}
	}
	m.linkInputs = capturedLinkInputs(m.Files, captured.Contents.Links)
	// A link makes its target a content dependency, including VERSION files.
	// Persist that distinction with the digest so history applies the same rule.
	for _, names := range m.linkInputs {
		for _, name := range names {
			entry := m.Files[name]
			entry.Version = false
			m.Files[name] = entry
		}
	}
	return m
}

// Validate declarations without filtering or hashing repository content.
func validateDeclaration(checkout string, layout project.Layout, component string, d CheckInputDeclaration) (map[string]string, error) {
	if d.Components != nil || d.Paths != nil {
		for _, root := range append(slices.Clone(d.Components), component) {
			if !slices.ContainsFunc(layout.Components, func(c project.Binding) bool { return c.Root == root }) {
				return nil, fmt.Errorf("unknown input Component %q", root)
			}
		}
		for _, path := range d.Paths {
			if path == "" || filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(path)) != path || path == ".." || strings.HasPrefix(path, "../") {
				return nil, fmt.Errorf("input path %q must be normalized and repository-relative", path)
			}
		}
	}
	witnesses := map[string]string{}
	for _, path := range d.Witnesses {
		absolute := path
		if !filepath.IsAbs(absolute) {
			absolute = filepath.Join(checkout, path)
		}
		value, err := validationWitness(absolute)
		if err != nil {
			return nil, fmt.Errorf("input witness %q: %w", path, err)
		}
		witnesses[absolute] = value
	}
	return witnesses, nil
}

// +spec=Checks observe their owning Component plus explicit inputs; nested Components and shared files never imply environment dependencies.
// +link=component://docs/validation.md
func selectInputs(m inputManifest, layout project.Layout, component string, d CheckInputDeclaration) ([]string, string) {
	selected := []string{}
	for name := range m.Files {
		owner := layout.Owner(name)
		belongs := owner != nil && (owner.Root == component || slices.Contains(d.Components, owner.Root))
		if belongs || slices.ContainsFunc(d.Paths, func(root string) bool { return root == "." || name == root || strings.HasPrefix(name, root+"/") }) {
			selected = append(selected, name)
		}
	}
	reason := "Component inputs"
	if len(d.Components) > 0 || len(d.Paths) > 0 {
		reason += "; explicit Component and shared-path inputs"
	}

	// A file link declares a content dependency, not an entire Component or Bin.
	linked := false
	for _, name := range slices.Clone(selected) {
		if inputs := m.linkInputs[name]; len(inputs) > 0 {
			linked = true
			selected = append(selected, inputs...)
		}
	}
	if linked {
		reason += "; captured file symlink targets"
	}
	slices.Sort(selected)
	return slices.Compact(selected), reason
}

// Compare both path sets so additions, removals and scope changes invalidate runs.
// +spec=Unreferenced VersionTag files may change between rounds; link payloads and linked content always participate in reuse identity.
func sameInputs(before inputManifest, previous []string, after inputManifest, current []string) bool {
	entries := func(m inputManifest, paths []string) (map[string]inputEntry, bool) {
		result := map[string]inputEntry{}
		for _, path := range paths {
			e, ok := m.Files[path]
			if !ok {
				return nil, false
			}
			if !e.Version {
				result[path] = e
			}
		}
		return result, true
	}
	a, okA := entries(before, previous)
	b, okB := entries(after, current)
	return okA && okB && maps.Equal(a, b)
}

func validationWitness(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return "", fmt.Errorf("expected regular receipt of at most 2 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
	if err != nil {
		return "", err
	}
	if len(data) > 2<<20 {
		return "", fmt.Errorf("receipt exceeded 2 MiB")
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func validationWitnessesMatch(plan ValidationPlan) bool {
	if !depotWitnessesMatch(plan) {
		return false
	}
	observed := map[string]string{}
	for _, group := range plan.Components {
		for _, spec := range group.Checks {
			for path, expected := range spec.Witnesses {
				actual, found := observed[path]
				if !found {
					var err error
					actual, err = validationWitness(path)
					if err != nil {
						return false
					}
					observed[path] = actual
				}
				if actual != expected {
					return false
				}
			}
		}
	}
	return true
}
