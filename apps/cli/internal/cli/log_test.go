package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func readLogs(t *testing.T, home string) string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(home, ".repokit", "logs", "*.log"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("log files: %v, %v", paths, err)
	}
	var all strings.Builder
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(data)
	}
	return all.String()
}

func TestDiffLogsDoNotChangeReport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := fixture(t)
	put(t, dir, "source file.ts", "export function a() { return 7; }\nexport function b() { return 2; }\n")
	args := []string{"impact", "--repo", dir, "--test-dir", "tests", "--json"}
	report := runJSON(t, args, "")
	if report.Scope != "focused" || len(report.SourceFiles) != 1 || len(report.TestFiles) != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	logs := readLogs(t, home)
	for _, want := range []string{"[repocli] time=", "msg=command.started", "version=" + Version, "args=", "msg=command.finished", "command=\"repocli impact\"", "exit_code=0"} {
		if !strings.Contains(logs, want) {
			t.Errorf("missing %q in %s", want, logs)
		}
	}
	ids := regexp.MustCompile(`run_id=([^ ]+)`).FindAllStringSubmatch(logs, -1)
	if len(ids) != 2 || ids[0][1] != ids[1][1] {
		t.Fatalf("run IDs: %v", ids)
	}
	if strings.Contains(logs, "export function") {
		t.Fatal("source contents in logs")
	}
	for path, perm := range map[string]os.FileMode{
		filepath.Join(home, ".repokit", "logs"):                                         0700,
		filepath.Join(home, ".repokit", "logs", time.Now().Format("2006-01-02")+".log"): 0600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != perm {
			t.Errorf("%s mode %v", path, info.Mode())
		}
	}
}

func TestDiffLogsOmitDiagnostics(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := fixture(t)
	put(t, dir, "Makefile", "all:\n\t@true\n")
	result := runJSON(t, []string{"impact", "--repo", dir, "--test-dir", "tests", "--json"}, "")
	if result.Complete || len(result.Diagnostics) == 0 {
		t.Fatalf("expected diagnostic: %+v", result)
	}
	logs := readLogs(t, home)
	if strings.Contains(logs, "msg=command.stdout") {
		t.Fatal(logs)
	}
	for _, d := range result.Diagnostics {
		if strings.Contains(logs, d.Code) {
			t.Fatalf("diagnostic retained %s: %s", d.Code, logs)
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, fmt.Errorf("private-output-error") }

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestDiffLogsFailures(t *testing.T) {
	for _, kind := range []string{"analysis", "output", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			repo := t.TempDir()
			if kind != "analysis" {
				repo = fixture(t)
			}
			var out, stderr bytes.Buffer
			var output io.Writer = &out
			if kind == "output" {
				output = failingWriter{}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "canceled" {
				cancel()
			}
			code := Execute(ctx, []string{"impact", "--repo", repo, "--json"}, strings.NewReader(""), output, &stderr)
			if code != 1 || (kind == "output" && stderr.Len() == 0) || (kind != "output" && string(resultData(t, out.Bytes())) != "null") {
				t.Fatalf("code %d: %s", code, stderr.String())
			}
			logs := readLogs(t, home)
			for _, want := range []string{"msg=command.failed", "level=ERROR", "exit_code=1", "elapsed_ms="} {
				if !strings.Contains(logs, want) {
					t.Errorf("missing %s: %s", want, logs)
				}
			}
			if strings.Contains(logs, "msg=command.finished") || strings.Contains(logs, "error=") || strings.Contains(logs, "private-output-error") {
				t.Fatal(logs)
			}

		})
	}
}

func TestInvalidCommandOutcomesAreLogged(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"missing-command"}, 2}, {[]string{"--invalid"}, 2},
		{[]string{"impact", "--no-log"}, 2},
		{[]string{"impact", "--timeout", "0s"}, 2},
		{[]string{"impact", "--timeout", "invalid"}, 2},
		{[]string{"impact", "--head", "HEAD", "--staged"}, 2},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			var stdout, stderr bytes.Buffer
			code := Execute(context.Background(), tc.args, strings.NewReader(""), &stdout, &stderr)
			if code != tc.code {
				t.Fatalf("code %d: %s", code, stderr.String())
			}
			logs := readLogs(t, home)
			for _, want := range []string{"msg=command.started", fmt.Sprintf("exit_code=%d", code)} {
				if !strings.Contains(logs, want) {
					t.Errorf("missing %q: %s", want, logs)
				}
			}
			if !strings.Contains(logs, "msg=command.failed") || stderr.Len() == 0 {
				t.Fatal(logs)
			}
			assertNoLogStreams(t, logs)
		})
	}
}

// A future command needs no log setup, hooks, or awareness of the log type.
func TestNewCommandInheritsLogging(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var stdout, stderr bytes.Buffer
	root := newRootCommand(&options{})
	root.AddCommand(&cobra.Command{Use: "future", RunE: func(command *cobra.Command, _ []string) error {
		fmt.Fprintln(command.OutOrStdout(), "future output")
		fmt.Fprintln(command.ErrOrStderr(), "future warning")
		return executionFailure(fmt.Errorf("future failure"))
	}})
	code := execute(context.Background(), root, []string{"future"}, strings.NewReader(""), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
	logs := readLogs(t, home)
	if !strings.Contains(logs, `command="repocli future"`) || !strings.Contains(logs, "exit_code=1") {
		t.Fatal(logs)
	}
	assertNoLogStreams(t, logs)
}

func TestLogFailurePreservesSuccessfulDiff(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	put(t, home, ".repokit", "block log directory")
	var out, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"impact", "--repo", fixture(t), "--json"}, strings.NewReader(""), &out, &stderr)
	if code != 0 || !json.Valid(out.Bytes()) || strings.Count(stderr.String(), "repocli: log warning:") != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, out.String(), stderr.String())
	}
}

func TestLogWriteFailuresWarnOnce(t *testing.T) {
	for _, output := range []io.Writer{failingWriter{}, shortWriter{}} {
		var stderr bytes.Buffer
		writer := &logWriter{output: output, stderr: &stderr}
		for range 2 {
			if _, err := writer.Write([]byte("record\n")); err == nil {
				t.Fatal("missing write error")
			}
		}
		if strings.Count(stderr.String(), "repocli: log warning:") != 1 {
			t.Fatal(stderr.String())
		}
	}
}

func TestLogRetention(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.FixedZone("local", 8*60*60))
	expired := now.AddDate(0, 0, -30).Format("2006-01-02") + ".log"
	kept := []string{now.Format("2006-01-02") + ".log", now.AddDate(0, 0, -29).Format("2006-01-02") + ".log", "2099-01-01.log", "notes.log", "2020-01-01.log.bak", "2020-99-99.log"}
	for _, name := range append(kept, expired) {
		put(t, dir, name, "record")
	}
	if err := os.Mkdir(filepath.Join(dir, "2020-01-01.log"), 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "2020-01-02.log")); err != nil {
		t.Fatal(err)
	}
	if err := pruneLogs(dir, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, expired)); !os.IsNotExist(err) {
		t.Fatalf("expired file: %v", err)
	}
	for _, name := range append(kept, "2020-01-01.log", "2020-01-02.log") {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			t.Errorf("removed %s: %v", name, err)
		}
	}
}

func TestConcurrentDiffLogs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := fixture(t)
	const runs = 6
	var wg sync.WaitGroup
	for range runs {
		wg.Go(func() {
			var out, stderr bytes.Buffer
			code := Execute(context.Background(), []string{"impact", "--repo", dir, "--json"}, strings.NewReader(""), &out, &stderr)
			if code != 0 || !json.Valid(out.Bytes()) || stderr.Len() != 0 {
				t.Errorf("code=%d stderr=%s", code, stderr.String())
			}
		})
	}
	wg.Wait()
	logs := readLogs(t, home)
	ids := map[string]int{}
	pattern := regexp.MustCompile(`run_id=([^ ]+)`)
	for line := range strings.SplitSeq(strings.TrimSpace(logs), "\n") {
		matches := pattern.FindAllStringSubmatch(line, -1)
		if !strings.HasPrefix(line, "[repocli] time=") || len(matches) != 1 {
			t.Fatalf("torn record: %s", line)
		}
		ids[matches[0][1]]++
	}
	if len(ids) != runs {
		t.Fatalf("run IDs: %v", ids)
	}
	for id, count := range ids {
		if count != 2 {
			t.Errorf("run %s has %d records", id, count)
		}
	}
}

func TestLogPreservesOutputStreams(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			dir := fixture(t)
			var stdout, stderr bytes.Buffer
			var output io.Writer = &stdout
			if fail {
				output = failingWriter{}
			}
			code := Execute(context.Background(), []string{"impact", "--repo", dir, "--json"}, strings.NewReader(""), output, &stderr)
			if (code != 0) != fail {
				t.Fatalf("code=%d: %s", code, stderr.String())
			}
			if fail {
				if !strings.Contains(stderr.String(), "private-output-error") {
					t.Fatal(stderr.String())
				}
			} else if !json.Valid(stdout.Bytes()) || stderr.Len() != 0 {
				t.Fatalf("stdout=%s stderr=%s", stdout.String(), stderr.String())
			}
			logs := readLogs(t, home)
			assertNoLogStreams(t, logs)
		})
	}
}

func assertNoLogStreams(t *testing.T, logs string) {
	t.Helper()
	for _, detail := range []string{"msg=command.stdout", "msg=command.stderr", " data=", " error=", "private-output-error", "future output", "future warning", "future failure"} {
		if strings.Contains(logs, detail) {
			t.Fatalf("output retained in log: %s", detail)
		}
	}
}

func TestLogArgumentsCanBeReplayed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	args := []string{"future", "a b", "[c d]", `quote"and\slash`, "line\nbreak", ""}
	root := newRootCommand(&options{})
	root.AddCommand(&cobra.Command{Use: "future", Run: func(*cobra.Command, []string) {}})
	var stdout, stderr bytes.Buffer
	if code := execute(context.Background(), root, args, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	logs := readLogs(t, home)
	match := regexp.MustCompile(`args=("(?:\\.|[^"\\])*")`).FindStringSubmatch(logs)
	if len(match) != 2 {
		t.Fatal(logs)
	}
	encoded, err := strconv.Unquote(match[1])
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal([]byte(encoded), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, args) {
		t.Fatalf("argv=%q want=%q", got, args)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	var fields bytes.Buffer
	slog.New(slog.NewTextHandler(&fields, nil)).Info("cwd", "cwd", cwd)
	_, expected, _ := strings.Cut(strings.TrimSpace(fields.String()), " cwd=")
	if !strings.Contains(logs, "cwd="+expected) {
		t.Fatal("missing invocation cwd")
	}
}

func TestLogSizeIsIndependentOfOutput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	payload := strings.Repeat("private report payload\n", 50000)
	root := newRootCommand(&options{})
	root.AddCommand(&cobra.Command{Use: "future", RunE: func(command *cobra.Command, _ []string) error {
		fmt.Fprint(command.OutOrStdout(), payload)
		fmt.Fprint(command.ErrOrStderr(), payload)
		return executionFailure(fmt.Errorf("future failure"))
	}})
	var stdout, stderr bytes.Buffer
	if code := execute(context.Background(), root, []string{"future"}, nil, &stdout, &stderr); code != 1 {
		t.Fatalf("exit=%d", code)
	}
	if stdout.String() != payload || stderr.String() != payload+"repocli: future failure\n" {
		t.Fatal("command output changed")
	}
	logs := readLogs(t, home)
	if len(logs) > 4096 || strings.Contains(logs, "private report payload") {
		t.Fatalf("log grew with output: %d bytes", len(logs))
	}
	assertNoLogStreams(t, logs)
}

func TestInformationCommandsDoNotInitializeLogs(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		for _, args := range [][]string{nil, {"--help"}, {"-h"}, {"help", "impact"}, {"impact", "--help"}, {"snapshot", "--help"}, {"version"}, {"version", "--json"}, {"--version"}, {"--repo", "--version", "--help"}} {
			t.Run(fmt.Sprintf("blocked=%v/%v", blocked, args), func(t *testing.T) {
				home := t.TempDir()
				t.Setenv("HOME", home)
				if blocked {
					put(t, home, ".repokit", "block log directory")
				}
				var stdout, stderr bytes.Buffer
				if code := Execute(context.Background(), args, nil, &stdout, &stderr); code != 0 || stderr.Len() != 0 || stdout.Len() == 0 {
					t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
				}
				if !blocked {
					if _, err := os.Stat(filepath.Join(home, ".repokit")); !os.IsNotExist(err) {
						t.Fatalf("informational command created log state: %v", err)
					}
				}
			})
		}
	}
}
