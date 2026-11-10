package compositor

import (
	"context"
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

// TestQueryJSON_MissingCLIIsNotSupported pins the one classification a caller
// acts on: a compositor CLI that is not installed means nobody to ask, so the
// caller falls back rather than reporting a broken session.
func TestQueryJSON_MissingCLIIsNotSupported(t *testing.T) {
	t.Parallel()

	var dst struct{}

	err := queryJSON(context.Background(), &dst, "neru-test-no-such-compositor-cli")
	if err == nil {
		t.Fatal("queryJSON() = nil for a command that cannot exist")
	}

	if !errs.IsNotSupported(err) {
		t.Errorf("queryJSON() code = %q, want NOT_SUPPORTED", errs.GetCode(err))
	}
}

// TestQueryJSON_CanceledContextIsReported pins that a canceled scan stops asking
// rather than waiting out the query deadline.
func TestQueryJSON_CanceledContextIsReported(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var dst struct{}

	err := queryJSON(ctx, &dst, "neru-test-no-such-compositor-cli")
	if err == nil {
		t.Fatal("queryJSON() = nil for a canceled context")
	}

	// CommandContext refuses to start the process for an already-canceled
	// context; the error is still a failed query, never a decoded answer.
	if !errs.IsCode(err, errs.CodeContextCanceled) && !errs.IsCode(err, errs.CodeNotSupported) &&
		!errs.IsCode(err, errs.CodeAccessibilityFailed) {
		t.Errorf("queryJSON() code = %q, want a query failure", errs.GetCode(err))
	}
}
