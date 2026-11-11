package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
