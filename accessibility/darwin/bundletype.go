//go:build darwin

package darwin

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"github.com/LouYuanbo1/neru-core/errs"
)

// The browser engines DetectBundleType reports. A hint walk treats the first
// two as noisy DOM trees and all four as worth pruning off screen, which is the
// distinction neru draws.
const (
	// BundleTypeElectron is an application built on the Electron framework.
	BundleTypeElectron = "electron"
	// BundleTypeChromium is a Chromium-based browser or a Chrome PWA shim.
	BundleTypeChromium = "chromium"
	// BundleTypeFirefox is a Gecko browser, recognised by its XUL engine.
	BundleTypeFirefox = "firefox"
	// BundleTypeWebKit is an application that renders with the system WebKit:
	// Safari, a Safari web app, or any app that registers an http scheme.
	BundleTypeWebKit = "webkit"
)

// Paths inside an application bundle the file rules inspect. They are the
// layout every engine uses; nothing here is branded.
const (
	bundleContentsDir        = "Contents"
	bundleInfoPlistName      = "Info.plist"
	bundleFrameworksDir      = "Frameworks"
	bundleElectronName       = "Electron Framework.framework"
	bundleSupportDir         = "Support"
	bundleChromiumLoaderDir  = "Helpers"
	bundleChromiumLoaderName = "app_mode_loader"
	bundleXULBinary          = "MacOS/XUL"
	bundleFirefoxDir         = "Resources/browser"
)

// chromiumShortcutKeyPrefix is the Info.plist key prefix a Chrome/Chromium PWA
// app shim carries (CrAppModeShortcutID and friends). A shim is a thin bundle
// that loads Chromium from the parent browser, so it has none of the framework
// files the earlier rule looks for.
const chromiumShortcutKeyPrefix = "CrAppMode"

// plistTemplateApplicationKey marks a Safari web app, which renders with the
// system WebKit and registers "x-webkit-app-launch" rather than http.
const plistTemplateApplicationKey = "LSTemplateApplication"

// plistURLTypesKey is the Info.plist key listing the URL schemes an
// application claims.
const plistURLTypesKey = "CFBundleURLTypes"

// plistURLSchemesKey is the key inside one URL type entry holding its schemes.
const plistURLSchemesKey = "CFBundleURLSchemes"

var (
	bundleTypeMu    sync.Mutex
	bundleTypeCache = make(map[string]string, 8)
)

// DetectBundleType inspects the bundle of a running application and reports the
// engine it is built on: BundleTypeElectron, BundleTypeChromium,
// BundleTypeFirefox or BundleTypeWebKit, or "" when nothing identifies it.
//
// Detection is file-based, with no application or framework name list: an
// Electron framework directory, a Chromium app-mode loader inside a framework,
// a Chrome PWA shim's CrAppMode keys, Firefox's XUL engine next to a browser
// component, a Safari web app template, and an http/https scheme registration,
// in that order. The first rule that matches wins.
//
// A bundle identifier the process cannot find a running application for, or a
// bundle whose rules all miss, is reported as "" with no error: "not a browser"
// is an answer, not a failure. An empty bundle identifier is
// errs.CodeInvalidInput. Results are cached per bundle identifier, because a
// running application's bundle contents do not change.
func DetectBundleType(bundleID string) (string, error) {
	bundleID = strings.TrimSpace(bundleID)
	if bundleID == "" {
		return "", errs.New(errs.CodeInvalidInput, "bundle identifier is empty")
	}

	bundleTypeMu.Lock()
	cached, ok := bundleTypeCache[bundleID]
	bundleTypeMu.Unlock()

	if ok {
		return cached, nil
	}

	bundlePath, found := runningApplicationBundlePath(bundleID)
	if !found {
		return "", nil
	}

	// An unreadable Info.plist costs the two plist-based rules and nothing
	// else, so its error is deliberately ignored here.
	info, _ := readInfoPlist(filepath.Join(bundlePath, bundleContentsDir, bundleInfoPlistName))

	bundleType := detectBundleTypeAt(bundlePath, info)

	bundleTypeMu.Lock()
	bundleTypeCache[bundleID] = bundleType
	bundleTypeMu.Unlock()

	return bundleType, nil
}

// detectBundleTypeAt applies the file-based engine rules to an application
// bundle that has already been found on disk. info is its decoded Info.plist,
// or nil when it could not be read, in which case only the file rules answer.
// It is separated from DetectBundleType so the rules can be tested against a
// bundle built in a temporary directory.
func detectBundleTypeAt(bundlePath string, info map[string]any) string {
	if bundlePath == "" {
		return ""
	}

	contents := filepath.Join(bundlePath, bundleContentsDir)

	// 1. The Electron framework is the definitive Electron marker.
	if pathExists(filepath.Join(contents, bundleFrameworksDir, bundleElectronName)) {
		return BundleTypeElectron
	}

	// 2. An app-mode loader inside a framework's Helpers/ marks a browser built
	//    on Chromium (Chrome, Edge, Brave, Vivaldi, Opera, Arc, ...). It is
	//    absent from Electron, CEF and Qt runtimes, so a non-browser Chromium
	//    application does not match. Some browsers nest the real bundle under
	//    Contents/Support/<App>.app, so those directories are scanned too.
	if hasChromiumAppModeLoader(contents) {
		return BundleTypeChromium
	}

	// 3. A CrAppMode* key identifies a Chrome/Chromium PWA app shim.
	for key := range info {
		if strings.HasPrefix(key, chromiumShortcutKeyPrefix) {
			return BundleTypeChromium
		}
	}

	// 4. The XUL engine plus a browser component marks a Firefox-based browser.
	//    The component is what separates a Gecko browser from a Gecko
	//    non-browser such as Thunderbird, which ships XUL but no browser.
	if pathExists(filepath.Join(contents, bundleXULBinary)) && pathIsDir(filepath.Join(contents, bundleFirefoxDir)) {
		return BundleTypeFirefox
	}

	// 5. LSTemplateApplication marks a Safari web app.
	if plistBool(info, plistTemplateApplicationKey) {
		return BundleTypeWebKit
	}

	// 6. Only a web browser registers an http or https URL scheme.
	if plistHasWebScheme(info) {
		return BundleTypeWebKit
	}

	return ""
}

// hasChromiumAppModeLoader scans the bundle's Frameworks directories for a
// framework that ships an app-mode loader.
func hasChromiumAppModeLoader(contents string) bool {
	directories := []string{filepath.Join(contents, bundleFrameworksDir)}

	for _, entry := range directoryNames(filepath.Join(contents, bundleSupportDir)) {
		if !strings.HasSuffix(entry, ".app") {
			continue
		}

		directories = append(directories, filepath.Join(
			contents, bundleSupportDir, entry, bundleContentsDir, bundleFrameworksDir,
		))
	}

	for _, directory := range directories {
		for _, entry := range directoryNames(directory) {
			if !strings.HasSuffix(entry, ".framework") {
				continue
			}

			if pathExists(filepath.Join(
				directory, entry, bundleChromiumLoaderDir, bundleChromiumLoaderName,
			)) {
				return true
			}
		}
	}

	return false
}

// plistBool reads a property-list boolean, accepting the textual spellings an
// XML property list may carry.
func plistBool(info map[string]any, key string) bool {
	switch value := info[key].(type) {
	case bool:
		return value
	case float64:
		return value != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "1", "true", "yes":
			return true
		}
	}

	return false
}

// plistHasWebScheme reports whether the application claims the http or https
// URL scheme, which no non-browser application does.
func plistHasWebScheme(info map[string]any) bool {
	types, ok := info[plistURLTypesKey].([]any)
	if !ok {
		return false
	}

	for _, entry := range types {
		typeEntry, ok := entry.(map[string]any)
		if !ok {
			continue
		}

		schemes, ok := typeEntry[plistURLSchemesKey].([]any)
		if !ok {
			continue
		}

		for _, scheme := range schemes {
			name, ok := scheme.(string)
			if !ok {
				continue
			}

			switch strings.ToLower(strings.TrimSpace(name)) {
			case "http", "https":
				return true
			}
		}
	}

	return false
}

// pathExists reports whether a path exists, file or directory.
func pathExists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}

// pathIsDir reports whether a path exists and is a directory.
func pathIsDir(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.IsDir()
}

// directoryNames lists a directory's entry names, and nothing when it cannot be
// read. Every caller treats a missing directory as "no match".
func directoryNames(path string) []string {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	return names
}

// readInfoPlist decodes an Info.plist, preferring CoreFoundation so a binary
// property list is read exactly as the loader reads it, and falling back to
// plutil when a symbol is unavailable.
func readInfoPlist(path string) (map[string]any, error) {
	info, err := readInfoPlistCoreFoundation(path)
	if err == nil {
		return info, nil
	}

	return readInfoPlistCommand(path)
}

// readInfoPlistCoreFoundation reads a property list through CoreFoundation.
// Both the XML and the binary format decode to the same CFType tree, which is
// the reason not to parse the bytes here.
func readInfoPlistCoreFoundation(path string) (map[string]any, error) {
	api, err := loadFrameworks()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("darwin: %s is empty", path)
	}

	cfData := api.cf.dataCreate(0, unsafe.SliceData(data), int64(len(data)))
	if cfData == 0 {
		return nil, fmt.Errorf("darwin: could not copy %s into CoreFoundation", path)
	}

	defer api.cf.release(cfData)

	var (
		format   uint64
		plistErr uintptr
	)

	plist := api.cf.propertyListCreateWithData(0, cfData, 0, &format, &plistErr)
	if plistErr != 0 {
		api.cf.release(plistErr)
	}

	if plist == 0 {
		return nil, fmt.Errorf("darwin: %s is not a property list", path)
	}

	defer api.cf.release(plist)

	decoded := api.plistValue(plist)

	info, ok := decoded.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("darwin: %s does not hold a dictionary", path)
	}

	return info, nil
}

// readInfoPlistCommand converts a property list to XML with plutil and decodes
// it here. It is the fallback for a process that could not bind the
// CoreFoundation property-list call.
func readInfoPlistCommand(path string) (map[string]any, error) {
	output, err := exec.Command("plutil", "-convert", "xml1", "-o", "-", path).Output()
	if err != nil {
		return nil, fmt.Errorf("darwin: plutil could not read %s: %w", path, err)
	}

	return parseXMLPlist(output)
}

// plistValue converts one CoreFoundation property-list node into its Go
// equivalent: string, bool, float64, []any or map[string]any. A node of another
// type becomes nil, which every caller treats as absent.
func (f *frameworks) plistValue(ref uintptr) any {
	if ref == 0 {
		return nil
	}

	switch {
	case f.isCFString(ref):
		return f.goString(ref)
	case f.cf.getTypeID(ref) == f.cf.booleanGetTypeID():
		return f.cf.booleanGetValue(ref) != 0
	case f.cf.getTypeID(ref) == f.cf.numberGetTypeID():
		value, ok := f.cfNumberFloat64(ref)
		if !ok {
			return nil
		}

		return value
	case f.isCFArray(ref):
		members := f.arrayValues(ref)

		values := make([]any, 0, len(members))
		for _, member := range members {
			values = append(values, f.plistValue(member))
		}

		return values
	case f.isCFDictionary(ref):
		return f.plistDictionary(ref)
	default:
		return nil
	}
}

// plistDictionary converts one CoreFoundation dictionary into a Go map. Only a
// string key can be represented, which is every key a property list has.
func (f *frameworks) plistDictionary(ref uintptr) map[string]any {
	count := f.cf.dictionaryGetCount(ref)
	if count <= 0 {
		return map[string]any{}
	}

	keys := make([]uintptr, count)
	values := make([]uintptr, count)

	f.cf.dictionaryGetKeysAndValues(ref, &keys[0], &values[0])

	dict := make(map[string]any, count)

	for index, key := range keys {
		name := f.plistValue(key)

		nameKey, ok := name.(string)
		if !ok {
			continue
		}

		dict[nameKey] = f.plistValue(values[index])
	}

	return dict
}

// parseXMLPlist decodes an XML property list into the same shape plistValue
// produces. plutil emits this format, and the parser is kept small on purpose:
// it only has to understand what plutil writes.
func parseXMLPlist(data []byte) (map[string]any, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))

	value, err := decodePlistValue(decoder)
	if err != nil {
		return nil, err
	}

	info, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("darwin: the property list root is not a dictionary")
	}

	return info, nil
}

// decodePlistValue decodes the next element in an XML property list, skipping
// the whitespace, comments and DOCTYPE a document carries between elements.
func decodePlistValue(decoder *xml.Decoder) (any, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}

		if start, ok := token.(xml.StartElement); ok {
			return decodePlistElement(decoder, start)
		}
	}
}

// decodePlistElement decodes one property-list element, whose start tag has
// already been read.
func decodePlistElement(decoder *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "dict":
		return decodePlistDict(decoder)
	case "array":
		return decodePlistArray(decoder)
	case "string":
		return decodePlistText(decoder)
	case "integer", "real":
		text, err := decodePlistText(decoder)
		if err != nil {
			return nil, err
		}

		value, convErr := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if convErr != nil {
			return nil, fmt.Errorf("darwin: property list number %q: %w", text, convErr)
		}

		return value, nil
	case "true":
		return true, decoder.Skip()
	case "false":
		return false, decoder.Skip()
	default:
		// date, data and anything newer are decoded as absent: no rule here
		// reads them.
		return nil, decoder.Skip()
	}
}

// decodePlistDict decodes a <dict> element, pairing each <key> with the element
// that follows it.
func decodePlistDict(decoder *xml.Decoder) (map[string]any, error) {
	dict := make(map[string]any)
	key := ""

	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}

		switch typed := token.(type) {
		case xml.StartElement:
			if typed.Name.Local == "key" {
				key, err = decodePlistText(decoder)
				if err != nil {
					return nil, err
				}

				continue
			}

			value, err := decodePlistElement(decoder, typed)
			if err != nil {
				return nil, err
			}

			dict[key] = value
			key = ""
		case xml.EndElement:
			if typed.Name.Local == "dict" {
				return dict, nil
			}
		}
	}
}

// decodePlistArray decodes an <array> element.
func decodePlistArray(decoder *xml.Decoder) ([]any, error) {
	values := make([]any, 0, 4)

	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}

		switch typed := token.(type) {
		case xml.StartElement:
			value, err := decodePlistElement(decoder, typed)
			if err != nil {
				return nil, err
			}

			values = append(values, value)
		case xml.EndElement:
			if typed.Name.Local == "array" {
				return values, nil
			}
		}
	}
}

// decodePlistText reads the character data of one element, whose start tag has
// already been read, and consumes its end tag.
func decodePlistText(decoder *xml.Decoder) (string, error) {
	var builder strings.Builder

	for {
		token, err := decoder.Token()
		if err != nil {
			return "", err
		}

		switch typed := token.(type) {
		case xml.CharData:
			builder.Write(typed)
		case xml.EndElement:
			return builder.String(), nil
		}
	}
}
