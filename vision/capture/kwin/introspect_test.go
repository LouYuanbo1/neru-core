package kwin

import (
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

// plasma6Introspection is the shape of KWin's ScreenShot2 document from Plasma
// 6: a dedicated service, a string-addressed CaptureScreen, and both
// CaptureActiveScreen and CaptureWorkspace.
const plasma6Introspection = `<node name="/org/kde/KWin/ScreenShot2">
  <interface name="org.freedesktop.DBus.Properties">
    <method name="Get"><arg name="interface" type="s" direction="in"/></method>
  </interface>
  <interface name="org.kde.KWin.ScreenShot2">
    <property name="Version" type="u" access="read"/>
    <method name="CaptureArea">
      <arg name="x" type="i" direction="in"/>
      <arg name="y" type="i" direction="in"/>
      <arg name="width" type="u" direction="in"/>
      <arg name="height" type="u" direction="in"/>
      <arg name="options" type="a{sv}" direction="in"/>
      <arg name="pipe" type="h" direction="in"/>
      <arg name="results" type="a{sv}" direction="out"/>
    </method>
    <method name="CaptureActiveScreen">
      <arg name="options" type="a{sv}" direction="in"/>
      <arg name="pipe" type="h" direction="in"/>
      <arg name="results" type="a{sv}" direction="out"/>
    </method>
    <method name="CaptureScreen">
      <arg name="name" type="s" direction="in"/>
      <arg name="options" type="a{sv}" direction="in"/>
      <arg name="pipe" type="h" direction="in"/>
      <arg name="results" type="a{sv}" direction="out"/>
    </method>
    <method name="CaptureWorkspace">
      <arg name="options" type="a{sv}" direction="in"/>
      <arg name="pipe" type="h" direction="in"/>
      <arg name="results" type="a{sv}" direction="out"/>
    </method>
  </interface>
</node>`

// plasma5Introspection is the shape an older KWin exported: no
// CaptureActiveScreen, no CaptureWorkspace, and a CaptureScreen addressed by a
// screen number.
const plasma5Introspection = `<node name="/org/kde/KWin/ScreenShot2">
  <interface name="org.kde.KWin.ScreenShot2">
    <method name="CaptureArea">
      <arg name="x" type="i" direction="in"/>
      <arg name="y" type="i" direction="in"/>
      <arg name="width" type="u" direction="in"/>
      <arg name="height" type="u" direction="in"/>
      <arg name="options" type="a{sv}" direction="in"/>
      <arg name="pipe" type="h" direction="in"/>
      <arg name="results" type="a{sv}" direction="out"/>
    </method>
    <method name="CaptureScreen">
      <arg name="screen" type="u" direction="in"/>
      <arg name="options" type="a{sv}" direction="in"/>
      <arg name="pipe" type="h" direction="in"/>
      <arg name="results" type="a{sv}" direction="out"/>
    </method>
  </interface>
</node>`

func TestParseIntrospection_ReadsTheScreenShot2Interface(t *testing.T) {
	screen, err := parseIntrospection([]byte(plasma6Introspection))
	if err != nil {
		t.Fatalf("parseIntrospection: %v", err)
	}

	if !screen.supports(methodCaptureArea, "i", "i", "u", "u", "a{sv}", "h") {
		t.Error("CaptureArea(i, i, u, u, a{sv}, h) was not read")
	}

	if !screen.supports(methodCaptureScreen, "s", "a{sv}", "h") {
		t.Error("CaptureScreen(s, a{sv}, h) was not read")
	}

	if !screen.areaCapture() {
		t.Error("areaCapture() = false, want true")
	}

	method, ok := screen.screenCaptureMethod()
	if !ok || method != methodCaptureActiveScreen {
		t.Errorf("screenCaptureMethod() = %q, %v, want %q, true", method, ok, methodCaptureActiveScreen)
	}
}

func TestScreenCaptureMethod_FallsBackToTheWorkspace(t *testing.T) {
	document := `<node>
  <interface name="org.kde.KWin.ScreenShot2">
    <method name="CaptureWorkspace">
      <arg name="options" type="a{sv}" direction="in"/>
      <arg name="pipe" type="h" direction="in"/>
      <arg name="results" type="a{sv}" direction="out"/>
    </method>
  </interface>
</node>`

	screen, err := parseIntrospection([]byte(document))
	if err != nil {
		t.Fatalf("parseIntrospection: %v", err)
	}

	method, ok := screen.screenCaptureMethod()
	if !ok || method != methodCaptureWorkspace {
		t.Errorf("screenCaptureMethod() = %q, %v, want %q, true", method, ok, methodCaptureWorkspace)
	}
}

func TestScreenCaptureMethod_RefusesAnInterfaceWithoutAUsableScreenMethod(t *testing.T) {
	screen, err := parseIntrospection([]byte(plasma5Introspection))
	if err != nil {
		t.Fatalf("parseIntrospection: %v", err)
	}

	if _, ok := screen.screenCaptureMethod(); ok {
		t.Error("screenCaptureMethod() found a method it must not call")
	}

	if !screen.areaCapture() {
		t.Error("areaCapture() = false, want true")
	}
}

func TestSupports_ChecksTheWholeSignature(t *testing.T) {
	screen, err := parseIntrospection([]byte(plasma6Introspection))
	if err != nil {
		t.Fatalf("parseIntrospection: %v", err)
	}

	tests := []struct {
		name   string
		method string
		in     []string
		want   bool
	}{
		{name: "exact", method: methodCaptureActiveScreen, in: []string{"a{sv}", "h"}, want: true},
		{name: "an extra argument", method: methodCaptureActiveScreen, in: []string{"a{sv}", "h", "s"}},
		{name: "a different type", method: methodCaptureActiveScreen, in: []string{"a{sv}", "i"}},
		{name: "the wrong order", method: methodCaptureArea, in: []string{"i", "i", "u", "u", "h", "a{sv}"}},
		{name: "an unknown method", method: "CaptureEverything", in: []string{"a{sv}", "h"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := screen.supports(test.method, test.in...); got != test.want {
				t.Errorf("supports(%q, %v) = %v, want %v", test.method, test.in, got, test.want)
			}
		})
	}
}

func TestParseIntrospection_RefusesADocumentWithoutTheInterface(t *testing.T) {
	document := `<node><interface name="org.freedesktop.DBus.Peer"/></node>`

	if _, err := parseIntrospection([]byte(document)); !errs.IsCode(err, errs.CodeNotSupported) {
		t.Errorf("error = %v, want CodeNotSupported", err)
	}
}

func TestParseIntrospection_RefusesMalformedXML(t *testing.T) {
	if _, err := parseIntrospection([]byte("<node><interface")); !errs.IsCode(err, errs.CodeInternal) {
		t.Errorf("error = %v, want CodeInternal", err)
	}
}

func TestParseIntrospection_RefusesAnAreaMethodWithTheWrongTypes(t *testing.T) {
	document := `<node>
  <interface name="org.kde.KWin.ScreenShot2">
    <method name="CaptureArea">
      <arg name="x" type="u" direction="in"/>
      <arg name="y" type="u" direction="in"/>
      <arg name="width" type="u" direction="in"/>
      <arg name="height" type="u" direction="in"/>
      <arg name="options" type="a{sv}" direction="in"/>
      <arg name="pipe" type="h" direction="in"/>
      <arg name="results" type="a{sv}" direction="out"/>
    </method>
  </interface>
</node>`

	screen, err := parseIntrospection([]byte(document))
	if err != nil {
		t.Fatalf("parseIntrospection: %v", err)
	}

	if screen.areaCapture() {
		t.Error("areaCapture() = true for an unsigned x and y, want false")
	}
}
