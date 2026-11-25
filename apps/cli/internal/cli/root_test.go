package cli

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestCobraHelpAndCompletion(t *testing.T) {
	for _, args := range [][]string{
		nil, {"--help"}, {"help", "impact"}, {"impact", "--help"},
		{"completion", "bash"}, {"completion", "zsh"}, {"completion", "fish"}, {"completion", "powershell"},
	} {
		var out, stderr bytes.Buffer
		code := Execute(context.Background(), args, strings.NewReader(""), &out, &stderr)
		if code != 0 || stderr.Len() != 0 || !strings.Contains(out.String(), "repocli") {
			t.Fatalf("%v: code=%d out=%s err=%s", args, code, out.String(), stderr.String())
		}
		if len(args) > 0 && args[0] == "help" && !strings.Contains(out.String(), "--test-dir") {
			t.Fatal("help did not select diff")
		}
	}
}

func TestRootHelpCommandGroups(t *testing.T) {
	var out, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"--help"}, strings.NewReader(""), &out, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("root help: code=%d err=%s", code, stderr.String())
	}
	help := out.String()
	if strings.Contains(help, "\nAdditional Commands:") {
		t.Fatalf("root help contains ungrouped commands:\n%s", help)
	}
	for _, group := range []struct {
		title    string
		commands []string
	}{
		{"Code Analysis", []string{"code", "impact", "view"}},
		{"Dependencies & Validation", []string{"depot", "format", "lint", "test", "validate"}},
		{"Git & Forge", []string{"diff", "git", "pr", "release"}},
		{"Repository & Workspace", []string{"clean", "inspect", "repo", "snapshot", "stats", "tree", "worktree"}},
		{"CLI Utilities", []string{"completion", "help", "upgrade", "version"}},
	} {
		_, body, found := strings.Cut(help, "\n"+group.title+":\n")
		if !found {
			t.Fatalf("root help is missing %q:\n%s", group.title, help)
		}
		section, _, _ := strings.Cut(body, "\n\n")
		var commands []string
		for _, line := range strings.Split(section, "\n") {
			name, _, _ := strings.Cut(strings.TrimSpace(line), " ")
			commands = append(commands, name)
		}
		if !reflect.DeepEqual(commands, group.commands) {
			t.Errorf("%s contains %v, want %v", group.title, commands, group.commands)
		}
	}
}

func TestCobraUsageErrorsStayOnStderr(t *testing.T) {
	for _, args := range [][]string{
		{"unknown"}, {"impact", "extra"}, {"impact", "--unknown"}, {"impact", "--base"},
		{"impact", "--timeout", "no"}, {"impact", "--timeout", "0s"},
		{"impact", "--json=invalid"}, {"impact", "--test-dir", "../outside"},
	} {
		var out, stderr bytes.Buffer
		code := Execute(context.Background(), args, strings.NewReader(""), &out, &stderr)
		if code != 2 || out.Len() != 0 || strings.Count(stderr.String(), "repocli:") != 1 {
			t.Fatalf("%v: code=%d out=%s err=%s", args, code, out.String(), stderr.String())
		}
		if strings.Contains(stderr.String(), "Usage:") {
			t.Fatalf("error output includes unsolicited help: %s", stderr.String())
		}
	}
}

func TestPersistentFlagsAndRepeatedTestDirectories(t *testing.T) {
	dir := fixture(t)
	put(t, dir, "more,tests/other.test.ts", "import { a } from '../source file';\n")
	gitCommand(t, dir, "add", ".")
	gitCommand(t, dir, "commit", "-qm", "another test directory")
	put(t, dir, "source file.ts", "export function a() { return 3; }\nexport function b() { return 2; }\n")
	before := runJSON(t, []string{"--repo", dir, "--json", "--timeout", "1m", "impact", "--test-dir", "tests", "--test-dir", "more,tests"}, "")
	after := runJSON(t, []string{"impact", "--repo", dir, "--json", "--timeout", "1m", "--test-dir", "tests", "--test-dir", "more,tests"}, "")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(before.TestFiles, []string{"more,tests/other.test.ts", "tests/a.test.ts"}) {
		t.Fatalf("global or repeated flags changed meaning: %+v / %+v", before, after)
	}
	// A later invocation must not inherit JSON mode, directories, or other flags.
	var out, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"impact", "--repo", dir}, strings.NewReader(""), &out, &stderr); code != 0 || stderr.Len() != 0 || !strings.Contains(out.String(), "impact scope: focused") {
		t.Fatalf("command state leaked: code=%d out=%s err=%s", code, out.String(), stderr.String())
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func TestReportWriteFailuresAreExecutionErrors(t *testing.T) {
	dir := fixture(t)
	for _, asJSON := range []bool{false, true} {
		args := []string{"impact", "--repo", dir}
		if asJSON {
			args = append(args, "--json")
		}
		var stderr bytes.Buffer
		if code := Execute(context.Background(), args, strings.NewReader(""), failedWriter{}, &stderr); code != 1 || !strings.Contains(stderr.String(), "output unavailable") {
			t.Fatalf("code=%d err=%s", code, stderr.String())
		}
	}
}
