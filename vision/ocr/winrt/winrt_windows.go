//go:build windows

package winrt

import (
	"syscall"
	"unsafe"
)

// The Windows Runtime, driven the way this module's UI Automation backend drives
// COM: raw vtable calls through syscall, no CGO, because the library builds with
// CGO_ENABLED=0 on every target. Everything here is the plumbing a WinRT class
// needs before its own methods can be called: the apartment, activation
// factories, HSTRINGs, the IInspectable vtable prefix, and the IVectorView every
// WinRT collection hands out.
//
// Interface IDs and vtable slots are read from the Windows SDK's generated
// headers (windows.media.ocr.idl, windows.globalization.idl,
// windows.graphics.imaging.idl and windows.storage.streams.idl). A wrong slot
// calls the wrong method with the wrong arguments, so every slot constant below
// names the method it stands for.

const (
	// roInitMultithreaded is RO_INIT_MULTITHREADED, the apartment the OCR worker
	// joins. Objects created there are agile and usable from any thread in it,
	// which is what lets one engine be cached for the process.
	roInitMultithreaded = 1

	// inspectableSlots is the IInspectable vtable prefix every WinRT interface
	// starts with: IUnknown's three slots, then GetIids, GetRuntimeClassName and
	// GetTrustLevel. A class's own methods start at inspectableSlots.
	inspectableSlots = 6

	// IUnknown's slots, which every COM object starts with.
	slotQueryInterface = 0
	slotRelease        = 2

	// bufferByteAccessBuffer is IBufferByteAccess::Buffer. That interface
	// derives from IUnknown rather than IInspectable, so the method is slot 3.
	bufferByteAccessBuffer = 3
)

var (
	combase = syscall.NewLazyDLL("combase.dll")

	procRoInitialize              = combase.NewProc("RoInitialize")
	procRoGetActivationFactory    = combase.NewProc("RoGetActivationFactory")
	procWindowsCreateString       = combase.NewProc("WindowsCreateString")
	procWindowsDeleteString       = combase.NewProc("WindowsDeleteString")
	procWindowsGetStringRawBuffer = combase.NewProc("WindowsGetStringRawBuffer")
)

// winrtCall invokes the method at vtable slot index on the WinRT object this.
// The object pointer is the implicit first argument; the return value is the
// HRESULT.
//
// The out-parameter addresses in args are uintptrs, which the runtime does not
// track. If the stack grows before SyscallN runs, the caller's variables move
// and the callee writes into the old frame. //go:uintptrescapes moves them to
// the heap and keeps them alive for the call, as syscall.Proc.Call does for its
// own arguments.
//
//go:uintptrescapes
func winrtCall(this unsafe.Pointer, slot int, args ...uintptr) uintptr {
	if this == nil {
		return uintptr(0x80004003) // E_POINTER
	}

	vtable := *(*unsafe.Pointer)(this)
	method := *(*uintptr)(unsafe.Add(vtable, uintptr(slot)*unsafe.Sizeof(uintptr(0))))

	full := make([]uintptr, 0, len(args)+1)
	full = append(full, uintptr(this))
	full = append(full, args...)

	result, _, _ := syscall.SyscallN(method, full...)

	return result
}

// releaseObject drops one reference. Nil is accepted so a defer can be
// registered before the object exists. A release has no failure a caller can act
// on, so its result is dropped.
func releaseObject(this unsafe.Pointer) {
	if this != nil {
		winrtCall(this, slotRelease)
	}
}

// winrtQuery asks this for another of its interfaces.
func winrtQuery(this unsafe.Pointer, iid *guid) (unsafe.Pointer, error) {
	var out unsafe.Pointer

	hresult := winrtCall(
		this,
		slotQueryInterface,
		uintptr(unsafe.Pointer(iid)),
		uintptr(unsafe.Pointer(&out)),
	)
	if hresultFailed(hresult) || out == nil {
		return nil, hresultError("QueryInterface", hresult)
	}

	return out, nil
}

// hstring is a WinRT HSTRING handle. The zero handle is the empty string, which
// WinRT accepts everywhere a string is read.
type hstring uintptr

// newHString allocates an HSTRING for s. The caller deletes it.
func newHString(s string) (hstring, error) {
	utf16, err := syscall.UTF16FromString(s)
	if err != nil {
		return 0, err
	}

	var handle hstring

	// The length excludes the terminator UTF16FromString appends.
	hresult, _, _ := procWindowsCreateString.Call(
		uintptr(unsafe.Pointer(&utf16[0])),
		uintptr(len(utf16)-1),
		uintptr(unsafe.Pointer(&handle)),
	)
	if hresultFailed(hresult) {
		return 0, hresultError("WindowsCreateString", hresult)
	}

	return handle, nil
}

// String copies the HSTRING's text into a Go string. WinRT owns the buffer only
// as long as the handle lives, so the copy happens before the handle is deleted.
func (h hstring) String() string {
	if h == 0 {
		return ""
	}

	var length uint32

	ret, _, _ := procWindowsGetStringRawBuffer.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&length)),
	)
	if ret == 0 || length == 0 {
		return ""
	}

	// Read the address WinRT returned through the slot the call wrote it to
	// rather than converting the word, which keeps go vet's unsafeptr check
	// satisfied: a pointer the runtime handed back is a pointer, not an integer.
	buffer := *(**uint16)(unsafe.Pointer(&ret))

	return syscall.UTF16ToString(unsafe.Slice(buffer, int(length)))
}

// delete frees the HSTRING. WinRT owns the characters, so this is the only way a
// string read out of the engine stops existing.
func (h hstring) delete() {
	if h != 0 {
		_, _, _ = procWindowsDeleteString.Call(uintptr(h))
	}
}

// roInitialize joins the calling OS thread to the multithreaded apartment. S_OK
// and S_FALSE both mean the thread is in; RPC_E_CHANGED_MODE means it already
// belongs to another apartment, which no thread this package owns does.
func roInitialize() error {
	hresult, _, _ := procRoInitialize.Call(roInitMultithreaded)
	if hresultFailed(hresult) {
		return hresultError("RoInitialize", hresult)
	}

	return nil
}

// activationFactory returns the interface iid of the activation factory for the
// runtime class className. The caller releases it.
func activationFactory(className string, iid *guid) (unsafe.Pointer, error) {
	name, err := newHString(className)
	if err != nil {
		return nil, err
	}
	defer name.delete()

	var factory unsafe.Pointer

	hresult, _, _ := procRoGetActivationFactory.Call(
		uintptr(name),
		uintptr(unsafe.Pointer(iid)),
		uintptr(unsafe.Pointer(&factory)),
	)
	if hresultFailed(hresult) || factory == nil {
		return nil, hresultError("RoGetActivationFactory("+className+")", hresult)
	}

	return factory, nil
}

// vectorView is the IVectorView<T> the OCR result hands out for lines and words.
// The interface is generic, but its vtable is not: after the IInspectable prefix
// come GetAt, get_Size, IndexOf and GetMany, in that order for every T
// (windows.foundation.collections.h).
type vectorView struct{ ptr unsafe.Pointer }

const (
	vectorViewGetAt = inspectableSlots + iota
	vectorViewSize
)

// size is the element count.
func (v vectorView) size() (uint32, error) {
	var count uint32

	hresult := winrtCall(v.ptr, vectorViewSize, uintptr(unsafe.Pointer(&count)))
	if hresultFailed(hresult) {
		return 0, hresultError("IVectorView::get_Size", hresult)
	}

	return count, nil
}

// at returns element index, an object the caller releases.
func (v vectorView) at(index uint32) (unsafe.Pointer, error) {
	var item unsafe.Pointer

	hresult := winrtCall(v.ptr, vectorViewGetAt, uintptr(index), uintptr(unsafe.Pointer(&item)))
	if hresultFailed(hresult) || item == nil {
		return nil, hresultError("IVectorView::GetAt", hresult)
	}

	return item, nil
}
