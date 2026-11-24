package git

import (
	"context"
	"fmt"
	"github.com/compforge/repokit/toolkit/go/internal/git/process"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/compforge/repokit/toolkit/go/internal/diff"
)

// Catalog returns regular-file paths with content only where requested. Nil
// values mean presence, never a captured source blob. Gitlinks and symlinks do
// not become project material; inspection never opens child repositories.
func (r *Repository) Catalog(ctx context.Context, head string, staged bool, needsContent func(string) bool) (map[string][]byte, error) {
	out, err := process.Material(r.Root).Entries(ctx, head)
	if err != nil {
		return nil, err
	}
	entries := map[string]process.Blob{}
	for _, entry := range strings.Split(string(out), "\x00") {
		if entry == "" {
			continue
		}
		metadata, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) != 3 || !diff.ValidPath(name) {
			return nil, fmt.Errorf("invalid Git catalog entry")
		}
		oid := fields[2]
		if head == "" {
			if fields[2] != "0" {
				return nil, fmt.Errorf("unmerged index entry: %s", name)
			}
			oid = fields[1]
		}
		// Working-tree types are checked below, including a tracked symlink that
		// has since become a regular file. Gitlinks remain repository boundaries.
		if fields[0] == "160000" {
			continue
		}
		if fields[0] == "120000" && (head != "" || staged) {
			if needsContent(name) {
				return nil, fmt.Errorf("metadata %s is a symlink", name)
			}
			continue
		}
		if fields[0] != "100644" && fields[0] != "100755" && fields[0] != "120000" {
			return nil, fmt.Errorf("unsupported Git entry mode for %s", name)
		}
		entries[name] = process.Blob{Name: name, OID: oid}
	}
	if head == "" && !staged {
		out, err := process.Material(r.Root).Untracked(ctx)
		if err != nil {
			return nil, err
		}
		for _, name := range strings.Split(string(out), "\x00") {
			// Git lists untracked nested repositories as directories. Do not enter.
			if name == "" || strings.HasSuffix(name, "/") {
				continue
			}
			if !diff.ValidPath(name) {
				return nil, fmt.Errorf("unsafe repository path %q", name)
			}
			entries[name] = process.Blob{Name: name}
		}
	}
	if len(entries) > maxFiles {
		return nil, fmt.Errorf("repository exceeds %d files", maxFiles)
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	captured := newSnapshot()
	var selected []process.Blob
	total := 0
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if head != "" || staged {
			captured.Files[name] = nil
			if needsContent(name) {
				selected = append(selected, entries[name])
			}
			continue
		}
		full := filepath.Join(r.Root, filepath.FromSlash(name))
		info, err := os.Lstat(full)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			if needsContent(name) {
				return nil, fmt.Errorf("metadata %s is not a regular file", name)
			}
			continue
		}
		// Lstat checks the leaf; a symlinked ancestor must not leak outside bytes.
		resolved, err := filepath.EvalSymlinks(filepath.Dir(full))
		if err != nil {
			return nil, err
		}
		if resolved != filepath.Dir(full) {
			if needsContent(name) {
				return nil, fmt.Errorf("metadata %s has a symlinked directory", name)
			}
			continue
		}
		captured.Files[name] = nil
		if !needsContent(name) {
			continue
		}
		if info.Size() > maxFileBytes {
			return nil, fmt.Errorf("metadata %s exceeds %d bytes", name, maxFileBytes)
		}
		file, err := os.Open(full)
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: file}, maxFileBytes+1))
		closeErr := file.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(data) > maxFileBytes {
			return nil, fmt.Errorf("metadata %s exceeds %d bytes", name, maxFileBytes)
		}
		total += len(data)
		if total > maxSnapshotBytes {
			return nil, fmt.Errorf("metadata exceeds 128 MiB")
		}
		captured.Files[name] = data
	}
	if len(selected) > 0 {
		captured, err = r.readBlobs(ctx, captured, selected, false)
		if err != nil {
			return nil, err
		}
	}
	return captured.Files, nil
}
