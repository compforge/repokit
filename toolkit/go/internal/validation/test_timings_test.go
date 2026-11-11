package validation

import (
	"strings"
	"testing"
)

// +case=Interleaved packages and nested cases retain independent elapsed values; Go cache replay is marked after the package summary arrives.
func TestGoTestTimingObservations(t *testing.T) {
	const at = `"Time":"2026-11-11T00:00:00Z",`
	output := "make test\n" +
		`{"Action":"start","Package":"a"}` + "\n" +
		`{"Action":"start","Package":"b"}` + "\n" +
		`{` + at + `"Action":"pass","Package":"a","Test":"TestParent/child","Elapsed":0.125}` + "\n" +
		`{` + at + `"Action":"pass","Package":"b","Test":"TestB","Elapsed":8}` + "\n" +
		`{` + at + `"Action":"pass","Package":"a","Test":"TestParent","Elapsed":1}` + "\n" +
		`{"Action":"output","Package":"b","Output":"ok  \tb\t(cached)\n"}` + "\n" +
		`{` + at + `"Action":"pass","Package":"b","Elapsed":0.01}` + "\n" +
		`{` + at + `"Action":"pass","Package":"a","Elapsed":2}` + "\n" +
		`{"Action":"pass","Package":"unstarted","Test":"Fake","Elapsed":99}` + "\n" +
		`{"Action":"start","Package":"c"}` + "\n" +
		`{"Action":"pass","Package":"c","Test":"Historical","Elapsed":3}` + "\n" +
		`{` + at + `"Action":"fail","Package":"c","Test":"Invalid","Elapsed":-1}` + "\n" +
		`{` + at + `"Action":"fail","Package":"c","Test":"NaN","Elapsed":1e999}` + "\n" +
		`{` + at + `"Action":"skip","Package":"c","Test":"Skipped","Elapsed":0}` + "\n" +
		`{"Action":"pass","Package":"c","Test":"MissingElapsed"}` + "\n" +
		`{"Action":"pass"`
	rows := goTestTimings(output)
	if len(rows) != 7 {
		t.Fatalf("%+v", rows)
	}
	if rows[0].Test != "TestParent/child" || rows[0].DurationSeconds != 0.125 || rows[0].Cached {
		t.Fatal(rows[0])
	}
	if !rows[1].Cached || !rows[3].Cached || rows[1].DurationSeconds != 8 {
		t.Fatal(rows)
	}
	if rows[2].Test != "TestParent" || rows[2].DurationSeconds != 1 || rows[4].DurationSeconds != 2 {
		t.Fatal(rows)
	}
	if !rows[5].Cached || rows[6].Status != "skipped" {
		t.Fatal(rows)
	}
	// Repeating a package in another invocation does not inherit a previous cache hit.
	repeated := output + "\n" + `{"Action":"start","Package":"b"}` + "\n" + `{` + at + `"Action":"pass","Package":"b","Elapsed":5}`
	if last := goTestTimings(repeated); last[len(last)-1].Cached {
		t.Fatal(last)
	}
	if len(goTestTimings(strings.Repeat("plain output\n", 10))) != 0 {
		t.Fatal("invented timings")
	}
}
