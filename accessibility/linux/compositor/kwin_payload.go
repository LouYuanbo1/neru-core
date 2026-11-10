package compositor

// The wire format between the KWin script and this package. It is one
// comma-separated string because KWin JavaScript's number marshaling is not
// worth depending on. Parsing is kept in kwinPayload's function so it can be
// tested without a bus.

const (
	// payloadParts is how many fields UpdateActiveWindow's payload has:
	// "x,y,w,h,resourceClass,resourceName,caption". The caption is last because
	// it is the one field that can contain a comma, and the split keeps the
	// remainder there.
	payloadParts = 7

	// payloadMinParts is the geometry minimum. A KWin version that reports no
	// identity still reports a rectangle.
	payloadMinParts = 4

	// The optional identity fields. Both application identifiers are carried
	// because KWin's foreign-toplevel app_id is built from either one, depending
	// on the window.
	payloadClassField = 4
	payloadNameField  = 5
	payloadTitleField = 6
)
