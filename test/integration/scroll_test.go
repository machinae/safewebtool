//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
)

// nestedScrollHTML reproduces the LinkedIn shape: a non-scrolling root, a
// fixed header, and the only real scroller nested in an absolutely-positioned
// container below the header.
const nestedScrollHTML = `<!DOCTYPE html>
<html><head><style>
  html, body { margin: 0; height: 100%; overflow: hidden; }
  header { position: fixed; top: 0; left: 0; right: 0; height: 60px; }
  main#workspace { position: absolute; top: 60px; bottom: 0; left: 0; right: 0; overflow-y: auto; }
  .spacer { height: 3000px; }
</style></head><body>
  <header>Fixed Nav</header>
  <main id="workspace">
    <h1>Top</h1>
    <div class="spacer"></div>
    <h2>Bottom</h2>
  </main>
</body></html>`

func TestScroll_DownAndUpChangesScrollPosition(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/dynamic")

	// Get initial scroll position.
	before, err := tab.Eval(ctx, "window.scrollY")
	if err != nil {
		t.Fatalf("Eval scrollY before: %v", err)
	}

	// Scroll down by 300 pixels.
	if _, err := tab.Scroll(ctx, 300); err != nil {
		t.Fatalf("Scroll down: %v", err)
	}

	after, err := tab.Eval(ctx, "window.scrollY")
	if err != nil {
		t.Fatalf("Eval scrollY after scroll down: %v", err)
	}

	if after == before {
		t.Errorf("expected scroll position to change after scrolling down, got %s both times", before)
	}

	// Scroll back up.
	if _, err := tab.Scroll(ctx, -300); err != nil {
		t.Fatalf("Scroll up: %v", err)
	}

	restored, err := tab.Eval(ctx, "window.scrollY")
	if err != nil {
		t.Fatalf("Eval scrollY after scroll up: %v", err)
	}

	if restored != before {
		t.Errorf("expected scroll position to restore to %s after scrolling up, got %s", before, restored)
	}
}

// TestScrollPage_DownAndUp: ScrollPage moves by one viewport height in the
// given direction and reports the applied delta.
func TestScrollPage_DownAndUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/dynamic")

	res, err := tab.ScrollPage(ctx, false)
	if err != nil {
		t.Fatalf("ScrollPage down: %v", err)
	}
	if !strings.Contains(res.Desc, "scrolled") {
		t.Errorf("expected result to report applied scroll, got Desc %q", res.Desc)
	}

	after, err := tab.Eval(ctx, "window.scrollY")
	if err != nil {
		t.Fatalf("Eval scrollY after page down: %v", err)
	}
	if after == "0" {
		t.Errorf("expected window.scrollY > 0 after page down, got 0")
	}

	if _, err := tab.ScrollPage(ctx, true); err != nil {
		t.Fatalf("ScrollPage up: %v", err)
	}
	restored, err := tab.Eval(ctx, "window.scrollY")
	if err != nil {
		t.Fatalf("Eval scrollY after page up: %v", err)
	}
	if restored != "0" {
		t.Errorf("expected window.scrollY 0 after page up, got %s", restored)
	}
}

// TestScroll_NonScrollablePageReportsNoScroll: on a page with nothing to
// scroll the verb reports the zero delta instead of a bare ok, so the model
// learns the page is already at the edge.
func TestScroll_NonScrollablePageReportsNoScroll(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/simple")

	res, err := tab.Scroll(ctx, 300)
	if err != nil {
		t.Fatalf("Scroll: %v", err)
	}
	if !strings.Contains(res.Desc, "no scroll applied") {
		t.Errorf("Desc = %q, want the no-scroll report", res.Desc)
	}
}

// TestScroll_MovesNestedScrollContainer pins the LinkedIn shape: the scroll
// must reach the nested #workspace scroller even though the root document
// never scrolls, with a blind `scroll pixels:N` call (no hover first).
func TestScroll_MovesNestedScrollContainer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/nested-scroll")

	// Both the nested scroller and the root start at the top.
	if got, err := tab.Eval(ctx, "document.getElementById('workspace').scrollTop"); err != nil {
		t.Fatalf("Eval workspace scrollTop before: %v", err)
	} else if got != "0" {
		t.Fatalf("expected #workspace.scrollTop 0 before scroll, got %s", got)
	}
	if got, err := tab.Eval(ctx, "window.scrollY"); err != nil {
		t.Fatalf("Eval scrollY before: %v", err)
	} else if got != "0" {
		t.Fatalf("expected window.scrollY 0 before scroll, got %s", got)
	}

	res, err := tab.Scroll(ctx, 500)
	if err != nil {
		t.Fatalf("Scroll: %v", err)
	}

	// The scroll reached the nested scroller.
	if got, err := tab.Eval(ctx, "document.getElementById('workspace').scrollTop"); err != nil {
		t.Fatalf("Eval workspace scrollTop after: %v", err)
	} else if got == "0" {
		t.Errorf("expected #workspace.scrollTop > 0 after scroll, got %s", got)
	}

	// The root document never scrolled.
	if got, err := tab.Eval(ctx, "window.scrollY"); err != nil {
		t.Fatalf("Eval scrollY after: %v", err)
	} else if got != "0" {
		t.Errorf("expected window.scrollY to stay 0, got %s", got)
	}

	// The result reports a nonzero applied delta, not a bare ok.
	if !strings.Contains(res.Desc, "scrolled") {
		t.Errorf("expected result to report applied scroll, got Desc %q", res.Desc)
	}
}
