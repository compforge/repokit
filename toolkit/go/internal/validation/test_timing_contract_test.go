package validation_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	repokit "github.com/compforge/repokit/toolkit/go"
)

// +case=Timing observations survive history reuse; passing JSON events cannot override a failed Make command.
func TestValidationTestTimingHistoryAndVerdict(t *testing.T) {
	root := validationRepo(t, "test:\n\t@cat events.fixture\n")
	events := "{\"Action\":\"start\",\"Package\":\"example/p\"}\n" +
		"{\"Time\":\"2026-11-11T00:00:00Z\",\"Action\":\"pass\",\"Package\":\"example/p\",\"Test\":\"TestSlow\",\"Elapsed\":1.25}\n" +
		"{\"Time\":\"2026-11-11T00:00:00Z\",\"Action\":\"pass\",\"Package\":\"example/p\",\"Elapsed\":1.5}\n"
	writeValidationFile(t, root, "events.fixture", events)
	req := repokit.ValidationRequest{Repository: root, Checks: []string{"test"}, HistoryDir: t.TempDir()}
	first, err := repokit.Validate(context.Background(), req)
	if err != nil || !first.Passed || !first.InputsConsistent {
		t.Fatal(first, err)
	}
	original := first.Components[0].Checks[0]
	if len(original.TestTimings) != 2 || original.TestTimings[0].DurationSeconds != 1.25 {
		t.Fatal(original)
	}
	second, err := repokit.Validate(context.Background(), req)
	if err != nil || !second.Passed {
		t.Fatal(second, err)
	}
	cached := second.Components[0].Checks[0]
	if !cached.Reused || !reflect.DeepEqual(cached.TestTimings, original.TestTimings) {
		t.Fatal(cached)
	}
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("test:\n\t@cat events.fixture\n\t@false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	failed, err := repokit.Validate(context.Background(), req)
	if err != nil || failed.Passed || failed.Components[0].Checks[0].Status != "failed" || len(failed.Components[0].Checks[0].TestTimings) != 2 {
		t.Fatal(failed, err)
	}
}
