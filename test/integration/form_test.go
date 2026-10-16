//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/machinae/safewebtool/browser"
)

// TestType_OverwritesExistingValue: Type selects existing text before
// inserting, so a second Type replaces the first value instead of appending
// to it.
func TestType_OverwritesExistingValue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/controlled")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	inputID := findElement(t, snap, `textbox "Task name"`)

	if _, err := tab.Fill(ctx, inputID, "first draft"); err != nil {
		t.Fatalf("Type first value: %v", err)
	}
	if _, err := tab.Fill(ctx, inputID, "replacement"); err != nil {
		t.Fatalf("Type second value: %v", err)
	}

	snap, err = tab.Snapshot(ctx, browser.ModeAll, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot after typing: %v", err)
	}
	if !strings.Contains(snap, "Task: replacement") {
		t.Errorf("expected preview to show the second value, got:\n%s", snap)
	}
	if strings.Contains(snap, "first draft") {
		t.Errorf("first value should have been overwritten, not appended to, got:\n%s", snap)
	}
}

// TestClick_DisabledButtonReturnsErrNotEnabled: the Add task button renders
// disabled while the task input is empty. Clicking it must surface the typed
// ErrNotEnabled instead of silently clicking nothing.
func TestClick_DisabledButtonReturnsErrNotEnabled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/controlled")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	saveID := findElement(t, snap, `button "Add task"`)

	_, err = tab.Click(ctx, saveID)
	var notEnabled *browser.ErrNotEnabled
	if !errors.As(err, &notEnabled) {
		t.Fatalf("Click on disabled button: want ErrNotEnabled, got %v", err)
	}
}

// TestSelect_UnknownOptionReturnsErrOptionNotFound: selecting an option text
// that does not exist must fail with the typed error and leave the select's
// value unchanged.
func TestSelect_UnknownOptionReturnsErrOptionNotFound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/controlled")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	_, err = tab.Select(ctx, findElement(t, snap, `combobox "Priority"`), "Bogus")
	var notFound *browser.ErrOptionNotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("Select with unknown option: want ErrOptionNotFound, got %v", err)
	}
	if notFound.Value != "Bogus" {
		t.Errorf("ErrOptionNotFound.Value = %q, want %q", notFound.Value, "Bogus")
	}

	val, err := tab.Eval(ctx, `document.getElementById("priority-select").value`)
	if err != nil {
		t.Fatalf("Eval select value: %v", err)
	}
	if val != "Normal" {
		t.Errorf("select value = %q after failed select, want unchanged %q", val, "Normal")
	}
}
