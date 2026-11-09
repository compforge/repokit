package cli

import (
	"context"
	"fmt"
	"github.com/compforge/go-stdx/timeline"
	"os"
	"testing"
)

// Default logging must never write into the developer's real home during tests.
func TestMain(m *testing.M) {
	os.Unsetenv("REPOKIT_HOME")
	home, err := os.MkdirTemp("", "repocli-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Setenv("HOME", home); err != nil {
		os.RemoveAll(home)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	manager, err := timeline.NewManager(nil, timeline.Config{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	timeline.SetDefault(manager)
	code := m.Run()
	if err := manager.Shutdown(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.RemoveAll(home)
	os.Exit(code)
}
