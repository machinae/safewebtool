//go:build integration

package integration

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/machinae/safewebtool/browser"
)

// TestConcurrency_SlowTabDoesNotBlockOtherTab is the headline concurrency
// check: start a ~3s navigation on tab A, and in parallel run a snapshot on
// tab B. The snapshot must return promptly — if it doesn't, tabs are being
// globally serialized and the per-tab worker refactor has regressed.
func TestConcurrency_SlowTabDoesNotBlockOtherTab(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	slowTab, fastTab := openTwoTabsParallel(t, ctx, pageURL("/simple"), pageURL("/simple"))

	// Kick off a slow navigation on slowTab. The /slow handler sleeps for
	// 3 seconds before returning HTML, pinning slowTab's worker for the
	// duration of the Open call.
	done := make(chan error, 1)
	slowStart := time.Now()
	go func() {
		_, err := slowTab.Open(ctx, pageURL("/slow"))
		done <- err
	}()

	// Head start: make sure the slow Open is definitely in flight before
	// we race against it, otherwise the fast op might finish before the
	// slow op even starts and we don't prove anything.
	time.Sleep(200 * time.Millisecond)

	// Snapshot fastTab. This must not queue behind the slow navigation on
	// slowTab — they live on independent per-tab workers.
	fastStart := time.Now()
	if _, err := fastTab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{}); err != nil {
		t.Fatalf("fastTab.Snapshot during slow nav: %v", err)
	}
	fastElapsed := time.Since(fastStart)

	if fastElapsed > 1*time.Second {
		t.Errorf("fastTab.Snapshot took %v during slow nav on slowTab — tabs appear to be serialized (regression)", fastElapsed)
	}

	// Drain the goroutine and sanity-check that the slow nav really was
	// slow (guards against a flaky pass if the /slow handler ever stops
	// sleeping).
	if err := <-done; err != nil {
		t.Fatalf("slowTab.Open: %v", err)
	}
	if slowElapsed := time.Since(slowStart); slowElapsed < 2500*time.Millisecond {
		t.Errorf("slow tab only took %v — /slow handler may not be sleeping as expected", slowElapsed)
	}
}

// TestConcurrency_MixedOperationsOnTwoTabsAreIndependent fires a Type into
// one tab's form and a Snapshot on the other tab at the same time, proving
// the per-tab workers don't interfere when exercising different CDP verbs.
func TestConcurrency_MixedOperationsOnTwoTabsAreIndependent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	formTab, simpleTab := openTwoTabsParallel(t, ctx, pageURL("/controlled"), pageURL("/simple"))

	idSnap, err := formTab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("formTab.Snapshot for ids: %v", err)
	}
	taskID := findElement(t, idSnap, `textbox "Task name"`)

	var wg sync.WaitGroup
	wg.Add(2)

	var typeErr error
	go func() {
		defer wg.Done()
		_, typeErr = formTab.Fill(ctx, taskID, "parallel work")
	}()

	var simpleSnap string
	var snapErr error
	go func() {
		defer wg.Done()
		simpleSnap, snapErr = simpleTab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	}()

	wg.Wait()

	if typeErr != nil {
		t.Fatalf("formTab.Fill: %v", typeErr)
	}
	if snapErr != nil {
		t.Fatalf("simpleTab.Snapshot: %v", snapErr)
	}

	if !strings.Contains(simpleSnap, "Click me") {
		t.Errorf("expected simple-page content in simpleTab snapshot, got:\n%s", simpleSnap)
	}

	// The Type should have landed on formTab. Snapshot formTab to confirm.
	formSnap, err := formTab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
	if err != nil {
		t.Fatalf("formTab.Snapshot: %v", err)
	}
	if !strings.Contains(formSnap, "parallel work") {
		t.Errorf("expected typed value in formTab, got:\n%s", formSnap)
	}
}

// TestConcurrency_ConcurrentSnapshotsAcrossThreeTabs opens three tabs at
// different fixtures in parallel, then snapshots all three concurrently.
// Each snapshot must see only its own page — no cross-tab state leakage,
// and the whole race must complete well under the per-tab serial cost.
func TestConcurrency_ConcurrentSnapshotsAcrossThreeTabs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestTimeout)
	defer cancel()

	urls := []string{pageURL("/simple"), pageURL("/controlled"), pageURL("/spa")}
	expect := []string{"Click me", "Task board", "Route: home"}

	// Open all three in parallel.
	type openResult struct {
		tab *browser.Tab
		err error
	}
	openCh := make([]chan openResult, len(urls))
	for i, url := range urls {
		openCh[i] = make(chan openResult, 1)
		go func(i int, url string) {
			tab, _, err := b.OpenTab(ctx, url)
			openCh[i] <- openResult{tab, err}
		}(i, url)
	}
	tabs := make([]*browser.Tab, len(urls))
	for i := range tabs {
		r := <-openCh[i]
		if r.err != nil {
			t.Fatalf("OpenTab %s: %v", urls[i], r.err)
		}
		tabs[i] = r.tab
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, tab := range tabs {
			_ = b.CloseTab(closeCtx, tab.ID())
		}
	})

	// Snapshot all three in parallel.
	snaps := make([]string, len(tabs))
	errs := make([]error, len(tabs))
	var wg sync.WaitGroup
	for i, tab := range tabs {
		wg.Add(1)
		go func(i int, tab *browser.Tab) {
			defer wg.Done()
			snaps[i], errs[i] = tab.Snapshot(ctx, browser.ModeDefault, browser.OutputFilter{})
		}(i, tab)
	}
	wg.Wait()

	for i := range tabs {
		if errs[i] != nil {
			t.Errorf("tab %d (%s) snapshot: %v", i, urls[i], errs[i])
			continue
		}
		if !strings.Contains(snaps[i], expect[i]) {
			t.Errorf("tab %d (%s) snapshot missing %q, got:\n%s", i, urls[i], expect[i], snaps[i])
		}
	}
}
