//go:build integration && linux

package libei

import (
	"context"
	"testing"
	"time"

	"github.com/LouYuanbo1/neru-core/errs"
)

// TestDial_ReportsASessionOrATypedRefusal drives the real portal handshake.
//
// It needs libei, a session bus and a compositor with a RemoteDesktop portal, so
// it is behind the integration tag. A consent dialog may appear and may be
// declined; a classified refusal is reported as a skip, because on a headless
// runner there is nothing to approve, while an unclassified error is a bug in
// the error mapping and fails.
func TestDial_ReportsASessionOrATypedRefusal(t *testing.T) {
	if !Available() {
		t.Skip("libei or a D-Bus session bus is unavailable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := Dial(ctx)
	if err != nil {
		switch errs.GetCode(err) {
		case errs.CodeNotSupported,
			errs.CodeAccessibilityDenied,
			errs.CodeAccessibilityFailed,
			errs.CodeTimeout,
			errs.CodeContextCanceled:
			t.Skipf("no libei session in this environment: %v", err)
		default:
			t.Fatalf("Dial() returned an error with no actionable code: %v", err)
		}

		return
	}

	t.Cleanup(func() { _ = client.Close() })

	if err := client.MoveTo(10, 10); err != nil {
		t.Fatalf("MoveTo(10, 10) error = %v", err)
	}

	if err := client.Scroll(0, 30); err != nil {
		t.Fatalf("Scroll(0, 30) error = %v", err)
	}

	if err := client.Scroll(-30, 0); err != nil {
		t.Fatalf("Scroll(-30, 0) error = %v", err)
	}

	if err := client.Button(buttonSelectorLeft, true); err != nil {
		t.Fatalf("Button(left, true) error = %v", err)
	}

	if err := client.Button(buttonSelectorLeft, false); err != nil {
		t.Fatalf("Button(left, false) error = %v", err)
	}

	if err := client.Button(7, true); err == nil {
		t.Error("Button(7, true) = nil, want a refusal for an unknown button")
	}

	if !client.HasKeyboard() {
		t.Log("the granted session carries no keyboard; modifier holds are unavailable")

		return
	}

	if err := client.Key(29, true); err != nil { // KEY_LEFTCTRL
		t.Fatalf("Key(29, true) error = %v", err)
	}

	if err := client.Key(29, false); err != nil {
		t.Fatalf("Key(29, false) error = %v", err)
	}
}
