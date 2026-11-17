package project

import (
	"path"
	"slices"

	cg "github.com/compforge/codegraph"
)

var materialTags, _ = cg.NewTagMatcher(nil)

// FileTags classifies a snapshot entry without parsing content. Links skip format tags.
func FileTags(document cg.Document) []cg.Tag {
	return materialTags.DocumentTags(document)
}

// ManifestEcosystem classifies a regular file, not whether discovery selects its directory.
func ManifestEcosystem(name string) string {
	for _, ecosystem := range ecosystems {
		for _, marker := range ecosystem.manifests {
			if path.Base(name) == marker {
				return ecosystem.name
			}
		}
	}
	return ""
}

// FileRole reports known static roles; unknown regular files need no invented role.
func FileRole(name string) string {
	if slices.Contains(materialTags.Match(name), cg.LockfileTag) {
		return "lockfile"
	}
	switch path.Base(name) {
	case "Makefile", "makefile", "GNUmakefile", "CMakeLists.txt", "build.gradle", "build.gradle.kts":
		return "build_script"
	}
	return ""
}
