package git

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/compforge/repokit/toolkit/go/internal/git/process"
	"sort"
	"strings"

	"github.com/compforge/repokit/toolkit/go/internal/diff"
)

func (r *Repository) ComparisonPatch(ctx context.Context, base, head string, staged bool) ([]byte, error) {
	return process.Material(r.Root).ComparisonPatch(ctx, base, head, staged)
}

// Staged reads the index without writing a tree or changing its stat cache.
func (r *Repository) Staged(ctx context.Context) (Snapshot, error) {
	s := newSnapshot()
	out, err := process.Material(r.Root).Entries(ctx, "")
	if err != nil {
		return s, err
	}
	var blobs []process.Blob
	for _, line := range strings.Split(string(out), "\x00") {
		if line == "" {
			continue
		}
		meta, name, ok := strings.Cut(line, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || !diff.ValidPath(name) {
			return s, fmt.Errorf("invalid index entry")
		}
		if fields[2] != "0" {
			return s, fmt.Errorf("unmerged index entry: %s", name)
		}
		if fields[0] == "160000" {
			r.readModule(ctx, &s, name, fields[1], false)
			continue
		}
		if fields[0] != "100644" && fields[0] != "100755" && fields[0] != "120000" {
			s.Issues = append(s.Issues, name+": unsupported Git entry mode")
			continue
		}
		blobs = append(blobs, process.Blob{Name: name, OID: fields[1], Symlink: fields[0] == "120000"})
	}
	return r.readBlobs(ctx, s, blobs, true)
}

// Digest identifies observed contents, not an atomic filesystem snapshot.
func (s Snapshot) Digest() string {
	h := sha256.New()
	h.Write([]byte("repocli-snapshot-v2\x00"))
	names := make([]string, 0, len(s.Files))
	for name := range s.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		data := s.Files[name]
		fmt.Fprintf(h, "%d:%s%d:", len(name), name, len(data))
		h.Write(data)
	}
	// Typed records cannot collide with length-framed regular-file entries.
	for _, group := range []struct {
		kind   string
		values map[string]string
	}{{"symlink", s.Links}, {"gitlink", s.Modules}, {"large_file", s.Opaque}} {
		names := make([]string, 0, len(group.values))
		for name := range group.values {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			value := group.values[name]
			fmt.Fprintf(h, "%s:%d:%s%d:%s", group.kind, len(name), name, len(value), value)
		}
	}
	issues := append([]string{}, s.Issues...)
	sort.Strings(issues)
	for _, issue := range issues {
		fmt.Fprintf(h, "issue:%d:%s", len(issue), issue)
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}
