//go:build integration && linux

package uinput

import (
	"testing"
	"time"
)

// TestDevice_DrivesTheKernelWheel is the integration test for the whole
// /dev/uinput path: it creates the device, scrolls, holds and releases a
// modifier, and tears the device down again.
//
// It needs a writable /dev/uinput, so it is behind the integration tag and
// skips rather than fails where the node is absent (CI containers, a user who
// has not joined the input group). What it cannot do is observe the effect: a
// headless runner has no compositor reading the events, so the assertion is that
// every ioctl and write the kernel was given was accepted.
func TestDevice_DrivesTheKernelWheel(t *testing.T) {
	if !Available() {
		t.Skip("no writable uinput node; skipping the device test")
	}

	device, err := Open()
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	t.Cleanup(func() { _ = device.Close() })

	// A whole notch up, then a diagonal movement that contributes a notch down
	// and one left, so both accumulators and both axes run.
	if err := device.Scroll(0, 2*scrollPixelsPerNotch); err != nil {
		t.Fatalf("Scroll(0, %d) error = %v", 2*scrollPixelsPerNotch, err)
	}

	if err := device.Scroll(scrollPixelsPerNotch, -scrollPixelsPerNotch); err != nil {
		t.Fatalf("Scroll(%d, %d) error = %v", scrollPixelsPerNotch, -scrollPixelsPerNotch, err)
	}

	// A sub-notch delta must be accepted: on a high-resolution kernel it goes out
	// as a fraction of a notch, and on an older one it is held for the next call.
	if err := device.Scroll(1, 1); err != nil {
		t.Fatalf("Scroll(1, 1) error = %v", err)
	}

	if err := device.Key(29, true); err != nil { // KEY_LEFTCTRL
		t.Fatalf("Key(29, true) error = %v", err)
	}

	if err := device.Key(29, false); err != nil {
		t.Fatalf("Key(29, false) error = %v", err)
	}

	// A code the device does not advertise must be refused, not dropped by the
	// kernel later.
	if err := device.Key(30, true); err == nil { // KEY_A
		t.Error("Key(30, true) = nil, want a refusal for an unadvertised key")
	}

	// Writes are consumed asynchronously; give the input core a moment before
	// the device disappears.
	time.Sleep(50 * time.Millisecond)

	if err := device.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if err := device.Key(29, true); err == nil {
		t.Error("Key on a closed device = nil, want an error")
	}
}
