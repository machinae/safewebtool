//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/machinae/safewebtool/browser"
)

// navSpaHTML reproduces the LinkedIn company-page bug on the local server: a
// click that does a same-document SPA navigation (history.pushState, so the URL
// changes but no load event fires), swaps in a bare "0 notifications" skeleton,
// then loads the real content async. A persistent nav keeps the page non-empty
// so the failure is "content missing", not "snapshot empty". The 800ms delay is
// longer than two settle ticks, so without the fix the click and the following
// snapshot both settle on the skeleton; with it, the click waits for the
// content to swap in before settling.
const navSpaHTML = `<!DOCTYPE html>
<html>
<head><title>SPA Nav</title><meta charset="utf-8"></head>
<body>
	<nav><a href="/home">Home</a></nav>
	<div id="content">
		<main><h1>Profile</h1><a id="go-company" href="/company">Acme Corp</a></main>
	</div>
	<script>
	(() => {
		document.addEventListener("click", (e) => {
			if (e.target.id !== "go-company") return;
			e.preventDefault();
			history.pushState({}, "", "/company");
			const c = document.getElementById("content");
			c.innerHTML = "<main><h1>0 notifications</h1></main>";
			setTimeout(() => {
				c.innerHTML =
					"<main><h1>Acme Corp Overview</h1>" +
					"<a href=\"/about\">About us</a>" +
					"<p>The real company content.</p></main>";
			}, 800);
		});
	})();
	</script>
</body>
</html>`

// TestClick_SoftNavWaitsForContent: a click that triggers a same-document SPA
// navigation must wait for the new content to replace the loading skeleton
// before the next read. The fixture changes the URL via pushState, shows a bare
// skeleton, and loads the real content 800ms later. Without the wait, the
// same-batch snapshot captures the skeleton and misses the content (the
// LinkedIn company-page bug).
func TestClick_SoftNavWaitsForContent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/nav-spa")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	linkID := findElement(t, snap, `link "Acme Corp"`)

	if _, err := tab.Click(ctx, linkID); err != nil {
		t.Fatalf("Click company link: %v", err)
	}

	snap2, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot after soft nav: %v", err)
	}
	if !strings.Contains(snap2, "Acme Corp Overview") {
		t.Errorf("after soft-nav click, snapshot is missing the async content; settled on the skeleton?\n%s", snap2)
	}
	if strings.Contains(snap2, "0 notifications") {
		t.Errorf("after soft-nav click, snapshot still shows the loading skeleton:\n%s", snap2)
	}
}
