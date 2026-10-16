package browser

import (
	"testing"

	"github.com/machinae/safewebtool/policy"
)

func TestWithChromeDataDir(t *testing.T) {
	b := New().WithChromeDataDir("/custom/chrome/data")

	if b.ChromeDataDir != "/custom/chrome/data" {
		t.Errorf("got ChromeDataDir %q, want %q", b.ChromeDataDir, "/custom/chrome/data")
	}
}

func TestWithURL(t *testing.T) {
	b := New().WithURL("ws://127.0.0.1:9222/devtools/browser/abc")

	if b.WSUrl != "ws://127.0.0.1:9222/devtools/browser/abc" {
		t.Errorf("got WSUrl %q, want %q", b.WSUrl, "ws://127.0.0.1:9222/devtools/browser/abc")
	}
}

// Policy reads must be safe against a concurrent ReloadPolicy (run under
// -race; authorizeAction calls Policy() on every action while the fsnotify
// watcher or the GUI can reload at any time).
func TestPolicyReloadConcurrent(t *testing.T) {
	b := New().WithPolicy(&policy.Policy{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			_ = b.Policy()
		}
	}()
	for range 1000 {
		if err := b.ReloadPolicy(&policy.Policy{}); err != nil {
			t.Fatalf("ReloadPolicy: %v", err)
		}
	}
	<-done
}

func TestChaining(t *testing.T) {
	b := New().WithChromeDataDir("/data").WithURL("ws://localhost:1234")

	if b.ChromeDataDir != "/data" {
		t.Errorf("got ChromeDataDir %q, want %q", b.ChromeDataDir, "/data")
	}
	if b.WSUrl != "ws://localhost:1234" {
		t.Errorf("got WSUrl %q, want %q", b.WSUrl, "ws://localhost:1234")
	}
}
