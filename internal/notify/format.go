package notify

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// How a message is written (ADR-0032, decision 5).
//
// Discord renders markdown, links and mentions in a message. Much of what is
// sent was written by strangers — a username chosen at signup, a release name,
// the addresses in a flood line — so a username of "@everyone" would ping the
// whole server, and one of "[Fix your server](https://…)" would show a
// disguised link in the operator's own channel. So everything this program did
// not write goes inside a code span, where Discord renders no markdown, no
// link and no mention; the message also switches mentions off (Discord.Send).

// Bounds on what one message and one value may hold.
const (
	// maxContent is under Discord's 2,000 characters, leaving room for the
	// closing line of a run that did not fit.
	maxContent = 1850
	// maxValue and maxBody clip a value: a name, and a detail.
	maxValue = 120
	maxBody  = 400
)

// code puts text this program did not write inside a code span. It is never
// given an empty string.
func code(s string, max int) string {
	s = clean(s, max)
	// A backslash before the closing backtick is left alone by Discord inside
	// a span, but nothing is gained by finding out.
	s = strings.TrimRight(s, `\`)
	if s == "" {
		s = "?"
	}
	return "`" + s + "`"
}

// clean makes text safe to place inside a code span, and short: a backtick,
// which would close the span, becomes an apostrophe; control characters and
// invisible formatting — bidirectional overrides, zero-width joiners, which
// can make one name look like another — are removed; runs of whitespace,
// newlines included, become one space; and the result is clipped to max
// characters, an ellipsis marking the cut.
func clean(s string, max int) string {
	out := make([]rune, 0, min(len(s), max+1))
	space := false
	for _, r := range s {
		switch {
		case r == '`':
			r = '\''
		case r == utf8.RuneError:
			r = '?'
		case unicode.IsSpace(r):
			space = len(out) > 0
			continue
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			continue
		}
		if space {
			out = append(out, ' ')
			space = false
		}
		out = append(out, r)
		if len(out) > max {
			return strings.TrimRight(string(out[:max-1]), " ") + "…"
		}
	}
	return string(out)
}

// width is how long Discord counts a text: in UTF-16 units, which is never
// less than the characters it holds.
func width(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xFFFF {
			n++
		}
	}
	return n
}

// stamp is a time as Discord shows it — in the reader's own timezone — the
// time of day for anything recent, the date as well for anything older.
func stamp(t, now time.Time) string {
	style := "t"
	if now.Sub(t) > 12*time.Hour {
		style = "f"
	}
	return fmt.Sprintf("<t:%d:%s>", t.Unix(), style)
}

// compact renders a stored before or after value on one line.
func compact(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.RawMessage:
		return string(x)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// pack puts lines into as few messages as fit, at most maxMessages; what does
// not fit is counted in a closing line on the last. It reports how many lines
// were left out.
func pack(lines []string, maxMessages int) ([]string, int) {
	var msgs []string
	var cur strings.Builder
	curWidth := 0
	for i, l := range lines {
		if width(l) > maxContent {
			// Cannot happen with the bounds above: every value in a line is
			// clipped. Were it to, cutting the line could leave a code span
			// open, so the line is not sent at all.
			l = "(a line too long to send — the Audit log has it)"
		}
		w := width(l)
		if curWidth > 0 && curWidth+1+w > maxContent {
			msgs = append(msgs, cur.String())
			cur.Reset()
			curWidth = 0
			if len(msgs) == maxMessages {
				left := len(lines) - i
				msgs[len(msgs)-1] += fmt.Sprintf("\n…and %d more — the Audit log has them all.", left)
				return msgs, left
			}
		}
		if curWidth > 0 {
			cur.WriteByte('\n')
			curWidth++
		}
		cur.WriteString(l)
		curWidth += w
	}
	if curWidth > 0 {
		msgs = append(msgs, cur.String())
	}
	return msgs, 0
}
