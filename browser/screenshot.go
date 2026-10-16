package browser

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/machinae/safewebtool/internal/rod"
	"github.com/machinae/safewebtool/internal/rod/lib/proto"
	"github.com/ysmood/gson"
)

// ScreenshotOpts selects what to capture. Output is always JPEG.
type ScreenshotOpts struct {
	// Full captures the whole scrollable page when true; otherwise just
	// the current viewport. Ignored when Element is set.
	Full bool
	// Element, when non-empty, captures just that element instead of
	// the page or viewport. An element id from the snapshot, same as
	// click/hover. Mutually exclusive with Full; the MCP layer
	// validates that.
	Element string
	// MaxHeight caps the total captured height in CSS pixels for full
	// page captures. Zero means use the safe default (defaultMaxHeight).
	// Only meaningful with Full=true.
	MaxHeight int
}

// screenshotQuality is the JPEG encoding quality. 80 is visually clean
// for UI screenshots while keeping payloads reasonable. No --quality
// flag yet; add when a real case demands tuning.
const screenshotQuality = 80

// defaultMaxHeight is the cap applied to full-page captures when the
// caller does not specify ScreenshotOpts.MaxHeight. Bounds the scroll
// loop on virtualized infinite feeds (X, Reddit) where CSSContentSize
// reports a synthetic height the loop would otherwise chase until the
// caller's context expires.
const defaultMaxHeight = 12000

// Screenshot captures the page, viewport, or a single element as JPEG
// and returns the result as a base64-encoded string.
func (t *Tab) Screenshot(ctx context.Context, opts ScreenshotOpts) (string, error) {
	return t.enqueue(ctx, func() (string, error) { return t.doScreenshot(ctx, opts) })
}

func (t *Tab) doScreenshot(ctx context.Context, opts ScreenshotOpts) (string, error) {
	// Foreground the tab so the compositor is awake. captureScreenshot
	// resolves only once the renderer presents a frame, and a hidden view
	// never presents; with focus emulation on (initPage), capture on an
	// inactive tab can hang outright. This steals the user's foreground,
	// which is the accepted cost of a screenshot. Best-effort: on failure,
	// try the capture anyway and let its own error or timeout speak.
	_, _ = t.doActivate(ctx)

	if opts.Element != "" {
		el, err := t.resolveElement(ctx, opts.Element)
		if err != nil {
			return "", err
		}
		// rod's Element.Screenshot handles ScrollIntoView and crops in
		// Go to work around CDP's buggy Clip option.
		bin, err := el.Context(ctx).Element().Screenshot(proto.PageCaptureScreenshotFormatJpeg, screenshotQuality)
		if err != nil {
			return "", fmt.Errorf("capturing element screenshot: %w", err)
		}
		return base64.StdEncoding.EncodeToString(bin), nil
	}

	// Full-page uses ScrollScreenshot: keeps the viewport at its normal
	// size, scrolls through the page, captures slices, and stitches them
	// in Go. Avoids the giant-bitmap rendering bugs of Page.Screenshot's
	// full-page mode on tall pages (e.g. social feeds at >20kpx).
	if opts.Full {
		quality := screenshotQuality
		maxHeight := opts.MaxHeight
		if maxHeight == 0 {
			maxHeight = defaultMaxHeight
		}
		bin, err := t.page.Context(ctx).ScrollScreenshot(&rod.ScrollScreenshotOptions{
			Format:        proto.PageCaptureScreenshotFormatJpeg,
			Quality:       &quality,
			WaitPerScroll: 500 * time.Millisecond,
			MaxHeight:     float64(maxHeight),
		})
		if err != nil {
			return "", fmt.Errorf("capturing full-page screenshot: %w", err)
		}
		return base64.StdEncoding.EncodeToString(bin), nil
	}

	req := &proto.PageCaptureScreenshot{
		Format:  proto.PageCaptureScreenshotFormatJpeg,
		Quality: gson.Int(screenshotQuality),
	}
	bin, err := t.page.Context(ctx).Screenshot(false, req)
	if err != nil {
		return "", fmt.Errorf("capturing screenshot: %w", err)
	}
	return base64.StdEncoding.EncodeToString(bin), nil
}
