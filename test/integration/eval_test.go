//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/machinae/safewebtool/browser"
)

func TestEval_ReturnsDocumentTitle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/simple")

	result, err := tab.Eval(ctx, "document.title")
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}

	if result != "Simple Test" {
		t.Errorf("expected 'Simple Test', got %q", result)
	}
}

func TestEval_Arithmetic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/simple")

	result, err := tab.Eval(ctx, "1 + 1")
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}

	if result != "2" {
		t.Errorf("expected '2', got %q", result)
	}
}

// A throwing expression must surface as an error carrying the JS exception
// message, not as a stringified success.
func TestEval_JSExceptionReturnsError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/simple")

	_, err := tab.Eval(ctx, `(() => { throw new Error("boom") })()`)
	if err == nil {
		t.Fatal("Eval of throwing expression: want error, got success")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %q, want it to carry the JS exception message", err)
	}
}

func TestEval_MutatesDOMVisibleInSnapshot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/simple")

	if _, err := tab.Eval(ctx, `document.querySelector('h1').textContent = 'Modified'`); err != nil {
		t.Fatalf("Eval: %v", err)
	}

	snap, err := tab.Snapshot(ctx, browser.ModeAll, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if !strings.Contains(snap, "Modified") {
		t.Errorf("expected 'Modified' in snapshot after eval, got:\n%s", snap)
	}
}
