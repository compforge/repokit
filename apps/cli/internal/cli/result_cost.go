package cli

import (
	"github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/apps/cli/internal/cli/validation"
)

type commandCost struct {
	WallSeconds float64          `json:"wallSeconds"`
	Phases      []phaseCost      `json:"phases,omitempty"`
	Validation  *validation.Cost `json:"validation,omitempty"`
}
type phaseCost = command.Phase
