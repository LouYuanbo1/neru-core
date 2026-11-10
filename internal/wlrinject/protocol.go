package wlrinject

// Wayland object ids, opcodes and enum values this package uses. The numbers
// come from the core wayland.xml protocol, wlr-virtual-pointer-unstable-v1.xml
// and virtual-keyboard-unstable-v1.xml. Requests are sent by opcode rather than
// through generated bindings because the package binds a fixed, small set of
// objects.
const (
	// displayID is the wl_display singleton, the only object with a fixed id.
	displayID = 1

	displayOpSync        = 0 // wl_display.sync(new_id callback)
	displayOpGetRegistry = 1 // wl_display.get_registry(new_id registry)

	displayEventError    = 0 // object id, code, message
	displayEventDeleteID = 1 // id the server is done with

	// wl_registry has a single bind request and no destructor: the registry
	// object lives until the connection does.
	registryOpBind = 0 // name, interface, version, new_id

	registryEventGlobal = 0 // name, interface, version

	callbackEventDone = 0 // serial

	seatOpRelease = 3 // wl_seat.release, since version 5

	seatEventCapabilities = 0 // capabilities
	seatEventName         = 1 // name (version 2+)

	// wl_seat capability bits. Only the pointer bit affects this client: it is
	// how the seat the virtual pointer is suggested to is chosen.
	seatCapabilityPointer = 1 << 0

	outputEventGeometry = 0 // x, y, physical width/height, subpixel, make, model, transform
	outputEventMode     = 1 // flags, width, height, refresh
	outputEventDone     = 2 // version 2+
	outputEventScale    = 3 // factor, version 2+

	// outputModeCurrent marks the mode currently driving the output. An output
	// may advertise several modes; only the current one is its size.
	outputModeCurrent = 1

	// Virtual pointer (zwlr_virtual_pointer_manager_v1 and
	// zwlr_virtual_pointer_v1). The manager's create request is the only one
	// needed for this client; the version 2 output-specific variant is unused.
	virtualPointerManagerOpCreate  = 0 // seat (nullable), new_id
	virtualPointerManagerOpDestroy = 1

	virtualPointerOpMotionAbsolute = 1 // time, x, y, x_extent, y_extent
	virtualPointerOpButton         = 2 // time, button, state
	virtualPointerOpAxis           = 3 // time, axis, fixed value
	virtualPointerOpFrame          = 4 // no arguments
	virtualPointerOpAxisSource     = 5 // axis source
	virtualPointerOpAxisDiscrete   = 7 // time, axis, fixed value, int discrete
	virtualPointerOpDestroy        = 8

	// wl_pointer.axis values.
	pointerAxisVertical   = 0
	pointerAxisHorizontal = 1

	// wl_pointer.axis_source values.
	pointerAxisSourceWheel      = 0
	pointerAxisSourceContinuous = 2

	// wl_pointer.button_state values.
	pointerButtonReleased = 0
	pointerButtonPressed  = 1

	// Virtual keyboard (zwp_virtual_keyboard_manager_v1 and
	// zwp_virtual_keyboard_v1). The manager interface has no destroy request.
	virtualKeyboardManagerOpCreate = 0 // seat, new_id

	virtualKeyboardOpKeymap    = 0 // format, fd, size
	virtualKeyboardOpKey       = 1 // time, key, state
	virtualKeyboardOpModifiers = 2 // depressed, latched, locked, group
	virtualKeyboardOpDestroy   = 3

	// wl_keyboard.keymap_format value for an XKB v1 text keymap.
	keyboardKeymapFormatXKBV1 = 1

	// wl_keyboard.key_state values.
	keyboardKeyStateReleased = 0
	keyboardKeyStatePressed  = 1
)

// Interface names as they appear in wl_registry.global events.
const (
	interfaceWLSeat                 = "wl_seat"
	interfaceWLOutput               = "wl_output"
	interfaceVirtualPointerManager  = "zwlr_virtual_pointer_manager_v1"
	interfaceVirtualKeyboardManager = "zwp_virtual_keyboard_manager_v1"
)

// Object versions this package binds.
const (
	// seatVersionCap is wl_seat 5: it carries capabilities and the release
	// destructor, and nothing newer is needed to create the virtual devices.
	seatVersionCap = 5

	// pointerManagerVersionCap is the highest zwlr_virtual_pointer_manager_v1
	// version this package understands. Version 1 alone is enough for
	// create_virtual_pointer; version 2 only adds the output-specific variant.
	pointerManagerVersionCap = 2

	// keyboardManagerVersionCap is the only version
	// zwp_virtual_keyboard_manager_v1 has.
	keyboardManagerVersionCap = 1

	// outputVersionCap is wl_output 3: geometry, mode, scale and done, without
	// the version 4 name and description events.
	outputVersionCap = 3
)

// axisValue120Threshold is the wl_seat version at which the compositor's
// wl_pointer carries the axis_value120 (high-resolution scroll) event. The
// version tracks the child wl_pointer resource the compositor creates, so a
// seat advertising 8 or later can deliver value120 to its clients.
const axisValue120Threshold = 8

// registryGlobal is one wl_registry.global event.
type registryGlobal struct {
	name    uint32
	iface   string
	version uint32
}

// parseRegistryGlobal decodes the arguments of a wl_registry.global event.
func parseRegistryGlobal(body []byte) (registryGlobal, error) {
	d := decoder{buf: body}

	name, err := d.uint32()
	if err != nil {
		return registryGlobal{}, err
	}

	iface, err := d.string()
	if err != nil {
		return registryGlobal{}, err
	}

	version, err := d.uint32()
	if err != nil {
		return registryGlobal{}, err
	}

	return registryGlobal{name: name, iface: iface, version: version}, nil
}
