//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/machinae/safewebtool/browser"
)

func TestHover_RevealsHiddenActions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/dynamic")

	// Before hover, the action buttons should not be visible.
	snap, err := tab.Snapshot(ctx, browser.ModeAll, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot before hover: %v", err)
	}
	if strings.Contains(snap, "Archive") {
		t.Fatalf("expected Archive button to be hidden before hover, got:\n%s", snap)
	}

	// Hover over the card's heading to reveal actions. The card div itself
	// is a generic container with no accessibility presence (no element id),
	// but the pointer landing on the heading is inside the card, so the
	// card's mouseenter fires.
	if _, err := tab.Hover(ctx, findElement(t, snap, `heading[2] "Project Alpha"`)); err != nil {
		t.Fatalf("Hover: %v", err)
	}

	snap, err = tab.Snapshot(ctx, browser.ModeAll, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot after hover: %v", err)
	}

	if !strings.Contains(snap, "Archive") {
		t.Errorf("expected Archive button visible after hover, got:\n%s", snap)
	}
	if !strings.Contains(snap, "Delete") {
		t.Errorf("expected Delete button visible after hover, got:\n%s", snap)
	}
}
