package commit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/compforge/repokit/toolkit/go/internal/gitworktree"
)

type operation struct {
	ctx    context.Context
	info   gitworktree.CheckoutInfo
	report *Report
	index  string
}

func (o *operation) query(args ...string) (string, error) {
	budget := 30 * time.Second
	if deadline, ok := o.ctx.Deadline(); ok {
		budget = time.Until(deadline)
	}
	r := gitworktree.RunIndex(o.ctx, o.info.Root, o.index, budget, args...)
	if r.Err != nil {
		return "", r.Err
	}
	return strings.TrimSuffix(r.Stdout, "\n"), nil
}
func fingerprint(path string) (string, []byte, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "missing", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), b, nil
}
func (o *operation) identity() (string, string, error) {
	b, e := o.query("symbolic-ref", "--quiet", "HEAD")
	if e != nil {
		return "", "", failure("detached", "commit requires an attached local branch")
	}
	if !strings.HasPrefix(b, "refs/heads/") {
		return "", "", failure("branch", "HEAD is not a local branch")
	}
	b = strings.TrimPrefix(b, "refs/heads/")
	h, exists, e := gitworktree.BranchCommit(o.ctx, o.info.Root, b)
	if e == nil && !exists {
		h = "unborn"
	}
	return b, h, e
}
func (o *operation) prepare(selection Selection) error {
	if selection.Mode != "files" && selection.Mode != "tracked" && selection.Mode != "staged" {
		return failure("selection", "choose staged, tracked or files")
	}
	if (selection.Mode == "files") != (len(selection.Paths) > 0) {
		return failure("selection", "files mode requires literal paths; other modes accept no paths")
	}
	paths := []string{}
	for _, path := range selection.Paths {
		if path == "" || strings.ContainsRune(path, 0) || filepath.IsAbs(path) {
			return failure("path", "paths must be nonempty checkout-root-relative literals")
		}
		path = filepath.Clean(path)
		if path == ".." || strings.HasPrefix(path, "../") {
			return failure("path", "path is outside the checkout")
		}
		paths = append(paths, filepath.ToSlash(path))
	}
	o.report.Selection = Selection{selection.Mode, paths}
	branch, head, e := o.identity()
	if e != nil {
		return e
	}
	o.report.Branch = branch
	o.report.Head = head
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply"} {
		_, e := os.Stat(filepath.Join(o.info.GitDir, name))
		if e == nil {
			return failure("in_progress", "finish or abort the active Git operation before committing")
		}
		if !os.IsNotExist(e) {
			return e
		}
	}
	original := filepath.Join(o.info.GitDir, "index")
	digest, b, e := fingerprint(original)
	if e != nil {
		return e
	}
	o.report.IndexFingerprint = digest
	if b != nil {
		if e = os.WriteFile(o.index, b, 0600); e != nil {
			return e
		}
	}
	base := head
	if head == "unborn" {
		base, e = o.query("mktree")
		if e != nil {
			return e
		}
	}
	// Keep index-only deletions, including rm --cached with a remaining local file.
	before, e := o.changes(base)
	if e != nil {
		return e
	}
	removed := map[string]bool{}
	for _, c := range before {
		if c.NewMode == "000000" {
			removed[c.Path] = true
		}
	}
	if selection.Mode == "tracked" {
		if _, e := o.query("add", "--update"); e != nil {
			return e
		}
	}
	if selection.Mode == "files" {
		args := []string{"--literal-pathspecs", "ls-files", "-z", "--cached", "--modified", "--deleted"}
		args = append(args, "--others", "--exclude-standard", "--")
		args = append(args, paths...)
		raw, e := o.query(args...)
		if e != nil {
			return e
		}
		names := strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
		slices.Sort(names)
		names = slices.Compact(names)
		add := []string{}
		for _, name := range names {
			if name != "" && !removed[name] {
				add = append(add, name)
			}
		}
		if selection.Mode == "files" {
			for _, path := range paths {
				known := false
				for _, name := range names {
					known = known || matches(name, []string{path})
				}
				for _, c := range before {
					known = known || matches(c.Path, []string{path})
				}
				if !known {
					return failure("path", fmt.Sprintf("path does not name a tracked or non-ignored file: %q", path))
				}
			}
		}
		if len(add) > 0 {
			pathspec := filepath.Join(filepath.Dir(o.index), "paths")
			if e = os.WriteFile(pathspec, []byte(strings.Join(add, "\x00")+"\x00"), 0600); e != nil {
				return e
			}
			if _, e = o.query("--literal-pathspecs", "add", "--pathspec-from-file="+pathspec, "--pathspec-file-nul"); e != nil {
				return e
			}
		}
	}
	tree, e := o.query("write-tree")
	if e != nil {
		return e
	}
	o.report.Tree = tree
	changes, e := o.changes(base)
	if e != nil {
		return e
	}
	o.report.Changes = changes
	o.report.Empty = len(changes) == 0
	if selection.Mode == "files" {
		for _, c := range changes {
			if !matches(c.Path, paths) {
				return failure("scope", fmt.Sprintf("index contains path outside explicit scope: %q", c.Path))
			}
		}
	}
	return o.gitlinks(changes)
}
func matches(path string, scopes []string) bool {
	for _, s := range scopes {
		if s == "." || path == s || strings.HasPrefix(path, s+"/") {
			return true
		}
	}
	return false
}
func (o *operation) changes(base string) ([]Change, error) {
	raw, e := o.query("diff-index", "--cached", "--raw", "--no-renames", "--ignore-submodules=none", "-z", base, "--")
	if e != nil {
		return nil, e
	}
	parts := strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
	out := []Change{}
	if raw == "" {
		return out, nil
	}
	if len(parts)%2 != 0 {
		return nil, failure("evidence", "malformed index changes")
	}
	for i := 0; i < len(parts); i += 2 {
		f := strings.Fields(parts[i])
		if len(f) != 5 {
			return nil, failure("evidence", "malformed index entry")
		}
		out = append(out, Change{parts[i+1], strings.TrimPrefix(f[0], ":"), f[1]})
	}
	return out, nil
}
func (o *operation) gitlinks(changes []Change) error {
	manifestChanged := false
	changed := map[string]bool{}
	for _, c := range changes {
		changed[c.Path] = true
		manifestChanged = manifestChanged || c.Path == ".gitmodules"
	}
	raw, e := o.query("ls-files", "--stage", "-z")
	if e != nil {
		return e
	}
	links := []string{}
	for _, entry := range strings.Split(raw, "\x00") {
		meta, path, ok := strings.Cut(entry, "\t")
		if ok && strings.HasPrefix(meta, "160000 ") && (manifestChanged || changed[path]) {
			links = append(links, path)
		}
	}
	if len(links) == 0 {
		return nil
	}
	r := gitworktree.RunIndex(o.ctx, o.info.Root, o.index, 5*time.Second, "config", "-z", "--blob", o.report.Tree+":.gitmodules", "--get-regexp", `^submodule\..*\.path$`)
	if r.Err != nil {
		return failure("gitlink", "candidate gitlinks require a readable staged .gitmodules registration")
	}
	registered := []string{}
	for _, v := range strings.Split(r.Stdout, "\x00") {
		_, path, ok := strings.Cut(v, "\n")
		if ok {
			registered = append(registered, path)
		}
	}
	for _, path := range links {
		if !slices.Contains(registered, path) {
			return failure("gitlink", fmt.Sprintf("unregistered gitlink: %q", path))
		}
	}
	return nil
}
