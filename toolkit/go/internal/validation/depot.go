package validation

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/compforge/repokit/toolkit/go/internal/project"
)

// +spec=Validation observes dependency Bins automatically; outputs and caches are not inputs.
// +spec=Missing or unmanaged Bins permit real execution but never historical reuse.
// +spec=Only owning and explicitly declared Components contribute Bins; shared installations add metadata, not member source trees.
// +link=component://docs/depot.md
func attachDepotInputs(checkout string, layout project.Layout, executor string, spec *CheckSpec, contents map[string][]byte) error {
	selected := map[string]bool{executor: true}
	for _, root := range spec.Inputs.Components {
		selected[root] = true
	}

	spec.DepotWitnesses = nil
	for _, component := range layout.Components {
		if !selected[component.Root] || component.Depot == nil {
			continue
		}
		for _, bin := range component.Depot.Bins {
			if bin.Kind != "dependencies" {
				continue
			}
			if _, seen := spec.DepotWitnesses[bin.Path]; seen {
				continue
			}
			value, err := depotWitness(checkout, bin.Path)
			if err != nil {
				return fmt.Errorf("dependency Bin %q: %w", bin.Path, err)
			}
			if spec.DepotWitnesses == nil {
				spec.DepotWitnesses = map[string]string{}
			}
			spec.DepotWitnesses[bin.Path] = value
			inputs, err := project.DependencyInputs(contents, path.Dir(bin.Path), path.Base(bin.Path) == ".venv")
			if err != nil {
				return fmt.Errorf("dependency Bin %q inputs: %w", bin.Path, err)
			}
			for _, name := range inputs {
				if _, exists := contents[name]; exists {
					spec.InputFiles = append(spec.InputFiles, name)
				}
			}
		}
	}
	slices.Sort(spec.InputFiles)
	spec.InputFiles = slices.Compact(spec.InputFiles)
	if len(spec.DepotWitnesses) > 0 {
		spec.InputReason += "; dependency Bin receipts and installation inputs"
	}
	return nil
}

func depotWitness(checkout, relative string) (string, error) {
	directory := filepath.Join(checkout, filepath.FromSlash(relative))
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "unsupported", nil
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", err
	}
	if resolved != directory {
		return "unsupported", nil
	}
	identity := depotDirectoryIdentity(info)
	if identity == "" {
		return "unsupported", nil
	}
	receipt := filepath.Join(directory, ".repokit-dependencies.json")
	stat, err := os.Lstat(receipt)
	if errors.Is(err, os.ErrNotExist) {
		return "unmanaged:" + identity, nil
	}
	if err != nil {
		return "", err
	}
	if !stat.Mode().IsRegular() || stat.Size() > 2<<20 {
		return "invalid:" + identity, nil
	}
	file, err := os.Open(receipt)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
	if err != nil {
		return "", err
	}
	var value struct{ Manager, Fingerprint string }
	if len(data) > 2<<20 || json.Unmarshal(data, &value) != nil || value.Manager == "" || value.Fingerprint == "" {
		return "invalid:" + identity, nil
	}
	digest := sha256.Sum256(append([]byte(identity+"\x00"), data...))
	return fmt.Sprintf("sha256:%x", digest), nil
}

func depotReusable(witnesses map[string]string) bool {
	for _, value := range witnesses {
		if !strings.HasPrefix(value, "sha256:") {
			return false
		}
	}
	return true
}

func depotWitnessesMatch(plan ValidationPlan) bool {
	observed := map[string]string{}
	for _, group := range plan.Components {
		for _, spec := range group.Checks {
			for name, expected := range spec.DepotWitnesses {
				actual, found := observed[name]
				if !found {
					var err error
					actual, err = depotWitness(plan.Checkout, name)
					if err != nil {
						return false
					}
					observed[name] = actual
				}
				if actual != expected {
					return false
				}
			}
		}
	}
	return true
}
