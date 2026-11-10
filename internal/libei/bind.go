//go:build linux

package libei

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/internal/native"
)

// This file binds libei's public C ABI with purego, through the module's
// internal/native seam. The library is opened by soname at run time, so nothing
// is linked at build time and the module still builds without a C toolchain.
//
// # Structs are opaque pointers
//
// libei hands out pointer handles to structs ei, ei_seat, ei_device and
// ei_event and nothing in this package ever dereferences one. They cross the
// boundary as uintptr, which is what purego passes for a C pointer. That is also
// why the signatures below do not need layouts: only the argument widths matter,
// and they are the C ones (int -> int32, uint32_t -> uint32, uint64_t -> uint64,
// double -> float64, bool -> bool, char* -> string).
//
// # The variadic seat bind
//
// ei_seat_bind_capabilities() is the one variadic function in the set: it takes
// a seat and a NULL-terminated list of capability enums. purego cannot express a
// C variadic prototype, but it does expand a []any passed as the last argument
// into that many fixed arguments, which is exactly what a variadic call looks
// like at the ABI level (each enum occupies one integer argument slot). So the
// binding is declared as func(seat uintptr, capabilities []any) and called with
// the caps followed by uint32(0) as the sentinel.
//
// # Symbol set
//
// This is the set the reference libei_client.c uses: the context lifecycle
// (ei_new_sender, ei_configure_name, ei_setup_backend_fd, ei_get_fd, ei_unref),
// the event loop (ei_dispatch, ei_get_event, ei_event_*), the seat
// (ei_seat_ref/unref, ei_seat_bind_capabilities, ei_event_get_seat) and the
// device emitters (ei_device_*).
//
// ei_setup_backend_socket() is deliberately not bound: the RemoteDesktop portal
// hands over an already-connected EIS socket through ConnectToEIS, so the fd
// form is the only setup this client needs, and binding an unused symbol would
// only add a failure mode on an older libei. There is also no ei_connect() in
// libei's public API; connecting is ei_setup_backend_fd/socket on a context
// created by ei_new_sender.

// libeiFuncs is the bound symbol table. Each field is one C function; see the
// binding list in bindLibei for the symbol it belongs to.
type libeiFuncs struct {
	// struct ei *ei_new_sender(void *user_data)
	newSender func(userData uintptr) uintptr
	// struct ei *ei_unref(struct ei *ei) — closes the EIS fd on the last ref.
	unref func(ei uintptr) uintptr
	// void ei_configure_name(struct ei *ei, const char *name)
	configureName func(ei uintptr, name string)
	// int ei_setup_backend_fd(struct ei *ei, int fd) — 0 on success, and libei
	// owns the descriptor from this call on.
	setupBackendFD func(ei uintptr, fd int32) int32
	// int ei_get_fd(struct ei *ei) — the single socket all events arrive on.
	getFD func(ei uintptr) int32
	// void ei_dispatch(struct ei *ei)
	dispatch func(ei uintptr)
	// struct ei_event *ei_get_event(struct ei *ei) — NULL when the queue is empty.
	getEvent func(ei uintptr) uintptr
	// uint64_t ei_now(struct ei *ei) — CLOCK_MONOTONIC microseconds.
	now func(ei uintptr) uint64

	// enum ei_event_type ei_event_get_type(struct ei_event *event)
	eventGetType func(event uintptr) int32
	// struct ei_device *ei_event_get_device(struct ei_event *event)
	eventGetDevice func(event uintptr) uintptr
	// struct ei_seat *ei_event_get_seat(struct ei_event *event)
	eventGetSeat func(event uintptr) uintptr
	// struct ei_event *ei_event_unref(struct ei_event *event)
	eventUnref func(event uintptr) uintptr

	// struct ei_seat *ei_seat_ref(struct ei_seat *seat)
	seatRef func(seat uintptr) uintptr
	// struct ei_seat *ei_seat_unref(struct ei_seat *seat)
	seatUnref func(seat uintptr) uintptr
	// void ei_seat_bind_capabilities(struct ei_seat *seat, ...) — NULL sentinel.
	seatBindCapabilities func(seat uintptr, capabilities []any)

	// struct ei_device *ei_device_ref(struct ei_device *device)
	deviceRef func(device uintptr) uintptr
	// struct ei_device *ei_device_unref(struct ei_device *device)
	deviceUnref func(device uintptr) uintptr
	// bool ei_device_has_capability(struct ei_device *, enum ei_device_capability)
	deviceHasCapability func(device uintptr, capability uint32) bool
	// void ei_device_start_emulating(struct ei_device *device, uint32_t sequence)
	deviceStartEmulating func(device uintptr, sequence uint32)
	// void ei_device_stop_emulating(struct ei_device *device)
	deviceStopEmulating func(device uintptr)
	// void ei_device_frame(struct ei_device *device, uint64_t time)
	deviceFrame func(device uintptr, time uint64)
	// void ei_device_pointer_motion_absolute(struct ei_device *, double x, double y)
	pointerMotionAbsolute func(device uintptr, x, y float64)
	// void ei_device_pointer_motion(struct ei_device *, double x, double y)
	pointerMotion func(device uintptr, x, y float64)
	// void ei_device_button_button(struct ei_device *, uint32_t button, bool is_press)
	buttonButton func(device uintptr, button uint32, isPress bool)
	// void ei_device_scroll_delta(struct ei_device *, double x, double y)
	scrollDelta func(device uintptr, x, y float64)
	// void ei_device_keyboard_key(struct ei_device *, uint32_t keycode, bool is_press)
	keyboardKey func(device uintptr, keycode uint32, isPress bool)
}

// libeiLibraryNames are the sonames tried in order. libei.so.1 is the stable
// ABI soname every distribution ships; libei.so only exists with the -dev
// package and is the fallback for a hand-built install.
var libeiLibraryNames = [...]string{"libei.so.1", "libei.so"}

var (
	libeiOnce sync.Once
	libeiFn   *libeiFuncs
	libeiErr  error
)

// loadLibei binds libei once per process and remembers the outcome, so a
// missing library is not re-opened on every action. The handle is never closed:
// a library handle is cheap to keep and the whole point of binding once is that
// a second injection does not pay for a dlopen, the same policy the X11 input
// backend uses.
func loadLibei() (*libeiFuncs, error) {
	libeiOnce.Do(func() {
		libeiFn, libeiErr = bindLibei()
	})

	return libeiFn, libeiErr
}

// bindLibei opens libei and binds every symbol this backend calls.
func bindLibei() (*libeiFuncs, error) {
	library, err := native.Open(libeiLibraryNames[:]...)
	if err != nil {
		return nil, errs.Wrapf(err, errs.CodeNotSupported,
			"libei is not installed; install the libei runtime (Debian/Ubuntu: "+
				"libei1, Fedora: libei) so Wayland input injection can reach "+
				"compositors that implement no virtual-pointer protocol")
	}

	functions := &libeiFuncs{}

	for _, binding := range [...]struct {
		symbol string
		target any
	}{
		{"ei_new_sender", &functions.newSender},
		{"ei_unref", &functions.unref},
		{"ei_configure_name", &functions.configureName},
		{"ei_setup_backend_fd", &functions.setupBackendFD},
		{"ei_get_fd", &functions.getFD},
		{"ei_dispatch", &functions.dispatch},
		{"ei_get_event", &functions.getEvent},
		{"ei_now", &functions.now},
		{"ei_event_get_type", &functions.eventGetType},
		{"ei_event_get_device", &functions.eventGetDevice},
		{"ei_event_get_seat", &functions.eventGetSeat},
		{"ei_event_unref", &functions.eventUnref},
		{"ei_seat_ref", &functions.seatRef},
		{"ei_seat_unref", &functions.seatUnref},
		{"ei_seat_bind_capabilities", &functions.seatBindCapabilities},
		{"ei_device_ref", &functions.deviceRef},
		{"ei_device_unref", &functions.deviceUnref},
		{"ei_device_has_capability", &functions.deviceHasCapability},
		{"ei_device_start_emulating", &functions.deviceStartEmulating},
		{"ei_device_stop_emulating", &functions.deviceStopEmulating},
		{"ei_device_frame", &functions.deviceFrame},
		{"ei_device_pointer_motion_absolute", &functions.pointerMotionAbsolute},
		{"ei_device_pointer_motion", &functions.pointerMotion},
		{"ei_device_button_button", &functions.buttonButton},
		{"ei_device_scroll_delta", &functions.scrollDelta},
		{"ei_device_keyboard_key", &functions.keyboardKey},
	} {
		if err := library.Func(binding.symbol, binding.target); err != nil {
			return nil, errs.Wrapf(err, errs.CodeNotSupported,
				"libei is installed but does not export %s; install libei 1.0 "+
					"or newer", binding.symbol)
		}
	}

	return functions, nil
}

// Available reports whether libei.so.1 and a portal session are usable.
//
// It is a cheap probe with no side effects: libei has to load, and a session bus
// address has to be discoverable from the environment. It deliberately does not
// dial the bus or ask for a session, because either can block or show a consent
// dialog and Available is expected to be callable from a capability probe.
// Whether the compositor actually grants a RemoteDesktop session is only known
// once Dial has run; a false positive here therefore means "try Dial", not "this
// will work".
func Available() bool {
	if _, err := loadLibei(); err != nil {
		return false
	}

	return sessionBusConfigured()
}

// sessionBusConfigured reports whether the environment names a session bus,
// without connecting to it. DBUS_SESSION_BUS_ADDRESS is authoritative when set;
// otherwise the bus socket under XDG_RUNTIME_DIR is what the libdbus default
// address points at.
func sessionBusConfigured() bool {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return true
	}

	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		return false
	}

	info, err := os.Stat(filepath.Join(runtimeDir, "bus"))

	return err == nil && !info.IsDir()
}
