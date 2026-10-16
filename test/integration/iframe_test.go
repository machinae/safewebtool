//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/machinae/safewebtool/browser"
)

// iframeParentHTML embeds a same-origin child document. The iframe carries a
// title, so its boundary line gets the accessible name "Checkout".
const iframeParentHTML = `<!DOCTYPE html>
<html>
<head><title>Iframe Parent</title><meta charset="utf-8"></head>
<body>
	<main>
		<h1>Parent</h1>
		<button id="parent-btn">Parent button</button>
		<iframe src="/iframe-child" title="Checkout" width="500" height="400"></iframe>
	</main>
</body>
</html>`

// iframeChildHTML is the embedded form. The submit handler rewrites a heading
// so a click inside the frame has an observable, snapshot-visible effect.
const iframeChildHTML = `<!DOCTYPE html>
<html>
<head><title>Child Form</title><meta charset="utf-8"></head>
<body>
	<main>
		<h1>Contact</h1>
		<label for="name-input">Full name</label>
		<input id="name-input" type="text" autocomplete="off">
		<button id="submit-btn">Submit</button>
		<h2 id="result">No submission</h2>
	</main>
	<script>
		document.getElementById("submit-btn").addEventListener("click", function () {
			var v = document.getElementById("name-input").value;
			document.getElementById("result").textContent = "Submitted: " + v;
		});
	</script>
</body>
</html>`

// TestIframe_SameOrigin_SnapshotResolveAct exercises same-origin iframe support
// end to end against real Chrome: the capture descent expands the frame inline
// with composite ids, and those composite ids resolve back through the frame so
// typing and clicking dispatch into the same-process child document. A scoped
// markdown read of a child element confirms the extract path reads back too.
func TestIframe_SameOrigin_SnapshotResolveAct(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/iframe-parent")

	// The frame expands inline: its boundary line carries the title, and its
	// form elements render with composite "<iframe>.<element>" ids.
	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !strings.Contains(snap, `iframe "Checkout"`) {
		t.Fatalf("snapshot missing the iframe boundary line:\n%s", snap)
	}
	for _, want := range []string{`textbox "Full name"`, `button "Submit"`} {
		if !strings.Contains(snap, want) {
			t.Errorf("snapshot missing iframe child %q:\n%s", want, snap)
		}
	}

	// An element inside the frame gets a dotted composite id; that is what
	// proves it lives in the frame and not the main document.
	nameID := findElement(t, snap, `textbox "Full name"`)
	if !strings.Contains(nameID, ".") {
		t.Fatalf("expected a composite (dotted) id for an element inside the iframe, got %q", nameID)
	}

	// Type into the child textbox by its composite id: capture descent +
	// composite resolve + input dispatch into the same-process frame.
	if _, err := tab.Fill(ctx, nameID, "Ada Lovelace"); err != nil {
		t.Fatalf("Type into iframe textbox %q: %v", nameID, err)
	}
	snap, err = tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot after fill: %v", err)
	}
	if !strings.Contains(snap, `value="Ada Lovelace"`) {
		t.Errorf("typed value did not land in the iframe textbox:\n%s", snap)
	}

	// Click the child button; its handler updates a heading inside the frame.
	submitID := findElement(t, snap, `button "Submit"`)
	if _, err := tab.Click(ctx, submitID); err != nil {
		t.Fatalf("Click iframe button %q: %v", submitID, err)
	}
	snap, err = tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot after click: %v", err)
	}
	if !strings.Contains(snap, `heading[2] "Submitted: Ada Lovelace"`) {
		t.Errorf("click inside the iframe did not run its handler:\n%s", snap)
	}

	// Scoped markdown on a composite id reads back the child element's content
	// (the resolved element serialized via the rod HTML primitive), and only
	// that element: parent content is excluded.
	resultID := findElement(t, snap, `heading[2] "Submitted: Ada Lovelace"`)
	md, err := tab.Extract(ctx, false, browser.OutputFilter{IncludeElementIDs: []string{resultID}})
	if err != nil {
		t.Fatalf("Extract scoped to iframe heading %q: %v", resultID, err)
	}
	if !strings.Contains(md, "Submitted: Ada Lovelace") {
		t.Errorf("scoped markdown missing the iframe heading text, got:\n%s", md)
	}
	if strings.Contains(md, "Parent") {
		t.Errorf("scoped markdown should not include parent content, got:\n%s", md)
	}
}
