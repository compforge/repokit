package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compforge/repokit/toolkit/go"
)

func inspectJSON(t *testing.T, dir string, extra ...string) repokit.InspectReport {
	t.Helper()
	var out, stderr bytes.Buffer
	args := append([]string{"inspect", "--repo", dir, "--json"}, extra...)
	if code := Execute(context.Background(), args, nil, &out, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d error=%s", code, stderr.String())
	}
	var report repokit.InspectReport
	if err := json.Unmarshal(resultData(t, out.Bytes()), &report); err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(resultData(t, out.Bytes()), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["snapshot"]; ok {
		t.Fatal("inspection unexpectedly reports a content identity")
	}
	if report.SchemaVersion != 1 || report.Diagnostics == nil || report.Components == nil {
		t.Fatalf("report: %+v", report)
	}
	return report
}

func TestInspectReadsMetadataWithoutSourceOrSubmoduleCapture(t *testing.T) {
	dir := fixture(t)
	put(t, dir, "go.mod", "module example.com/app\n")
	put(t, dir, "testdata/corpus/go.mod", "module fixture\n")
	put(t, dir, ".gitignore", "ignored/\n")
	put(t, dir, "ignored/package.json", `{"name":"ignored"}`)
	gitCommand(t, dir, "add", ".")
	gitCommand(t, dir, "commit", "-qm", "layout")
	commit := gitCommand(t, dir, "rev-parse", "HEAD")
	// An unavailable gitlink is irrelevant to parent Component discovery.
	gitCommand(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+strings.TrimSpace(commit)+",child")
	gitCommand(t, dir, "commit", "-qm", "uninitialized child")
	large := filepath.Join(dir, "large.go")
	file, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(256 << 20); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(large, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(large, 0600) })
	for _, flags := range [][]string{nil, {"--staged"}, {"--head", "HEAD"}} {
		report := inspectJSON(t, dir, flags...)
		if !report.Complete || len(report.Components) != 1 || report.Components[0].Root != "." || report.Components[0].Language != "go" {
			t.Fatalf("report: %+v", report)
		}
	}

}

func TestSnapshotIsIndependentOfRepositoryConfiguration(t *testing.T) {
	dir := fixture(t)
	put(t, dir, ".repocli.json", `{`)
	put(t, dir, "package.json", `{"name":`)
	report := snapshotJSON(t, dir)
	if !report.Complete {
		t.Fatalf("configuration blocked content identity: %+v", report)
	}
	var out, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"snapshot", "--repo", dir, "--json"}, nil, &out, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(resultData(t, out.Bytes()), &raw); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"repository", "components", "observations"} {
		if _, ok := raw[field]; ok {
			t.Fatalf("snapshot leaked %s", field)
		}
	}
	out.Reset()
	stderr.Reset()
	if code := Execute(context.Background(), []string{"inspect", "--repo", dir}, nil, &out, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
}

func TestInspectUsageAndOutputErrors(t *testing.T) {
	for _, args := range [][]string{{"inspect", "extra"}, {"inspect", "--timeout", "0s"}, {"inspect", "--head", "HEAD", "--staged"}} {
		var out, stderr bytes.Buffer
		if code := Execute(context.Background(), args, nil, &out, &stderr); code != 2 || out.Len() != 0 {
			t.Fatal(args, code, out.String(), stderr.String())
		}
	}
	dir := fixture(t)
	for _, flags := range [][]string{nil, {"--json"}} {
		var stderr bytes.Buffer
		args := append([]string{"inspect", "--repo", dir}, flags...)
		if code := Execute(context.Background(), args, nil, failedWriter{}, &stderr); code != 1 || !strings.Contains(stderr.String(), "output unavailable") {
			t.Fatal(code, stderr.String())
		}
	}
}

func TestInspectRejectsUnreadableMetadataInsteadOfInventingLayout(t *testing.T) {
	for _, mode := range []string{"symlink", "large"} {
		t.Run(mode, func(t *testing.T) {
			dir := fixture(t)
			if mode == "symlink" {
				if err := os.Symlink("source file.ts", filepath.Join(dir, "package.json")); err != nil {
					t.Fatal(err)
				}
			} else {
				put(t, dir, "package.json", strings.Repeat(" ", (2<<20)+1))
			}
			gitCommand(t, dir, "add", ".")
			gitCommand(t, dir, "commit", "-qm", "metadata")
			for _, flags := range [][]string{nil, {"--staged"}, {"--head", "HEAD"}} {
				var out, stderr bytes.Buffer
				args := append([]string{"inspect", "--repo", dir, "--json"}, flags...)
				if code := Execute(context.Background(), args, nil, &out, &stderr); code != 1 || stderr.Len() != 0 || !strings.Contains(out.String(), "metadata package.json") || string(resultData(t, out.Bytes())) != "null" {
					t.Fatal(code, out.String(), stderr.String())
				}
			}
		})
	}
}
