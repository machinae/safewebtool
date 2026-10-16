package rod

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/machinae/safewebtool/internal/rod/lib/input"
	"github.com/machinae/safewebtool/internal/rod/lib/proto"
	"github.com/machinae/safewebtool/internal/rod/lib/utils"
	"github.com/ysmood/gson"
)

// Keyboard represents the keyboard on a page, it's always related the main frame.
// It's a thin wrapper that carries the page (and so the page's ctx); the key
// state is shared between all clones of the same page, see [Page.Context].
type Keyboard struct {
	page  *Page
	state *keyboardState
}

// keyboardState is shared between all Keyboard clones of the same page.
// The mutex only guards the state; it's never held across CDP calls, so a
// stuck call can't block other input on the page.
type keyboardState struct {
	sync.Mutex

	// pressed keys must be released before it can be pressed again
	pressed map[input.Key]struct{}
}

func (p *Page) newKeyboard() *Page {
	p.Keyboard = &Keyboard{page: p, state: &keyboardState{pressed: map[input.Key]struct{}{}}}
	return p
}

func (k *Keyboard) getModifiers() int {
	k.state.Lock()
	defer k.state.Unlock()
	return k.state.modifiers()
}

func (s *keyboardState) modifiers() int {
	ms := 0
	for key := range s.pressed {
		ms |= key.Modifier()
	}
	return ms
}

func (s *keyboardState) modifiersWithout(key input.Key) int {
	ms := 0
	for pressed := range s.pressed {
		if pressed != key {
			ms |= pressed.Modifier()
		}
	}
	return ms
}

// Press the key down.
// To input characters that are not on the keyboard, such as Chinese or Japanese, you should
// use method like [Page.InsertText].
func (k *Keyboard) Press(key input.Key) error {
	defer k.page.tryTrace(TraceTypeInput, "press key: "+key.Info().Code)()
	k.page.browser.trySlowMotion()

	k.state.Lock()
	modifiers := k.state.modifiers() | key.Modifier()
	k.state.Unlock()

	err := key.Encode(proto.InputDispatchKeyEventTypeKeyDown, modifiers).Call(k.page)
	if err != nil {
		return err
	}

	k.state.Lock()
	k.state.pressed[key] = struct{}{}
	k.state.Unlock()
	return nil
}

// Release the key.
func (k *Keyboard) Release(key input.Key) error {
	defer k.page.tryTrace(TraceTypeInput, "release key: "+key.Info().Code)()

	// Send the keyUp even if the key isn't tracked as pressed: a failed
	// keyUp must be retryable, or the key stays held in the browser forever.
	k.state.Lock()
	modifiers := k.state.modifiersWithout(key)
	k.state.Unlock()

	err := key.Encode(proto.InputDispatchKeyEventTypeKeyUp, modifiers).Call(k.page)
	if err != nil {
		return err
	}

	k.state.Lock()
	delete(k.state.pressed, key)
	k.state.Unlock()
	return nil
}

// Type releases the key after the press.
func (k *Keyboard) Type(keys ...input.Key) (err error) {
	for _, key := range keys {
		err = k.Press(key)
		if err != nil {
			return
		}
		err = k.Release(key)
		if err != nil {
			// The key is down in the browser and the caller's ctx is likely
			// dead; retry the release on a short detached ctx so the key
			// isn't left held forever.
			ctx, cancel := context.WithTimeout(k.page.browser.rootContext(), 5*time.Second)
			if rerr := k.page.Context(ctx).Keyboard.Release(key); rerr != nil {
				err = fmt.Errorf("%w (compensating key release failed: %v)", err, rerr)
			}
			cancel()
			return
		}
	}
	return
}

// KeyActionType enum.
type KeyActionType int

// KeyActionTypes.
const (
	KeyActionPress KeyActionType = iota
	KeyActionRelease
	KeyActionTypeKey
)

// KeyAction to perform.
type KeyAction struct {
	Type KeyActionType
	Key  input.Key
}

// KeyActions to simulate.
type KeyActions struct {
	keyboard *Keyboard

	Actions []KeyAction
}

// KeyActions simulates the type actions on a physical keyboard.
// Useful when input shortcuts like ctrl+enter .
func (p *Page) KeyActions() *KeyActions {
	return &KeyActions{keyboard: p.Keyboard}
}

// Press keys is guaranteed to have a release at the end of actions.
func (ka *KeyActions) Press(keys ...input.Key) *KeyActions {
	for _, key := range keys {
		ka.Actions = append(ka.Actions, KeyAction{KeyActionPress, key})
	}
	return ka
}

// Release keys.
func (ka *KeyActions) Release(keys ...input.Key) *KeyActions {
	for _, key := range keys {
		ka.Actions = append(ka.Actions, KeyAction{KeyActionRelease, key})
	}
	return ka
}

// Type will release the key immediately after the pressing.
func (ka *KeyActions) Type(keys ...input.Key) *KeyActions {
	for _, key := range keys {
		ka.Actions = append(ka.Actions, KeyAction{KeyActionTypeKey, key})
	}
	return ka
}

// Do the actions.
func (ka *KeyActions) Do() (err error) {
	held := map[input.Key]struct{}{}
	for _, a := range ka.balance() {
		switch a.Type {
		case KeyActionPress:
			err = ka.keyboard.Press(a.Key)
			if err == nil {
				held[a.Key] = struct{}{}
			}
		case KeyActionRelease:
			err = ka.keyboard.Release(a.Key)
			if err == nil {
				delete(held, a.Key)
			}
		case KeyActionTypeKey:
			err = ka.keyboard.Type(a.Key)
		}
		if err != nil {
			// The caller's ctx is likely dead; release the keys this
			// sequence still holds on a short detached ctx so they aren't
			// left held in the browser forever (same as Keyboard.Type).
			ctx, cancel := context.WithTimeout(ka.keyboard.page.browser.rootContext(), 5*time.Second)
			kb := ka.keyboard.page.Context(ctx).Keyboard
			for key := range held {
				if rerr := kb.Release(key); rerr != nil {
					err = fmt.Errorf("%w (compensating key release failed: %v)", err, rerr)
				}
			}
			cancel()
			return
		}
	}
	return
}

// Make sure there's at least one release after the presses, such as:
//
//	p1,p2,p1,r1 => p1,p2,p1,r1,r2
func (ka *KeyActions) balance() []KeyAction {
	actions := ka.Actions

	h := map[input.Key]bool{}
	for _, a := range actions {
		switch a.Type {
		case KeyActionPress:
			h[a.Key] = true
		case KeyActionRelease, KeyActionTypeKey:
			h[a.Key] = false
		}
	}

	for key, needRelease := range h {
		if needRelease {
			actions = append(actions, KeyAction{KeyActionRelease, key})
		}
	}

	return actions
}

// InsertText is like pasting text into the page.
func (p *Page) InsertText(text string) error {
	defer p.tryTrace(TraceTypeInput, "insert text "+text)()
	p.browser.trySlowMotion()

	err := proto.InputInsertText{Text: text}.Call(p)
	return err
}

// Mouse represents the mouse on a page, it's always related the main frame.
// It's a thin wrapper that carries the page (and so the page's ctx); the
// cursor state is shared between all clones of the same page, see [Page.Context].
type Mouse struct {
	page  *Page
	state *mouseState
}

// mouseState is shared between all Mouse clones of the same page.
// The mutex only guards the state; it's never held across CDP calls, so a
// stuck call can't block other input on the page.
type mouseState struct {
	sync.Mutex

	id string // mouse svg dom element id

	pos proto.Point

	// the buttons is currently being pressed, reflects the press order
	buttons []proto.InputMouseButton
}

func (p *Page) newMouse() *Page {
	p.Mouse = &Mouse{page: p, state: &mouseState{id: utils.RandString(8)}}
	return p
}

// Position of current cursor.
func (m *Mouse) Position() proto.Point {
	m.state.Lock()
	defer m.state.Unlock()
	return m.state.pos
}

// MoveTo the absolute position.
func (m *Mouse) MoveTo(p proto.Point) error {
	m.state.Lock()
	button, buttons := input.EncodeMouseButton(m.state.buttons)
	m.state.Unlock()

	m.page.browser.trySlowMotion()

	err := proto.InputDispatchMouseEvent{
		Type:      proto.InputDispatchMouseEventTypeMouseMoved,
		X:         p.X,
		Y:         p.Y,
		Button:    button,
		Buttons:   gson.Int(buttons),
		Modifiers: m.page.Keyboard.getModifiers(),
	}.Call(m.page)
	if err != nil {
		return err
	}

	// to make sure set only when call is successful
	m.state.Lock()
	m.state.pos = p
	m.state.Unlock()

	if m.page.browser.trace || m.page.browser.cursor {
		if !m.updateMouseTracer() {
			m.initMouseTracer()
			m.updateMouseTracer()
		}
	}

	return nil
}

// MoveAlong the guide function.
// Every time the guide function is called it should return the next mouse position, return true to stop.
// Read the source code of [Mouse.MoveLinear] as an example to use this method.
func (m *Mouse) MoveAlong(guide func() (proto.Point, bool)) error {
	for {
		p, stop := guide()
		if stop {
			return m.MoveTo(p)
		}

		err := m.MoveTo(p)
		if err != nil {
			return err
		}
	}
}

// MoveLinear to the absolute position with the given steps.
// Such as move from (0,0) to (6,6) with 3 steps, the mouse will first move to (2,2) then (4,4) then (6,6).
func (m *Mouse) MoveLinear(to proto.Point, steps int) error {
	p := m.Position()
	step := to.Minus(p).Scale(1 / float64(steps))
	count := 0

	return m.MoveAlong(func() (proto.Point, bool) {
		count++
		if count == steps {
			return to, true
		}

		p = p.Add(step)
		return p, false
	})
}

// Scroll the relative offset with specified steps.
func (m *Mouse) Scroll(offsetX, offsetY float64, steps int) error {
	defer m.page.tryTrace(TraceTypeInput, fmt.Sprintf("scroll (%.2f, %.2f)", offsetX, offsetY))()
	m.page.browser.trySlowMotion()

	if steps < 1 {
		steps = 1
	}

	m.state.Lock()
	button, buttons := input.EncodeMouseButton(m.state.buttons)
	pos := m.state.pos
	m.state.Unlock()

	stepX := offsetX / float64(steps)
	stepY := offsetY / float64(steps)

	for i := 0; i < steps; i++ {
		err := proto.InputDispatchMouseEvent{
			Type:      proto.InputDispatchMouseEventTypeMouseWheel,
			Button:    button,
			Buttons:   gson.Int(buttons),
			Modifiers: m.page.Keyboard.getModifiers(),
			DeltaX:    stepX,
			DeltaY:    stepY,
			X:         pos.X,
			Y:         pos.Y,
		}.Call(m.page)
		if err != nil {
			return err
		}
	}

	return nil
}

// Down holds the button down.
func (m *Mouse) Down(button proto.InputMouseButton, clickCount int) error {
	m.state.Lock()
	toButtons := append(append([]proto.InputMouseButton{}, m.state.buttons...), button)
	_, buttons := input.EncodeMouseButton(toButtons)
	pos := m.state.pos
	m.state.Unlock()

	err := proto.InputDispatchMouseEvent{
		Type:       proto.InputDispatchMouseEventTypeMousePressed,
		Button:     button,
		Buttons:    gson.Int(buttons),
		ClickCount: clickCount,
		Modifiers:  m.page.Keyboard.getModifiers(),
		X:          pos.X,
		Y:          pos.Y,
	}.Call(m.page)
	if err != nil {
		return err
	}

	m.state.Lock()
	m.state.buttons = toButtons
	m.state.Unlock()
	return nil
}

// Up releases the button.
func (m *Mouse) Up(button proto.InputMouseButton, clickCount int) error {
	m.state.Lock()
	toButtons := []proto.InputMouseButton{}
	for _, btn := range m.state.buttons {
		if btn == button {
			continue
		}
		toButtons = append(toButtons, btn)
	}
	_, buttons := input.EncodeMouseButton(toButtons)
	pos := m.state.pos
	m.state.Unlock()

	err := proto.InputDispatchMouseEvent{
		Type:       proto.InputDispatchMouseEventTypeMouseReleased,
		Button:     button,
		Buttons:    gson.Int(buttons),
		ClickCount: clickCount,
		Modifiers:  m.page.Keyboard.getModifiers(),
		X:          pos.X,
		Y:          pos.Y,
	}.Call(m.page)
	if err != nil {
		return err
	}

	m.state.Lock()
	m.state.buttons = toButtons
	m.state.Unlock()
	return nil
}

// Click the button. It's the combination of [Mouse.Down] and [Mouse.Up].
func (m *Mouse) Click(button proto.InputMouseButton, clickCount int) error {
	m.page.browser.trySlowMotion()

	err := m.Down(button, clickCount)
	if err != nil {
		return err
	}

	err = m.Up(button, clickCount)
	if err != nil {
		// The button is down in the browser and the caller's ctx is likely
		// dead; retry the release on a short detached ctx, or every later
		// move with the latched button becomes a drag.
		ctx, cancel := context.WithTimeout(m.page.browser.rootContext(), 5*time.Second)
		defer cancel()
		if rerr := m.page.Context(ctx).Mouse.Up(button, clickCount); rerr != nil {
			return fmt.Errorf("%w (compensating mouse up failed: %v)", err, rerr)
		}
	}
	return err
}

// Touch presents a touch device, such as a hand with fingers, each finger is a [proto.InputTouchPoint].
// Touch events is stateless, we use the struct here only as a namespace to make the API style unified.
type Touch struct {
	page *Page
}

func (p *Page) newTouch() *Page {
	p.Touch = &Touch{page: p}
	return p
}

// Start a touch action.
func (t *Touch) Start(points ...*proto.InputTouchPoint) error {
	// TODO: https://crbug.com/613219
	_ = t.page.WaitRepaint()
	_ = t.page.WaitRepaint()

	return proto.InputDispatchTouchEvent{
		Type:        proto.InputDispatchTouchEventTypeTouchStart,
		TouchPoints: points,
		Modifiers:   t.page.Keyboard.getModifiers(),
	}.Call(t.page)
}

// Move touch points. Use the [proto.InputTouchPoint.ID] (Touch.identifier) to track points.
// Doc: https://developer.mozilla.org/en-US/docs/Web/API/Touch_events
func (t *Touch) Move(points ...*proto.InputTouchPoint) error {
	return proto.InputDispatchTouchEvent{
		Type:        proto.InputDispatchTouchEventTypeTouchMove,
		TouchPoints: points,
		Modifiers:   t.page.Keyboard.getModifiers(),
	}.Call(t.page)
}

// End touch action.
func (t *Touch) End() error {
	return proto.InputDispatchTouchEvent{
		Type:        proto.InputDispatchTouchEventTypeTouchEnd,
		TouchPoints: []*proto.InputTouchPoint{},
		Modifiers:   t.page.Keyboard.getModifiers(),
	}.Call(t.page)
}

// Cancel touch action.
func (t *Touch) Cancel() error {
	return proto.InputDispatchTouchEvent{
		Type:        proto.InputDispatchTouchEventTypeTouchCancel,
		TouchPoints: []*proto.InputTouchPoint{},
		Modifiers:   t.page.Keyboard.getModifiers(),
	}.Call(t.page)
}

// Tap dispatches a touchstart and touchend event.
func (t *Touch) Tap(x, y float64) error {
	defer t.page.tryTrace(TraceTypeInput, "touch")()
	t.page.browser.trySlowMotion()

	p := &proto.InputTouchPoint{X: x, Y: y}

	err := t.Start(p)
	if err != nil {
		return err
	}

	return t.End()
}
