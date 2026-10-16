package browser

import (
	"context"
	"fmt"

	"github.com/machinae/safewebtool/internal/rod"
)

// Eval executes a JavaScript expression in the page and returns the result.
// Uses CDP Runtime.evaluate directly (like the Chrome console) so any
// expression works, not just function bodies.
func (t *Tab) Eval(ctx context.Context, js string) (string, error) {
	return t.enqueue(ctx, func() (string, error) { return t.doEval(ctx, js) })
}

func (t *Tab) doEval(ctx context.Context, js string) (string, error) {
	// Rod expects a function definition — it calls .apply() on the expression.
	// Wrap in an async arrow so arbitrary expressions work and top-level await
	// is supported. ByUser marks the call as a user gesture so APIs gated on
	// user activation (clipboard, media autoplay) work from the agent.
	wrapped := fmt.Sprintf("async () => { return (%s); }", js)
	result, err := t.page.Context(ctx).Evaluate(rod.Eval(wrapped).ByPromise().ByUser())
	if err != nil {
		return "", fmt.Errorf("evaluating JS: %w", err)
	}
	return result.Value.String(), nil
}
