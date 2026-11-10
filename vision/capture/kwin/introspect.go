package kwin

import (
	"encoding/xml"

	"github.com/LouYuanbo1/neru-core/errs"
)

// The D-Bus introspection interface, which every object Qt exports answers.
const introspectInterface = "org.freedesktop.DBus.Introspectable"

// screenShotInterface is the interface this package speaks to, both as its
// D-Bus name and as the name a method call carries.
const screenShotInterface = "org.kde.KWin.ScreenShot2"

// screenShotObjectPath is the object KWin exports ScreenShot2 at.
const screenShotObjectPath = "/org/kde/KWin/ScreenShot2"

// The ScreenShot2 methods this package knows how to call. CaptureScreen is
// listed because it is part of the interface, but it is never called: it
// addresses an output by connector name in Plasma 5.27 and later and by screen
// number in early 5.x releases, and nothing this package can read maps either to
// a rectangle. Capturing whatever output happens to be named or numbered first
// would hand the caller pixels of a screen it did not ask for, so a KWin without
// CaptureActiveScreen and without CaptureWorkspace is refused instead (see
// screenCaptureMethod).
const (
	methodCaptureActiveScreen = "CaptureActiveScreen"
	methodCaptureWorkspace    = "CaptureWorkspace"
	methodCaptureArea         = "CaptureArea"
	methodCaptureScreen       = "CaptureScreen"
)

// screenShot2 is KWin's ScreenShot2 interface as introspection describes it:
// each method it exports and the D-Bus types of its input arguments.
type screenShot2 struct {
	// methods maps a method name to the signature of its input arguments, in
	// declaration order. Output arguments are dropped: none of the methods this
	// package calls has an interesting one.
	methods map[string][]string
}

// parseIntrospection reads the object's introspection document and pulls out
// the ScreenShot2 interface.
//
// The document is read at run time instead of trusting a hardcoded signature
// because the interface moved: Plasma 5 exported it from org.kde.KWin and
// Plasma 6 registers a dedicated service, CaptureScreen changed its argument
// from a screen number to an output name, and methods were added with each
// interface version. Reading the document is how this package tells those apart
// without a version table that would have to be right about every release.
func parseIntrospection(document []byte) (screenShot2, error) {
	var node dbusNode

	if err := xml.Unmarshal(document, &node); err != nil {
		return screenShot2{}, errs.Wrap(
			err,
			errs.CodeInternal,
			"KWin's ScreenShot2 introspection is not readable XML",
		)
	}

	for _, iface := range node.Interfaces {
		if iface.Name != screenShotInterface {
			continue
		}

		screen := screenShot2{methods: make(map[string][]string, len(iface.Methods))}

		for _, method := range iface.Methods {
			signature := make([]string, 0, len(method.Args))

			for _, argument := range method.Args {
				if argument.Direction == "out" {
					continue
				}

				signature = append(signature, argument.Type)
			}

			screen.methods[method.Name] = signature
		}

		return screen, nil
	}

	return screenShot2{}, errs.Newf(
		errs.CodeNotSupported,
		"the object at %s does not export KWin's %s interface",
		screenShotObjectPath,
		screenShotInterface,
	)
}

// supports reports whether the interface exports method with exactly the given
// input signature. The signature is checked because calling a method with the
// wrong argument types fails at KWin with an argument error that would be
// reported as an internal fault rather than as the version mismatch it is.
func (s screenShot2) supports(method string, in ...string) bool {
	signature, present := s.methods[method]
	if !present || len(signature) != len(in) {
		return false
	}

	for index, want := range in {
		if signature[index] != want {
			return false
		}
	}

	return true
}

// screenCaptureMethod is the method this package calls for a whole-screen
// capture, or false when the interface exports neither.
//
// CaptureActiveScreen is preferred because it is the one screen method whose
// target KWin chooses; CaptureWorkspace is the fallback for an interface so old
// that it predates the active-screen method, and captures the whole desktop
// instead of one output. The caller reports the difference through the frame's
// region, which is the layout's whole bounds in the second case.
func (s screenShot2) screenCaptureMethod() (string, bool) {
	if s.supports(methodCaptureActiveScreen, "a{sv}", "h") {
		return methodCaptureActiveScreen, true
	}

	if s.supports(methodCaptureWorkspace, "a{sv}", "h") {
		return methodCaptureWorkspace, true
	}

	return "", false
}

// areaCapture reports whether the interface exports CaptureArea with the
// signature this package calls.
func (s screenShot2) areaCapture() bool {
	return s.supports(methodCaptureArea, "i", "i", "u", "u", "a{sv}", "h")
}

// dbusNode is the subset of a D-Bus introspection document this package reads.
type dbusNode struct {
	Interfaces []dbusInterface `xml:"interface"`
}

// dbusInterface is one interface element of an introspection document.
type dbusInterface struct {
	Name    string       `xml:"name,attr"`
	Methods []dbusMethod `xml:"method"`
}

// dbusMethod is one method element of an introspection document.
type dbusMethod struct {
	Name string    `xml:"name,attr"`
	Args []dbusArg `xml:"arg"`
}

// dbusArg is one argument element of an introspection document. A missing
// direction means an input argument, per the D-Bus introspection format.
type dbusArg struct {
	Type      string `xml:"type,attr"`
	Direction string `xml:"direction,attr"`
}
