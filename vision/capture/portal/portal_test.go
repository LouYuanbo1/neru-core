//go:build linux

package portal

import (
	"context"
	"image"
	"testing"
	"time"

	"github.com/LouYuanbo1/neru-core/errs"
)

// noSessionBus points the session bus at a socket nothing is listening on, so a
// test can exercise the no-portal path without depending on the machine it runs
// on.
const noSessionBus = "unix:path=/nonexistent/neru-core-portal-test-bus"

func TestAvailable_ReportsFalseWithoutASessionBus(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", noSessionBus)

	if Available() {
		t.Error("Available reported a session with no reachable bus")
	}
}

func TestCaptureScreen_WithoutAPortalIsNotSupported(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", noSessionBus)

	_, err := CaptureScreen(context.Background())
	if !errs.IsCode(err, errs.CodeNotSupported) {
		t.Errorf("error = %v, want CodeNotSupported", err)
	}
}

func TestCaptureScreen_ReportsAnAlreadyCanceledCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := CaptureScreen(ctx)
	if !errs.IsCode(err, errs.CodeContextCanceled) {
		t.Errorf("error = %v, want CodeContextCanceled", err)
	}
}

func TestCaptureRect_RejectsAnEmptyRegionBeforeAskingThePortal(t *testing.T) {
	// No session bus is configured, so an empty region that reached the portal
	// would fail as CodeNotSupported instead of CodeInvalidInput.
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", noSessionBus)

	tests := []struct {
		name   string
		region image.Rectangle
	}{
		{name: "the zero rectangle", region: image.Rectangle{}},
		{name: "a rectangle with no width", region: image.Rect(10, 10, 10, 40)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := CaptureRect(context.Background(), test.region)
			if !errs.IsCode(err, errs.CodeInvalidInput) {
				t.Errorf("error = %v, want CodeInvalidInput", err)
			}
		})
	}
}

func TestWithBudget_KeepsTheCallersOwnDeadline(t *testing.T) {
	want := time.Now().Add(5 * time.Second)

	ctx, cancel := context.WithDeadline(context.Background(), want)
	defer cancel()

	budgeted, done := withBudget(ctx)
	defer done()

	got, hasDeadline := budgeted.Deadline()
	if !hasDeadline || !got.Equal(want) {
		t.Errorf("deadline = %v (set %t), want the caller's %v", got, hasDeadline, want)
	}
}

func TestWithBudget_AppliesTheRequestBudgetWithoutOne(t *testing.T) {
	budgeted, done := withBudget(context.Background())
	defer done()

	got, hasDeadline := budgeted.Deadline()
	if !hasDeadline {
		t.Fatal("withBudget left a call with no deadline unbounded")
	}

	if remaining := time.Until(got); remaining > requestBudget {
		t.Errorf("deadline is %v away, want no more than %v", remaining, requestBudget)
	}
}
