//go:build !linux && !darwin

package command

import (
	"os/exec"
)

func gitProcessGroup(*exec.Cmd) {}
