package browser

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/machinae/safewebtool/internal/rod/lib/proto"
)

// maxSnapshotDepth limits AX tree recursion to avoid runaway nesting.
const maxSnapshotDepth = 10

// maxTextLength is the maximum rune length for any text content in snapshots.
// All text (names, summaries, labels) is truncated to this length. If the
// model needs full text, it requests the group's markdown or html output for
// the element.
const maxTextLength = 160

// maxURLLength is the maximum rune length for link URLs in snapshots.
const maxURLLength = 40

// SnapshotMode controls the verbosity of the snapshot output.
//
// TODO: ModeInteractive and ModeAll are no longer reachable from the MCP
// server (groups declare an "output"; snapshots are always ModeDefault).
// Delete the modes and the mode parameter.
type SnapshotMode int

const (
	// ModeDefault shows interactive elements, structural grouping, headings,
	// labels, status/alert, and content-container summaries.
	ModeDefault SnapshotMode = iota
	// ModeInteractive shows only interactive elements and structural grouping.
	ModeInteractive
	// ModeAll shows everything in default plus text-bearing content.
	ModeAll
)

// Snapshot returns a token-efficient text representation of the page. f
// scopes and filters the output: the include roots come from f's element
// ids and AX selectors (united), exclude subtracts, and match filters the
// rendered lines. The zero value renders the whole page. Element ids are
// computed over the full tree, so scoped and full snapshots mint identical
// ids; an id absent from the page is a loud miss so the caller re-snapshots.
func (t *Tab) Snapshot(ctx context.Context, mode SnapshotMode, f OutputFilter) (string, error) {
	return t.enqueue(ctx, func() (string, error) { return t.doSnapshot(ctx, mode, f) })
}

func (t *Tab) doSnapshot(ctx context.Context, mode SnapshotMode, f OutputFilter) (string, error) {
	waitPageSettle(ctx, t.page)
	ps, err := t.snapshotWithIDs(ctx, mode, f.IncludeElementIDs)
	if err != nil {
		return "", err
	}
	out, err := applySection(ps.Filtered(f), f.Section, sectionSnapshot)
	if err != nil {
		return "", err
	}
	return applyMatch(out, f.Match)
}

// snapshotWithIDs captures the AX tree (the one fetch a read already does)
// and requires every requested element id to be present in it. A missing
// id is a loud ErrNoElement, not a wait: a read errors so the caller
// re-snapshots, unlike an action, which waits for the element. ids may be
// empty, in which case it is a plain capture.
func (t *Tab) snapshotWithIDs(ctx context.Context, mode SnapshotMode, ids []string) (*PageSnapshot, error) {
	ps, err := t.captureSnapshot(ctx, mode)
	if err != nil {
		return nil, err
	}

	// A same-document SPA navigation can poison the document's accessibility
	// cache: the tree comes back with no interactive elements even though the
	// DOM is full. Reload once to rebuild it — but only for an unscoped read.
	// A scoped read names ids hashed against the current document id, and a
	// reload makes a new document id, so the requested id could never survive
	// the reload. A scoped miss instead falls through to ErrNoElement below and
	// the caller re-snapshots unscoped, which recovers here.
	if len(ids) == 0 && !ps.hasInteractive() {
		ps, err = t.reloadAndCapture(ctx, mode)
		if err != nil {
			return nil, err
		}
	}

	if missing := ps.missingIDs(ids); len(missing) > 0 {
		return nil, &ErrNoElement{ID: missing[0]}
	}
	return ps, nil
}

// reloadAndCapture reloads the page to rebuild a poisoned accessibility cache,
// waits for the new document to load and settle, then recaptures. The
// recapture is terminal: its result is returned as-is, so a page that is
// genuinely empty (not poisoned) reloads at most once and never loops.
func (t *Tab) reloadAndCapture(ctx context.Context, mode SnapshotMode) (*PageSnapshot, error) {
	if err := t.page.Context(ctx).Reload(); err != nil {
		return nil, fmt.Errorf("reloading to rebuild accessibility tree: %w", err)
	}
	// Reload commits the new document but does not wait for load, so settle
	// before recapturing. WaitLoad is best-effort, like waitPageSettle: a
	// timeout means the page is still loading and we recapture whatever exists
	// rather than fail — its error is dropped deliberately, not silently.
	_ = t.page.Context(ctx).WaitLoad()
	waitPageSettle(ctx, t.page)
	return t.captureSnapshot(ctx, mode)
}

// captureSnapshot does the reads a snapshot is built from — the page
// url/title, the per-frame document ids, and the accessibility tree — and
// parses them into a frame-aware PageSnapshot. The frame tree (which carries
// the document id) is read before the AX tree so a navigation landing between
// them mints the new tree under the old doc id (a loud miss) rather than
// letting stale ids resolve on the new document.
//
// This is the single frame-discovery path, shared by snapshot rendering and id
// resolution, so the two never disagree on which frames exist or how their ids
// are computed. Same-origin (same-process) child frames are descended inline;
// cross-origin frames are marked as placeholders (phase 1).
func (t *Tab) captureSnapshot(ctx context.Context, mode SnapshotMode) (*PageSnapshot, error) {
	page := t.page.Context(ctx)

	infoRes, err := proto.TargetGetTargetInfo{TargetID: proto.TargetTargetID(t.targetID)}.Call(page)
	if err != nil {
		return nil, fmt.Errorf("getting page info: %w", err)
	}

	frameTree, err := proto.PageGetFrameTree{}.Call(page)
	if err != nil {
		return nil, fmt.Errorf("getting frame tree: %w", err)
	}
	frames := make(map[proto.PageFrameID]*proto.PageFrame)
	flattenFrameTree(frameTree.FrameTree, frames)
	mainDocID := string(frameTree.FrameTree.Frame.LoaderID)

	result, err := proto.AccessibilityGetFullAXTree{}.Call(page)
	if err != nil {
		return nil, fmt.Errorf("getting accessibility tree: %w", err)
	}

	ps, err := NewPageSnapshot(infoRes.TargetInfo.URL, infoRes.TargetInfo.Title, mainDocID, result.Nodes, mode)
	if err != nil {
		return nil, err
	}
	if err := t.descendFrames(ctx, ps, frames, mode, 0); err != nil {
		return nil, err
	}
	return ps, nil
}

// descendFrames walks ps for iframe nodes and, for each same-origin frame,
// fetches its accessibility tree and links a child PageSnapshot (recursing into
// nested frames). Discovery is AX-driven: one DOM.describeNode on the iframe's
// backend node yields both the child frame id (to fetch the AX tree) and the
// presence of a content document (same-origin vs out-of-process). Cross-origin
// frames are marked and not fetched. Bounded by maxSnapshotDepth.
func (t *Tab) descendFrames(ctx context.Context, ps *PageSnapshot, frames map[proto.PageFrameID]*proto.PageFrame, mode SnapshotMode, depth int) error {
	if depth >= maxSnapshotDepth {
		return nil
	}
	page := t.page.Context(ctx)
	// nodeOrder gives a deterministic walk (Chrome emits document order).
	for _, nodeID := range ps.nodeOrder {
		node := ps.nodeMap[nodeID]
		if node == nil || axStr(node.Role) != roleIframe || node.BackendDOMNodeID == 0 {
			continue
		}

		desc, err := proto.DOMDescribeNode{BackendNodeID: node.BackendDOMNodeID}.Call(page)
		if err != nil {
			// Can't describe it: treat as opaque, render a bare placeholder.
			ps.markCrossOrigin(nodeID, "")
			continue
		}
		frameID := desc.Node.FrameID
		label := frameLabel(frames[frameID])

		// No content document on this session means an out-of-process frame
		// (OOPIF). Phase 1 does not expand it.
		if frameID == "" || desc.Node.ContentDocument == nil {
			ps.markCrossOrigin(nodeID, label)
			continue
		}

		childTree, err := proto.AccessibilityGetFullAXTree{FrameID: frameID}.Call(page)
		if err != nil {
			// The frame claims a content document but its tree is unreadable
			// from this session: treat as cross-origin rather than fail.
			ps.markCrossOrigin(nodeID, label)
			continue
		}

		childDocID := ""
		if f := frames[frameID]; f != nil {
			childDocID = string(f.LoaderID)
		}
		childURL := ""
		if f := frames[frameID]; f != nil {
			childURL = f.URL
		}
		child, err := NewPageSnapshot(childURL, "", childDocID, childTree.Nodes, mode)
		if err != nil {
			// An empty or still-loading child document: nothing to link.
			continue
		}
		ps.linkFrame(nodeID, child, label)
		if err := t.descendFrames(ctx, child, frames, mode, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// flattenFrameTree collects every frame in the tree into into, keyed by frame
// id, so the per-frame loader id (the frame-scoped doc id) and url/host label
// are one lookup away.
func flattenFrameTree(tree *proto.PageFrameTree, into map[proto.PageFrameID]*proto.PageFrame) {
	if tree == nil || tree.Frame == nil {
		return
	}
	into[tree.Frame.ID] = tree.Frame
	for _, child := range tree.ChildFrames {
		flattenFrameTree(child, into)
	}
}

// frameLabel is the host fallback label for an iframe line, used when the
// iframe has no accessible name. Empty when the frame is unknown or its url has
// no host (e.g. about:blank).
func frameLabel(frame *proto.PageFrame) string {
	if frame == nil {
		return ""
	}
	if u, err := url.Parse(frame.URL); err == nil && u.Host != "" {
		return u.Host
	}
	return ""
}

// hasInteractive reports whether any non-ignored node carries an interactive
// role. A populated page has many; none means the snapshot collapsed to
// nothing actionable, the signature of a poisoned accessibility cache.
func (ps *PageSnapshot) hasInteractive() bool {
	// A same-origin frame holds its content in its own snapshot, so a page
	// whose interactive elements all live inside a frame is not poisoned.
	if len(ps.childFrames) > 0 {
		return true
	}
	for _, n := range ps.nodeMap {
		if n == nil || n.Ignored {
			continue
		}
		if interactiveRoles[axStr(n.Role)] {
			return true
		}
	}
	return false
}

// nodeKind classifies how a node should be handled during tree walking.
type nodeKind int

const (
	kindCollapse    nodeKind = iota // don't show, recurse children at same depth
	kindStructural                  // show + recurse children at depth+1
	kindInteractive                 // show as leaf (no recursion)
	kindInfo                        // show as leaf (no recursion)
	kindFrame                       // iframe boundary: show line, recurse into child frame
)

// roleIframe is the AX role Chrome reports for an <iframe> owner element.
const roleIframe = "Iframe"

// structuralRoles are container roles that provide page structure.
var structuralRoles = map[string]bool{
	"form":          true,
	"navigation":    true,
	"main":          true,
	"dialog":        true,
	"alertdialog":   true,
	"banner":        true,
	"contentinfo":   true,
	"complementary": true,
	"search":        true,
	"toolbar":       true,
	"menu":          true,
	"menubar":       true,
	"list":          true,
	"listbox":       true,
	"listitem":      true,
	"table":         true,
	"row":           true,
	"tree":          true,
	"tablist":       true,
	"region":        true,
	"group":         true,
	"article":       true,
	"section":       true,
}

// interactiveRoles are elements the user can act on.
var interactiveRoles = map[string]bool{
	"button":            true,
	"link":              true,
	"textbox":           true,
	"searchbox":         true,
	"combobox":          true,
	"checkbox":          true,
	"radio":             true,
	"slider":            true,
	"spinbutton":        true,
	"switch":            true,
	"menuitem":          true,
	"menuitemcheckbox":  true,
	"menuitemradio":     true,
	"option":            true,
	"tab":               true,
	"treeitem":          true,
	"textarea":          true,
	"select":            true,
	"ProgressIndicator": true,
}

// contentContainerRoles are structural containers that represent one repeated
// content unit (inbox row, search result, product card, table row). When
// unnamed, these get a synthetic summary from descendant text.
var contentContainerRoles = map[string]bool{
	"listitem": true,
	"article":  true,
	"row":      true,
}

// classify determines how a node should be rendered based on its AX role and
// the current snapshot mode. Interactive roles are always shown. Info roles
// (headings, status messages, text content) are progressively included as the
// mode moves from interactive → default → all.
func classify(role string, hasName bool, mode SnapshotMode) nodeKind {
	// An iframe is a frame boundary: it renders a line and recurses into the
	// child frame, and (unlike a collapsed node) it gets a content-hash id so
	// the id can serve as the frame handle. It is deliberately not in
	// interactiveRoles — it is a container, not a click target.
	if role == roleIframe {
		return kindFrame
	}
	if interactiveRoles[role] {
		return kindInteractive
	}

	// Info roles vary by mode: interactive shows none, default adds
	// headings/labels/images/status/alert, all adds text content on top.
	switch mode {
	case ModeInteractive:
		// Strip all info nodes — only interactive elements and structure.
	case ModeDefault:
		if role == "heading" || role == "LabelText" {
			return kindInfo
		}
		if role == "img" && hasName {
			return kindInfo
		}
		if role == "status" || role == "alert" {
			return kindInfo
		}
	case ModeAll:
		if role == "heading" || role == "LabelText" {
			return kindInfo
		}
		if role == "img" && hasName {
			return kindInfo
		}
		if role == "status" || role == "alert" {
			return kindInfo
		}
		// Text-bearing content only in all mode.
		if role == "paragraph" || role == "blockquote" || role == "code" || role == "StaticText" {
			return kindInfo
		}
	}

	if structuralRoles[role] {
		// Unnamed region/group/section are too generic to be useful — collapse
		// them so their children bubble up without extra nesting.
		if (role == "region" || role == "group" || role == "section") && !hasName {
			return kindCollapse
		}
		return kindStructural
	}
	return kindCollapse
}

// PageSnapshot holds the parsed accessibility tree and page metadata.
type PageSnapshot struct {
	URL   string
	Title string
	// Origin is scheme+host (e.g. "https://example.com"), used to convert
	// same-origin link URLs to relative paths. Empty if the page URL could
	// not be parsed.
	Origin   string
	Mode     SnapshotMode
	nodeMap  map[proto.AccessibilityAXNodeID]*proto.AccessibilityAXNode
	childMap map[proto.AccessibilityAXNodeID][]proto.AccessibilityAXNodeID
	rootID   proto.AccessibilityAXNodeID
	// ids maps AX nodes to the content-hash element ids the snapshot
	// prints, computed once per snapshot (see computeIdentities).
	ids map[proto.AccessibilityAXNodeID]string
	// byID is the reverse lookup, for scoped rendering.
	byID map[string]*proto.AccessibilityAXNode
	// nodeOrder lists node ids in document order, for AX-scope selector
	// matching (Chrome emits the tree in document order).
	nodeOrder []proto.AccessibilityAXNodeID
	// excluded is the set of AX-scope exclude roots for the current walk:
	// walkTree prunes any node in it. Set only during Filtered; nil
	// otherwise (a nil map reads as all-false).
	excluded map[proto.AccessibilityAXNodeID]bool

	// childFrames links each same-origin iframe AX node to the frame snapshot
	// captured for its content. The key is the iframe's AX node id in this
	// (parent) snapshot, so an "abcdef" prefix segment goes
	// byID["abcdef"].NodeID -> childFrames[...]. Each child is its own
	// PageSnapshot with its own maps, so AX node ids never collide across
	// frames. nil until the capture descent links a frame.
	childFrames map[proto.AccessibilityAXNodeID]*PageSnapshot
	// crossOrigin marks iframe AX nodes that are out-of-process (OOPIF) and so
	// not expanded in phase 1. They render an honest placeholder line.
	crossOrigin map[proto.AccessibilityAXNodeID]bool
	// frameLabel holds a host fallback label per iframe AX node, used when the
	// iframe has no accessible name (so a nameless frame line still means
	// something).
	frameLabel map[proto.AccessibilityAXNodeID]string
}

// linkFrame attaches a captured same-origin child frame under the iframe AX
// node that owns it and records the iframe's host fallback label.
func (ps *PageSnapshot) linkFrame(iframeNode proto.AccessibilityAXNodeID, child *PageSnapshot, label string) {
	if ps.childFrames == nil {
		ps.childFrames = make(map[proto.AccessibilityAXNodeID]*PageSnapshot)
	}
	ps.childFrames[iframeNode] = child
	ps.setFrameLabel(iframeNode, label)
}

// markCrossOrigin records an iframe AX node as an unexpanded out-of-process
// frame and stores its host label for the placeholder line.
func (ps *PageSnapshot) markCrossOrigin(iframeNode proto.AccessibilityAXNodeID, label string) {
	if ps.crossOrigin == nil {
		ps.crossOrigin = make(map[proto.AccessibilityAXNodeID]bool)
	}
	ps.crossOrigin[iframeNode] = true
	ps.setFrameLabel(iframeNode, label)
}

func (ps *PageSnapshot) setFrameLabel(iframeNode proto.AccessibilityAXNodeID, label string) {
	if label == "" {
		return
	}
	if ps.frameLabel == nil {
		ps.frameLabel = make(map[proto.AccessibilityAXNodeID]string)
	}
	ps.frameLabel[iframeNode] = label
}

// NewPageSnapshot builds a PageSnapshot from raw AX tree nodes. docID
// scopes the minted element ids to the current document (see documentID).
func NewPageSnapshot(pageURL, title, docID string, nodes []*proto.AccessibilityAXNode, mode SnapshotMode) (*PageSnapshot, error) {
	filtered := make([]*proto.AccessibilityAXNode, 0, len(nodes))
	for _, n := range nodes {
		if n != nil {
			filtered = append(filtered, n)
		}
	}
	if len(filtered) == 0 {
		return nil, fmt.Errorf("accessibility tree is empty")
	}

	nodeMap := make(map[proto.AccessibilityAXNodeID]*proto.AccessibilityAXNode, len(filtered))
	childMap := make(map[proto.AccessibilityAXNodeID][]proto.AccessibilityAXNodeID, len(filtered))
	for _, n := range filtered {
		nodeMap[n.NodeID] = n
		if len(n.ChildIDs) > 0 {
			childMap[n.NodeID] = n.ChildIDs
		}
	}

	var rootID proto.AccessibilityAXNodeID
	for _, n := range filtered {
		if n.ParentID == "" {
			rootID = n.NodeID
			break
		}
	}

	var origin string
	if u, err := url.Parse(pageURL); err == nil && u.Host != "" {
		origin = strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
	}

	byID, ids := computeIdentities(filtered, docID)

	order := make([]proto.AccessibilityAXNodeID, len(filtered))
	for i, n := range filtered {
		order[i] = n.NodeID
	}

	return &PageSnapshot{
		URL:       pageURL,
		Title:     title,
		Origin:    origin,
		Mode:      mode,
		nodeMap:   nodeMap,
		childMap:  childMap,
		rootID:    rootID,
		ids:       ids,
		byID:      byID,
		nodeOrder: order,
	}, nil
}

// String renders the text snapshot. Implements fmt.Stringer.
func (ps *PageSnapshot) String() string {
	var buf strings.Builder
	ps.walkTree(&buf, ps.rootID, 0, false, "")
	return buf.String()
}

// Scoped renders only the subtree rooted at the element id names: the
// same lines a full snapshot indents under it, re-based to depth zero.
// ok is false when the id has no element in this snapshot.
func (ps *PageSnapshot) Scoped(id string) (out string, ok bool) {
	r, ok := ps.resolveElementID(id)
	if !ok {
		return "", false
	}
	var buf strings.Builder
	r.owner.walkTree(&buf, r.node.NodeID, 0, true, r.idPrefix)
	return buf.String(), true
}

// Filtered renders the snapshot under the filter's scope: only nodes
// beneath the include roots (element ids and AX selectors, united; the
// whole page when none are given), with any nodes beneath the exclude
// selectors removed. Includes and exclude compose. An include that matches
// nothing yields empty output; an exclude that matches nothing removes
// nothing. Match is not applied here (it runs once on the final rendered
// string for every mode).
func (ps *PageSnapshot) Filtered(f OutputFilter) string {
	includeRoots := ps.includeRootIDs(f)
	ps.excluded = ps.selectSet(f.ExcludeAXScope)
	defer func() { ps.excluded = nil }()

	var buf strings.Builder
	if !f.hasIncludeScope() {
		ps.walkTree(&buf, ps.rootID, 0, false, "")
	} else {
		for _, r := range includeRoots {
			r.owner.walkTree(&buf, r.node.NodeID, 0, true, r.idPrefix)
		}
	}
	return buf.String()
}

// includeRootIDs gathers the roots to render: the element ids present in the
// frame-aware tree (each resolved to the frame it lives in) plus the nodes
// matching the AX include selectors (main frame only), with nested roots
// pruned so a region inside another is not rendered twice. Absent element ids
// are silently skipped here; callers that need a miss to be loud check
// missingIDs first (see snapshotWithIDs).
func (ps *PageSnapshot) includeRootIDs(f OutputFilter) []idResolution {
	var roots []idResolution
	for _, id := range f.IncludeElementIDs {
		if r, ok := ps.resolveElementID(id); ok {
			roots = append(roots, r)
		}
	}
	for _, nid := range ps.selectNodes(f.IncludeAXScope) {
		roots = append(roots, idResolution{owner: ps, node: ps.nodeMap[nid]})
	}
	return ps.pruneNestedRoots(roots)
}

// pruneNestedRoots drops a root whose ancestor is also a root in the same
// frame, so a region nested inside another included region is rendered once.
// Ancestry is per-tree, so roots are grouped by owner; roots in different
// frames live in different snapshots and cannot nest.
func (ps *PageSnapshot) pruneNestedRoots(roots []idResolution) []idResolution {
	if len(roots) < 2 {
		return roots
	}
	perOwner := make(map[*PageSnapshot]map[proto.AccessibilityAXNodeID]bool)
	for _, r := range roots {
		if perOwner[r.owner] == nil {
			perOwner[r.owner] = make(map[proto.AccessibilityAXNodeID]bool)
		}
		perOwner[r.owner][r.node.NodeID] = true
	}
	out := roots[:0]
	for _, r := range roots {
		if r.owner.hasAncestorInSet(r.node.NodeID, perOwner[r.owner]) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// missingIDs returns the element ids absent from the frame-aware tree.
func (ps *PageSnapshot) missingIDs(ids []string) []string {
	var missing []string
	for _, id := range ids {
		if _, ok := ps.resolveElementID(id); !ok {
			missing = append(missing, id)
		}
	}
	return missing
}

// selectNodes returns the ids of nodes matching any of the AX selectors,
// in document order. No selectors yields no matches.
func (ps *PageSnapshot) selectNodes(selectors []string) []proto.AccessibilityAXNodeID {
	if len(selectors) == 0 {
		return nil
	}
	sels := make([]axSelector, len(selectors))
	for i, s := range selectors {
		sels[i] = parseAXSelector(s)
	}
	var out []proto.AccessibilityAXNodeID
	for _, id := range ps.nodeOrder {
		n := ps.nodeMap[id]
		if n == nil {
			continue
		}
		role, name := axStr(n.Role), axStr(n.Name)
		for _, sel := range sels {
			if sel.matches(role, name) {
				out = append(out, id)
				break
			}
		}
	}
	return out
}

// selectSet is selectNodes as a set, for exclude-root membership tests.
func (ps *PageSnapshot) selectSet(selectors []string) map[proto.AccessibilityAXNodeID]bool {
	ids := ps.selectNodes(selectors)
	if len(ids) == 0 {
		return nil
	}
	set := make(map[proto.AccessibilityAXNodeID]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

// pruneNested drops ids whose ancestor is also in the set, so an included
// region nested inside another included region is rendered once, not twice.
func (ps *PageSnapshot) pruneNested(ids []proto.AccessibilityAXNodeID) []proto.AccessibilityAXNodeID {
	if len(ids) < 2 {
		return ids
	}
	set := make(map[proto.AccessibilityAXNodeID]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	out := ids[:0]
	for _, id := range ids {
		if !ps.hasAncestorInSet(id, set) {
			out = append(out, id)
		}
	}
	return out
}

// hasAncestorInSet reports whether any ancestor of id is in set.
func (ps *PageSnapshot) hasAncestorInSet(id proto.AccessibilityAXNodeID, set map[proto.AccessibilityAXNodeID]bool) bool {
	n := ps.nodeMap[id]
	if n == nil {
		return false
	}
	for cur := ps.nodeMap[n.ParentID]; cur != nil; cur = ps.nodeMap[cur.ParentID] {
		if set[cur.NodeID] {
			return true
		}
	}
	return false
}

// ancestorsOf returns every ancestor of every node in set: the nodes that
// straddle an excluded subtree and so must be descended into rather than
// serialized whole. Used by the markdown/html scope path.
func (ps *PageSnapshot) ancestorsOf(set map[proto.AccessibilityAXNodeID]bool) map[proto.AccessibilityAXNodeID]bool {
	if len(set) == 0 {
		return nil
	}
	out := make(map[proto.AccessibilityAXNodeID]bool)
	for id := range set {
		n := ps.nodeMap[id]
		if n == nil {
			continue
		}
		for cur := ps.nodeMap[n.ParentID]; cur != nil; cur = ps.nodeMap[cur.ParentID] {
			if out[cur.NodeID] {
				break // this chain to the root is already recorded
			}
			out[cur.NodeID] = true
		}
	}
	return out
}

// isCollapsed reports whether a node carries an explicit collapsed state
// (the "expanded" property present and not "true"). It mirrors the condition
// under which formatNode prints the "collapsed" marker, so a node shown as
// collapsed is exactly one whose hidden children the walk prunes.
func isCollapsed(node *proto.AccessibilityAXNode) bool {
	val, exists := nodePropertyValue(node, "expanded")
	return exists && val != "true"
}

// walkTree recursively walks the AX tree, writing snapshot lines for relevant
// nodes. Returns true if the subtree contains "retainable" content — in default
// and interactive modes that means interactive elements; in all mode it also
// includes info nodes. Structural containers are only emitted when at least one
// retainable descendant exists (prevents empty nav/form/list noise).
func (ps *PageSnapshot) walkTree(buf *strings.Builder, nodeID proto.AccessibilityAXNodeID, depth int, isScopeRoot bool, idPrefix string) bool {
	if depth > maxSnapshotDepth {
		return false
	}

	node, ok := ps.nodeMap[nodeID]
	if !ok {
		return false
	}

	// AX-scope exclude root: prune the whole subtree from the output.
	if ps.excluded[nodeID] {
		return false
	}

	// Ignored nodes: don't show, but recurse children at same depth.
	if node.Ignored {
		hasInteractive := false
		for _, childID := range ps.childMap[nodeID] {
			if ps.walkTree(buf, childID, depth, false, idPrefix) {
				hasInteractive = true
			}
		}
		return hasInteractive
	}

	role := axStr(node.Role)
	name := axStr(node.Name)
	kind := classify(role, name != "", ps.Mode)

	switch kind {
	case kindFrame:
		return ps.walkFrame(buf, node, name, depth, idPrefix)

	case kindStructural:
		// Write children to a temp buffer first. We only emit the container
		// line if children produced retainable content — otherwise the
		// container is silently pruned from output.
		var childBuf strings.Builder
		hasRetainable := false
		for _, childID := range ps.childMap[nodeID] {
			if ps.walkTree(&childBuf, childID, depth+1, false, idPrefix) {
				hasRetainable = true
			}
		}

		// For unnamed content containers (listitem, article, row), compute a
		// synthetic summary from non-interactive descendant text.
		if name == "" && contentContainerRoles[role] && ps.Mode != ModeInteractive {
			name = collectContainerText(nodeID, ps.nodeMap, ps.childMap)
		}

		if !hasRetainable {
			// Content containers can be retained by their summary alone:
			// - In -a mode: all content containers with a non-empty summary
			// - In default mode: only table header rows (rows with columnheader children)
			retain := false
			if contentContainerRoles[role] && name != "" {
				if ps.Mode == ModeAll {
					retain = true
				} else if ps.Mode == ModeDefault && role == "row" && ps.isHeaderRow(nodeID) {
					retain = true
				}
			}
			if !retain {
				return false
			}
			// Summary-only: emit the container line without children to
			// avoid duplicating text that's already in the summary.
			ps.formatNode(buf, node, role, name, depth, idPrefix)
			return true
		}

		ps.formatNode(buf, node, role, name, depth, idPrefix)
		buf.WriteString(childBuf.String())
		return true

	case kindInteractive, kindInfo:
		if name == "" {
			name = collectStaticText(nodeID, ps.nodeMap, ps.childMap)
		}
		// Normalize before truncating so invisible padding (e.g. preheader
		// CGJ runs) does not consume the rune budget and evict real text.
		name = normalizeText(name)
		if kind == kindInfo && name == "" {
			// A nameless info node prints no line of its own, but its
			// subtree can still hold interactive content (an icon-only
			// button inside an unnamed alert). Walk it transparently,
			// like the default branch, so those children are not lost.
			hasRetainable := false
			for _, childID := range ps.childMap[nodeID] {
				if ps.walkTree(buf, childID, depth, false, idPrefix) {
					hasRetainable = true
				}
			}
			return hasRetainable
		}
		name = truncate(name, maxTextLength)
		ps.formatNode(buf, node, role, name, depth, idPrefix)
		// Recurse into children so nested interactive elements aren't lost
		// (e.g. a search textbox inside a heading). Exception: a collapsed
		// interactive node (closed combobox/select, submenu, collapsed tree
		// branch) hides its children from the user, so skip them — the line
		// already carries its value and the "collapsed" marker. When this node
		// is the scope root the model explicitly read (an includeElementIds
		// scope), render its full contents so the hidden options can be
		// discovered in one turn.
		skipChildren := kind == kindInteractive && !isScopeRoot && isCollapsed(node)
		hasRetainable := false
		if !skipChildren {
			for _, childID := range ps.childMap[nodeID] {
				if ps.walkTree(buf, childID, depth+1, false, idPrefix) {
					hasRetainable = true
				}
			}
		}
		if kind == kindInteractive {
			return true
		}
		if role == "status" || role == "alert" {
			return true
		}
		// An info node with retainable descendants must report them, or an
		// ancestor prunes this subtree with the descendants' lines already
		// rendered inside it (a link inside a heading inside a card).
		return hasRetainable || ps.Mode == ModeAll

	default:
		hasInteractive := false
		for _, childID := range ps.childMap[nodeID] {
			if ps.walkTree(buf, childID, depth, false, idPrefix) {
				hasInteractive = true
			}
		}
		return hasInteractive
	}
}

// walkFrame renders an iframe boundary line and, for a same-origin frame,
// recurses into the child frame's snapshot so its content prints inline with
// composite ids. The child's elements carry idPrefix + the iframe's own hash +
// "." so each printed id is a self-contained, copy-pasteable token.
//
// A same-origin frame line is emitted only when the child frame has retainable
// content (mirrors the structural "only show if it carries signal" rule). A
// cross-origin frame is a placeholder shown only when the iframe is named or
// has a host label, since nameless out-of-process frames are usually tracking
// pixels.
func (ps *PageSnapshot) walkFrame(buf *strings.Builder, node *proto.AccessibilityAXNode, name string, depth int, idPrefix string) bool {
	nodeID := node.NodeID
	if name == "" {
		name = ps.frameLabel[nodeID]
	}

	child := ps.childFrames[nodeID]
	if ps.crossOrigin[nodeID] || child == nil {
		// Cross-origin (or otherwise unexpandable): a placeholder, shown only
		// if it carries a label so nameless tracking frames stay out.
		if name == "" {
			return false
		}
		ps.formatFrameNode(buf, node, name, depth, idPrefix, true)
		return true
	}

	// Same-origin: render the child to a temp buffer first; emit the boundary
	// line only if the child produced retainable content.
	framePrefix := idPrefix + ps.ids[nodeID] + "."
	var childBuf strings.Builder
	if !child.walkTree(&childBuf, child.rootID, depth+1, false, framePrefix) {
		return false
	}
	ps.formatFrameNode(buf, node, name, depth, idPrefix, false)
	buf.WriteString(childBuf.String())
	return true
}

// formatFrameNode writes the single boundary line for an iframe:
// "[prefix.hash] iframe \"Label\"", plus the not-expanded marker for a
// cross-origin frame.
func (ps *PageSnapshot) formatFrameNode(buf *strings.Builder, node *proto.AccessibilityAXNode, name string, depth int, idPrefix string, crossOrigin bool) {
	indent := strings.Repeat("  ", depth)
	name = normalizeText(name)
	if id := ps.ids[node.NodeID]; id != "" {
		fmt.Fprintf(buf, "%s[%s] iframe", indent, idPrefix+id)
	} else {
		fmt.Fprintf(buf, "%siframe", indent)
	}
	if name != "" {
		fmt.Fprintf(buf, " %q", name)
	}
	if crossOrigin {
		buf.WriteString(" (cross-origin, not expanded)")
	}
	buf.WriteByte('\n')
}

// formatNode writes a single snapshot line for a node. All text content is
// run through normalizeText so invisible characters (zero-width spaces,
// BOMs, nbsp) never reach the output.
func (ps *PageSnapshot) formatNode(buf *strings.Builder, node *proto.AccessibilityAXNode, role, name string, depth int, idPrefix string) {
	indent := strings.Repeat("  ", depth)
	name = normalizeText(name)

	roleStr := role
	if role == "LabelText" {
		roleStr = "label"
	}
	if role == "StaticText" {
		roleStr = "text"
	}
	if role == "heading" {
		if lvl := nodeProperty(node, "level"); lvl != "" {
			roleStr = fmt.Sprintf("heading[%s]", lvl)
		}
	}

	if id := ps.ids[node.NodeID]; id != "" {
		fmt.Fprintf(buf, "%s[%s] %s", indent, idPrefix+id, roleStr)
	} else {
		fmt.Fprintf(buf, "%s%s", indent, roleStr)
	}

	if name != "" {
		fmt.Fprintf(buf, " %q", name)
	}

	// valuetext is the human-readable form of a range widget's value
	// (e.g. "50%" for a slider whose raw value is 50); prefer it.
	val := normalizeText(axStr(node.Value))
	if vt := normalizeText(nodeProperty(node, "valuetext")); vt != "" {
		val = vt
	}
	if val != "" {
		fmt.Fprintf(buf, " value=%q", val)
	}
	if vmin := nodeProperty(node, "valuemin"); vmin != "" {
		fmt.Fprintf(buf, " min=%s", vmin)
	}
	if vmax := nodeProperty(node, "valuemax"); vmax != "" {
		fmt.Fprintf(buf, " max=%s", vmax)
	}

	if linkURL := nodeProperty(node, "url"); linkURL != "" {
		linkURL = stripQueryString(linkURL)
		linkURL = ps.relativizeURL(linkURL)
		fmt.Fprintf(buf, " url=%q", truncate(linkURL, maxURLLength))
	}

	if popup := nodeProperty(node, "hasPopup"); popup != "" && popup != "false" {
		fmt.Fprintf(buf, " haspopup=%s", popup)
	}

	if nodePropertyBool(node, "focused") {
		buf.WriteString(" focused")
	}
	if nodePropertyBool(node, "checked") {
		buf.WriteString(" checked")
	}
	if nodePropertyBool(node, "pressed") {
		buf.WriteString(" pressed")
	}
	if nodePropertyBool(node, "disabled") {
		buf.WriteString(" disabled")
	}
	if nodePropertyBool(node, "readonly") {
		buf.WriteString(" readonly")
	}
	if nodePropertyBool(node, "required") {
		buf.WriteString(" required")
	}
	if nodePropertyBool(node, "selected") {
		buf.WriteString(" selected")
	}
	if nodePropertyBool(node, "invalid") {
		buf.WriteString(" invalid")
	}
	if nodePropertyBool(node, "modal") {
		buf.WriteString(" modal")
	}
	if nodePropertyBool(node, "multiline") {
		buf.WriteString(" multiline")
	}
	if nodePropertyBool(node, "multiselectable") {
		buf.WriteString(" multiselectable")
	}
	if nodePropertyBool(node, "busy") {
		buf.WriteString(" busy")
	}
	// Distinguish expanded (true), collapsed (false), and absent (no property).
	// nodePropertyBool can't tell false from absent, so check existence first.
	if val, exists := nodePropertyValue(node, "expanded"); exists {
		if val == "true" {
			buf.WriteString(" expanded")
		} else {
			buf.WriteString(" collapsed")
		}
	}

	buf.WriteByte('\n')
}

// isHeaderRow returns true if a row contains at least one columnheader child.
func (ps *PageSnapshot) isHeaderRow(nodeID proto.AccessibilityAXNodeID) bool {
	for _, childID := range ps.childMap[nodeID] {
		child, ok := ps.nodeMap[childID]
		if !ok {
			continue
		}
		if axStr(child.Role) == "columnheader" {
			return true
		}
	}
	return false
}

// collectStaticText recursively collects text from StaticText descendants.
// Used to resolve the display text for nodes like LabelText where Chrome puts
// the visible text in StaticText children rather than the node's own name.
func collectStaticText(
	nodeID proto.AccessibilityAXNodeID,
	nodeMap map[proto.AccessibilityAXNodeID]*proto.AccessibilityAXNode,
	childMap map[proto.AccessibilityAXNodeID][]proto.AccessibilityAXNodeID,
) string {
	var parts []string
	for _, childID := range childMap[nodeID] {
		child, ok := nodeMap[childID]
		if !ok {
			continue
		}
		if axStr(child.Role) == "StaticText" {
			if t := axStr(child.Name); t != "" {
				parts = append(parts, t)
			}
		} else {
			// Recurse into formatting wrappers (e.g. <strong>, <em>).
			if t := collectStaticText(childID, nodeMap, childMap); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "")
}

// collectContainerText builds a synthetic summary for content containers
// (listitem, article, row) by gathering non-interactive text from descendants.
// Gives the LLM enough context to identify repeated units (inbox rows, search
// results, product cards) without showing every StaticText node — e.g. a Gmail
// listitem produces "John Doe | Mar 10" while its interactive children
// (checkbox, subject link) are shown as separate lines.
//
// Rules:
//   - Interactive children are skipped (already shown as their own lines).
//   - Whitespace-only or single-char StaticText is skipped (decorative bullets).
//   - Non-interactive, non-StaticText children are recursed into.
//   - Fragments are joined with " | " and truncated at maxTextLength.
func collectContainerText(
	nodeID proto.AccessibilityAXNodeID,
	nodeMap map[proto.AccessibilityAXNodeID]*proto.AccessibilityAXNode,
	childMap map[proto.AccessibilityAXNodeID][]proto.AccessibilityAXNodeID,
) string {
	var fragments []string
	collectContainerTextWalk(nodeID, nodeMap, childMap, &fragments)
	joined := strings.Join(fragments, " | ")
	return truncate(joined, maxTextLength)
}

// collectContainerTextWalk is the recursive helper for collectContainerText.
func collectContainerTextWalk(
	nodeID proto.AccessibilityAXNodeID,
	nodeMap map[proto.AccessibilityAXNodeID]*proto.AccessibilityAXNode,
	childMap map[proto.AccessibilityAXNodeID][]proto.AccessibilityAXNodeID,
	fragments *[]string,
) {
	for _, childID := range childMap[nodeID] {
		child, ok := nodeMap[childID]
		if !ok {
			continue
		}
		role := axStr(child.Role)

		// Skip interactive elements entirely — their text is already shown
		// as separate child lines in the snapshot output.
		if interactiveRoles[role] {
			continue
		}

		if role == "StaticText" {
			// Normalize before measuring so invisible padding does not count
			// toward the fragment's length or the joined summary's budget; a
			// pure-padding fragment normalizes to "" and is skipped here.
			text := normalizeText(axStr(child.Name))
			// Skip whitespace-only and single-char decorative text
			// (bullets, separators, icons, etc.).
			if len([]rune(text)) > 1 {
				*fragments = append(*fragments, text)
			}
			continue
		}

		// Recurse into non-interactive wrappers (generic divs, spans,
		// formatting elements) to pick up their StaticText descendants.
		collectContainerTextWalk(childID, nodeMap, childMap, fragments)
	}
}

// truncate truncates s to maxLen runes, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}

// nodeProperty returns the string value of a named property, or "".
func nodeProperty(node *proto.AccessibilityAXNode, name proto.AccessibilityAXPropertyName) string {
	for _, p := range node.Properties {
		if p.Name == name && p.Value != nil {
			return p.Value.Value.Str()
		}
	}
	return ""
}

// nodePropertyBool returns true if a named boolean property is "true".
func nodePropertyBool(node *proto.AccessibilityAXNode, name proto.AccessibilityAXPropertyName) bool {
	return nodeProperty(node, name) == "true"
}

// nodePropertyValue returns the string value and existence of a named property.
// Unlike nodeProperty, this distinguishes "property absent" from "property set
// to empty/false" — used for expanded/collapsed where absent and false mean
// different things.
func nodePropertyValue(node *proto.AccessibilityAXNode, name proto.AccessibilityAXPropertyName) (string, bool) {
	for _, p := range node.Properties {
		if p.Name == name && p.Value != nil {
			return p.Value.Value.Str(), true
		}
	}
	return "", false
}

// relativizeURL converts a same-origin URL to a relative path by stripping the origin prefix.
func (ps *PageSnapshot) relativizeURL(rawURL string) string {
	if ps.Origin != "" && strings.HasPrefix(rawURL, ps.Origin) {
		return rawURL[len(ps.Origin):]
	}
	return rawURL
}

// stripQueryString removes the query string and fragment from a URL.
func stripQueryString(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
