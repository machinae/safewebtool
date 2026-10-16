package browser

// resolve.go owns element identity: the content-hash ids snapshots print
// and the stateless resolution that turns an id back into a live element.
// An id is the first 7 hex chars of sha256(role|name|anchor|ordinal), so
// it names what the model saw, not a DOM node; React replacing the node
// behind identical content does not invalidate it. Resolution recomputes
// identities against a fresh accessibility tree on every action: an id
// resolves if its content exists right now, no matter which snapshot
// minted it. No per-tab state, no fuzzy matching: every resolution is
// exact-by-content or a loud error (plans/snapshot-refactor.md).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/machinae/safewebtool/internal/rod"
	"github.com/machinae/safewebtool/internal/rod/lib/proto"
)

// --- Identity ---

// idLength is the rendered id length: the first idLength hex chars of
// the tuple hash. Collisions between distinct tuples are resolved
// deterministically in computeIdentities.
const idLength = 7

// maxAnchorLength caps anchor text so the hash keeps the stable head of
// a container's summary (a row's leading text) and excludes volatile
// tails (trailing timestamps, badges).
const maxAnchorLength = 32

// identitySep separates tuple fields in the hash input so field contents
// cannot run into each other.
const identitySep = "\x1f"

// computeIdentities assigns a content-hash id to every node a snapshot
// could display in any mode (classify under ModeAll, the superset), so
// ids are mode-independent. It returns the id-to-node lookup resolution
// uses and the node-to-id lookup snapshot rendering uses. Pure function
// over the node slice; no CDP. Iteration follows the slice (Chrome emits
// the tree in document order), which makes ordinals and collision
// handling deterministic for a given tree.
//
// docID scopes ids to one document (see documentID): after a navigation
// every id changes, so an id minted before it can never match
// same-looking content on the new page and act there by accident.
func computeIdentities(nodes []*proto.AccessibilityAXNode, docID string) (map[string]*proto.AccessibilityAXNode, map[proto.AccessibilityAXNodeID]string) {
	nodeMap := make(map[proto.AccessibilityAXNodeID]*proto.AccessibilityAXNode, len(nodes))
	childMap := make(map[proto.AccessibilityAXNodeID][]proto.AccessibilityAXNodeID, len(nodes))
	for _, n := range nodes {
		if n == nil {
			continue
		}
		nodeMap[n.NodeID] = n
		if len(n.ChildIDs) > 0 {
			childMap[n.NodeID] = n.ChildIDs
		}
	}

	byID := make(map[string]*proto.AccessibilityAXNode)
	byAXNode := make(map[proto.AccessibilityAXNodeID]string)
	ordinals := make(map[string]int)
	anchors := make(map[proto.AccessibilityAXNodeID]string)

	for _, n := range nodes {
		if n == nil || n.Ignored || n.BackendDOMNodeID == 0 {
			continue
		}
		role := axStr(n.Role)
		kind := classify(role, axStr(n.Name) != "", ModeAll)
		if kind == kindCollapse {
			continue
		}

		tuple := docID + identitySep +
			role + identitySep +
			identityName(n, role, kind, nodeMap, childMap) + identitySep +
			identityAnchor(n, nodeMap, childMap, anchors)
		ordinal := ordinals[tuple]
		ordinals[tuple] = ordinal + 1

		id := hashID(tuple, ordinal, 0)
		for bump := 1; byID[id] != nil; bump++ {
			id = hashID(tuple, ordinal, bump)
		}
		byID[id] = n
		byAXNode[n.NodeID] = id
	}
	return byID, byAXNode
}

// identityName mirrors walkTree's display-name rules so identity and
// display agree: the accessible name, falling back to the synthetic
// summary for unnamed content containers and to descendant static text
// for unnamed interactive/info nodes. Normalized, capped at the render
// cap.
func identityName(
	node *proto.AccessibilityAXNode,
	role string,
	kind nodeKind,
	nodeMap map[proto.AccessibilityAXNodeID]*proto.AccessibilityAXNode,
	childMap map[proto.AccessibilityAXNodeID][]proto.AccessibilityAXNodeID,
) string {
	name := axStr(node.Name)
	if name == "" {
		switch kind {
		case kindStructural:
			if contentContainerRoles[role] {
				name = collectContainerText(node.NodeID, nodeMap, childMap)
			}
		case kindInteractive, kindInfo:
			name = collectStaticText(node.NodeID, nodeMap, childMap)
		}
	}
	return truncate(normalizeText(name), maxTextLength)
}

// identityAnchor returns "role:text" for the node's nearest structural
// ancestor, the container line the element indents under in a snapshot.
// text is the container's name, or its summary for unnamed content
// containers, normalized and capped at maxAnchorLength runes. Empty when
// no structural ancestor exists. The anchor is what gives duplicate
// elements per-container identity ("the Archive button in John's row")
// instead of a positional one. anchors caches text per container so
// siblings don't recompute it.
func identityAnchor(
	node *proto.AccessibilityAXNode,
	nodeMap map[proto.AccessibilityAXNodeID]*proto.AccessibilityAXNode,
	childMap map[proto.AccessibilityAXNodeID][]proto.AccessibilityAXNodeID,
	anchors map[proto.AccessibilityAXNodeID]string,
) string {
	for cur := nodeMap[node.ParentID]; cur != nil; cur = nodeMap[cur.ParentID] {
		if cur.Ignored {
			continue
		}
		role := axStr(cur.Role)
		if classify(role, axStr(cur.Name) != "", ModeAll) != kindStructural {
			continue
		}
		if a, ok := anchors[cur.NodeID]; ok {
			return a
		}
		text := axStr(cur.Name)
		if text == "" && contentContainerRoles[role] {
			text = collectContainerText(cur.NodeID, nodeMap, childMap)
		}
		text = normalizeText(text)
		if runes := []rune(text); len(runes) > maxAnchorLength {
			text = string(runes[:maxAnchorLength])
		}
		a := role + ":" + text
		anchors[cur.NodeID] = a
		return a
	}
	return ""
}

// hashID hashes one identity tuple down to an id. bump is 0 normally and
// counts up only when two distinct tuples truncate to the same id.
func hashID(tuple string, ordinal, bump int) string {
	key := tuple + identitySep + strconv.Itoa(ordinal)
	if bump > 0 {
		key += identitySep + strconv.Itoa(bump)
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:idLength]
}

// normalizeText canonicalizes text for hashing and snapshot display:
// invisible characters stripped, nbsp treated as space, whitespace runs
// collapsed to one space, trimmed. Keeps invisible markup changes from
// churning ids and out of snapshot output.
//
// The strip covers two classes. unicode.Cf (format) catches the whole
// zero-width family (ZWSP/ZWNJ/ZWJ, word joiner, BOM), soft hyphen, and the
// bidi marks and isolates (LRM/RLM, LRI/PDI) \u2014 email preheaders stuff these
// in as padding. U+034F COMBINING GRAPHEME JOINER is the other common padding
// char but is a nonspacing mark (Mn), not Cf, so it gets its own case.
// Variation selectors are also Mn but are deliberately kept so emoji render.
func normalizeText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\u034F' || unicode.Is(unicode.Cf, r) {
			return -1
		}
		if r == '\u00A0' { // nbsp: fold to a normal space
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// --- Resolution ---

// idResolution locates a public element id inside the frame-aware snapshot
// tree. A bare id resolves locally; a dotted "a.b.leaf" walks the frame chain.
type idResolution struct {
	owner    *PageSnapshot              // frame snapshot the element lives in (ps itself for a main-frame id)
	node     *proto.AccessibilityAXNode // the element's AX node, within owner's maps
	idPrefix string                     // dotted frame path to prepend when printing ("" for a main-frame id)
}

// resolveElementID is the single place a dotted id is parsed, so every
// id-consuming call site (render roots, the loud-miss check, action/element
// resolution) agrees on where an element lives. A bare id resolves against the
// receiver's local byID map, exactly as before. "a.b.leaf" splits on "." and
// walks childFrames: each leading segment is an iframe handle (its own
// content-hash in the parent frame), the last segment is the element. ok is
// false if any segment is absent.
//
// idPrefix is returned because the frame path is the chain of parent iframe
// hashes living across separate snapshots; a caller cannot rebuild it without
// re-splitting on "." (which would put dot-parsing in two places), and walkTree
// cannot reconstruct it from the leaf node alone.
func (ps *PageSnapshot) resolveElementID(id string) (idResolution, bool) {
	if !strings.Contains(id, ".") {
		node := ps.byID[id]
		if node == nil {
			return idResolution{}, false
		}
		return idResolution{owner: ps, node: node}, true
	}

	segments := strings.Split(id, ".")
	cur := ps
	var prefix strings.Builder
	for _, seg := range segments[:len(segments)-1] {
		frameNode := cur.byID[seg]
		if frameNode == nil {
			return idResolution{}, false
		}
		child := cur.childFrames[frameNode.NodeID]
		if child == nil {
			return idResolution{}, false
		}
		prefix.WriteString(seg)
		prefix.WriteByte('.')
		cur = child
	}

	leaf := cur.byID[segments[len(segments)-1]]
	if leaf == nil {
		return idResolution{}, false
	}
	return idResolution{owner: cur, node: leaf, idPrefix: prefix.String()}, true
}

// resolveElement finds the element a snapshot id names and returns it
// wrapped with the role and name of the matched AX node. Stateless: it
// fetches the live accessibility tree and matches the id by content, so
// an id stays valid across re-renders while its element exists, and an
// id from any earlier snapshot of the document resolves if its content
// is on the page right now. A miss is retried by findByIDRetry,
// covering an earlier action's render still being mid-flight.
func (t *Tab) resolveElement(ctx context.Context, id string) (*Element, error) {
	node, err := t.findByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if node == nil {
		if node, err = t.findByIDRetry(ctx, id); err != nil {
			return nil, err
		}
	}
	if node == nil {
		return nil, &ErrNoElement{ID: id}
	}

	el, err := t.page.Context(ctx).ElementFromNode(&proto.DOMNode{
		BackendNodeID: node.BackendDOMNodeID,
	})
	if err != nil {
		return nil, fmt.Errorf("element %q just left the page: %w", id, err)
	}

	// rod's auto-waits (WaitVisible inside WaitStable, WaitEnabled inside
	// Input, WaitInteractable) poll until their predicate holds, bounded
	// only by ctx — on a detached or hidden node that is a hang for the
	// whole action timeout. NotFoundSleeper makes any not-ready predicate
	// fail on its first check instead. Fail fast and loud; recovery is
	// re-resolution at our layer (resolvePrepared), not blind waiting.
	el = el.Sleeper(rod.NotFoundSleeper)

	return &Element{
		el:   el,
		Role: axStr(node.Role),
		Name: axStr(node.Name),
	}, nil
}

// resolvePrepared resolves an id and runs prepare on the element,
// retrying the pair once if prepare fails. Between resolution and input
// dispatch the page can re-render and detach the node (WaitStable's
// quiet period, ScrollIntoView on a virtualized list), which would
// surface a raw CDP error for an element that still exists.
// Re-resolution is by content, so a retry can only find the same
// logical element again or fail loudly, never retarget. prepare must be
// free of input dispatch; callers dispatch exactly once after this
// returns.
func (t *Tab) resolvePrepared(ctx context.Context, id string, prepare func(el *Element) error) (*Element, error) {
	for attempt := 0; ; attempt++ {
		el, err := t.resolveElement(ctx, id)
		if err != nil {
			return nil, err
		}
		err = prepare(el)
		if err == nil {
			return el, nil
		}
		if attempt == 1 || ctx.Err() != nil {
			return nil, err
		}
	}
}

// findByID fetches the live accessibility tree and looks the id up among
// freshly computed identities. A nil node with nil error means the id
// matches nothing on the page right now. The doc id is read after the
// tree: if a navigation lands between the two calls, the old tree gets
// the new doc id and nothing matches (a loud miss), instead of the old
// doc id validating same-looking content on the new page.
func (t *Tab) findByID(ctx context.Context, id string) (*proto.AccessibilityAXNode, error) {
	// Composite id: descend the frame tree and walk the frame path. This costs
	// a few extra CDP calls (the same descent the snapshot does), so it is
	// reserved for ids that actually name a framed element.
	if strings.Contains(id, ".") {
		ps, err := t.captureSnapshot(ctx, ModeDefault)
		if err != nil {
			return nil, err
		}
		if r, ok := ps.resolveElementID(id); ok {
			return r.node, nil
		}
		return nil, nil
	}

	// Bare id: lives in the main frame, so resolve against the main tree alone
	// without descending frames.
	result, err := proto.AccessibilityGetFullAXTree{}.Call(t.page.Context(ctx))
	if err != nil {
		return nil, fmt.Errorf("getting accessibility tree: %w", err)
	}
	docID, err := t.documentID(ctx)
	if err != nil {
		return nil, err
	}
	byID, _ := computeIdentities(result.Nodes, docID)
	return byID[id], nil
}

// findByIDRetry polls findByID until the id resolves or PageSettleTimeout
// expires, returning nil on a final miss. A miss usually means an earlier
// action's re-render is still mid-flight with the content briefly absent.
// We poll for the element itself rather than waiting for DOM quiet: a
// static loading placeholder is quiet too, so a settle wait can declare
// the page stable while the content is still on its way back.
func (t *Tab) findByIDRetry(ctx context.Context, id string) (*proto.AccessibilityAXNode, error) {
	deadline := time.After(PageSettleTimeout)
	ticker := time.NewTicker(pageSettleTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, nil
		case <-ticker.C:
			node, err := t.findByID(ctx, id)
			if err != nil || node != nil {
				return node, err
			}
		}
	}
}

// documentID returns the loader id of the tab's current document: fresh
// on every document load (including same-URL reloads), unchanged across
// re-renders and pushState route changes. It is hashed into element ids
// so an id is only ever valid on the document it was minted from.
func (t *Tab) documentID(ctx context.Context) (string, error) {
	tree, err := proto.PageGetFrameTree{}.Call(t.page.Context(ctx))
	if err != nil {
		return "", fmt.Errorf("getting frame tree: %w", err)
	}
	return string(tree.FrameTree.Frame.LoaderID), nil
}

// axStr extracts the string value from an AXValue, or "" if nil/empty.
func axStr(v *proto.AccessibilityAXValue) string {
	if v == nil {
		return ""
	}
	return v.Value.Str()
}

// translateInteractableErr maps Rod's interactability errors to our typed
// BrowserError set. The target string is whatever the caller wants surfaced
// in the error's Sel field, typically el.String() once the element has
// been resolved, so logs show "combobox \"Where to?\"" rather than a
// raw element id.
func translateInteractableErr(err error, target string) error {
	var covered *rod.CoveredError
	if errors.As(err, &covered) {
		return &ErrObscured{Sel: target, Blocker: blockerDesc(covered.Element)}
	}
	var invisible *rod.InvisibleShapeError
	if errors.As(err, &invisible) {
		return &ErrNotVisible{Sel: target}
	}
	var noPointer *rod.NoPointerEventsError
	if errors.As(err, &noPointer) {
		return &ErrNoPointerEvents{Sel: target}
	}
	return fmt.Errorf("element not interactable: %s: %w", target, err)
}

// blockerDesc describes a covering element by its accessibility role and
// name, e.g. `dialog "Cookie consent"`, so ErrObscured can say what is in
// the way (most often a cookie banner or modal). Best-effort enrichment of
// an error already being returned: any failure here means "", which falls
// back to ErrObscured's generic message.
func blockerDesc(el *rod.Element) string {
	if el == nil {
		return ""
	}
	node, err := el.Describe(0, false)
	if err != nil {
		return ""
	}
	tree, err := proto.AccessibilityGetPartialAXTree{BackendNodeID: node.BackendNodeID}.Call(el)
	if err != nil {
		return ""
	}
	for _, ax := range tree.Nodes {
		if ax == nil || ax.BackendDOMNodeID != node.BackendNodeID || ax.Ignored {
			continue
		}
		role := axStr(ax.Role)
		name := axStr(ax.Name)
		switch {
		case role != "" && name != "":
			return fmt.Sprintf("%s %q", role, name)
		case role != "" && role != "generic" && role != "none":
			return role
		}
		return ""
	}
	return ""
}
