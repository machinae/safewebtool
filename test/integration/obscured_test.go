//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/machinae/safewebtool/browser"
)

// obscuredHTML simulates the common first-load cookie banner: a full-viewport
// dialog overlay covers the page, its consent panel sits at the bottom, and
// the "Submit order" button up top is hit-tested against the overlay itself.
const obscuredHTML = `<!DOCTYPE html>
<html>
<head>
	<title>Obscured Test</title>
	<style>
		#consent { position: fixed; inset: 0; background: rgba(0, 0, 0, 0.4); }
		#consent .panel { position: absolute; bottom: 0; left: 0; right: 0; background: white; padding: 16px; }
	</style>
</head>
<body>
	<h1>Checkout</h1>
	<button id="submit" onclick="document.getElementById('output').textContent = 'submitted'">Submit order</button>
	<div id="output"></div>
	<div id="consent" role="dialog" aria-label="Cookie consent">
		<div class="panel">
			<p>We use cookies to improve your experience.</p>
			<button onclick="document.getElementById('consent').style.display = 'none'">Accept all</button>
		</div>
	</div>
</body>
</html>`

// TestObscured_BlockerNamedAndDismissable covers the cookie-banner recovery
// loop: a click on a covered element fails with ErrObscured naming the
// blocker by role and name, and after dismissing the blocker the id from the
// original snapshot still resolves and the click succeeds.
func TestObscured_BlockerNamedAndDismissable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/obscured")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	submitID := findElement(t, snap, "Submit order")
	acceptID := findElement(t, snap, "Accept all")

	_, err = tab.Click(ctx, submitID)
	var obscured *browser.ErrObscured
	if !errors.As(err, &obscured) {
		t.Fatalf("Click on covered button: want ErrObscured, got %v", err)
	}
	if obscured.Blocker != `dialog "Cookie consent"` {
		t.Errorf("Blocker = %q, want %q", obscured.Blocker, `dialog "Cookie consent"`)
	}

	if _, err := tab.Click(ctx, acceptID); err != nil {
		t.Fatalf("Click 'Accept all': %v", err)
	}
	if _, err := tab.Click(ctx, submitID); err != nil {
		t.Fatalf("Click 'Submit order' after dismissing overlay: %v", err)
	}

	snap, err = tab.Snapshot(ctx, browser.ModeAll, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot after submit: %v", err)
	}
	if !strings.Contains(snap, "submitted") {
		t.Errorf("expected 'submitted' in snapshot after click, got:\n%s", snap)
	}
}
