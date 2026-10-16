//go:generate go run ./lib/utils/setup
//go:generate go run ./lib/proto/generate
//go:generate go run ./lib/js/generate
//go:generate go run ./lib/assets/generate
//go:generate go run ./lib/utils/lint

// Package rod is a high-level driver directly based on DevTools Protocol.
package rod

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/machinae/safewebtool/internal/rod/lib/cdp"
	"github.com/machinae/safewebtool/internal/rod/lib/defaults"
	"github.com/machinae/safewebtool/internal/rod/lib/devices"
	"github.com/machinae/safewebtool/internal/rod/lib/proto"
	"github.com/machinae/safewebtool/internal/rod/lib/utils"
	"github.com/ysmood/goob"
)

// Browser implements these interfaces.
var (
	_ proto.Client      = &Browser{}
	_ proto.Contextable = &Browser{}
)

// Browser represents the browser.
// It doesn't depends on file system, it should work with remote browser seamlessly.
// To check the env var you can use to quickly enable options from CLI, check here:
// https://pkg.go.dev/github.com/go-rod/rod/lib/defaults
type Browser struct {
	// BrowserContextID is the id for incognito window
	BrowserContextID proto.BrowserBrowserContextID

	e eFunc

	ctx context.Context

	// The browser's lifetime ctx, captured at Connect. Cached pages derive
	// their session ctx from it, NOT from the receiver's ctx, so bounding a
	// call like b.Context(ctx).PageFromTarget(id) can't kill the cached page
	// when ctx expires.
	rootCtx context.Context

	sleeper func() utils.Sleeper

	logger utils.Logger

	slowMotion time.Duration // see defaults.slow
	trace      bool          // see defaults.Trace
	cursor     bool          // see Browser.ShowCursor
	monitor    string

	defaultDevice devices.Device

	controlURL  string
	client      CDPClient
	event       *goob.Observable // all the browser events from cdp client
	targetsLock *sync.Mutex

	// stores all the previous cdp call of same type. Browser doesn't have enough API
	// for us to retrieve all its internal states. This is an workaround to map them to local.
	// For example you can't use cdp API to get the current position of mouse.
	states *sync.Map
}

// New creates a controller.
// DefaultDevice to emulate is set to [devices.LaptopWithMDPIScreen].Landscape(), it will change the default
// user-agent and can make the actual view area smaller than the browser window on headful mode,
// you can use [Browser.NoDefaultDevice] to disable it.
func New() *Browser {
	return (&Browser{
		ctx:           context.Background(),
		sleeper:       DefaultSleeper,
		controlURL:    defaults.URL,
		slowMotion:    defaults.Slow,
		trace:         defaults.Trace,
		monitor:       defaults.Monitor,
		logger:        DefaultLogger,
		defaultDevice: devices.LaptopWithMDPIScreen.Landscape(),
		targetsLock:   &sync.Mutex{},
		states:        &sync.Map{},
	}).WithPanic(utils.Panic)
}

// Incognito creates a new incognito browser.
func (b *Browser) Incognito() (*Browser, error) {
	res, err := proto.TargetCreateBrowserContext{}.Call(b)
	if err != nil {
		return nil, err
	}

	incognito := *b
	incognito.BrowserContextID = res.BrowserContextID

	return &incognito, nil
}

// ControlURL set the url to remote control browser.
func (b *Browser) ControlURL(url string) *Browser {
	b.controlURL = url
	return b
}

// SlowMotion set the delay for each control action, such as the simulation of the human inputs.
func (b *Browser) SlowMotion(delay time.Duration) *Browser {
	b.slowMotion = delay
	return b
}

// Trace enables/disables the visual tracing of the input actions on the page.
func (b *Browser) Trace(enable bool) *Browser {
	b.trace = enable
	return b
}

// ShowCursor draws a visible mouse cursor that follows input actions, without
// enabling the rest of trace's debug overlays and logging. Intended for demo
// recordings where only the pointer should be visible.
func (b *Browser) ShowCursor(enable bool) *Browser {
	b.cursor = enable
	return b
}

// Monitor address to listen if not empty. Shortcut for [Browser.ServeMonitor].
func (b *Browser) Monitor(url string) *Browser {
	b.monitor = url
	return b
}

// Logger overrides the default log functions for tracing.
func (b *Browser) Logger(l utils.Logger) *Browser {
	b.logger = l
	return b
}

// Client set the cdp client.
func (b *Browser) Client(c CDPClient) *Browser {
	b.client = c
	return b
}

// DefaultDevice sets the default device for new page to emulate in the future.
// Default is [devices.LaptopWithMDPIScreen].
// Set it to [devices.Clear] to disable it.
func (b *Browser) DefaultDevice(d devices.Device) *Browser {
	b.defaultDevice = d
	return b
}

// NoDefaultDevice is the same as [Browser.DefaultDevice](devices.Clear).
func (b *Browser) NoDefaultDevice() *Browser {
	return b.DefaultDevice(devices.Clear)
}

// Connect to the browser and start to control it.
// A client or control URL must be configured first with [Browser.Client] or
// [Browser.ControlURL]; unlike upstream rod, this fork never launches or
// downloads a browser (that path linked leakless's embedded helper
// executable, a known antivirus trigger, into every consumer binary).
func (b *Browser) Connect() error {
	b.rootCtx = b.ctx

	if b.client == nil {
		if b.controlURL == "" {
			return ErrNoControlURL
		}

		c, err := cdp.StartWithURL(b.ctx, b.controlURL, nil)
		if err != nil {
			return err
		}
		b.client = c
	} else if b.controlURL != "" {
		panic("Browser.Client and Browser.ControlURL can't be set at the same time")
	}

	b.initEvents()

	if b.monitor != "" {
		// Served but not auto-opened in a browser; opening used lib/launcher,
		// which this fork keeps out of the link graph (see Connect's doc).
		b.logger.Println("rod monitor served on", b.ServeMonitor(b.monitor))
	}

	return proto.TargetSetDiscoverTargets{Discover: true}.Call(b)
}

// Close the browser.
func (b *Browser) Close() error {
	if b.BrowserContextID == "" {
		return proto.BrowserClose{}.Call(b)
	}
	return proto.TargetDisposeBrowserContext{BrowserContextID: b.BrowserContextID}.Call(b)
}

// Page creates a new browser tab. If opts.URL is empty, the default target will be "about:blank".
func (b *Browser) Page(opts proto.TargetCreateTarget) (p *Page, err error) {
	req := opts
	req.BrowserContextID = b.BrowserContextID
	req.URL = "about:blank"

	target, err := req.Call(b)
	if err != nil {
		return nil, err
	}
	defer func() {
		// If Navigate or PageFromTarget fails we should close the target to
		// prevent a leak. The failure often means the receiver's ctx is
		// already dead, so close on an independent short ctx (runZero
		// 22837ae); a failed close is appended to the primary error.
		if err != nil {
			ctx, done := context.WithTimeout(b.rootContext(), 5*time.Second)
			defer done()
			if _, cerr := (proto.TargetCloseTarget{TargetID: target.TargetID}).Call(b.Context(ctx)); cerr != nil {
				err = fmt.Errorf("%w (rollback close of target %s failed: %v)", err, target.TargetID, cerr)
			}
		}
	}()

	p, err = b.PageFromTarget(target.TargetID)
	if err != nil {
		return
	}

	if opts.URL == "" {
		return
	}

	err = p.Navigate(opts.URL)

	return
}

// Pages retrieves all visible pages.
func (b *Browser) Pages() (Pages, error) {
	list, err := proto.TargetGetTargets{}.Call(b)
	if err != nil {
		return nil, err
	}

	pageList := Pages{}
	for _, target := range list.TargetInfos {
		if target.Type != proto.TargetTargetInfoTypePage {
			continue
		}

		page, err := b.PageFromTarget(target.TargetID)
		if err != nil {
			return nil, err
		}
		pageList = append(pageList, page)
	}

	return pageList, nil
}

// Call implements the [proto.Client] to call raw cdp interface directly.
func (b *Browser) Call(ctx context.Context, sessionID, methodName string, params interface{}) (res []byte, err error) {
	res, err = b.client.Call(ctx, sessionID, methodName, params)
	if err != nil {
		return nil, err
	}

	b.set(proto.TargetSessionID(sessionID), methodName, params)
	return
}

// PageFromSession is used for low-level debugging.
func (b *Browser) PageFromSession(sessionID proto.TargetSessionID) *Page {
	sessionCtx, cancel := context.WithCancel(b.ctx)
	return &Page{
		e:             b.e,
		ctx:           sessionCtx,
		sessionCancel: cancel,
		sleeper:       b.sleeper,
		browser:       b,
		SessionID:     sessionID,
	}
}

// rootContext returns the browser's lifetime ctx, captured at Connect,
// falling back to the receiver's ctx when Connect wasn't used.
func (b *Browser) rootContext() context.Context {
	if b.rootCtx != nil {
		return b.rootCtx
	}
	return b.ctx
}

// PageFromTarget gets or creates a Page instance.
// The setup calls run on the receiver's ctx while the cached page's lifetime
// derives from the browser's root ctx, so bounding the call with
// b.Context(ctx).PageFromTarget(id) is safe: a timeout fails this call
// without killing the returned page for later callers.
func (b *Browser) PageFromTarget(targetID proto.TargetTargetID) (*Page, error) {
	b.targetsLock.Lock()
	page := b.loadCachedPage(targetID)
	b.targetsLock.Unlock()
	if page != nil {
		return page, nil
	}

	// Attach and set up the page WITHOUT holding the browser-wide lock. A
	// tab that accepts the attach but never answers the setup calls (e.g.
	// discarded by Chrome's Memory Saver, it has no renderer process) must
	// only block this call, not every future attach on the connection.
	session, err := proto.TargetAttachToTarget{
		TargetID: targetID,
		Flatten:  true, // if it's not set no response will return
	}.Call(b)
	if err != nil {
		return nil, err
	}

	sessionCtx, cancel := context.WithCancel(b.rootContext())

	// The receiver's ctx may already be dead when cleanup runs (a timeout
	// mid-setup is exactly when cleanup is needed), so detach on its own
	// short ctx derived from the root ctx.
	detach := func() error {
		cancel()
		ctx, done := context.WithTimeout(b.rootContext(), 5*time.Second)
		defer done()
		return proto.TargetDetachFromTarget{SessionID: session.SessionID}.Call(b.Context(ctx))
	}

	page = &Page{
		e:             b.e,
		ctx:           sessionCtx,
		sessionCancel: cancel,
		sleeper:       b.sleeper,
		browser:       b,
		TargetID:      targetID,
		SessionID:     session.SessionID,
		FrameID:       proto.PageFrameID(targetID),
		jsCtxLock:     &sync.Mutex{},
		jsCtxID:       new(proto.RuntimeRemoteObjectID),
		helpersLock:   &sync.Mutex{},
		helpers:       map[proto.RuntimeRemoteObjectID]map[string]proto.RuntimeRemoteObjectID{},
	}

	page.root = page
	page.newKeyboard().newMouse().newTouch()

	if !b.defaultDevice.IsClear() {
		err = page.Context(b.ctx).Emulate(b.defaultDevice)
		if err != nil {
			if derr := detach(); derr != nil {
				err = fmt.Errorf("%w (detach of session failed: %v)", err, derr)
			}
			return nil, err
		}
	}

	// Start the event pump before the page is visible in the cache, so a
	// concurrent cache hit never sees a page without one. If we lose the
	// insert race below, detach's cancel stops the pump again.
	page.initEvents()

	// If we don't enable it, it will cause a lot of unexpected browser behavior.
	// Such as proto.PageAddScriptToEvaluateOnNewDocument won't work. A page
	// whose Page domain failed to enable is broken for its whole life, so
	// fail the attach instead of caching it.
	if _, err := page.Context(b.ctx).EnableDomain(&proto.PageEnable{}); err != nil {
		if derr := detach(); derr != nil {
			err = fmt.Errorf("%w (detach of session failed: %v)", err, derr)
		}
		return nil, err
	}

	b.targetsLock.Lock()
	if cached := b.loadCachedPage(targetID); cached != nil {
		// Another caller attached this target while we were off the lock.
		// Keep theirs, drop our duplicate session. A failed detach can't
		// fail the call (the returned page works); the duplicate session
		// dies with the tab at the latest.
		b.targetsLock.Unlock()
		_ = detach()
		return cached, nil
	}
	b.cachePage(page)
	b.targetsLock.Unlock()

	return page, nil
}

// EachEvent is similar to [Page.EachEvent], but catches events of the entire browser.
func (b *Browser) EachEvent(callbacks ...interface{}) (wait func() error) {
	return b.eachEvent("", callbacks...)
}

// WaitEvent waits for the next event for one time. It will also load the data into the event object.
func (b *Browser) WaitEvent(e proto.Event) (wait func() error) {
	return b.waitEvent("", e)
}

// waits for the next event for one time. It will also load the data into the event object.
func (b *Browser) waitEvent(sessionID proto.TargetSessionID, e proto.Event) (wait func() error) {
	valE := reflect.ValueOf(e)
	valTrue := reflect.ValueOf(true)

	if valE.Kind() != reflect.Ptr {
		valE = reflect.New(valE.Type())
	}

	// dynamically creates a function on runtime:
	//
	// func(ee proto.Event) bool {
	//   *e = *ee
	//   return true
	// }
	fnType := reflect.FuncOf([]reflect.Type{valE.Type()}, []reflect.Type{valTrue.Type()}, false)
	fnVal := reflect.MakeFunc(fnType, func(args []reflect.Value) []reflect.Value {
		valE.Elem().Set(args[0].Elem())
		return []reflect.Value{valTrue}
	})

	return b.eachEvent(sessionID, fnVal.Interface())
}

// If the any callback returns true the event loop will stop.
// It will enable the related domains if not enabled, and restore them after wait ends.
// The returned wait reports why it ended: nil when a callback returned true,
// the enable error when a required domain couldn't be enabled (the event
// could never arrive), the ctx error when the ctx died first, or
// [ErrEventStreamClosed] when the browser connection ended. Domain restores
// are best-effort cleanup.
func (b *Browser) eachEvent(sessionID proto.TargetSessionID, callbacks ...interface{}) (wait func() error) {
	cbMap := map[string]reflect.Value{}
	restores := []func() error{}

	parent := b
	b, cancel := b.WithCancel()
	// Subscribe before enabling the domains, so events emitted while the
	// enable is in flight can't fall in a gap (a paused Fetch request whose
	// event is dropped stays paused forever).
	messages := b.Event()

	var enableErr error
	for _, cb := range callbacks {
		cbVal := reflect.ValueOf(cb)
		eType := cbVal.Type().In(0)
		name := reflect.New(eType.Elem()).Interface().(proto.Event).ProtoEvent() //nolint: forcetypeassert
		cbMap[name] = cbVal

		// Only enabled domains will emit events to cdp client.
		// We enable the domains for the event types if it's not enabled.
		// We restore the domains to their previous states after the wait ends.
		domain, _ := proto.ParseMethodName(name)
		if req := proto.GetType(domain + ".enable"); req != nil {
			enable := reflect.New(req).Interface().(proto.Request) //nolint: forcetypeassert
			restore, err := parent.EnableDomain(sessionID, enable)
			if err != nil && enableErr == nil {
				enableErr = err
			}
			restores = append(restores, restore)
		}
	}

	return func() (err error) {
		if messages == nil {
			panic("can't use wait function twice")
		}

		defer func() {
			cancel()
			messages = nil
			for _, restore := range restores {
				// A failed restore leaves the domain enabled; report it
				// unless the wait already failed for a more primary reason.
				if rerr := restore(); rerr != nil && err == nil {
					err = rerr
				}
			}
		}()

		if enableErr != nil {
			return enableErr
		}

		for msg := range messages {
			if !(sessionID == "" || msg.SessionID == sessionID) {
				continue
			}

			if cbVal, has := cbMap[msg.Method]; has {
				e := reflect.New(proto.GetType(msg.Method))
				msg.Load(e.Interface().(proto.Event)) //nolint: forcetypeassert
				args := []reflect.Value{e}
				if cbVal.Type().NumIn() == 2 {
					args = append(args, reflect.ValueOf(msg.SessionID))
				}
				res := cbVal.Call(args)
				if len(res) > 0 {
					if res[0].Bool() {
						return nil
					}
				}
			}
		}

		// The message channel closed without a callback match: the ctx died
		// or the browser connection ended. Report it, a timed-out wait must
		// be distinguishable from the event arriving.
		if cerr := parent.ctx.Err(); cerr != nil {
			return cerr
		}
		return ErrEventStreamClosed
	}
}

// Event of the browser.
func (b *Browser) Event() <-chan *Message {
	src := b.event.Subscribe(b.ctx)
	dst := make(chan *Message)
	go func() {
		defer close(dst)
		for {
			select {
			case <-b.ctx.Done():
				return
			case e, ok := <-src:
				if !ok {
					return
				}
				select {
				case <-b.ctx.Done():
					return
				case dst <- e.(*Message): //nolint: forcetypeassert
				}
			}
		}
	}()
	return dst
}

func (b *Browser) initEvents() {
	ctx, cancel := context.WithCancel(b.ctx)
	b.event = goob.New(ctx)
	event := b.client.Event()

	go func() {
		defer cancel()
		for e := range event {
			b.event.Publish(&Message{
				SessionID: proto.TargetSessionID(e.SessionID),
				Method:    e.Method,
				lock:      &sync.Mutex{},
				data:      e.Params,
			})
		}
	}()
}

func (b *Browser) pageInfo(id proto.TargetTargetID) (*proto.TargetTargetInfo, error) {
	res, err := proto.TargetGetTargetInfo{TargetID: id}.Call(b)
	if err != nil {
		return nil, err
	}
	return res.TargetInfo, nil
}

func (b *Browser) isHeadless() (enabled bool) {
	res, _ := proto.BrowserGetBrowserCommandLine{}.Call(b)
	for _, v := range res.Arguments {
		if strings.Contains(v, "headless") {
			return true
		}
	}
	return false
}

// IgnoreCertErrors switch. If enabled, all certificate errors will be ignored.
func (b *Browser) IgnoreCertErrors(enable bool) error {
	return proto.SecuritySetIgnoreCertificateErrors{Ignore: enable}.Call(b)
}

// GetCookies from the browser.
func (b *Browser) GetCookies() ([]*proto.NetworkCookie, error) {
	res, err := proto.StorageGetCookies{BrowserContextID: b.BrowserContextID}.Call(b)
	if err != nil {
		return nil, err
	}
	return res.Cookies, nil
}

// SetCookies to the browser. If the cookies is nil it will clear all the cookies.
func (b *Browser) SetCookies(cookies []*proto.NetworkCookieParam) error {
	if cookies == nil {
		return proto.StorageClearCookies{BrowserContextID: b.BrowserContextID}.Call(b)
	}

	return proto.StorageSetCookies{
		Cookies:          cookies,
		BrowserContextID: b.BrowserContextID,
	}.Call(b)
}

// WaitDownload returns a helper to get the next download file.
// The file path will be:
//
//	filepath.Join(dir, info.GUID)
func (b *Browser) WaitDownload(dir string) func() (info *proto.PageDownloadWillBegin, err error) {
	var oldDownloadBehavior proto.BrowserSetDownloadBehavior
	has := b.LoadState("", &oldDownloadBehavior)

	if err := (proto.BrowserSetDownloadBehavior{
		Behavior:         proto.BrowserSetDownloadBehaviorBehaviorAllowAndName,
		BrowserContextID: b.BrowserContextID,
		DownloadPath:     dir,
	}).Call(b); err != nil {
		// Downloads can never land in dir: fail fast instead of returning a
		// wait that can't fire.
		return func() (*proto.PageDownloadWillBegin, error) { return nil, err }
	}

	var start *proto.PageDownloadWillBegin

	waitProgress := b.EachEvent(func(e *proto.PageDownloadWillBegin) {
		start = e
	}, func(e *proto.PageDownloadProgress) bool {
		return start != nil && start.GUID == e.GUID && e.State == proto.PageDownloadProgressStateCompleted
	})

	return func() (info *proto.PageDownloadWillBegin, err error) {
		defer func() {
			// Restore the previous download behavior; a failure means
			// downloads keep landing in dir, so report it unless the wait
			// already failed for a more primary reason.
			var rerr error
			if has {
				rerr = oldDownloadBehavior.Call(b)
			} else {
				rerr = proto.BrowserSetDownloadBehavior{
					Behavior:         proto.BrowserSetDownloadBehaviorBehaviorDefault,
					BrowserContextID: b.BrowserContextID,
				}.Call(b)
			}
			if rerr != nil && err == nil {
				err = rerr
			}
		}()

		if err := waitProgress(); err != nil {
			return nil, err
		}

		return start, nil
	}
}

// Version info of the browser.
func (b *Browser) Version() (*proto.BrowserGetVersionResult, error) {
	return proto.BrowserGetVersion{}.Call(b)
}
