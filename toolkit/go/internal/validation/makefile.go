package validation

import "github.com/compforge/repokit/toolkit/go/internal/makefile"

// +spec=Format selects the first declared target in format, fmt, fix order; execution failure never selects another alias.
// +link=component://docs/validation.md
func validationTarget(contract makefile.Contract, check string) string {
	var candidates []string
	switch check {
	case "format":
		candidates = []string{"format", "fmt", "fix"}
	case "lint":
		candidates = []string{"lint-ci", "lint"}
	case "test":
		candidates = []string{"test", "test-ci", "test-local"}
	case "build":
		candidates = []string{"build"}
	}
	for _, target := range candidates {
		if contract.Targets[target] {
			return target
		}
	}
	return ""
}
