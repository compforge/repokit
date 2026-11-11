package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/compforge/repokit/toolkit/go/state"
)

func TestCleanEnvelopeDryRunAndPartialFailure(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "repositories", "idle")
	os.MkdirAll(root, 0700)
	data, _ := json.Marshal(map[string]any{"version": 1, "lastAccessAt": time.Now().Add(-40 * 24 * time.Hour).Unix(), "checkouts": map[string]any{}})
	os.WriteFile(filepath.Join(root, "activity.json"), data, 0600)
	broken := filepath.Join(home, "repositories", "broken")
	os.MkdirAll(broken, 0700)
	os.WriteFile(filepath.Join(broken, "activity.json"), []byte("broken"), 0600)
	for _, dry := range []bool{true, false} {
		args := []string{"clean", "--home", home, "--json"}
		if dry {
			args = append(args, "--dry-run")
		}
		var out, stderr bytes.Buffer
		code := Execute(context.Background(), args, strings.NewReader(""), &out, &stderr)
		var result commandResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err, out.String())
		}
		if code != 1 || result.Outcome != "partial" || result.Command != "repocli clean" || len(result.Diagnostics) != 1 || result.Error != nil {
			t.Fatal(code, out.String(), stderr.String())
		}
		if dry {
			got, _ := os.ReadFile(filepath.Join(root, "activity.json"))
			if !bytes.Equal(got, data) {
				t.Fatal("dry run refreshed activity")
			}
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
func TestCleanArgumentErrorsAndEmptyHome(t *testing.T) {
	home := t.TempDir()
	for _, days := range []string{"0", "-1", "36501"} {
		var out, stderr bytes.Buffer
		code := Execute(context.Background(), []string{"clean", "--home", home, "--inactive-days", days, "--json"}, nil, &out, &stderr)
		var result commandResult
		json.Unmarshal(out.Bytes(), &result)
		if code != 2 || result.Error == nil || result.Error.Code != "usage.invalid_argument" {
			t.Fatal(code, out.String())
		}
	}
	var out, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"clean", "--home", home, "--json"}, nil, &out, &stderr); code != 0 {
		t.Fatal(out.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, "repositories")); !os.IsNotExist(err) {
		t.Fatal("clean created repository records")
	}
}
func TestRepositoryCommandRecordsSelectedCheckoutAccess(t *testing.T) {
	repo := fixture(t)
	home := t.TempDir()
	var out, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"inspect", "--repo", repo, "--home", home, "--json"}, nil, &out, &stderr); code != 0 {
		t.Fatal(code, out.String(), stderr.String())
	}
	paths, err := filepath.Glob(filepath.Join(home, "repositories", "*", "activity.json"))
	if err != nil || len(paths) != 1 {
		t.Fatal(paths, err)
	}
	var value struct {
		Checkouts map[string]any `json:"checkouts"`
	}
	data, _ := os.ReadFile(paths[0])
	if err = json.Unmarshal(data, &value); err != nil || len(value.Checkouts) != 1 {
		t.Fatal(string(data), err)
	}
	out.Reset()
	stderr.Reset()
	if code := Execute(context.Background(), []string{"validate", "--history", "--repo", repo, "--home", home}, nil, &out, &stderr); code != 0 {
		t.Fatal(code, out.String(), stderr.String())
	}
	after, _ := os.ReadFile(paths[0])
	if !bytes.Equal(data, after) {
		t.Fatal("history query renewed access")
	}

}

// +case=A PR connection error appears on stderr, rather than only the final generic cleanup failure.
func TestCleanTextReportsPRConnectionError(t *testing.T) {
	repo := fixture(t)
	home := t.TempDir()
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	gitCommand(t, repo, "remote", "add", "origin", "https://github.com/example/cleanup-fixture.git")
	if _, err := state.Observe(context.Background(), repo, home, false); err != nil {
		t.Fatal(err)
	}
	for _, structured := range []bool{false, true} {
		args := []string{"clean", "--repo", repo, "--home", home, "--dry-run"}
		if structured {
			args = append(args, "--json")
		}
		var out, stderr bytes.Buffer
		code := Execute(context.Background(), args, nil, &out, &stderr)
		if code != 1 {
			t.Fatal(code, out.String(), stderr.String())
		}
		if structured {
			var result commandResult
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Outcome != "partial" || len(result.Diagnostics) != 1 || !strings.Contains(result.Diagnostics[0].Message, "no github credential") {
				t.Fatal(result)
			}
			if strings.Contains(stderr.String(), "error:") {
				t.Fatal("text errors leaked into JSON mode", stderr.String())
			}
		} else if !strings.Contains(stderr.String(), "PR status unavailable") || !strings.Contains(stderr.String(), "no github credential") || !strings.Contains(stderr.String(), repo) || !strings.Contains(stderr.String(), "cleanup incomplete") {
			t.Fatal("missing cleanup cause", stderr.String())
		}
	}
}

// +case=Retaining recently written or locked state is a successful skip, not incomplete cleanup.
func TestCleanTextStateProtectionIsNotFailure(t *testing.T) {
	for _, reason := range []string{"recent_state_write", "state_locked"} {
		t.Run(reason, func(t *testing.T) {
			home := t.TempDir()
			directory := filepath.Join(home, "repositories", "idle")
			if err := os.MkdirAll(directory, 0700); err != nil {
				t.Fatal(err)
			}
			if reason == "state_locked" {
				if err := os.Mkdir(filepath.Join(directory, "topology.lock"), 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(directory, "observation.json"), []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			code := Execute(context.Background(), []string{"clean", "--home", home, "--dry-run"}, nil, &out, &stderr)
			if code != 0 || !strings.Contains(out.String(), reason) || strings.Contains(stderr.String(), "cleanup incomplete") {
				t.Fatal(code, out.String(), stderr.String())
			}
			if _, err := os.Stat(directory); err != nil {
				t.Fatal(err)
			}
		})
	}
}
