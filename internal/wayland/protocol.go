//go:build linux

package wayland

// Wayland object ids, opcodes and event codes this package uses. The numbers
// come from the core wayland.xml protocol and from
// wlr-screencopy-unstable-v1.xml (both shipped by the compositor or by the
// wlroots tree). Every request is sent by opcode, so they are spelled out
// rather than generated from XML: the package binds a fixed, small set of
// objects.
const (
	// displayID is the wl_display singleton, the only object with a fixed id.
	displayID = 1

	displayOpSync        = 0 // wl_display.sync(new_id callback)
	displayOpGetRegistry = 1 // wl_display.get_registry(new_id registry)

	displayEventError    = 0 // object id, code, message
	displayEventDeleteID = 1 // id the server is done with

	registryOpBind = 0 // name, interface, version, new_id

	registryEventGlobal       = 0 // name, interface, version
	registryEventGlobalRemove = 1 // name

	callbackEventDone = 0 // serial

	shmOpCreatePool = 0 // new_id pool, fd, size
	shmEventFormat  = 0 // format

	shmPoolOpCreateBuffer = 0 // new_id buffer, offset, width, height, stride, format
	shmPoolOpDestroy      = 1

	bufferOpDestroy = 0

	outputEventGeometry = 0 // x, y, physical width/height, subpixel, make, model, transform
	outputEventMode     = 1 // flags, width, height, refresh
	outputEventDone     = 2
	outputEventScale    = 3 // factor

	// outputModeCurrent marks the mode currently driving the output. An output
	// may advertise several modes; only the current one is its size.
	outputModeCurrent = 1

	// outputTransformNormal is wl_output_transform 0. Any other value means the
	// compositor rotates the output before it reaches the desktop.
	outputTransformNormal = 0

	screencopyOpCaptureOutput = 0 // new_id frame, overlay_cursor, wl_output
	screencopyOpDestroy       = 2

	frameEventBuffer      = 0 // format, width, height, stride
	frameEventFlags       = 1 // flags
	frameEventReady       = 2 // tv_sec_hi, tv_sec_lo, tv_nsec
	frameEventFailed      = 3
	frameEventDamage      = 4 // x, y, width, height (version 2+)
	frameEventLinuxDMABuf = 5 // format, width, height (version 3+)
	frameEventBufferDone  = 6 // version 3+

	frameOpCopy    = 0 // buffer
	frameOpDestroy = 1

	// frameFlagYInvert means the frame's rows are stored bottom-up.
	frameFlagYInvert = 1

	// wl_shm format codes from the core protocol's enum. Both are packed 32-bit
	// pixels; on the little-endian machines this package targets the bytes in
	// memory are B, G, R, A.
	shmFormatARGB8888 = 0
	shmFormatXRGB8888 = 1
)

// Interface names as they appear in wl_registry.global events.
const (
	interfaceWLShm      = "wl_shm"
	interfaceWLOutput   = "wl_output"
	interfaceScreencopy = "zwlr_screencopy_manager_v1"
)

// Object versions this package binds. Version 1 of wl_shm is all a pool needs.
// wl_output 3 carries geometry/mode/done/scale without the version 4 name and
// description events. The screencopy manager is bound as high as version 3 so
// buffer_done and linux_dmabuf are reported, which is what lets a dmabuf-only
// compositor be diagnosed instead of timing out.
const (
	shmVersion               = 1
	outputVersion            = 3
	screencopyManagerVersion = 3
)

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
