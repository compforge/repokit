package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// +case=Lookup failures retain file-specific coverage evidence without reporting zero callers for an unresolved target.
func TestCallersLookupFailureDiagnostics(t *testing.T) {
	root := fixture(t)
	home := t.TempDir()
	put(t, root, "large.go", "package demo\nfunc Target() {}\n//"+strings.Repeat("x", 2<<20))
	for _, tc := range []struct{ at, reason string }{
		{"large.go:2:6", "byte limit"},
		{"source file.ts:1:1", "no indexed callable declaration name"},
	} {
		t.Run(tc.at, func(t *testing.T) {
			var out, stderr bytes.Buffer
			code := Execute(context.Background(), []string{"code", "callers", "--at", tc.at, "--json", "--repo", root, "--home", home}, nil, &out, &stderr)
			var result commandResult
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatalf("invalid result: %v, stderr=%s", err, &stderr)
			}
			if code != 1 || result.Outcome != "failed" || result.Error == nil || !strings.Contains(result.Error.Message, tc.reason) || strings.Contains(result.Summary, "0 static call") {
				t.Fatalf("unexpected lookup result: %s", &out)
			}
			if !slices.ContainsFunc(result.Diagnostics, func(d diagnostic) bool {
				return d.Path == "large.go" && d.Code == "document_byte_limit" && d.Evidence != ""
			}) {
				t.Fatalf("missing file budget diagnostic: %s", &out)
			}
		})
	}
}
