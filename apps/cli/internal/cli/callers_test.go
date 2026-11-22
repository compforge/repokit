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

// +case=Symbol commands expose the same declaration positions and report coverage through the normal CLI entry point.
func TestSymbolNavigationCLI(t *testing.T) {
	root, home := fixture(t), t.TempDir()
	put(t, root, "nav.ts", "class Base {}\nclass Mid extends Base {}\nclass Leaf extends Mid {}\nfunction run() { work(); }\nfunction work() {}\n")
	for _, tc := range []struct {
		args   []string
		code   int
		want   []string
		absent string
	}{
		{[]string{"hierarchy", "--at", "nav.ts:2:7"}, 0, []string{"extends Base (nav.ts:1:7)", "extended by Leaf (nav.ts:3:7)"}, ""},
		{[]string{"hierarchy", "--at", "nav.ts:2:7", "--direction", "children"}, 0, []string{"extended by Leaf"}, "Parents"},
		{[]string{"hierarchy", "--at", "nav.ts:2:7", "--direction", "parents"}, 0, []string{"extends Base"}, "Children"},
		{[]string{"hierarchy", "--at", "nav.ts:2:7", "--direction", "invalid"}, 2, []string{"direction"}, ""},
		{[]string{"callees", "--at", "nav.ts:4:10"}, 0, []string{"nav.ts:4:", "work (nav.ts:5:10)"}, ""},
		{[]string{"callees", "--at", "nav.ts:2:7", "--json"}, 1, []string{"code.callees_failed"}, ""},
	} {
		var out, stderr bytes.Buffer
		args := append([]string{"code"}, tc.args...)
		args = append(args, "--repo", root, "--home", home)
		code := Execute(context.Background(), args, nil, &out, &stderr)
		text := out.String() + stderr.String()
		if code != tc.code {
			t.Fatalf("%v: exit %d: %s", tc.args, code, text)
		}
		for _, part := range tc.want {
			if !strings.Contains(text, part) {
				t.Errorf("%v missing %q: %s", tc.args, part, text)
			}
		}
		if tc.absent != "" && strings.Contains(text, tc.absent) {
			t.Errorf("%v contained %q: %s", tc.args, tc.absent, text)
		}
	}
}
