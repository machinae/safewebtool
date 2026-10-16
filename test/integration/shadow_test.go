//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/machinae/safewebtool/browser"
)

func TestShadowDOM_SnapshotAndClick(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/dynamic")

	// Question 1: Does the snapshot include shadow DOM content?
	snap, err := tab.Snapshot(ctx, browser.ModeAll, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if !strings.Contains(snap, "Shadow Heading") {
		t.Errorf("snapshot does not contain shadow DOM heading")
	}
	if !strings.Contains(snap, "Shadow Action") {
		t.Errorf("snapshot does not contain shadow DOM button")
	}

	// Question 2: Can we click a shadow DOM element by backendNodeId?
	btnID := findElement(t, snap, "Shadow Action")

	if _, err := tab.Click(ctx, btnID); err != nil {
		t.Fatalf("Click shadow button by backendNodeId: %v", err)
	}

	// Question 3: Did the click actually work inside the shadow root?
	snap, err = tab.Snapshot(ctx, browser.ModeAll, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot after click: %v", err)
	}

	if !strings.Contains(snap, "Shadow clicked") {
		t.Errorf("expected 'Shadow clicked' after clicking shadow button, got:\n%s", snap)
	}
}
