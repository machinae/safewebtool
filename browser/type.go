package browser

import (
	"context"
	"fmt"
	"strings"

	"github.com/machinae/safewebtool/internal/rod/lib/input"
)

// modAliases maps modifier names (case-insensitive) to their left-side keys,
// the same normalization Playwright applies.
var modAliases = map[string]input.Key{
	"shift":   input.ShiftLeft,
	"ctrl":    input.ControlLeft,
	"control": input.ControlLeft,
	"alt":     input.AltLeft,
	"cmd":     input.MetaLeft,
	"meta":    input.MetaLeft,
}

// parseKeys parses a key name ("Enter"), a single character ("a", "#"), or a
// "+"-chord ("Control+Shift+K") into the modifiers to hold and the key to
// press. Names are case-insensitive and follow W3C codes via the rod keymap.
func parseKeys(s string) ([]input.Key, input.Key, error) {
	if r := []rune(s); len(r) == 1 {
		key, err := printableKey(r[0])
		return nil, key, err
	}

	parts := strings.Split(s, "+")
	// A trailing "+" is the "+" key itself ("Control++"), which Split
	// turns into two empty trailing parts.
	if strings.HasSuffix(s, "++") {
		parts = append(strings.Split(s[:len(s)-2], "+"), "+")
	}
	var mods []input.Key
	for _, p := range parts[:len(parts)-1] {
		mod, ok := modAliases[strings.ToLower(p)]
		if !ok {
			return nil, 0, fmt.Errorf(`unknown modifier %q in key %q: modifiers are Control (or Ctrl), Shift, Alt, and Meta (or Cmd)`, p, s)
		}
		mods = append(mods, mod)
	}

	last := parts[len(parts)-1]
	if r := []rune(last); len(r) == 1 {
		key, err := printableKey(r[0])
		return mods, key, err
	}
	if key, ok := input.Lookup(last); ok {
		return mods, key, nil
	}
	if mod, ok := modAliases[strings.ToLower(last)]; ok {
		return mods, mod, nil
	}
	return nil, 0, fmt.Errorf(`unknown key %q: use a single character ("a"), a W3C key name ("Enter", "ArrowDown", "F5"), or a chord of modifiers plus a key ("Control+Shift+K")`, s)
}

// printableKey returns the key for a single character on the US layout.
func printableKey(r rune) (input.Key, error) {
	if r < ' ' || r > '~' {
		return 0, fmt.Errorf(`character %q is not on the US keyboard layout and cannot be sent as keystrokes; use "fill" to set an element's value`, r)
	}
	return input.Key(r), nil
}

// shiftedKey reports whether k is the shifted variant of a base key, like
// "!" (Shift+1) or "A" (Shift+a), and so needs Shift held to type honestly.
func shiftedKey(k input.Key) bool {
	base, ok := input.Lookup(k.Info().Code)
	if !ok || base == k {
		return false
	}
	s, ok := base.Shift()
	return ok && s == k
}

// Type sends a key press to the focused element: a key name, a single
// character, or a modifier chord. Dispatches real keyDown/keyUp CDP events,
// unlike Fill which uses insertText.
func (t *Tab) Type(ctx context.Context, name string) (ActionResult, error) {
	return t.act(ctx, "type", func() (string, error) { return t.doType(ctx, name) })
}

func (t *Tab) doType(ctx context.Context, name string) (string, error) {
	mods, key, err := parseKeys(name)
	if err != nil {
		return "", err
	}
	// KeyActions presses the modifiers, presses and releases the key, then
	// releases the modifiers (balance guarantees the trailing releases).
	if err := t.page.Context(ctx).KeyActions().Press(mods...).Type(key).Do(); err != nil {
		return "", fmt.Errorf("pressing key %q: %w", name, err)
	}

	// Wait for the page to settle so a form submit or navigation triggered
	// by this key (typically Enter) has time to reach the hijack router.
	// The worker only surfaces ErrBlocked from policyErr after fn returns
	// (see tab_worker.go run loop), so returning before the request is
	// dispatched would let the action report "ok" even though the policy
	// blocked the request.
	waitPageSettle(ctx, t.page)
	return "ok", nil
}

// TypeText sends text to the focused element as real keystrokes, one
// keyDown/keyUp pair per character with Shift held for shifted characters.
// This is the write path for canvas editors (Google Docs) that ignore
// insertText; it is US-layout only.
func (t *Tab) TypeText(ctx context.Context, text string) (ActionResult, error) {
	return t.act(ctx, "type", func() (string, error) { return t.doTypeText(ctx, text) })
}

func (t *Tab) doTypeText(ctx context.Context, text string) (string, error) {
	// Build the whole sequence before dispatching so an untypeable
	// character rejects the string without sending half of it.
	ka := t.page.Context(ctx).KeyActions()
	for _, r := range text {
		if r == '\n' {
			ka.Type(input.Enter)
			continue
		}
		if r == '\t' {
			ka.Type(input.Tab)
			continue
		}
		key, err := printableKey(r)
		if err != nil {
			return "", err
		}
		if shiftedKey(key) {
			ka.Press(input.ShiftLeft).Type(key).Release(input.ShiftLeft)
		} else {
			ka.Type(key)
		}
	}
	if err := ka.Do(); err != nil {
		return "", fmt.Errorf("typing text: %w", err)
	}

	// Settle once at the end of the string, not per keystroke (see doType):
	// a mid-string Enter that triggers a blocked navigation is still in
	// flight when this runs, so ErrBlocked surfaces the same way as a
	// single key press. Per-character settling would make sentence-length
	// typing unusably slow.
	waitPageSettle(ctx, t.page)
	return "ok", nil
}
