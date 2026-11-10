package gnome

import (
	"testing"

	"github.com/LouYuanbo1/neru-core/errs"
)

// shellIntrospection is the shape of GNOME Shell's Screenshot document: the
// whole-screen method this package calls beside the methods it does not.
const shellIntrospection = `<node>
  <interface name="org.gnome.Shell.Screenshot">
    <method name="InteractiveScreenshot">
      <arg type="b" direction="out" name="success"/>
      <arg type="s" direction="out" name="uri"/>
    </method>
    <method name="Screenshot">
      <arg type="b" direction="in" name="include_cursor"/>
      <arg type="b" direction="in" name="flash"/>
      <arg type="s" direction="in" name="filename"/>
      <arg type="b" direction="out" name="success"/>
      <arg type="s" direction="out" name="filename_used"/>
    </method>
    <method name="ScreenshotWindow">
      <arg type="b" direction="in" name="include_frame"/>
      <arg type="b" direction="in" name="include_cursor"/>
      <arg type="b" direction="in" name="flash"/>
      <arg type="s" direction="in" name="filename"/>
      <arg type="b" direction="out" name="success"/>
      <arg type="s" direction="out" name="filename_used"/>
    </method>
    <method name="ScreenshotArea">
      <arg type="i" direction="in" name="x"/>
      <arg type="i" direction="in" name="y"/>
      <arg type="i" direction="in" name="width"/>
      <arg type="i" direction="in" name="height"/>
      <arg type="b" direction="in" name="flash"/>
      <arg type="s" direction="in" name="filename"/>
      <arg type="b" direction="out" name="success"/>
      <arg type="s" direction="out" name="filename_used"/>
    </method>
  </interface>
</node>`

func TestParseIntrospection_ReadsTheScreenshotInterface(t *testing.T) {
	shell, err := parseIntrospection([]byte(shellIntrospection))
	if err != nil {
		t.Fatalf("parseIntrospection: %v", err)
	}

	if !shell.takesScreenshots() {
		t.Error("takesScreenshots() = false, want true")
	}

	if !shell.supports(methodScreenshotArea, "i", "i", "i", "i", "b", "s") {
		t.Error("ScreenshotArea's signature was not read")
	}

	if !shell.supports(methodScreenshotWindow, "b", "b", "b", "s") {
		t.Error("ScreenshotWindow's signature was not read")
	}
}

func TestTakesScreenshots_RequiresTheWholeScreenMethod(t *testing.T) {
	document := `<node>
  <interface name="org.gnome.Shell.Screenshot">
    <method name="ScreenshotArea">
      <arg type="i" direction="in" name="x"/>
      <arg type="i" direction="in" name="y"/>
      <arg type="i" direction="in" name="width"/>
      <arg type="i" direction="in" name="height"/>
      <arg type="b" direction="in" name="flash"/>
      <arg type="s" direction="in" name="filename"/>
      <arg type="b" direction="out" name="success"/>
      <arg type="s" direction="out" name="filename_used"/>
    </method>
  </interface>
</node>`

	shell, err := parseIntrospection([]byte(document))
	if err != nil {
		t.Fatalf("parseIntrospection: %v", err)
	}

	if shell.takesScreenshots() {
		t.Error("takesScreenshots() = true without a Screenshot method")
	}
}

func TestTakesScreenshots_RejectsADifferentSignature(t *testing.T) {
	document := `<node>
  <interface name="org.gnome.Shell.Screenshot">
    <method name="Screenshot">
      <arg type="s" direction="in" name="filename"/>
      <arg type="b" direction="in" name="include_cursor"/>
      <arg type="b" direction="in" name="flash"/>
      <arg type="b" direction="out" name="success"/>
      <arg type="s" direction="out" name="filename_used"/>
    </method>
  </interface>
</node>`

	shell, err := parseIntrospection([]byte(document))
	if err != nil {
		t.Fatalf("parseIntrospection: %v", err)
	}

	if shell.takesScreenshots() {
		t.Error("takesScreenshots() = true for a Screenshot(s, b, b) signature")
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
