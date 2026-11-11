package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/compforge/repokit/toolkit/go"
	"github.com/compforge/repokit/toolkit/go/forge"
	"github.com/compforge/repokit/toolkit/go/state"
	"github.com/spf13/cobra"
)

func resultData(t *testing.T, b []byte) json.RawMessage {
	t.Helper()
	var r struct {
		SchemaVersion string          `json:"schemaVersion"`
		Command       string          `json:"command"`
		Outcome       string          `json:"outcome"`
		Summary       string          `json:"summary"`
		Diagnostics   []diagnostic    `json:"diagnostics"`
		Data          json.RawMessage `json:"data"`
		Artifacts     []artifact      `json:"artifacts"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("invalid result JSON: %v: %s", err, b)
	}
	if r.SchemaVersion != "1.0" || r.Command == "" || r.Summary == "" || r.Diagnostics == nil || r.Artifacts == nil {
		t.Fatalf("incomplete result envelope: %s", b)
	}
	switch r.Outcome {
	case "succeeded", "failed", "partial", "unknown":
	default:
		t.Fatalf("unknown outcome: %s", b)
	}
	return r.Data
}

// +case=Malformed arguments still produce one structured failure even when --json follows the offending argument
func TestJSONUsageFailures(t *testing.T) {
	for _, args := range [][]string{{"unknown", "--json"}, {"impact", "--unknown", "--json"}, {"impact", "--timeout", "bad", "--json"}, {"impact", "--timeout", "0s", "--json"}, {"view", "--json"}, {"validate", "--plan", "--unknown"}, {"validate", "--history", "--timeout", "0s"}, {"validate", "--unknown", "--plan"}} {
		var out, stderr bytes.Buffer
		if code := Execute(context.Background(), args, nil, &out, &stderr); code != 2 || stderr.Len() != 0 {
			t.Fatalf("%v: code=%d stdout=%s stderr=%s", args, code, &out, &stderr)
		}
		resultData(t, out.Bytes())
		var r commandResult
		json.Unmarshal(out.Bytes(), &r)
		if r.Outcome != "failed" || r.Error == nil || r.Error.Code != "usage.invalid_argument" || r.Data != nil {
			t.Fatalf("%v: %+v", args, r)
		}
	}
}

func TestJSONFlagIntentRespectsValuesAndDelimiter(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"pr", "create", "--title", "--json"}, false},
		{[]string{"impact", "--", "--json"}, false},
		{[]string{"--json", "impact", "--json=false"}, false},
		{[]string{"impact", "--json=true"}, true},
	} {
		if got := requestedJSON(newRootCommand(&options{}), tc.args); got != tc.want {
			t.Fatalf("%v: %t", tc.args, got)
		}
	}
}

func runResultCommand(t *testing.T, data any, err error, stdout io.Writer) (int, string) {
	t.Helper()
	root := newRootCommand(&options{})
	root.AddCommand(&cobra.Command{Use: "fixture-result", RunE: func(cmd *cobra.Command, _ []string) error {
		if data != nil {
			if e := writeJSON(cmd.OutOrStdout(), data); e != nil {
				return executionFailure(e)
			}
		}
		if err != nil {
			return executionFailure(err)
		}
		return nil
	}})
	var stderr bytes.Buffer
	code := execute(context.Background(), root, []string{"fixture-result", "--json"}, nil, stdout, &stderr)
	return code, stderr.String()
}

// +case=Confirmed PR creation survives maintenance errors without a retryable creation failure
func TestPRMaintenanceWarningPreservesSuccess(t *testing.T) {
	v := forge.OperationResult{Created: true, Maintenance: state.WorktreeCleanReport{Errors: []string{"lookup failed"}}}
	var out bytes.Buffer
	code, stderr := runResultCommand(t, v, nil, &out)
	if code != 0 || stderr != "" {
		t.Fatal(code, stderr)
	}
	resultData(t, out.Bytes())
	var r commandResult
	json.Unmarshal(out.Bytes(), &r)
	if r.Outcome != "succeeded" || len(r.Diagnostics) != 1 || r.Diagnostics[0].Severity != "warning" {
		t.Fatalf("%+v", r)
	}
	var data forge.OperationResult
	json.Unmarshal(resultData(t, out.Bytes()), &data)
	if !data.Created {
		t.Fatal("lost confirmed write")
	}
}

// +case=Unknown Forge write outcomes require reconciliation and are never collapsed into ordinary failure
func TestUnknownWriteOutcome(t *testing.T) {
	err := operationFailure("forge.operation_failed", "create", "example/repo source=topic target=main", &forge.OutcomeUnknown{Err: context.DeadlineExceeded})
	var out bytes.Buffer
	code, stderr := runResultCommand(t, nil, err, &out)
	if code != 1 || stderr != "" {
		t.Fatal(code, stderr)
	}
	var r commandResult
	json.Unmarshal(out.Bytes(), &r)
	if r.Outcome != "unknown" || r.Error == nil || r.Error.Code != "forge.outcome_unknown" || r.Error.Subject == "" || r.Error.Hint == "" {
		t.Fatalf("%+v", r)
	}
}

func TestPartialWorktreeResultsRemainAvailable(t *testing.T) {
	v := state.WorktreeCleanReport{Actions: []state.WorktreeAction{{Status: "completed", Path: "done"}, {Status: "failed", Path: "blocked", Reason: "denied"}}, Errors: []string{"denied"}}
	var out bytes.Buffer
	code, stderr := runResultCommand(t, v, &resultFailure{"maintenance incomplete"}, &out)
	if code != 1 || stderr != "" {
		t.Fatal(code, stderr)
	}
	var r commandResult
	json.Unmarshal(out.Bytes(), &r)
	var data state.WorktreeCleanReport
	json.Unmarshal(resultData(t, out.Bytes()), &data)
	if r.Outcome != "partial" || len(data.Actions) != 2 || len(r.Diagnostics) != 2 {
		t.Fatalf("%+v %+v", r, data)
	}
}

func TestKnownEmptyPRList(t *testing.T) {
	var prs []forge.PullRequest
	var out bytes.Buffer
	code, stderr := runResultCommand(t, prs, nil, &out)
	if code != 0 || stderr != "" || string(resultData(t, out.Bytes())) != "[]" {
		t.Fatalf("%d %s %s", code, stderr, &out)
	}
}

func TestJSONDeliveryFailureDoesNotRetryOrAppendEnvelope(t *testing.T) {
	for _, w := range []io.Writer{failedWriter{}, shortWriter{}} {
		code, stderr := runResultCommand(t, map[string]bool{"created": true}, nil, w)
		if code != 1 || !strings.Contains(stderr, "output.write_failed") || !strings.Contains(stderr, "outcome=succeeded") {
			t.Fatal(code, stderr)
		}
	}
}

func TestValidationResultAndTextEvidence(t *testing.T) {
	v := repokit.ValidationReport{Complete: true, Passed: false, InputsConsistent: true, Components: []repokit.ComponentValidationResult{{Checks: []repokit.CheckResult{
		{Status: "passed", Stdout: "successful noise", Reused: true},
		{Status: "failed", Diagnostic: "test failed", Stderr: strings.Repeat("noise\n", 50) + "assertion failed"},
	}}}}
	var out bytes.Buffer
	code, stderr := runResultCommand(t, v, &resultFailure{"validation failed"}, &out)
	if code != 1 || stderr != "" {
		t.Fatal(code, stderr)
	}
	var r commandResult
	json.Unmarshal(out.Bytes(), &r)
	if r.Outcome != "partial" || len(r.Diagnostics) != 1 || r.Diagnostics[0].Evidence != "/data/components/0/checks/1" {
		t.Fatalf("%+v", r)
	}
	out.Reset()
	if err := writeValidationText(&out, v); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "Validation passed=false") || strings.Contains(out.String(), "successful noise") || !strings.Contains(out.String(), "assertion failed") || !strings.Contains(out.String(), "display truncated") {
		t.Fatal(out.String())
	}
}

func TestErrorClassificationUsesTypes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{executionFailure(context.Canceled), "execution.canceled"},
		{executionFailure(&forge.HTTPError{Status: 401}), "forge.authentication_required"},
		{executionFailure(errors.New("network timeout")), "execution.failed"},
	} {
		var typed *commandError
		if !errors.As(executionFailure(tc.err), &typed) || typed.Code != tc.code {
			t.Fatalf("%+v != %s", typed, tc.code)
		}
	}
}

// +case=Cache hits expose lookup cost without charging the previous run's execution again
func TestValidationCostSeparatesReuse(t *testing.T) {
	report := repokit.ValidationReport{Passed: true, Complete: true, InputsConsistent: true, Components: []repokit.ComponentValidationResult{{Checks: []repokit.CheckResult{
		{Status: "passed", DurationSeconds: 10, LookupSeconds: 0.2},
		{Status: "passed", Reused: true, DurationSeconds: 150, LookupSeconds: 0.3},
		{Status: "skipped"},
	}}}}
	var result commandResult
	describeValidation(&result, report)
	cost := result.Cost.Validation
	if cost.ExecutionSeconds != 10 || cost.LookupSeconds != 0.5 || len(cost.Steps) != 3 || cost.Steps[1].ExecutionSeconds != 0 || !cost.Steps[1].Reused {
		t.Fatalf("%+v", cost)
	}
	if report.Components[0].Checks[1].DurationSeconds != 150 {
		t.Fatal("changed historical evidence")
	}
}

func TestTypedErrorPreservesCauseAndContext(t *testing.T) {
	original := &forge.OutcomeUnknown{Err: context.DeadlineExceeded}
	err := operationFailure("forge.operation_failed", "create", "owner/repo", original)
	if executionFailure(err) != err || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lost cause or rewrapped typed error")
	}
	var unknown *forge.OutcomeUnknown
	if !errors.As(err, &unknown) || unknown != original {
		t.Fatal("lost SDK error")
	}
	var typed *commandError
	if !errors.As(err, &typed) || typed.Code != "forge.outcome_unknown" || typed.Subject != "owner/repo" {
		t.Fatal(err)
	}
}

// +case=Checks canceled or unavailable before process start do not count as executed
func TestValidationSummaryDoesNotClaimUnstartedChecksRan(t *testing.T) {
	var result commandResult
	describeValidation(&result, repokit.ValidationReport{Components: []repokit.ComponentValidationResult{{Checks: []repokit.CheckResult{
		{Status: "cancelled"}, {Status: "unavailable"},
	}}}})
	if !strings.Contains(result.Summary, "0 executed") || result.Outcome != "failed" {
		t.Fatalf("%+v", result)
	}
}
