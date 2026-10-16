//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/machinae/safewebtool/browser"
)

func TestSimple_SnapshotShowsElements(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/simple")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if !strings.Contains(snap, "Click me") {
		t.Errorf("expected 'Click me' button in snapshot, got:\n%s", snap)
	}
	if !strings.Contains(snap, "Hello") {
		t.Errorf("expected 'Hello' heading in snapshot, got:\n%s", snap)
	}
}

func TestSimple_ClickUpdatesDOM(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/simple")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	btnID := findElement(t, snap, "Click me")

	if _, err := tab.Click(ctx, btnID); err != nil {
		t.Fatalf("Click: %v", err)
	}

	// After clicking, the JS should have written "clicked" into the output div.
	snap, err = tab.Snapshot(ctx, browser.ModeAll, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot after click: %v", err)
	}

	if !strings.Contains(snap, "clicked") {
		t.Errorf("expected 'clicked' in snapshot after button click, got:\n%s", snap)
	}
}
