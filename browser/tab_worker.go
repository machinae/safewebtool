package browser

import (
	"context"
	"sync"

	"github.com/machinae/safewebtool/internal/rod"
)

// Tab is a live browser tab with its own worker goroutine. Every action on
// the tab (click, snapshot, etc.) is serialized through the job queue,
// preserving causal consistency for sequences like click → snapshot while
// still letting independent tabs run in parallel.
//
// Exactly one goroutine — the worker started by run() — touches page.
// The hijack handler may write policyErr from a Rod goroutine; that write
// is guarded by mu.
//
// Not wired into Browser yet; the coordinator in Step 2 constructs and
// owns Tab instances.
type Tab struct {
	// targetID is the full CDP target ID. Source of truth; ID() truncates
	// to the 7-char prefix used as the tab's public handle.
	targetID string
	// page is the Rod page handle used for all CDP operations on this tab.
	page *rod.Page

	// ctx is the tab's lifecycle context. Close() calls cancel() to
	// signal the worker to exit and to make every in-flight enqueue
	// return ErrTabClosed. Background-rooted so it is independent of any
	// request context. Stored on the struct deliberately; a Tab's
	// lifetime IS its cancellation, which is the idiomatic exception
	// to the "no contexts in structs" rule.
	ctx    context.Context
	cancel context.CancelFunc

	// chJobs is the incoming command queue. Buffered size 1 (matching
	// the original single-worker server); enqueue blocks when full and
	// exits via ctx.Done on timeout. Never closed — shutdown is signaled
	// exclusively through ctx so concurrent senders can never panic.
	chJobs chan job
	// chDone is closed after the worker goroutine exits. Close() waits
	// on it so callers know no fn is in flight once Close returns.
	chDone chan struct{}

	// mu guards policyErr. Held only briefly by the hijack handler and
	// the worker — never across a Rod/CDP call.
	mu sync.Mutex
	// policyErr is the most recent request blocked by the security
	// policy, stashed by the hijack handler as an *ErrBlocked. The
	// worker clears it before running a job and consumes it after, so a
	// policy block surfaces to the caller even when Rod returned a
	// generic navigation error.
	policyErr error

	// routerMu guards router lifecycle (install, Stop, reinstall).
	// Deliberately separate from mu — router operations run CDP calls
	// that can block on a slow Chrome, and the worker takes mu on every
	// job. Keeping them on different mutexes means a stuck ReloadPolicy
	// cannot stall this tab's worker.
	routerMu sync.Mutex
	// router is the Rod hijack router. Guarded by routerMu.
	router *rod.HijackRouter

	// readyOnce closes hijackReady exactly once, regardless of how many
	// times ensureHijacked is called.
	readyOnce sync.Once
	// hijackReady is closed when first-install completes (or decides no
	// policy applies). Callers of Tab()/OpenTab() wait on this before
	// returning the Tab, so no job can run before the policy is in
	// effect. Ctx-bounded on the wait side so a stuck installer doesn't
	// hang every caller forever.
	hijackReady chan struct{}
}

// job is a single unit of work enqueued on a tab's worker queue.
type job struct {
	// ctx carries the per-command timeout. The budget starts at enqueue
	// and covers both queue wait and execution (plan §9). This is the
	// caller's context, distinct from the tab's lifecycle context.
	ctx context.Context
	// fn is the command body, executed on the worker goroutine. The
	// returned text is the response payload; the returned error is the
	// raw Rod/browser error, wrapped through FriendlyError before reply.
	fn func() (string, error)
	// chResult is the per-request reply channel. Buffered size 1 so the
	// worker never blocks on send, even if the caller has already given
	// up waiting.
	chResult chan result
}

// result is the outcome of a job.
type result struct {
	// text is the rendered response payload, or "" on error.
	text string
	// err is the execution error, already passed through FriendlyError.
	err error
}

// newTab constructs a Tab ready to receive work. The caller must start
// the worker goroutine with `go t.run()` before any enqueue call.
func newTab(targetID string, page *rod.Page) *Tab {
	ctx, cancel := context.WithCancel(context.Background())
	return &Tab{
		targetID:    targetID,
		page:        page,
		ctx:         ctx,
		cancel:      cancel,
		chJobs:      make(chan job, 1),
		chDone:      make(chan struct{}),
		hijackReady: make(chan struct{}),
	}
}

// ID returns the lowercase 7-char prefix used as this tab's public handle.
func (t *Tab) ID() string {
	return prefixOf(t.targetID)
}

// Close signals the worker to stop and waits, bounded by ctx, for it
// to exit. Safe to call concurrently with enqueue: cancel() is
// idempotent, in-flight enqueue calls return ErrTabClosed, and chJobs
// is never closed so no concurrent sender can panic.
//
// Return contract — the coordinator MUST honour this in CloseTab:
//
//   - nil: the worker has exited. No fn will ever run on this Tab
//     again. The caller may now safely perform destructive teardown —
//     stop the hijack router, send Target.closeTarget via CDP, remove
//     the Tab from the known map.
//
//   - ctx error (DeadlineExceeded / Canceled): close was REQUESTED —
//     cancel() fired, new enqueues already return ErrTabClosed — but
//     the worker did not exit before the passed ctx did. An fn may
//     still be running against t.page. The caller MUST NOT proceed
//     with destructive teardown: tearing down the router or the
//     target while fn is live against the same page creates a
//     double-ownership race outside the reach of t.ctx. The safe
//     response is to quarantine the Tab — leave it in the known map
//     as dead, do no further cleanup, and accept that the worker
//     will finish on its own eventually.
func (t *Tab) Close(ctx context.Context) error {
	t.cancel()
	select {
	case <-t.chDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// run is the worker loop. Exactly one goroutine per Tab executes this.
//
// It pulls jobs from chJobs in FIFO order, skips jobs whose per-request
// ctx has already expired (plan §9 — stale jobs must never fire actions
// against a page that has since changed), and exits when the tab's
// lifecycle ctx is cancelled by Close().
//
// Concurrency note on the outer select: if Close() is called while a
// job is buffered in chJobs, BOTH the t.ctx.Done arm and the chJobs
// arm may be ready on the next iteration. Go's select picks uniformly
// at random — it does NOT prefer ctx.Done. Without the explicit
// t.ctx.Err() check below, the worker could run a buffered job whose
// caller has already received ErrTabClosed from its Wait Phase — a
// silent contract violation where the agent is told the tab closed
// but a side effect (click, navigation) still fired.
//
// Invariant: once this function returns, no more fn calls will ever
// happen on this Tab.
func (t *Tab) run() {
	// Closing chDone is the signal to Close() that the worker has
	// exited and no more fn calls will happen.
	defer close(t.chDone)
	for {
		select {
		case <-t.ctx.Done():
			// Tab is being torn down. Any jobs still buffered in
			// chJobs are abandoned; their callers' Wait Phase in
			// enqueue sees t.ctx.Done and returns ErrTabClosed.
			return
		case j := <-t.chJobs:
			// Stale-job check (caller ctx): the caller's timeout may
			// have expired while the job sat in the queue. Refuse fn
			// and continue — the tab itself is still healthy, and
			// subsequent jobs should run normally.
			if err := j.ctx.Err(); err != nil {
				j.chResult <- result{err: err}
				continue
			}

			// Close-safety check (tab ctx): Close() fired between
			// enqueue's Send Phase and our pull. Without this, the
			// random select above could pick this arm even when
			// t.ctx.Done is also ready, and fn would execute after
			// the caller was told ErrTabClosed. Reply to uphold the
			// per-job reply contract, then exit — no more fn runs.
			if t.ctx.Err() != nil {
				j.chResult <- result{err: &ErrTabClosed{}}
				return
			}

			// Clear any policy-block error carried over from a prior
			// job's hijack event. Without this, a block observed
			// BEFORE this job began could falsely attach to it.
			t.clearPolicyErr()

			text, err := j.fn()

			// If the hijack handler stashed a policy-block during
			// this fn's execution, that is the real error — Rod may
			// have surfaced a generic navigation failure that hides
			// the rule that blocked the request. Override.
			if blockedErr := t.takePolicyErr(); blockedErr != nil {
				err = blockedErr
			}

			// Reply. chResult is buffered size 1, so this send
			// always succeeds without blocking, even if the caller
			// has already given up in its Wait Phase.
			j.chResult <- result{text: text, err: newConnError(err)}
		}
	}
}

// enqueue submits fn to the worker and waits for its result.
//
// The function has two phases, each implemented as a select:
//
//  1. SEND PHASE — place the job on chJobs. Blocks if a prior job is
//     already buffered.
//  2. WAIT PHASE — wait for the worker to reply on chRes.
//
// Three exit paths apply to both phases:
//
//   - Happy path:  send lands / reply arrives.
//   - Caller ctx:  per-request timeout → ctx.Err().
//   - Tab ctx:     Close() was called on the tab → ErrTabClosed.
//
// If both ctxs are Done simultaneously (e.g. caller timed out at the
// same instant Close was called), we prefer the caller's error. "Your
// action timed out" is more actionable for the agent than "tab
// closed," which would incorrectly suggest the tab vanished.
//
// Race subtleties worth stating explicitly:
//
//   - We NEVER close chJobs. Close() cancels t.ctx instead. That means
//     a concurrent Close() cannot panic the Send Phase — at worst the
//     select picks the t.ctx.Done arm and enqueue returns ErrTabClosed.
//
//   - If Send Phase and Close race and Send wins (buffer had space),
//     the job lands in chJobs. The worker may or may not run it,
//     depending on how its own outer select resolved. Either way our
//     Wait Phase's select will see t.ctx.Done and return ErrTabClosed.
//     An orphaned job sits in the buffered channel and is reclaimed
//     when the Tab itself is GC'd.
//
//   - If Wait Phase's ctx (or the tab ctx) fires, we first drain chRes
//     non-blockingly and return any result already delivered there. The
//     worker always replies once fn returns and chRes is buffered size
//     1, so a completed result — frequently a success whose side effect
//     already applied — must win over the cancellation. Reporting a
//     timeout while that result sits unread was the enqueue race.
//     Only when no result is available do we return ctx.Err(): the
//     worker is still mid-fn and its eventual reply is dropped.
func (t *Tab) enqueue(ctx context.Context, fn func() (string, error)) (string, error) {
	// Per-request reply channel. Buffered size 1 so the worker can
	// always send without blocking, even if we have already given up.
	chRes := make(chan result, 1)

	// SEND PHASE — try to put the job on the worker's queue.
	select {
	case t.chJobs <- job{ctx: ctx, fn: fn, chResult: chRes}:
		// Enqueued; fall through to Wait Phase.
	case <-ctx.Done():
		// Caller's timeout expired while we were waiting for queue
		// capacity. Job was never enqueued; fn will not run.
		return "", ctx.Err()
	case <-t.ctx.Done():
		// Tab was closed while we were waiting for queue capacity.
		// Job was never enqueued. Prefer caller's error if their ctx
		// also fired — see function-level note on both-ctxs-Done.
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return "", &ErrTabClosed{}
	}

	// WAIT PHASE — job is now either buffered or being pulled by the
	// worker. We can't cancel it from here; our options are to wait
	// for the reply, give up via our own ctx, or notice Close().
	select {
	case res := <-chRes:
		return res.text, res.err
	case <-ctx.Done():
	case <-t.ctx.Done():
	}

	// A cancellation fired. The worker may have already delivered a
	// result: chRes is buffered (size 1) and the worker always replies
	// once fn returns. Prefer a real outcome over the cancellation —
	// returning a timeout while a completed result (often a success
	// whose side effect already applied) sits unread in chRes is the
	// enqueue race. The drain is non-blocking: we take a result only if
	// one is already there, never wait for one.
	select {
	case res := <-chRes:
		return res.text, res.err
	default:
	}

	// No result is available. Report the cancellation, preferring the
	// caller's timeout over tab-closed: "your action timed out" is more
	// actionable for the agent than "tab closed," which would wrongly
	// suggest the tab vanished.
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", &ErrTabClosed{}
}

// clearPolicyErr drops any buffered policy-block error. Called before
// each job so the worker only surfaces blocks produced by that job.
func (t *Tab) clearPolicyErr() {
	t.mu.Lock()
	t.policyErr = nil
	t.mu.Unlock()
}

// setPolicyErr stashes a policy-block error for the worker to surface
// after the current job finishes. Called by the hijack handler from a
// Rod-owned goroutine. If a second block arrives before the first is
// consumed (e.g., multiple blocked subresources during one navigation),
// the most recent wins — any block is informative, and "last" is the
// natural semantic for a single-slot field.
func (t *Tab) setPolicyErr(err error) {
	t.mu.Lock()
	t.policyErr = err
	t.mu.Unlock()
}

// takePolicyErr removes and returns the policy-block error, or nil if
// none. Called after each job; a non-nil return overrides whatever Rod
// returned.
func (t *Tab) takePolicyErr() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	err := t.policyErr
	t.policyErr = nil
	return err
}
