package home

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedHomePrecedence(t *testing.T) {
	data, err := os.ReadFile("../../../../conformance/home.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct{ Name, Explicit, Environment, Expected string }
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	user := filepath.Join(root, "user")
	t.Setenv("HOME", user)
	t.Chdir(root)
	expand := func(value string) string { return strings.NewReplacer("USER", user, "CWD", root).Replace(value) }
	for _, row := range rows {
		t.Run(row.Name, func(t *testing.T) {
			t.Setenv("REPOKIT_HOME", expand(row.Environment))
			got, err := Resolve(expand(row.Explicit))
			want := expand(row.Expected)
			if err != nil || got != want {
				t.Fatalf("%s %v want %s", got, err, want)
			}
			if _, err := os.Stat(want); !os.IsNotExist(err) {
				t.Fatalf("resolution wrote files: %v", err)
			}
		})
	}
}
