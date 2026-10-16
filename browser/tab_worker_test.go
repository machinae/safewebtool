package browser

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Arbitrary 16-char hex target ID; ID() returns the first 7 chars.
const testTargetID = "0123456789abcdef"

// runTestTab returns a running Tab with a nil page. Tests supply inline
// fn closures and never touch t.page, so nil is safe. Cleanup calls
// Close() which signals shutdown and waits for the worker to exit.
//
// Tests that spawn helper goroutines must still join them (via wg.Wait)
// before returning, so cleanup does not observe pending sends.
func runTestTab(t *testing.T) *Tab {
	t.Helper()
	tab := newTab(testTargetID, nil)
	go tab.run()
	t.Cleanup(func() {
		// Bounded cleanup: if a test leaves an fn wedged, we do not
		// want the whole suite to hang. One second is plenty for
		// cooperating tests.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tab.Close(ctx)
	})
	return tab
}

// waitForBuffer polls until the job buffer has reached the given length.
// Used to synchronize on observable queue state instead of fixed sleeps.
func waitForBuffer(t *testing.T, tab *Tab, n int) {
	t.Helper()
	for range 200 {
		if len(tab.chJobs) >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("buffer never reached len %d (have %d)", n, len(tab.chJobs))
}

func TestTab_IDPrefix(t *testing.T) {
	tab := newTab(testTargetID, nil)
	if got, want := tab.ID(), testTargetID[:7]; got != want {
		t.Fatalf("ID() = %q, want %q", got, want)
	}
}

// The worker must run one fn at a time; cross-job overlap would let a
// snapshot fire against a page another command is mutating.
func TestTab_SerializesOneAtATime(t *testing.T) {
	tab := runTestTab(t)
	var running, maxObs atomic.Int32
	const N = 10
	var wg sync.WaitGroup
	for range N {
		wg.Go(func() {
			_, _ = tab.enqueue(context.Background(), func() (string, error) {
				n := running.Add(1)
				for {
					m := maxObs.Load()
					if n <= m || maxObs.CompareAndSwap(m, n) {
						break
					}
				}
				time.Sleep(5 * time.Millisecond)
				running.Add(-1)
				return "", nil
			})
		})
	}
	wg.Wait()
	if got := maxObs.Load(); got != 1 {
		t.Fatalf("max concurrent fns = %d, want 1", got)
	}
}

// FIFO within a single submitter: sequential enqueues from one
// goroutine execute in submit order. This is the only FIFO guarantee
// we make to agents — each MCP command is a separate synchronous
// enqueue, so within a single request stream commands serialize in
// order.
//
// We deliberately do NOT test FIFO across concurrent senders. Go's
// runtime happens to queue buffered-channel sends in FIFO order, but
// that is a runtime implementation detail, not a language guarantee,
// and the webtool API makes no such promise to agents.
func TestTab_FIFOOrder(t *testing.T) {
	tab := runTestTab(t)

	const N = 5
	var order []int
	for i := range N {
		_, err := tab.enqueue(context.Background(), func() (string, error) {
			order = append(order, i)
			return "", nil
		})
		if err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	for i, v := range order {
		if v != i {
			t.Fatalf("order[%d] = %d, want %d; full: %v", i, v, i, order)
		}
	}
}

// A job whose ctx has expired before the worker pulls it must not run.
// A backed-up queue must never fire stale actions (plan §9).
func TestTab_StaleJobSkipped(t *testing.T) {
	tab := runTestTab(t)

	chRelease := make(chan struct{})
	chStarted := make(chan struct{})
	chDoneErr := make(chan error, 1)
	var ran atomic.Bool
	var wg sync.WaitGroup

	// Job 1: occupies the worker until chRelease closes.
	wg.Go(func() {
		_, _ = tab.enqueue(context.Background(), func() (string, error) {
			close(chStarted)
			<-chRelease
			return "", nil
		})
	})
	<-chStarted

	// Job 2: cancellable; reports its error via chDoneErr.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wg.Go(func() {
		_, err := tab.enqueue(ctx, func() (string, error) {
			ran.Store(true)
			return "", nil
		})
		chDoneErr <- err
	})
	waitForBuffer(t, tab, 1)

	cancel()         // expire ctx before the worker pulls job 2.
	close(chRelease) // let fn 1 complete so the worker advances.
	wg.Wait()

	err := <-chDoneErr
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("enqueue err = %v, want context.Canceled", err)
	}
	if ran.Load() {
		t.Fatal("stale fn ran; must be skipped")
	}
}

// When chJobs is full, enqueue must block on send and abort via ctx.
func TestTab_EnqueueCtxCancel_Queueing(t *testing.T) {
	tab := runTestTab(t)

	chRelease := make(chan struct{})
	chStarted := make(chan struct{})
	var wg sync.WaitGroup

	wg.Go(func() {
		_, _ = tab.enqueue(context.Background(), func() (string, error) {
			close(chStarted)
			<-chRelease
			return "", nil
		})
	})
	<-chStarted

	wg.Go(func() {
		_, _ = tab.enqueue(context.Background(), func() (string, error) { return "", nil })
	})
	waitForBuffer(t, tab, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	var ran atomic.Bool
	_, err := tab.enqueue(ctx, func() (string, error) {
		ran.Store(true)
		return "", nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("enqueue err = %v, want DeadlineExceeded", err)
	}
	if ran.Load() {
		t.Fatal("fn 3 ran; must be skipped (never reached the queue)")
	}

	close(chRelease)
	wg.Wait()
}

// With ctx expiring on the reply leg, enqueue returns promptly; the
// worker keeps running fn to completion and the buffered reply channel
// absorbs the discarded result.
func TestTab_EnqueueCtxCancel_Waiting(t *testing.T) {
	tab := runTestTab(t)

	chRelease := make(chan struct{})
	defer close(chRelease)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := tab.enqueue(ctx, func() (string, error) {
		<-chRelease
		return "ok", nil
	})
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("enqueue err = %v, want DeadlineExceeded", err)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("enqueue returned late: %v", elapsed)
	}
}

// A policy block stashed during the job must override a success result.
func TestTab_PolicyErrOverridesSuccess(t *testing.T) {
	tab := runTestTab(t)
	blockedErr := &ErrBlocked{Method: "GET", URL: "https://blocked.example", Rule: "deny"}
	_, err := tab.enqueue(context.Background(), func() (string, error) {
		tab.mu.Lock()
		tab.policyErr = blockedErr
		tab.mu.Unlock()
		return "ok", nil
	})
	var gotErr *ErrBlocked
	if !errors.As(err, &gotErr) || gotErr != blockedErr {
		t.Fatalf("err = %v, want %v", err, blockedErr)
	}
}

// A policy block stashed during the job must override whatever error fn
// returned.
func TestTab_PolicyErrOverridesFnErr(t *testing.T) {
	tab := runTestTab(t)
	blockedErr := &ErrBlocked{Method: "POST", URL: "https://blocked.example", Rule: "deny"}
	fnErr := errors.New("rod failure")
	_, err := tab.enqueue(context.Background(), func() (string, error) {
		tab.mu.Lock()
		tab.policyErr = blockedErr
		tab.mu.Unlock()
		return "", fnErr
	})
	var gotErr *ErrBlocked
	if !errors.As(err, &gotErr) || gotErr != blockedErr {
		t.Fatalf("err = %v, want %v", err, blockedErr)
	}
}

// A stale policy block left from a prior cycle must be cleared before
// the next job runs; otherwise it would falsely attach to an unrelated
// job.
func TestTab_ClearPolicyErrBetweenJobs(t *testing.T) {
	tab := runTestTab(t)

	staleErr := &ErrBlocked{Method: "GET", URL: "https://stale", Rule: "deny"}
	tab.mu.Lock()
	tab.policyErr = staleErr
	tab.mu.Unlock()

	_, err := tab.enqueue(context.Background(), func() (string, error) { return "ok", nil })
	if err != nil {
		t.Fatalf("first job err = %v, want nil (staleErr should be cleared)", err)
	}

	freshErr := &ErrBlocked{Method: "POST", URL: "https://fresh", Rule: "deny"}
	_, err = tab.enqueue(context.Background(), func() (string, error) {
		tab.mu.Lock()
		tab.policyErr = freshErr
		tab.mu.Unlock()
		return "ok", nil
	})
	var gotErr *ErrBlocked
	if !errors.As(err, &gotErr) || gotErr != freshErr {
		t.Fatalf("second job err = %v, want %v", err, freshErr)
	}
}

// Close on an idle worker returns promptly with no error.
func TestTab_CleanShutdown(t *testing.T) {
	tab := newTab(testTargetID, nil)
	go tab.run()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := tab.Close(ctx); err != nil {
		t.Fatalf("Close err = %v, want nil", err)
	}
}

// Close must be safe to call multiple times — cancel() is idempotent
// and the second Close sees chDone already closed.
func TestTab_CloseIsIdempotent(t *testing.T) {
	tab := newTab(testTargetID, nil)
	go tab.run()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := tab.Close(ctx); err != nil {
		t.Fatalf("first Close err = %v, want nil", err)
	}
	if err := tab.Close(ctx); err != nil {
		t.Fatalf("second Close err = %v, want nil (must be idempotent)", err)
	}
}

// After Close returns, a fresh enqueue must not block and must return
// ErrTabClosed without running fn.
func TestTab_EnqueueAfterCloseReturnsErr(t *testing.T) {
	tab := newTab(testTargetID, nil)
	go tab.run()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = tab.Close(ctx)

	var ran atomic.Bool
	done := make(chan error, 1)
	go func() {
		_, err := tab.enqueue(context.Background(), func() (string, error) {
			ran.Store(true)
			return "", nil
		})
		done <- err
	}()

	select {
	case err := <-done:
		var tabClosed *ErrTabClosed
		if !errors.As(err, &tabClosed) {
			t.Fatalf("enqueue after Close = %v, want *ErrTabClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("enqueue after Close blocked; must return immediately")
	}
	if ran.Load() {
		t.Fatal("fn ran after Close()")
	}
}

// A goroutine parked on a full chJobs send must be released by Close
// with ErrTabClosed — not held indefinitely.
func TestTab_CloseReleasesBlockedEnqueuers(t *testing.T) {
	tab := newTab(testTargetID, nil)
	go tab.run()

	chRelease := make(chan struct{})
	chStarted := make(chan struct{})
	var wg sync.WaitGroup

	// Job 1: in-flight, blocks.
	wg.Go(func() {
		_, _ = tab.enqueue(context.Background(), func() (string, error) {
			close(chStarted)
			<-chRelease
			return "", nil
		})
	})
	<-chStarted

	// Job 2: fills the size-1 buffer.
	wg.Go(func() {
		_, _ = tab.enqueue(context.Background(), func() (string, error) { return "", nil })
	})
	waitForBuffer(t, tab, 1)

	// Job 3: parks on full-buffer send.
	chJob3Err := make(chan error, 1)
	wg.Go(func() {
		_, err := tab.enqueue(context.Background(), func() (string, error) { return "", nil })
		chJob3Err <- err
	})
	// Give job 3 time to reach the send.
	time.Sleep(10 * time.Millisecond)

	// Close in a goroutine; it will block until fn 1 finishes, but
	// must release job 3's send selection via t.ctx.Done().
	chCloseErr := make(chan error, 1)
	go func() {
		chCloseErr <- tab.Close(context.Background())
	}()

	select {
	case err := <-chJob3Err:
		var tabClosed *ErrTabClosed
		if !errors.As(err, &tabClosed) {
			t.Fatalf("job 3 err = %v, want *ErrTabClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("job 3 deadlocked on full chJobs send after Close")
	}

	// Release fn 1 so Close can return.
	close(chRelease)
	select {
	case err := <-chCloseErr:
		if err != nil {
			t.Fatalf("Close err = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not return after fn 1 completed")
	}
	wg.Wait()
}

// Close must block while fn is executing (no mid-fn interruption) and
// must return promptly once fn finishes — no deadlock either way.
func TestTab_CloseWhileWorkerInFn(t *testing.T) {
	tab := newTab(testTargetID, nil)
	go tab.run()

	chRelease := make(chan struct{})
	chStarted := make(chan struct{})
	var wg sync.WaitGroup

	wg.Go(func() {
		_, _ = tab.enqueue(context.Background(), func() (string, error) {
			close(chStarted)
			<-chRelease
			return "", nil
		})
	})
	<-chStarted

	chCloseErr := make(chan error, 1)
	go func() {
		chCloseErr <- tab.Close(context.Background())
	}()

	// Close must NOT return while fn is still running.
	select {
	case <-chCloseErr:
		t.Fatal("Close returned while fn was still executing")
	case <-time.After(30 * time.Millisecond):
	}

	close(chRelease)
	select {
	case err := <-chCloseErr:
		if err != nil {
			t.Fatalf("Close err = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not return after fn finished")
	}
	wg.Wait()
}

// Concurrent enqueue + Close must never panic and every enqueue must
// return either nil or ErrTabClosed (never a raw channel panic).
func TestTab_CloseRacesWithEnqueue(t *testing.T) {
	tab := newTab(testTargetID, nil)
	go tab.run()

	const N = 50
	errs := make([]error, N)
	var wg sync.WaitGroup

	for i := range N {
		wg.Go(func() {
			_, err := tab.enqueue(context.Background(), func() (string, error) {
				return "", nil
			})
			errs[i] = err
		})
	}

	if err := tab.Close(context.Background()); err != nil {
		t.Fatalf("Close err = %v, want nil", err)
	}
	wg.Wait()

	for i, err := range errs {
		var tabClosed *ErrTabClosed
		if err != nil && !errors.As(err, &tabClosed) {
			t.Errorf("enqueue %d: err = %v, want nil or *ErrTabClosed", i, err)
		}
	}
}

// A job that landed in chJobs before Close() must NOT run after Close
// cancels the tab ctx. Without the t.ctx.Err() check inside the
// worker's chJobs arm, Go's random select can pick chJobs even when
// t.ctx.Done is also ready, causing the queued fn to fire AFTER the
// caller's enqueue has already returned ErrTabClosed — a silent
// contract violation.
//
// The race is probabilistic (~50% per run). Stress the test under
// -count=50 or higher to catch any regression.
func TestTab_QueuedJobDoesNotRunAfterClose(t *testing.T) {
	tab := newTab(testTargetID, nil)
	go tab.run()

	chRelease := make(chan struct{})
	chStarted := make(chan struct{})
	var wg sync.WaitGroup

	// Job 1: holds the worker until chRelease closes. This guarantees
	// job 2 reaches the chJobs buffer while the worker is busy.
	wg.Go(func() {
		_, _ = tab.enqueue(context.Background(), func() (string, error) {
			close(chStarted)
			<-chRelease
			return "", nil
		})
	})
	<-chStarted

	// Job 2: the canary. It must NOT execute — the invariant the fix
	// protects.
	var job2Ran atomic.Bool
	wg.Go(func() {
		_, _ = tab.enqueue(context.Background(), func() (string, error) {
			job2Ran.Store(true)
			return "", nil
		})
	})
	waitForBuffer(t, tab, 1)

	// Fire Close. Worker is still blocked in fn 1, so Close waits on
	// chDone until fn 1 returns.
	chCloseErr := make(chan error, 1)
	go func() {
		chCloseErr <- tab.Close(context.Background())
	}()

	// Wait for Close's cancel() to have actually fired. Without this,
	// close(chRelease) below could race the Close goroutine and let
	// the worker see an uncancelled t.ctx when it loops — bypassing
	// the very path we're trying to test.
	<-tab.ctx.Done()

	// Release fn 1. Worker loops: t.ctx.Done and chJobs both ready.
	// With the fix, whichever arm is picked, fn 2 is skipped — the
	// chJobs arm checks t.ctx.Err() and exits.
	close(chRelease)

	if err := <-chCloseErr; err != nil {
		t.Fatalf("Close err = %v, want nil", err)
	}
	wg.Wait()

	if job2Ran.Load() {
		t.Fatal("buffered job ran after Close; worker must refuse queued jobs once tab ctx is cancelled")
	}
}

// Close must respect its own ctx and return promptly even when fn is
// hung. The worker goroutine is allowed to leak in that case — the
// coordinator's priority is not blocking on a wedged tab.
func TestTab_CloseCtxBounded(t *testing.T) {
	tab := newTab(testTargetID, nil)
	go tab.run()

	chHang := make(chan struct{}) // intentionally never closed
	chStarted := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		_, _ = tab.enqueue(context.Background(), func() (string, error) {
			close(chStarted)
			<-chHang
			return "", nil
		})
	})
	<-chStarted

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := tab.Close(ctx)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close err = %v, want DeadlineExceeded", err)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("Close returned late: %v", elapsed)
	}

	// Unhang the fn so the goroutine can exit; otherwise the test
	// process leaks it.
	close(chHang)
	wg.Wait()
}
