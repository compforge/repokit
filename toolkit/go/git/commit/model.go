// Package commit prepares explicit index content and confirms native Git commits.
package commit

import "github.com/compforge/repokit/toolkit/go/errs"

type Selection struct {
	Mode  string   `json:"mode"`  // staged, tracked, or files
	Paths []string `json:"paths"` // literal checkout-root-relative paths (files mode only)
}
type Intent struct {
	Selection
	Branch           string `json:"branch"`
	Head             string `json:"head"` // full commit ID or "unborn"
	Tree             string `json:"tree"`
	IndexFingerprint string `json:"indexFingerprint"`
}
type Options struct {
	Intent
	Message string
}
type Change struct {
	Path    string `json:"path"`
	OldMode string `json:"oldMode"`
	NewMode string `json:"newMode"`
}
type Phase struct {
	Name            string  `json:"name"`
	DurationSeconds float64 `json:"durationSeconds"`
}
type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type Report struct {
	Intent
	Checkout       string       `json:"checkout"`
	Action         string       `json:"action"`
	Changes        []Change     `json:"changes"`
	Empty          bool         `json:"empty"`
	Attempted      bool         `json:"attempted"`
	Commit         string       `json:"commit,omitempty"`
	ActualTree     string       `json:"actualTree,omitempty"`
	Parents        []string     `json:"parents"`
	Confirmed      bool         `json:"confirmed"`
	IndexInstalled bool         `json:"indexInstalled"`
	RecoveryPath   string       `json:"recoveryPath,omitempty"`
	Diagnostics    []Diagnostic `json:"diagnostics"`
	Phases         []Phase      `json:"phases"`
}

func failure(code, message string) error {
	return &errs.Error{Code: "git.commit_" + code, Message: message}
}
