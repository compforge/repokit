package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/compforge/repokit/toolkit/go/depot"
	"os"
	"path/filepath"
	"testing"
)

// +case=Depot CLI preserves failed Bin evidence and phase costs in one JSON envelope; present is a warning, not ready.
func TestDepotCLIReport(t *testing.T) {
	repo := fixture(t)
	if e := os.WriteFile(filepath.Join(repo, "package.json"), []byte(`{"name":"fixture"}`), 0644); e != nil {
		t.Fatal(e)
	}
	t.Setenv("REPOKIT_HOME", t.TempDir())
	for _, operation := range []string{"inspect", "prepare"} {
		var out, stderr bytes.Buffer
		code := Execute(context.Background(), []string{"depot", operation, "--repo", repo, "--json"}, nil, &out, &stderr)
		var report depot.Report
		if e := json.Unmarshal(resultData(t, out.Bytes()), &report); e != nil {
			t.Fatal(e)
		}
		var result commandResult
		_ = json.Unmarshal(out.Bytes(), &result)
		if stderr.Len() != 0 || len(report.Bins) != 1 || report.Bins[0].Status != "missing" || len(result.Cost.Phases) == 0 {
			t.Fatal(code, out.String(), stderr.String())
		}
		if operation == "prepare" && (code == 0 || result.Error == nil || report.Bins[0].Error == nil) {
			t.Fatal(out.String())
		}
		if operation == "inspect" && code != 0 {
			t.Fatal(out.String())
		}
	}
	if e := os.Mkdir(filepath.Join(repo, "node_modules"), 0755); e != nil {
		t.Fatal(e)
	}
	var out, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"depot", "prepare", "--repo", repo, "--json"}, nil, &out, &stderr)
	var result commandResult
	_ = json.Unmarshal(out.Bytes(), &result)
	if code != 0 || len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != "depot.unverified_environment" {
		t.Fatal(out.String())
	}
}
