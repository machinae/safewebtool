//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/machinae/safewebtool/browser"
)

func TestUpload_FileInputShowsFilename(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	// Create a temp file to upload.
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "testfile.txt")
	if err := os.WriteFile(tmpFile, []byte("hello"), 0644); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}

	tab := openTab(t, ctx, "/controlled")

	snap, err := tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// The file input renders as a button named by its label.
	if _, err := tab.Upload(ctx, findElement(t, snap, `button "Attachment"`), []string{tmpFile}); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	snap, err = tab.Snapshot(ctx, browser.ModeAll, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if !strings.Contains(snap, "testfile.txt") {
		t.Errorf("expected filename in snapshot after upload, got:\n%s", snap)
	}
}
