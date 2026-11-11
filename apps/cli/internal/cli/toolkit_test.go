package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/compforge/quality-harness/sdks/go/common"
	"github.com/compforge/repokit/apps/cli/internal/cli"
	"github.com/compforge/repokit/toolkit/go"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=toolkit-test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=toolkit-test", "GIT_COMMITTER_EMAIL=test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
}

func put(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init", "-q", "-b", "main")
	put(t, root, "go.mod", "module example/toolkit\n\ngo 1.26\n")
	put(t, root, "main.go", "package main\nfunc main() {}\n")
	put(t, root, "testdata/corpus/go.mod", "module example/corpus\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "fixture")
	return root
}

// +case=`Go callers and CLI adapters observe the same selected contents and organization`
func TestLibraryAndCLIShareReports(t *testing.T) {
	root, home := fixture(t), t.TempDir()
	t.Setenv("HOME", home)
	put(t, root, "main.go", "package main\nfunc main() { println(1) }\n")
	git(t, root, "add", "main.go")
	put(t, root, "main.go", "package main\nfunc main() { println(2) }\n")
	for _, tc := range []struct {
		name  string
		req   repokit.InputRequest
		flags []string
	}{
		{"working", repokit.InputRequest{Repository: root}, nil},
		{"index", repokit.InputRequest{Repository: root, Staged: true}, []string{"--staged"}},
		{"commit", repokit.InputRequest{Repository: root, Head: "HEAD"}, []string{"--head", "HEAD"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inspect, err := repokit.Inspect(context.Background(), tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if len(inspect.Components) != 1 || inspect.Owner("testdata/corpus/missing.go").Root != "." {
				t.Fatalf("wrong organization: %+v", inspect)
			}
			// Public results retain common identity types, without a conversion DTO.
			var binding *repokit.ComponentBinding = inspect.Owner("main.go")
			var identity common.Component = binding.Component
			var repository *common.Repository = inspect.Repository
			var products []common.Product = binding.Products
			_ = repository
			_ = products
			if identity.Name != binding.Name {
				t.Fatal("component identity drift")
			}
			snapshot, err := repokit.Snapshot(context.Background(), tc.req)
			if err != nil {
				t.Fatal(err)
			}
			diff, err := repokit.AnalyzeImpact(context.Background(), repokit.DiffRequest{Repository: root, Head: tc.req.Head, Staged: tc.req.Staged})
			if err != nil {
				t.Fatal(err)
			}
			if diff.Snapshot != snapshot.Snapshot {
				t.Fatal("content identity differs")
			}
			for _, report := range []struct {
				command string
				value   any
			}{{"inspect", inspect}, {"snapshot", snapshot}, {"impact", diff}} {
				want, err := json.Marshal(report.value)
				if err != nil {
					t.Fatal(err)
				}
				var stdout, stderr bytes.Buffer
				args := append([]string{report.command, "--repo", root, "--json"}, tc.flags...)
				if code := cli.Execute(context.Background(), args, nil, &stdout, &stderr); code != 0 {
					t.Fatal(code, stderr.String())
				}
				var a, b any
				if err := json.Unmarshal(want, &a); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(stdout.Bytes(), &b); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(a, b.(map[string]any)["data"]) {
					t.Fatalf("%s CLI/library mismatch", report.command)
				}
			}
		})
	}
}
