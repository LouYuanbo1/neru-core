//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/LouYuanbo1/neru-core/errs"
)

// This file is the AT-SPI half of the backend: the D-Bus plumbing and the
// primitive reads (role, state, extents, text, children) that the tree walk in
// scan.go composes. AT-SPI is a D-Bus interface; every element reference is a
// (bus name, object path) pair returned by the parent, so the walk addresses
// each read with the pair it was handed.

// Well-known names and object paths of the accessibility bus and its registry.
const (
	// atspiBusDestination is the session-bus service that reports where the
	// accessibility bus listens. It is D-Bus activatable, so asking for it starts
	// at-spi-bus-launcher on a session that has at-spi2-core installed.
	atspiBusDestination = "org.a11y.Bus"
	// atspiBusPath is the object that carries GetAddress.
	atspiBusPath dbus.ObjectPath = "/org/a11y/bus"
	// atspiBusInterface carries GetAddress.
	atspiBusInterface = "org.a11y.Bus"

	// atspiStatusInterface is the session-bus interface holding the
	// assistive-technology flags toolkits check before building a tree.
	atspiStatusInterface = "org.a11y.Status"
	// atspiStatusIsEnabled is set while assistive technology is on.
	atspiStatusIsEnabled = "IsEnabled"
	// atspiStatusScreenReader is set while a screen reader is running. Toolkits
	// treat either flag as a reason to expose their tree.
	atspiStatusScreenReader = "ScreenReaderEnabled"

	// atspiRegistryDestination is the well-known name every application
	// registers its root accessible under.
	atspiRegistryDestination = "org.a11y.atspi.Registry"
	// atspiRegistryRootPath is the registry's root accessible, whose children are
	// the applications on the bus.
	atspiRegistryRootPath dbus.ObjectPath = "/org/a11y/atspi/accessible/root"
	// atspiNullPath is what AT-SPI returns for "there is no accessible here", in
	// place of an empty reference.
	atspiNullPath dbus.ObjectPath = "/org/a11y/atspi/null"
)

// Interfaces used by this backend, and the callable members assembled from them.
// The strings are spelled out because this module carries no generated D-Bus
// bindings.
const (
	ifaceAccessible  = "org.a11y.atspi.Accessible"
	ifaceApplication = "org.a11y.atspi.Application"
	ifaceComponent   = "org.a11y.atspi.Component"
	ifaceText        = "org.a11y.atspi.Text"
	ifaceProperties  = "org.freedesktop.DBus.Properties"

	methodGetAddress      = atspiBusInterface + ".GetAddress"
	methodPropertiesGet   = ifaceProperties + ".Get"
	methodPropertiesSet   = ifaceProperties + ".Set"
	methodGetChildren     = ifaceAccessible + ".GetChildren"
	methodGetChildAtIndex = ifaceAccessible + ".GetChildAtIndex"
	methodGetRoleName     = ifaceAccessible + ".GetRoleName"
	methodGetState        = ifaceAccessible + ".GetState"
	methodGetExtents      = ifaceComponent + ".GetExtents"
	methodGetText         = ifaceText + ".GetText"
)

// Property names read through org.freedesktop.DBus.Properties.
const (
	propertyName         = "Name"
	propertyDescription  = "Description"
	propertyAccessibleID = "AccessibleId"
	propertyChildCount   = "ChildCount"
	// propertyApplicationID is the owning process id on
	// org.a11y.atspi.Application. It was added after the rest of the interface, so
	// a read of it may fail on an older at-spi2-core.
	propertyApplicationID = "Id"
)

// The two timeouts every AT-SPI read is bounded by. A call timeout keeps one
// wedged application from stalling a scan: a healthy toolkit answers in
// milliseconds, so tripping it means the application is not responding and the
// walk should move on. The dial timeout bounds the bus lookup, which can involve
// D-Bus activating at-spi-bus-launcher.
const (
	atspiCallTimeout = 500 * time.Millisecond
	atspiDialTimeout = 5 * time.Second
)

// accessible is one object on the accessibility bus: the unique bus name of the
// application connection that owns it, and its object path there. AT-SPI hands
// this pair out everywhere a reference to an element is needed, and every
// property read or method call is addressed with it.
type accessible struct {
	bus  string
	path dbus.ObjectPath
}

// valid reports whether the reference addresses a real element. AT-SPI returns a
// null reference, or one with an empty bus name, for "no accessible" rather than
// an empty reply.
func (a accessible) valid() bool {
	return a.bus != "" && a.path != "" && a.path != atspiNullPath
}

// String makes a reference printable, for error messages and node ids.
func (a accessible) String() string {
	return string(a.path) + "@" + a.bus
}

// registryRoot is the root of the whole accessibility tree.
func registryRoot() accessible {
	return accessible{bus: atspiRegistryDestination, path: atspiRegistryRootPath}
}

// rawAccessible mirrors the (so) struct AT-SPI returns for an element
// reference. godbus matches fields by position, so the order is the wire order.
type rawAccessible struct {
	Name string
	Path dbus.ObjectPath
}

// rawExtents mirrors the (iiii) struct Component.GetExtents returns.
type rawExtents struct {
	X      int32
	Y      int32
	Width  int32
	Height int32
}

// atspiSession returns the client's accessibility bus connection, opening it on
// first use.
//
// It is called on every tree operation, so it holds the connection open for the
// client's lifetime and only reconnects when godbus reports the connection gone.
// Close closes it, and any later call reports the client as closed rather than
// reconnecting, so a closed client cannot silently start using the bus again.
func (c *Client) atspiSession(ctx context.Context) (*dbus.Conn, error) {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	if c.closed.Load() {
		return nil, errClientClosed()
	}

	if c.conn != nil && c.conn.Connected() {
		return c.conn, nil
	}

	// Enabling accessibility is best effort. The flags ask toolkits to build a
	// tree, but an application that already has one is readable without them, and
	// a session where org.a11y.Status cannot be written (a locked-down bus policy)
	// still has a working accessibility bus. Failing the whole scan here would
	// throw away the trees that do exist.
	_ = c.enableAccessibility(ctx)

	address, err := accessibilityBusAddress(ctx)
	if err != nil {
		return nil, err
	}

	conn, err := dbus.Connect(address)
	if err != nil {
		return nil, errs.Wrap(
			err,
			errs.CodeAccessibilityFailed,
			"could not connect to the AT-SPI accessibility bus",
		)
	}

	c.conn = conn

	return conn, nil
}

// accessibilityBusAddress asks org.a11y.Bus where the accessibility bus listens.
// The lookup runs on a private session-bus connection that is closed straight
// away, so this package never holds the process-wide shared session connection
// open on its own account.
func accessibilityBusAddress(ctx context.Context) (string, error) {
	session, err := dbus.SessionBusPrivate()
	if err != nil {
		return "", errs.Wrap(
			err,
			errs.CodeAccessibilityFailed,
			"could not connect to the D-Bus session bus",
		)
	}
	defer session.Close()

	callCtx, cancel := context.WithTimeout(ctx, atspiDialTimeout)
	defer cancel()

	var address string

	err = session.Object(atspiBusDestination, atspiBusPath).
		CallWithContext(callCtx, methodGetAddress, 0).Store(&address)
	if err != nil {
		return "", errs.Wrap(
			err,
			errs.CodeAccessibilityFailed,
			"could not read the accessibility bus address from org.a11y.Bus; "+
				"is at-spi2-core installed and running on this session",
		)
	}

	if address == "" {
		return "", errs.New(
			errs.CodeAccessibilityFailed,
			"org.a11y.Bus reported an empty accessibility bus address",
		)
	}

	return address, nil
}

// enableAccessibility turns the session-wide assistive-technology flags on and
// remembers the values it replaced.
//
// Toolkits build their accessibility tree only while one of those flags is set,
// so a scan of a freshly booted desktop finds nothing until it is done. The
// previous values are kept for Close to restore, so a client that shuts down
// leaves the session as it found it. That matters because the flags are
// session-wide: another assistive tool may have been the one that set them.
func (c *Client) enableAccessibility(ctx context.Context) error {
	if c.a11yEnabled {
		return nil
	}

	isEnabled, screenReader, err := readA11yStatus(ctx)
	if err != nil {
		return err
	}

	// Enabling sets IsEnabled before ScreenReaderEnabled, so there is no window
	// in which a toolkit sees a screen reader with accessibility switched off.
	if err := setA11yStatus(ctx, atspiStatusIsEnabled, true); err != nil {
		return err
	}

	if err := setA11yStatus(ctx, atspiStatusScreenReader, true); err != nil {
		// Best effort rollback, so a half-enabled session is not left behind.
		_ = setA11yStatus(ctx, atspiStatusIsEnabled, isEnabled)

		return err
	}

	c.savedIsEnabled = isEnabled
	c.savedScreenReader = screenReader
	c.a11yEnabled = true

	return nil
}

// restoreAccessibility puts the assistive-technology flags back the way they
// were before this client turned them on. It is a no-op when the client never
// enabled them, so Close of a client that never read a tree touches nothing.
func (c *Client) restoreAccessibility() error {
	if !c.a11yEnabled {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), atspiDialTimeout)
	defer cancel()

	// Disabling clears ScreenReaderEnabled before IsEnabled, the reverse of
	// enabling, so a toolkit never sees a screen reader with accessibility off.
	var failures []error

	if err := setA11yStatus(ctx, atspiStatusScreenReader, c.savedScreenReader); err != nil {
		failures = append(failures, err)
	}

	if err := setA11yStatus(ctx, atspiStatusIsEnabled, c.savedIsEnabled); err != nil {
		failures = append(failures, err)
	}

	c.a11yEnabled = false

	return errors.Join(failures...)
}

// readA11yStatus reads the two assistive-technology flags over one session-bus
// connection.
func readA11yStatus(ctx context.Context) (bool, bool, error) {
	session, err := dbus.SessionBusPrivate()
	if err != nil {
		return false, false, errs.Wrap(
			err,
			errs.CodeAccessibilityFailed,
			"could not connect to the D-Bus session bus",
		)
	}
	defer session.Close()

	read := func(property string) (bool, error) {
		callCtx, cancel := context.WithTimeout(ctx, atspiDialTimeout)
		defer cancel()

		var value dbus.Variant

		err := session.Object(atspiBusDestination, atspiBusPath).CallWithContext(
			callCtx, methodPropertiesGet, 0, atspiStatusInterface, property,
		).Store(&value)
		if err != nil {
			return false, errs.Wrapf(
				err,
				errs.CodeAccessibilityFailed,
				"could not read %s.%s", atspiStatusInterface, property,
			)
		}

		flag, _ := value.Value().(bool)

		return flag, nil
	}

	isEnabled, err := read(atspiStatusIsEnabled)
	if err != nil {
		return false, false, err
	}

	screenReader, err := read(atspiStatusScreenReader)
	if err != nil {
		return false, false, err
	}

	return isEnabled, screenReader, nil
}

// setA11yStatus writes one org.a11y.Status property.
func setA11yStatus(ctx context.Context, property string, value bool) error {
	session, err := dbus.SessionBusPrivate()
	if err != nil {
		return errs.Wrap(
			err,
			errs.CodeAccessibilityFailed,
			"could not connect to the D-Bus session bus",
		)
	}
	defer session.Close()

	callCtx, cancel := context.WithTimeout(ctx, atspiDialTimeout)
	defer cancel()

	err = session.Object(atspiBusDestination, atspiBusPath).CallWithContext(
		callCtx,
		methodPropertiesSet,
		0,
		atspiStatusInterface,
		property,
		dbus.MakeVariant(value),
	).Err
	if err != nil {
		return errs.Wrapf(
			err,
			errs.CodeAccessibilityFailed,
			"could not set %s.%s", atspiStatusInterface, property,
		)
	}

	return nil
}

// object returns the D-Bus object an element reference addresses.
func object(conn *dbus.Conn, ref accessible) dbus.BusObject {
	return conn.Object(ref.bus, ref.path)
}

// call performs one bounded round trip against an element. Every read in this
// file goes through it so no single unresponsive application can stall a scan.
func call(ctx context.Context, conn *dbus.Conn, ref accessible, method string, out any, args ...any) error {
	callCtx, cancel := context.WithTimeout(ctx, atspiCallTimeout)
	defer cancel()

	err := object(conn, ref).CallWithContext(callCtx, method, 0, args...).Store(out)

	return atspiCallError(err, method)
}

// atspiCallError maps a failed round trip onto the library's error vocabulary.
// The distinction matters to a caller: a timeout means one application is
// wedged and a retry may work, while a service-unknown error means it left the
// bus and the reference is dead.
func atspiCallError(err error, method string) error {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return errs.Wrapf(err, errs.CodeTimeout, "AT-SPI call timed out: %s", method)
	case errors.Is(err, context.Canceled):
		return errs.Wrapf(err, errs.CodeContextCanceled, "AT-SPI call canceled: %s", method)
	default:
		return errs.Wrapf(err, errs.CodeAccessibilityFailed, "AT-SPI call failed: %s", method)
	}
}

// property reads one property of an element.
func property(
	ctx context.Context,
	conn *dbus.Conn,
	ref accessible,
	iface string,
	name string,
) (dbus.Variant, error) {
	var value dbus.Variant
	err := call(ctx, conn, ref, methodPropertiesGet, &value, iface, name)

	return value, err
}

// propertyString reads a string property, reporting "" for a failed read or a
// property that is not a string. Text is never load-bearing for a hint, so a
// missing title must not fail a whole scan.
func propertyString(
	ctx context.Context,
	conn *dbus.Conn,
	ref accessible,
	iface string,
	name string,
) string {
	value, err := property(ctx, conn, ref, iface, name)
	if err != nil {
		return ""
	}

	text, _ := value.Value().(string)

	return text
}

// roleName reads an element's role name, normalized. AT-SPI already reports
// locale-independent lowercase names ("push button", "page tab list"); the
// normalization guards against a toolkit that spells one differently and makes
// every comparison in this package consistent.
func roleName(ctx context.Context, conn *dbus.Conn, ref accessible) string {
	var name string
	if err := call(ctx, conn, ref, methodGetRoleName, &name); err != nil {
		return ""
	}

	return normalizeRole(name)
}

// statesRead reads an element's AT-SPI state bitfield.
func statesRead(ctx context.Context, conn *dbus.Conn, ref accessible) ([]uint32, bool) {
	var states []uint32
	if err := call(ctx, conn, ref, methodGetState, &states); err != nil || len(states) == 0 {
		return nil, false
	}

	return states, true
}

// extents reads an element's rectangle in the given coordinate space, and
// reports false when the read failed or the element has no area. A zero-area
// element is not an error: toolkits report one for every element they have not
// laid out, and such an element cannot be clicked.
func extents(
	ctx context.Context,
	conn *dbus.Conn,
	ref accessible,
	coordType uint32,
) (image.Rectangle, bool) {
	var raw rawExtents
	if err := call(ctx, conn, ref, methodGetExtents, &raw, coordType); err != nil {
		return image.Rectangle{}, false
	}

	if raw.Width <= 0 || raw.Height <= 0 {
		return image.Rectangle{}, false
	}

	return image.Rect(
		int(raw.X),
		int(raw.Y),
		int(raw.X+raw.Width),
		int(raw.Y+raw.Height),
	), true
}

// children returns an element's children.
//
// GetChildren is the bulk call and the one every current toolkit implements. A
// few older toolkits only offer ChildCount plus GetChildAtIndex, which costs one
// round trip per child, so it is used only after the bulk call has failed.
func children(ctx context.Context, conn *dbus.Conn, ref accessible) ([]accessible, error) {
	var raw []rawAccessible

	bulkErr := call(ctx, conn, ref, methodGetChildren, &raw)
	if bulkErr == nil {
		return usableChildren(raw), nil
	}

	// ChildCount is declared as an int32 on the wire, and godbus stores a
	// property by exact type, so the count is read as one and widened here.
	var count int32
	if err := call(
		ctx, conn, ref, methodPropertiesGet, &count, ifaceAccessible, propertyChildCount,
	); err != nil {
		return nil, bulkErr
	}

	if count <= 0 {
		return nil, nil
	}

	kids := make([]accessible, 0, count)

	for index := range int(count) {
		var child rawAccessible
		if err := call(ctx, conn, ref, methodGetChildAtIndex, &child, int32(index)); err != nil {
			// One unreadable child must not lose its siblings, so it is skipped.
			continue
		}

		if kid := child.reference(); kid.valid() {
			kids = append(kids, kid)
		}
	}

	return kids, nil
}

// usableChildren converts a wire child list, dropping the null references a
// toolkit uses for gaps.
func usableChildren(raw []rawAccessible) []accessible {
	kids := make([]accessible, 0, len(raw))

	for _, entry := range raw {
		if kid := entry.reference(); kid.valid() {
			kids = append(kids, kid)
		}
	}

	return kids
}

// reference converts a wire element reference into a usable one.
func (r rawAccessible) reference() accessible {
	return accessible{bus: r.Name, path: r.Path}
}

// textValue reads an element's text content, which is the element's value for
// the roles that have one. GetText takes a start and end offset; (0, -1) means
// the whole text.
func textValue(ctx context.Context, conn *dbus.Conn, ref accessible) string {
	var text string
	if err := call(ctx, conn, ref, methodGetText, &text, int32(0), int32(-1)); err != nil {
		return ""
	}

	return text
}

// applicationPID reads the process id an application accessible reports. It
// reports 0 when the toolkit does not answer, which is the case on at-spi2-core
// releases older than the Id property.
func applicationPID(ctx context.Context, conn *dbus.Conn, ref accessible) int32 {
	value, err := property(ctx, conn, ref, ifaceApplication, propertyApplicationID)
	if err != nil {
		return 0
	}

	switch pid := value.Value().(type) {
	case int32:
		return pid
	case uint32:
		return int32(pid)
	case int64:
		return int32(pid)
	default:
		return 0
	}
}

// processName returns the executable name of a process from /proc, which is the
// closest thing Linux has to the identity a bundle identifier names elsewhere.
//
// The comm file holds the kernel's short name ("firefox", "gnome-terminal-"),
// which is what a desktop file is usually named after; cmdline holds the full
// path of the executable, whose base name covers the cases where comm was
// truncated or rewritten by the application itself.
func processName(pid int32) string {
	if pid <= 0 {
		return ""
	}

	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); err == nil {
		if name := strings.TrimSpace(string(data)); name != "" {
			return name
		}
	}

	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return ""
	}

	executable, _, _ := strings.Cut(string(data), "\x00")
	if executable == "" {
		return ""
	}

	return filepath.Base(executable)
}
