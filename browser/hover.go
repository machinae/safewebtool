package browser

import (
	"context"
	"fmt"

	"github.com/machinae/safewebtool/internal/rod/lib/proto"
)

// Hover moves the mouse over an element without clicking. Triggers CSS :hover
// states and JS mouseenter/mouseover events. Runs the same prepare pipeline
// as click minus the stability wait (scroll into view, interactable check
// with typed errors), retried once as a pair with resolution, then dispatches
// a single mouse move. We don't use rod's Hover: its WaitInteractable retries
// forever while the element is covered instead of failing fast.
func (t *Tab) Hover(ctx context.Context, selector string) (ActionResult, error) {
	return t.act(ctx, "hover", func() (string, error) { return t.doHover(ctx, selector) })
}

func (t *Tab) doHover(ctx context.Context, selector string) (string, error) {
	el, err := t.hoverEl(ctx, selector)
	if err != nil {
		return "", err
	}
	return el.String(), nil
}

func (t *Tab) hoverEl(ctx context.Context, selector string) (*Element, error) {
	var pt proto.Point
	el, err := t.resolvePrepared(ctx, selector, func(el *Element) error {
		el = el.Context(ctx)

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

	if err := t.page.Context(ctx).Mouse.MoveTo(pt); err != nil {
		return nil, fmt.Errorf("moving mouse to element: %w", err)
	}
	return el, nil
}
