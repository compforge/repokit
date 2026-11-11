//go:build !darwin && !linux

package depot

import (
	"errors"
	"os/exec"
)

func processGroup(cmd *exec.Cmd) (func(), error) {
	return nil, errors.New("Depot preparation currently requires macOS or Linux process groups")
}
