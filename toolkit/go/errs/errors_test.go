package errs_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
)

// +case=SDK consumers receive identical error facts in JSON and text without serializing the cause
func TestRepresentationsAndCause(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cause := errors.New("private-cause-detail")
	e := &errs.Error{Code: "forge.outcome_unknown", Message: "request failed", Op: "POST", Subject: "/repos/acme/repo/pulls", Uncertain: true, HTTPStatus: 503, RetryAt: &at, Hint: "Reconcile before retrying", Cause: cause}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["code"] != e.Code || wire["op"] != e.Op || wire["subject"] != e.Subject || wire["uncertain"] != true || wire["httpStatus"] != float64(503) || wire["retryAt"] != "2026-01-01T00:00:00Z" {
		t.Fatalf("%s", data)
	}
	if strings.Contains(string(data), "cause") || strings.Contains(e.Text(), "private-cause-detail") || !errors.Is(e, cause) {
		t.Fatal("lost cause isolation")
	}
	for _, want := range []string{e.Code, e.Op, e.Subject, e.Message, e.Hint, "outcome unknown", "HTTP 503", "2026-01-01T00:00:00Z"} {
		if !strings.Contains(e.Text(), want) {
			t.Fatalf("missing %q: %s", want, e.Text())
		}
	}
}

func TestWrapPreservesClassificationAndContext(t *testing.T) {
	original := &errs.Error{Code: "forge.outcome_unknown", Message: "response lost", Op: "POST", Uncertain: true, Cause: context.DeadlineExceeded}
	outer := fmt.Errorf("create PR: %w", original)
	wrapped := errs.Wrap(outer, "execution.failed", "cli", "acme/repo")
	var e *errs.Error
	if !errors.As(wrapped, &e) || e.Code != original.Code || e.Op != "POST" || e.Subject != "acme/repo" || !e.Uncertain || e.Message != outer.Error() || !errors.Is(wrapped, context.DeadlineExceeded) {
		t.Fatalf("%+v", wrapped)
	}
	if original.Subject != "" || errs.Wrap(wrapped, "ignored", "ignored", "ignored") != wrapped {
		t.Fatal("mutated or replaced existing context")
	}
	if errs.Wrap(nil, "failed", "test", "") != nil {
		t.Fatal("typed nil error")
	}
}

func TestStandardClassificationDoesNotParseText(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{context.Canceled, "execution.canceled"}, {context.DeadlineExceeded, "execution.deadline_exceeded"},
		{&os.PathError{Op: "open", Path: "missing", Err: os.ErrNotExist}, "filesystem.not_found"},
		{errors.New("network timeout permission denied"), "repository.failed"},
	} {
		wrapped := errs.Wrap(tc.err, "repository.failed", "inspect", "repo")
		var e *errs.Error
		if !errors.As(wrapped, &e) || e.Code != tc.code || !errors.Is(wrapped, tc.err) {
			t.Fatalf("%+v", wrapped)
		}
	}
}
