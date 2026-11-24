// Package depot observes and prepares checkout-local Component supporting content.
package depot

import (
	"github.com/compforge/quality-harness/sdks/go/common"
	"github.com/compforge/repokit/toolkit/go/errs"
)

type Options struct{ Components []string }
type Component struct {
	Root  string        `json:"root"`
	Depot *common.Depot `json:"depot"`
}
type Phase struct {
	Name            string  `json:"name"`
	Bin             string  `json:"bin,omitempty"`
	DurationSeconds float64 `json:"durationSeconds"`
}
type Bin struct {
	common.Bin
	RecipeRoot  string      `json:"recipeRoot,omitempty"`
	Components  []string    `json:"components"`
	Root        string      `json:"root"`
	Manager     string      `json:"manager,omitempty"`
	Command     []string    `json:"command"`
	Inputs      []string    `json:"inputs"`
	Fingerprint string      `json:"fingerprint,omitempty"`
	Status      string      `json:"status"`
	Reason      string      `json:"reason,omitempty"`
	Action      string      `json:"action,omitempty"`
	Executed    bool        `json:"executed"`
	ExitCode    *int        `json:"exitCode,omitempty"`
	Stdout      string      `json:"stdout,omitempty"`
	Stderr      string      `json:"stderr,omitempty"`
	Error       *errs.Error `json:"error,omitempty"`
}

// MakeRecipe is a Component-owned installation command, executed once for its physical Bins.
type MakeRecipe struct {
	Root        string      `json:"root"`
	Error       *errs.Error `json:"error,omitempty"`
	Command     []string    `json:"command"`
	Fingerprint string      `json:"fingerprint"`
	Executed    bool        `json:"executed"`
	ExitCode    *int        `json:"exitCode,omitempty"`
	Stdout      string      `json:"stdout,omitempty"`
	Stderr      string      `json:"stderr,omitempty"`
}

type Report struct {
	Recipes    []MakeRecipe `json:"recipes,omitempty"`
	Checkout   string       `json:"checkout"`
	Components []Component  `json:"components"`
	Bins       []Bin        `json:"bins"`
	Complete   bool         `json:"complete"`
	Phases     []Phase      `json:"phases"`
}
