//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/machinae/safewebtool/browser"
)

// TestTabHandle_PrefixResolvesToSameTab: resolving an attached tab's 7-char
// prefix through the coordinator returns the same *Tab (same worker), not a
// second attachment driving the same page.
func TestTabHandle_PrefixResolvesToSameTab(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := newTab(t, ctx, pageURL("/simple"))

	got, err := b.Tab(ctx, tab.ID())
	if err != nil {
		t.Fatalf("Tab(%s): %v", tab.ID(), err)
	}
	if got != tab {
		t.Errorf("Tab(%s) returned a different *Tab than the one OpenTab handed out", tab.ID())
	}

	snap, err := got.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot via resolved handle: %v", err)
	}
	if !strings.Contains(snap, "Click me") {
		t.Errorf("expected simple page content via resolved handle, got:\n%s", snap)
	}
}

// TestTabHandle_BadPrefixFailsLoudly covers the two lookup error paths: a
// well-formed prefix matching no tab, and a malformed (wrong-length) prefix.
func TestTabHandle_BadPrefixFailsLoudly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	// Target IDs are hex, so a "z" prefix can never match.
	_, err := b.Tab(ctx, "zzzzzzz")
	if err == nil {
		t.Fatal("Tab with unmatched prefix: want error, got success")
	}
	if !strings.Contains(err.Error(), "tab not found") {
		t.Errorf("error = %q, want the tab-not-found error", err)
	}

	_, err = b.Tab(ctx, "abc")
	if err == nil {
		t.Fatal("Tab with short prefix: want error, got success")
	}
	if !strings.Contains(err.Error(), "7 chars") {
		t.Errorf("error = %q, want the prefix-length error", err)
	}
}

// TestTabInfo_ReportsMetadata: Info returns the tab's prefix, live title and
// URL, and reports it attached.
func TestTabInfo_ReportsMetadata(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	url := pageURL("/simple")
	tab := newTab(t, ctx, url)

	info, err := tab.Info(ctx)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Prefix != tab.ID() {
		t.Errorf("Info.Prefix = %q, want %q", info.Prefix, tab.ID())
	}
	if info.URL != url {
		t.Errorf("Info.URL = %q, want %q", info.URL, url)
	}
	if info.Title != "Simple Test" {
		t.Errorf("Info.Title = %q, want %q", info.Title, "Simple Test")
	}
	if !info.Attached {
		t.Errorf("Info.Attached = false for a live worker-owned tab")
	}
}

// TestClosedTab_HandleReturnsErrTabClosed: after CloseTab, actions on the
// stale *Tab handle must fail with the typed ErrTabClosed, not hang or act
// on a dead page.
func TestClosedTab_HandleReturnsErrTabClosed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab, _, err := b.OpenTab(ctx, pageURL("/simple"))
	if err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	if err := b.CloseTab(ctx, tab.ID()); err != nil {
		t.Fatalf("CloseTab: %v", err)
	}

	_, err = tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	var closed *browser.ErrTabClosed
	if !errors.As(err, &closed) {
		t.Errorf("Snapshot on closed tab handle: want ErrTabClosed, got %v", err)
	}
}

// TestVersion_ReturnsProduct: the liveness probe returns Chrome's product
// string over the shared connection.
func TestVersion_ReturnsProduct(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	v, err := b.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if !strings.Contains(v, "Chrome") {
		t.Errorf("Version = %q, want a Chrome product string", v)
	}
}
