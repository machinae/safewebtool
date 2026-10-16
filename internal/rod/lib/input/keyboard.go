// Package input ...
package input

import (
	"strings"

	"github.com/machinae/safewebtool/internal/rod/lib/proto"
	"github.com/ysmood/gson"
)

// Modifier values.
const (
	ModifierAlt     = 1
	ModifierControl = 2
	ModifierMeta    = 4
	ModifierShift   = 8
)

// Key symbol.
type Key rune

// keyMap for key description.
var keyMap = map[Key]KeyInfo{}

// keyMapShifted for shifted key description.
var keyMapShifted = map[Key]KeyInfo{}

var keyShiftedMap = map[Key]Key{}

// keyMapByCode indexes keys by lowercased W3C code, such as "arrowup" or "keya".
var keyMapByCode = map[string]Key{}

// AddKey to KeyMap.
func AddKey(key string, shiftedKey string, code string, keyCode int, location int) Key {
	if len(key) == 1 {
		r := Key(key[0])
		if _, has := keyMap[r]; !has {
			keyMap[r] = KeyInfo{key, code, keyCode, location}
			keyMapByCode[strings.ToLower(code)] = r

			if len(shiftedKey) == 1 {
				rs := Key(shiftedKey[0])
				keyMapShifted[rs] = KeyInfo{shiftedKey, code, keyCode, location}
				keyShiftedMap[r] = rs
			}
			return r
		}
	}

	k := Key(keyCode + (location+1)*256)
	keyMap[k] = KeyInfo{key, code, keyCode, location}
	keyMapByCode[strings.ToLower(code)] = k

	return k
}

// Lookup returns the key with the given W3C code, such as "Enter", "KeyA"
// or "ShiftLeft". The code is matched case-insensitively.
func Lookup(code string) (Key, bool) {
	k, has := keyMapByCode[strings.ToLower(code)]
	return k, has
}

// Info of the key.
func (k Key) Info() KeyInfo {
	if k, has := keyMap[k]; has {
		return k
	}
	if k, has := keyMapShifted[k]; has {
		return k
	}

	panic("key not defined")
}

// KeyInfo of a key
// https://developer.mozilla.org/en-US/docs/Web/API/KeyboardEvent
type KeyInfo struct {
	// Here's the value for Shift key on the keyboard

	Key      string // Shift
	Code     string // ShiftLeft
	KeyCode  int    // 16
	Location int    // 1
}

// Shift returns the shifted key, such as shifted "1" is "!".
func (k Key) Shift() (Key, bool) {
	s, has := keyShiftedMap[k]
	return s, has
}

// Printable returns true if the key is printable.
func (k Key) Printable() bool {
	return len(k.Info().Key) == 1
}

// Modifier returns the modifier value of the key.
func (k Key) Modifier() int {
	switch k.Info().KeyCode {
	case 18:
		return ModifierAlt
	case 17:
		return ModifierControl
	case 91, 92:
		return ModifierMeta
	case 16:
		return ModifierShift
	}
	return 0
}

// Encode general key event.
func (k Key) Encode(t proto.InputDispatchKeyEventType, modifiers int) *proto.InputDispatchKeyEvent {
	tp := t
	if t == proto.InputDispatchKeyEventTypeKeyDown && !k.Printable() {
		tp = proto.InputDispatchKeyEventTypeRawKeyDown
	}

	info := k.Info()
	l := gson.Int(info.Location)
	keypad := false
	if info.Location == 3 {
		l = nil
		keypad = true
	}

	txt := ""
	if k.Printable() {
		txt = info.Key
	}

	// macCommands is keyed Playwright style: active modifiers in
	// Shift, Control, Alt, Meta order, then the W3C code, e.g.
	// "Shift+Meta+ArrowUp". A bare key value would miss every chord
	// entry and keys whose value differs from their code (Enter is "\r").
	var cmd []string
	if IsMac {
		shortcut := ""
		if modifiers&ModifierShift != 0 {
			shortcut += "Shift+"
		}
		if modifiers&ModifierControl != 0 {
			shortcut += "Control+"
		}
		if modifiers&ModifierAlt != 0 {
			shortcut += "Alt+"
		}
		if modifiers&ModifierMeta != 0 {
			shortcut += "Meta+"
		}
		cmd = macCommands[shortcut+info.Code]
	}

	e := &proto.InputDispatchKeyEvent{
		Type:                  tp,
		WindowsVirtualKeyCode: info.KeyCode,
		Code:                  info.Code,
		Key:                   info.Key,
		Text:                  txt,
		UnmodifiedText:        txt,
		Location:              l,
		IsKeypad:              keypad,
		Modifiers:             modifiers,
		Commands:              cmd,
	}

	return e
}
