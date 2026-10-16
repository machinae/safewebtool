package browser

import (
	"context"
	"fmt"

	"github.com/machinae/safewebtool/internal/rod"
)

// Element wraps a rod element with accessibility metadata. The Role and Name
// fields are populated from the accessibility tree at resolution time, before
// any action is taken on the element.
type Element struct {
	el   *rod.Element
	Role string // e.g. "button", "textbox", "link"
	Name string // accessible name, e.g. "Sign in", "Email"
}

// Element returns the underlying rod element for direct rod API calls.
func (e *Element) Element() *rod.Element {
	return e.el
}

// Context sets the context on the underlying rod element and returns
// the same Element, so callers can write el = el.Context(ctx).
func (e *Element) Context(ctx context.Context) *Element {
	e.el = e.el.Context(ctx)
	return e
}

// String renders the element as the short response payload for
// element-returning actions, e.g. `button "Sign in"`.
func (e *Element) String() string {
	if e.Name != "" {
		return fmt.Sprintf("%s %q", e.Role, e.Name)
	}
	return "ok"
}
