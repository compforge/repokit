//go:build !linux && !darwin

package gitworktree

import (
	"os/exec"
)

func gitProcessGroup(*exec.Cmd) {}
