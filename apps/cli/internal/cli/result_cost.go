package cli

import (
	"io"
	"time"

	"github.com/compforge/repokit/toolkit/go"
)

type commandCost struct {
	WallSeconds float64         `json:"wallSeconds"`
	Phases      []phaseCost     `json:"phases,omitempty"`
	Validation  *validationCost `json:"validation,omitempty"`
}
type phaseCost struct {
	Name            string  `json:"name"`
	DurationSeconds float64 `json:"durationSeconds"`
}
type validationCost struct {
	ExecutionSeconds float64     `json:"executionSeconds"`
	LookupSeconds    float64     `json:"lookupSeconds"`
	Steps            []checkCost `json:"steps"`
}
type checkCost struct {
	Component        string  `json:"component"`
	Check            string  `json:"check"`
	Status           string  `json:"status"`
	Scope            string  `json:"scope"`
	Reused           bool    `json:"reused"`
	ExecutionSeconds float64 `json:"executionSeconds"`
	LookupSeconds    float64 `json:"lookupSeconds"`
	Evidence         string  `json:"evidence"`
}

// +spec=Current invocation cost excludes execution durations of reused historical runs
// +why=Summed concurrent check time is not wall time and cannot be used to calculate CLI overhead
func currentCheckCost(component string, check repokit.CheckResult, evidence string) checkCost {
	cost := checkCost{Component: component, Check: check.Check, Status: check.Status, Scope: check.Scope, Reused: check.Reused, LookupSeconds: check.LookupSeconds, Evidence: evidence}
	if !check.Reused {
		cost.ExecutionSeconds = check.DurationSeconds
	}
	return cost
}

func measurePhase(w io.Writer, name string) func() {
	start := time.Now()
	return func() {
		if o, ok := w.(*resultOutput); ok {
			o.result.Cost.Phases = append(o.result.Cost.Phases, phaseCost{Name: name, DurationSeconds: time.Since(start).Seconds()})
		}
	}
}
