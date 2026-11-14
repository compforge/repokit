package validation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/compforge/repokit/toolkit/go/internal/project"
)

func validationMakeContract(ctx context.Context, checkout, component string) (makeContract, error) {
	directory := filepath.Join(checkout, component)
	real, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return makeContract{}, err
	}
	if real != directory {
		return makeContract{}, fmt.Errorf("Component %q crosses a symlink", component)
	}
	makefile := filepath.Join(directory, "Makefile")
	info, err := os.Lstat(makefile)
	if err == nil && !info.Mode().IsRegular() {
		return makeContract{}, fmt.Errorf("Component %q Makefile must be a regular file", component)
	}
	text, err := os.ReadFile(makefile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return makeContract{}, err
	}
	return readMakeContract(ctx, text), nil
}

// Each selected Component/check is planned once; Makefile recipes own any further orchestration.
func appendValidationCheck(plan *ValidationPlan, component project.Binding, spec CheckSpec) {
	for i := range plan.Components {
		if plan.Components[i].Component.Root == component.Root {
			plan.Components[i].Checks = append(plan.Components[i].Checks, spec)
			return
		}
	}
	plan.Components = append(plan.Components, ComponentValidationPlan{Component: component, Checks: []CheckSpec{spec}})
}
