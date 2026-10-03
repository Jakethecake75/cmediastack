package notify

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode"
)

// A stranger's text stays text: one code span, nothing inside that can close
// it, break the line, or hide what it says.
func TestAStrangersTextCannotBecomeMarkup(t *testing.T) {
	for _, s := range []string{
		"@everyone", "@here", "<@123456789012345678>", "<#123>", "<@&42>",
		"[Fix your server](https://evil.example)", "https://evil.example/", "<https://evil.example>",
		"`rm -rf /`", "``double``", "**bold** __under__ ~~strike~~ ||spoiler|| > quote", "# heading",
		"line one\nline two\r\n\tthree", "\u202Eevil\u202C", "zero\u200Bwidth\u2060joiner\ufeff",
		"ends in a backslash\\", "\\\\", "\xff\xfe invalid", "\x00\x07\x1b[31mred",
		"", "   ", strings.Repeat("long ", 200), strings.Repeat("😀", 300),
	} {
		got := code(s, maxValue)
		inner := strings.TrimSuffix(strings.TrimPrefix(got, "`"), "`")
		if !strings.HasPrefix(got, "`") || !strings.HasSuffix(got, "`") || strings.Contains(inner, "`") {
			t.Errorf("%q → %q: not one code span", s, got)
		}
		if strings.HasSuffix(inner, `\`) {
			t.Errorf("%q → %q: a backslash before the closing backtick", s, got)
		}
		for _, r := range inner {
			if r == '\n' || r == '\r' || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				t.Errorf("%q → %q: holds %U", s, got, r)
			}
		}
		if n := len([]rune(inner)); n > maxValue {
			t.Errorf("%q → %d characters, want at most %d", s, n, maxValue)
		}
	}
	if got := code("line one\n\n  line two  ", maxValue); got != "`line one line two`" {
		t.Errorf("whitespace: %q", got)
	}
	if got := code(strings.Repeat("a", 200), 10); got != "`aaaaaaaaa…`" {
		t.Errorf("clipped: %q", got)
	}
}

// Messages keep to Discord's limit, however many lines there are and whatever
// characters they hold, and what does not fit is counted.
func TestMessagesKeepToDiscordsLimit(t *testing.T) {
	var lines []string
	for i := 0; i < 300; i++ {
		lines = append(lines, fmt.Sprintf("🔒 **Line %d** · %s", i, code(strings.Repeat("😀", 100), maxBody)))
	}
	msgs, left := pack(lines, maxMessages)
	if len(msgs) != maxMessages || left == 0 {
		t.Fatalf("%d messages, %d left", len(msgs), left)
	}
	sent := 0
	for _, m := range msgs {
		if w := width(m); w > 2000 {
			t.Fatalf("a message of %d UTF-16 units", w)
		}
		sent += strings.Count(m, "**Line ")
	}
	if sent+left != 300 || !strings.Contains(msgs[2], fmt.Sprintf("…and %d more — the Audit log has them all.", left)) {
		t.Fatalf("%d sent, %d left: %q", sent, left, msgs[2][len(msgs[2])-80:])
	}
	if msgs, left := pack([]string{"a", "b"}, maxMessages); len(msgs) != 1 || msgs[0] != "a\nb" || left != 0 {
		t.Fatalf("two short lines: %q %d", msgs, left)
	}
	if msgs, _ := pack(nil, maxMessages); len(msgs) != 0 {
		t.Fatalf("no lines: %q", msgs)
	}
	if w := width("a😀"); w != 3 {
		t.Fatalf("width = %d", w)
	}
}

// Times are Discord's, in the reader's timezone: the time for today's, the
// date as well for older.
func TestTimesAreDiscordsOwn(t *testing.T) {
	now := time.Date(2026, 9, 27, 20, 0, 0, 0, time.UTC)
	if got := stamp(now.Add(-time.Hour), now); got != fmt.Sprintf("<t:%d:t>", now.Add(-time.Hour).Unix()) {
		t.Errorf("recent: %s", got)
	}
	if got := stamp(now.Add(-13*time.Hour), now); got != fmt.Sprintf("<t:%d:f>", now.Add(-13*time.Hour).Unix()) {
		t.Errorf("older: %s", got)
	}
}
