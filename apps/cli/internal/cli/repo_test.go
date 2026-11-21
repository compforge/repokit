package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRepoFindEmptyHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "uncreated")
	var out, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"repo", "find", "example", "--home", home, "--json"}, nil, &out, &stderr)
	var result struct {
		Outcome string
		Data    struct{ Matches []json.RawMessage }
	}
	if code != 0 || json.Unmarshal(out.Bytes(), &result) != nil || result.Outcome != "succeeded" || result.Data.Matches == nil || len(result.Data.Matches) != 0 {
		t.Fatalf("%d: %s %s", code, out.String(), stderr.String())
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("query created Home: %v", err)
	}
}
