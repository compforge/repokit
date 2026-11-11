package state

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	directory, err := os.MkdirTemp("", "repokit-tests-")
	if err != nil {
		panic(err)
	}
	os.Setenv("REPOKIT_HOME", directory)
	code := m.Run()
	os.RemoveAll(directory)
	os.Exit(code)
}
