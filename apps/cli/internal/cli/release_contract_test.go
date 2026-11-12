package cli

import (
	"golang.org/x/mod/modfile"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// +case=Local validation rejects CLI/module version drift before immutable release tags are published.
// +link=component://docs/release.md
func TestReleaseModuleVersions(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	read := func(name string) []byte {
		t.Helper()
		b, e := os.ReadFile(filepath.Join(root, name))
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	version := "v" + strings.TrimSpace(string(read("VERSION")))
	module := "github.com/compforge/repokit/toolkit/go"
	cli, e := modfile.Parse("go.mod", read("apps/cli/go.mod"), nil)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, r := range cli.Require {
		if r.Mod.Path == module {
			found = true
			if r.Mod.Version != version {
				t.Fatalf("CLI requires %s; VERSION requires %s", r.Mod.Version, version)
			}
		}
	}
	if !found {
		t.Fatal("CLI toolkit dependency missing")
	}
	workspace, e := modfile.ParseWork("go.work", read("go.work"), nil)
	if e != nil {
		t.Fatal(e)
	}
	found = false
	for _, r := range workspace.Replace {
		if r.Old.Path == module {
			found = true
			if r.Old.Version != version || r.New.Path != "./toolkit/go" {
				t.Fatalf("workspace toolkit binding does not match %s: %+v", version, r)
			}
		}
	}
	if !found {
		t.Fatal("workspace toolkit binding missing")
	}
}
