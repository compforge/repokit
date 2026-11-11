// Package errs defines RepoKit operation errors and their JSON/text representation.
// It owns no process exit codes, streams, logging, or retry policy.
package errs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// Error carries machine-readable facts alongside human-readable context.
// +spec=Error JSON excludes the cause while errors.Is/As retain the complete cause chain
// +link=component://docs/errors.md
type Error struct {
	Code       string     `json:"code"`
	Message    string     `json:"message"`
	Op         string     `json:"op,omitempty"`
	Subject    string     `json:"subject,omitempty"`
	Hint       string     `json:"hint,omitempty"`
	Uncertain  bool       `json:"uncertain,omitempty"`
	HTTPStatus int        `json:"httpStatus,omitempty"`
	RetryAt    *time.Time `json:"retryAt,omitempty"`
	Cause      error      `json:"-"`
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

// Text renders the same error facts used by JSON, without terminal or stream policy.
func (e *Error) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]", e.Code)
	if e.Op != "" {
		fmt.Fprintf(&b, " %s", e.Op)
	}
	if e.Subject != "" {
		fmt.Fprintf(&b, " (%s)", e.Subject)
	}
	fmt.Fprintf(&b, ": %s", e.Message)
	if e.Uncertain {
		b.WriteString("; outcome unknown")
	}
	if e.HTTPStatus != 0 {
		fmt.Fprintf(&b, "; HTTP %d", e.HTTPStatus)
	}
	if e.RetryAt != nil {
		fmt.Fprintf(&b, "; retry after %s", e.RetryAt.UTC().Format(time.RFC3339Nano))
	}
	if e.Hint != "" {
		fmt.Fprintf(&b, "\nHint: %s", e.Hint)
	}
	return b.String()
}

// Wrap preserves an existing classification and fills only missing operation context.
// Untyped standard errors are classified by errors.Is/As; other errors use code.
// +spec=Wrapping never mutates shared errors or erases uncertainty and upstream evidence
func Wrap(err error, code, op, subject string) error {
	if err == nil {
		return nil
	}
	var original *Error
	if errors.As(err, &original) {
		if err == original && (original.Op != "" || op == "") && (original.Subject != "" || subject == "") {
			return err
		}
		result := *original
		result.Message, result.Cause = err.Error(), err
		if result.Op == "" {
			result.Op = op
		}
		if result.Subject == "" {
			result.Subject = subject
		}
		return &result
	}
	result := &Error{Code: code, Message: err.Error(), Op: op, Subject: subject, Cause: err}
	var network net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		result.Code = "execution.deadline_exceeded"
	case errors.Is(err, context.Canceled):
		result.Code = "execution.canceled"
	case errors.Is(err, os.ErrPermission):
		result.Code = "filesystem.permission_denied"
	case errors.Is(err, os.ErrNotExist):
		result.Code = "filesystem.not_found"
	case errors.As(err, &network):
		result.Code = "network.failed"
	}
	return result
}
