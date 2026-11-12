//go:build linux

package window

import (
	"encoding/binary"
	"image"
	"os"
	"slices"
	"strings"
	"sync"
	"unsafe"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/internal/native"
)

// This file binds the Xlib calls the X11 backend makes. libX11 is opened with
// purego at run time, so nothing here needs a C toolchain or CGo, and no Xlib
// symbol is a build-time dependency: a system without libX11 fails in New with a
// coded error rather than at link time.
//
// The calls are all core Xlib protocol. Everything EWMH-shaped — the client list,
// the active window, a window's type, its state, its pid — is one property read
// through XGetWindowProperty, and activation and placement are client messages
// through XSendEvent, so no X11 extension has to be present beyond what the window
// manager itself uses.
//
// Two ABI facts shape the code. A C long is 8 bytes on the 64-bit Linux
// architectures and 4 on the 32-bit ones, and Xlib's Window, Atom, Time and long
// arguments are all a long wide, which is why they are uintptr here and why
// longSize sizes the structures built below. And Xlib widens a property's 32-bit
// items to the client's long, so a WINDOW or ATOM array arrives with one item per
// long rather than one item per 4 bytes.
//
// Xlib reports a request against a window that no longer exists as a BadWindow
// protocol error, delivered to the process's X error handler; the default handler
// prints a diagnostic and returns, which is what happens here. No handler is
// installed: XSetErrorHandler applies to every Display in the process, so a handler
// set by this package would also swallow the diagnostics of whatever else the host
// process does with Xlib. A window destroyed in the middle of an enumeration can
// therefore leave one "X Error of failed request" line on stderr — the record of a
// race with another client, not a failure of the query.

// longSize is sizeof(long) on this ABI: the width of an Xlib Atom, Window and
// Time, and of the long arguments and the data of a client message. Linux is LP64
// on the 64-bit architectures and ILP32 on the 32-bit ones, and uintptr follows
// the same width, so it stands in for long in every binding below.
const longSize = int(unsafe.Sizeof(uintptr(0)))

// xEventSize is sizeof(XEvent): Xlib's event union is padded to 24 longs, which is
// 192 bytes on the 64-bit ABIs and 96 on the 32-bit ones. XSendEvent reads a whole
// XEvent, so the buffer it is handed must be this large even though a client
// message only fills the beginning of it.
const xEventSize = 24 * longSize

// xStatusSuccess is Success from X11/X.h. XGetWindowProperty is the one call in
// this file that reports success as zero — every other Xlib call reports success
// as nonzero — so its status is compared with this constant and not with zero.
const xStatusSuccess = 0

// atomAnyPropertyType is AnyPropertyType: the type argument that accepts a
// property whatever type it stores. It is used where the type is what the read
// wants to learn, such as the two forms of a window title.
const atomAnyPropertyType = 0

// maxPropertyItems caps one property read, counted in 32-bit units. A property is
// read in a single request, so the cap keeps a broken or hostile window from
// asking for an unbounded allocation: 65536 units is 256 KiB of property data, far
// more than a client list, a window type list or a title needs.
const maxPropertyItems = 1 << 16

// xEventClientMessage is ClientMessage, the event type a window-manager message is
// carried in.
const xEventClientMessage = 33

// X11 event and focus constants.
const (
	// substructureMasks is SubstructureRedirectMask|SubstructureNotifyMask
	// (0x00180000). A client message about the desktop is sent to the root window
	// with it, which is how the window manager — the client that redirected
	// substructure requests on the root — receives it.
	substructureMasks = 0x00180000

	// sourcePager is the source indication an EWMH client message carries when the
	// request stands for a pager or taskbar, which is what this backend is: it
	// offers the desktop's windows back to the user. A window manager may treat
	// an application's request differently from a pager's, and the pager's is the
	// one that reliably raises and focuses the window.
	sourcePager = 2

	// currentTime is CurrentTime, which asks the server to stamp the request with
	// its own current time. A real timestamp would have to come from an event this
	// backend never receives, and a fabricated one loses the window manager's
	// freshness comparison, so "now, as the server sees it" is the honest answer.
	currentTime = 0

	// revertToPointerRoot is RevertToPointerRoot: where keyboard focus goes if the
	// focused window disappears. It hands focus back to the root window, whose
	// pointer position decides, rather than to one of the window's ancestors,
	// which for a reparented window is the window manager's frame.
	revertToPointerRoot = 1

	// EWMH _NET_MOVERESIZE_WINDOW fields. data.l[0] carries a gravity in its low
	// byte, one bit per geometry field the message supplies, and the source
	// indication in the high byte.
	//
	// StaticGravity is what makes the requested coordinates the client's own
	// corner rather than the frame's: the window manager must not shift them by
	// the decorations it draws around the client, which are not part of the
	// rectangle this package reports.
	gravityStatic = 10

	moveResizeX      = 1 << 8
	moveResizeY      = 1 << 9
	moveResizeWidth  = 1 << 10
	moveResizeHeight = 1 << 11
	moveResizeSource = 12
)

// xEvent is one Xlib event as raw bytes.
//
// The client message is built field by field at the offsets of the C structure
// instead of through a Go struct mirroring XClientMessageEvent. The layout is the
// C ABI's, not Go's: the structure starts with an int, pads to a long for the
// serial, and then lays the address- and long-sized fields one long apart. Writing
// the fields at offsets expressed in longs keeps the same code correct on the 32-
// and 64-bit ABIs, and the values are written in the platform's native byte order,
// which is the order Xlib reads them in.
type xEvent [xEventSize]byte

// Offsets of the XClientMessageEvent fields this backend fills, inside an XEvent.
// The serial sits between the type and send_event at 1*longSize and is left zero,
// because the server assigns it.
const (
	xEventOffsetType        = 0
	xEventOffsetSendEvent   = 2 * longSize
	xEventOffsetDisplay     = 3 * longSize
	xEventOffsetWindow      = 4 * longSize
	xEventOffsetMessageType = 5 * longSize
	xEventOffsetFormat      = 6 * longSize
	xEventOffsetData        = 7 * longSize
)

// The client message has to fit inside the XEvent it is sent in. This fails the
// build rather than letting a future change to the offsets write past the buffer.
const _ = xEventSize - (xEventOffsetData + 5*longSize)

// x11Funcs holds the Xlib entry points this backend calls.
//
// Pointers are unsafe.Pointer and pointer-sized values are uintptr, because that
// is what purego passes for a C pointer and for C's long. The C int arguments —
// Bool, Status, int and the item widths — are int32, and an Atom, Window or Time,
// all of which are C's unsigned long, is uintptr.
type x11Funcs struct {
	openDisplay       func(name uintptr) uintptr
	closeDisplay      func(display uintptr) int32
	flush             func(display uintptr) int32
	defaultRootWindow func(display uintptr) uintptr
	internAtom        func(display uintptr, name unsafe.Pointer, onlyIfExists int32) uintptr

	getWindowProperty func(
		display, window, property uintptr,
		offset, length uintptr,
		deleteAfterRead int32,
		propertyType uintptr,
		actualType *uintptr,
		actualFormat *int32,
		items, bytesAfter *uintptr,
		data *unsafe.Pointer,
	) int32

	getWindowAttributes  func(display, window uintptr, attributes unsafe.Pointer) int32
	translateCoordinates func(display, source, destination uintptr, sourceX, sourceY int32, destinationX, destinationY *int32, child *uintptr) int32
	sendEvent            func(display, window uintptr, propagate int32, eventMask uintptr, event unsafe.Pointer) int32
	raiseWindow          func(display, window uintptr) int32
	setInputFocus        func(display, window uintptr, revertTo int32, time uintptr) int32
	free                 func(data unsafe.Pointer) int32
}

// x11Libraries is libX11, opened once for the process.
//
// The library handle is never closed: it is cheap to keep, and the whole point of
// caching it is that a second manager must not pay for another dlopen. Closing it
// would also close it for every other user in the process.
type x11Libraries struct {
	x11 *native.Library
	fn  x11Funcs
}

var (
	x11Once     sync.Once
	x11Loaded   *x11Libraries
	x11LoadFail error
)

// loadX11 opens and binds libX11, once.
//
// The soname comes first and the unversioned name second, because a system with
// only the runtime package or only the development symlink installed should still
// work. A failure is remembered: the library does not appear while the process
// runs.
func loadX11() (*x11Libraries, error) {
	x11Once.Do(func() {
		x11Loaded, x11LoadFail = openX11Libraries()
	})

	return x11Loaded, x11LoadFail
}

// openX11Libraries opens libX11 and binds the symbols this backend calls.
func openX11Libraries() (*x11Libraries, error) {
	library, err := native.Open("libX11.so.6", "libX11.so")
	if err != nil {
		return nil, err
	}

	libraries := &x11Libraries{x11: library}

	if err := libraries.bind(); err != nil {
		_ = library.Close()

		return nil, err
	}

	return libraries, nil
}

// bind resolves every symbol the backend uses. The table keeps the symbol name next
// to the function variable it fills, so a missing symbol is reported by name rather
// than as a nil call later.
func (l *x11Libraries) bind() error {
	bindings := []struct {
		symbol string
		target any
	}{
		{"XOpenDisplay", &l.fn.openDisplay},
		{"XCloseDisplay", &l.fn.closeDisplay},
		{"XFlush", &l.fn.flush},
		{"XDefaultRootWindow", &l.fn.defaultRootWindow},
		{"XInternAtom", &l.fn.internAtom},
		{"XGetWindowProperty", &l.fn.getWindowProperty},
		{"XGetWindowAttributes", &l.fn.getWindowAttributes},
		{"XTranslateCoordinates", &l.fn.translateCoordinates},
		{"XSendEvent", &l.fn.sendEvent},
		{"XRaiseWindow", &l.fn.raiseWindow},
		{"XSetInputFocus", &l.fn.setInputFocus},
		{"XFree", &l.fn.free},
	}

	for _, binding := range bindings {
		if err := l.x11.Func(binding.symbol, binding.target); err != nil {
			return err
		}
	}

	return nil
}

// x11Conn is one connection to the X server, the root window every query starts
// from, and the atoms this backend reads.
type x11Conn struct {
	display uintptr
	root    uintptr
	fn      *x11Funcs
	atoms   x11Atoms
}

// openX11Connection opens the display $DISPLAY names, resolves the root window,
// binds the atoms, and checks that the session has an EWMH window manager.
//
// The checks are ordered so the caller gets the most specific answer. A missing
// display name and an X server that cannot be reached are failures;
// _NET_SUPPORTING_WM_CHECK being absent is not, because the connection is fine and
// the session simply has nobody to talk to. That case is CodeNotSupported so a
// caller falls back instead of retrying, which is the same distinction the rest of
// the module draws.
func openX11Connection() (*x11Conn, error) {
	displayName := os.Getenv("DISPLAY")
	if displayName == "" {
		return nil, errs.New(
			errs.CodeNotSupported,
			"window management needs an X11 display and DISPLAY is not set",
		)
	}

	libraries, err := loadX11()
	if err != nil {
		return nil, errs.Wrap(
			err,
			errs.CodeNotSupported,
			"window management needs libX11, which could not be loaded",
		)
	}

	// A null display name makes Xlib read $DISPLAY itself, which is more correct
	// than re-parsing it here: a remote or proxy display string is not always
	// parseable by hand.
	display := libraries.fn.openDisplay(0)
	if display == 0 {
		return nil, errs.Newf(
			errs.CodeAccessibilityFailed,
			"could not open the X display %q",
			displayName,
		)
	}

	conn := &x11Conn{display: display, fn: &libraries.fn}

	conn.root = conn.fn.defaultRootWindow(display)
	if conn.root == 0 {
		_ = conn.close()

		return nil, errs.New(
			errs.CodeAccessibilityFailed,
			"the X display has no default root window",
		)
	}

	if err := conn.internAtoms(); err != nil {
		_ = conn.close()

		return nil, err
	}

	// _NET_SUPPORTING_WM_CHECK is the EWMH handshake: a root window that carries it
	// belongs to a session with an EWMH window manager, and one that does not has
	// no window list to read — twm, a bare X server, or a session where the
	// window manager never started. The property's value is the id of the window
	// manager's own window; this backend does not need it, only the proof that
	// somebody is there to answer.
	//
	// The specification also suggests checking the property on that window to
	// catch a stale value left by a window manager that crashed. It is not checked
	// here: a stale handshake makes the property reads return stale window ids,
	// which Activate's verification turns into a reported failure, whereas a
	// stricter check would refuse a session on one property a window manager is
	// merely supposed to maintain.
	check, err := conn.getProperty(propertyRequest{
		name:         "_NET_SUPPORTING_WM_CHECK",
		window:       conn.root,
		property:     conn.atoms.propSupportingWMCheck,
		propertyType: conn.atoms.typeWindow,
		format:       32,
	})
	if err != nil {
		_ = conn.close()

		return nil, err
	}

	if len(check.longs()) == 0 {
		_ = conn.close()

		return nil, errs.New(
			errs.CodeNotSupported,
			"window management needs an X11 session with an EWMH window manager, and this "+
				"X server has no _NET_SUPPORTING_WM_CHECK on its root window",
		)
	}

	return conn, nil
}

// close releases the X connection. The library stays loaded for the process, so a
// later manager does not pay for another dlopen.
func (c *x11Conn) close() error {
	if c.display == 0 {
		return nil
	}

	display := c.display
	c.display = 0

	if c.fn.closeDisplay(display) == 0 {
		return errs.New(errs.CodeAccessibilityFailed, "could not close the X display")
	}

	return nil
}

// x11Atoms is every atom this backend resolves once per connection.
type x11Atoms struct {
	// EWMH properties on the root window.
	propSupportingWMCheck  uintptr
	propActiveWindow       uintptr
	propClientList         uintptr
	propClientListStacking uintptr
	propMoveResizeWindow   uintptr

	// EWMH properties on a client window.
	propWMName      uintptr
	propWindowType  uintptr
	propWindowState uintptr
	propPID         uintptr

	// propICCCMWMName is WM_NAME, the ICCCM title every X11 window has and the
	// fallback for a window that predates _NET_WM_NAME.
	propICCCMWMName uintptr

	// Property types, which is how a property says what its items mean.
	typeAtom       uintptr
	typeWindow     uintptr
	typeCardinal   uintptr
	typeString     uintptr
	typeUTF8String uintptr

	// Window types this backend lists, and the state that marks a minimized
	// window.
	windowTypeNormal uintptr
	windowTypeDialog uintptr
	stateHidden      uintptr

	// skippedWindowTypes is every _NET_WM_WINDOW_TYPE this backend does not list:
	// the surfaces a desktop or an application creates for something other than a
	// document window. It is filled from skippedWindowTypeNames, in that order.
	skippedWindowTypes []uintptr
}

// skippedWindowTypeNames lists the _NET_WM_WINDOW_TYPE atoms whose windows this
// backend hides, in the order EWMH presents them in its type table.
//
// Each one is a surface rather than a window a user switches to: a desktop
// background, a dock or panel, a window's own toolbar or menu, a utility window
// that belongs to another window, a splash screen, a tooltip, a notification
// bubble, a combo-box dropdown, a drag-and-drop icon, or a popup menu. Listing them
// would fill a caller's window list with pieces of the desktop and bury the windows
// it can actually switch to.
var skippedWindowTypeNames = []string{
	"_NET_WM_WINDOW_TYPE_DESKTOP",
	"_NET_WM_WINDOW_TYPE_DOCK",
	"_NET_WM_WINDOW_TYPE_TOOLBAR",
	"_NET_WM_WINDOW_TYPE_MENU",
	"_NET_WM_WINDOW_TYPE_UTILITY",
	"_NET_WM_WINDOW_TYPE_SPLASH",
	"_NET_WM_WINDOW_TYPE_TOOLTIP",
	"_NET_WM_WINDOW_TYPE_NOTIFICATION",
	"_NET_WM_WINDOW_TYPE_COMBO",
	"_NET_WM_WINDOW_TYPE_DND",
	"_NET_WM_WINDOW_TYPE_POPUP_MENU",
}

// internAtoms resolves every atom this backend uses.
//
// XInternAtom is called with only_if_exists false, which creates an atom the server
// does not have yet. That is what an EWMH client wants for the names it publishes
// itself, and for the predefined types — ATOM, WINDOW, CARDINAL, STRING — it returns
// the atom the server already has.
func (c *x11Conn) internAtoms() error {
	bindings := []struct {
		name   string
		target *uintptr
	}{
		{"_NET_SUPPORTING_WM_CHECK", &c.atoms.propSupportingWMCheck},
		{"_NET_ACTIVE_WINDOW", &c.atoms.propActiveWindow},
		{"_NET_CLIENT_LIST", &c.atoms.propClientList},
		{"_NET_CLIENT_LIST_STACKING", &c.atoms.propClientListStacking},
		{"_NET_MOVERESIZE_WINDOW", &c.atoms.propMoveResizeWindow},
		{"_NET_WM_NAME", &c.atoms.propWMName},
		{"_NET_WM_WINDOW_TYPE", &c.atoms.propWindowType},
		{"_NET_WM_STATE", &c.atoms.propWindowState},
		{"_NET_WM_PID", &c.atoms.propPID},
		{"WM_NAME", &c.atoms.propICCCMWMName},
		{"ATOM", &c.atoms.typeAtom},
		{"WINDOW", &c.atoms.typeWindow},
		{"CARDINAL", &c.atoms.typeCardinal},
		{"STRING", &c.atoms.typeString},
		{"UTF8_STRING", &c.atoms.typeUTF8String},
		{"_NET_WM_WINDOW_TYPE_NORMAL", &c.atoms.windowTypeNormal},
		{"_NET_WM_WINDOW_TYPE_DIALOG", &c.atoms.windowTypeDialog},
		{"_NET_WM_STATE_HIDDEN", &c.atoms.stateHidden},
	}

	for _, binding := range bindings {
		atom, err := c.internAtom(binding.name)
		if err != nil {
			return err
		}

		*binding.target = atom
	}

	c.atoms.skippedWindowTypes = make([]uintptr, 0, len(skippedWindowTypeNames))

	for _, name := range skippedWindowTypeNames {
		atom, err := c.internAtom(name)
		if err != nil {
			return err
		}

		c.atoms.skippedWindowTypes = append(c.atoms.skippedWindowTypes, atom)
	}

	return nil
}

// internAtom resolves one atom name.
//
// The name reaches Xlib as a NUL-terminated byte string. Xlib copies it into the
// server's atom table during the call, so the slice does not have to outlive it.
func (c *x11Conn) internAtom(name string) (uintptr, error) {
	cString := append([]byte(name), 0)

	atom := c.fn.internAtom(c.display, unsafe.Pointer(&cString[0]), 0)
	if atom == 0 {
		return 0, errs.Newf(errs.CodeAccessibilityFailed, "the X server has no atom %q", name)
	}

	return atom, nil
}

// propertyRequest describes one property read.
type propertyRequest struct {
	// name is the property's atom name. It is only used in error messages, so a
	// failure says which property could not be read.
	name string
	// window is the window the property is read from.
	window uintptr
	// property is the property's atom.
	property uintptr
	// propertyType is the type the property must store, or atomAnyPropertyType to
	// accept whatever type it has.
	propertyType uintptr
	// format is the item width in bits the property must store, or 0 to accept
	// whatever width it has.
	format int32
}

// propertyValue is the result of one property read.
type propertyValue struct {
	// propertyType is the atom the property is stored with. It is how a title says
	// whether it is UTF-8 or Latin-1.
	propertyType uintptr
	// format is the item width in bits the property stores, or zero when the
	// property is absent or stores a type the read did not name.
	format int32
	// items is the number of items the property holds.
	items int
	// remaining is how many bytes of the property were not read because it is
	// longer than maxPropertyItems.
	remaining int
	// data is the property's content, copied out of Xlib's buffer. Format 32 keeps
	// Xlib's widening of the server's 32-bit items to the client's long, so there
	// are 8 bytes per item on a 64-bit build and 4 on a 32-bit one; formats 8 and
	// 16 keep their packed widths.
	data []byte
}

// absent reports whether the read found no value: the property does not exist, it
// is empty, or it stores a type other than the one the read named, which Xlib
// reports as no items rather than as a failure.
func (v propertyValue) absent() bool {
	return v.items == 0 || v.format == 0
}

// truncated reports whether the property holds more data than one read returns, so
// data is only its beginning.
func (v propertyValue) truncated() bool {
	return v.remaining > 0
}

// longs returns the property's items as the unsigned longs they are stored as:
// atoms, window ids and cardinal values all travel one long per item.
//
// The result is nil for an absent value and for a property that is not stored in
// 32-bit items, which only a read that accepted any format can produce.
func (v propertyValue) longs() []uintptr {
	if v.absent() || v.format != 32 {
		return nil
	}

	values := make([]uintptr, v.items)
	for index := range values {
		values[index] = readLong(v.data, index)
	}

	return values
}

// text returns a format-8 property's bytes as a string.
func (v propertyValue) text() string {
	if v.absent() || v.format != 8 {
		return ""
	}

	return string(v.data)
}

// readLong reads one unsigned long out of a property buffer.
func readLong(data []byte, index int) uintptr {
	offset := index * longSize

	if longSize == 8 {
		return uintptr(binary.NativeEndian.Uint64(data[offset:]))
	}

	return uintptr(binary.NativeEndian.Uint32(data[offset:]))
}

// getProperty reads one window property.
//
// offset and length are C longs — pointer-sized, and therefore uintptr — and length
// counts 32-bit units, not bytes. Xlib allocates the returned buffer and the client
// frees it with XFree; the bytes this function returns are a copy, so nothing points
// into Xlib's memory once it returns.
//
// A property stored in a format the request did not name is rejected rather than
// reinterpreted. EWMH fixes each property's type and format, so a difference means
// the window is not publishing what it claims, and decoding its bytes at the wrong
// width would produce window ids and pids assembled out of the middle of the data.
func (c *x11Conn) getProperty(request propertyRequest) (propertyValue, error) {
	var (
		actualType   uintptr
		actualFormat int32
		items        uintptr
		bytesAfter   uintptr
		buffer       unsafe.Pointer
	)

	status := c.fn.getWindowProperty(
		c.display,
		request.window,
		request.property,
		0,
		maxPropertyItems,
		0, // read without deleting the property
		request.propertyType,
		&actualType,
		&actualFormat,
		&items,
		&bytesAfter,
		&buffer,
	)

	if status != xStatusSuccess {
		return propertyValue{}, errs.Newf(
			errs.CodeAccessibilityFailed,
			"could not read the %s property of window %s",
			request.name,
			formatWindowID(request.window),
		)
	}

	// Xlib allocates the buffer even for a property it returns no items for; only a
	// null pointer means it allocated nothing.
	if buffer != nil {
		defer c.fn.free(buffer)
	}

	// No items, or a zero format, is how Xlib reports a property that is absent or
	// stored in a type the request did not name. Neither is a failure: EWMH makes
	// most of these properties optional, and the caller decides what its absence
	// means.
	if items == 0 || actualFormat == 0 {
		return propertyValue{propertyType: actualType}, nil
	}

	value := propertyValue{
		propertyType: actualType,
		format:       actualFormat,
		items:        int(items),
		remaining:    int(bytesAfter),
	}

	if request.format != 0 && value.format != request.format {
		return propertyValue{}, errs.Newf(
			errs.CodeAccessibilityFailed,
			"the %s property of window %s is stored in %d-bit items, not the %d-bit ones this backend reads",
			request.name,
			formatWindowID(request.window),
			value.format,
			request.format,
		)
	}

	// Xlib widens a format-32 property's items to one long each, so its buffer is
	// items*sizeof(long) bytes even though the server calls those items 32-bit. A
	// format that is already byte-packed keeps its packed width. Sizing this from
	// the format alone would copy half of a format-32 property on a 64-bit build
	// and leave readLong reading past the copy.
	size := value.items * propertyItemWidth(value.format)
	value.data = make([]byte, size)
	copy(value.data, propertyBytes(buffer, size))

	return value, nil
}

// propertyItemWidth is how many bytes one item of a property occupies in the buffer
// Xlib returns, which is not always the width the server names.
//
// Xlib converts a format-32 property to the client's C long, because that is the
// type the protocol's 32-bit items are widened to in the client library (atoms,
// window ids and cardinals are all read as longs). Formats 8 and 16 are not widened:
// their items stay packed in the buffer the server sent.
func propertyItemWidth(format int32) int {
	if format == 32 {
		return longSize
	}

	return int(format) / 8
}

// propertyBytes views the buffer Xlib allocated as bytes.
//
// The address arrives as an unsafe.Pointer because that is how Xlib's
// unsigned char** out parameter is bound, so the slice is built from a pointer
// rather than from a uintptr. The memory is Xlib's: it is not Go memory, it does not
// move, and it lives until XFree. Copying out of it is the only way to keep a
// property's content past the free, and the copy is sized from the item count the
// server reported rather than from a terminator, because property data is not
// NUL-terminated.
func propertyBytes(buffer unsafe.Pointer, length int) []byte {
	if length == 0 || buffer == nil {
		return nil
	}

	return unsafe.Slice((*byte)(buffer), length)
}

// activeWindow returns the window the window manager considers active, or 0 when
// nothing is active.
func (c *x11Conn) activeWindow() (uintptr, error) {
	value, err := c.getProperty(propertyRequest{
		name:         "_NET_ACTIVE_WINDOW",
		window:       c.root,
		property:     c.atoms.propActiveWindow,
		propertyType: c.atoms.typeWindow,
		format:       32,
	})
	if err != nil {
		return 0, err
	}

	active := value.longs()
	if len(active) == 0 {
		return 0, nil
	}

	return active[0], nil
}

// clientList returns the windows the window manager manages, front to back.
//
// The stacking order comes from _NET_CLIENT_LIST_STACKING, which EWMH defines bottom
// to top, so it is reversed into the contract's front-to-back order. The property is
// only recommended and a window manager may not publish it, so _NET_CLIENT_LIST is
// the fallback: its order is the initial mapping order, the order the windows first
// appeared in. That is a weaker answer, and moving the focused window to the front
// is the part of the order a caller can rely on either way.
func (c *x11Conn) clientList() ([]uintptr, error) {
	stacking, err := c.windowList("_NET_CLIENT_LIST_STACKING", c.atoms.propClientListStacking)
	if err != nil {
		return nil, err
	}

	if len(stacking) > 0 {
		slices.Reverse(stacking)

		return stacking, nil
	}

	return c.windowList("_NET_CLIENT_LIST", c.atoms.propClientList)
}

// windowList reads one WINDOW[] property from the root window.
//
// A list longer than the read cap is refused rather than truncated: the windows past
// the cap would disappear from the desktop without a trace, and a caller that had a
// silently short list could not tell. 65536 windows is far more than any session
// has, so the refusal never fires in practice.
func (c *x11Conn) windowList(name string, property uintptr) ([]uintptr, error) {
	value, err := c.getProperty(propertyRequest{
		name:         name,
		window:       c.root,
		property:     property,
		propertyType: c.atoms.typeWindow,
		format:       32,
	})
	if err != nil {
		return nil, err
	}

	if value.truncated() {
		return nil, errs.Newf(
			errs.CodeAccessibilityFailed,
			"the window manager's %s property holds more windows than the %d this backend reads in one request",
			name,
			maxPropertyItems,
		)
	}

	return value.longs(), nil
}

// windowTypes returns a window's _NET_WM_WINDOW_TYPE atoms.
//
// The property is optional, and a window that does not set it is an ordinary
// application window, so an absent property is an empty list rather than an error.
// A window may list several types; showsWindow decides what the set means.
func (c *x11Conn) windowTypes(win uintptr) ([]uintptr, error) {
	value, err := c.getProperty(propertyRequest{
		name:         "_NET_WM_WINDOW_TYPE",
		window:       win,
		property:     c.atoms.propWindowType,
		propertyType: c.atoms.typeAtom,
		format:       32,
	})
	if err != nil {
		return nil, err
	}

	return value.longs(), nil
}

// windowStates returns a window's _NET_WM_STATE atoms.
//
// The states are what the window manager did to the window — maximized, fullscreen,
// shaded, hidden — and the property is optional: a window that is none of those has
// an empty list.
func (c *x11Conn) windowStates(win uintptr) ([]uintptr, error) {
	value, err := c.getProperty(propertyRequest{
		name:         "_NET_WM_STATE",
		window:       win,
		property:     c.atoms.propWindowState,
		propertyType: c.atoms.typeAtom,
		format:       32,
	})
	if err != nil {
		return nil, err
	}

	return value.longs(), nil
}

// showsWindow reports whether a window with these _NET_WM_WINDOW_TYPE atoms is one
// this backend lists.
//
// Normal and dialog windows are the windows a user switches between; the types in
// skippedWindowTypes are pieces of a desktop or of another window. A window with no
// type is listed, because the property is optional and a toolkit that never sets it
// is still showing an application window. A type this backend does not know is not
// listed: that is a surface a toolkit or a window manager invented a label for, and
// offering it as an application window would be a guess rather than a report.
func (c *x11Conn) showsWindow(types []uintptr) bool {
	if len(types) == 0 {
		return true
	}

	// The skip list wins over the keep list, so a window that carries both — a
	// toolkit can be generous with the types it sets — is treated as the surface it
	// says it is, not as a document window.
	for _, windowType := range types {
		if slices.Contains(c.atoms.skippedWindowTypes, windowType) {
			return false
		}
	}

	for _, windowType := range types {
		if windowType == c.atoms.windowTypeNormal || windowType == c.atoms.windowTypeDialog {
			return true
		}
	}

	return false
}

// isMinimized reports whether a window's _NET_WM_STATE list says it is minimized.
//
// EWMH calls that state HIDDEN: the window is unmapped, whether because the user
// minimized it or because the window manager put it aside, and either way its frame
// no longer describes anything on screen.
func (c *x11Conn) isMinimized(states []uintptr) bool {
	return slices.Contains(states, c.atoms.stateHidden)
}

// windowTitle returns the text a window shows in its title bar.
//
// _NET_WM_NAME is the property a modern toolkit maintains, and EWMH fixes its type
// to UTF8_STRING. WM_NAME is the ICCCM property it replaced, and a window that never
// set the new one still carries it: its STRING type is Latin-1, so it is decoded one
// byte per code point instead of being read as UTF-8. COMPOUND_TEXT is the third form
// WM_NAME may take and this backend does not decode it; such a window reports no
// title rather than a mis-decoded one, which keeps mojibake out of the list at the
// cost of a window that is then dropped as untitled.
func (c *x11Conn) windowTitle(win uintptr) (string, error) {
	modern, err := c.getProperty(propertyRequest{
		name:         "_NET_WM_NAME",
		window:       win,
		property:     c.atoms.propWMName,
		propertyType: c.atoms.typeUTF8String,
		format:       8,
	})
	if err != nil {
		return "", err
	}

	if !modern.absent() {
		return strings.TrimSpace(modern.text()), nil
	}

	legacy, err := c.getProperty(propertyRequest{
		name:         "WM_NAME",
		window:       win,
		property:     c.atoms.propICCCMWMName,
		propertyType: atomAnyPropertyType,
		format:       8,
	})
	if err != nil {
		return "", err
	}

	if legacy.absent() {
		return "", nil
	}

	switch legacy.propertyType {
	case c.atoms.typeUTF8String:
		return strings.TrimSpace(legacy.text()), nil
	case c.atoms.typeString:
		return strings.TrimSpace(latin1String(legacy.data)), nil
	default:
		return "", nil
	}
}

// latin1String decodes an ISO-8859-1 byte string.
//
// WM_NAME's STRING type is Latin-1, not UTF-8: one byte per code point up to
// U+00FF. Decoding those bytes as UTF-8 would turn every accented character into a
// replacement character, so each byte is widened into the code point it names.
func latin1String(data []byte) string {
	var text strings.Builder

	text.Grow(len(data))

	for _, b := range data {
		text.WriteRune(rune(b))
	}

	return text.String()
}

// windowPID returns the process that owns a window, or 0 when the window carries no
// _NET_WM_PID.
//
// The property is optional, and it is only meaningful when the client runs on the
// same host as the X server: a client forwarded over the network sets the pid of a
// process on the other machine. A zero pid costs the application name and nothing
// else, so an absent property is not an error.
func (c *x11Conn) windowPID(win uintptr) (int, error) {
	value, err := c.getProperty(propertyRequest{
		name:         "_NET_WM_PID",
		window:       win,
		property:     c.atoms.propPID,
		propertyType: c.atoms.typeCardinal,
		format:       32,
	})
	if err != nil {
		return 0, err
	}

	pids := value.longs()
	if len(pids) == 0 {
		return 0, nil
	}

	return int(pids[0]), nil
}

// xWindowAttributes mirrors Xlib's XWindowAttributes, which XGetWindowAttributes
// fills in whole.
//
// The mirror is field for field rather than a buffer with spare room, because the
// map state this backend reads sits in the middle of the structure and its offset
// differs between the ABIs. Go lays these fields out exactly as C does: a uintptr
// and an unsafe.Pointer are 8 bytes on LP64 and 4 on ILP32, which is the width and
// alignment of C's unsigned long and pointer, and the int32 fields are C's int. So
// the structure is 136 bytes on a 64-bit build and 92 on a 32-bit one, and every
// field lands where Xlib writes it.
type xWindowAttributes struct {
	x, y               int32
	width, height      int32
	borderWidth        int32
	depth              int32
	visual             unsafe.Pointer
	root               uintptr
	class              int32
	bitGravity         int32
	winGravity         int32
	backingStore       int32
	backingPlanes      uintptr
	backingPixel       uintptr
	saveUnder          int32
	colormap           uintptr
	mapInstalled       int32
	mapState           int32
	allEventMasks      uintptr
	yourEventMask      uintptr
	doNotPropagateMask uintptr
	overrideRedirect   int32
	screen             unsafe.Pointer
}

// isViewable is IsViewable, the map state a window is in while it is on screen. The
// other two states XGetWindowAttributes reports are IsUnmapped and IsUnviewable.
const isViewable = 2

// windowAttributes reads a window's attributes, and reports false when the window
// no longer exists.
func (c *x11Conn) windowAttributes(win uintptr) (xWindowAttributes, bool) {
	var attributes xWindowAttributes

	if c.fn.getWindowAttributes(c.display, win, unsafe.Pointer(&attributes)) == 0 {
		return xWindowAttributes{}, false
	}

	return attributes, true
}

// windowGeometry returns a window's client rectangle in root coordinates, and
// reports false when the window no longer exists.
//
// The rectangle is the client area, not the frame. X11 has no protocol that reports
// a window's decorations, because they are the window manager's own child windows
// inside the frame it reparented the client into; XGetWindowAttributes therefore
// gives the client's size, and XTranslateCoordinates gives where the client's origin
// is on screen. The two together are the rectangle in the global top-left-origin
// space the contract asks for, which is also the space a click has to be injected
// in, so an overlay drawn over these bounds lines up with the application's own
// content.
//
// XGetWindowAttributes' x and y are deliberately not used: they are the window's
// position inside its parent, and for a reparented window the parent is the window
// manager's frame, so they describe a position relative to decoration rather than a
// position on screen.
//
// A window whose origin cannot be translated is reported as gone. That is the
// honest answer for a window destroyed mid-enumeration, and for any other failure the
// alternative would be a rectangle in a coordinate space the caller cannot use.
func (c *x11Conn) windowGeometry(win uintptr) (image.Rectangle, bool) {
	attributes, ok := c.windowAttributes(win)
	if !ok {
		return image.Rectangle{}, false
	}

	var (
		x, y  int32
		child uintptr
	)

	if c.fn.translateCoordinates(c.display, win, c.root, 0, 0, &x, &y, &child) == 0 {
		return image.Rectangle{}, false
	}

	return image.Rect(
		int(x),
		int(y),
		int(x)+int(attributes.width),
		int(y)+int(attributes.height),
	), true
}

// focusWindow gives the keyboard to win, if the server would accept it.
//
// XSetInputFocus is a core-protocol request, and the server refuses it with BadMatch
// when the window is not viewable — which is exactly the state of a window that is
// minimized or on another desktop and that the window manager has not mapped yet, since
// EWMH makes those windows unmapped rather than merely out of sight. The request is
// therefore sent only to a viewable window; for the rest the _NET_ACTIVE_WINDOW message
// is the request, and the window manager focuses the window itself when it maps it. A
// protocol error here would be reported asynchronously to a handler this backend does
// not own, so it could not even be attributed to the activation that caused it.
func (c *x11Conn) focusWindow(win uintptr) {
	attributes, ok := c.windowAttributes(win)
	if !ok || attributes.mapState != isViewable {
		return
	}

	_ = c.fn.setInputFocus(c.display, win, revertToPointerRoot, currentTime)
}

// activateWindow asks the window manager to make win the active window.
//
// The request is the EWMH one: a _NET_ACTIVE_WINDOW client message sent to the root
// window, which only the window manager receives because it is the client that
// redirected substructure requests on the root. data.l[0] is the source indication —
// a pager, so the manager treats the request the way it treats a taskbar click and
// deiconifies the window — data.l[1] a timestamp, CurrentTime, and data.l[2] the
// window that is active now, which a client that has not checked leaves at zero.
//
// XRaiseWindow and XSetInputFocus follow the message rather than replace it. The
// message is a request, and the window manager is free to stack and focus the window
// itself; the direct calls are what still produces a usable window under a manager
// that ignores or has not yet handled the message. XFlush is what puts all three on
// the wire, and Activate then verifies the result instead of trusting it.
func (c *x11Conn) activateWindow(win uintptr) error {
	event := newClientMessageEvent(
		c.display,
		c.root,
		c.atoms.propActiveWindow,
		[5]uint64{sourcePager, currentTime, 0, 0, 0},
	)

	if c.fn.sendEvent(c.display, c.root, 0, substructureMasks, unsafe.Pointer(event)) == 0 {
		return errs.Newf(
			errs.CodeAccessibilityFailed,
			"the X server rejected the activation request for window %s",
			formatWindowID(win),
		)
	}

	_ = c.fn.raiseWindow(c.display, win)

	c.focusWindow(win)

	if c.fn.flush(c.display) == 0 {
		return errs.New(errs.CodeAccessibilityFailed, "could not flush the X connection")
	}

	return nil
}

// moveResizeWindow asks the window manager to give win the requested client
// rectangle.
//
// The request is the EWMH one: a _NET_MOVERESIZE_WINDOW client message to the
// root window, with every geometry field flagged as supplied. Using the window
// manager rather than XMoveResizeWindow is what keeps a reparenting or tiling
// manager's constraints in force: the manager may clamp, remap or refuse, and
// SetBounds treats a frame that never changes as the refusal it is.
func (c *x11Conn) moveResizeWindow(win uintptr, bounds image.Rectangle) error {
	event := newClientMessageEvent(
		c.display,
		c.root,
		c.atoms.propMoveResizeWindow,
		moveResizeData(bounds),
	)

	if c.fn.sendEvent(c.display, c.root, 0, substructureMasks, unsafe.Pointer(event)) == 0 {
		return errs.Newf(
			errs.CodeAccessibilityFailed,
			"the X server rejected the placement request for window %s",
			formatWindowID(win),
		)
	}

	if c.fn.flush(c.display) == 0 {
		return errs.New(errs.CodeAccessibilityFailed, "could not flush the X connection")
	}

	return nil
}

// moveResizeData builds the five longs of a _NET_MOVERESIZE_WINDOW message: the
// flags, then x, y, width and height.
//
// The coordinates are written as their 32-bit values. Xlib reads each field as a
// C long and the protocol packs the low 32 bits, so a negative origin keeps its
// two's-complement encoding on both ABIs.
func moveResizeData(bounds image.Rectangle) [5]uint64 {
	flags := uint64(moveResizeX|moveResizeY|moveResizeWidth|moveResizeHeight|gravityStatic) |
		uint64(sourcePager)<<moveResizeSource

	return [5]uint64{
		flags,
		uint64(uint32(bounds.Min.X)),
		uint64(uint32(bounds.Min.Y)),
		uint64(uint32(bounds.Dx())),
		uint64(uint32(bounds.Dy())),
	}
}

// newClientMessageEvent builds the XEvent a client message is sent in.
//
// The data array is five C longs, so each value is written one long apart even
// though the protocol carries 32 bits per field: Xlib reads data.l and converts each
// long to the 32-bit wire value.
func newClientMessageEvent(display, window, messageType uintptr, data [5]uint64) *xEvent {
	event := &xEvent{}

	putInt32(event[:], xEventOffsetType, xEventClientMessage)
	putInt32(event[:], xEventOffsetSendEvent, 1)
	putLong(event[:], xEventOffsetDisplay, uint64(display))
	putLong(event[:], xEventOffsetWindow, uint64(window))
	putLong(event[:], xEventOffsetMessageType, uint64(messageType))
	putInt32(event[:], xEventOffsetFormat, 32)

	for index, value := range data {
		putLong(event[:], xEventOffsetData+index*longSize, value)
	}

	return event
}

// putInt32 writes one C int at off.
func putInt32(buffer []byte, off int, value int32) {
	binary.NativeEndian.PutUint32(buffer[off:], uint32(value))
}

// putLong writes one C long at off, truncating a value that does not fit the 32-bit
// long of an ILP32 ABI.
func putLong(buffer []byte, off int, value uint64) {
	if longSize == 8 {
		binary.NativeEndian.PutUint64(buffer[off:], value)

		return
	}

	binary.NativeEndian.PutUint32(buffer[off:], uint32(value))
}
