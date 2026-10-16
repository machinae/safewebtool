package browser

import (
	"context"
	"encoding/json"
	"fmt"
)

// CDP sends a raw Chrome DevTools Protocol command and returns the JSON result as a string.
func (t *Tab) CDP(ctx context.Context, method string, params json.RawMessage) (string, error) {
	return t.enqueue(ctx, func() (string, error) { return t.doCDP(ctx, method, params) })
}

func (t *Tab) doCDP(ctx context.Context, method string, params json.RawMessage) (string, error) {
	// Empty/nil params is valid — some CDP methods take no arguments.
	var p map[string]any
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return "", fmt.Errorf("invalid params JSON: %w", err)
		}
	}
	res, err := t.page.Context(ctx).Call(ctx, string(t.page.SessionID), method, p)
	if err != nil {
		return "", fmt.Errorf("cdp %s: %w", method, err)
	}
	return string(res), nil
}
