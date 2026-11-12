package validation

import "github.com/compforge/repokit/toolkit/go"

type Cost struct {
	ExecutionSeconds float64     `json:"executionSeconds"`
	LookupSeconds    float64     `json:"lookupSeconds"`
	Steps            []CheckCost `json:"steps"`
}
type CheckCost struct {
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
func currentCheckCost(component string, check repokit.CheckResult, evidence string) CheckCost {
	cost := CheckCost{Component: component, Check: check.Check, Status: check.Status, Scope: check.Scope, Reused: check.Reused, LookupSeconds: check.LookupSeconds, Evidence: evidence}
	if !check.Reused {
		cost.ExecutionSeconds = check.DurationSeconds
	}
	return cost
}
