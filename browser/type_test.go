package browser

import (
	"strings"
	"testing"

	"github.com/machinae/safewebtool/internal/rod/lib/input"
)

func TestParseKeys(t *testing.T) {
	tests := []struct {
		in   string
		mods []input.Key
		key  input.Key
	}{
		{"Enter", nil, input.Enter},
		{"escape", nil, input.Escape},
		{"F5", nil, input.F5},
		{"a", nil, input.Key('a')},
		{"#", nil, input.Key('#')},
		{"+", nil, input.Key('+')},
		{"Control+A", []input.Key{input.ControlLeft}, input.Key('A')},
		{"ctrl+a", []input.Key{input.ControlLeft}, input.Key('a')},
		{"Shift+ArrowRight", []input.Key{input.ShiftLeft}, input.ArrowRight},
		{"Control+Shift+K", []input.Key{input.ControlLeft, input.ShiftLeft}, input.Key('K')},
		{"Cmd+A", []input.Key{input.MetaLeft}, input.Key('A')},
		{"Control++", []input.Key{input.ControlLeft}, input.Key('+')},
		{"Shift", nil, input.ShiftLeft},
	}
	for _, tt := range tests {
		mods, key, err := parseKeys(tt.in)
		if err != nil {
			t.Errorf("parseKeys(%q): unexpected error %v", tt.in, err)
			continue
		}
		if key != tt.key {
			t.Errorf("parseKeys(%q) key = %v, want %v", tt.in, key, tt.key)
		}
		if len(mods) != len(tt.mods) {
			t.Errorf("parseKeys(%q) mods = %v, want %v", tt.in, mods, tt.mods)
			continue
		}
		for i := range mods {
			if mods[i] != tt.mods[i] {
				t.Errorf("parseKeys(%q) mods[%d] = %v, want %v", tt.in, i, mods[i], tt.mods[i])
			}
		}
	}
}

func TestParseKeysErrors(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Hyperspace", "unknown key"},
		{"Warp+K", "unknown modifier"},
		{"é", "US keyboard layout"},
		{"", "unknown key"},
	}
	for _, tt := range tests {
		_, _, err := parseKeys(tt.in)
		if err == nil {
			t.Errorf("parseKeys(%q): want error containing %q, got nil", tt.in, tt.want)
			continue
		}
		if !strings.Contains(err.Error(), tt.want) {
			t.Errorf("parseKeys(%q) error = %q, want it to contain %q", tt.in, err, tt.want)
		}
	}
}

func TestShiftedKey(t *testing.T) {
	for _, r := range "A!@?~" {
		if !shiftedKey(input.Key(r)) {
			t.Errorf("shiftedKey(%q) = false, want true", r)
		}
	}
	for _, r := range "a1; " {
		if shiftedKey(input.Key(r)) {
			t.Errorf("shiftedKey(%q) = true, want false", r)
		}
	}
	if shiftedKey(input.Enter) {
		t.Error("shiftedKey(Enter) = true, want false")
	}
}
