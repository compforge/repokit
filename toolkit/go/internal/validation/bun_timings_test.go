package validation

import (
	"reflect"
	"strings"
	"testing"
)

// +case=Bun splits its banner and result streams; missing elapsed values, diagnostics and suite totals do not invent test timings.
func TestBunTimingObservations(t *testing.T) {
	const banner = "bun test v1.3.11 (af24e281)\n"
	const results = `tests/example.test.ts:
(pass) suite > fast
(pass) suite > slow [13.08ms]
(skip) suite > skip
(todo) suite > todo
error: expect(received).toBe(expected)
(fail) suite > bad [0.10ms]
(pass) no elapsed
(pass) negative [-1.0ms]
(pass) unknown unit [1min]
(pass) missing name [NaNms]
Ran 5 tests across 1 file. [31.00ms]
(pass) outside suite [9ms]
`
	want := []TestTiming{
		{Package: "tests/example.test.ts", Test: "suite > slow", Status: "passed", DurationSeconds: 0.01308},
		{Package: "tests/example.test.ts", Test: "suite > bad", Status: "failed", DurationSeconds: 0.0001},
	}
	for _, streams := range [][2]string{{banner, results}, {banner + results, ""}, {"", banner + results}} {
		got := testTimings(streams[0], streams[1])
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v; want %+v", got, want)
		}
	}
	if got := testTimings("", results); len(got) != 0 {
		t.Fatalf("accepted non-Bun output: %+v", got)
	}
	// No terminal summary is needed after cancellation; retain completed rows only.
	interrupted := "\x1b[1mtests/with space.spec.ts:\x1b[0m\r\n\x1b[32m✓ suite > done [1.25s]\x1b[0m\r\n(fail) truncated ["
	got := testTimings(banner, interrupted)
	if len(got) != 1 || got[0].Package != "tests/with space.spec.ts" || got[0].DurationSeconds != 1.25 || got[0].Cached {
		t.Fatal(got)
	}
	if got := testTimings(banner+"tests/a.test.ts:\n", "(pass) no file in stream [2ms]\n"); len(got) != 0 {
		t.Fatalf("file leaked across streams: %+v", got)
	}
	repeated := results + "tests/other.test.ts:\n(pass) other [2ms]\n" + banner + "(pass) no heading [3ms]\n"
	if got := testTimings(banner, repeated); len(got) != 3 {
		t.Fatal(got)
	}
	overflow := "tests/a.test.ts:\n(pass) overflow [" + strings.Repeat("9", 400) + "s]\n"
	if got := testTimings(banner, overflow); len(got) != 0 {
		t.Fatal(got)
	}
}
