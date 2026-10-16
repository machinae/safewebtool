//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/machinae/safewebtool/browser"
)

// rerenderHTML is the self-correction fixture: an inbox with two rows whose
// Archive buttons are identical by role and name (only the row anchors
// differ), plus a status line that records which button was clicked. Inline
// onclick handlers survive innerHTML round-trips, so tests can destroy and
// recreate the whole app without rewiring.
const rerenderHTML = `<!DOCTYPE html>
<html>
<head><title>Rerender Test</title><meta charset="utf-8"></head>
<body>
	<div id="app">
		<h1>Inbox</h1>
		<ul id="inbox">
			<li><span>John Doe</span> <button id="john-archive" onclick="archived('John')">Archive</button></li>
			<li><span>Jane Roe</span> <button id="jane-archive" onclick="archived('Jane')">Archive</button></li>
		</ul>
		<button onclick="document.getElementById('inbox').remove(); archived('Cleared')">Clear inbox</button>
		<div id="status" role="status"></div>
	</div>
	<script>
	function archived(who) {
		document.getElementById("status").textContent = who + " archived";
	}
	</script>
</body>
</html>`

// findElementAfter returns the element id of the first snapshot line
// matching match that appears after the line matching anchor. Used to pick
// one of several identically named elements by its container row.
func findElementAfter(t *testing.T, snapshot, anchor, match string) string {
	t.Helper()
	seen := false
	for _, line := range strings.Split(snapshot, "\n") {
		if !seen {
			if strings.Contains(line, anchor) {
				seen = true
			}
			continue
		}
		if strings.Contains(line, match) {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "[") {
				continue
			}
			if end := strings.Index(line, "]"); end > 0 {
				return line[1:end]
			}
		}
	}
	t.Fatalf("no element %q after %q in snapshot:\n%s", match, anchor, snapshot)
	return ""
}

func statusText(t *testing.T, ctx context.Context, tab *browser.Tab) string {
	t.Helper()
	status, err := tab.Eval(ctx, `document.getElementById("status").textContent`)
	if err != nil {
		t.Fatalf("Eval status: %v", err)
	}
	return status
}

// TestRerender_IdSurvivesTransientAbsence: the headline self-correction.
// An async re-render (AJAX response swapping the view) makes the target
// briefly absent from the page; clicking its id during that window used to
// be a stale-id error and a wasted model turn. Resolution must wait out the
// re-render and click with no error surfaced.
func TestRerender_IdSurvivesTransientAbsence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/rerender")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	johnID := findElementAfter(t, snap, `listitem "John Doe"`, `button "Archive"`)

	// Replace the app with a loading placeholder right now and restore the
	// identical content 400ms later, like a fetch response re-rendering.
	if _, err := tab.Eval(ctx, `(() => {
		const app = document.getElementById("app");
		const html = app.innerHTML;
		app.innerHTML = "<p>Loading</p>";
		setTimeout(() => { app.innerHTML = html; }, 400);
		return "swapped";
	})()`); err != nil {
		t.Fatalf("Eval swap: %v", err)
	}

	if _, err := tab.Click(ctx, johnID); err != nil {
		t.Fatalf("Click during re-render: %v", err)
	}
	if got := statusText(t, ctx, tab); got != "John archived" {
		t.Errorf("status = %q, want %q", got, "John archived")
	}
}

// TestRerender_IdSurvivesDetachDuringPrepare: the bounded mid-pipeline
// retry. The element resolves fine, then a re-render destroys and recreates
// every node inside the click's actionability window (WaitStable's 500ms
// quiet period). The detached-node failure must be retried internally, not
// surfaced.
func TestRerender_IdSurvivesDetachDuringPrepare(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/rerender")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	johnID := findElementAfter(t, snap, `listitem "John Doe"`, `button "Archive"`)

	// One-shot re-render 250ms from now: identical content, all-new nodes.
	if _, err := tab.Eval(ctx, `(() => {
		setTimeout(() => {
			const app = document.getElementById("app");
			app.innerHTML = app.innerHTML;
		}, 250);
		return "scheduled";
	})()`); err != nil {
		t.Fatalf("Eval schedule: %v", err)
	}

	if _, err := tab.Click(ctx, johnID); err != nil {
		t.Fatalf("Click across mid-pipeline re-render: %v", err)
	}
	if got := statusText(t, ctx, tab); got != "John archived" {
		t.Errorf("status = %q, want %q", got, "John archived")
	}
}

// TestRerender_DuplicateButtonsClickRightTarget: two buttons identical by
// role and name; only the container anchor tells them apart. Clicking the
// second row's id must hit the second row's button, never the first.
func TestRerender_DuplicateButtonsClickRightTarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/rerender")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	johnID := findElementAfter(t, snap, `listitem "John Doe"`, `button "Archive"`)
	janeID := findElementAfter(t, snap, `listitem "Jane Roe"`, `button "Archive"`)
	if johnID == janeID {
		t.Fatalf("same-named buttons in different rows share id %s", johnID)
	}

	if _, err := tab.Click(ctx, janeID); err != nil {
		t.Fatalf("Click Jane's Archive: %v", err)
	}
	if got := statusText(t, ctx, tab); got != "Jane archived" {
		t.Errorf("status = %q, want %q (clicked the wrong row's button?)", got, "Jane archived")
	}
}

// TestRerender_DeletedMidSequenceFailsFast: an earlier action in the same
// sequence deletes the target before any new snapshot is taken. The click on
// the dead id must return the element-gone error quickly (one settle wait,
// not the whole action timeout burned in a hidden wait) and click nothing.
func TestRerender_DeletedMidSequenceFailsFast(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/rerender")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	johnID := findElementAfter(t, snap, `listitem "John Doe"`, `button "Archive"`)
	clearID := findElement(t, snap, `button "Clear inbox"`)

	if _, err := tab.Click(ctx, clearID); err != nil {
		t.Fatalf("Click 'Clear inbox': %v", err)
	}

	start := time.Now()
	_, err = tab.Click(ctx, johnID)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("Click on deleted element: want element-gone error, got success")
	}
	if !strings.Contains(err.Error(), "no element with id") {
		t.Errorf("error = %q, want the element-gone error", err)
	}
	if elapsed > 10*time.Second {
		t.Errorf("failing click took %v, want fail-fast (a settle wait, not the action timeout)", elapsed)
	}
	if got := statusText(t, ctx, tab); got != "Cleared archived" {
		t.Errorf("status = %q, want %q (the dead click should change nothing)", got, "Cleared archived")
	}
}

// TestRerender_NavigationInvalidatesIds: ids are scoped to the document.
// After navigating to a page with identical content, an id minted on the
// old document must fail loudly instead of matching the same-looking
// element on the new page and acting there.
func TestRerender_NavigationInvalidatesIds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/rerender")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	johnID := findElementAfter(t, snap, `listitem "John Doe"`, `button "Archive"`)

	// New document, identical content: only the doc scoping can tell them apart.
	if _, err := tab.Open(ctx, pageURL("/rerender-2")); err != nil {
		t.Fatalf("navigate to /rerender-2: %v", err)
	}

	_, err = tab.Click(ctx, johnID)
	if err == nil {
		t.Fatalf("Click with pre-navigation id: want element-gone error, got success")
	}
	if !strings.Contains(err.Error(), "no element with id") {
		t.Errorf("error = %q, want the element-gone error", err)
	}
	if got := statusText(t, ctx, tab); got != "" {
		t.Errorf("status = %q, want empty (stale id acted on the new document)", got)
	}
}

// TestRerender_ElementGoneReturnsLoudError: when the content an id names has
// genuinely left the page, resolution must fail loudly with the element-gone
// error and click nothing — no fallback, no silent retargeting.
func TestRerender_ElementGoneReturnsLoudError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/rerender")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	johnID := findElementAfter(t, snap, `listitem "John Doe"`, `button "Archive"`)

	if _, err := tab.Eval(ctx, `(() => {
		document.getElementById("john-archive").remove();
		return "removed";
	})()`); err != nil {
		t.Fatalf("Eval remove: %v", err)
	}

	_, err = tab.Click(ctx, johnID)
	if err == nil {
		t.Fatalf("Click on removed element: want element-gone error, got success")
	}
	if !strings.Contains(err.Error(), "no element with id") || !strings.Contains(err.Error(), johnID) {
		t.Errorf("error = %q, want it to name the missing id %s", err, johnID)
	}
	if got := statusText(t, ctx, tab); got != "" {
		t.Errorf("status = %q, want empty (something got clicked despite the error)", got)
	}
}
