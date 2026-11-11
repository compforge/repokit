package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/compforge/repokit/toolkit/go/state"
)

// +case=Ensure JSON carries confirmed identity, explicit action and phase costs; conflicting requests retain a structured failure.
func TestWorktreeEnsureCLI(t *testing.T) {
	root := fixture(t)
	t.Setenv("REPOKIT_HOME", t.TempDir())
	target := filepath.Join(root, "linked")
	for _, want := range []string{"created", "reused"} {
		var out, stderr bytes.Buffer
		code := Execute(context.Background(), []string{"worktree", "ensure", target, "--branch", "topic", "--base", "HEAD", "--repo", root, "--json"}, nil, &out, &stderr)
		var r state.EnsureWorktreeReport
		if e := json.Unmarshal(resultData(t, out.Bytes()), &r); e != nil {
			t.Fatal(e)
		}
		var envelope commandResult
		_ = json.Unmarshal(out.Bytes(), &envelope)
		if code != 0 || !r.Confirmed || r.Action != want || len(envelope.Cost.Phases) == 0 || stderr.Len() != 0 {
			t.Fatal(code, out.String(), stderr.String())
		}
	}
	var out, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"worktree", "ensure", target, "--branch", "different", "--base", "HEAD", "--repo", root, "--json"}, nil, &out, &stderr)
	var envelope commandResult
	_ = json.Unmarshal(out.Bytes(), &envelope)
	if code != 1 || envelope.Error == nil || envelope.Error.Code != "state.worktree_conflict" {
		t.Fatal(code, out.String())
	}
	out.Reset()
	stderr.Reset()
	code = Execute(context.Background(), []string{"worktree", "ensure", target, "--repo", root, "--json"}, nil, &out, &stderr)
	if code != 2 {
		t.Fatal(code, out.String())
	}
}
