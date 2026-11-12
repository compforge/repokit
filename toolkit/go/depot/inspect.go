package depot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
	depotdata "github.com/compforge/repokit/toolkit/go/internal/depot"
	"github.com/compforge/repokit/toolkit/go/internal/git"
	"github.com/compforge/repokit/toolkit/go/internal/project"
)

const receiptName = depotdata.ReceiptName

var nodeLocks = map[string][]string{"npm": {"npm-shrinkwrap.json", "package-lock.json"}, "pnpm": {"pnpm-lock.yaml"}, "yarn": {"yarn.lock"}, "bun": {"bun.lock", "bun.lockb"}}

func problem(code, message string) *errs.Error {
	return &errs.Error{Code: "depot." + code, Message: message}
}
func measure(report *Report, name, bin string, start time.Time) {
	report.Phases = append(report.Phases, Phase{name, bin, time.Since(start).Seconds()})
}

// Inspect observes declared Bins without installing tools, dependencies or creating state.
// +spec=Multiple Components referencing one Bin share an observation; an undeclared Depot is not proof of no dependencies.
// +link=component://docs/depot.md
func Inspect(ctx context.Context, repo string, options Options) (report Report, err error) {
	start := time.Now()
	defer func() {
		measure(&report, "observe", "", start)
		err = errs.Wrap(err, "depot.inspect_failed", "depot.inspect", repo)
	}()
	report.Components = []Component{}
	report.Bins = []Bin{}
	repository, err := git.Open(ctx, repo)
	if err != nil {
		return report, err
	}
	report.Checkout = repository.Root
	files, err := repository.Catalog(ctx, "", false, project.NeedsContent)
	if err != nil {
		return report, err
	}
	layout, err := project.Load(files, "")
	if err != nil {
		return report, err
	}
	selected := map[string]bool{}
	for _, name := range options.Components {
		selected[filepath.ToSlash(filepath.Clean(name))] = true
	}
	found := map[string]bool{}
	bins := map[string]*Bin{}
	for _, component := range layout.Components {
		if component.Depot == nil {
			continue
		}
		for _, bin := range component.Depot.Bins {
			entry := bins[bin.Path]
			if entry == nil {
				entry = &Bin{Bin: bin, Root: path.Dir(bin.Path), Components: []string{}, Command: []string{}, Inputs: []string{}}
				bins[bin.Path] = entry
			}
			entry.Components = append(entry.Components, component.Root)
		}
	}
	wanted := map[string]bool{}
	for _, component := range layout.Components {
		if len(selected) > 0 && !selected[component.Root] {
			continue
		}
		found[component.Root] = true
		report.Components = append(report.Components, Component{component.Root, component.Depot})
		if component.Depot != nil {
			for _, bin := range component.Depot.Bins {
				wanted[bin.Path] = true
			}
		}
	}
	for name := range selected {
		if !found[name] {
			return report, problem("invalid_component", "unknown Component: "+name)
		}
	}
	names := []string{}
	for name := range wanted {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err = ctx.Err(); err != nil {
			return report, err
		}
		entry := bins[name]
		slices.Sort(entry.Components)
		if entry.Kind != "dependencies" {
			entry.Status = "unsupported"
			entry.Reason = "no preparation recipe for this Bin kind"
		} else {
			if err = recipe(report.Checkout, files, entry); err != nil {
				return report, err
			}
			if entry.Status != "unsupported" {
				if err = observe(report.Checkout, entry); err != nil {
					return report, err
				}
			}
		}
		report.Bins = append(report.Bins, *entry)
	}
	latest, err := repository.Catalog(ctx, "", false, project.NeedsContent)
	if err != nil {
		return report, err
	}
	if !reflect.DeepEqual(files, latest) {
		return report, problem("inputs_changed", "repository layout changed during observation")
	}
	report.Complete = true
	return report, nil
}

// checkedPath rejects symlinked ancestors and nested checkouts before reading or executing in a Bin.
func checkedPath(checkout, relative string) (string, error) {
	if relative == "." {
		return checkout, nil
	}
	if path.IsAbs(relative) || path.Clean(relative) != relative || relative == ".." || strings.HasPrefix(relative, "../") {
		return "", problem("unsafe_path", "resource escapes checkout")
	}
	current := checkout
	for _, part := range strings.Split(relative, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", problem("unsafe_path", "symlinked resource: "+relative)
		}
		if info.IsDir() {
			if _, e := os.Lstat(filepath.Join(current, ".git")); e == nil {
				return "", problem("unsafe_path", "nested checkout: "+relative)
			} else if !errors.Is(e, os.ErrNotExist) {
				return "", e
			}
		}
	}
	return current, nil
}

func readInput(checkout, name string) ([]byte, bool, error) {
	file, err := checkedPath(checkout, name)
	if err != nil {
		return nil, false, err
	}
	info, err := os.Lstat(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, problem("unsafe_input", "dependency input is not a regular file: "+name)
	}
	f, err := os.Open(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, problem("unsafe_input", "dependency input is not a regular file: "+name)
	}
	data, err := io.ReadAll(io.LimitReader(f, 32<<20+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > 32<<20 {
		return nil, false, problem("input_limit", "dependency input exceeds 32 MiB: "+name)
	}
	return data, true, nil
}

func recipe(checkout string, files map[string][]byte, bin *Bin) error {
	python := path.Base(bin.Path) == ".venv"
	if !python {
		if _, exists, e := readInput(checkout, path.Join(bin.Root, ".pnp.cjs")); e != nil {
			return e
		} else if exists {
			bin.Status = "unsupported"
			bin.Reason = "Yarn PnP is not supported"
			return nil
		}
	}
	inputs, err := project.DependencyManifests(files, bin.Root, python)
	if err != nil {
		return err
	}
	contents := map[string][]byte{}
	add := func(name string) ([]byte, bool, error) {
		data, exists, e := readInput(checkout, name)
		if e == nil && exists {
			contents[name] = data
		}
		return data, exists, e
	}
	for _, name := range inputs {
		if _, exists, e := add(name); e != nil {
			return e
		} else if !exists {
			return problem("inputs_changed", "manifest disappeared: "+name)
		}
	}
	if python {
		bin.Manager = "uv"
		_, exists, e := add(path.Join(bin.Root, "uv.lock"))
		if e != nil {
			return e
		}
		if exists {
			bin.Command = []string{"uv", "sync", "--locked"}
			if len(inputs) > 1 {
				bin.Command = append(bin.Command, "--all-packages")
			}
		}
	} else {
		var meta struct {
			PackageManager string `json:"packageManager"`
		}
		if err := json.Unmarshal(contents[path.Join(bin.Root, "package.json")], &meta); err != nil {
			return err
		}
		bin.Manager, _, _ = strings.Cut(meta.PackageManager, "@")
		candidates := []string{}
		locks := map[string][]byte{}
		for manager, names := range nodeLocks {
			for _, name := range names {
				data, exists, e := add(path.Join(bin.Root, name))
				if e != nil {
					return e
				}
				if exists {
					if _, ok := locks[manager]; !ok {
						candidates = append(candidates, manager)
						locks[manager] = data
					}
				}
			}
		}
		if bin.Manager == "" {
			if len(candidates) > 1 {
				return problem("ambiguous_manager", "multiple lockfile ecosystems; declare packageManager")
			}
			if len(candidates) == 1 {
				bin.Manager = candidates[0]
			} else {
				bin.Manager = "node"
			}
		}
		if data, ok := locks[bin.Manager]; ok {
			switch bin.Manager {
			case "npm":
				bin.Command = []string{"npm", "ci", "--prefer-offline"}
			case "pnpm", "bun":
				bin.Command = []string{bin.Manager, "install", "--frozen-lockfile"}
			case "yarn":
				flag := "--immutable"
				if strings.HasPrefix(meta.PackageManager, "yarn@1.") || strings.HasPrefix(string(data), "# yarn lockfile v1") {
					flag = "--frozen-lockfile"
				}
				bin.Command = []string{"yarn", "install", flag}
			}
		}
	}
	for _, name := range []string{"pnpm-workspace.yaml", ".npmrc", ".yarnrc.yml", "bunfig.toml", "uv.toml"} {
		if _, _, err := add(path.Join(bin.Root, name)); err != nil {
			return err
		}
	}
	absoluteRoot, err := checkedPath(checkout, bin.Root)
	if err != nil {
		return err
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(absoluteRoot))
	bin.Inputs = []string{}
	for name := range contents {
		bin.Inputs = append(bin.Inputs, name)
	}
	slices.Sort(bin.Inputs)
	for _, name := range bin.Inputs {
		relative, err := filepath.Rel(absoluteRoot, filepath.Join(checkout, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(digest, "%s%c", filepath.ToSlash(relative), 0)
		_, _ = digest.Write(contents[name])
		_, _ = digest.Write([]byte{0})
	}
	bin.Fingerprint = hex.EncodeToString(digest.Sum(nil))
	return nil
}

type receipt = depotdata.Receipt

func observe(checkout string, bin *Bin) error {
	directory, err := checkedPath(checkout, bin.Path)
	if err != nil {
		var e *errs.Error
		if errors.As(err, &e) && e.Code == "depot.unsafe_path" {
			bin.Status, bin.Reason = "unsupported", e.Message
			return nil
		}
		return err
	}
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		bin.Status = "missing"
		if len(bin.Command) == 0 {
			bin.Reason = "no supported locked installer"
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		bin.Status, bin.Reason = "unsupported", "dependency path is not a directory"
		return nil
	}
	value, err := depotdata.ReadReceipt(directory)
	if errors.Is(err, depotdata.ErrUnsafeReceipt) {
		bin.Status, bin.Reason = "unsupported", err.Error()
		return nil
	}
	if errors.Is(err, depotdata.ErrInvalidReceipt) {
		bin.Status, bin.Reason = "stale", err.Error()
		return nil
	}
	if err != nil {
		return err
	}
	if value == nil {
		bin.Status, bin.Reason = "present", "dependencies present without a receipt; consistency unverified"
		return nil
	}
	if value.Manager != bin.Manager || value.Fingerprint != bin.Fingerprint {
		bin.Status, bin.Reason = "stale", "dependency inputs or receipt changed"
		return nil
	}
	bin.Status, bin.Reason = "ready", ""
	return nil
}
