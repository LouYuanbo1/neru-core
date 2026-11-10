//go:build integration && linux

package wlrinject

import (
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

// TestClient_InjectsOnTheRunningCompositor is the live check. It is opt-in
// because it moves the real cursor, clicks, scrolls and taps shift on the
// developer's session; it skips when there is no Wayland session or when the
// compositor exposes neither wlroots protocol.
func TestClient_InjectsOnTheRunningCompositor(t *testing.T) {
	if !Available() {
		t.Skip("no Wayland socket: set WAYLAND_DISPLAY to run this test")
	}

	client, err := Dial()
	if err != nil {
		if errs.IsNotSupported(err) {
			t.Skipf("this compositor has no wlroots input protocols: %v", err)
		}

		t.Fatalf("Dial: %v", err)
	}

	if !client.HasPointer() && !client.HasKeyboard() {
		t.Fatal("Dial succeeded without giving this client any device")
	}

	if client.HasPointer() {
		if err := client.MoveTo(1, 1); err != nil {
			t.Fatalf("MoveTo: %v", err)
		}

		if err := client.Button(1, true); err != nil {
			t.Fatalf("Button(1, press): %v", err)
		}

		if err := client.Button(1, false); err != nil {
			t.Fatalf("Button(1, release): %v", err)
		}

		// The evdev numbering is accepted too.
		if err := client.Button(273, true); err != nil {
			t.Fatalf("Button(BTN_RIGHT, press): %v", err)
		}

		if err := client.Button(273, false); err != nil {
			t.Fatalf("Button(BTN_RIGHT, release): %v", err)
		}

		if err := client.Scroll(0, 30); err != nil {
			t.Fatalf("Scroll vertical: %v", err)
		}

		if err := client.Scroll(30, 0); err != nil {
			t.Fatalf("Scroll horizontal: %v", err)
		}

		if err := client.Button(99, true); err == nil {
			t.Error("Button accepted a code that is not a mouse button")
		}
	}

	if client.HasKeyboard() {
		if err := client.Key(42, true); err != nil {
			t.Fatalf("Key(shift, press): %v", err)
		}

		if err := client.Key(42, false); err != nil {
			t.Fatalf("Key(shift, release): %v", err)
		}

		if err := client.Key(0, true); err == nil {
			t.Error("Key accepted the reserved code 0")
		}
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	if client.HasPointer() || client.HasKeyboard() {
		t.Error("a closed client still reports a device")
	}

	if err := client.MoveTo(1, 1); err == nil {
		t.Error("a closed client accepted a move")
	}
}
