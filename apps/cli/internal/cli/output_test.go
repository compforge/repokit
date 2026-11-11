package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/compforge/repokit/toolkit/go"
)

func TestTextReportExplainsDependencyGap(t *testing.T) {
	report := repokit.ImpactReport{Diagnostics: []repokit.Diagnostic{{
		Code: "impact_uncertain", Path: "src/loader.ts", Line: 42,
		Reason: "unresolved_import", Relation: "imports", Version: "after",
		Message: "Cannot resolve ./generated; an unselected candidate may depend on it",
	}}}
	var out bytes.Buffer
	if err := writeReport(&out, report, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"src/loader.ts:42", "reason=unresolved_import", "relation=imports", "version=after", "./generated", "additional affected files"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
}

func TestTextReportBoundsLocalGapDetails(t *testing.T) {
	report := repokit.ImpactReport{}
	for i := 0; i < 1000; i++ {
		report.Observations = append(report.Observations, repokit.Uncertainty{Reason: "unresolved_call", Subject: "relations", Disposition: "local_gap", Path: "src/app.ts", Line: i + 1, Version: "after", Message: "unknown"})
	}
	var text, raw bytes.Buffer
	if err := writeReport(&text, report, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "1000 occurrences") || !strings.Contains(text.String(), "997 more") || strings.Count(text.String(), "src/app.ts") != 3 {
		t.Fatalf("unbounded summary: %s", text.String())
	}
	output := &resultOutput{Writer: &raw, started: time.Now()}
	if err := writeReport(output, report, true); err != nil {
		t.Fatal(err)
	}
	if _, err := output.finish("repocli impact", nil, 0, &text); err != nil {
		t.Fatal(err)
	}
	if strings.Count(raw.String(), `"path": "src/app.ts"`) != 1000 {
		t.Fatal("JSON lost raw gaps")
	}
}
