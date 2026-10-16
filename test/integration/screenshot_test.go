//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/machinae/safewebtool/browser"
)

// JPEG starts with FF D8 FF.
var jpegMagic = []byte{0xFF, 0xD8, 0xFF}

func TestScreenshot_ViewportJPEG(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/simple")

	b64, err := tab.Screenshot(ctx, browser.ScreenshotOpts{})
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}

	path := filepath.Join(t.TempDir(), "viewport.jpg")
	data := writeScreenshotFile(t, path, b64)
	if !bytes.HasPrefix(data, jpegMagic) {
		t.Errorf("expected JPEG magic %x, got %x", jpegMagic, data[:min(len(data), 4)])
	}
}

func TestScreenshot_FullPageJPEG_LargerThanViewport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	// /dynamic has min-height: 200vh so the full-page capture is meaningfully
	// taller than the viewport. If full-page worked correctly the JPEG bytes
	// should be larger than the viewport-only capture.
	tab := openTab(t, ctx, "/dynamic")

	viewportB64, err := tab.Screenshot(ctx, browser.ScreenshotOpts{})
	if err != nil {
		t.Fatalf("Screenshot viewport: %v", err)
	}
	viewportPath := filepath.Join(t.TempDir(), "viewport.jpg")
	viewportData := writeScreenshotFile(t, viewportPath, viewportB64)

	fullB64, err := tab.Screenshot(ctx, browser.ScreenshotOpts{Full: true})
	if err != nil {
		t.Fatalf("Screenshot full: %v", err)
	}
	fullPath := filepath.Join(t.TempDir(), "full.jpg")
	fullData := writeScreenshotFile(t, fullPath, fullB64)

	if !bytes.HasPrefix(fullData, jpegMagic) {
		t.Errorf("expected JPEG magic for full-page, got %x", fullData[:min(len(fullData), 4)])
	}
	if len(fullData) <= len(viewportData) {
		t.Errorf("full-page JPEG should be larger than viewport (full=%d viewport=%d)",
			len(fullData), len(viewportData))
	}
}

func TestScreenshot_ElementJPEG(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	tab := openTab(t, ctx, "/simple")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	btnID := findElement(t, snap, `button "Click me"`)

	b64, err := tab.Screenshot(ctx, browser.ScreenshotOpts{
		Element: btnID,
	})
	if err != nil {
		t.Fatalf("Screenshot element: %v", err)
	}

	path := filepath.Join(t.TempDir(), "element.jpg")
	data := writeScreenshotFile(t, path, b64)
	if !bytes.HasPrefix(data, jpegMagic) {
		t.Errorf("expected JPEG magic for element, got %x", data[:min(len(data), 4)])
	}
	// A single button cropped from the page should be small.
	if len(data) > 50*1024 {
		t.Errorf("element screenshot unexpectedly large: %d bytes", len(data))
	}
}

// writeScreenshotFile decodes a base64 screenshot, writes it to path, and
// returns the decoded bytes. Failures fail the test.
func writeScreenshotFile(t *testing.T, path, b64 string) []byte {
	t.Helper()
	if b64 == "" {
		t.Fatalf("screenshot base64 is empty")
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decoding screenshot base64: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("decoded screenshot has 0 bytes")
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	t.Logf("wrote %s (%d bytes)", path, len(data))
	return data
}
