package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestUnownedImpactAppearsInJSONAndText(t *testing.T) {
	dir := t.TempDir()
	gitCommand(t, dir, "init", "-q")
	gitCommand(t, dir, "config", "user.email", "fixture@example.invalid")
	gitCommand(t, dir, "config", "user.name", "Fixture")
	put(t, dir, "app/Makefile", "test:\n\ttrue\n")
	put(t, dir, "shared.py", "value = 1\n")
	put(t, dir, "tests/test_shared.py", "from shared import value\n")
	gitCommand(t, dir, "add", ".")
	gitCommand(t, dir, "commit", "-qm", "fixture")
	put(t, dir, "shared.py", "value = 2\n")
	args := []string{"impact", "--repo", dir}
	report := runJSON(t, append(args, "--json"), "")
	if len(report.Components) != 1 || report.Components[0].Root != "app" || report.Components[0].Affected ||
		!report.Unowned.Affected || len(report.Unowned.TestFiles) != 1 {
		t.Fatal(report)
	}
	wire, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(decoded["components"]), "[") || !strings.Contains(string(decoded["unowned"]), "tests/test_shared.py") {
		t.Fatal(string(wire))
	}
	var out, stderr bytes.Buffer
	if code := Execute(context.Background(), args, strings.NewReader(""), &out, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	if !strings.Contains(out.String(), "unowned, affected=true") || !strings.Contains(out.String(), "    reachable test tests/test_shared.py") {
		t.Fatal(out.String())
	}
}
