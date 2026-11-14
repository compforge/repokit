package project

import (
	"path"
	"slices"
)

func NodeLockfiles() map[string][]string {
	return map[string][]string{"npm": {"npm-shrinkwrap.json", "package-lock.json"}, "pnpm": {"pnpm-lock.yaml"}, "yarn": {"yarn.lock"}, "bun": {"bun.lock", "bun.lockb"}}
}

// DependencyInputs returns manifests and candidate lock/configuration paths for
// an installation. Optional paths may be absent. Sharing a Bin does not make
// every member's source an input.
// +spec=Depot preparation and validation use the same installation input paths.
// +link=component://docs/depot.md
func DependencyInputs(files map[string][]byte, root string, python bool) ([]string, error) {
	inputs, err := DependencyManifests(files, root, python)
	if err != nil {
		return nil, err
	}
	names := []string{"pnpm-workspace.yaml", ".npmrc", ".yarnrc.yml", "bunfig.toml", "uv.toml"}
	if python {
		names = append(names, "uv.lock")
	} else {
		for _, locks := range NodeLockfiles() {
			names = append(names, locks...)
		}
	}
	for _, name := range names {
		name = path.Join(root, name)
		inputs = append(inputs, name)
	}
	slices.Sort(inputs)
	return slices.Compact(inputs), nil
}
