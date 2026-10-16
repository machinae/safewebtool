//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/machinae/safewebtool/browser"
)

func TestExtract_FullPageMarkdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/extract")

	md, err := tab.Extract(ctx, false, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if !strings.Contains(md, "Main Heading") {
		t.Errorf("expected heading in markdown, got:\n%s", md)
	}
	if !strings.Contains(md, "main content paragraph") {
		t.Errorf("expected paragraph text in markdown, got:\n%s", md)
	}
	if !strings.Contains(md, "Example Link") {
		t.Errorf("expected link text in markdown, got:\n%s", md)
	}
	if !strings.Contains(md, "Footer content") {
		t.Errorf("expected footer in full page extract, got:\n%s", md)
	}
}

func TestExtract_ScopedToMain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/extract")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	md, err := tab.Extract(ctx, false, browser.OutputFilter{IncludeElementIDs: []string{findElement(t, snap, "] main")}})
	if err != nil {
		t.Fatalf("Extract scoped to main: %v", err)
	}

	if !strings.Contains(md, "Main Heading") {
		t.Errorf("expected heading in main extract, got:\n%s", md)
	}
	if !strings.Contains(md, "main content paragraph") {
		t.Errorf("expected paragraph in main extract, got:\n%s", md)
	}
	if strings.Contains(md, "Footer content") {
		t.Errorf("main extract should not contain footer, got:\n%s", md)
	}
}

func TestExtract_RawHTML(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/extract")

	html, err := tab.Extract(ctx, true, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Extract --html: %v", err)
	}

	if !strings.Contains(html, "<h1>") {
		t.Errorf("expected HTML tags in raw extract, got:\n%s", html)
	}
	if !strings.Contains(html, "Main Heading") {
		t.Errorf("expected heading text in raw extract, got:\n%s", html)
	}
}

func TestExtract_ScopedToElement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/extract")

	// The text-only list is visible in an all-mode snapshot (no interactive
	// children, so default mode prunes it).
	snap, err := tab.Snapshot(ctx, browser.ModeAll, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	md, err := tab.Extract(ctx, false, browser.OutputFilter{IncludeElementIDs: []string{findElement(t, snap, "] list")}})
	if err != nil {
		t.Fatalf("Extract scoped to list: %v", err)
	}

	if !strings.Contains(md, "Item one") || !strings.Contains(md, "Item two") {
		t.Errorf("expected list items in scoped extract, got:\n%s", md)
	}
	if strings.Contains(md, "Main Heading") {
		t.Errorf("scoped extract should not contain heading, got:\n%s", md)
	}
}
