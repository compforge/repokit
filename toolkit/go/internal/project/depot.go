package project

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/compforge/quality-harness/sdks/go/common"
	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

// discoverDepot describes recoverable locations even when no directory exists.
// +spec=Each Component has at most one logical Depot; workspace members may share a Bin path.
// +rule=Discovery is not deletion authorization and never reads generated directories.
// +link=component://docs/depot.md
func discoverDepot(files map[string][]byte, component string) *common.Depot {
	bins := []common.Bin{}
	for _, python := range []bool{false, true} {
		manifest, directory := "package.json", "node_modules"
		if python {
			manifest, directory = "pyproject.toml", ".venv"
		}
		if _, ok := files[path.Join(component, manifest)]; !ok {
			continue
		}
		root, ok := installationRoot(files, component, python)
		if !ok {
			continue
		}
		if python {
			if _, ok := files[path.Join(root, "uv.lock")]; !ok {
				continue
			}
		} else {
			if _, ok := files[path.Join(root, ".pnp.cjs")]; ok {
				continue
			}
			if data, ok := files[path.Join(root, ".yarnrc.yml")]; ok {
				var config struct {
					NodeLinker string `yaml:"nodeLinker"`
				}
				if yaml.Unmarshal(data, &config) != nil || config.NodeLinker == "pnp" {
					continue
				}
			}
		}
		bins = append(bins, common.Bin{Path: path.Join(root, directory), Kind: "dependencies"})
	}
	if len(bins) == 0 {
		return nil
	}
	slices.SortFunc(bins, func(a, b common.Bin) int { return strings.Compare(a.Path, b.Path) })
	return &common.Depot{Bins: bins}
}

func installationRoot(files map[string][]byte, component string, python bool) (string, bool) {
	selected := component
	for ancestor := component; ; ancestor = path.Dir(ancestor) {
		patterns, ok := workspacePatterns(files, ancestor, python)
		if !ok {
			return "", false
		}
		if ancestor != selected {
			relative := strings.TrimPrefix(selected, ancestor+"/")
			if ancestor == "." {
				relative = selected
			}
			included, excluded := false, false
			for _, pattern := range patterns {
				negative := strings.HasPrefix(pattern, "!")
				if workspaceMatch(strings.TrimPrefix(pattern, "!"), relative) {
					if negative {
						excluded = true
					} else {
						included = true
					}
				}
			}
			if included && !excluded {
				selected = ancestor
			}
		}
		if ancestor == "." {
			break
		}
	}
	return selected, true
}

func workspacePatterns(files map[string][]byte, root string, python bool) ([]string, bool) {
	var values []string
	if python {
		if data, exists := files[path.Join(root, "pyproject.toml")]; exists {
			var metadata struct {
				Tool struct {
					UV struct {
						Workspace struct{ Members, Exclude []string }
					}
				}
			}
			if toml.Unmarshal(data, &metadata) != nil {
				return nil, false
			}
			values = metadata.Tool.UV.Workspace.Members
			for _, p := range metadata.Tool.UV.Workspace.Exclude {
				values = append(values, "!"+p)
			}
		}
	} else if data, exists := files[path.Join(root, "pnpm-workspace.yaml")]; exists {
		var metadata struct {
			Packages []string `yaml:"packages"`
		}
		// Decode strings strictly: YAML numbers must not become different glob names.
		var value map[string]any
		if yaml.Unmarshal(data, &value) != nil {
			return nil, false
		}
		if raw, ok := value["packages"]; ok {
			list, ok := raw.([]any)
			if !ok {
				return nil, false
			}
			for _, entry := range list {
				if _, ok := entry.(string); !ok {
					return nil, false
				}
			}
		}
		if yaml.Unmarshal(data, &metadata) != nil {
			return nil, false
		}
		values = metadata.Packages
	} else if data, exists := files[path.Join(root, "package.json")]; exists {
		var metadata struct {
			Workspaces json.RawMessage `json:"workspaces"`
		}
		if json.Unmarshal(data, &metadata) != nil {
			return nil, false
		}
		if len(metadata.Workspaces) > 0 && string(metadata.Workspaces) != "null" {
			if json.Unmarshal(metadata.Workspaces, &values) != nil {
				var workspace struct {
					Packages []string `json:"packages"`
				}
				if json.Unmarshal(metadata.Workspaces, &workspace) != nil {
					return nil, false
				}
				values = workspace.Packages
			}
		}
	}
	for _, value := range values {
		pattern := strings.TrimPrefix(value, "!")
		if pattern == "" || pattern == "." || strings.HasPrefix(pattern, "/") || strings.ContainsAny(pattern, "\\\x00?[]{}()") {
			return nil, false
		}
		for _, part := range strings.Split(pattern, "/") {
			if part == "" || part == "." || part == ".." {
				return nil, false
			}
		}
	}
	return values, true
}

// Workspace globs use slash-separated segments. ** spans segments; * never does.
func workspaceMatch(pattern, name string) bool {
	patterns, parts := strings.Split(pattern, "/"), strings.Split(name, "/")
	// Dynamic programming avoids exponential expansion of repeated ** patterns.
	states := make([]bool, len(parts)+1)
	states[0] = true
	for _, pattern := range patterns {
		next := make([]bool, len(parts)+1)
		for j := 0; j <= len(parts); j++ {
			if pattern == "**" {
				next[j] = states[j] || j > 0 && next[j-1]
			} else if j > 0 && states[j-1] {
				next[j], _ = path.Match(pattern, parts[j-1])
			}
		}
		states = next
	}
	return states[len(parts)]
}

// DependencyManifests uses the same workspace membership rules as Depot discovery.
func DependencyManifests(files map[string][]byte, root string, python bool) ([]string, error) {
	manifest := "package.json"
	if python {
		manifest = "pyproject.toml"
	}
	result := []string{path.Join(root, manifest)}
	patterns, ok := workspacePatterns(files, root, python)
	if !ok {
		return nil, fmt.Errorf("unsupported workspace declaration at %s", root)
	}
	for name := range files {
		if path.Base(name) != manifest || name == result[0] {
			continue
		}
		rel := path.Dir(name)
		if root != "." {
			if !strings.HasPrefix(rel, root+"/") {
				continue
			}
			rel = strings.TrimPrefix(rel, root+"/")
		}
		included, excluded := false, false
		for _, pattern := range patterns {
			if workspaceMatch(strings.TrimPrefix(pattern, "!"), rel) {
				if strings.HasPrefix(pattern, "!") {
					excluded = true
				} else {
					included = true
				}
			}
		}
		if included && !excluded {
			result = append(result, name)
		}
	}
	slices.Sort(result)
	return result, nil
}
