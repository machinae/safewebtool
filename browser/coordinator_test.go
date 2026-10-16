package browser

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/machinae/safewebtool/internal/rod/lib/proto"
)

func TestIsUserTab(t *testing.T) {
	cases := []struct {
		name string
		info *proto.TargetTargetInfo
		want bool
	}{
		{"http page", &proto.TargetTargetInfo{Type: proto.TargetTargetInfoTypePage, URL: "http://example.com"}, true},
		{"https page", &proto.TargetTargetInfo{Type: proto.TargetTargetInfoTypePage, URL: "https://example.com/path"}, true},
		{"chrome internal", &proto.TargetTargetInfo{Type: proto.TargetTargetInfoTypePage, URL: "chrome://newtab/"}, false},
		{"devtools", &proto.TargetTargetInfo{Type: proto.TargetTargetInfoTypePage, URL: "devtools://devtools/bundled/inspector.html"}, false},
		{"about blank", &proto.TargetTargetInfo{Type: proto.TargetTargetInfoTypePage, URL: "about:blank"}, false},
		{"non-page type", &proto.TargetTargetInfo{Type: proto.TargetTargetInfoTypeServiceWorker, URL: "https://example.com/sw.js"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUserTab(tc.info); got != tc.want {
				t.Fatalf("isUserTab(%q) = %v, want %v", tc.info.URL, got, tc.want)
			}
		})
	}
}

func TestMatchPrefix(t *testing.T) {
	targets := []*proto.TargetTargetInfo{
		{TargetID: "abc1234deadbeef"},
		{TargetID: "abc1234cafebabe"},
		{TargetID: "xyz9876feedface"},
		{TargetID: "short"}, // len < 7, must be ignored
	}

	cases := []struct {
		name    string
		prefix  string
		wantIDs []string
	}{
		{"no match", "zzz0000", nil},
		{"single match", "xyz9876", []string{"xyz9876feedface"}},
		{"two matches", "abc1234", []string{"abc1234deadbeef", "abc1234cafebabe"}},
		{"mixed case prefix matches lowercase id", "ABC1234", []string{"abc1234deadbeef", "abc1234cafebabe"}},
		{"short target ignored", "short12", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			matches := matchPrefix(tc.prefix, targets)
			gotIDs := make([]string, len(matches))
			for i, m := range matches {
				gotIDs[i] = string(m.TargetID)
			}
			if len(gotIDs) != len(tc.wantIDs) {
				t.Fatalf("got %v, want %v", gotIDs, tc.wantIDs)
			}
			for i := range gotIDs {
				if gotIDs[i] != tc.wantIDs[i] {
					t.Fatalf("got %v, want %v", gotIDs, tc.wantIDs)
				}
			}
		})
	}
}

// The public handle is always lowercase, whatever case Chrome's target
// id uses (real CDP target ids are uppercase hex).
func TestPrefixOfLowercase(t *testing.T) {
	if got := prefixOf("ABC1234DEADBEEF"); got != "abc1234" {
		t.Errorf("prefixOf = %q; want abc1234", got)
	}
	if got := prefixOf("SHORT"); got != "short" {
		t.Errorf("prefixOf short id = %q; want short", got)
	}
}

func TestCheckURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"http", "http://example.com", false},
		{"https", "https://example.com", false},
		{"https with path", "https://example.com/path?q=1", false},
		{"about:blank", "about:blank", false},
		{"HTTP uppercase", "HTTP://example.com", false},
		{"HTTPS mixed case", "HtTpS://example.com", false},
		{"file URL", "file:///etc/passwd", true},
		{"file URL Windows", "file:///C:/Users/secret.txt", true},
		{"chrome URL", "chrome://settings", true},
		{"chrome extension", "chrome-extension://abc123/page.html", true},
		{"devtools", "devtools://devtools/bundled/inspector.html", true},
		{"javascript", "javascript:alert(1)", true},
		{"data URL", "data:text/html,<h1>hi</h1>", true},
		{"ftp", "ftp://example.com/file", true},
		{"no scheme", "example.com", true},
		{"empty string", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("checkURL(%q) error = %v, wantErr = %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestAttachedSnapshot(t *testing.T) {
	b := New()

	const liveID = "abc1234deadbeef"
	live := newTab(liveID, nil)
	b.known[liveID] = live

	const quarantinedID = "def5678cafebabe"
	quarantined := newTab(quarantinedID, nil)
	quarantined.cancel()
	b.known[quarantinedID] = quarantined

	snap := b.attachedSnapshot()

	if !snap[liveID] {
		t.Errorf("expected live tab attached=true, got %v", snap[liveID])
	}
	if snap[quarantinedID] {
		t.Errorf("expected quarantined tab attached=false, got %v", snap[quarantinedID])
	}
	if _, ok := snap["xyz9876feedface"]; ok {
		t.Errorf("expected missing tab absent from snapshot")
	}
}

func TestClose_CleansState(t *testing.T) {
	b := New()

	const id1 = "abc1234deadbeef"
	const id2 = "def5678cafebabe"
	tab1 := newTab(id1, nil)
	tab2 := newTab(id2, nil)
	b.known[id1] = tab1
	b.known[id2] = tab2

	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if len(b.known) != 0 {
		t.Errorf("expected known cleared, got %d entries", len(b.known))
	}
	if b.rod.Load() != nil {
		t.Errorf("expected rod nil after Close")
	}
	if tab1.ctx.Err() == nil || tab2.ctx.Err() == nil {
		t.Errorf("expected both tab ctxs cancelled")
	}
}

// TestClose_Concurrent hammers Close alongside IsConnected/getRod readers
// to catch deadlocks and data races. The -race detector surfaces any
// unsynchronized access; a 2s deadline catches a hang.
func TestClose_Concurrent(t *testing.T) {
	b := New()
	for i := range 10 {
		id := string(rune('a'+i)) + "bc1234deadbee"
		b.known[id] = newTab(id, nil)
	}

	done := make(chan struct{})
	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
					_ = b.IsConnected()
					_, _ = b.getRod()
				}
			}
		})
	}

	for range 4 {
		wg.Go(func() {
			_ = b.Close()
		})
	}

	time.Sleep(100 * time.Millisecond)
	close(done)

	waitDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: goroutines did not finish in 2s")
	}
}

func TestAttach_QuarantinedReturnsErrTabClosed(t *testing.T) {
	b := New()
	const targetID = "abc1234deadbeef"

	// Insert a Tab whose lifecycle ctx is already cancelled — simulating
	// a Tab.Close(ctx) that timed out and left the tab quarantined.
	quarantined := newTab(targetID, nil)
	quarantined.cancel()
	b.known[targetID] = quarantined

	tab, err := b.attach(context.Background(), targetID)
	var tabClosed *ErrTabClosed
	if !errors.As(err, &tabClosed) {
		t.Fatalf("attach err = %v, want *ErrTabClosed", err)
	}
	if tab != nil {
		t.Fatalf("attach returned tab = %v, want nil", tab)
	}
}
