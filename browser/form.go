package browser

import (
	"context"
	"errors"
	"fmt"

	"github.com/machinae/safewebtool/internal/rod"
)

// Fill finds an element by selector and sets its value to text. Uses Rod's
// Element.Input (CDP Input.insertText) so the entire string is inserted in
// one call — like a paste. Avoids the bot-detection fingerprint of uniformly-
// timed synthetic keystrokes, while still firing isTrusted input events
// compatible with React/Vue controlled inputs. Existing text is selected
// first so the new text overwrites it (select all → insert).
func (t *Tab) Fill(ctx context.Context, selector string, text string) (ActionResult, error) {
	return t.act(ctx, "fill", func() (string, error) { return t.doFill(ctx, selector, text) })
}

func (t *Tab) doFill(ctx context.Context, selector string, text string) (string, error) {
	el, err := t.fillEl(ctx, selector, text)
	if err != nil {
		return "", err
	}
	return el.String(), nil
}

func (t *Tab) fillEl(ctx context.Context, selector string, text string) (*Element, error) {
	el, err := t.resolvePrepared(ctx, selector, func(el *Element) error {
		el = el.Context(ctx)

		// A focused element receives input regardless of geometry, so it
		// skips the visibility gate below. A11y capture sinks (Google
		// Docs' hidden editor iframe body) are deliberately tiny or
		// off-screen and would fail it.
		focused, err := el.Element().Eval(`() => this === document.activeElement || this.contains(document.activeElement)`)
		if err != nil {
			return fmt.Errorf("checking focus: %w", err)
		}
		if focused.Value.Bool() {
			return nil
		}

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

		if _, err := el.Element().Interactable(); err != nil {
			return translateInteractableErr(err, el.String())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Select existing text so new text replaces it. Fails silently on
	// contenteditable elements (no .select() method).
	_ = el.Element().SelectAllText()

	if err := el.Element().Input(text); err != nil {
		return nil, fmt.Errorf("typing text: %w", err)
	}

	return el, nil
}

// Select finds a <select> element and picks the option matching the given visible text.
func (t *Tab) Select(ctx context.Context, selector string, value string) (ActionResult, error) {
	return t.act(ctx, "select", func() (string, error) { return t.doSelect(ctx, selector, value) })
}

func (t *Tab) doSelect(ctx context.Context, selector string, value string) (string, error) {
	el, err := t.selectEl(ctx, selector, value)
	if err != nil {
		return "", err
	}
	return el.String(), nil
}

func (t *Tab) selectEl(ctx context.Context, selector string, value string) (*Element, error) {
	el, err := t.resolveElement(ctx, selector)
	if err != nil {
		return nil, err
	}

	el = el.Context(ctx)

	if err := el.Element().Select([]string{value}, true, rod.SelectorTypeText); err != nil {
		var notFound *rod.ElementNotFoundError
		if errors.As(err, &notFound) {
			return nil, &ErrOptionNotFound{Sel: el.String(), Value: value}
		}
		return nil, fmt.Errorf("selecting option %q: %w", value, err)
	}

	return el, nil
}

// Upload sets one or more files on a <input type="file"> element.
func (t *Tab) Upload(ctx context.Context, selector string, files []string) (ActionResult, error) {
	return t.act(ctx, "upload", func() (string, error) { return t.doUpload(ctx, selector, files) })
}

func (t *Tab) doUpload(ctx context.Context, selector string, files []string) (string, error) {
	el, err := t.uploadEl(ctx, selector, files)
	if err != nil {
		return "", err
	}
	return el.String(), nil
}

func (t *Tab) uploadEl(ctx context.Context, selector string, files []string) (*Element, error) {
	el, err := t.resolveElement(ctx, selector)
	if err != nil {
		return nil, err
	}

	el = el.Context(ctx)

	if err := el.Element().SetFiles(files); err != nil {
		return nil, fmt.Errorf("setting files: %w", err)
	}

	return el, nil
}
