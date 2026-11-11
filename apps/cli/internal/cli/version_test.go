package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestVersionCommandMatchesFlagWithoutRepository(t *testing.T) {
	want := "repocli version " + Version + "\n"
	for _, args := range [][]string{{"--version"}, {"version"}} {
		// Version belongs to the executable, independent of a checkout's availability.
		args = append([]string{"--repo", t.TempDir()}, args...)
		var out, stderr bytes.Buffer
		if code := Execute(context.Background(), args, nil, &out, &stderr); code != 0 || out.String() != want || stderr.Len() != 0 {
			t.Fatalf("args=%v code=%d out=%q err=%s", args, code, out.String(), stderr.String())
		}
	}
	var out, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"version", "--json"}, nil, &out, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	var data map[string]string
	if err := json.Unmarshal(resultData(t, out.Bytes()), &data); err != nil || data["version"] != Version {
		t.Fatalf("%s: %v", out.String(), err)
	}
}

func TestVersionUsageAndOutputErrors(t *testing.T) {
	var out, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"version", "extra"}, nil, &out, &stderr); code != 2 || out.Len() != 0 {
		t.Fatalf("code=%d out=%q", code, out.String())
	}
	for _, args := range [][]string{{"version"}, {"version", "--json"}} {
		stderr.Reset()
		if code := Execute(context.Background(), args, nil, failedWriter{}, &stderr); code != 1 || !strings.Contains(stderr.String(), "output unavailable") {
			t.Fatalf("code=%d err=%s", code, stderr.String())
		}
	}
}
