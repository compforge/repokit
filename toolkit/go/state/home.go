package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ResolveHome gives explicit options precedence over REPOKIT_HOME and ~/.repokit.
// +spec=`Resolving a home neither creates directories nor enables a resource operation.`
func ResolveHome(explicit string) (string, error) {
	value := explicit
	if value == "" {
		value = os.Getenv("REPOKIT_HOME")
	}
	if value == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(user, ".repokit")
	}
	if strings.TrimSpace(value) == "" {
		return "", errors.New("RepoKit home must be nonempty")
	}
	return filepath.Abs(value)
}
