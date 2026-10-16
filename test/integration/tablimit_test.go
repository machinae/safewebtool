//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
)

// TestOpenTabRefusedAtLimit caps the shared browser at one tab (the
// suite's shared tab is already attached) and expects OpenTab to refuse.
func TestOpenTabRefusedAtLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	b.WithMaxTabs(1)
	defer b.WithMaxTabs(0)

	tab, _, err := b.OpenTab(ctx, pageURL("/simple"))
	if err == nil {
		_ = b.CloseTab(ctx, tab.ID())
		t.Fatal("OpenTab succeeded; want tab limit error")
	}
	if !strings.Contains(err.Error(), "tab limit reached") {
		t.Fatalf("got %v; want a tab limit error", err)
	}
}

// TestOpenTabFailureCleansUp checks that an OpenTab whose navigation
// fails closes the tab it created instead of leaking a blank attached
// tab. Cap at two (the shared tab plus one slot), burn a failed open,
// then a good open must still fit in the freed slot.
func TestOpenTabFailureCleansUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	b.WithMaxTabs(2)
	defer b.WithMaxTabs(0)

	// Port 1 is never listening, so the tab is created but the initial
	// navigation fails with connection refused.
	tab, _, err := b.OpenTab(ctx, "http://127.0.0.1:1/")
	if err == nil {
		_ = b.CloseTab(ctx, tab.ID())
		t.Fatal("OpenTab succeeded; want a navigation error")
	}

	good, _, err := b.OpenTab(ctx, pageURL("/simple"))
	if err != nil {
		t.Fatalf("OpenTab after a failed open: %v; the failed open leaked its tab", err)
	}
	if err := b.CloseTab(ctx, good.ID()); err != nil {
		t.Fatalf("CloseTab: %v", err)
	}
}

// TestOpenTabAllowedUnderLimit opens a second tab with a cap of two.
func TestOpenTabAllowedUnderLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	b.WithMaxTabs(2)
	defer b.WithMaxTabs(0)

	tab, _, err := b.OpenTab(ctx, pageURL("/simple"))
	if err != nil {
		t.Fatalf("OpenTab under limit: %v", err)
	}
	if err := b.CloseTab(ctx, tab.ID()); err != nil {
		t.Fatalf("CloseTab: %v", err)
	}
}
