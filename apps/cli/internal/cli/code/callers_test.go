package code

import (
	"errors"
	"io"
	"testing"

	cg "github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go"
)

func TestCallersPosition(t *testing.T) {
	for _, tc := range []struct{ at, path string }{
		{"dir/file.go:12:8", "dir/file.go"},
		{"./dir/a:b.go:12:8", "dir/a:b.go"},
	} {
		req, err := callersPosition(tc.at)
		if err != nil || req.Path != tc.path || req.Line != 12 || req.Column != 8 {
			t.Errorf("%q: got %+v, %v", tc.at, req, err)
		}
	}
	for _, at := range []string{"", ":1:2", "a.go:1", "a.go:x:2", "a.go:1:x", "a.go:0:1", "a.go:1:-1", "../a.go:1:2", "/a.go:1:2"} {
		if _, err := callersPosition(at); err == nil {
			t.Errorf("accepted %q", at)
		}
	}
}

func TestWriteCallersPropagatesWriteError(t *testing.T) {
	report := repokit.CallersReport{Target: cg.Node{NameLocation: &cg.Location{Path: "a.go", Line: 1, Column: 6}}}
	r, w := io.Pipe()
	r.Close()
	defer w.Close()
	if err := writeCallers(w, report); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("got %v, want %v", err, io.ErrClosedPipe)
	}
}
