//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
)

// TestCDP_PassthroughReturnsJSON: the raw CDP verb reaches Chrome on this
// tab's session and returns the method's JSON result.
func TestCDP_PassthroughReturnsJSON(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/simple")

	res, err := tab.CDP(ctx, "Page.getNavigationHistory", nil)
	if err != nil {
		t.Fatalf("CDP Page.getNavigationHistory: %v", err)
	}
	if !strings.Contains(res, "entries") {
		t.Errorf("expected navigation history JSON with entries, got:\n%s", res)
	}
	if !strings.Contains(res, "/simple") {
		t.Errorf("expected the current URL in navigation history, got:\n%s", res)
	}
}

// TestCDP_UnknownMethodFails: Chrome's error for a nonexistent method must
// surface instead of an empty success.
func TestCDP_UnknownMethodFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/simple")

	if _, err := tab.CDP(ctx, "Bogus.method", nil); err == nil {
		t.Fatal("CDP with unknown method: want error, got success")
	}
}
