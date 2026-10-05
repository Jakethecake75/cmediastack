package request

import (
	"errors"
	"slices"
	"testing"
)

// A scope is part of the match key, so one choice must always read the same
// (ADR-0075): sorted, without repeats, an episode its season covers dropped.
func TestAScopeHasOneCanonicalForm(t *testing.T) {
	got, err := FormatScope([]Part{{Season: 2, Episode: 5}, {Season: 1}, {Season: 1, Episode: 3}, {Season: 2, Episode: 5}, {Season: 2, Episode: 1}})
	if err != nil || got != "S1,S2E1,S2E5" {
		t.Fatalf("FormatScope = %q, %v; want S1,S2E1,S2E5", got, err)
	}
	back, err := ParseScope(got)
	if err != nil || !slices.Equal(back, []Part{{Season: 1}, {Season: 2, Episode: 1}, {Season: 2, Episode: 5}}) {
		t.Errorf("ParseScope(%q) = %v, %v", got, back, err)
	}
	if s, _ := FormatScope(nil); s != "" {
		t.Errorf("no parts = %q, want the whole title", s)
	}
	for _, bad := range [][]Part{{{Season: -1}}, {{Season: 101}}, {{Season: 1, Episode: 2001}}} {
		if _, err := FormatScope(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("FormatScope(%v) = %v, want ErrInvalid", bad, err)
		}
	}
	if _, err := ParseScope("E5"); err == nil {
		t.Error("ParseScope read a scope with no season")
	}
}
