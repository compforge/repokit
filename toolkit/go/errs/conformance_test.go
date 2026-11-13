package errs_test

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/compforge/repokit/toolkit/go/errs"
)

// +case=Go, TypeScript and Python use identical public error JSON and text fixtures.
func TestSharedRepresentations(t *testing.T) {
	data, err := os.ReadFile("../../../conformance/errors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Wire json.RawMessage
		Text string
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, row := range cases {
		var e errs.Error
		if err := json.Unmarshal(row.Wire, &e); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(&e)
		if err != nil {
			t.Fatal(err)
		}
		var got, want map[string]any
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(row.Wire, &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) || e.Text() != row.Text {
			t.Fatalf("error facts differ: %s / %s", data, e.Text())
		}
	}
}
