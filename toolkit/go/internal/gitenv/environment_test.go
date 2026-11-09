package gitenv

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestSharedEnvironment(t *testing.T) {
	data, err := os.ReadFile("../../../../conformance/git/environment.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Removed, Preserved []string
		Configurations     []struct{ Input, Output map[string]string }
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, row := range fixture.Configurations {
		env := []string{}
		for k, v := range row.Input {
			env = append(env, k+"="+v)
		}
		result := map[string]string{}
		for _, entry := range ForCheckout(env) {
			k, v, _ := strings.Cut(entry, "=")
			result[k] = v
		}
		if !reflect.DeepEqual(result, row.Output) {
			t.Fatalf("got %v want %v", result, row.Output)
		}
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
