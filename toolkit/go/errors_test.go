package repokit_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/compforge/repokit/toolkit/go"
	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/state"
)

// +case=SDK calls expose structured operation errors without going through the CLI
func TestPublicOperationErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	_, discovery := repokit.FindCheckout(context.Background(), missing)
	_, validation := repokit.PlanValidation(context.Background(), repokit.ValidationRequest{Repository: missing, Checks: []string{"unknown"}})
	_, topology := state.Checkout(context.Background(), missing)
	policy := (state.WorktreePolicy{MaxCount: -1}).Validate()
	for _, err := range []error{discovery, validation, topology, policy} {
		var e *errs.Error
		if !errors.As(err, &e) || e.Code == "" || e.Op == "" {
			t.Fatalf("unstructured error: %T %v", err, err)
		}
	}
	var e *errs.Error
	if !errors.As(discovery, &e) || e.Code != "filesystem.not_found" || e.Subject != missing {
		t.Fatal(discovery)
	}
}
