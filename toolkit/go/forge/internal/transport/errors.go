package transport

import (
	"errors"
	"fmt"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
)

func HTTPFailure(status int, retryAt *time.Time) *errs.Error {
	code := "forge.http_error"
	switch status {
	case 401:
		code = "forge.authentication_required"
	case 403:
		code = "forge.access_denied"
	case 404:
		code = "forge.not_found"
	case 429:
		code = "forge.rate_limited"
	}
	result := &errs.Error{Code: code, Message: fmt.Sprintf("Forge HTTP %d", status), HTTPStatus: status}
	if retryAt != nil {
		at := *retryAt
		result.RetryAt = &at
	}
	return result
}

// +spec=Uncertain writes retain their transport evidence and require reconciliation before another write
func UnknownFailure(err error) *errs.Error {
	result := &errs.Error{}
	var upstream *errs.Error
	if errors.As(err, &upstream) {
		*result = *upstream
	}
	result.Code, result.Message = "forge.outcome_unknown", "Forge write outcome unknown: "+err.Error()
	result.Uncertain, result.Cause = true, err
	result.Hint = "Query the target PR or source/target branches to reconcile the write before retrying."
	return result
}
