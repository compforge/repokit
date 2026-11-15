package code

import (
	"errors"
	"github.com/compforge/repokit/toolkit/go"
	"testing"
)

type deadcodeBrokenWriter struct{}

func (deadcodeBrokenWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestDeadcodeWriterErrors(t *testing.T) {
	if err := writeDead(deadcodeBrokenWriter{}, repokit.DeadcodeReport{}, false); err == nil {
		t.Fatal("lost write error")
	}
}
