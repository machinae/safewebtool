//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/machinae/safewebtool/browser"
)

func TestControlledForm_ClickTypeKeyAddsItem(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/controlled")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if !strings.Contains(snap, "Task name") {
		t.Fatalf("expected Task name textbox in snapshot, got:\n%s", snap)
	}
	if !strings.Contains(snap, "Priority") {
		t.Fatalf("expected Priority select in snapshot, got:\n%s", snap)
	}
	if !strings.Contains(snap, "Notes") || !strings.Contains(snap, "Owner email") {
		t.Fatalf("expected textarea and email controls in snapshot, got:\n%s", snap)
	}
	if !strings.Contains(snap, "Standard delivery") || !strings.Contains(snap, "Notify team") {
		t.Fatalf("expected radio and checkbox controls in snapshot, got:\n%s", snap)
	}

	if _, err := tab.Select(ctx, findElement(t, snap, `combobox "Priority"`), "Urgent"); err != nil {
		t.Fatalf("Select priority: %v", err)
	}

	snap, err = tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot after select: %v", err)
	}

	if !strings.Contains(snap, "Priority set to Urgent") {
		t.Fatalf("expected status text after select, got:\n%s", snap)
	}

	inputID := findElement(t, snap, `textbox "Task name"`)
	if _, err := tab.Click(ctx, inputID); err != nil {
		t.Fatalf("Click input: %v", err)
	}

	if _, err := tab.Fill(ctx, inputID, "Ship integration coverage"); err != nil {
		t.Fatalf("Fill: %v", err)
	}

	// All ids come from the post-select snapshot. Every action below
	// re-renders the form, but content-hash ids survive re-renders, so
	// they all stay valid without re-snapshotting.
	if _, err := tab.Fill(ctx, findElement(t, snap, `textbox "Notes"`), "React rerender smoke test"); err != nil {
		t.Fatalf("Type notes: %v", err)
	}

	if _, err := tab.Fill(ctx, findElement(t, snap, `textbox "Owner email"`), "owner@example.com"); err != nil {
		t.Fatalf("Type owner email: %v", err)
	}

	if _, err := tab.Click(ctx, findElement(t, snap, `radio "Expedite delivery"`)); err != nil {
		t.Fatalf("Click delivery radio: %v", err)
	}

	if _, err := tab.Click(ctx, findElement(t, snap, `checkbox "Notify team"`)); err != nil {
		t.Fatalf("Click notify checkbox: %v", err)
	}

	snap, err = tab.Snapshot(ctx, browser.ModeAll, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot after filling form: %v", err)
	}

	if !strings.Contains(snap, "Notifications enabled") {
		t.Fatalf("expected checkbox change status, got:\n%s", snap)
	}
	if !strings.Contains(snap, "Priority: Urgent") ||
		!strings.Contains(snap, "Notes: React rerender smoke test") ||
		!strings.Contains(snap, "Owner: owner@example.com") ||
		!strings.Contains(snap, "Delivery: Expedite") ||
		!strings.Contains(snap, "Notify: Yes") {
		t.Fatalf("expected live summary to reflect all form changes, got:\n%s", snap)
	}

	// Refocus with the id minted before all the typing: the input's value
	// changed but its identity (role, label, container) did not.
	if _, err := tab.Click(ctx, inputID); err != nil {
		t.Fatalf("Refocus task input: %v", err)
	}

	if _, err := tab.Type(ctx, "Enter"); err != nil {
		t.Fatalf("Type Enter: %v", err)
	}

	snap, err = tab.Snapshot(ctx, browser.ModeAll, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot after enter: %v", err)
	}

	if !strings.Contains(snap, "Added [Urgent] Ship integration coverage / Expedite / notify=yes / owner=owner@example.com / notes=React rerender smoke test") {
		t.Errorf("expected status text after submit, got:\n%s", snap)
	}
	if !strings.Contains(snap, "[Urgent] Ship integration coverage / Expedite / notify=yes / owner=owner@example.com / notes=React rerender smoke test") {
		t.Errorf("expected saved list item after submit, got:\n%s", snap)
	}
}

func TestSPA_ClickBackForwardRestoresRoute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/spa")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if !strings.Contains(snap, "Home") || !strings.Contains(snap, "Route: home") {
		t.Fatalf("expected home route in initial snapshot, got:\n%s", snap)
	}

	settingsID := findElement(t, snap, `button "Settings"`)
	if _, err := tab.Click(ctx, settingsID); err != nil {
		t.Fatalf("Click settings nav: %v", err)
	}

	snap, err = tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot after click: %v", err)
	}

	if !strings.Contains(snap, "Settings") || !strings.Contains(snap, "Route: settings") {
		t.Fatalf("expected settings route after click, got:\n%s", snap)
	}

	if _, err := tab.Back(ctx); err != nil {
		t.Fatalf("Back: %v", err)
	}

	snap, err = tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot after back: %v", err)
	}

	if !strings.Contains(snap, `heading[1] "Home"`) || !strings.Contains(snap, "Route: home") {
		t.Fatalf("expected home route after back, got:\n%s", snap)
	}

	if _, err := tab.Forward(ctx); err != nil {
		t.Fatalf("Forward: %v", err)
	}

	snap, err = tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot after forward: %v", err)
	}

	if !strings.Contains(snap, `heading[1] "Settings"`) || !strings.Contains(snap, "Route: settings") {
		t.Errorf("expected settings route after forward, got:\n%s", snap)
	}
}

// TestTabs_MultipleTabsOperateIndependently opens two tabs and snapshots each
// independently via its *Tab handle. The old "active tab + Switch" semantics
// are gone — per-tab handles are the only way to target a tab now.
func TestTabs_MultipleTabsOperateIndependently(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	baseURL := pageURL("/simple") + fmt.Sprintf("?case=tabs-indep-%d", time.Now().UnixNano())
	spaURL := pageURL("/spa") + fmt.Sprintf("?case=tabs-indep-%d", time.Now().UnixNano())

	baseTab := newTab(t, ctx, baseURL)
	spaTab := newTab(t, ctx, spaURL)

	tabs, err := b.Tabs(ctx)
	if err != nil {
		t.Fatalf("Tabs after opening new tabs: %v", err)
	}

	findTabByURL(t, tabs, baseURL)
	findTabByURL(t, tabs, spaURL)

	baseSnap, err := baseTab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot base tab: %v", err)
	}
	if !strings.Contains(baseSnap, "Click me") {
		t.Fatalf("expected simple page content in base tab, got:\n%s", baseSnap)
	}

	spaSnap, err := spaTab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot SPA tab: %v", err)
	}
	if !strings.Contains(spaSnap, "Route: home") {
		t.Fatalf("expected SPA content in SPA tab, got:\n%s", spaSnap)
	}
}

// TestTabs_MultipleNewTabsAllListed opens three tabs and verifies every one
// shows up in Tabs(). Replaces the old active-tab tracking test.
func TestTabs_MultipleNewTabsAllListed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	firstURL := pageURL("/simple") + fmt.Sprintf("?case=tabs-list-%d", time.Now().UnixNano())
	secondURL := pageURL("/controlled") + fmt.Sprintf("?case=tabs-list-%d", time.Now().UnixNano())
	thirdURL := pageURL("/spa") + fmt.Sprintf("?case=tabs-list-%d", time.Now().UnixNano())

	newTab(t, ctx, firstURL)
	secondTab := newTab(t, ctx, secondURL)
	newTab(t, ctx, thirdURL)

	tabs, err := b.Tabs(ctx)
	if err != nil {
		t.Fatalf("Tabs after multiple new tabs: %v", err)
	}

	findTabByURL(t, tabs, firstURL)
	findTabByURL(t, tabs, secondURL)
	findTabByURL(t, tabs, thirdURL)

	snap, err := secondTab.Snapshot(ctx, browser.ModeAll, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot second tab: %v", err)
	}
	if !strings.Contains(snap, "Task board") || !strings.Contains(snap, "Priority") {
		t.Fatalf("expected controlled form in second tab, got:\n%s", snap)
	}
}

func TestSnapshotModes_InteractiveReturnsFewerElements(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/dynamic")

	interactive, err := tab.Snapshot(ctx, browser.ModeInteractive, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot interactive: %v", err)
	}

	def, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot default: %v", err)
	}

	all, err := tab.Snapshot(ctx, browser.ModeAll, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot all: %v", err)
	}

	iLines := strings.Count(interactive, "\n")
	dLines := strings.Count(def, "\n")
	aLines := strings.Count(all, "\n")

	if iLines >= dLines {
		t.Errorf("expected interactive (%d lines) < default (%d lines)\ninteractive:\n%s\ndefault:\n%s",
			iLines, dLines, interactive, def)
	}
	if dLines >= aLines {
		t.Errorf("expected default (%d lines) < all (%d lines)\ndefault:\n%s\nall:\n%s",
			dLines, aLines, def, all)
	}

	// Interactive should have buttons but not headings.
	if !strings.Contains(interactive, "button") {
		t.Errorf("expected buttons in interactive snapshot, got:\n%s", interactive)
	}
	if strings.Contains(interactive, "heading") {
		t.Errorf("expected no headings in interactive snapshot, got:\n%s", interactive)
	}

	// All should include static text content like paragraph text.
	if !strings.Contains(all, "Deploy v1.0 completed") {
		t.Errorf("expected static text in all snapshot, got:\n%s", all)
	}
}
