package git

import (
	"context"
	"fmt"
	"github.com/compforge/repokit/toolkit/go/internal/git/process"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/compforge/repokit/toolkit/go/internal/diff"
)

// Entry is a parent repository entry; gitlinks never expose child contents.
type Entry struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Mode string `json:"mode"`
	OID  string `json:"oid,omitempty"`
}

// Entries observes a version's paths and types, without reading file contents.
// Working gitlinks use checkout HEAD when initialized, otherwise the index OID.
func (r *Repository) Entries(ctx context.Context, head string, staged bool) ([]Entry, error) {
	out, err := process.Material(r.Root).Entries(ctx, head)
	if err != nil {
		return nil, err
	}
	entries := map[string]Entry{}
	for _, raw := range strings.Split(string(out), "\x00") {
		if raw == "" {
			continue
		}
		meta, name, ok := strings.Cut(raw, "\t")
		parts := strings.Fields(meta)
		if !ok || len(parts) != 3 || !diff.ValidPath(name) {
			return nil, fmt.Errorf("invalid Git entry")
		}
		oid := parts[2]
		if head == "" {
			if parts[2] != "0" {
				return nil, fmt.Errorf("unmerged index entry: %s", name)
			}
			oid = parts[1]
		}
		entries[name] = Entry{Path: name, Mode: parts[0], OID: oid}
	}
	if head == "" && !staged {
		out, err = process.Material(r.Root).Untracked(ctx)
		if err != nil {
			return nil, err
		}
		for _, name := range strings.Split(string(out), "\x00") {
			if name == "" || strings.HasSuffix(name, "/") {
				continue
			}
			if !diff.ValidPath(name) {
				return nil, fmt.Errorf("unsafe repository path")
			}
			if _, ok := entries[name]; !ok {
				entries[name] = Entry{Path: name}
			}
		}
	}
	if len(entries) > maxFiles {
		return nil, fmt.Errorf("repository exceeds %d files", maxFiles)
	}
	result := []Entry{}
	for name, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if head == "" && !staged && entry.Mode != "160000" {
			full := filepath.Join(r.Root, filepath.FromSlash(name))
			parent, err := filepath.EvalSymlinks(filepath.Dir(full))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if parent != filepath.Dir(full) {
				return nil, fmt.Errorf("symlinked directory: %s", name)
			}
			info, err := os.Lstat(full)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			switch {
			case info.Mode()&os.ModeSymlink != 0:
				entry.Mode = "120000"
			case info.Mode().IsRegular():
				entry.Mode = "100644"
				if info.Mode().Perm()&0111 != 0 {
					entry.Mode = "100755"
				}
			default:
				return nil, fmt.Errorf("unsupported entry: %s", name)
			}
			entry.OID = ""
		}
		if head == "" && !staged && entry.Mode == "160000" {
			entry.OID, err = r.gitlinkOID(ctx, name, entry.OID)
			if err != nil {
				return nil, err
			}
		}
		switch entry.Mode {
		case "100644", "100755":
			entry.Kind = "regular"
		case "120000":
			entry.Kind = "symlink"
		case "160000":
			entry.Kind = "gitlink"
		default:
			return nil, fmt.Errorf("unsupported Git mode: %s", entry.Mode)
		}
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}
