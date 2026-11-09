package gitenv

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestSharedEnvironment(t *testing.T) {
	data, err := os.ReadFile("../../../../conformance/git/environment.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct{ Removed, Preserved []string }
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	var input []string
	for _, k := range append(fixture.Removed, fixture.Preserved...) {
		input = append(input, k+"=value")
	}
	input = append(input, "GIT_CONFIG_KEY_0=core.worktree", "GIT_CONFIG_VALUE_0=wrong")
	got := ForCheckout(input)
	if len(got) != len(fixture.Preserved) {
		t.Fatalf("%v", got)
	}
	for _, k := range fixture.Preserved {
		if !strings.Contains(strings.Join(got, "\n"), k+"=value") {
			t.Fatal(k)
		}
	}
}
