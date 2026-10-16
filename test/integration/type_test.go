//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/machinae/safewebtool/browser"
)

// keysHTML has two labelled inputs for focus traversal and a keydown log
// that records every key name the page receives, proving Type dispatches
// real keydown events (unlike Fill's insertText).
const keysHTML = `<!DOCTYPE html>
<html>
<head><title>Key Test</title><meta charset="utf-8"></head>
<body>
	<main>
		<h1>Keys</h1>
		<label for="first">First field</label>
		<input id="first" type="text" autocomplete="off">
		<label for="second">Second field</label>
		<input id="second" type="text" autocomplete="off">
		<div id="key-log"></div>
	</main>
	<script>
		document.addEventListener("keydown", (e) => {
			document.getElementById("key-log").textContent += e.key + ";";
		});
	</script>
</body>
</html>`

// TestType_TabMovesFocusAndKeydownReachesPage: pressing Tab must move focus
// to the next field (the browser's default handling of a real key event),
// and the page's keydown listener must see each key by name.
func TestType_TabMovesFocusAndKeydownReachesPage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/keys")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if _, err := tab.Click(ctx, findElement(t, snap, `textbox "First field"`)); err != nil {
		t.Fatalf("Click to focus first field: %v", err)
	}
	focused, err := tab.Eval(ctx, "document.activeElement.id")
	if err != nil {
		t.Fatalf("Eval activeElement: %v", err)
	}
	if focused != "first" {
		t.Fatalf("expected focus on #first after click, got %q", focused)
	}

	if _, err := tab.Type(ctx, "Tab"); err != nil {
		t.Fatalf("Type Tab: %v", err)
	}
	focused, err = tab.Eval(ctx, "document.activeElement.id")
	if err != nil {
		t.Fatalf("Eval activeElement after Tab: %v", err)
	}
	if focused != "second" {
		t.Errorf("expected Tab to move focus to #second, got %q", focused)
	}

	if _, err := tab.Type(ctx, "Escape"); err != nil {
		t.Fatalf("Type Escape: %v", err)
	}
	log, err := tab.Eval(ctx, `document.getElementById("key-log").textContent`)
	if err != nil {
		t.Fatalf("Eval key log: %v", err)
	}
	if log != "Tab;Escape;" {
		t.Errorf("key log = %q, want %q (keydown events by name, in order)", log, "Tab;Escape;")
	}
}

// TestType_UnknownKeyNameFailsLoudly: a key name outside the W3C map must
// error instead of silently pressing nothing.
func TestType_UnknownKeyNameFailsLoudly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/keys")

	_, err := tab.Type(ctx, "Hyperspace")
	if err == nil {
		t.Fatal("Type with unknown key name: want error, got success")
	}
	if !strings.Contains(err.Error(), "unknown key") {
		t.Errorf("error = %q, want the unknown-key error", err)
	}
}

// TestType_ChordShiftTabMovesFocusBackward: a modifier chord must dispatch
// with the modifier held, so Shift+Tab triggers the browser's reverse focus
// traversal.
func TestType_ChordShiftTabMovesFocusBackward(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/keys")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if _, err := tab.Click(ctx, findElement(t, snap, `textbox "Second field"`)); err != nil {
		t.Fatalf("Click to focus second field: %v", err)
	}

	if _, err := tab.Type(ctx, "Shift+Tab"); err != nil {
		t.Fatalf("Type Shift+Tab: %v", err)
	}
	focused, err := tab.Eval(ctx, "document.activeElement.id")
	if err != nil {
		t.Fatalf("Eval activeElement: %v", err)
	}
	if focused != "first" {
		t.Errorf("expected Shift+Tab to move focus back to #first, got %q", focused)
	}
}

// TestType_TextSendsKeystrokes: text mode must type the string into the
// focused element as real keystrokes, including shifted characters.
func TestType_TextSendsKeystrokes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/keys")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if _, err := tab.Click(ctx, findElement(t, snap, `textbox "First field"`)); err != nil {
		t.Fatalf("Click to focus first field: %v", err)
	}

	if _, err := tab.TypeText(ctx, "Hi, World!"); err != nil {
		t.Fatalf("TypeText: %v", err)
	}
	value, err := tab.Eval(ctx, `document.getElementById("first").value`)
	if err != nil {
		t.Fatalf("Eval value: %v", err)
	}
	if value != "Hi, World!" {
		t.Errorf("value = %q, want %q", value, "Hi, World!")
	}
}

// TestType_TextRejectsNonUSLayout: a character with no keydown sequence must
// reject the whole string before any key is dispatched.
func TestType_TextRejectsNonUSLayout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/keys")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if _, err := tab.Click(ctx, findElement(t, snap, `textbox "First field"`)); err != nil {
		t.Fatalf("Click to focus first field: %v", err)
	}

	_, err = tab.TypeText(ctx, "héllo")
	if err == nil {
		t.Fatal("TypeText with non-US character: want error, got success")
	}
	if !strings.Contains(err.Error(), "US keyboard layout") {
		t.Errorf("error = %q, want the US-layout error", err)
	}
	value, err := tab.Eval(ctx, `document.getElementById("first").value`)
	if err != nil {
		t.Fatalf("Eval value: %v", err)
	}
	if value != "" {
		t.Errorf("value = %q, want empty: nothing may be typed when the string is rejected", value)
	}
}

// captureHTML mimics a canvas app's keyboard capture sink: an off-screen
// input that grabs focus on load. The visibility gate would reject it; the
// focused-element exception must let Fill through.
const captureHTML = `<!DOCTYPE html>
<html>
<head><title>Capture Test</title><meta charset="utf-8"></head>
<body>
	<main>
		<h1>Capture</h1>
		<input id="cap" aria-label="Capture" type="text" autocomplete="off"
			style="position:absolute; left:-9999px; width:10px; height:10px">
	</main>
	<script>document.getElementById("cap").focus();</script>
</body>
</html>`

// TestFill_FocusedOffscreenElement: Fill must accept an element that holds
// focus even though it is off-screen and would fail the visibility check.
func TestFill_FocusedOffscreenElement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/capture")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if _, err := tab.Fill(ctx, findElement(t, snap, `textbox "Capture"`), "hello"); err != nil {
		t.Fatalf("Fill focused off-screen input: %v", err)
	}
	value, err := tab.Eval(ctx, `document.getElementById("cap").value`)
	if err != nil {
		t.Fatalf("Eval value: %v", err)
	}
	if value != "hello" {
		t.Errorf("value = %q, want %q", value, "hello")
	}
}
