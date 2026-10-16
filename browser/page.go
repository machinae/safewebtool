package browser

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/machinae/safewebtool/internal/rod"
	"github.com/machinae/safewebtool/internal/rod/lib/proto"
)

// initPage applies the one-time session setup every attached page gets.
//
// Accessibility.enable keeps Chrome's AX tree computed for the session, so
// snapshots read a warm tree instead of racing the lazy first build.
//
// Emulation.setFocusEmulationEnabled makes the page believe it is focused,
// and its browser-side handler registers a visible capturer, which forces
// the page to render as visible. Our tabs are permanently background tabs:
// without this, timers are throttled, the compositor produces no frames (so
// no rAF and no IntersectionObserver delivery, which is what gates
// lazy-loading on sites like X), mousemove and wheel events sit in the
// renderer queue for up to 5s, and document.hasFocus() reads false.
//
// Best-effort: a failure degrades quality (colder snapshots, unfocused
// behavior) but must not fail the attach; both calls are logged on error.
// Session-scoped: Chrome drops both on detach, and a re-attach re-runs this.
//
// Callers must run this before the tab becomes reachable by other
// goroutines: it drives page directly, outside the tab's worker.
func initPage(ctx context.Context, page *rod.Page) {
	p := page.Context(ctx)
	if err := (proto.AccessibilityEnable{}).Call(p); err != nil {
		log.Printf("initPage: accessibility enable: %v", err)
	}
	if err := (proto.EmulationSetFocusEmulationEnabled{Enabled: true}).Call(p); err != nil {
		log.Printf("initPage: focus emulation: %v", err)
	}
}

// stableQuietPeriod is the duration an element's position and size must
// remain unchanged before it is considered stable. This ensures animations,
// layout shifts, and transitions have settled before acting on the element.
const stableQuietPeriod = 500 * time.Millisecond

// PageSettleTimeout is the maximum time to wait for the DOM to settle
// before a read (snapshot, extract). If the page is still changing after
// this duration, we return whatever state exists — the agent sees the
// page as-of-now rather than hanging indefinitely.
var PageSettleTimeout = 2 * time.Second

// pageSettleTick is the interval between DOM snapshot comparisons during
// post-action stabilization.
const pageSettleTick = 250 * time.Millisecond

// pageSettleDiff is the maximum percentage of DOM change (0.0–1.0)
// considered "stable." 0.01 = 1% change tolerance, which ignores noise
// like timestamps and cursor blinks while catching meaningful re-renders.
const pageSettleDiff = 0.01

// navDetectGrace bounds how long waitNavSettle watches the URL for a click-
// triggered navigation. Detection exits the moment the URL changes, so only a
// click that does NOT navigate pays this in full before settling. Keep it
// short; it is added latency on every non-navigating click.
const navDetectGrace = 200 * time.Millisecond

// navDetectTick is the URL poll interval inside navDetectGrace.
const navDetectTick = 25 * time.Millisecond

// waitPageSettle waits for the DOM to stabilize. Called at the head of
// read verbs (snapshot, extract) so the agent sees a quiesced page.
// Timeout errors are silently ignored — they mean the page is still busy,
// and the caller sees whatever state exists.
func waitPageSettle(ctx context.Context, page *rod.Page) {
	waitCtx, cancel := context.WithTimeout(ctx, PageSettleTimeout)
	defer cancel()
	_ = page.Context(waitCtx).WaitDOMStable(pageSettleTick, pageSettleDiff)
}

// waitPageLoad runs navigate, then waits for the CDP load event and for the
// DOM to settle. navigate returns the navigation's loader id; the wait is
// pinned to it so a replayed lifecycle event from the OLD document (or an
// iframe's load) can't satisfy it. An empty loader id means the browser
// performed a same-document navigation (fragment-only URL change): no load
// event will ever fire for those, so waiting would block until the ctx
// deadline. The skipped wait's event subscription is cleaned up when ctx ends.
func waitPageLoad(ctx context.Context, page *rod.Page, navigate func() (proto.NetworkLoaderID, error)) error {
	wait := page.Context(ctx).WaitNavigation(proto.PageLifecycleEventNameLoad)
	loaderID, err := navigate()
	if err != nil {
		return err
	}
	if loaderID != "" {
		if err := wait(loaderID); err != nil {
			return fmt.Errorf("waiting for load event: %w", err)
		}
	}
	waitPageSettle(ctx, page)
	return nil
}

// PageURL returns the tab's current document URL, or "" if it cannot be read.
// A same-document SPA navigation (history.pushState) updates this, so it is the
// signal waitNavSettle uses to tell whether a click navigated.
func (t *Tab) PageURL(ctx context.Context) string {
	res, err := proto.TargetGetTargetInfo{TargetID: proto.TargetTargetID(t.targetID)}.Call(t.page.Context(ctx))
	if err != nil {
		return ""
	}
	return res.TargetInfo.URL
}

// waitNavSettle settles the page after a click. If the click changed the URL (a
// navigation, including a same-document SPA route that fires no load event), the
// read taken immediately after can catch a bare loading skeleton whose real
// content arrives async; so on a URL change it first waits for the DOM to grow
// past that skeleton. A click that did not navigate settles immediately, exactly
// as before. urlBefore is the URL captured before the click.
func (t *Tab) waitNavSettle(ctx context.Context, urlBefore string) {
	if t.urlChanged(ctx, urlBefore) {
		t.waitForContent(ctx)
	}
	waitPageSettle(ctx, t.page)
}

// urlChanged reports whether the page URL moved away from urlBefore within
// navDetectGrace, i.e. the click triggered a navigation (hard or soft). It
// returns as soon as the URL differs. An unchanged URL means the click did not
// navigate (or only mutated the current page, e.g. toggled a control).
func (t *Tab) urlChanged(ctx context.Context, urlBefore string) bool {
	ctx, cancel := context.WithTimeout(ctx, navDetectGrace)
	defer cancel()
	for {
		if u := t.PageURL(ctx); u != "" && u != urlBefore {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(navDetectTick):
		}
	}
}

// waitForContent waits for the DOM to grow past a loading skeleton after a
// navigation. A same-document SPA route can swap in a bare shell first and load
// the real content async; settling then would capture the shell. It captures a
// baseline DOM snapshot and returns once the snapshot's shared string table
// changes by more than pageSettleDiff (content swapped in) or PageSettleTimeout
// elapses. Reuses the same CaptureDOMSnapshot WaitDOMStable diffs, comparing
// table size rather than running the full LCS.
func (t *Tab) waitForContent(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, PageSettleTimeout)
	defer cancel()

	base, err := t.page.Context(ctx).CaptureDOMSnapshot()
	if err != nil || base == nil {
		return // can't baseline; waitPageSettle handles whatever state exists
	}
	baseLen := len(base.Strings)

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(pageSettleTick):
		}
		cur, err := t.page.Context(ctx).CaptureDOMSnapshot()
		if err != nil || cur == nil {
			continue
		}
		if changedBeyond(baseLen, len(cur.Strings), pageSettleDiff) {
			return
		}
	}
}

// changedBeyond reports whether the DOM snapshot string-table size moved from
// base to cur by more than frac (a fraction, e.g. 0.01 for 1%).
func changedBeyond(base, cur int, frac float64) bool {
	if base == 0 {
		return cur > 0
	}
	delta := cur - base
	if delta < 0 {
		delta = -delta
	}
	return float64(delta)/float64(base) > frac
}
