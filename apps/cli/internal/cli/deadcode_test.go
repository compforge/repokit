package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/compforge/repokit/toolkit/go"
)

func TestDeadcodeCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	gitCommand(t, root, "init", "-q", "-b", "main")
	put(t, root, "main.go", "package main\nfunc main() { used() }\nfunc used() {}\nfunc orphan() {}\n")
	gitCommand(t, root, "add", ".")
	gitCommand(t, root, "commit", "-qm", "fixture")
	for _, flags := range [][]string{nil, {"--head", "HEAD"}, {"--staged"}} {
		var out, stderr bytes.Buffer
		args := append([]string{"deadcode", "--repo", root, "--json"}, flags...)
		if code := Execute(context.Background(), args, nil, &out, &stderr); code != 0 {
			t.Fatalf("%d: %s", code, &stderr)
		}
		var report repokit.DeadcodeReport
		if err := json.Unmarshal(resultData(t, out.Bytes()), &report); err != nil {
			t.Fatal(err)
		}
		if len(report.Nodes) != 2 || report.Nodes[0].Name != "main" || report.Nodes[1].Name != "orphan" {
			t.Fatal(report.Nodes)
		}
	}
	var filtered, filterErrors bytes.Buffer
	if code := Execute(context.Background(), []string{"deadcode", "--repo", root, "--exclude-tests", "--exclude-entrypoints", "--json"}, nil, &filtered, &filterErrors); code != 0 {
		t.Fatalf("%d: %s", code, &filterErrors)
	}
	var report repokit.DeadcodeReport
	if err := json.Unmarshal(resultData(t, filtered.Bytes()), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Nodes) != 1 || report.Nodes[0].Name != "orphan" {
		t.Fatal(report.Nodes)
	}
	var out, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"deadcode", "--repo", root}, nil, &out, &stderr); code != 0 {
		t.Fatalf("%d: %s", code, &stderr)
	}
	if !strings.Contains(out.String(), "Deadcode candidates: 2") || !strings.Contains(out.String(), "main.go:4\tFunction\torphan") {
		t.Fatal(out.String())
	}
	for _, flags := range [][]string{{"--max-nodes", "-1"}, {"--max-relations", "-1"}, {"--timeout", "0s"}, {"--head", "HEAD", "--staged"}, {"unexpected"}} {
		out.Reset()
		stderr.Reset()
		if code := Execute(context.Background(), append([]string{"deadcode", "--repo", root}, flags...), nil, &out, &stderr); code != 2 {
			t.Fatalf("%v: %d %s", flags, code, &stderr)
		}
	}
	out.Reset()
	stderr.Reset()
	if code := Execute(context.Background(), []string{"deadcode", "--repo", root, "--head", "missing-ref"}, nil, &out, &stderr); code != 1 {
		t.Fatalf("%d %s", code, &stderr)
	}
}

type deadcodeBrokenWriter struct{}

func (deadcodeBrokenWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestDeadcodeWriterErrors(t *testing.T) {
	if err := writeDeadcode(deadcodeBrokenWriter{}, repokit.DeadcodeReport{}, false); err == nil {
		t.Fatal("lost write error")
	}
}

func TestDeadcodeCLIDiffAndFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	gitCommand(t, root, "init", "-q", "-b", "main")
	put(t, root, "main.go", "package main\nfunc main(){target()}\n")
	put(t, root, "target.go", "package main\nfunc target(){leaf()}\nfunc leaf(){}\nfunc unrelated(){}\n")
	gitCommand(t, root, "add", ".")
	gitCommand(t, root, "commit", "-qm", "before")
	put(t, root, "main.go", "package main\nfunc main(){}\n")
	var out, stderr bytes.Buffer
	args := []string{"deadcode", "--repo", root, "--base", "HEAD", "--mode", "unreachable", "--json"}
	if code := Execute(context.Background(), args, nil, &out, &stderr); code != 0 {
		t.Fatalf("code=%d %s", code, &stderr)
	}
	var report repokit.DeadcodeReport
	if err := json.Unmarshal(resultData(t, out.Bytes()), &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 2 || len(report.Nodes) != 2 || report.Baseline == nil {
		t.Fatalf("report=%+v", report)
	}
	for _, node := range report.Nodes {
		if node.Change != "became_dead" || node.Location.Path != "target.go" {
			t.Fatalf("candidate=%+v", node)
		}
	}
	out.Reset()
	stderr.Reset()
	if code := Execute(context.Background(), []string{"deadcode", "--repo", root, "--file", "target.go", "--json"}, nil, &out, &stderr); code != 0 {
		t.Fatalf("code=%d %s", code, &stderr)
	}
	if err := json.Unmarshal(resultData(t, out.Bytes()), &report); err != nil {
		t.Fatal(err)
	}
	if report.Scope.Kind != "files" || len(report.Nodes) != 2 {
		t.Fatalf("report=%+v", report)
	}
	for _, flags := range [][]string{{"--mode", "invalid"}, {"--root", "whatever"}, {"--file", "../outside.go"}} {
		out.Reset()
		stderr.Reset()
		if code := Execute(context.Background(), append([]string{"deadcode", "--repo", root}, flags...), nil, &out, &stderr); code != 2 {
			t.Fatalf("flags=%v code=%d stderr=%s", flags, code, &stderr)
		}
	}
}

// +case:id=deadcode_cli_literal_file_paths,desc=`File selection receives comma-containing paths, repeated flags or an empty argument`,expect=`Preserve literal paths and reject empty arguments without changing the default repository scope`
func TestDeadcodeCLILiteralFilePaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	gitCommand(t, root, "init", "-q", "-b", "main")
	put(t, root, "comma,name.go", "package fixture\nfunc unusedComma(){}\n")
	put(t, root, "normal.go", "package fixture\nfunc unusedNormal(){}\n")
	put(t, root, "other.go", "package fixture\nfunc unusedOther(){}\n")
	for _, tc := range []struct {
		name        string
		files, want []string
		code        int
	}{
		{name: "repository", want: []string{"comma,name.go", "normal.go", "other.go"}},
		{name: "comma", files: []string{"comma,name.go"}, want: []string{"comma,name.go"}},
		{name: "repeated", files: []string{"comma,name.go", "normal.go"}, want: []string{"comma,name.go", "normal.go"}},
		{name: "empty", files: []string{""}, code: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"deadcode", "--repo", root, "--json"}
			for _, file := range tc.files {
				args = append(args, "--file", file)
			}
			var out, stderr bytes.Buffer
			if code := Execute(context.Background(), args, nil, &out, &stderr); code != tc.code {
				t.Fatalf("code=%d want=%d stdout=%s stderr=%s", code, tc.code, &out, &stderr)
			}
			if tc.code != 0 {
				var result commandResult
				if err := json.Unmarshal(out.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.Outcome != "failed" || result.Error == nil || result.Error.Code != "usage.invalid_argument" || result.Data != nil {
					t.Fatalf("expected argument failure, got %+v", result)
				}
				return
			}
			var report repokit.DeadcodeReport
			if err := json.Unmarshal(resultData(t, out.Bytes()), &report); err != nil {
				t.Fatal(err)
			}
			var paths []string
			for _, node := range report.Nodes {
				paths = append(paths, node.Location.Path)
			}
			kind := "files"
			if tc.files == nil {
				kind = "repository"
			}
			if report.Scope.Kind != kind || !slices.Equal(report.Scope.Files, tc.files) || !slices.Equal(paths, tc.want) {
				t.Fatalf("scope=%+v paths=%v want=%v", report.Scope, paths, tc.want)
			}
		})
	}
}
