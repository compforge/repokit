package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	cg "github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go"
)

func TestDiffUnitsCapturedInputsAndDisplay(t *testing.T) {
	dir := fixture(t)
	source := "import {parse} from './lib';\nexport function first(){return parse('1')}\nexport function second(){return parse('2')}\n"
	put(t, dir, "app.ts", source)
	put(t, dir, "lib.ts", "export function parse(s:string){return s}\n")
	gitCommand(t, dir, "add", "app.ts", "lib.ts")
	patch := gitCommand(t, dir, "diff", "--cached", "--binary")
	baseline := gitCommand(t, dir, "status", "--porcelain=v1")
	for _, mode := range []string{"working", "index", "patch", "commit"} {
		t.Run(mode, func(t *testing.T) {
			args := []string{"diff", "--repo", dir, "--units", "--json"}
			input := ""
			switch mode {
			case "index":
				args = append(args, "--staged")
			case "patch":
				args = append(args, "--file", "-")
				input = patch
			case "commit":
				gitCommand(t, dir, "commit", "-qm", "add sources")
				args = append(args, "--base", "HEAD~", "--head", "HEAD")
			}
			before := gitCommand(t, dir, "status", "--porcelain=v1")
			var out, stderr bytes.Buffer
			if code := Execute(context.Background(), args, strings.NewReader(input), &out, &stderr); code != 0 {
				t.Fatalf("%d: %s", code, &stderr)
			}
			var r repokit.UnitReport
			if err := json.Unmarshal(resultData(t, out.Bytes()), &r); err != nil {
				t.Fatal(err)
			}
			if len(r.Changes) != 2 || len(r.Fragments) == 0 || len(r.Units) == 0 {
				t.Fatal(out.String())
			}
			for _, change := range r.Changes {
				var fs []repokit.Fragment
				for _, f := range r.Fragments {
					if f.Path == change.Path() {
						fs = append(fs, f)
					}
				}
				if err := repokit.ValidateFragmentEdits(change, fs); err != nil {
					t.Fatal(err)
				}
			}
			if after := gitCommand(t, dir, "status", "--porcelain=v1"); after != before {
				t.Fatalf("analysis wrote Git state: %q -> %q", before, after)
			}
			var display bytes.Buffer
			if err := writeUnits(&display, r, false); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(display.String(), "Diff -> Fragment -> Unit") || !strings.Contains(display.String(), "import:1") {
				t.Fatal(display.String())
			}
		})
	}
	if baseline == "" {
		t.Fatal("missing staged fixture")
	}
}

func TestUnitDisplayShowsChangeTags(t *testing.T) {
	dir := fixture(t)
	put(t, dir, "kitex_gen/generated.go", "package generated\nfunc Work(){}\n")
	for _, jsonOutput := range []bool{false, true} {
		args := []string{"diff", "--repo", dir, "--units"}
		if jsonOutput {
			args = append(args, "--json")
		}
		var out, stderr bytes.Buffer
		if code := Execute(context.Background(), args, strings.NewReader(""), &out, &stderr); code != 0 {
			t.Fatalf("%d: %s", code, &stderr)
		}
		if !jsonOutput {
			if !strings.Contains(out.String(), "tags: [generated source]") {
				t.Fatal(out.String())
			}
			continue
		}
		var report repokit.UnitReport
		if err := json.Unmarshal(resultData(t, out.Bytes()), &report); err != nil {
			t.Fatal(err)
		}
		if len(report.Changes) != 1 || len(report.Changes[0].Tags) != 2 || report.Changes[0].Tags[0] != cg.GeneratedTag || report.Changes[0].Tags[1] != cg.SourceTag {
			t.Fatal(report.Changes)
		}
	}
}
