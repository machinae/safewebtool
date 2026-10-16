package browser

import (
	"strings"
	"testing"

	"github.com/machinae/safewebtool/internal/rod/lib/proto"
)

// frameMainTree is a page whose main document holds one <iframe> under main.
// The iframe carries the given accessible name (empty for the host-label case).
func frameMainTree(iframeName string) []*proto.AccessibilityAXNode {
	iframe := &proto.AccessibilityAXNode{NodeID: "ifr", ParentID: "main", Role: axVal("Iframe"), BackendDOMNodeID: 10}
	if iframeName != "" {
		iframe.Name = axVal(iframeName)
	}
	return []*proto.AccessibilityAXNode{
		{NodeID: "root", Role: axVal("RootWebArea"), ChildIDs: []proto.AccessibilityAXNodeID{"main"}},
		{NodeID: "main", ParentID: "root", Role: axVal("main"), BackendDOMNodeID: 1, ChildIDs: []proto.AccessibilityAXNodeID{"ifr"}},
		iframe,
	}
}

// frameChildTree is the content of the embedded same-origin frame: a button
// and a textbox the parent never sees in its own AX tree.
func frameChildTree() []*proto.AccessibilityAXNode {
	return []*proto.AccessibilityAXNode{
		{NodeID: "croot", Role: axVal("RootWebArea"), ChildIDs: []proto.AccessibilityAXNodeID{"cbtn", "ctext"}},
		{NodeID: "cbtn", ParentID: "croot", Role: axVal("button"), Name: axVal("Pay now"), BackendDOMNodeID: 20},
		{NodeID: "ctext", ParentID: "croot", Role: axVal("textbox"), Name: axVal("Card number"), BackendDOMNodeID: 21},
	}
}

// childDocID is the embedded frame's loader id: distinct from the main
// document's testDocID, so a child element's suffix is independent of the parent.
const childDocID = "doc-child"

// TestSameOriginFrameRendersInline is the phase-1 happy path: a same-origin
// iframe expands inline, its elements carry the composite "<iframe>.<element>"
// id, and that composite id resolves back to the leaf node in the child frame.
func TestSameOriginFrameRendersInline(t *testing.T) {
	parent, err := NewPageSnapshot("https://shop.example.com", "Shop", testDocID, frameMainTree("Checkout"), ModeDefault)
	if err != nil {
		t.Fatalf("parent NewPageSnapshot: %v", err)
	}
	child, err := NewPageSnapshot("https://shop.example.com/pay", "", childDocID, frameChildTree(), ModeDefault)
	if err != nil {
		t.Fatalf("child NewPageSnapshot: %v", err)
	}
	parent.linkFrame("ifr", child, "shop.example.com")

	frameID := parent.ids["ifr"]
	leafID := child.ids["cbtn"]
	if frameID == "" || leafID == "" {
		t.Fatalf("expected ids for iframe (%q) and child button (%q)", frameID, leafID)
	}

	// The boundary line prints the iframe's bare hash; the child element prints
	// the full composite so the model copies one self-contained token.
	out := parent.String()
	wantFrame := "[" + frameID + `] iframe "Checkout"`
	wantLeaf := "[" + frameID + "." + leafID + `] button "Pay now"`
	for _, want := range []string{wantFrame, wantLeaf} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in render:\n%s", want, out)
		}
	}

	// The composite id resolves to the leaf node inside the child snapshot.
	r, ok := parent.resolveElementID(frameID + "." + leafID)
	if !ok {
		t.Fatalf("composite id %s.%s did not resolve", frameID, leafID)
	}
	if r.owner != child || r.node.NodeID != "cbtn" || r.idPrefix != frameID+"." {
		t.Errorf("resolution = {owner=%p node=%s prefix=%q}, want {child=%p cbtn %q.}",
			r.owner, r.node.NodeID, r.idPrefix, child, frameID)
	}

	// A loud miss: a well-formed composite whose leaf is absent is reported
	// missing, not silently dropped.
	if missing := parent.missingIDs([]string{frameID + "." + leafID, frameID + ".deadbee"}); len(missing) != 1 || missing[0] != frameID+".deadbee" {
		t.Errorf("missingIDs = %v, want just the absent composite", missing)
	}

	// Scoping to the composite id renders just that element's subtree.
	scoped := parent.Filtered(OutputFilter{IncludeElementIDs: []string{frameID + "." + leafID}})
	if !strings.Contains(scoped, wantLeaf) {
		t.Errorf("scoped render missing %q:\n%s", wantLeaf, scoped)
	}
	if strings.Contains(scoped, "Card number") {
		t.Errorf("scoped render should not include the sibling textbox:\n%s", scoped)
	}
}

// TestFrameHostLabelFallback: a nameless same-origin iframe still gets a
// meaningful line from its host label.
func TestFrameHostLabelFallback(t *testing.T) {
	parent, err := NewPageSnapshot("https://shop.example.com", "Shop", testDocID, frameMainTree(""), ModeDefault)
	if err != nil {
		t.Fatalf("parent NewPageSnapshot: %v", err)
	}
	child, err := NewPageSnapshot("https://embed.example.com/pay", "", childDocID, frameChildTree(), ModeDefault)
	if err != nil {
		t.Fatalf("child NewPageSnapshot: %v", err)
	}
	parent.linkFrame("ifr", child, "embed.example.com")

	out := parent.String()
	want := "[" + parent.ids["ifr"] + `] iframe "embed.example.com"`
	if !strings.Contains(out, want) {
		t.Errorf("expected host-label line %q in render:\n%s", want, out)
	}
}

// TestCrossOriginFramePlaceholder is the phase-1 error case: a cross-origin
// frame renders an honest placeholder and is not expanded. A nameless,
// label-less cross-origin frame is suppressed entirely (tracking-pixel noise).
func TestCrossOriginFramePlaceholder(t *testing.T) {
	parent, err := NewPageSnapshot("https://shop.example.com", "Shop", testDocID, frameMainTree("Video"), ModeDefault)
	if err != nil {
		t.Fatalf("parent NewPageSnapshot: %v", err)
	}
	parent.markCrossOrigin("ifr", "player.vimeo.com")

	out := parent.String()
	want := "[" + parent.ids["ifr"] + `] iframe "Video" (cross-origin, not expanded)`
	if !strings.Contains(out, want) {
		t.Errorf("expected placeholder %q in render:\n%s", want, out)
	}
	if strings.Contains(out, "Pay now") {
		t.Errorf("cross-origin frame must not expand child content:\n%s", out)
	}

	// A nameless, label-less cross-origin frame is dropped, not shown empty.
	nameless, err := NewPageSnapshot("https://shop.example.com", "Shop", testDocID, frameMainTree(""), ModeDefault)
	if err != nil {
		t.Fatalf("nameless NewPageSnapshot: %v", err)
	}
	nameless.markCrossOrigin("ifr", "")
	if got := nameless.String(); strings.Contains(got, "iframe") {
		t.Errorf("nameless cross-origin frame should be suppressed, got:\n%s", got)
	}
}

// TestNestedFrameResolves: a frame inside a frame produces an "a.b.leaf"
// composite id that resolves through two childFrames hops and renders with the
// full prefix.
func TestNestedFrameResolves(t *testing.T) {
	// Outer main tree: iframe "Outer" (ifr, backend 10).
	outer, err := NewPageSnapshot("https://a.example.com", "A", testDocID, frameMainTree("Outer"), ModeDefault)
	if err != nil {
		t.Fatalf("outer NewPageSnapshot: %v", err)
	}
	// Middle frame: a RootWebArea holding another iframe "Inner" (mifr, backend 30).
	middleNodes := []*proto.AccessibilityAXNode{
		{NodeID: "mroot", Role: axVal("RootWebArea"), ChildIDs: []proto.AccessibilityAXNodeID{"mifr"}},
		{NodeID: "mifr", ParentID: "mroot", Role: axVal("Iframe"), Name: axVal("Inner"), BackendDOMNodeID: 30},
	}
	middle, err := NewPageSnapshot("https://a.example.com/mid", "", childDocID, middleNodes, ModeDefault)
	if err != nil {
		t.Fatalf("middle NewPageSnapshot: %v", err)
	}
	// Innermost frame: a single button.
	innerNodes := []*proto.AccessibilityAXNode{
		{NodeID: "iroot", Role: axVal("RootWebArea"), ChildIDs: []proto.AccessibilityAXNodeID{"ibtn"}},
		{NodeID: "ibtn", ParentID: "iroot", Role: axVal("button"), Name: axVal("Confirm"), BackendDOMNodeID: 40},
	}
	inner, err := NewPageSnapshot("https://a.example.com/in", "", "doc-inner", innerNodes, ModeDefault)
	if err != nil {
		t.Fatalf("inner NewPageSnapshot: %v", err)
	}
	outer.linkFrame("ifr", middle, "a.example.com")
	middle.linkFrame("mifr", inner, "a.example.com")

	a := outer.ids["ifr"]
	b := middle.ids["mifr"]
	leaf := inner.ids["ibtn"]
	composite := a + "." + b + "." + leaf

	r, ok := outer.resolveElementID(composite)
	if !ok {
		t.Fatalf("nested composite %s did not resolve", composite)
	}
	if r.owner != inner || r.node.NodeID != "ibtn" || r.idPrefix != a+"."+b+"." {
		t.Errorf("nested resolution = {owner=%p node=%s prefix=%q}, want {inner=%p ibtn %q.%q.}",
			r.owner, r.node.NodeID, r.idPrefix, inner, a, b)
	}

	out := outer.String()
	if want := "[" + composite + `] button "Confirm"`; !strings.Contains(out, want) {
		t.Errorf("expected nested line %q in render:\n%s", want, out)
	}
}

// scopeTree is a small page used across the AX-scope tests:
//
//	banner   > link "Logo"
//	main     > heading "Profile Title", button "Save", region "About" > link "Profile"
//	navigation > link "Home"
func scopeTree() []*proto.AccessibilityAXNode {
	return []*proto.AccessibilityAXNode{
		{NodeID: "root", Role: axVal("RootWebArea"), ChildIDs: []proto.AccessibilityAXNodeID{"banner", "main", "nav"}},
		{NodeID: "banner", ParentID: "root", Role: axVal("banner"), BackendDOMNodeID: 1, ChildIDs: []proto.AccessibilityAXNodeID{"bannerlink"}},
		{NodeID: "bannerlink", ParentID: "banner", Role: axVal("link"), Name: axVal("Logo"), BackendDOMNodeID: 2},
		{NodeID: "main", ParentID: "root", Role: axVal("main"), BackendDOMNodeID: 3, ChildIDs: []proto.AccessibilityAXNodeID{"h1", "save", "about"}},
		{NodeID: "h1", ParentID: "main", Role: axVal("heading"), Name: axVal("Profile Title"), BackendDOMNodeID: 4,
			Properties: []*proto.AccessibilityAXProperty{prop("level", axVal("1"))}},
		{NodeID: "save", ParentID: "main", Role: axVal("button"), Name: axVal("Save"), BackendDOMNodeID: 5},
		{NodeID: "about", ParentID: "main", Role: axVal("region"), Name: axVal("About"), BackendDOMNodeID: 6, ChildIDs: []proto.AccessibilityAXNodeID{"aboutlink"}},
		{NodeID: "aboutlink", ParentID: "about", Role: axVal("link"), Name: axVal("Profile"), BackendDOMNodeID: 7},
		{NodeID: "nav", ParentID: "root", Role: axVal("navigation"), BackendDOMNodeID: 8, ChildIDs: []proto.AccessibilityAXNodeID{"navlink"}},
		{NodeID: "navlink", ParentID: "nav", Role: axVal("link"), Name: axVal("Home"), BackendDOMNodeID: 9},
	}
}

func TestSnapshotFiltered(t *testing.T) {
	tests := []struct {
		name     string
		filter   OutputFilter
		contains []string
		excludes []string
	}{
		{
			name:   "include keeps only the region",
			filter: OutputFilter{IncludeAXScope: []string{"main"}},
			contains: []string{
				`[#] main`,
				`[#] heading[1] "Profile Title"`,
				`[#] button "Save"`,
				`[#] region "About"`,
			},
			excludes: []string{"banner", "navigation", "Logo", "Home"},
		},
		{
			name:   "exclude drops the region",
			filter: OutputFilter{ExcludeAXScope: []string{"navigation"}},
			contains: []string{
				`[#] banner`,
				`[#] main`,
				`[#] button "Save"`,
			},
			excludes: []string{"navigation", "Home"},
		},
		{
			name:   "include and exclude compose",
			filter: OutputFilter{IncludeAXScope: []string{"main"}, ExcludeAXScope: []string{"region:About"}},
			contains: []string{
				`[#] main`,
				`[#] heading[1] "Profile Title"`,
				`[#] button "Save"`,
			},
			excludes: []string{`region "About"`, `link "Profile"`, "navigation"},
		},
		{
			name:     "include matching nothing yields empty output",
			filter:   OutputFilter{IncludeAXScope: []string{"contentinfo"}},
			excludes: []string{"main", "banner", "navigation"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps, err := NewPageSnapshot("https://example.com", "Test", testDocID, scopeTree(), ModeDefault)
			if err != nil {
				t.Fatalf("NewPageSnapshot error: %v", err)
			}
			got := normalizeIDs(ps.Filtered(tt.filter))
			for _, want := range tt.contains {
				if !strings.Contains(got, normalizeIDs(want)) {
					t.Errorf("expected %q in output, got:\n%s", want, got)
				}
			}
			for _, bad := range tt.excludes {
				if strings.Contains(got, bad) {
					t.Errorf("expected %q absent, got:\n%s", bad, got)
				}
			}
		})
	}
}

func TestSnapshotFilteredByElementID(t *testing.T) {
	ps, err := NewPageSnapshot("https://example.com", "Test", testDocID, scopeTree(), ModeDefault)
	if err != nil {
		t.Fatalf("NewPageSnapshot error: %v", err)
	}
	mainID := ps.ids["main"]
	bannerID := ps.ids["banner"]
	if mainID == "" || bannerID == "" {
		t.Fatalf("expected ids for main (%q) and banner (%q)", mainID, bannerID)
	}

	// Include by element id renders just that subtree.
	got := normalizeIDs(ps.Filtered(OutputFilter{IncludeElementIDs: []string{mainID}}))
	for _, want := range []string{`[#] main`, `[#] button "Save"`} {
		if !strings.Contains(got, normalizeIDs(want)) {
			t.Errorf("include by id: expected %q in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"banner", "navigation"} {
		if strings.Contains(got, bad) {
			t.Errorf("include by id: expected %q absent in:\n%s", bad, got)
		}
	}

	// An element id and an AX selector unite into one set of include roots.
	got = normalizeIDs(ps.Filtered(OutputFilter{
		IncludeElementIDs: []string{bannerID},
		IncludeAXScope:    []string{"main"},
	}))
	for _, want := range []string{`[#] banner`, `[#] main`, `[#] button "Save"`} {
		if !strings.Contains(got, normalizeIDs(want)) {
			t.Errorf("unite: expected %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "navigation") {
		t.Errorf("unite: expected navigation absent in:\n%s", got)
	}
}

// TestCollapsedComboboxScopeRevealsOptions exercises the depth-0 carve-out: a
// collapsed combobox hides its options in the full page render, but rooting the
// read at the combobox itself (includeElementIds) renders every option, so the
// model can discover them in a single turn before selecting.
func TestCollapsedComboboxScopeRevealsOptions(t *testing.T) {
	nodes := []*proto.AccessibilityAXNode{
		{NodeID: "root", Role: axVal("RootWebArea"), ChildIDs: []proto.AccessibilityAXNodeID{"combo"}},
		{NodeID: "combo", ParentID: "root", Role: axVal("combobox"), Name: axVal("Country"), Value: axVal("United States"), BackendDOMNodeID: 600,
			Properties: []*proto.AccessibilityAXProperty{prop("expanded", axBoolVal(false))},
			ChildIDs:   []proto.AccessibilityAXNodeID{"o1", "o2", "o3"}},
		{NodeID: "o1", ParentID: "combo", Role: axVal("option"), Name: axVal("United States"), BackendDOMNodeID: 601},
		{NodeID: "o2", ParentID: "combo", Role: axVal("option"), Name: axVal("Canada"), BackendDOMNodeID: 602},
		{NodeID: "o3", ParentID: "combo", Role: axVal("option"), Name: axVal("Germany"), BackendDOMNodeID: 603},
	}
	ps, err := NewPageSnapshot("https://example.com", "Test", testDocID, nodes, ModeDefault)
	if err != nil {
		t.Fatalf("NewPageSnapshot error: %v", err)
	}

	// Full page render: the combobox line stays (with value and marker), but
	// its options are pruned.
	full := normalizeIDs(ps.String())
	if !strings.Contains(full, normalizeIDs(`[#] combobox "Country" value="United States" collapsed`)) {
		t.Errorf("full render missing combobox line:\n%s", full)
	}
	for _, opt := range []string{`option "Canada"`, `option "Germany"`} {
		if strings.Contains(full, opt) {
			t.Errorf("full render should prune %q:\n%s", opt, full)
		}
	}

	// Scoped to the combobox id: the combobox is the depth-0 root, so its
	// options render in full for discovery.
	comboID := ps.ids["combo"]
	if comboID == "" {
		t.Fatal("expected an element id for the combobox")
	}
	scoped := normalizeIDs(ps.Filtered(OutputFilter{IncludeElementIDs: []string{comboID}}))
	for _, opt := range []string{`option "United States"`, `option "Canada"`, `option "Germany"`} {
		if !strings.Contains(scoped, opt) {
			t.Errorf("scoped render missing %q:\n%s", opt, scoped)
		}
	}
}

func TestApplySection(t *testing.T) {
	const md = "# Coffee\nintro line\n## History\nhist para\n### Etymology\netym para\n## Cultivation\ncult para\n"
	const snap = "[a1] main\n  [b2] heading[2] \"History\"\n  [c3] paragraph\n    [d4] text \"hist\"\n  [e5] heading[3] \"Etymology\"\n  [f6] heading[2] \"Culture\"\n"
	// A "#" inside a fenced code block is not a heading boundary.
	const fenced = "## Setup\n```\n# not a heading\n```\nstep one\n## Teardown\nbye\n"

	tests := []struct {
		name     string
		text     string
		section  string
		format   sectionFormat
		contains []string
		excludes []string
	}{
		{
			name:     "markdown keeps the section, header included, deeper kept",
			text:     md,
			section:  "history", // case-insensitive
			format:   sectionMarkdown,
			contains: []string{"## History", "hist para", "### Etymology", "etym para"},
			excludes: []string{"# Coffee", "intro line", "## Cultivation", "cult para"},
		},
		{
			name:     "snapshot keeps the section by heading[N]",
			text:     snap,
			section:  "History",
			format:   sectionSnapshot,
			contains: []string{`heading[2] "History"`, `text "hist"`, `heading[3] "Etymology"`},
			excludes: []string{"main", `heading[2] "Culture"`},
		},
		{
			name:     "fenced hash is not a boundary",
			text:     fenced,
			section:  "Setup",
			format:   sectionMarkdown,
			contains: []string{"## Setup", "# not a heading", "step one"},
			excludes: []string{"## Teardown", "bye"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := applySection(tt.text, tt.section, tt.format)
			if err != nil {
				t.Fatalf("applySection error: %v", err)
			}
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("expected %q in:\n%s", want, got)
				}
			}
			for _, bad := range tt.excludes {
				if strings.Contains(got, bad) {
					t.Errorf("expected %q absent in:\n%s", bad, got)
				}
			}
		})
	}

	// A name matching no heading is a loud error listing the headings present.
	_, err := applySection(md, "Nonexistent", sectionMarkdown)
	if err == nil {
		t.Fatal("expected an error for a section that matches no heading")
	}
	for _, h := range []string{"Coffee", "History", "Cultivation"} {
		if !strings.Contains(err.Error(), h) {
			t.Errorf("error should list heading %q, got: %v", h, err)
		}
	}

	// Empty section name is an identity pass-through.
	if got, _ := applySection(md, "", sectionMarkdown); got != md {
		t.Errorf("empty section changed the output: %q", got)
	}
}

func TestApplyMatch(t *testing.T) {
	const snap = "[a1] main\n[b2] button \"Save\"\n[c3] link \"Home\"\n"

	got, err := applyMatch(snap, "button")
	if err != nil {
		t.Fatalf("applyMatch error: %v", err)
	}
	if got != `[b2] button "Save"` {
		t.Errorf("match kept the wrong lines: %q", got)
	}

	// Empty pattern is an identity pass-through.
	if got, _ := applyMatch(snap, ""); got != snap {
		t.Errorf("empty pattern changed the output: %q", got)
	}

	// An invalid regex is a loud error, not a silent pass-through.
	if _, err := applyMatch(snap, "[unterminated"); err == nil {
		t.Error("expected an error for an invalid regex, got nil")
	}
}
