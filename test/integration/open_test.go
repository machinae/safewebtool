//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/machinae/safewebtool/browser"
)

// A fragment-only URL change is a same-document navigation: Chrome never
// fires a load event for it, so open must not wait for one. Before the
// loader-id check this blocked until the action deadline and reported
// "context deadline exceeded" even though the navigation had committed.
func TestOpen_HashFragmentSameDocument(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	// The spa fixture replaceStates its URL to #home on load, so this
	// open differs from the committed URL only in the fragment.
	tab := openTab(t, ctx, "/spa")

	if _, err := tab.Open(ctx, pageURL("/spa#settings")); err != nil {
		t.Fatalf("Open with fragment: %v", err)
	}

	hash, err := tab.Eval(ctx, "location.hash")
	if err != nil {
		t.Fatalf("Eval location.hash: %v", err)
	}
	if hash != "#settings" {
		t.Errorf("expected location.hash \"#settings\", got %q", hash)
	}
}

// Back and Forward across real document loads. The SPA test covers the
// same-document case; this one crosses two full navigations, so each step
// must wait for the restored document to load before the next read.
func TestBackForward_HardNavigation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/simple")
	if _, err := tab.Open(ctx, pageURL("/extract")); err != nil {
		t.Fatalf("Open /extract: %v", err)
	}

	if _, err := tab.Back(ctx); err != nil {
		t.Fatalf("Back: %v", err)
	}
	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot after back: %v", err)
	}
	if !strings.Contains(snap, "Click me") {
		t.Errorf("expected the simple page after back, got:\n%s", snap)
	}

	if _, err := tab.Forward(ctx); err != nil {
		t.Fatalf("Forward: %v", err)
	}
	snap, err = tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot after forward: %v", err)
	}
	if !strings.Contains(snap, "Main Heading") {
		t.Errorf("expected the extract page after forward, got:\n%s", snap)
	}
}
