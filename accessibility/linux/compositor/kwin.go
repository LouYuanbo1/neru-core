package compositor

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/LouYuanbo1/neru-core/errs"
)

// KWin's window source. KWin has no command line, so the geometry comes from a
// script this package loads over org.kde.KWin's scripting D-Bus interface. The
// script reports the focused window's client geometry (content rectangle,
// excluding the title bar, so it aligns with AT-SPI's content origin) and its
// identity by calling back into an object this package exports.
//
// The bridge is process-wide. A second instance would mean a second owner of the
// D-Bus name and a second script inside KWin, so resolvers share it through
// acquire/release: the last release closes the connection.
//
// Nothing is exported, owned or written on a session that is not KWin: the
// script is only installed after NameHasOwner confirms org.kde.KWin is on the
// bus, and only when a resolver was constructed for BackendKWin.

const (
	// kwinBridgeName is the well-known name the loaded script calls. It is
	// specific to this library so it cannot collide with the daemon's bridge.
	kwinBridgeName = "org.neru.core.KWinBridge"
	// kwinBridgePath is the object carrying the receiver methods.
	kwinBridgePath dbus.ObjectPath = "/org/neru/core/KWinBridge"
	// kwinBridgeInterface is the receiver's D-Bus interface.
	kwinBridgeInterface = "org.neru.core.KWinBridge"

	// kwinScriptingDestination is KWin's scripting service.
	kwinScriptingDestination = "org.kde.KWin"
	// kwinScriptingPath is the object carrying loadScript and start.
	kwinScriptingPath dbus.ObjectPath = "/Scripting"
	// kwinScriptingInterface is the scripting interface.
	kwinScriptingInterface = "org.kde.kwin.Scripting"

	// kwinNameHasOwner checks whether KWin is on the bus before anything is
	// exported or written.
	kwinNameHasOwner = "org.freedesktop.DBus.NameHasOwner"

	// kwinScriptFileName is the loaded script's name under XDG_RUNTIME_DIR. It
	// is stable so a restarted process unloads its predecessor's copy instead of
	// stacking a second one.
	kwinScriptFileName = "neru-core-kwin-geometry.js"

	// kwinScriptFileMode is the script file's mode. KWin executes it, so it is
	// private.
	kwinScriptFileMode = 0o600

	// kwinScriptDirShared are the bits that would expose the script's directory
	// to anyone but its owner. Systemd-logind creates XDG_RUNTIME_DIR private
	// (0700); a directory pointed somewhere shared is the same exposure as a
	// world-writable /tmp, and a fixed-name file there that the compositor
	// executes must never be written.
	kwinScriptDirShared = 0o077

	// kwinInstallRetryInterval is how long a failed install is trusted before the
	// next query tries again. A KWin that starts after the library, or a session
	// bus that was down at construction, resolves in seconds.
	kwinInstallRetryInterval = 30 * time.Second

	// kwinDBusTimeout bounds each install round trip, so a wedged KWin cannot
	// stall the goroutine that is trying to set the bridge up.
	kwinDBusTimeout = 2 * time.Second

	// kwinInstallRetries is how many attempts one install sequence makes before
	// giving up until the retry interval passes.
	kwinInstallRetries = 2
)

// errKWinAbsent is returned when org.kde.KWin is not on the session bus. It is
// distinct from "no window is focused": only the second is an ordinary desktop.
var errKWinAbsent = errors.New("org.kde.KWin is not on the session bus")

// errKWinNoRuntimeDir is returned when there is no private directory to load the
// script from.
var errKWinNoRuntimeDir = errors.New(
	"XDG_RUNTIME_DIR is not set, so there is no private directory to load the " +
		"KWin geometry script from",
)

// errKWinRuntimeDirNotPrivate is returned when XDG_RUNTIME_DIR exists but is not
// private to this user.
var errKWinRuntimeDirNotPrivate = errors.New(
	"XDG_RUNTIME_DIR is not a directory private to this user, so it is not " +
		"somewhere the KWin geometry script may be loaded from",
)

// kwinWindow is what KWin last said about the focused window: its rectangle and
// identity. The identity lets a caller check that the rectangle and an AT-SPI
// frame describe the same window.
type kwinWindow struct {
	Rect  image.Rectangle
	Class string
	Name  string
	Title string
}

// public converts KWin's window to the public Window, preferring resourceClass
// as the application identity because that is what a foreign-toplevel app_id is
// usually built from.
func (w kwinWindow) public() Window {
	appID := w.Class
	if appID == "" {
		appID = w.Name
	}

	return Window{AppID: appID, Title: w.Title, Rect: w.Rect}
}

// kwinSource answers KWin's questions from the cache its script fills.
type kwinSource struct {
	// mu guards every field below. It is held only for cache reads and writes,
	// never across a D-Bus call, so a push from the script cannot wait on an
	// install.
	mu sync.Mutex

	// conn is the bridge's own session-bus connection.
	conn *dbus.Conn
	// installing is true while an install sequence runs.
	installing bool
	// installed is true once the script has been loaded.
	installed bool
	// lastAttempt is when the last install sequence began, for the retry gate.
	lastAttempt time.Time
	// installErr is why the last install sequence failed, nil after a success.
	installErr error

	// generation bumps whenever the bridge is dropped. An attempt publishes
	// nothing unless the generation it began in is still current, so an install
	// that finishes after Close cannot publish a connection nobody owns.
	generation uint64

	window kwinWindow
	valid  bool
}

// newKWinSource returns a source with nothing installed. It touches no bus.
func newKWinSource() *kwinSource {
	return &kwinSource{}
}

var (
	// kwinSharedMu guards the shared bridge and its reference count.
	kwinSharedMu sync.Mutex
	// kwinShared is the process-wide bridge, nil when no resolver holds one.
	kwinShared *kwinSource
	// kwinReferences is how many resolvers hold the shared bridge.
	kwinReferences int
)

// acquireKWinSource returns the shared bridge and a release function. There is
// one bridge because there must be one owner of its D-Bus name and one script
// inside KWin; the last release closes the connection.
func acquireKWinSource() (*kwinSource, func() error) {
	kwinSharedMu.Lock()

	if kwinShared == nil {
		kwinShared = newKWinSource()
	}

	kwinReferences++
	source := kwinShared

	kwinSharedMu.Unlock()

	var once sync.Once

	return source, func() error {
		once.Do(func() {
			kwinSharedMu.Lock()
			kwinReferences--

			last := kwinReferences == 0
			if last {
				kwinShared = nil
			}

			kwinSharedMu.Unlock()

			if last {
				_ = source.close()
			}
		})

		return nil
	}
}

// focused reports the focused window as KWin last reported it.
func (s *kwinSource) focused(_ context.Context) (Window, bool, error) {
	s.refresh()

	s.mu.Lock()
	window, valid, installErr := s.window, s.valid, s.installErr
	s.mu.Unlock()

	switch {
	case valid:
		return window.public(), true, nil
	case installErr != nil:
		return Window{}, false, kwinUnavailable(installErr)
	default:
		// The bridge is installing its first copy, or KWin reported no window:
		// both are "not yet an answer", and the caller falls back to AT-SPI.
		return Window{}, false, nil
	}
}

// origin returns the cached origin, and only when the cached window is the one
// frame describes.
//
// The cache comes from KWin and the frame from AT-SPI, so they can differ (a
// transient surface KWin ignored, a transition one side saw first), and that
// origin would move every hint to the other window. Identity and size guard it;
// see kwinOrigin.
func (s *kwinSource) origin(_ context.Context, frame Window) (image.Point, bool, error) {
	s.refresh()

	s.mu.Lock()
	window, valid, installErr := s.window, s.valid, s.installErr
	s.mu.Unlock()

	if !valid {
		if installErr != nil {
			return image.Point{}, false, kwinUnavailable(installErr)
		}

		return image.Point{}, false, nil
	}

	origin, ok := kwinOrigin(window, frame)

	return origin, ok, nil
}

// close releases the bridge's connection. The loaded script stays in KWin, where
// its next callback finds no owner of the bridge name and is dropped; unloading
// it would race a script KWin may still be running.
func (s *kwinSource) close() error {
	s.mu.Lock()
	conn := s.conn
	s.conn = nil
	s.installed = false
	s.installing = false
	s.installErr = nil
	s.valid = false
	s.window = kwinWindow{}
	s.generation++
	s.mu.Unlock()

	if conn == nil {
		return nil
	}

	return conn.Close()
}

// refresh drops a bridge whose connection died (a KWin restart takes the script
// with it) and starts an install when one is due.
func (s *kwinSource) refresh() {
	s.mu.Lock()

	if s.conn != nil && !s.conn.Connected() {
		conn := s.conn
		s.conn = nil
		s.installed = false
		s.installing = false
		s.installErr = nil
		s.valid = false
		s.window = kwinWindow{}
		s.generation++

		go func() { _ = conn.Close() }()
	}

	s.ensureStartedLocked()

	s.mu.Unlock()
}

// ensureStartedLocked starts an install sequence when none is running and the
// last failure is older than the retry interval. It is called with mu held.
func (s *kwinSource) ensureStartedLocked() {
	if s.installed || s.installing {
		return
	}

	if s.installErr != nil && time.Since(s.lastAttempt) < kwinInstallRetryInterval {
		return
	}

	s.installing = true
	s.lastAttempt = time.Now()
	generation := s.generation

	go s.install(generation)
}

// install runs one install sequence, retrying a failure on this goroutine before
// publishing the outcome. It publishes nothing when the bridge was dropped (a
// close, or a KWin restart) while the sequence ran.
func (s *kwinSource) install(generation uint64) {
	var (
		conn *dbus.Conn
		err  error
	)

	for attempt := 0; attempt <= kwinInstallRetries; attempt++ {
		conn, err = s.installOnce()
		if err == nil {
			break
		}

		if conn != nil {
			_ = conn.Close()
			conn = nil
		}

		if attempt < kwinInstallRetries {
			time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
		}
	}

	s.mu.Lock()
	s.installing = false

	if generation != s.generation {
		s.mu.Unlock()

		if conn != nil {
			_ = conn.Close()
		}

		return
	}

	if err != nil {
		s.installed = false
		s.installErr = err
		s.mu.Unlock()

		return
	}

	s.conn = conn
	s.installed = true
	s.installErr = nil
	s.mu.Unlock()
}

// installOnce performs one attempt: probe KWin, export the receiver, own the
// bridge name, write the script and load it.
func (s *kwinSource) installOnce() (*dbus.Conn, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("session bus: %w", err)
	}

	callCtx, cancel := context.WithTimeout(context.Background(), kwinDBusTimeout)
	defer cancel()

	var hasOwner bool

	err = conn.BusObject().
		CallWithContext(callCtx, kwinNameHasOwner, 0, kwinScriptingDestination).
		Store(&hasOwner)
	if err != nil {
		return conn, fmt.Errorf("NameHasOwner(%s): %w", kwinScriptingDestination, err)
	}

	if !hasOwner {
		return conn, errKWinAbsent
	}

	err = conn.Export(s, kwinBridgePath, kwinBridgeInterface)
	if err != nil {
		return conn, fmt.Errorf("export %s: %w", kwinBridgePath, err)
	}

	// A second owner of the name would receive the script's calls, so this
	// process's cache would never fill. NameFlagReplaceExisting makes the race
	// converge on the newest bridge instead of leaving a dead one installed.
	reply, nameErr := conn.RequestName(kwinBridgeName, dbus.NameFlagReplaceExisting)
	if nameErr != nil {
		return conn, fmt.Errorf("RequestName(%s): %w", kwinBridgeName, nameErr)
	}

	if reply != dbus.RequestNameReplyPrimaryOwner {
		return conn, fmt.Errorf("RequestName(%s) was refused: %d", kwinBridgeName, reply)
	}

	loadCtx, loadCancel := context.WithTimeout(context.Background(), kwinDBusTimeout)
	defer loadCancel()

	err = s.loadScript(loadCtx, conn)
	if err != nil {
		return conn, err
	}

	return conn, nil
}

// loadScript writes the script to a private directory and asks KWin to load and
// start it.
func (s *kwinSource) loadScript(ctx context.Context, conn *dbus.Conn) error {
	dir, err := kwinScriptDir()
	if err != nil {
		return err
	}

	path := filepath.Join(dir, kwinScriptFileName)

	err = writeKWinScript(path)
	if err != nil {
		return fmt.Errorf("writing the KWin geometry script: %w", err)
	}

	object := conn.Object(kwinScriptingDestination, kwinScriptingPath)

	// A stale copy from a previous run is unloaded first, best effort, so the
	// same file is not loaded twice in one KWin session.
	_ = object.CallWithContext(
		ctx, kwinScriptingInterface+".unloadScript", 0, path,
	).Err

	var id int32

	err = object.CallWithContext(ctx, kwinScriptingInterface+".loadScript", 0, path).Store(&id)
	if err != nil {
		return fmt.Errorf("loadScript: %w", err)
	}

	err = object.CallWithContext(ctx, kwinScriptingInterface+".start", 0).Err
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}

	return nil
}

// UpdateActiveWindow is the exported D-Bus method the KWin script calls when
// there is a focused window to report. The payload is
// "x,y,w,h,resourceClass,resourceName,caption", one string because KWin
// JavaScript's number marshaling is not worth depending on.
//
// A push that does not describe a window on screen is dropped, because the
// previous geometry is a better answer than an empty rectangle a caller cannot
// tell from a real one.
func (s *kwinSource) UpdateActiveWindow(payload string) *dbus.Error {
	window, ok := parseWindowPayload(payload)
	if !ok {
		return nil
	}

	s.mu.Lock()
	s.window, s.valid = window, true
	s.mu.Unlock()

	return nil
}

// ClearActiveWindow is the exported D-Bus method the KWin script calls when
// activation has left every window it can report on: the desktop is focused, the
// focused window was minimized, or the last one closed.
//
// It is separate from UpdateActiveWindow because the two say opposite things
// about the cache. A malformed push failed, so the previous rectangle survives
// it; a clear says the previous rectangle describes nothing on screen.
func (s *kwinSource) ClearActiveWindow(_ string) *dbus.Error {
	s.mu.Lock()
	s.window, s.valid = kwinWindow{}, false
	s.mu.Unlock()

	return nil
}

// kwinUnavailable is the "the bridge could not be installed" error. It is
// CodeNotSupported because what a person has to do about it is not check a
// permission: the desktop is KDE but the script did not load.
func kwinUnavailable(err error) error {
	return errs.Wrap(
		err,
		errs.CodeNotSupported,
		"the KWin focused-window geometry script is not installed, so KWin "+
			"cannot report the focused window",
	)
}

// kwinScriptDir resolves the directory the script is loaded from, which is
// XDG_RUNTIME_DIR and nowhere else.
func kwinScriptDir() (string, error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return "", errKWinNoRuntimeDir
	}

	// Stat, not Lstat, because a symlinked runtime directory is judged by what it
	// resolves to: the directory the script would really land in.
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errKWinRuntimeDirNotPrivate, err)
	}

	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s is not a directory at all", errKWinRuntimeDirNotPrivate, dir)
	}

	if info.Mode().Perm()&kwinScriptDirShared != 0 {
		return "", fmt.Errorf(
			"%w: %s is mode %04o, wanted 0700",
			errKWinRuntimeDirNotPrivate, dir, info.Mode().Perm(),
		)
	}

	return dir, nil
}

// writeKWinScript writes a fresh script beside path and renames it over it, so
// the path only ever names a complete script and KWin never executes half of
// one.
func writeKWinScript(path string) error {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}

	defer func() { _ = os.Remove(temp.Name()) }()

	err = temp.Chmod(kwinScriptFileMode)
	if err != nil {
		_ = temp.Close()

		return err
	}

	_, err = temp.WriteString(kwinGeometryScript)
	if err != nil {
		_ = temp.Close()

		return err
	}

	err = temp.Close()
	if err != nil {
		return err
	}

	return os.Rename(temp.Name(), path)
}

// parseWindowPayload turns a push from the script into a window, or reports it
// unusable: too few fields, a non-numeric coordinate, or a non-positive area.
func parseWindowPayload(payload string) (kwinWindow, bool) {
	parts := strings.SplitN(payload, ",", payloadParts)
	if len(parts) < payloadMinParts {
		return kwinWindow{}, false
	}

	originX, errX := strconv.Atoi(strings.TrimSpace(parts[0]))
	originY, errY := strconv.Atoi(strings.TrimSpace(parts[1]))
	width, errW := strconv.Atoi(strings.TrimSpace(parts[2]))
	height, errH := strconv.Atoi(strings.TrimSpace(parts[3]))

	if errX != nil || errY != nil || errW != nil || errH != nil {
		return kwinWindow{}, false
	}

	if width <= 0 || height <= 0 {
		return kwinWindow{}, false
	}

	return kwinWindow{
		Rect:  image.Rect(originX, originY, originX+width, originY+height),
		Class: payloadField(parts, payloadClassField),
		Name:  payloadField(parts, payloadNameField),
		Title: payloadField(parts, payloadTitleField),
	}, true
}

// payloadField reads an optional trailing field, so a KWin version reporting no
// identity still reports a rectangle.
func payloadField(parts []string, index int) string {
	if index >= len(parts) {
		return ""
	}

	return parts[index]
}
