package git

import (
	"path/filepath"
	"strings"
	"time"

	repogit "github.com/compforge/repokit/toolkit/go/internal/git"
)

type pushTracking struct {
	destination string
	addMapping  bool
	excluded    bool
}

// +spec=Only extend a narrow clone made of conventional exact branch mappings. Git retains ownership of custom fetch layouts and explicit exclusions.
// +link=component://docs/git.md#普通分支发布
func planPushTracking(specs, remote, branch string) pushTracking {
	source := "refs/heads/" + branch
	prefix := "refs/remotes/" + remote + "/"
	standard := true
	hasBranchMapping := false
	covered := false
	for _, spec := range strings.Split(specs, "\n") {
		if excluded, ok := strings.CutPrefix(spec, "^"); ok {
			left, right, wildcard := strings.Cut(excluded, "*")
			if source == excluded || (wildcard && len(source) >= len(left)+len(right) && strings.HasPrefix(source, left) && strings.HasSuffix(source, right)) {
				return pushTracking{excluded: true}
			}
			continue
		}
		src, dst, _ := strings.Cut(spec, ":")
		name, ok := strings.CutPrefix(src, "+refs/heads/")
		if !ok || name == "" || dst != prefix+name || (name != "*" && strings.Contains(name, "*")) {
			standard = false
			continue
		}
		hasBranchMapping = true
		covered = covered || name == "*" || name == branch
	}
	if !standard || !hasBranchMapping {
		return pushTracking{}
	}
	return pushTracking{destination: prefix + branch, addMapping: !covered}
}

// fetchTip reads fresh remote evidence and updates just the conventional tracking ref.
func (tracking pushTracking) fetchTip(repo, remote, endpoint, ref string, query repogit.Query) (string, bool) {
	if tracking.destination == "" {
		return "", false
	}
	url, err := query(5*time.Second, "remote", "get-url", remote)
	if err != nil || url == "" {
		return "", false
	}
	if !strings.Contains(url, ":") && !filepath.IsAbs(url) {
		url = filepath.Join(repo, url)
	}
	if url != endpoint {
		return "", false
	}
	raw, err := query(30*time.Second, "fetch", "--porcelain", "--verbose", "--no-tags", "--no-write-fetch-head", "--recurse-submodules=no", "--", endpoint, "+"+ref+":"+tracking.destination)
	if err != nil {
		return "", false
	}
	return fetchedBranchTip(raw, tracking.destination)
}

// The verbose porcelain row contains the SHA observed by this fetch even when the tracking ref is up to date.
func fetchedBranchTip(raw, destination string) (string, bool) {
	if strings.ContainsAny(raw, "\r\n") || len(raw) < 2 || raw[1] != ' ' || !strings.ContainsRune(" *=+", rune(raw[0])) {
		return "", false
	}
	fields := strings.Fields(raw[2:])
	if len(fields) != 3 || fields[2] != destination || !repogit.ValidObjectID(fields[0]) || !repogit.ValidObjectID(fields[1]) {
		return "", false
	}
	return fields[1], true
}
