package browser

import (
	"context"
	"fmt"
)

// scrollJS aims, scrolls, and reports in one eval. It walks up from the
// element at the viewport center (the hit-test a center-dispatched wheel
// would do) to find the real page scroller, then scrolls it and returns the
// pixels actually applied. Running through Eval keeps the call bounded by the
// per-action ctx and works on occluded tabs, unlike a synthetic wheel: see
// plans/scroll-fix.md and plans/timeouts.md root cause 1.
const scrollJS = `(pixels) => {
	const cx = innerWidth / 2, cy = innerHeight / 2;
	const scrollable = (el) => {
		if (el.scrollHeight <= el.clientHeight) return false;
		const oy = getComputedStyle(el).overflowY;
		return oy === 'auto' || oy === 'scroll';
	};
	// Skip trivially small scrollers (embedded code blocks, maps): a real page
	// scroller fills at least a third of the viewport. Chrome's own scroll
	// chaining would also pass through small exhausted scrollers; the height
	// guard approximates that.
	let target = null;
	for (let el = document.elementFromPoint(cx, cy); el; el = el.parentElement) {
		if (scrollable(el) && el.clientHeight >= innerHeight / 3) { target = el; break; }
	}
	if (!target) target = document.scrollingElement;
	const before = target.scrollTop;
	// behavior: 'instant' overrides CSS scroll-behavior: smooth, so the
	// position read right after is the final one.
	target.scrollBy({ top: pixels, behavior: 'instant' });
	return target.scrollTop - before;
}`

// Scroll scrolls the page by the given number of pixels. Positive scrolls down.
func (t *Tab) Scroll(ctx context.Context, pixels int) (ActionResult, error) {
	return t.act(ctx, "scroll", func() (string, error) { return t.doScroll(ctx, pixels) })
}

func (t *Tab) doScroll(ctx context.Context, pixels int) (string, error) {
	res, err := t.page.Context(ctx).Eval(scrollJS, pixels)
	if err != nil {
		return "", fmt.Errorf("scrolling: %w", err)
	}
	// scrollBy with behavior: 'instant' updates scrollTop before the eval
	// returns, so this delta is the final applied scroll, no wait needed.
	applied := int(res.Value.Num())
	if applied == 0 {
		return "no scroll applied (already at edge?)", nil
	}
	return fmt.Sprintf("scrolled %dpx", applied), nil
}

// ScrollPage scrolls by one viewport height. Direction is set by up. The
// viewport read and the scroll run inside one worker job so a concurrent
// queued action on the same tab cannot race the viewport read.
func (t *Tab) ScrollPage(ctx context.Context, up bool) (ActionResult, error) {
	return t.act(ctx, "scroll", func() (string, error) { return t.doScrollPage(ctx, up) })
}

func (t *Tab) doScrollPage(ctx context.Context, up bool) (string, error) {
	pixels := 600
	if bounds, err := t.page.Context(ctx).GetWindow(); err == nil && bounds.Height != nil {
		pixels = *bounds.Height
	}
	if up {
		pixels = -pixels
	}
	return t.doScroll(ctx, pixels)
}
