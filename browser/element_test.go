package browser

import "testing"

func TestElementString(t *testing.T) {
	named := &Element{Role: "button", Name: "Sign in"}
	if got, want := named.String(), `button "Sign in"`; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	unnamed := &Element{Role: "textbox"}
	if got, want := unnamed.String(), "ok"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
