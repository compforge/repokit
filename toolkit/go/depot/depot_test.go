package depot_test

import (
	"context"
	"encoding/json"
	"github.com/compforge/repokit/toolkit/go/depot"
	"github.com/compforge/repokit/toolkit/go/state"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, root, name, text string) {
	t.Helper()
	file := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
}
func fixture(t *testing.T, script string) string {
	t.Helper()
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-q", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	write(t, root, ".gitignore", "node_modules/\n.venv/\ncalls\nstarted\n")
	write(t, root, "package.json", `{"name":"fixture","packageManager":"npm@10.0.0","workspaces":["apps/*"]}`)
	write(t, root, "package-lock.json", `{"lockfileVersion":3}`)
	write(t, root, "apps/a/package.json", `{"name":"a"}`)
	write(t, root, "apps/b/package.json", `{"name":"b"}`)
	bin := t.TempDir()
	write(t, bin, "npm", "#!/bin/sh\n"+script+"\n")
	if err := os.Chmod(filepath.Join(bin, "npm"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("REPOKIT_HOME", t.TempDir())
	return root
}

const install = "echo install >> calls\nmkdir -p node_modules\necho installed"

// +case=Shared workspace Bins install once and reuse receipts; lock or member manifest changes invalidate readiness.
func TestSharedInstallationAndReuse(t *testing.T) {
	root := fixture(t, install)
	before, err := depot.Inspect(context.Background(), root, depot.Options{Components: []string{"apps/a"}})
	if err != nil || !before.Complete || len(before.Bins) != 1 || len(before.Bins[0].Components) != 3 || before.Bins[0].Status != "missing" {
		t.Fatal(before, err)
	}
	if _, err := os.Stat(filepath.Join(root, "node_modules")); !os.IsNotExist(err) {
		t.Fatal("inspection wrote output", err)
	}
	report, err := depot.Prepare(context.Background(), root, depot.Options{})
	if err != nil || len(report.Bins) != 1 || report.Bins[0].Status != "ready" || !report.Bins[0].Executed {
		t.Fatal(report, err)
	}
	report, err = depot.Prepare(context.Background(), root, depot.Options{})
	if err != nil || report.Bins[0].Action != "reused" || report.Bins[0].Executed {
		t.Fatal(report, err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "calls"))
	if string(data) != "install\n" {
		t.Fatal(string(data))
	}
	write(t, root, "apps/b/package.json", `{"name":"b","version":"2.0.0"}`)
	report, err = depot.Inspect(context.Background(), root, depot.Options{})
	if err != nil || report.Bins[0].Status != "stale" {
		t.Fatal(report, err)
	}
}
func TestPresentAndUnsupported(t *testing.T) {
	root := fixture(t, install)
	if err := os.Mkdir(filepath.Join(root, "node_modules"), 0755); err != nil {
		t.Fatal(err)
	}
	report, err := depot.Prepare(context.Background(), root, depot.Options{})
	if err != nil || report.Bins[0].Status != "present" || report.Bins[0].Action != "preserved" || report.Bins[0].Executed {
		t.Fatal(report, err)
	}
	if err := os.Remove(filepath.Join(root, "node_modules")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "package-lock.json")); err != nil {
		t.Fatal(err)
	}
	report, err = depot.Prepare(context.Background(), root, depot.Options{})
	if err == nil || report.Bins[0].Action != "unsupported" || report.Bins[0].Executed {
		t.Fatal(report, err)
	}
	if _, err = depot.Inspect(context.Background(), root, depot.Options{Components: []string{"unknown"}}); err == nil {
		t.Fatal("unknown component accepted")
	}
}

// +case=Installer failure, changing inputs and cancellation never produce a ready receipt.
func TestFailedInstallation(t *testing.T) {
	for _, tc := range []struct {
		name, script, action string
		timeout              time.Duration
	}{
		{"exit", "mkdir -p node_modules; echo broken >&2; exit 3", "failed", 0},
		{"input changed", install + "\necho changed >> package-lock.json", "failed", 0},
		{"missing output", "true", "failed", 0},
		{"cancel", "mkdir -p node_modules; touch started; sleep 30", "uncertain", 10 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := fixture(t, tc.script)
			ctx := context.Background()
			if tc.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.timeout)
				defer cancel()
				// Cancel after installation starts, independent of Git inspection latency.
				go func() {
					ticker := time.NewTicker(10 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-ticker.C:
							if _, err := os.Stat(filepath.Join(root, "started")); err == nil {
								cancel()
								return
							}
						}
					}
				}()
			}
			report, err := depot.Prepare(ctx, root, depot.Options{})
			if err == nil || len(report.Bins) != 1 || report.Bins[0].Action != tc.action || report.Bins[0].Status == "ready" {
				t.Fatal(report, err)
			}
			if _, e := os.Stat(filepath.Join(root, "node_modules/.repokit-dependencies.json")); !os.IsNotExist(e) {
				t.Fatal("ready receipt survived", e)
			}
		})
	}
}
func TestUnsafeAndTrackedBins(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		root := fixture(t, install)
		if err := os.Symlink(t.TempDir(), filepath.Join(root, "node_modules")); err != nil {
			t.Fatal(err)
		}
		r, err := depot.Prepare(context.Background(), root, depot.Options{})
		if err == nil || r.Bins[0].Executed || r.Bins[0].Status != "unsupported" {
			t.Fatal(r, err)
		}
	})
	t.Run("tracked", func(t *testing.T) {
		root := fixture(t, install)
		write(t, root, "node_modules/file", "valuable")
		write(t, root, "node_modules/.repokit-dependencies.json", `{"fingerprint":"stale","manager":"npm"}`)
		cmd := exec.Command("git", "-C", root, "add", "-f", "node_modules/file")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(out), err)
		}
		r, err := depot.Prepare(context.Background(), root, depot.Options{})
		if err == nil || r.Bins[0].Executed {
			t.Fatal(r, err)
		}
		data, _ := os.ReadFile(filepath.Join(root, "node_modules/file"))
		if string(data) != "valuable" {
			t.Fatal(string(data))
		}
	})
}

// +case=Preparation waits for the shared cleanup lease, honors the caller deadline, then executes only once across concurrent callers.
func TestLeaseAndConcurrentPreparation(t *testing.T) {
	root := fixture(t, install)
	locked, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- state.WithCheckoutResources(context.Background(), root, []string{"node_modules"}, func(context.Context) error { close(locked); <-release; return nil })
	}()
	<-locked
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r, err := depot.Prepare(ctx, root, depot.Options{})
	if err == nil || ctx.Err() != context.DeadlineExceeded {
		t.Fatal(r, err)
	}
	// The caller's deadline may expire during initial inspection, before Bins exist.
	for _, bin := range r.Bins {
		if bin.Executed {
			t.Fatal("installation ran while the lease was held", r)
		}
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for range 2 {
		go func() { _, e := depot.Prepare(context.Background(), root, depot.Options{}); results <- e }()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(root, "calls"))
	if strings.Count(string(data), "install") != 1 {
		t.Fatal(string(data))
	}
}
func TestUVEnvironmentAndReceipt(t *testing.T) {
	root := fixture(t, install)
	os.Remove(filepath.Join(root, "package.json"))
	os.RemoveAll(filepath.Join(root, "apps"))
	write(t, root, "pyproject.toml", "[project]\nname='fixture'\nversion='1.0.0'\n")
	write(t, root, "uv.lock", "version = 1\n")
	bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	write(t, bin, "uv", "#!/bin/sh\ntest -z \"$VIRTUAL_ENV$UV_PROJECT_ENVIRONMENT\" || exit 7\nmkdir -p .venv\n")
	os.Chmod(filepath.Join(bin, "uv"), 0755)
	t.Setenv("VIRTUAL_ENV", "elsewhere")
	t.Setenv("UV_PROJECT_ENVIRONMENT", "elsewhere")
	r, err := depot.Prepare(context.Background(), root, depot.Options{})
	if err != nil || r.Bins[0].Status != "ready" {
		t.Fatal(r, err)
	}
	data, _ := os.ReadFile(filepath.Join(root, ".venv/.repokit-dependencies.json"))
	var receipt map[string]string
	if json.Unmarshal(data, &receipt) != nil || receipt["manager"] != "uv" || receipt["generation"] == "" {
		t.Fatal(string(data))
	}
}
