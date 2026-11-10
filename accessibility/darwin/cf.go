//go:build darwin

package darwin

import (
	"bytes"
	"sync"
	"unsafe"

	"github.com/LouYuanbo1/neru-core/element"
)

// attribute cache. Accessibility attributes are compared by CFString value, so
// the backend can build its own strings once and keep them for the process
// lifetime. There are fewer than twenty, and a client that lives for the whole
// process would otherwise recreate them on every tree walk.
//
// The mutex covers the map and the creation together, because walks run on
// several goroutines and an unsynchronised map write would fault the process
// rather than merely duplicate a string.
var (
	attributeMu    sync.Mutex
	attributeCache = make(map[string]uintptr, 16)
)

// cfStringRef returns the CoreFoundation string for name, creating and caching
// it on first use. A nil result means the runtime refused to make the string,
// which no caller can act on; the attribute read then fails like any other.
func (f *frameworks) cfStringRef(name string) uintptr {
	attributeMu.Lock()
	defer attributeMu.Unlock()

	if cached, ok := attributeCache[name]; ok {
		return cached
	}

	ref := f.cf.stringCreateWithCString(0, name, cfStringEncodingUTF8)
	attributeCache[name] = ref

	return ref
}

// goString copies a CFString into Go memory. The caller keeps ownership of the
// CFString; the copy is what survives the next CFRelease.
func (f *frameworks) goString(ref uintptr) string {
	if ref == 0 {
		return ""
	}

	length := f.cf.stringGetLength(ref)
	if length <= 0 {
		return ""
	}

	maxSize := f.cf.stringGetMaximumSize(length, cfStringEncodingUTF8)
	if maxSize <= 0 {
		return ""
	}

	buf := make([]byte, maxSize+1)
	if f.cf.stringGetCString(ref, unsafe.SliceData(buf), int64(len(buf)), cfStringEncodingUTF8) == 0 {
		return ""
	}

	if end := bytes.IndexByte(buf, 0); end >= 0 {
		buf = buf[:end]
	}

	return string(buf)
}

// cfBool reads a CoreFoundation boolean and reports whether ref really was
// one. An attribute that is missing or of another type stays distinguishable
// from an explicit false, which is what AXEnabled needs.
func (f *frameworks) cfBool(ref uintptr) (bool, bool) {
	if ref == 0 || f.cf.getTypeID(ref) != f.cf.booleanGetTypeID() {
		return false, false
	}

	return f.cf.booleanGetValue(ref) != 0, true
}

// isCFString reports whether ref is a CFString, so a batch value of an
// unexpected type (an AX error placeholder, kCFNull) is skipped instead of
// being read as text.
func (f *frameworks) isCFString(ref uintptr) bool {
	return ref != 0 && f.cf.getTypeID(ref) == f.cf.stringGetTypeID()
}

// cfNumberSInt32 reads a CoreFoundation number as a signed 32-bit integer and
// reports whether ref really was a number. WindowManager's undocumented "wid"
// attribute and the window server's window ids are both 32-bit.
func (f *frameworks) cfNumberSInt32(ref uintptr) (int32, bool) {
	var value int32

	if ref == 0 || f.cf.getTypeID(ref) != f.cf.numberGetTypeID() {
		return 0, false
	}

	if f.cf.numberGetValue(ref, cfNumberSInt32Type, unsafe.Pointer(&value)) == 0 {
		return 0, false
	}

	return value, true
}

// cfNumberFloat64 reads a CoreFoundation number as a double. A property list
// number carries no type a caller can rely on, so this is how one is converted
// when only its value matters.
func (f *frameworks) cfNumberFloat64(ref uintptr) (float64, bool) {
	var value float64

	if ref == 0 || f.cf.getTypeID(ref) != f.cf.numberGetTypeID() {
		return 0, false
	}

	if f.cf.numberGetValue(ref, cfNumberFloat64Type, unsafe.Pointer(&value)) == 0 {
		return 0, false
	}

	return value, true
}

// isCFDictionary reports whether ref is a CFDictionary, so a property-list
// node is dispatched on its real type.
func (f *frameworks) isCFDictionary(ref uintptr) bool {
	return ref != 0 && f.cf.getTypeID(ref) == f.cf.dictionaryGetTypeID()
}

// isCFArray reports whether ref is a CFArray.
func (f *frameworks) isCFArray(ref uintptr) bool {
	return ref != 0 && f.cf.getTypeID(ref) == f.cf.arrayGetTypeID()
}

// isAXValue reports whether ref is an AXValue, the box AX uses for a point,
// size or rect.
func (f *frameworks) isAXValue(ref uintptr) bool {
	return ref != 0 && f.cf.getTypeID(ref) == f.ax.valueGetTypeID()
}

// arrayValues returns the borrowed members of a CFArray. The array keeps them
// alive; a caller that needs one beyond the array's lifetime must retain it.
func (f *frameworks) arrayValues(array uintptr) []uintptr {
	if array == 0 {
		return nil
	}

	count := f.cf.arrayGetCount(array)
	if count <= 0 {
		return nil
	}

	values := make([]uintptr, count)
	for index := range values {
		values[index] = f.cf.arrayGetValueAtIndex(array, int64(index))
	}

	return values
}

// copyAttribute reads one attribute and returns the owned value. The caller
// releases it. A non-zero AXError carries the reason, so callers can tell an
// unsupported attribute from a dead element.
func (f *frameworks) copyAttribute(elementRef uintptr, name string) (uintptr, int32) {
	attribute := f.cfStringRef(name)
	if attribute == 0 {
		return 0, kAXErrorIllegalArgument
	}

	var value uintptr

	status := f.ax.copyAttributeValue(elementRef, attribute, &value)
	if status != kAXErrorSuccess {
		return 0, status
	}

	return value, kAXErrorSuccess
}

// pointValue reads an AXValue holding a CGPoint.
func (f *frameworks) pointValue(ref uintptr) (cgPoint, bool) {
	var point cgPoint

	if !f.isAXValue(ref) {
		return point, false
	}

	if f.ax.valueGetType(ref) != axValueCGPointType {
		return point, false
	}

	if f.ax.valueGetValue(ref, axValueCGPointType, unsafe.Pointer(&point)) == 0 {
		return point, false
	}

	return point, true
}

// sizeValue reads an AXValue holding a CGSize.
func (f *frameworks) sizeValue(ref uintptr) (cgSize, bool) {
	var size cgSize

	if !f.isAXValue(ref) {
		return size, false
	}

	if f.ax.valueGetType(ref) != axValueCGSizeType {
		return size, false
	}

	if f.ax.valueGetValue(ref, axValueCGSizeType, unsafe.Pointer(&size)) == 0 {
		return size, false
	}

	return size, true
}

// roleName is the neutral name of a native role, for error messages and for
// the few internal decisions that are easier to read in neutral terms.
func roleName(native string) element.Role {
	if role, ok := (element.AXRoleMapper{}).MapRole(native); ok {
		return role
	}

	return element.RoleUnknown
}

// cfArrayGetTypeID is the CFArray type id, used to confirm an attribute really
// held an array before its members are read.
func (f *frameworks) cfArrayGetTypeID() uint64 {
	return f.cf.arrayGetTypeID()
}
