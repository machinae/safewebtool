package browser

import "testing"

func TestActionResultStringOmitsEmptyFields(t *testing.T) {
	got := (ActionResult{
		Action:   "click",
		TabID:    "abc1234",
		Snapshot: "[url] https://example.com",
	}).String()
	want := "action: click\ntab: abc1234\n[url] https://example.com"
	if got != want {
		t.Fatalf("ActionResult.String() = %q, want %q", got, want)
	}
}

func TestActionResultStringIncludesDesc(t *testing.T) {
	got := (ActionResult{
		Action: "click",
		TabID:  "abc1234",
		Desc:   `button "Submit"`,
	}).String()
	want := "action: click\ntab: abc1234\nbutton \"Submit\""
	if got != want {
		t.Fatalf("ActionResult.String() = %q, want %q", got, want)
	}
}
