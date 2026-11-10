//go:build linux

package linux

import (
	"image"
	"strings"
)

// This file holds every decision the backend makes about an AT-SPI tree or an
// X11 pointer without talking to either: which roles count as clickable, how
// deep one walk goes, how a window's extents become screen pixels, and how many
// wheel notches a scroll delta is. Keeping those decisions pure is what makes
// the backend unit-testable on a host with no desktop, and it keeps the D-Bus
// and X11 files to the parts that need a live session.

const (
	// defaultMaxDepth is how deep ClickableNodes reads when the caller passes a
	// non-positive maxDepth. Real windows are far shallower than this; the bound
	// exists so a toolkit whose tree loops cannot turn one scan into an
	// unbounded amount of D-Bus traffic.
	defaultMaxDepth = 50

	// maxWalkDepth caps a caller-supplied depth, for the same reason. A caller
	// asking for "everything" gets this, not infinity.
	maxWalkDepth = 256

	// maxNodesPerScan bounds how many accessibles one walk reads. A page that
	// grows lazily (a virtualized table) can expose far more nodes than a screen
	// can hold, and reading them all would cost more than any hint is worth.
	maxNodesPerScan = 2000

	// walkWorkers is how many AT-SPI round trips one registry probe keeps in
	// flight. godbus multiplexes them over a single connection, so this caps
	// concurrent work rather than connections.
	walkWorkers = 8
)

// AT-SPI role names this package has to name explicitly. They are the exact
// strings Accessible.GetRoleName returns, which are the GEnum nicks of
// AtspiRole with hyphens replaced by spaces.
const (
	roleFrame       = "frame"
	roleWindow      = "window"
	roleDialog      = "dialog"
	roleEntry       = "entry"
	roleText        = "text"
	roleSpinButton  = "spin button"
	roleApplication = "application"
)

// Coordinate spaces a Component.GetExtents call can be asked for, as the
// AtspiCoordType enum defines them.
const (
	// coordScreen means the extents are relative to the screen's top-left corner.
	coordScreen = uint32(0)
	// coordWindow means they are relative to the top-level window's origin.
	coordWindow = uint32(1)
)

// AT-SPI state bit indices, from at-spi2-core's atspi-constants.h. All of them
// are below 32, so they live in the first word of the state bitfield GetState
// returns.
const (
	// stateActive marks the window the toolkit considers active. It is the only
	// focus signal AT-SPI offers.
	stateActive uint = 1
	// stateEditable marks text that accepts typing. It is what tells an entry
	// from read-only text, since GTK 3 reports both as the "text" role.
	stateEditable uint = 7
	// stateEnabled marks an element that can take input.
	stateEnabled uint = 8
	// stateFocused marks the element holding keyboard focus.
	stateFocused uint = 12
	// stateSensitive marks an element that is not greyed out. GTK 4 sets this and
	// never stateEnabled, where other toolkits set both.
	stateSensitive uint = 24
	// stateShowing marks an element that is mapped and visible.
	stateShowing uint = 25
)

// bitsPerStateWord is the width of one word of the AT-SPI state bitfield, which
// is one bit per state index.
const bitsPerStateWord = 32

// scrollPixelsPerNotch is how many pixels of a caller's scroll delta make one
// wheel notch. X11 has no sub-notch scroll to synthesize: a core wheel button
// event is exactly one notch, so a pixel delta is rounded to whole notches.
const scrollPixelsPerNotch = 30

// maxScrollNotchesPerAxis caps one axis of one scroll so a huge delta (a config
// that asks for a full page in one press) cannot flood the X connection with
// thousands of button events. The cap matches what other X11 tooling sends per
// request.
const maxScrollNotchesPerAxis = 50

// normalizeRole turns one role name into the form comparisons use. AT-SPI
// already reports lowercase hyphen-free names, but a configuration file may
// spell "Page Tab  List" or "PUSH BUTTON", and both have to address the same
// element, so case is folded and runs of whitespace collapsed.
func normalizeRole(role string) string {
	return strings.Join(strings.Fields(strings.ToLower(role)), " ")
}

// normalizeRoles normalizes a role list and drops empties and duplicates while
// preserving the caller's order. Order is kept because ClickableRoles reports
// the list back, and a caller that sets "entry, push button" should read that
// back rather than a reordered set.
func normalizeRoles(roles []string) []string {
	if len(roles) == 0 {
		return nil
	}

	out := make([]string, 0, len(roles))
	seen := make(map[string]struct{}, len(roles))

	for _, role := range roles {
		normalized := normalizeRole(role)
		if normalized == "" {
			continue
		}

		if _, duplicate := seen[normalized]; duplicate {
			continue
		}

		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

// roleSet turns a normalized role list into the lookup set a walk tests
// against. Case is folded again so a caller may pass raw configuration text.
func roleSet(roles []string) map[string]struct{} {
	set := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		normalized := normalizeRole(role)
		if normalized != "" {
			set[normalized] = struct{}{}
		}
	}

	return set
}

// effectiveRole returns the role an element is matched and reported as.
//
// GTK 3 reports entries, including multi-line text views, as the "text" role,
// and so does everyone else for read-only text; only the state bit distinguishes
// them. Editable text therefore reads as an entry, which is what the library's
// text_field and text_area roles resolve to, while read-only text stays static
// text. Doing this here means both the walk and a caller's role list see one
// consistent name.
func effectiveRole(role string, states []uint32) string {
	if role == roleText && atspiStateBit(states, stateEditable) {
		return roleEntry
	}

	return role
}

// wantsRole reports whether an element of the given role can be in the set once
// effectiveRole has read its states. It must be called before the states are
// read to decide whether reading them is worth a round trip, and again after, so
// it accepts the unrewritten "text" role as a possible entry.
func wantsRole(role string, set map[string]struct{}) bool {
	if _, ok := set[role]; ok {
		return true
	}

	_, wantsEntry := set[roleEntry]

	return wantsEntry && role == roleText
}

// isValueRole reports whether an element's text is its value, as AXValue is on
// macOS. Password text is deliberately absent: a password must never leave the
// application, so it is read as absent rather than read and discarded later.
func isValueRole(role string) bool {
	return role == roleEntry || role == roleSpinButton
}

// isWindowRole reports whether an AT-SPI role denotes a top-level window. The
// registry lists an application's windows as its children, mixed with menus and
// other application-level surfaces, so a window enumeration has to filter.
func isWindowRole(role string) bool {
	return role == roleFrame || role == roleWindow || role == roleDialog
}

// atspiStateBit reports whether one AT-SPI state bit is set in a state bitfield
// returned by GetState.
func atspiStateBit(states []uint32, bit uint) bool {
	word := bit / bitsPerStateWord
	if int(word) >= len(states) {
		return false
	}

	return states[word]&(1<<(bit%bitsPerStateWord)) != 0
}

// atspiEnabled reports whether an element takes input. GTK 4 sets SENSITIVE and
// never ENABLED where other toolkits set both, so either one counts.
func atspiEnabled(states []uint32) bool {
	return atspiStateBit(states, stateEnabled) || atspiStateBit(states, stateSensitive)
}

// walkDepth turns a caller's maxDepth into the deepest level one walk reads.
// A non-positive value means "use the default", and anything beyond the ceiling
// is clamped, so a walk always terminates.
func walkDepth(maxDepth int) int {
	if maxDepth <= 0 {
		return defaultMaxDepth
	}

	return min(maxDepth, maxWalkDepth)
}

// depthAllowed reports whether level depth may be read. The root is level 0, so
// a limit of 50 reads the root plus 50 levels of descendants.
func depthAllowed(depth, limit int) bool {
	return depth <= limit
}

// depthLimit resolves the deepest level one walk reads: the call's maxDepth when
// it sets one, else the client's configured default, else the package default.
// A per-call value always wins, so a caller that narrows a scan is never
// widened by the client's configuration, and both go through walkDepth so both
// get the ceiling.
func depthLimit(maxDepth, configured int) int {
	if maxDepth > 0 {
		return walkDepth(maxDepth)
	}

	return walkDepth(configured)
}

// nodeLimit resolves how many elements one walk may read.
func nodeLimit(configured int) int {
	if configured > 0 {
		return configured
	}

	return maxNodesPerScan
}

// placement is how a window's AT-SPI extents become screen pixels: the
// coordinate space the extents are read in, and the offset that space is
// translated by.
type placement struct {
	// coordType is an AtspiCoordType: the coordinate space GetExtents answers in.
	coordType uint32
	// offset is added to an extents origin to reach screen pixels.
	offset image.Point
}

// screenPlacement reads extents in screen coordinates and adds nothing, which is
// correct on X11 and for XWayland clients, and is the fallback whenever the
// window cannot answer in window coordinates.
func screenPlacement() placement {
	return placement{coordType: coordScreen}
}

// place maps one element's extents into screen pixels.
func (p placement) place(rect image.Rectangle) image.Rectangle {
	return rect.Add(p.offset)
}

// placementFor decides how a window's elements are mapped from AT-SPI extents
// into screen pixels, from the window's own extents in both coordinate spaces.
//
// It is the fallback used when the compositor cannot supply the window's origin
// (X11, GNOME without the Neru extension, a session with no bridge, or the
// embedder's Options.DisableCompositor). The compositor path in placementFor
// (scan.go) is preferred, because a toolkit's own answer can be wrong: GTK 4
// reports a zero screen origin for every element on Wayland.
//
// The reconstruction is only used when it is provably meaningful:
//
//   - Both rects must be valid. A window with no extents yields no nodes either
//     way, so the screen fallback costs nothing.
//   - The two origins must differ. When they are equal the toolkit either has
//     the window at the screen origin, where both spaces coincide, or reports
//     the same values for both coordinate spaces, where translating would add
//     the window position a second time. Screen coordinates are then the safe
//     answer.
//   - The window's screen rect must still intersect the screen, so a window that
//     vanished mid-scan cannot shift every hint in it by a stale offset.
func placementFor(frameScreen, frameWindow, screen image.Rectangle) placement {
	if frameScreen.Empty() || frameWindow.Empty() {
		return screenPlacement()
	}

	if frameScreen.Min == frameWindow.Min {
		return screenPlacement()
	}

	if !screen.Empty() && !frameScreen.Overlaps(screen) {
		return screenPlacement()
	}

	return placement{
		coordType: coordWindow,
		offset:    frameScreen.Min.Sub(frameWindow.Min),
	}
}

// clipToScreen intersects an element's rectangle with the screen and reports
// whether any of it is on screen.
//
// A hint lands at the centre of the rectangle it is given, so an element that
// hangs off the edge of the screen is reported by its visible part: the centre
// then falls on a pixel the user can actually see and click. An element that is
// entirely off screen is dropped, because a hint on it would point at nothing.
// An unknown screen (empty rect) clips nothing.
func clipToScreen(rect, screen image.Rectangle) (image.Rectangle, bool) {
	if rect.Empty() {
		return image.Rectangle{}, false
	}

	if screen.Empty() {
		return rect, true
	}

	visible := rect.Intersect(screen)

	return visible, !visible.Empty()
}

// scrollNotches is how many wheel notches a scroll of delta pixels is, rounded
// to the nearest notch and never fewer than one for a non-zero delta, then
// capped. Truncating would turn a small configured step into no scroll at all,
// and an uncapped delta into thousands of button events.
func scrollNotches(delta int) int {
	if delta == 0 {
		return 0
	}

	notches := (abs(delta) + scrollPixelsPerNotch/2) / scrollPixelsPerNotch

	return min(max(notches, 1), maxScrollNotchesPerAxis)
}

// abs returns the magnitude of an int without importing math for one case.
func abs(value int) int {
	if value < 0 {
		return -value
	}

	return value
}

// frameCandidate is what focus selection needs to know about one AT-SPI
// top-level window.
type frameCandidate struct {
	// active is the AT-SPI ACTIVE state, which a toolkit sets on the window it
	// believes is active.
	active bool
	// showing is the AT-SPI SHOWING state, set on a mapped window.
	showing bool
	// appMatch is true when this window's application names the compositor's
	// focused application.
	appMatch bool
	// titleMatch is true when this window's title equals the compositor's focused
	// title.
	titleMatch bool
}

// selectAppFrame picks among frames the compositor says belong to the focused
// application. A title match outranks the ACTIVE state, which outranks SHOWING,
// because two windows of one application share an app_id and only their titles
// tell them apart.
//
// activeIdentifiesFocus gates the ACTIVE step on the one compositor whose ACTIVE
// state is trustworthy. KWin sets it on the compositor-focused window; on
// wlroots it is set inconsistently across frames, so a unique ACTIVE sibling
// there may be a background window, and the step is skipped rather than guessed.
//
// Every step requires a unique match. Returning nothing is deliberate: picking
// one of several equally plausible siblings would put hints in a background
// window, which is worse than letting the caller report no focused window.
func selectAppFrame(frames []frameCandidate, activeIdentifiesFocus bool) (int, bool) {
	if index, ok := uniqueFrame(frames, func(frame frameCandidate) bool {
		return frame.appMatch && frame.titleMatch
	}); ok {
		return index, true
	}

	if activeIdentifiesFocus {
		if index, ok := uniqueFrame(frames, func(frame frameCandidate) bool {
			return frame.appMatch && frame.active && frame.showing
		}); ok {
			return index, true
		}
	}

	if index, ok := uniqueFrame(frames, func(frame frameCandidate) bool {
		return frame.appMatch && frame.showing
	}); ok {
		return index, true
	}

	return uniqueFrame(frames, func(frame frameCandidate) bool {
		return frame.appMatch
	})
}

// selectTitleFrame picks the one frame whose title names the focused window,
// across every application. It is the fallback for a compositor app_id that no
// AT-SPI application name answers to, which happens when the two spell one
// application differently ("gnome-text-editor" against
// "org.gnome.TextEditor").
func selectTitleFrame(frames []frameCandidate) (int, bool) {
	return uniqueFrame(frames, func(frame frameCandidate) bool {
		return frame.titleMatch
	})
}

// uniqueFrame returns the index of the one frame that matches, and false when
// none or several do.
func uniqueFrame(frames []frameCandidate, match func(frameCandidate) bool) (int, bool) {
	found := -1

	for index, frame := range frames {
		if !match(frame) {
			continue
		}

		if found >= 0 {
			return 0, false
		}

		found = index
	}

	if found < 0 {
		return 0, false
	}

	return found, true
}

// selectFocusedFrame returns the index in frames of the window that best
// identifies the focused one, and false when nothing qualifies.
//
// AT-SPI has no "focused window" of its own, so the ranking is:
//
//  1. a window that is both ACTIVE and SHOWING, which is the normal answer;
//  2. any ACTIVE window, for a toolkit that marks a not-yet-mapped window active
//     during a switch;
//  3. any SHOWING window, which is the last resort on a session where no toolkit
//     reports ACTIVE at all.
//
// Ties go to the first window in registry and tree order, which is stable across
// calls and therefore keeps a hint session from jumping between two windows that
// both claim focus. Returning no frame is deliberate: guessing between equally
// active windows would put hints in the wrong one, which is worse than none.
func selectFocusedFrame(frames []frameCandidate) (int, bool) {
	for pass := range 3 {
		for index, frame := range frames {
			switch pass {
			case 0:
				if frame.active && frame.showing {
					return index, true
				}
			case 1:
				if frame.active {
					return index, true
				}
			default:
				if frame.showing {
					return index, true
				}
			}
		}
	}

	return 0, false
}

// identifierMatches reports whether one spelling of an application identity
// names the same application as another.
//
// A display name ("Firefox") rarely equals a desktop id ("org.mozilla.firefox"),
// so the last dot-separated segment of either side is compared too. That is what
// lets a configured "org.mozilla.firefox" reach an application that reports
// itself as "firefox", and a configured "nautilus" reach one that reports itself
// as "org.gnome.Nautilus".
func identifierMatches(candidate, want string) bool {
	candidate = normalizeIdentifier(candidate)
	if candidate == "" {
		return false
	}

	if candidate == want {
		return true
	}

	if segment := lastDotSegment(want); segment != "" && segment == candidate {
		return true
	}

	if segment := lastDotSegment(candidate); segment != "" && segment == want {
		return true
	}

	return false
}

// normalizeIdentifier folds the spelling differences that must not matter when
// comparing application identities: a desktop id is case-insensitive and may
// arrive with surrounding whitespace from a configuration file.
func normalizeIdentifier(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// lastDotSegment returns the substring after the final '.', or "" when there is
// no interior dot. A value with no segment to compare reports "", never an empty
// match: comparing the empty string would make every identity equal.
func lastDotSegment(value string) string {
	index := strings.LastIndexByte(value, '.')

	if index < 0 || index == len(value)-1 {
		return ""
	}

	return value[index+1:]
}
