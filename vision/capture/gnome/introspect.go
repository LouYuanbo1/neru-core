package gnome

import (
	"encoding/xml"

	"github.com/LouYuanbo1/neru-core/errs"
)

// The interface this package speaks to and the object GNOME Shell exports it at.
const (
	screenshotInterface  = "org.gnome.Shell.Screenshot"
	screenshotObjectPath = "/org/gnome/Shell/Screenshot"
)

// The interface's methods, as far as this package is concerned.
const (
	// methodScreenshot captures the whole screen into a file. It is the only
	// method this package calls.
	methodScreenshot = "Screenshot"
	// methodScreenshotWindow captures the focused window. It is listed so the
	// introspection this package reads can be described completely.
	methodScreenshotWindow = "ScreenshotWindow"
	// methodScreenshotArea captures a rectangle. It is deliberately not called:
	// the interface does not say whether the rectangle is in logical or physical
	// pixels, nor what scale the file comes back at, so a frame built from it
	// would have to guess its own geometry. A whole-screen capture cropped
	// locally gives the same pixels with no guess at all.
	methodScreenshotArea = "ScreenshotArea"
)

// introspectInterface is the D-Bus introspection interface every exported
// object answers.
const introspectInterface = "org.freedesktop.DBus.Introspectable"

// shellScreenshot is org.gnome.Shell.Screenshot as introspection describes it:
// each method it exports and the D-Bus types of its input arguments.
type shellScreenshot struct {
	// methods maps a method name to the signature of its input arguments, in
	// declaration order. Output arguments are dropped.
	methods map[string][]string
}

// parseIntrospection reads the object's introspection document and pulls out the
// Screenshot interface.
//
// The document is read at run time rather than assumed, because the interface
// gained and lost methods across GNOME releases (PickColor and
// InteractiveScreenshot are recent additions) and a screenshot call to a shell
// that does not export the method would fail as an unknown method instead of
// reporting that this desktop cannot take this path.
func parseIntrospection(document []byte) (shellScreenshot, error) {
	var node dbusNode

	if err := xml.Unmarshal(document, &node); err != nil {
		return shellScreenshot{}, errs.Wrap(
			err,
			errs.CodeInternal,
			"GNOME Shell's Screenshot introspection is not readable XML",
		)
	}

	for _, iface := range node.Interfaces {
		if iface.Name != screenshotInterface {
			continue
		}

		shell := shellScreenshot{methods: make(map[string][]string, len(iface.Methods))}

		for _, method := range iface.Methods {
			signature := make([]string, 0, len(method.Args))

			for _, argument := range method.Args {
				if argument.Direction == "out" {
					continue
				}

				signature = append(signature, argument.Type)
			}

			shell.methods[method.Name] = signature
		}

		return shell, nil
	}

	return shellScreenshot{}, errs.Newf(
		errs.CodeNotSupported,
		"the object at %s does not export GNOME Shell's %s interface",
		screenshotObjectPath,
		screenshotInterface,
	)
}

// supports reports whether the interface exports method with exactly the given
// input signature.
func (s shellScreenshot) supports(method string, in ...string) bool {
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

// takesScreenshots reports whether the shell exports the whole-screen method
// with the signature this package calls.
func (s shellScreenshot) takesScreenshots() bool {
	return s.supports(methodScreenshot, "b", "b", "s")
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
