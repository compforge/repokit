package cli

import (
	"context"
	"errors"
	"net"
	"os"

	"github.com/compforge/repokit/toolkit/go/forge"
)

// commandError is the CLI's terminal error contract. Codes and context are
// machine-readable; Message/Hint are explanatory. Cause never enters JSON.
// +spec=Errors preserve their cause and use stable codes instead of parsing error text
// +link=../../../../docs/cli-output.md
type commandError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Phase      string `json:"phase"`
	Subject    string `json:"subject,omitempty"`
	Hint       string `json:"hint,omitempty"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
	cause      error
	outcome    string
	exitCode   int
}

func (e *commandError) Error() string { return e.Message }
func (e *commandError) Unwrap() error { return e.cause }

func usageFailure(err error) *commandError {
	return &commandError{Code: "usage.invalid_argument", Message: err.Error(), Phase: "arguments", cause: err, outcome: "failed", exitCode: 2}
}

func executionFailure(err error) error {
	if err == nil {
		return nil
	}
	var typed *commandError
	var reported *resultFailure
	if errors.As(err, &typed) || errors.As(err, &reported) {
		return err
	}
	e := &commandError{Code: "execution.failed", Message: err.Error(), Phase: "execution", cause: err, outcome: "failed", exitCode: 1}
	var unknown *forge.OutcomeUnknown
	var httpErr *forge.HTTPError
	var network net.Error
	switch {
	case errors.As(err, &unknown):
		e.Code, e.outcome = "forge.outcome_unknown", "unknown"
		e.Hint = "Query the target PR or source/target branches to reconcile the write before retrying."
	case errors.Is(err, context.DeadlineExceeded):
		e.Code = "execution.deadline_exceeded"
	case errors.Is(err, context.Canceled):
		e.Code = "execution.canceled"
	case errors.As(err, &httpErr):
		e.Code, e.HTTPStatus = "forge.http_error", httpErr.Status
		switch httpErr.Status {
		case 401:
			e.Code = "forge.authentication_required"
		case 403:
			e.Code = "forge.access_denied"
		case 404:
			e.Code = "forge.not_found"
		case 429:
			e.Code = "forge.rate_limited"
		}
	case errors.Is(err, os.ErrPermission):
		e.Code = "filesystem.permission_denied"
	case errors.Is(err, os.ErrNotExist):
		e.Code = "filesystem.not_found"
	case errors.As(err, &network):
		e.Code = "network.failed"
	}
	return e
}

func operationFailure(code, phase, subject string, err error) error {
	classified := executionFailure(err)
	var original *commandError
	if !errors.As(classified, &original) {
		return classified
	}
	// Copy before adding context: wrappers do not mutate an error shared by callers.
	e := *original
	if e.Code == "execution.failed" {
		e.Code = code
	}
	e.Phase, e.Subject = phase, subject
	return &e
}
