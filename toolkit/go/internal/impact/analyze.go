// Package impact selects tests through separately queried before/after code graphs.
package impact

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/compforge/repokit/toolkit/go/internal/codegraph"
	"github.com/compforge/repokit/toolkit/go/internal/diff"
	"github.com/compforge/repokit/toolkit/go/internal/project"
)

type FileChange struct {
	diff.Change
	Before []codegraph.Symbol `json:"beforeSymbols"`
	After  []codegraph.Symbol `json:"afterSymbols"`
}

type Reason struct {
	Seed           Seed                 `json:"seed"`
	Confidence     codegraph.Confidence `json:"confidence"`
	Distance       int                  `json:"distance"`
	Version        string               `json:"version,omitempty"` // Snapshot owning the dependency evidence.
	TestFile       string               `json:"testFile"`
	Kind           string               `json:"kind"`
	DependencyPath []string             `json:"dependencyPath,omitempty"`
	Relations      []codegraph.Relation `json:"relations,omitempty"`
}

type FileImpact struct {
	Path           string               `json:"path"`
	Version        string               `json:"version"`
	Seed           Seed                 `json:"seed"`
	Confidence     codegraph.Confidence `json:"confidence"`
	Distance       int                  `json:"distance"`
	DependencyPath []string             `json:"dependencyPath"`
	Relations      []codegraph.Relation `json:"relations"`
}

type Result struct {
	TestSelection   TestSelection `json:"testSelection"`
	SchemaVersion   int           `json:"schemaVersion"`
	Changes         []FileChange  `json:"changes"`
	Scope           string        `json:"scope"`
	TestFiles       []string      `json:"testFiles"`
	SourceFiles     []string      `json:"sourceFiles"`
	AffectedFiles   []FileImpact  `json:"affectedFiles"`
	Seeds           []Seed        `json:"seeds"`
	Reasons         []Reason      `json:"reasons"`
	FallbackReasons []string      `json:"fallbackReasons"` // Legacy wire name for incompleteness reasons; no fallback tests are synthesized.
	Uncertainties   []Uncertainty `json:"-"`
	Observations    []Uncertainty `json:"observations,omitempty"`
}

type Request struct {
	MaxFiles, MaxNodes, MaxRelations int
	Before, After                    map[string][]byte
	BeforeResources, AfterResources  map[string][]byte
	Changes                          []diff.Change
	TestDirs                         []string
	Issues                           []string
	Skipped                          map[string]string
	Gitlinks                         map[string]bool
	OldLayout, NewLayout             project.Layout
}

// +spec=Impact reports known dependency evidence and analysis gaps without exempting documentation, version markers or descriptive manifest edits.
// +why=File tags are overlapping facts; validation policy belongs to callers.
// +link=component://docs/diff.md
func Analyze(ctx context.Context, req Request) (Result, error) {
	r := Result{TestSelection: emptySelection(), SchemaVersion: 4, Scope: "focused", Changes: []FileChange{}, TestFiles: []string{}, SourceFiles: []string{}, AffectedFiles: []FileImpact{}, Seeds: []Seed{}, Reasons: []Reason{}, FallbackReasons: []string{}}
	if len(req.Changes) == 0 && len(skippedGaps(req)) == 0 {
		return r, ctx.Err()
	}
	tests := discoverTests(req.After, req.NewLayout, selectComponents(req), req.TestDirs)
	builds, err := buildWorksets(ctx, req, tests)
	if err != nil {
		return r, err
	}
	for _, c := range req.Changes {
		if codegraph.Language(c.Path) != "" {
			r.SourceFiles = append(r.SourceFiles, c.Path)
		}
		oldName := c.Path
		if c.OldPath != "" {
			oldName = c.OldPath
		}
		before := builds[0].Sources[oldName]
		after := builds[1].Sources[c.Path]
		r.Changes = append(r.Changes, FileChange{Change: c, Before: changedSymbols(before.Symbols, c.Hunks, true), After: changedSymbols(after.Symbols, c.Hunks, false)})
	}
	r.SourceFiles = unique(r.SourceFiles)
	graphs := []*codegraph.Graph{builds[0].Graph, builds[1].Graph}
	gaps := skippedGaps(req)
	for _, c := range req.Changes {
		oldName := c.Path
		if c.OldPath != "" {
			oldName = c.OldPath
		}
		if _, exists := req.Before[oldName]; exists || req.Gitlinks[oldName] {
			r.Seeds = append(r.Seeds, changeSeeds(c, builds[0].Sources[oldName], true, req.Gitlinks[oldName])...)
		}
		if _, exists := req.After[c.Path]; exists || req.Gitlinks[c.Path] {
			r.Seeds = append(r.Seeds, changeSeeds(c, builds[1].Sources[c.Path], false, req.Gitlinks[c.Path])...)
		}
		for _, name := range []string{c.Path, c.OldPath} {
			if name == "" || req.Gitlinks[name] {
				continue
			}
			if ignoredDependency(name) {
				gaps = append(gaps, gap{reason: "source_boundary", path: name, message: "vendored or environment dependencies are not indexed", global: true})
			} else if broadChange(name) {
				gaps = append(gaps, gap{reason: "configuration_change", path: name, message: "build, dependency, or test configuration changed", component: true})

			} else if codegraph.Language(name) == "" && !referencedResource(graphs, name) {
				gaps = append(gaps, gap{reason: "unsupported_resource_change", path: name, message: "non-source dependency impact is not modeled", component: true})
			}
		}
	}
	// Reading a child config does not import child sources. A consumed resource
	// changing across snapshots is nevertheless a parent resolution input change.
	resources := map[string]bool{}
	for name := range req.BeforeResources {
		resources[name] = true
	}
	for name := range req.AfterResources {
		resources[name] = true
	}
	for name := range resources {
		if bytes.Equal(req.BeforeResources[name], req.AfterResources[name]) {
			continue
		}
		for _, g := range graphs {
			consumed := false
			for _, edge := range g.Incoming(name) {
				if edge.Kind == codegraph.ConfigExtends {
					consumed = true
				}
			}
			if consumed {
				gaps = append(gaps, gap{path: name, reason: "configuration_change", message: "inherited configuration resource changed"})
				break
			}
		}
	}
	if err := r.selectTests(ctx, graphs, builds, req, tests, gaps); err != nil {
		return r, err
	}
	r.TestSelection = recommendTests(ctx, req, r.Changes, graphs, tests)
	return r, ctx.Err()
}

func changedSymbols(symbols []codegraph.Symbol, hunks []diff.Hunk, old bool) []codegraph.Symbol {
	out := []codegraph.Symbol{}
	for _, s := range symbols {
		for _, h := range hunks {
			r := h.New
			if old {
				r = h.Old
			}
			// Only actual changed lines select symbols; insertion anchors are not
			// edits to the neighbouring declaration on the opposite snapshot.
			if r.Count > 0 && r.Start <= s.EndLine && r.Start+r.Count-1 >= s.StartLine {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

func IsTest(name string) bool { return project.IsTestFile(name) }

func within(name string, dirs []string) bool {
	for _, dir := range dirs {
		if dir == "." || name == dir || strings.HasPrefix(name, dir+"/") {
			return true
		}
	}
	return false
}

func broadChange(name string) bool {
	b := path.Base(name)
	switch b {
	case "go.mod", "go.sum", "go.work", "go.work.sum", "package.json", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "pnpm-workspace.yaml", "bun.lock", "bun.lockb", "pyproject.toml", "uv.lock", "poetry.lock", "Pipfile", "Pipfile.lock", "setup.py", "setup.cfg", "tox.ini", "pytest.ini", "conftest.py", "Makefile":
		return true
	}
	return strings.HasPrefix(b, "tsconfig") || strings.HasPrefix(b, "requirements") || strings.Contains(b, ".config.") || strings.HasPrefix(b, ".env")
}

func unique(values []string) []string {
	sort.Strings(values)
	out := make([]string, 0, len(values))
	for _, v := range values {
		if len(out) == 0 || out[len(out)-1] != v {
			out = append(out, v)
		}
	}
	return out
}

func ValidateDirs(dirs []string) ([]string, error) {
	out := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		if dir == "" || strings.HasPrefix(dir, "/") {
			return nil, fmt.Errorf("test directory must be relative to repository root: %q", dir)
		}
		dir = strings.TrimSuffix(dir, "/")
		dir = strings.TrimPrefix(dir, "./")
		if dir == "" {
			dir = "."
		}
		if dir != "." && !diff.ValidPath(dir) {
			return nil, fmt.Errorf("test directory must be relative to repository root: %q", dir)
		}
		out = append(out, dir)
	}
	return unique(out), nil
}

// A supplied resource with a proven incoming reference has modeled dependents.
// Unknown paths remain separate CodeGraph diagnostics; names alone prove nothing.
func referencedResource(graphs []*codegraph.Graph, name string) bool {
	for _, g := range graphs {
		for _, edge := range g.Incoming(name) {
			if edge.Kind == codegraph.UsesResource {
				return true
			}
		}
	}
	return false
}
