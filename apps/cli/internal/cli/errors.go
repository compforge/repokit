package cli

import (
	"errors"

	"github.com/compforge/repokit/toolkit/go/errs"
)

func usageFailure(err error) *errs.Error {
	return &errs.Error{Code: "usage.invalid_argument", Message: err.Error(), Op: "arguments", Cause: err}
}

func executionFailure(err error) error {
	var reported *resultFailure
	if errors.As(err, &reported) {
		return err
	}
	return errs.Wrap(err, "execution.failed", "execution", "")
}

func operationFailure(code, op, subject string, err error) error {
	return errs.Wrap(err, code, op, subject)
}
