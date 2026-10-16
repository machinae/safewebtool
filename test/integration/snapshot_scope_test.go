//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/machinae/safewebtool/browser"
)

// A snapshot scoped to a container renders only that subtree, with the
// same ids a full snapshot mints, so the model can watch one region of a
// large page cheaply.
func TestSnapshot_ScopedToContainer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/rerender")

	full, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	listID := findElement(t, full, "] list")
	johnID := findElementAfter(t, full, `listitem "John Doe"`, `button "Archive"`)

	scoped, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{IncludeElementIDs: []string{listID}})
	if err != nil {
		t.Fatalf("Scoped snapshot: %v", err)
	}

	if !strings.Contains(scoped, johnID) {
		t.Errorf("scoped snapshot should mint the same ids as the full one; %s missing:\n%s", johnID, scoped)
	}
	if !strings.Contains(scoped, `button "Archive"`) {
		t.Errorf("expected Archive buttons inside the scoped container, got:\n%s", scoped)
	}
	if strings.Contains(scoped, "Clear inbox") {
		t.Errorf("scoped snapshot leaked content outside the container:\n%s", scoped)
	}
}

func TestSnapshot_ScopeUnknownIDFailsLoudly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/rerender")

	_, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{IncludeElementIDs: []string{"0000000"}})
	if err == nil {
		t.Fatalf("scoped snapshot with unknown id: want error, got success")
	}
	if !strings.Contains(err.Error(), "no element with id") {
		t.Errorf("error = %q, want the element-gone error", err)
	}
}

// includeAXScope keeps only the named region, set blind with no prior
// snapshot: a markdown read scoped to main returns the main content and
// drops the surrounding nav and footer chrome.
func TestScope_IncludeByRole(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/extract")

	md, err := tab.Extract(ctx, false, browser.OutputFilter{IncludeAXScope: []string{"main"}})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, want := range []string{"Main Heading", "main content paragraph", "Item one"} {
		if !strings.Contains(md, want) {
			t.Errorf("include main should keep %q, got:\n%s", want, md)
		}
	}
	for _, bad := range []string{"Home", "Footer content"} {
		if strings.Contains(md, bad) {
			t.Errorf("include main should drop %q, got:\n%s", bad, md)
		}
	}
}

// excludeAXScope subtracts a region from the rest of the page: excluding
// navigation drops the nav links while keeping main and footer.
func TestScope_ExcludeByRole(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/extract")

	md, err := tab.Extract(ctx, false, browser.OutputFilter{ExcludeAXScope: []string{"navigation"}})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, want := range []string{"Main Heading", "Footer content"} {
		if !strings.Contains(md, want) {
			t.Errorf("exclude navigation should keep %q, got:\n%s", want, md)
		}
	}
	if strings.Contains(md, "Home") {
		t.Errorf("exclude navigation should drop the nav link, got:\n%s", md)
	}
}

// Rooting a read at an element id from a prior snapshot returns just that
// subtree: snapshot first to learn the id, then scope to it.
func TestScope_IncludeByElementID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/extract")

	full, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	mainID := findElement(t, full, "] main")

	scoped, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{IncludeElementIDs: []string{mainID}})
	if err != nil {
		t.Fatalf("Scoped snapshot: %v", err)
	}
	if !strings.Contains(scoped, "Main Heading") {
		t.Errorf("scoped snapshot should keep the main heading, got:\n%s", scoped)
	}
	if strings.Contains(scoped, `link "Home"`) {
		t.Errorf("scoped snapshot should drop the nav link, got:\n%s", scoped)
	}
}

// section slices the rendered output to one heading's section in document
// order, surviving wrapped markup that defeats AX-subtree scoping. A
// markdown read of "History" keeps the heading, its prose, and the deeper
// Etymology subsection, and drops the page intro and the next section.
func TestScope_BySection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/sections")

	md, err := tab.Extract(ctx, false, browser.OutputFilter{Section: "History"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, want := range []string{"History", "15th century", "Etymology", "entered English"} {
		if !strings.Contains(md, want) {
			t.Errorf("section History should keep %q, got:\n%s", want, md)
		}
	}
	for _, bad := range []string{"brewed drink", "Cultivation", "seventy countries"} {
		if strings.Contains(md, bad) {
			t.Errorf("section History should drop %q, got:\n%s", bad, md)
		}
	}

	// The same slice drives a snapshot, recognizing heading[N] lines.
	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{Section: "History"})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !strings.Contains(snap, `heading[2] "History"`) {
		t.Errorf("section snapshot should keep the History heading, got:\n%s", snap)
	}
	if strings.Contains(snap, `heading[2] "Cultivation"`) {
		t.Errorf("section snapshot should stop before the next h2, got:\n%s", snap)
	}

	// A name that matches no heading errors loudly with the headings present.
	_, err = tab.Extract(ctx, false, browser.OutputFilter{Section: "Nonexistent"})
	if err == nil {
		t.Fatal("section with no matching heading: want error, got success")
	}
	if !strings.Contains(err.Error(), "no heading matching") {
		t.Errorf("error = %q, want the no-heading error listing available headings", err)
	}
}

// An AX selector can carry a name after the colon: "group:Delivery mode"
// keeps only the fieldset named by its legend, not every group on the page.
func TestScope_IncludeByRoleAndName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/controlled")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{IncludeAXScope: []string{"group:Delivery mode"}})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, want := range []string{`radio "Standard delivery"`, `radio "Expedite delivery"`} {
		if !strings.Contains(snap, want) {
			t.Errorf("include group:Delivery mode should keep %q, got:\n%s", want, snap)
		}
	}
	for _, bad := range []string{`textbox "Task name"`, `combobox "Priority"`} {
		if strings.Contains(snap, bad) {
			t.Errorf("include group:Delivery mode should drop %q, got:\n%s", bad, snap)
		}
	}
}

// excludeAXScope drives a snapshot too, not just markdown: excluding
// navigation drops the nav link while keeping the rest of the page.
func TestScope_ExcludeByRoleSnapshot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/extract")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{ExcludeAXScope: []string{"navigation"}})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !strings.Contains(snap, `link "Example Link"`) {
		t.Errorf("exclude navigation should keep the main link, got:\n%s", snap)
	}
	if strings.Contains(snap, `link "Home"`) {
		t.Errorf("exclude navigation should drop the nav link, got:\n%s", snap)
	}
}

// Include and exclude compose: include selects the root, exclude cuts a hole
// inside it, and the region is serialized around the hole.
func TestScope_IncludeWithExcludeHole(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/extract")

	md, err := tab.Extract(ctx, false, browser.OutputFilter{
		IncludeAXScope: []string{"main"},
		ExcludeAXScope: []string{"list"},
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, want := range []string{"Main Heading", "main content paragraph"} {
		if !strings.Contains(md, want) {
			t.Errorf("include main should keep %q around the hole, got:\n%s", want, md)
		}
	}
	if strings.Contains(md, "Item one") {
		t.Errorf("exclude list should cut the list out of main, got:\n%s", md)
	}
	if strings.Contains(md, "Footer content") {
		t.Errorf("include main should still drop the footer, got:\n%s", md)
	}
}

// Element-id roots and AX-selector roots unite into one include set, so a
// read can combine a known id with a blind role selector in one call.
func TestScope_MixedIncludeRootsUnite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/extract")

	// The footer is text-only, so its container id is only visible in all mode.
	full, err := tab.Snapshot(ctx, browser.ModeAll, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	footerID := findElement(t, full, "] contentinfo")

	md, err := tab.Extract(ctx, false, browser.OutputFilter{
		IncludeElementIDs: []string{footerID},
		IncludeAXScope:    []string{"navigation"},
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, want := range []string{"Home", "Footer content"} {
		if !strings.Contains(md, want) {
			t.Errorf("united roots should keep %q, got:\n%s", want, md)
		}
	}
	if strings.Contains(md, "Main Heading") {
		t.Errorf("united roots should not include main, got:\n%s", md)
	}
}

// Match keeps only rendered lines matching the regex, applied to the final
// output, and an invalid pattern errors loudly instead of passing through.
func TestMatch_FiltersRenderedLines(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/controlled")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{Match: "button"})
	if err != nil {
		t.Fatalf("Snapshot with match: %v", err)
	}
	if !strings.Contains(snap, `button "Add task"`) {
		t.Errorf("match=button should keep button lines, got:\n%s", snap)
	}
	if strings.Contains(snap, "textbox") {
		t.Errorf("match=button should drop textbox lines, got:\n%s", snap)
	}

	_, err = tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{Match: "("})
	if err == nil {
		t.Fatal("snapshot with invalid match regex: want error, got success")
	}
	if !strings.Contains(err.Error(), "invalid match regex") {
		t.Errorf("error = %q, want the invalid-regex error", err)
	}
}

// Section is a document-order slice of rendered text, which html output does
// not have; asking for it must be a loud error, not a silent whole page.
func TestScope_SectionOnHTMLFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/sections")

	_, err := tab.Extract(ctx, true, browser.OutputFilter{Section: "History"})
	if err == nil {
		t.Fatal("html extract with section: want error, got success")
	}
	if !strings.Contains(err.Error(), "not supported for html") {
		t.Errorf("error = %q, want the section-on-html error", err)
	}
}

// includeAXScope drives a snapshot too, not just markdown: scoping to main
// yields only the main subtree's lines, dropping the nav and footer chrome.
func TestScope_IncludeByRoleSnapshot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/extract")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{IncludeAXScope: []string{"main"}})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !strings.Contains(snap, "Main Heading") {
		t.Errorf("include main should keep the main heading, got:\n%s", snap)
	}
	if !strings.Contains(snap, `link "Example Link"`) {
		t.Errorf("include main should keep links inside main, got:\n%s", snap)
	}
	if strings.Contains(snap, `link "Home"`) {
		t.Errorf("include main should drop the nav link, got:\n%s", snap)
	}
}
