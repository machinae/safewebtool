package browser

import (
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/machinae/safewebtool/internal/rod"
	"github.com/machinae/safewebtool/internal/rod/lib/proto"
)

// testDocID is the fixed document id unit tests mint and resolve under;
// identity tests that need a navigation use a second, different value.
const testDocID = "doc-test"

// axn is a compact AX node spec for identity tests. ChildIDs are derived
// from parent links in slice order, so specs stay flat and readable.
type axn struct {
	id      string
	parent  string
	role    string
	name    string
	backend int
	ignored bool
}

func buildNodes(t *testing.T, specs []axn) []*proto.AccessibilityAXNode {
	t.Helper()
	byID := make(map[string]*proto.AccessibilityAXNode, len(specs))
	nodes := make([]*proto.AccessibilityAXNode, 0, len(specs))
	for _, s := range specs {
		n := &proto.AccessibilityAXNode{
			NodeID:           proto.AccessibilityAXNodeID(s.id),
			ParentID:         proto.AccessibilityAXNodeID(s.parent),
			BackendDOMNodeID: proto.DOMBackendNodeID(s.backend),
			Ignored:          s.ignored,
		}
		if s.role != "" {
			n.Role = axVal(s.role)
		}
		if s.name != "" {
			n.Name = axVal(s.name)
		}
		byID[s.id] = n
		nodes = append(nodes, n)
	}
	for _, s := range specs {
		if s.parent == "" {
			continue
		}
		p, ok := byID[s.parent]
		if !ok {
			t.Fatalf("spec %q references unknown parent %q", s.id, s.parent)
		}
		p.ChildIDs = append(p.ChildIDs, proto.AccessibilityAXNodeID(s.id))
	}
	return nodes
}

// idFor returns the element id assigned to the node with the given
// backendNodeId.
func idFor(t *testing.T, byID map[string]*proto.AccessibilityAXNode, backend int) string {
	t.Helper()
	for id, n := range byID {
		if int(n.BackendDOMNodeID) == backend {
			return id
		}
	}
	t.Fatalf("no element id for backend node %d", backend)
	return ""
}

// inboxSpecs is the canonical duplicate-elements fixture: two unnamed
// listitems, each with sender text and an identically named Archive button.
func inboxSpecs() []axn {
	return []axn{
		{id: "root", role: "RootWebArea", backend: 1},
		{id: "list", parent: "root", role: "list", backend: 2},
		{id: "li1", parent: "list", role: "listitem", backend: 3},
		{id: "t1", parent: "li1", role: "StaticText", name: "John Doe", backend: 4},
		{id: "b1", parent: "li1", role: "button", name: "Archive", backend: 5},
		{id: "li2", parent: "list", role: "listitem", backend: 6},
		{id: "t2", parent: "li2", role: "StaticText", name: "Jane Roe", backend: 7},
		{id: "b2", parent: "li2", role: "button", name: "Archive", backend: 8},
	}
}

// TestComputeIdentitiesStableAcrossRerender is the core property: a React
// re-render replaces every DOM node and AX node id, but content-hash ids
// survive because they depend only on content.
func TestComputeIdentitiesStableAcrossRerender(t *testing.T) {
	before, _ := computeIdentities(buildNodes(t, inboxSpecs()), testDocID)

	// Same logical page, every Chrome-side identifier different.
	specs := inboxSpecs()
	for i := range specs {
		specs[i].id += "-rerendered"
		if specs[i].parent != "" {
			specs[i].parent += "-rerendered"
		}
		specs[i].backend += 1000
	}
	after, _ := computeIdentities(buildNodes(t, specs), testDocID)

	if len(before) != len(after) {
		t.Fatalf("id count changed across re-render: %d vs %d", len(before), len(after))
	}
	for id, n := range before {
		m, ok := after[id]
		if !ok {
			t.Errorf("id %s (%s %q) missing after re-render", id, axStr(n.Role), axStr(n.Name))
			continue
		}
		if axStr(m.Role) != axStr(n.Role) || axStr(m.Name) != axStr(n.Name) {
			t.Errorf("id %s names %s %q before but %s %q after",
				id, axStr(n.Role), axStr(n.Name), axStr(m.Role), axStr(m.Name))
		}
	}
}

// TestComputeIdentitiesScopedToDocument: identical content on a different
// document (a navigation) must mint all-new ids, so a stale id can never
// resolve and act across a page load.
func TestComputeIdentitiesScopedToDocument(t *testing.T) {
	nodes := buildNodes(t, inboxSpecs())
	before, _ := computeIdentities(nodes, testDocID)
	after, _ := computeIdentities(nodes, "doc-after-navigation")

	if len(before) != len(after) {
		t.Fatalf("id count changed across documents: %d vs %d", len(before), len(after))
	}
	for id, n := range before {
		if after[id] != nil {
			t.Errorf("id %s (%s %q) survived a document change", id, axStr(n.Role), axStr(n.Name))
		}
	}
}

// TestComputeIdentitiesDeterministic locks down that the same tree always
// produces the same assignment (ordinals and collision handling follow
// document order, not map iteration).
func TestComputeIdentitiesDeterministic(t *testing.T) {
	nodes := buildNodes(t, inboxSpecs())
	_, first := computeIdentities(nodes, testDocID)
	_, second := computeIdentities(nodes, testDocID)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("two passes over the same tree disagree:\n%v\n%v", first, second)
	}
}

// TestComputeIdentitiesMapsAgree checks the two returned views describe the
// same assignment: every byAXNode entry round-trips through byID.
func TestComputeIdentitiesMapsAgree(t *testing.T) {
	byID, byAX := computeIdentities(buildNodes(t, inboxSpecs()), testDocID)
	if len(byID) != len(byAX) {
		t.Fatalf("byID has %d entries, byAXNode has %d", len(byID), len(byAX))
	}
	for axID, id := range byAX {
		n := byID[id]
		if n == nil {
			t.Errorf("byAXNode maps %s to %s, which byID does not know", axID, id)
			continue
		}
		if n.NodeID != axID {
			t.Errorf("id %s round-trips to AX node %s, want %s", id, n.NodeID, axID)
		}
	}
}

// TestComputeIdentitiesIDFormat: ids are exactly idLength lowercase hex chars.
func TestComputeIdentitiesIDFormat(t *testing.T) {
	byID, _ := computeIdentities(buildNodes(t, inboxSpecs()), testDocID)
	pattern := regexp.MustCompile(`^[0-9a-f]{7}$`)
	for id := range byID {
		if !pattern.MatchString(id) {
			t.Errorf("id %q is not 7 lowercase hex chars", id)
		}
	}
}

// TestComputeIdentitiesDuplicateNames: identically named elements in
// different containers get different ids (the anchor disambiguates), and an
// id survives its sibling container disappearing (anchored, not positional).
func TestComputeIdentitiesDuplicateNames(t *testing.T) {
	byID, _ := computeIdentities(buildNodes(t, inboxSpecs()), testDocID)
	john := idFor(t, byID, 5)
	jane := idFor(t, byID, 8)
	if john == jane {
		t.Fatalf("Archive buttons in different rows share id %s", john)
	}

	// Jane's row is gone; John's Archive must keep its id.
	after, _ := computeIdentities(buildNodes(t, inboxSpecs()[:5]), testDocID)
	if n := after[john]; n == nil || int(n.BackendDOMNodeID) != 5 {
		t.Errorf("John's Archive id %s did not survive Jane's row being removed", john)
	}
}

// TestComputeIdentitiesTwins: identical (role, name, anchor) elements in the
// same container are disambiguated by ordinal.
func TestComputeIdentitiesTwins(t *testing.T) {
	specs := []axn{
		{id: "root", role: "RootWebArea", backend: 1},
		{id: "li", parent: "root", role: "listitem", backend: 2},
		{id: "t", parent: "li", role: "StaticText", name: "John Doe", backend: 3},
		{id: "b1", parent: "li", role: "button", name: "Archive", backend: 4},
		{id: "b2", parent: "li", role: "button", name: "Archive", backend: 5},
	}
	byID, _ := computeIdentities(buildNodes(t, specs), testDocID)
	first := idFor(t, byID, 4)
	second := idFor(t, byID, 5)
	if first == second {
		t.Fatalf("twin buttons share id %s", first)
	}
}

// TestComputeIdentitiesEligibility: which nodes get ids at all. Eligibility
// follows what a ModeAll snapshot could display, so it is mode-independent.
func TestComputeIdentitiesEligibility(t *testing.T) {
	tests := []struct {
		name   string
		spec   axn
		wantID bool
	}{
		{"interactive element", axn{role: "button", name: "Go", backend: 10}, true},
		{"named static text", axn{role: "StaticText", name: "hello world", backend: 11}, true},
		{"generic container", axn{role: "generic", backend: 12}, false},
		{"ignored node", axn{role: "button", name: "Go", backend: 13, ignored: true}, false},
		{"no backend DOM node", axn{role: "button", name: "Go", backend: 0}, false},
		{"unnamed region", axn{role: "region", backend: 14}, false},
		{"named region", axn{role: "region", name: "Sidebar", backend: 15}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := tt.spec
			spec.id = "node"
			spec.parent = "root"
			nodes := buildNodes(t, []axn{{id: "root", role: "RootWebArea", backend: 1}, spec})
			byID, _ := computeIdentities(nodes, testDocID)

			got := false
			for _, n := range byID {
				if n == nodes[1] {
					got = true
				}
			}
			if got != tt.wantID {
				t.Errorf("node got id = %v, want %v", got, tt.wantID)
			}
		})
	}
}

// TestComputeIdentitiesAnchorSkipsCollapsedWrappers: wrapping an element in
// containers the snapshot collapses (generic, unnamed section) must not
// change its anchor, and therefore not its id.
func TestComputeIdentitiesAnchorSkipsCollapsedWrappers(t *testing.T) {
	plain := []axn{
		{id: "root", role: "RootWebArea", backend: 1},
		{id: "region", parent: "root", role: "region", name: "Sidebar", backend: 2},
		{id: "b", parent: "region", role: "button", name: "Go", backend: 3},
	}
	wrapped := []axn{
		{id: "root", role: "RootWebArea", backend: 1},
		{id: "region", parent: "root", role: "region", name: "Sidebar", backend: 2},
		{id: "div", parent: "region", role: "generic", backend: 4},
		{id: "section", parent: "div", role: "section", backend: 5},
		{id: "b", parent: "section", role: "button", name: "Go", backend: 3},
	}
	plainIDs, _ := computeIdentities(buildNodes(t, plain), testDocID)
	wrappedIDs, _ := computeIdentities(buildNodes(t, wrapped), testDocID)
	if got, want := idFor(t, wrappedIDs, 3), idFor(t, plainIDs, 3); got != want {
		t.Errorf("wrapping changed the button id: %s vs %s", got, want)
	}
}

// TestComputeIdentitiesAnchorCapExcludesVolatileTail: the anchor hashes only
// the first maxAnchorLength runes of the container summary, so changes past
// the cap (trailing timestamps, badges) do not churn ids.
func TestComputeIdentitiesAnchorCapExcludesVolatileTail(t *testing.T) {
	row := func(tail string) []axn {
		return []axn{
			{id: "root", role: "RootWebArea", backend: 1},
			{id: "li", parent: "root", role: "listitem", backend: 2},
			{id: "t", parent: "li", role: "StaticText", name: strings.Repeat("a", 40) + tail, backend: 3},
			{id: "b", parent: "li", role: "button", name: "Archive", backend: 4},
		}
	}
	morning, _ := computeIdentities(buildNodes(t, row(" at 9:00 AM")), testDocID)
	evening, _ := computeIdentities(buildNodes(t, row(" at 9:00 PM")), testDocID)
	if got, want := idFor(t, evening, 4), idFor(t, morning, 4); got != want {
		t.Errorf("text change past the anchor cap churned the button id: %s vs %s", got, want)
	}
}

// TestComputeIdentitiesNameFallback: an unnamed interactive element takes its
// identity from descendant static text, so different text means different id.
func TestComputeIdentitiesNameFallback(t *testing.T) {
	specs := []axn{
		{id: "root", role: "RootWebArea", backend: 1},
		{id: "b1", parent: "root", role: "button", backend: 2},
		{id: "t1", parent: "b1", role: "StaticText", name: "Save", backend: 3},
		{id: "b2", parent: "root", role: "button", backend: 4},
		{id: "t2", parent: "b2", role: "StaticText", name: "Cancel", backend: 5},
	}
	byID, _ := computeIdentities(buildNodes(t, specs), testDocID)
	if idFor(t, byID, 2) == idFor(t, byID, 4) {
		t.Errorf("unnamed buttons with different static text share an id")
	}
}

// TestSnapshotIDsModeIndependent: an id minted while rendering one mode is
// valid in any other, because identity always computes over the ModeAll
// superset.
func TestSnapshotIDsModeIndependent(t *testing.T) {
	nodes := buildNodes(t, inboxSpecs())
	def, err := NewPageSnapshot("https://example.com", "T", testDocID, nodes, ModeDefault)
	if err != nil {
		t.Fatalf("NewPageSnapshot: %v", err)
	}
	all, err := NewPageSnapshot("https://example.com", "T", testDocID, nodes, ModeAll)
	if err != nil {
		t.Fatalf("NewPageSnapshot: %v", err)
	}
	if !reflect.DeepEqual(def.ids, all.ids) {
		t.Errorf("ModeDefault and ModeAll minted different ids:\n%v\n%v", def.ids, all.ids)
	}
}

// TestSnapshotPrintsResolvableIDs ties the print side to the resolve side:
// every id a snapshot prints must look up, via a fresh identity pass over
// the same tree, to a node with the printed role. This is the invariant the
// whole refactor rests on.
func TestSnapshotPrintsResolvableIDs(t *testing.T) {
	nodes := buildNodes(t, inboxSpecs())
	ps, err := NewPageSnapshot("https://example.com", "Inbox", testDocID, nodes, ModeDefault)
	if err != nil {
		t.Fatalf("NewPageSnapshot: %v", err)
	}
	out := ps.String()

	byID, _ := computeIdentities(nodes, testDocID)
	linePattern := regexp.MustCompile(`(?m)^\s*\[([0-9a-f]{7})\] (\w+)`)
	matches := linePattern.FindAllStringSubmatch(out, -1)
	if len(matches) == 0 {
		t.Fatalf("no element ids in snapshot output:\n%s", out)
	}
	for _, m := range matches {
		id, role := m[1], m[2]
		node := byID[id]
		if node == nil {
			t.Errorf("snapshot printed id %s but resolution does not know it", id)
			continue
		}
		if axStr(node.Role) != role {
			t.Errorf("id %s prints as %s but resolves to %s", id, role, axStr(node.Role))
		}
	}
}

func TestHashID(t *testing.T) {
	base := hashID("button\x1fArchive\x1flistitem:John", 0, 0)
	if !regexp.MustCompile(`^[0-9a-f]{7}$`).MatchString(base) {
		t.Errorf("hashID returned %q, want 7 lowercase hex chars", base)
	}
	if again := hashID("button\x1fArchive\x1flistitem:John", 0, 0); again != base {
		t.Errorf("hashID is not deterministic: %s vs %s", base, again)
	}
	if next := hashID("button\x1fArchive\x1flistitem:John", 1, 0); next == base {
		t.Errorf("ordinal does not change the id")
	}
	if bumped := hashID("button\x1fArchive\x1flistitem:John", 0, 1); bumped == base {
		t.Errorf("collision bump does not change the id")
	}
}

// TestTranslateInteractableErr: rod's interactability errors map to the
// typed BrowserError set with the caller's target string in Sel.
func TestTranslateInteractableErr(t *testing.T) {
	target := `button "Submit"`

	var obscured *ErrObscured
	if err := translateInteractableErr(&rod.CoveredError{}, target); !errors.As(err, &obscured) || obscured.Sel != target {
		t.Errorf("covered: got %#v, want ErrObscured with Sel %q", err, target)
	} else if obscured.Blocker != "" {
		t.Errorf("covered with nil element: Blocker = %q, want empty", obscured.Blocker)
	}

	var notVisible *ErrNotVisible
	if err := translateInteractableErr(&rod.InvisibleShapeError{}, target); !errors.As(err, &notVisible) || notVisible.Sel != target {
		t.Errorf("invisible: got %#v, want ErrNotVisible with Sel %q", err, target)
	}

	var noPointer *ErrNoPointerEvents
	if err := translateInteractableErr(&rod.NoPointerEventsError{}, target); !errors.As(err, &noPointer) || noPointer.Sel != target {
		t.Errorf("no pointer events: got %#v, want ErrNoPointerEvents with Sel %q", err, target)
	}

	plain := errors.New("boom")
	if err := translateInteractableErr(plain, target); !errors.Is(err, plain) || !strings.Contains(err.Error(), target) {
		t.Errorf("unknown error: got %v, want the original wrapped and the target named", err)
	}
}

func TestNormalizeText(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain text untouched", "Sign in", "Sign in"},
		{"zero-width chars stripped", "Sign" + string(rune(0x200B)) + string(rune(0x200C)) + string(rune(0x200D)) + string(rune(0x2060)) + " in", "Sign in"},
		{"BOM stripped", string(rune(0xFEFF)) + "Sign in", "Sign in"},
		{"nbsp becomes space", "Sign" + string(rune(0x00A0)) + "in", "Sign in"},
		{"whitespace runs collapse", "  Sign \t\n in  ", "Sign in"},
		{"combining grapheme joiner stripped", "Sale" + string(rune(0x034F)) + " 50%", "Sale 50%"},
		{"preheader CGJ padding collapses to nothing", "Code" + strings.Repeat(string(rune(0x034F))+" ", 8), "Code"},
		{"bidi isolates stripped", "and " + string(rune(0x2066)) + "1" + string(rune(0x2069)) + " more item", "and 1 more item"},
		{"soft hyphen stripped", "Mic" + string(rune(0x00AD)) + "rosoft", "Microsoft"},
		{"emoji and variation selector kept", "Star " + string(rune(0x2B50)) + string(rune(0xFE0F)), "Star " + string(rune(0x2B50)) + string(rune(0xFE0F))},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeText(tt.input); got != tt.want {
				t.Errorf("normalizeText(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
