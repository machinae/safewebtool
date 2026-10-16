package browser

import (
	"context"
	"errors"
	"fmt"

	"github.com/machinae/safewebtool/internal/rod"
	"github.com/machinae/safewebtool/internal/rod/lib/proto"
)

// Click finds an element by selector and clicks it. Actionability checks run
// first: WaitStable (animations settled), Disabled (not disabled), and
// Interactable (visible, not obscured, pointer-events ok). Resolution and
// those checks are retried once as a pair if a check fails (a re-render can
// detach the node mid-pipeline; see resolvePrepared); the mouse events after
// them run exactly once. Uses explicit MoveTo + Mouse.Click rather than
// Rod's el.Click so the typed errors from Interactable (ErrObscured,
// ErrNotVisible, ErrNoPointerEvents) surface to the agent instead of being
// buried in a generic click error.
func (t *Tab) Click(ctx context.Context, selector string) (ActionResult, error) {
	return t.act(ctx, "click", func() (string, error) { return t.doClick(ctx, selector) })
}

func (t *Tab) doClick(ctx context.Context, selector string) (string, error) {
	el, err := t.clickEl(ctx, selector)
	if err != nil {
		return "", err
	}
	return el.String(), nil
}

func (t *Tab) clickEl(ctx context.Context, selector string) (*Element, error) {
	var pt proto.Point
	el, err := t.resolvePrepared(ctx, selector, func(el *Element) error {
		el = el.Context(ctx)

		if err := el.Element().WaitStable(stableQuietPeriod); err != nil {
			// The fail-fast sleeper turns WaitStable's visibility
			// precheck into ElementNotFoundError: the node is detached
			// or has no visible shape, not unstable.
			var gone *rod.ElementNotFoundError
			if errors.As(err, &gone) {
				return &ErrNotVisible{Sel: el.String()}
			}
			return &ErrNotStable{Sel: el.String()}
		}

		disabled, err := el.Element().Disabled()
		if err != nil {
			return fmt.Errorf("checking disabled state: %w", err)
		}
		if disabled {
			return &ErrNotEnabled{Sel: el.String()}
		}

		if err := el.Element().ScrollIntoViewNoWait(); err != nil {
			return fmt.Errorf("scrolling element into view: %w", err)
		}

		p, err := el.Element().Interactable()
		if err != nil {
			return translateInteractableErr(err, el.String())
		}
		pt = *p
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Capture the URL before the click so waitNavSettle can tell whether the
	// click navigated, including a same-document SPA route that changes the URL
	// but fires no load event, and wait for the new content if so.
	urlBefore := t.PageURL(ctx)

	mouse := t.page.Context(ctx).Mouse

	if err := mouse.MoveTo(pt); err != nil {
		return nil, fmt.Errorf("moving mouse to element: %w", err)
	}

	if err := mouse.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return nil, fmt.Errorf("clicking element: %w", err)
	}

	// Settle, waiting out any navigation the click triggered so a same-batch
	// read sees the new page rather than the old one or a bare loading shell.
	// This also gives the navigation or POST time to reach the hijack router:
	// the worker only surfaces ErrBlocked from policyErr after fn returns (see
	// tab_worker.go run loop), so returning before the request is dispatched
	// would let the action report "ok" even though the policy blocked it.
	t.waitNavSettle(ctx, urlBefore)
	return el, nil
}
