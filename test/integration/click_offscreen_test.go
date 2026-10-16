//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/machinae/safewebtool/browser"
)

// longPageHTML puts its only button far below the fold, so a click must
// scroll it into view before the pointer can reach it.
const longPageHTML = `<!DOCTYPE html>
<html>
<head>
	<title>Long Page</title>
	<meta charset="utf-8">
	<style>#spacer { height: 2500px; }</style>
</head>
<body>
	<main>
		<h1>Long Page</h1>
		<p>Content above the fold. <a href="/wiki/Top">Top link</a></p>
		<div id="spacer"></div>
		<button id="deep-btn" onclick="document.getElementById('deep-output').textContent = 'deep clicked'">Deep action</button>
		<div id="deep-output"></div>
	</main>
</body>
</html>`

// TestClick_ScrollsOffscreenElementIntoView: an element outside the viewport
// still appears in the snapshot (the AX tree is layout-independent), and
// clicking its id must scroll it into view and dispatch, not fail with a
// visibility error.
func TestClick_ScrollsOffscreenElementIntoView(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/longpage")

	// Sanity: the button starts below the fold.
	below, err := tab.Eval(ctx, `document.getElementById("deep-btn").getBoundingClientRect().top > innerHeight`)
	if err != nil {
		t.Fatalf("Eval button position: %v", err)
	}
	if below != "true" {
		t.Fatalf("expected the button to start below the fold, got below=%s", below)
	}

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	deepID := findElement(t, snap, `button "Deep action"`)

	if _, err := tab.Click(ctx, deepID); err != nil {
		t.Fatalf("Click below-the-fold button: %v", err)
	}

	out, err := tab.Eval(ctx, `document.getElementById("deep-output").textContent`)
	if err != nil {
		t.Fatalf("Eval output: %v", err)
	}
	if out != "deep clicked" {
		t.Errorf("output = %q, want %q (click handler did not run)", out, "deep clicked")
	}

	// The click can only have landed by scrolling the page.
	y, err := tab.Eval(ctx, "window.scrollY")
	if err != nil {
		t.Fatalf("Eval scrollY: %v", err)
	}
	if y == "0" {
		t.Errorf("window.scrollY is still 0 after clicking a below-the-fold element")
	}
}
