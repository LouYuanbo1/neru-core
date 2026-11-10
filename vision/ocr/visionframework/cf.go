//go:build darwin

package visionframework

import (
	"sort"
	"strings"
	"sync"
	"unsafe"

	"github.com/LouYuanbo1/neru-core/errs"
)

// The recognizer's advertised languages are fixed for the life of the process,
// so they are fetched once and kept. The map is keyed by lower-case tag so a
// caller's "EN-us" matches whatever casing Vision advertises, and it holds
// Vision's own spelling as the value; the list keeps those spellings in a
// stable order, for error messages.
var (
	supportedLanguagesOnce  sync.Once
	supportedLanguagesByTag map[string]string
	supportedLanguageList   []string
	supportedLanguagesOK    bool
)

// goString copies an NSString into Go memory. The NSString owns its storage
// and is released with the enclosing pool, so the copy is what is safe to hold
// afterwards.
//
// The length comes from lengthOfBytesUsingEncoding: rather than from scanning
// for a terminator, so a recognized line of any length is copied whole. A
// string that cannot be encoded as UTF-8 reports zero bytes and is treated as
// empty, which is what NSString's UTF8String would answer too.
func (api *frameworks) goString(nsString uintptr) string {
	if nsString == 0 {
		return ""
	}

	length := api.objc.msgSendUintWithUint(
		nsString,
		api.selector("lengthOfBytesUsingEncoding:"),
		nsUTF8StringEncoding,
	)
	if length == 0 {
		return ""
	}

	characters := api.objc.msgSendCStr(nsString, api.selector("UTF8String"))
	if characters == nil {
		return ""
	}

	return string(unsafe.Slice(characters, length))
}

// arrayItems returns the borrowed members of an NSArray. The array keeps them
// alive; a caller that needs one beyond the array's lifetime must retain it.
// A nil array, which is what Vision answers for "no results", yields nil.
func (api *frameworks) arrayItems(array uintptr) []uintptr {
	if array == 0 {
		return nil
	}

	count := int(api.objc.msgSendUint(array, api.selector("count")))
	if count <= 0 {
		return nil
	}

	items := make([]uintptr, count)
	for index := range items {
		items[index] = api.objc.msgSendIDWithUint(array, api.selector("objectAtIndex:"), uintptr(index))
	}

	return items
}

// stringArray builds an autoreleased NSArray of NSStrings for the values. It
// returns 0 when no value converted, so a caller can leave Vision's own
// default in place instead of installing an empty list.
func (api *frameworks) stringArray(values []string) uintptr {
	items := make([]uintptr, 0, len(values))

	for _, value := range values {
		stringRef := api.objc.msgSendIDWithCStr(
			api.class("NSString"),
			api.selector("stringWithUTF8String:"),
			value,
		)
		if stringRef == 0 {
			continue
		}

		items = append(items, stringRef)
	}

	if len(items) == 0 {
		return 0
	}

	return api.objc.msgSendArrayWithObjects(
		api.class("NSArray"),
		api.selector("arrayWithObjects:count:"),
		items,
		uintptr(len(items)),
	)
}

// visionError turns the NSError performRequests:error: filled in into a
// failure with the code this package uses for a request that ran and failed.
// The description is Vision's own account of why the request failed; it never
// carries image content.
func (api *frameworks) visionError(name string, errorRef uintptr) error {
	if errorRef == 0 {
		return errs.Newf(
			errs.CodeAccessibilityFailed,
			"Vision %s failed without reporting an NSError",
			name,
		)
	}

	code := api.objc.msgSendInt(errorRef, api.selector("code"))
	description := api.goString(
		api.objc.msgSendID(errorRef, api.selector("localizedDescription")),
	)
	if description == "" {
		description = "no localized description"
	}

	return errs.Newf(
		errs.CodeAccessibilityFailed,
		"Vision %s failed (NSError %d): %s",
		name,
		code,
		description,
	)
}

// normalizeLanguages turns the requested tags into the tags passed to Vision,
// in the order asked, and reports errs.CodeNotSupported when none of them are
// recognizable. An empty request stays empty, which leaves Vision's default
// order alone.
//
// The supported list is cached for the process: it depends on the installed
// Vision model set, not on the image. A framework that will not answer the
// question is given the benefit of the doubt and the tags pass through, so an
// unrecognized tag fails the request afterwards instead of being refused here.
func (api *frameworks) normalizeLanguages(requested []string) ([]string, error) {
	if len(requested) == 0 {
		return nil, nil
	}

	supported, known := api.supportedLanguages()
	if !known {
		return requested, nil
	}

	kept := make([]string, 0, len(requested))

	var missing []string

	for _, tag := range requested {
		if resolved, ok := resolveLanguage(tag, supported); ok {
			kept = append(kept, resolved)

			continue
		}

		missing = append(missing, tag)
	}

	if len(kept) == 0 {
		return nil, errs.Newf(
			errs.CodeNotSupported,
			"Vision recognizes none of the languages asked for (%s); it recognizes %s",
			strings.Join(missing, ", "),
			strings.Join(supportedLanguageNames(), ", "),
		)
	}

	return kept, nil
}

// resolveLanguage maps a caller's tag onto the spelling Vision advertises, and
// reports whether it found one. Matching ignores case, and a language without
// a region is accepted when any advertised tag starts with it: asking for "en"
// selects an advertised "en-GB" or "en-US" rather than failing, which is what a
// caller means by a base tag.
//
// When several advertised tags share a prefix, the alphabetically first one
// wins, so the same request asks Vision for the same language every time
// instead of depending on map iteration order.
func resolveLanguage(tag string, supported map[string]string) (string, bool) {
	lower := strings.ToLower(tag)
	if spelled, ok := supported[lower]; ok {
		return spelled, true
	}

	best := ""

	for candidate, spelled := range supported {
		if !strings.HasPrefix(candidate, lower+"-") {
			continue
		}

		if best == "" || spelled < best {
			best = spelled
		}
	}

	return best, best != ""
}

// supportedLanguages returns the tags Vision advertises, keyed lower-case with
// Vision's spelling as the value, and whether the framework answered. It must
// be called inside an autorelease pool: the first call sends messages.
//
// A framework that cannot answer (an old build, a request that errors) reports
// false, and callers pass their own list through rather than refusing it.
func (api *frameworks) supportedLanguages() (map[string]string, bool) {
	supportedLanguagesOnce.Do(func() {
		supportedLanguagesByTag, supportedLanguagesOK = api.fetchSupportedLanguages()
	})

	return supportedLanguagesByTag, supportedLanguagesOK
}

// fetchSupportedLanguages asks VNRecognizeTextRequest which tags it supports,
// through the class method that answers the question without a request.
func (api *frameworks) fetchSupportedLanguages() (map[string]string, bool) {
	class := api.class("VNRecognizeTextRequest")
	if class == 0 {
		return nil, false
	}

	var errorRef uintptr

	languages := api.objc.msgSendIDWithError(
		class,
		api.selector("supportedRecognitionLanguagesAndReturnError:"),
		&errorRef,
	)
	if languages == 0 || errorRef != 0 {
		return nil, false
	}

	items := api.arrayItems(languages)
	if len(items) == 0 {
		return nil, false
	}

	byTag := make(map[string]string, len(items))
	list := make([]string, 0, len(items))

	for _, item := range items {
		name := api.goString(item)
		if name == "" {
			continue
		}

		byTag[strings.ToLower(name)] = name

		list = append(list, name)
	}

	if len(byTag) == 0 {
		return nil, false
	}

	supportedLanguageList = list

	return byTag, true
}

// supportedLanguageNames lists the cached tags in a stable order, for an error
// message that names what this machine can read instead.
func supportedLanguageNames() []string {
	names := make([]string, len(supportedLanguageList))
	copy(names, supportedLanguageList)

	sort.Strings(names)

	return names
}
