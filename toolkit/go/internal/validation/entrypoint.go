package validation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/compforge/repokit/toolkit/go/internal/makefile"
	"github.com/compforge/repokit/toolkit/go/internal/project"
)

func validationMakeContract(ctx context.Context, checkout, component string) (makefile.Contract, error) {
	directory := filepath.Join(checkout, component)
	real, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return makefile.Contract{}, err
	}
	if real != directory {
		return makefile.Contract{}, fmt.Errorf("Component %q crosses a symlink", component)
	}
	makePath := filepath.Join(directory, "Makefile")
	info, err := os.Lstat(makePath)
	if err == nil && !info.Mode().IsRegular() {
		return makefile.Contract{}, fmt.Errorf("Component %q Makefile must be a regular file", component)
	}
	text, err := os.ReadFile(makePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return makefile.Contract{}, err
	}
	return makefile.Parse(ctx, text), nil
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
