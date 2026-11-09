package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestHomeFlagOwnsLogsAndState(t *testing.T) {
	user, envHome, explicit, repo := t.TempDir(), filepath.Join(t.TempDir(), "environment"), filepath.Join(t.TempDir(), "explicit"), t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("REPOKIT_HOME", envHome)
	gitCommand(t, repo, "init", "-q")
	var out, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"inspect", "--repo", repo, "--home", explicit, "--json"}, nil, &out, &stderr); code != 0 {
		t.Fatalf("%d %s", code, stderr.String())
	}
	paths, _ := filepath.Glob(filepath.Join(explicit, "logs", "*.log"))
	if len(paths) != 1 {
		t.Fatal(paths)
	}
	for _, path := range []string{envHome, filepath.Join(user, ".repokit"), filepath.Join(user, ".repocli")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("unexpected home write", path, err)
		}
	}
	out.Reset()
	stderr.Reset()
	if code := Execute(context.Background(), []string{"stats", "--home", explicit, "--json"}, nil, &out, &stderr); code != 0 {
		t.Fatalf("%d %s", code, stderr.String())
	}
}
