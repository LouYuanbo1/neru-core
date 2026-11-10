//go:build darwin

package darwin

import (
	"context"
	"image"
	"strings"
)

// Roles that never contain interactive children, so their subtree is skipped
// without a children query. Both are leaves in every AppKit tree.
var nonInteractiveRoles = map[string]bool{
	"AXStaticText": true,
	"AXImage":      true,
}

// Roles that are interactive leaves. Traversal normally stops at one, because
// its children are decoration; a few of them can still open a popover, sheet
// or menu, and those are checked for an important container child instead.
var interactiveLeafRoles = map[string]bool{
	"AXButton":             true,
	"AXMenuButton":         true,
	"AXComboBox":           true,
	"AXCheckBox":           true,
	"AXLink":               true,
	"AXPopUpButton":        true,
	"AXSlider":             true,
	"AXTabButton":          true,
	"AXSwitch":             true,
	"AXDisclosureTriangle": true,
	"AXTextField":          true,
	"AXGenericElement":     true,
	"AXTextArea":           true,
	"AXRadioButton":        true,
}

// Leaf roles that can still hold an important container child.
var leafRolesWithImportantChildren = map[string]bool{
	"AXButton":             true,
	"AXMenuButton":         true,
	"AXPopUpButton":        true,
	"AXLink":               true,
	"AXComboBox":           true,
	"AXDisclosureTriangle": true,
	"AXGenericElement":     true,
	"AXTextArea":           true,
	"AXRadioButton":        true,
}

// Roles that carry a clickable child even when the parent itself has no
// bounds, such as the popover a button opens.
var importantContainerRoles = map[string]bool{
	"AXPopover": true,
	"AXSheet":   true,
	"AXMenu":    true,
	"AXList":    true,
	"AXLink":    true,
	"AXButton":  true,
}

// containerRoles keep their children worth walking even when the container's
// own rectangle misses the clip region. A window, group or scroll area often
// reports an empty rectangle while its children carry the real geometry.
var containerRoles = map[string]bool{
	"AXApplication":    true,
	"AXWindow":         true,
	"AXSheet":          true,
	"AXPopover":        true,
	"AXGroup":          true,
	"AXScrollArea":     true,
	"AXSplitGroup":     true,
	"AXTabGroup":       true,
	"AXList":           true,
	"AXTable":          true,
	"AXOutline":        true,
	"AXMenu":           true,
	"AXMenuBar":        true,
	"AXToolbar":        true,
	"AXGenericElement": true,
}

// treeNode is one visited element while a walk is in progress. The node owns
// its AX reference until the walk decides whether the element is kept.
type treeNode struct {
	ref      uintptr
	info     *elementInfo
	children []*treeNode
	search   string
	kept     bool
	// parent is set while the walk runs and cleared once the tree is released.
	// It exists because Mission Control recognises the Spaces bar by an
	// ancestor's AXIdentifier, which only a parent link can answer.
	parent *treeNode
}

// walkOptions is what one ClickableNodes call needs from the client plus its
// arguments.
type walkOptions struct {
	roles             map[string]struct{}
	maxDepth          int
	maxNodes          int
	clip              image.Rectangle
	screen            image.Rectangle
	hitTestVisibility bool

	// bundleType is the browser engine the walked application is built on
	// ("chromium", "electron", "firefox", "webkit"), or "" for a native
	// application. The strict Chromium/Electron filter and the off-screen
	// prune only apply when it names an engine, so a native tree walks exactly
	// as it did before.
	bundleType string

	// visit is called once for every element the walk reaches, root included,
	// before any filter or hit test reads it. It may rewrite the element's info
	// and reports whether the element and its subtree stay in the tree. A nil
	// hook accepts every element unchanged, which is the default.
	visit func(node *treeNode, info *elementInfo) bool
}

// walkStats counts what a walk did, for diagnostics and for tests.
type walkStats struct {
	visited  int
	kept     int
	depthHit int
	nodesHit int
	// filtered counts children the per-child filters dropped and pruned counts
	// subtrees the browser-engine prune never descended into.
	filtered int
	pruned   int
}

// buildTree walks root's subtree and returns it with search text accumulated.
// The returned tree owns every reference it holds; releaseTree drops them. The
// root itself is borrowed from the caller and is never released here.
func (c *Client) buildTree(
	ctx context.Context,
	root uintptr,
	rootInfo *elementInfo,
	opts *walkOptions,
) (*treeNode, walkStats) {
	stats := walkStats{}

	tree := &treeNode{info: rootInfo}
	if tree.info == nil {
		tree.info, _ = c.readInfo(root)
	}

	// The root is visited like any other element. Mission Control's hook
	// ignores it, and a plain walk has no hook at all.
	if opts.visit != nil && !opts.visit(tree, tree.info) {
		return tree, stats
	}

	c.walkChildren(ctx, tree, root, 1, opts, &stats)
	accumulateSearchText(tree)

	return tree, stats
}

// walkChildren visits node's children and every level below them. ref is the
// node's AX reference, borrowed from its owner; the children the walk keeps
// are retained and owned by the tree.
func (c *Client) walkChildren(
	ctx context.Context,
	node *treeNode,
	ref uintptr,
	depth int,
	opts *walkOptions,
	stats *walkStats,
) {
	if ctx.Err() != nil || node == nil || node.info == nil {
		return
	}

	if opts.maxDepth > 0 && depth > opts.maxDepth {
		stats.depthHit++

		return
	}

	if node.info.hidden || !node.info.visible {
		return
	}

	role := node.info.role
	if nonInteractiveRoles[role] {
		return
	}

	if interactiveLeafRoles[role] && !leafRolesWithImportantChildren[role] {
		return
	}

	bounds := node.info.bounds()

	// Browser engines build a deep DOM-backed tree whose off-screen subtrees
	// cost one accessibility round trip per child to discover and can never
	// yield a hint. Pruning before the children query is what keeps a long
	// Chromium page affordable. A frame-less container is not judged off
	// screen, because its children may still carry the real geometry.
	if isBrowserEngine(opts.bundleType) && !bounds.Empty() && !bounds.Overlaps(opts.clip) {
		stats.pruned++

		return
	}

	if !bounds.Empty() && !bounds.Overlaps(opts.clip) && !containerRoles[role] {
		return
	}

	if stats.visited >= opts.maxNodes {
		stats.nodesHit++

		return
	}

	children := c.childRefs(ref)
	if len(children) == 0 {
		return
	}

	nodes := make([]*treeNode, 0, len(children))

	for _, childRef := range children {
		if stats.visited >= opts.maxNodes {
			stats.nodesHit++
			cfRelease(childRef)

			continue
		}

		if ctx.Err() != nil {
			cfRelease(childRef)

			continue
		}

		stats.visited++

		info, err := c.readInfo(childRef)
		if err != nil || info.role == "" {
			cfRelease(childRef)

			continue
		}

		child := &treeNode{ref: childRef, info: info, parent: node}

		if opts.visit != nil && !opts.visit(child, info) {
			stats.filtered++
			cfRelease(childRef)

			continue
		}

		if !opts.includeBrowserChild(info) {
			stats.filtered++
			cfRelease(childRef)

			continue
		}

		c.walkChildren(ctx, child, childRef, depth+1, opts, stats)
		nodes = append(nodes, child)
	}

	if interactiveLeafRoles[role] {
		if !hasImportantContainer(nodes) {
			for _, child := range nodes {
				releaseTree(child)
			}

			return
		}
	}

	node.children = nodes
}

// minBrowserElementSize is the smallest side, in points, a non-interactive
// element may have in a Chromium or Electron tree before it counts as DOM
// noise rather than a control.
const minBrowserElementSize = 10

// isBrowserEngine reports whether a detected bundle type names a browser
// engine, which is what turns on the browser-specific walk filtering.
func isBrowserEngine(bundleType string) bool {
	switch bundleType {
	case BundleTypeChromium, BundleTypeElectron, BundleTypeFirefox, BundleTypeWebKit:
		return true
	default:
		return false
	}
}

// includeBrowserChild applies the filtering neru applies to one child of a
// browser-rendered tree. A native application accepts every child, so an empty
// bundle type leaves the walk unchanged.
//
// A child that misses the clip region is dropped whatever the engine. In a
// Chromium or Electron tree so is a child too small to click, and one whose
// center falls outside the clip region, unless the role is itself interactive.
// Those two trees carry thousands of structural DOM nodes a few points wide or
// scrolled out of view; keeping them would bury the real controls in noise.
func (o *walkOptions) includeBrowserChild(info *elementInfo) bool {
	if !isBrowserEngine(o.bundleType) || info == nil {
		return true
	}

	bounds := info.bounds()
	if !bounds.Empty() && !bounds.Overlaps(o.clip) {
		return false
	}

	if o.bundleType != BundleTypeChromium && o.bundleType != BundleTypeElectron {
		return true
	}

	if bounds.Empty() {
		return true
	}

	if bounds.Dx() < minBrowserElementSize || bounds.Dy() < minBrowserElementSize {
		if !interactiveLeafRoles[info.role] {
			return false
		}
	}

	// A center outside the clip region means the element overflows the window
	// or its scroll container. Interactive roles are kept, because a button
	// half scrolled off the edge is still a legitimate hint target.
	centerX := bounds.Min.X + bounds.Dx()/2
	centerY := bounds.Min.Y + bounds.Dy()/2

	if centerX < o.clip.Min.X || centerX > o.clip.Max.X ||
		centerY < o.clip.Min.Y || centerY > o.clip.Max.Y {
		return interactiveLeafRoles[info.role]
	}

	return true
}

// hasImportantContainer reports whether a leaf's children include a popover,
// sheet or menu, which is the only reason to walk into an interactive leaf.
func hasImportantContainer(children []*treeNode) bool {
	for _, child := range children {
		if child.info == nil {
			continue
		}

		if importantContainerRoles[child.info.role] || importantContainerRoles[child.info.subrole] {
			return true
		}
	}

	return false
}

// childRefs returns the element's children, each retained so the walk owns
// them. A failed or non-array answer means no children, which is the same as
// the element being a leaf.
func (c *Client) childRefs(ref uintptr) []uintptr {
	value, status := c.f.copyAttribute(ref, "AXChildren")
	if status != kAXErrorSuccess || value == 0 {
		return nil
	}

	defer c.f.cf.release(value)

	if c.f.cf.getTypeID(value) != c.f.cfArrayGetTypeID() {
		return nil
	}

	members := c.f.arrayValues(value)
	children := make([]uintptr, 0, len(members))

	for _, member := range members {
		if member == 0 {
			continue
		}

		children = append(children, c.f.cf.retain(member))
	}

	return children
}

// accumulateSearchText fills every node's search text with the text of its own
// element and of its whole subtree, in one post-order pass.
func accumulateSearchText(node *treeNode) string {
	if node == nil {
		return ""
	}

	var builder strings.Builder
	seen := make(map[string]struct{}, 4)

	appendSearchText(&builder, seen, node.info.title)
	appendSearchText(&builder, seen, node.info.description)
	appendSearchText(&builder, seen, node.info.value)

	for _, child := range node.children {
		appendSearchText(&builder, seen, accumulateSearchText(child))
	}

	node.search = builder.String()

	return node.search
}

// appendSearchText adds one non-empty, not-yet-seen string to the builder.
func appendSearchText(builder *strings.Builder, seen map[string]struct{}, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	if _, duplicate := seen[text]; duplicate {
		return
	}

	if builder.Len() > 0 {
		builder.WriteByte(' ')
	}

	builder.WriteString(text)
	seen[text] = struct{}{}
}

// collectClickable walks the built tree and returns the elements that pass the
// role, state and visibility checks, in tree order. The root itself is not a
// candidate: a caller asks for what is clickable inside the window or
// application it passed.
func (c *Client) collectClickable(
	ctx context.Context,
	node *treeNode,
	opts *walkOptions,
	out *[]*treeNode,
	stats *walkStats,
) {
	if node == nil || ctx.Err() != nil {
		return
	}

	if node.info != nil && node.info.matchesRole(opts.roles) && c.isClickable(node, opts) {
		node.kept = true
		stats.kept++

		*out = append(*out, node)
	}

	for _, child := range node.children {
		c.collectClickable(ctx, child, opts, out, stats)
	}
}

// isClickable applies the checks that decide whether a hint can land on the
// element: it must be visible, enabled, on screen, and (unless the caller
// disabled the check) actually be the element the window server reports at its
// center. The last check is what keeps an obscured button under a modal sheet
// from receiving a hint.
func (c *Client) isClickable(node *treeNode, opts *walkOptions) bool {
	info := node.info
	if info == nil || info.hidden || !info.visible {
		return false
	}

	if info.hasEnabled && !info.enabled {
		return false
	}

	bounds := info.bounds()
	if bounds.Empty() {
		return false
	}

	if !bounds.Overlaps(opts.screen) {
		return false
	}

	if !opts.hitTestVisibility {
		return true
	}

	return c.hitTestVisible(node.ref, image.Pt(
		bounds.Min.X+bounds.Dx()/2,
		bounds.Min.Y+bounds.Dy()/2,
	))
}

// hitTestVisible reports whether the element, or a descendant of it, is what
// the accessibility server returns at point. Walking up to sixteen parents
// covers web content that wraps a link's text in anonymous static-text nodes,
// while staying cheap.
func (c *Client) hitTestVisible(target uintptr, point image.Point) bool {
	system := c.systemWideElement()
	if system == 0 {
		// Without a system-wide element the hit test cannot run; trusting the
		// tree is better than dropping every hint.
		return true
	}

	var hit uintptr

	status := c.f.ax.copyElementAtPoint(system, float32(point.X), float32(point.Y), &hit)
	if status != kAXErrorSuccess || hit == 0 {
		return false
	}

	current := hit

	for range 16 {
		if c.f.cf.equal(current, target) != 0 {
			cfRelease(current)

			return true
		}

		parent, parentStatus := c.f.copyAttribute(current, "AXParent")
		cfRelease(current)

		if parentStatus != kAXErrorSuccess || parent == 0 {
			return false
		}

		current = parent
	}

	cfRelease(current)

	return false
}

// releaseTree drops every reference the tree still owns, except nodes marked
// kept, whose references become the returned nodes' responsibility.
func releaseTree(node *treeNode) {
	if node == nil {
		return
	}

	for _, child := range node.children {
		releaseTree(child)
	}

	if !node.kept && node.ref != 0 {
		cfRelease(node.ref)
		node.ref = 0
	}

	node.children = nil
	node.parent = nil
}
