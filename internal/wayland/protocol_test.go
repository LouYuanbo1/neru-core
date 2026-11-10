//go:build linux

package wayland

import (
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

func TestParseRegistryGlobal_ReturnsNameInterfaceAndVersion(t *testing.T) {
	body := encodeArguments(func(e *encoder) {
		e.uint32(42)
		e.string("zwlr_screencopy_manager_v1")
		e.uint32(3)
	})

	global, err := parseRegistryGlobal(body)
	if err != nil {
		t.Fatalf("parseRegistryGlobal: %v", err)
	}

	if global.name != 42 || global.iface != "zwlr_screencopy_manager_v1" || global.version != 3 {
		t.Errorf("global = %+v, want name 42, screencopy manager at version 3", global)
	}
}

func TestParseRegistryGlobal_RejectsTruncatedBody(t *testing.T) {
	if _, err := parseRegistryGlobal([]byte{1, 0, 0, 0}); err == nil {
		t.Fatal("parseRegistryGlobal accepted a body with no interface or version")
	}
}

func TestOutputState_AppliesGeometryModeScaleAndDone(t *testing.T) {
	state := &outputState{globalName: 3, wlName: 17, scale: 1}

	geometry := encodeArguments(func(e *encoder) {
		e.int32(1920)
		e.int32(0)
		e.int32(600) // physical width, mm
		e.int32(340) // physical height, mm
		e.int32(0)   // subpixel
		e.string("Dell")
		e.string("U2720Q")
		e.int32(0) // transform: normal
	})
	if err := state.apply(event{object: 17, opcode: outputEventGeometry, body: geometry}); err != nil {
		t.Fatalf("geometry: %v", err)
	}

	mode := encodeArguments(func(e *encoder) {
		e.uint32(outputModeCurrent)
		e.uint32(3840)
		e.uint32(2160)
		e.uint32(60000)
	})
	if err := state.apply(event{object: 17, opcode: outputEventMode, body: mode}); err != nil {
		t.Fatalf("mode: %v", err)
	}

	scale := encodeArguments(func(e *encoder) { e.uint32(2) })
	if err := state.apply(event{object: 17, opcode: outputEventScale, body: scale}); err != nil {
		t.Fatalf("scale: %v", err)
	}

	if err := state.apply(event{object: 17, opcode: outputEventDone}); err != nil {
		t.Fatalf("done: %v", err)
	}

	snapshot, ok := state.snapshot()
	if !ok {
		t.Fatal("snapshot rejected an output with a current mode")
	}

	want := Output{
		GlobalName: 3,
		X:          1920,
		Y:          0,
		Width:      1920,
		Height:     1080,
		Scale:      2,
		Name:       "Dell U2720Q",
	}
	if snapshot != want {
		t.Errorf("snapshot = %+v, want %+v", snapshot, want)
	}
}

func TestOutputState_KeepsCurrentModeWhenMoreModesArrive(t *testing.T) {
	state := &outputState{wlName: 1, scale: 1}

	current := encodeArguments(func(e *encoder) {
		e.uint32(outputModeCurrent)
		e.uint32(2560)
		e.uint32(1440)
		e.uint32(60000)
	})
	if err := state.apply(event{object: 1, opcode: outputEventMode, body: current}); err != nil {
		t.Fatalf("current mode: %v", err)
	}

	alternative := encodeArguments(func(e *encoder) {
		e.uint32(0)
		e.uint32(800)
		e.uint32(600)
		e.uint32(60000)
	})
	if err := state.apply(event{object: 1, opcode: outputEventMode, body: alternative}); err != nil {
		t.Fatalf("alternative mode: %v", err)
	}

	if state.width != 2560 || state.height != 1440 {
		t.Errorf("mode = %dx%d, want the current 2560x1440", state.width, state.height)
	}
}

func TestOutputState_UsesFirstModeWhenNoneIsMarkedCurrent(t *testing.T) {
	state := &outputState{wlName: 1, scale: 1}

	first := encodeArguments(func(e *encoder) {
		e.uint32(0)
		e.uint32(1024)
		e.uint32(768)
		e.uint32(60000)
	})
	if err := state.apply(event{object: 1, opcode: outputEventMode, body: first}); err != nil {
		t.Fatalf("first mode: %v", err)
	}

	second := encodeArguments(func(e *encoder) {
		e.uint32(0)
		e.uint32(1920)
		e.uint32(1080)
		e.uint32(60000)
	})
	if err := state.apply(event{object: 1, opcode: outputEventMode, body: second}); err != nil {
		t.Fatalf("second mode: %v", err)
	}

	if state.width != 1024 || state.height != 768 {
		t.Errorf("mode = %dx%d, want the first advertised 1024x768", state.width, state.height)
	}
}

func TestOutputState_IgnoresEventsForAnotherObject(t *testing.T) {
	state := &outputState{wlName: 1, scale: 1}

	body := encodeArguments(func(e *encoder) {
		e.int32(50)
		e.int32(50)
		e.int32(0)
		e.int32(0)
		e.int32(0)
		e.string("")
		e.string("")
		e.int32(0)
	})

	if err := state.apply(event{object: 2, opcode: outputEventGeometry, body: body}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if state.hasGeometry {
		t.Error("geometry from another object was applied")
	}
}

func TestOutputSnapshot_ReportsUnitScaleForAnUnscaledOutput(t *testing.T) {
	state := &outputState{wlName: 1, width: 1920, height: 1080, hasMode: true, scale: 1}

	snapshot, ok := state.snapshot()
	if !ok {
		t.Fatal("snapshot rejected a sized output")
	}

	if snapshot.Width != 1920 || snapshot.Height != 1080 || snapshot.Scale != 1 {
		t.Errorf("snapshot = %+v, want 1920x1080 at scale 1", snapshot)
	}
}

func TestOutputSnapshot_RejectsOutputWithoutMode(t *testing.T) {
	state := &outputState{wlName: 1, hasGeometry: true, x: 10, y: 10, scale: 1}

	if _, ok := state.snapshot(); ok {
		t.Fatal("snapshot accepted an output with no mode")
	}
}

func TestOutputSnapshot_NamesOutputByGlobalWhenMakeAndModelAreEmpty(t *testing.T) {
	state := &outputState{globalName: 9, wlName: 1, width: 800, height: 600, hasMode: true, scale: 1}

	snapshot, ok := state.snapshot()
	if !ok {
		t.Fatal("snapshot rejected a sized output")
	}

	if snapshot.Name != "output-9" {
		t.Errorf("name = %q, want output-9", snapshot.Name)
	}
}

func TestCheckOutputCapturable_RefusesRotatedOutput(t *testing.T) {
	err := checkOutputCapturable(&outputState{globalName: 1, transform: 1})

	if !errs.IsCode(err, errs.CodeNotSupported) {
		t.Fatalf("error = %v, want CodeNotSupported", err)
	}

	if err := checkOutputCapturable(&outputState{globalName: 1, transform: outputTransformNormal}); err != nil {
		t.Errorf("a normal output was refused: %v", err)
	}
}

func TestDecodeHeader_ReadsObjectAndOpcode(t *testing.T) {
	body := encodeArguments(func(e *encoder) { e.uint32(1) })
	message := encodeRequest(9, 4, body)

	header, err := decodeHeader(message)
	if err != nil {
		t.Fatalf("decodeHeader: %v", err)
	}

	if header.object != 9 || header.opcode != 4 {
		t.Errorf("header = %+v, want object 9 opcode 4", header)
	}
}

func TestDecodeHeader_RejectsShortInput(t *testing.T) {
	if _, err := decodeHeader([]byte{1, 2}); err == nil {
		t.Fatal("decodeHeader accepted fewer than eight bytes")
	}
}
