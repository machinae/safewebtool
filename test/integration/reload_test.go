//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/machinae/safewebtool/browser"
)

func TestReload_RestoresOriginalContent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/dynamic")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Increment the counter to change the page state.
	if _, err := tab.Click(ctx, findElement(t, snap, `button "Increment"`)); err != nil {
		t.Fatalf("Click increment: %v", err)
	}

	// Verify increment took effect using Eval to read the exact counter value.
	result, err := tab.Eval(ctx, `document.getElementById("counter-value").textContent`)
	if err != nil {
		t.Fatalf("Eval counter after increment: %v", err)
	}
	if result != "1" {
		t.Fatalf("expected counter to be '1' after increment, got %q", result)
	}

	// Reload should reset counter back to 0.
	if _, err := tab.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	snap, err = tab.Snapshot(ctx, browser.ModeAll, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot after reload: %v", err)
	}

	if !strings.Contains(snap, "Dashboard") {
		t.Errorf("expected page content after reload, got:\n%s", snap)
	}

	// Verify the counter reset by checking the counter value element.
	result, err = tab.Eval(ctx, `document.getElementById("counter-value").textContent`)
	if err != nil {
		t.Fatalf("Eval counter after reload: %v", err)
	}
	if result != "0" {
		t.Errorf("expected counter to reset to 0 after reload, got %q", result)
	}
}
