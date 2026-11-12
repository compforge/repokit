package cli

import (
	"github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/toolkit/go/errs"
)

func usageFailure(err error) *errs.Error {
	return &errs.Error{Code: "usage.invalid_argument", Message: err.Error(), Op: "arguments", Cause: err}
}

func executionFailure(err error) error { return command.ExecutionFailure(err) }

func operationFailure(code, op, subject string, err error) error {
	return errs.Wrap(err, code, op, subject)
}
