//go:build darwin

package darwin

import (
	"sync"
	"unsafe"
)

// Objective-C class and selector names are interned by the runtime, so the
// lookups below are cached and reused for the life of the process. The mutexes
// cover the maps and the interning call together, because bundle lookups run on
// several goroutines and an unsynchronised map write would fault the process.
var (
	objcMu        sync.Mutex
	objcClasses   = make(map[string]uintptr, 4)
	objcSelectors = make(map[string]uintptr, 8)
)

func (f *frameworks) class(name string) uintptr {
	objcMu.Lock()
	defer objcMu.Unlock()

	if cached, ok := objcClasses[name]; ok {
		return cached
	}

	class := f.objc.getClass(name)
	objcClasses[name] = class

	return class
}

func (f *frameworks) selector(name string) uintptr {
	objcMu.Lock()
	defer objcMu.Unlock()

	if cached, ok := objcSelectors[name]; ok {
		return cached
	}

	selector := f.objc.registerName(name)
	objcSelectors[name] = selector

	return selector
}

// withPool runs fn inside an autorelease pool. Go threads have no run loop and
// therefore no pool of their own, so every Objective-C call that autoreleases
// anything (a created NSString, an array returned by a convenience method)
// must be bracketed or the process keeps the objects until it exits.
func (f *frameworks) withPool(fn func()) {
	pool := f.objc.poolPush()
	defer f.objc.poolPop(pool)

	fn()
}

// goNSString converts a Go string to an autoreleased NSString. It must be
// called inside withPool, and the result must not outlive that call.
func (f *frameworks) goNSString(value string) uintptr {
	stringClass := f.class("NSString")
	if stringClass == 0 {
		return 0
	}

	return f.objc.msgSendIDWithCStr(stringClass, f.selector("stringWithUTF8String:"), value)
}

// nsStringString copies an NSString into Go memory.
func (f *frameworks) nsStringString(value uintptr) string {
	if value == 0 {
		return ""
	}

	return goCString(f.objc.msgSendCStr(value, f.selector("UTF8String")))
}

// goCString copies a NUL-terminated C string into Go memory. The accessibility
// metadata it carries is short; the cap keeps a malformed string from scanning
// the address space.
func goCString(value *byte) string {
	if value == nil {
		return ""
	}

	const maxLength = 4096

	buffer := make([]byte, 0, 64)
	for index := 0; index < maxLength; index++ {
		char := *(*byte)(unsafe.Add(unsafe.Pointer(value), index))
		if char == 0 {
			break
		}

		buffer = append(buffer, char)
	}

	return string(buffer)
}

// runningApplicationPID returns the process id behind a bundle identifier, and
// whether a running application matched. LaunchServices is not consulted:
// accessibility works on live processes, so an installed-but-not-running
// application is no use here.
func runningApplicationPID(bundleID string) (int32, bool) {
	api, err := loadFrameworks()
	if err != nil {
		return 0, false
	}

	var (
		pid   int32
		found bool
	)

	api.withPool(func() {
		class := api.class("NSRunningApplication")
		if class == 0 {
			return
		}

		nsBundleID := api.goNSString(bundleID)
		if nsBundleID == 0 {
			return
		}

		applications := api.objc.msgSendIDWithID(
			class,
			api.selector("runningApplicationsWithBundleIdentifier:"),
			nsBundleID,
		)
		if applications == 0 {
			return
		}

		if api.objc.msgSendUint(applications, api.selector("count")) == 0 {
			return
		}

		first := api.objc.msgSendID(applications, api.selector("firstObject"))
		if first == 0 {
			return
		}

		pid = api.objc.msgSendInt(first, api.selector("processIdentifier"))
		found = pid > 0
	})

	return pid, found
}

// frontmostApplicationPID returns the process id of the application the window
// server considers frontmost. It is the fallback when AX's focused-application
// attribute answers no element, which happens while a menu is tracking.
func frontmostApplicationPID() (int32, bool) {
	api, err := loadFrameworks()
	if err != nil {
		return 0, false
	}

	var (
		pid   int32
		found bool
	)

	api.withPool(func() {
		workspaceClass := api.class("NSWorkspace")
		if workspaceClass == 0 {
			return
		}

		workspace := api.objc.msgSendID(workspaceClass, api.selector("sharedWorkspace"))
		if workspace == 0 {
			return
		}

		application := api.objc.msgSendID(workspace, api.selector("frontmostApplication"))
		if application == 0 {
			return
		}

		pid = api.objc.msgSendInt(application, api.selector("processIdentifier"))
		found = pid > 0
	})

	return pid, found
}

// runningApplicationBundleID returns the bundle identifier of a process, and
// "" when the process is not a running application or has no bundle.
func runningApplicationBundleID(pid int32) string {
	if pid <= 0 {
		return ""
	}

	api, err := loadFrameworks()
	if err != nil {
		return ""
	}

	var bundleID string

	api.withPool(func() {
		class := api.class("NSRunningApplication")
		if class == 0 {
			return
		}

		application := api.objc.msgSendIDWithInt(
			class,
			api.selector("runningApplicationWithProcessIdentifier:"),
			pid,
		)
		if application == 0 {
			return
		}

		bundleID = api.nsStringString(
			api.objc.msgSendID(application, api.selector("bundleIdentifier")),
		)
	})

	return bundleID
}

// runningApplicationBundlePath returns the on-disk path of a running
// application's bundle, and false when no running application matches or the
// process is not a bundled application. Bundle-type detection reads the
// application's files from this path, so an application that is installed but
// not running has no path: accessibility works on live processes, and so does
// the detection.
func runningApplicationBundlePath(bundleID string) (string, bool) {
	if bundleID == "" {
		return "", false
	}

	api, err := loadFrameworks()
	if err != nil {
		return "", false
	}

	var (
		bundlePath string
		found      bool
	)

	api.withPool(func() {
		class := api.class("NSRunningApplication")
		if class == 0 {
			return
		}

		nsBundleID := api.goNSString(bundleID)
		if nsBundleID == 0 {
			return
		}

		applications := api.objc.msgSendIDWithID(
			class,
			api.selector("runningApplicationsWithBundleIdentifier:"),
			nsBundleID,
		)
		if applications == 0 {
			return
		}

		if api.objc.msgSendUint(applications, api.selector("count")) == 0 {
			return
		}

		first := api.objc.msgSendID(applications, api.selector("firstObject"))
		if first == 0 {
			return
		}

		bundleURL := api.objc.msgSendID(first, api.selector("bundleURL"))
		if bundleURL == 0 {
			return
		}

		bundlePath = api.nsStringString(api.objc.msgSendID(bundleURL, api.selector("path")))
		found = bundlePath != ""
	})

	return bundlePath, found
}

// runningApplicationName returns the localized name of a process, used when AX
// answers the application's own title with nothing.
func runningApplicationName(pid int32) string {
	if pid <= 0 {
		return ""
	}

	api, err := loadFrameworks()
	if err != nil {
		return ""
	}

	var name string

	api.withPool(func() {
		class := api.class("NSRunningApplication")
		if class == 0 {
			return
		}

		application := api.objc.msgSendIDWithInt(
			class,
			api.selector("runningApplicationWithProcessIdentifier:"),
			pid,
		)
		if application == 0 {
			return
		}

		name = api.nsStringString(
			api.objc.msgSendID(application, api.selector("localizedName")),
		)
	})

	return name
}
