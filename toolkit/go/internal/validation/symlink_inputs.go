package validation

import (
	"path"
	"strings"
)

// +spec=File symlinks contribute their captured link chain and final file to check inputs, without filesystem reads or importing another Component's environment.
// +why=Link text cannot detect target edits; directory aliases do not establish a captured file dependency.
// +link=component://docs/validation.md
func capturedLinkInputs(files map[string]inputEntry, links map[string]string) map[string][]string {
	if len(links) == 0 {
		return nil
	}
	directories := map[string]bool{".": true}
	for name := range files {
		for dir := path.Dir(name); !directories[dir]; dir = path.Dir(dir) {
			directories[dir] = true
		}
	}
	result := make(map[string][]string, len(links))
	for name := range links {
		result[name] = resolveFileLink(name, files, links, directories)
	}
	return result
}

func resolveFileLink(name string, files map[string]inputEntry, links map[string]string, directories map[string]bool) []string {
	pending := strings.Split(name, "/")
	current := "."
	var inputs []string
	seen := map[string]bool{}
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			if current == "." {
				return nil
			}
			current = path.Dir(current)
			continue
		}
		candidate := path.Join(current, part)
		if target, ok := links[candidate]; ok {
			// Only follow terminal file aliases; never expand directory aliases.
			if len(pending) > 0 || seen[candidate] || target == "" || path.IsAbs(target) {
				return nil
			}
			seen[candidate] = true
			inputs = append(inputs, candidate)
			pending = strings.Split(target, "/")
			continue
		}
		entry, captured := files[candidate]
		if len(pending) > 0 {
			// Validate the entry before interpreting a later '..' or trailing slash.
			if captured || !directories[candidate] {
				return nil
			}
			current = candidate
			continue
		}
		if entry.Kind != "file" && entry.Kind != "opaque" {
			return nil
		}
		return append(inputs, candidate)
	}
	return nil
}
