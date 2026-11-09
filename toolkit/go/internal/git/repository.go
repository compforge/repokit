// Package git reads repository snapshots; it never changes the index or worktree.
package git

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/compforge/repokit/toolkit/go/internal/gitenv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/compforge/repokit/toolkit/go/internal/diff"
)

const maxFiles = 10000
const maxFileBytes = 2 << 20
const maxSnapshotBytes = 128 << 20

type blob struct {
	name, oid string
	symlink   bool
}

type Snapshot struct {
	Resources map[string][]byte // Additional captured configuration; never populated from child repositories.
	Files     map[string][]byte
	Links     map[string]string
	Modules   map[string]string
	Opaque    map[string]string
	Issues    []string
}

type Repository struct {
	Root             string
	MaxFiles         int   // Zero retains the default capture limit.
	MaxSnapshotBytes int64 // Zero retains the default per-snapshot source byte budget.
}

// CheckFileCount rejects incomplete snapshots rather than truncating their identity.
func (r *Repository) CheckFileCount(count int) error {
	limit := r.MaxFiles
	if limit == 0 {
		limit = maxFiles
	}
	if count <= limit {
		return nil
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return fmt.Errorf("snapshot file budget exceeded: files=%d limit=%d; current process Go heap allocation=%.1f MiB (not RSS or peak); increase MaxFiles to capture this snapshot", count, limit, float64(memory.HeapAlloc)/(1<<20))
}

func (r *Repository) snapshotByteLimit() int64 {
	if r.MaxSnapshotBytes > 0 {
		return r.MaxSnapshotBytes
	}
	return maxSnapshotBytes
}

func (r *Repository) CheckSnapshotBytes(total int64) error {
	limit := r.snapshotByteLimit()
	if total <= limit {
		return nil
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return fmt.Errorf("snapshot byte budget exceeded: bytes=%d limit=%d; current process Go heap allocation=%.1f MiB (not RSS or peak); increase MaxSnapshotBytes to capture this snapshot", total, limit, float64(memory.HeapAlloc)/(1<<20))
}

func (r *Repository) Origin(ctx context.Context) (string, error) {
	out, err := r.run(ctx, "config", "--get", "remote.origin.url")
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return "", nil
	}
	return strings.TrimSpace(string(out)), err
}

// CommonDir identifies the shared metadata directory across linked worktrees.
func (r *Repository) CommonDir(ctx context.Context) (string, error) {
	out, err := r.run(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(strings.TrimSuffix(string(out), "\n"))
}

func Open(ctx context.Context, dir string) (*Repository, error) {
	r := &Repository{Root: dir}
	out, err := r.run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	r.Root = strings.TrimSuffix(string(out), "\n")
	return r, nil
}

func (r *Repository) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-pager"}, args...)...)
	cmd.Env = gitenv.ForCheckout(os.Environ())
	cmd.Dir = r.Root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (r *Repository) Resolve(ctx context.Context, ref string) (string, error) {
	out, err := r.run(ctx, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	return strings.TrimSpace(string(out)), err
}

func (r *Repository) Patch(ctx context.Context, base string) ([]byte, error) {
	return r.run(ctx, "diff", "--no-ext-diff", "--no-textconv", "--binary", "--find-renames", base, "--")
}

func (r *Repository) Base(ctx context.Context, ref string) (Snapshot, error) {
	s := newSnapshot()
	out, err := r.run(ctx, "ls-tree", "-r", "-z", "--full-tree", ref)
	if err != nil {
		return s, err
	}
	var blobs []blob
	for _, line := range strings.Split(string(out), "\x00") {
		if line == "" {
			continue
		}
		meta, name, ok := strings.Cut(line, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || !diff.ValidPath(name) {
			return s, fmt.Errorf("invalid git tree entry")
		}
		if fields[0] == "160000" {
			r.readModule(ctx, &s, name, fields[2], false)
			continue
		}
		if fields[0] != "100644" && fields[0] != "100755" && fields[0] != "120000" {
			s.Issues = append(s.Issues, name+": unsupported Git entry mode")
			continue
		}
		blobs = append(blobs, blob{name, fields[2], fields[0] == "120000"})
	}
	return r.readBlobs(ctx, s, blobs, true)
}

// Full capture streams oversized blobs into an identity; metadata-only readers
// reject them before reading their contents. Both use the same bounded batch I/O.
func (r *Repository) readBlobs(ctx context.Context, s Snapshot, blobs []blob, hashLargeFiles bool) (Snapshot, error) {
	if err := r.CheckFileCount(len(blobs)); err != nil {
		return s, err
	}
	// One cat-file process avoids spawning Git once for every unchanged file.
	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch")
	cmd.Env = gitenv.ForCheckout(os.Environ())
	cmd.Dir = r.Root
	var input strings.Builder
	for _, b := range blobs {
		fmt.Fprintln(&input, b.oid)
	}
	cmd.Stdin = strings.NewReader(input.String())
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return s, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return s, err
	}
	finished := false
	defer func() {
		if !finished {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	reader := bufio.NewReader(pipe)
	total := 0
	for _, b := range blobs {
		header, err := reader.ReadString('\n')
		if err != nil {
			return s, fmt.Errorf("read blob %s: %w", b.name, err)
		}
		parts := strings.Fields(header)
		if len(parts) != 3 || parts[1] != "blob" {
			return s, fmt.Errorf("invalid blob header for %s", b.name)
		}
		size, err := strconv.Atoi(parts[2])
		if err != nil || size < 0 {
			return s, fmt.Errorf("invalid blob size for %s", b.name)
		}
		if size > maxFileBytes {
			if !hashLargeFiles {
				return s, fmt.Errorf("metadata %s exceeds %d bytes", b.name, maxFileBytes)
			}
			digest := sha256.New()
			if _, err = io.CopyN(digest, reader, int64(size)); err != nil {
				return s, err
			}
			if _, err = reader.ReadByte(); err != nil {
				return s, err
			}
			s.Opaque[b.name] = fmt.Sprintf("%d:sha256:%x", size, digest.Sum(nil))
			continue
		}
		total += size
		if err := r.CheckSnapshotBytes(int64(total)); err != nil {
			return s, err
		}
		data := make([]byte, size+1)
		if _, err = io.ReadFull(reader, data); err != nil {
			return s, err
		}
		if b.symlink {
			s.Links[b.name] = string(data[:size])
		} else {
			s.Files[b.name] = data[:size]
		}
	}
	err = cmd.Wait()
	finished = true
	if err != nil {
		return s, fmt.Errorf("git cat-file: %w: %s", err, stderr.String())
	}
	s.checkLinks()
	return s, nil
}

func (r *Repository) Working(ctx context.Context) (Snapshot, []string, error) {
	s := newSnapshot()
	tracked, err := r.run(ctx, "ls-files", "-z", "--cached")
	if err != nil {
		return s, nil, err
	}
	untracked, err := r.run(ctx, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return s, nil, err
	}
	entries, err := r.run(ctx, "ls-files", "--stage", "-z")
	if err != nil {
		return s, nil, err
	}
	modules := map[string]string{}
	for _, line := range strings.Split(string(entries), "\x00") {
		if line == "" {
			continue
		}
		meta, name, ok := strings.Cut(line, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || !diff.ValidPath(name) {
			return s, nil, fmt.Errorf("invalid index entry")
		}
		if fields[2] != "0" {
			return s, nil, fmt.Errorf("unmerged index entry: %s", name)
		}
		if fields[0] == "160000" {
			modules[name] = fields[1]
		}
	}
	names := map[string]bool{}
	var added []string
	for _, name := range strings.Split(string(tracked), "\x00") {
		if name != "" {
			names[name] = true
		}
	}
	for _, name := range strings.Split(string(untracked), "\x00") {
		// Without --directory, ls-files emits trailing slashes for untracked
		// embedded repositories, including linked worktrees. They belong to
		// another checkout, not to this repository's files or added changes.
		// Validate the directory spelling without weakening file-path checks.
		if directory, ok := strings.CutSuffix(name, "/"); ok {
			if !diff.ValidPath(directory) {
				return s, nil, fmt.Errorf("unsafe repository path %q", name)
			}
			continue
		}
		if name != "" {
			names[name] = true
			added = append(added, name)
		}
	}
	if err := r.CheckFileCount(len(names)); err != nil {
		return s, nil, err
	}
	paths := make([]string, 0, len(names))
	for name := range names {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	// Validation and submodule capture stay serial: readModule recurses into
	// nested checkouts and mutates the snapshot. Only plain file I/O parallelizes.
	var files []string
	for _, name := range paths {
		if !diff.ValidPath(name) {
			return s, nil, fmt.Errorf("unsafe repository path %q", name)
		}
		if oid, ok := modules[name]; ok {
			r.readModule(ctx, &s, name, oid, true)
			continue
		}
		files = append(files, name)
	}
	if err := r.readWorkingFiles(ctx, &s, files); err != nil {
		return s, nil, err
	}
	if err := ctx.Err(); err != nil {
		return s, nil, err
	}
	s.checkLinks()
	return s, added, nil
}

// workingFile is the per-file outcome of one parallel working-tree read.
type workingFile struct {
	name       string
	data       []byte
	link       string
	opaque     string
	issue      string
	overBudget bool
	err        error
}

// readWorkingFiles reads working-tree files concurrently. Per-open latency on
// some environments (endpoint protection hooking file opens) dominates capture
// time, so a worker pool hides it; merging stays in sorted-path order, keeping
// issues, byte accounting and first-error precedence identical to serial reads.
func (r *Repository) readWorkingFiles(ctx context.Context, s *Snapshot, files []string) error {
	results := make([]workingFile, len(files))
	// EvalSymlinks walks every path component; sibling files share ancestors,
	// so directory resolutions are cached. Only successes are cached: a failed
	// resolution mirrors the serial behavior for the affected file alone.
	resolvedDirs := map[string]string{}
	var dirsMu sync.Mutex
	resolveDir := func(dir string) (string, error) {
		dirsMu.Lock()
		defer dirsMu.Unlock()
		if resolved, ok := resolvedDirs[dir]; ok {
			return resolved, nil
		}
		resolved, err := filepath.EvalSymlinks(dir)
		if err == nil {
			resolvedDirs[dir] = resolved
		}
		return resolved, err
	}
	// budget bounds peak memory the way the serial cap check did: workers
	// claim a file's size before reading it, and once claimed bytes cross
	// maxSnapshotBytes the file is left unread instead of materializing the
	// whole tree before the merge can fail. The merge still re-verifies actual
	// bytes in sorted order, so the error contract is unchanged.
	var budget atomic.Int64
	read := func(name string) workingFile {
		result := workingFile{name: name}
		if err := ctx.Err(); err != nil {
			result.err = err
			return result
		}
		full := filepath.Join(r.Root, filepath.FromSlash(name))
		info, err := os.Lstat(full)
		if os.IsNotExist(err) {
			return result
		}
		if err != nil {
			result.err = err
			return result
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(full)
			if err != nil {
				result.err = err
				return result
			}
			result.link = target
			return result
		}
		// The file itself is no symlink (checked above), so resolving its
		// directory is equivalent to resolving the full path.
		dir, base := filepath.Split(full)
		resolvedDir, err := resolveDir(strings.TrimSuffix(dir, string(filepath.Separator)))
		if os.IsNotExist(err) {
			return result
		}
		if err != nil {
			result.err = err
			return result
		}
		if resolved := filepath.Join(resolvedDir, base); resolved != full {
			result.issue = name + ": symlink is not analyzed"
			return result
		}
		if !info.Mode().IsRegular() {
			result.issue = name + ": non-regular file is not analyzed"
			return result
		}
		if info.Size() > maxFileBytes {
			file, err := os.Open(full)
			if err != nil {
				result.err = err
				return result
			}
			digest := sha256.New()
			size, err := io.Copy(digest, contextReader{ctx: ctx, reader: file})
			closeErr := file.Close()
			if err != nil {
				result.err = err
				return result
			}
			if closeErr != nil {
				result.err = closeErr
				return result
			}
			result.opaque = fmt.Sprintf("%d:sha256:%x", size, digest.Sum(nil))
			return result
		}
		if budget.Add(info.Size()) > r.snapshotByteLimit() {
			result.overBudget = true
			return result
		}
		data, err := os.ReadFile(full)
		if err != nil {
			result.err = err
			return result
		}
		result.data = data
		return result
	}
	workers := min(16, len(files))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i] = read(files[i])
			}
		}()
	}
	for i := range files {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	total := 0
	for _, result := range results {
		if result.err != nil {
			return result.err
		}
		if result.overBudget {
			return r.CheckSnapshotBytes(budget.Load())
		}
		switch {
		case result.link != "":
			s.Links[result.name] = result.link
		case result.opaque != "":
			s.Opaque[result.name] = result.opaque
		case result.issue != "":
			s.Issues = append(s.Issues, result.issue)
		case result.data != nil:
			total += len(result.data)
			if err := r.CheckSnapshotBytes(int64(total)); err != nil {
				return err
			}
			s.Files[result.name] = result.data
		}
	}
	return nil
}
