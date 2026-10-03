package indexer

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// CSS selectors, the subset Cardigann definitions use (ADR-0058, decision 3).
//
// Type, universal, #id, .class; [a], [a=v], [a^=v], [a$=v], [a*=v], [a~=v];
// descendant, child (>), adjacent (+) and general (~) sibling combinators;
// selector lists; :nth-child, :first-child, :last-child, :contains, :has and
// :not. Anything else is a parse error, which is how a definition using it is
// refused when it is saved rather than misread when it is searched.

// cssSelector is a selector list: an element matches when any chain does.
type cssSelector []cssChain

// cssChain is compounds joined by combinators, read right to left when
// matching: combs[i] joins parts[i] and parts[i+1].
type cssChain struct {
	parts []cssCompound
	combs []byte
}

type cssCompound struct {
	tag     string // "" for any
	id      string
	classes []string
	attrs   []cssAttr
	pseudos []cssPseudo
}

type cssAttr struct {
	name, op, val string
}

type cssPseudo struct {
	kind string // nth-child, first-child, last-child, contains, has, not
	a, b int    // an+b for nth-child
	arg  string // contains
	sub  cssSelector
}

// parseSelector parses a selector list.
func parseSelector(s string) (cssSelector, error) {
	p := &cssParser{s: s}
	sel, err := p.list()
	if err != nil {
		return nil, fmt.Errorf("selector %q: %w", s, err)
	}
	if p.skipSpace(); p.i < len(p.s) {
		return nil, fmt.Errorf("selector %q: unexpected %q", s, p.s[p.i:])
	}
	return sel, nil
}

type cssParser struct {
	s string
	i int
}

func (p *cssParser) skipSpace() bool {
	start := p.i
	for p.i < len(p.s) && strings.ContainsRune(" \t\r\n", rune(p.s[p.i])) {
		p.i++
	}
	return p.i > start
}

func (p *cssParser) list() (cssSelector, error) {
	var out cssSelector
	for {
		p.skipSpace()
		c, err := p.chain()
		if err != nil {
			return nil, err
		}
		out = append(out, c)
		p.skipSpace()
		if p.i < len(p.s) && p.s[p.i] == ',' {
			p.i++
			continue
		}
		return out, nil
	}
}

func (p *cssParser) chain() (cssChain, error) {
	var c cssChain
	first, err := p.compound()
	if err != nil {
		return c, err
	}
	c.parts = append(c.parts, first)
	for {
		spaced := p.skipSpace()
		if p.i >= len(p.s) || p.s[p.i] == ',' || p.s[p.i] == ')' {
			return c, nil
		}
		comb := byte(' ')
		switch p.s[p.i] {
		case '>', '+', '~':
			comb = p.s[p.i]
			p.i++
			p.skipSpace()
		default:
			if !spaced {
				return c, fmt.Errorf("unexpected %q", p.s[p.i:])
			}
		}
		next, err := p.compound()
		if err != nil {
			return c, err
		}
		c.combs = append(c.combs, comb)
		c.parts = append(c.parts, next)
	}
}

func isIdentByte(b byte) bool {
	return b == '-' || b == '_' || b >= 0x80 ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func (p *cssParser) ident() string {
	start := p.i
	for p.i < len(p.s) {
		if p.s[p.i] == '\\' && p.i+1 < len(p.s) {
			p.i += 2
			continue
		}
		if !isIdentByte(p.s[p.i]) {
			break
		}
		p.i++
	}
	return strings.ReplaceAll(p.s[start:p.i], `\`, "")
}

func (p *cssParser) compound() (cssCompound, error) {
	var c cssCompound
	start := p.i
	if p.i < len(p.s) && p.s[p.i] == '*' {
		p.i++
	} else {
		c.tag = strings.ToLower(p.ident())
	}
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case '#':
			p.i++
			if c.id = p.ident(); c.id == "" {
				return c, errors.New("an empty #id")
			}
		case '.':
			p.i++
			cls := p.ident()
			if cls == "" {
				return c, errors.New("an empty .class")
			}
			c.classes = append(c.classes, cls)
		case '[':
			p.i++
			a, err := p.attr()
			if err != nil {
				return c, err
			}
			c.attrs = append(c.attrs, a)
		case ':':
			p.i++
			ps, err := p.pseudo()
			if err != nil {
				return c, err
			}
			c.pseudos = append(c.pseudos, ps)
		default:
			if p.i == start {
				return c, fmt.Errorf("expected a selector at %q", p.s[p.i:])
			}
			return c, nil
		}
	}
	if p.i == start {
		return c, errors.New("an empty selector")
	}
	return c, nil
}

// quoted reads a "..." or '...' string, or an unquoted run up to stop.
func (p *cssParser) quoted(stop byte) (string, error) {
	p.skipSpace()
	if p.i < len(p.s) && (p.s[p.i] == '"' || p.s[p.i] == '\'') {
		q := p.s[p.i]
		end := strings.IndexByte(p.s[p.i+1:], q)
		if end < 0 {
			return "", errors.New("an unterminated string")
		}
		v := p.s[p.i+1 : p.i+1+end]
		p.i += end + 2
		p.skipSpace()
		return v, nil
	}
	end := strings.IndexByte(p.s[p.i:], stop)
	if end < 0 {
		return "", fmt.Errorf("no closing %q", stop)
	}
	v := strings.TrimSpace(p.s[p.i : p.i+end])
	p.i += end
	return v, nil
}

func (p *cssParser) attr() (cssAttr, error) {
	p.skipSpace()
	a := cssAttr{name: strings.ToLower(p.ident())}
	if a.name == "" {
		return a, errors.New("an attribute selector without a name")
	}
	p.skipSpace()
	if p.i < len(p.s) && p.s[p.i] == ']' {
		p.i++
		return a, nil
	}
	for _, op := range []string{"^=", "$=", "*=", "~=", "="} {
		if strings.HasPrefix(p.s[p.i:], op) {
			a.op = op
			p.i += len(op)
			break
		}
	}
	if a.op == "" {
		return a, fmt.Errorf("an attribute operator this build does not follow at %q", p.s[p.i:])
	}
	v, err := p.quoted(']')
	if err != nil {
		return a, err
	}
	a.val = v
	if p.i >= len(p.s) || p.s[p.i] != ']' {
		return a, errors.New("an attribute selector without its ]")
	}
	p.i++
	return a, nil
}

func (p *cssParser) pseudo() (cssPseudo, error) {
	ps := cssPseudo{kind: strings.ToLower(p.ident())}
	switch ps.kind {
	case "first-child":
		ps.kind, ps.a, ps.b = "nth-child", 0, 1
		return ps, nil
	case "last-child":
		return ps, nil
	case "nth-child", "contains", "has", "not":
	default:
		return ps, fmt.Errorf(":%s is not a selector this build follows", ps.kind)
	}
	if p.i >= len(p.s) || p.s[p.i] != '(' {
		return ps, fmt.Errorf(":%s without its argument", ps.kind)
	}
	p.i++
	var err error
	switch ps.kind {
	case "nth-child":
		var arg string
		if arg, err = p.quoted(')'); err == nil {
			ps.a, ps.b, err = parseNth(arg)
		}
	case "contains":
		ps.arg, err = p.quoted(')')
	default:
		ps.sub, err = p.list()
	}
	if err != nil {
		return ps, err
	}
	p.skipSpace()
	if p.i >= len(p.s) || p.s[p.i] != ')' {
		return ps, fmt.Errorf(":%s without its )", ps.kind)
	}
	p.i++
	return ps, nil
}

// parseNth reads an+b, odd, even, or a number.
func parseNth(s string) (int, int, error) {
	s = strings.ToLower(strings.ReplaceAll(s, " ", ""))
	switch s {
	case "odd":
		return 2, 1, nil
	case "even":
		return 2, 0, nil
	}
	n := strings.IndexByte(s, 'n')
	if n < 0 {
		b, err := strconv.Atoi(s)
		return 0, b, err
	}
	a := 1
	switch head := s[:n]; head {
	case "", "+":
	case "-":
		a = -1
	default:
		v, err := strconv.Atoi(head)
		if err != nil {
			return 0, 0, fmt.Errorf("an nth-child of %q", s)
		}
		a = v
	}
	b := 0
	if tail := s[n+1:]; tail != "" {
		v, err := strconv.Atoi(tail)
		if err != nil {
			return 0, 0, fmt.Errorf("an nth-child of %q", s)
		}
		b = v
	}
	return a, b, nil
}

// ---------------------------------------------------------------------------
// Matching
// ---------------------------------------------------------------------------

func (sel cssSelector) matches(n *html.Node) bool {
	if n == nil || n.Type != html.ElementNode {
		return false
	}
	for _, c := range sel {
		if c.matchAt(len(c.parts)-1, n) {
			return true
		}
	}
	return false
}

func (c cssChain) matchAt(i int, n *html.Node) bool {
	if !c.parts[i].matches(n) {
		return false
	}
	if i == 0 {
		return true
	}
	switch c.combs[i-1] {
	case '>':
		p := n.Parent
		return p != nil && p.Type == html.ElementNode && c.matchAt(i-1, p)
	case '+':
		s := prevElement(n)
		return s != nil && c.matchAt(i-1, s)
	case '~':
		for s := prevElement(n); s != nil; s = prevElement(s) {
			if c.matchAt(i-1, s) {
				return true
			}
		}
		return false
	}
	for p := n.Parent; p != nil && p.Type == html.ElementNode; p = p.Parent {
		if c.matchAt(i-1, p) {
			return true
		}
	}
	return false
}

func prevElement(n *html.Node) *html.Node {
	for s := n.PrevSibling; s != nil; s = s.PrevSibling {
		if s.Type == html.ElementNode {
			return s
		}
	}
	return nil
}

func attrOf(n *html.Node, name string) (string, bool) {
	for _, a := range n.Attr {
		if a.Namespace == "" && strings.EqualFold(a.Key, name) {
			return a.Val, true
		}
	}
	return "", false
}

func (c cssCompound) matches(n *html.Node) bool {
	if c.tag != "" && n.Data != c.tag {
		return false
	}
	if c.id != "" {
		if v, _ := attrOf(n, "id"); v != c.id {
			return false
		}
	}
	if len(c.classes) > 0 {
		v, _ := attrOf(n, "class")
		have := strings.Fields(v)
		for _, want := range c.classes {
			if !containsString(have, want) {
				return false
			}
		}
	}
	for _, a := range c.attrs {
		v, ok := attrOf(n, a.name)
		if !ok {
			return false
		}
		switch a.op {
		case "=":
			ok = v == a.val
		case "^=":
			ok = a.val != "" && strings.HasPrefix(v, a.val)
		case "$=":
			ok = a.val != "" && strings.HasSuffix(v, a.val)
		case "*=":
			ok = a.val != "" && strings.Contains(v, a.val)
		case "~=":
			ok = containsString(strings.Fields(v), a.val)
		}
		if !ok {
			return false
		}
	}
	for _, ps := range c.pseudos {
		if !ps.matches(n) {
			return false
		}
	}
	return true
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (ps cssPseudo) matches(n *html.Node) bool {
	switch ps.kind {
	case "nth-child":
		pos := 1
		for s := prevElement(n); s != nil; s = prevElement(s) {
			pos++
		}
		if ps.a == 0 {
			return pos == ps.b
		}
		k := pos - ps.b
		return k%ps.a == 0 && k/ps.a >= 0
	case "last-child":
		for s := n.NextSibling; s != nil; s = s.NextSibling {
			if s.Type == html.ElementNode {
				return false
			}
		}
		return true
	case "contains":
		return strings.Contains(strings.ToLower(nodeText(n, nil)), strings.ToLower(ps.arg))
	case "has":
		found := false
		walk(n, func(d *html.Node) bool {
			if d != n && ps.sub.matches(d) {
				found = true
			}
			return !found
		})
		return found
	case "not":
		return !ps.sub.matches(n)
	}
	return false
}

// walk visits n and its descendants in document order while visit says to go
// on.
func walk(n *html.Node, visit func(*html.Node) bool) bool {
	if !visit(n) {
		return false
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if !walk(c, visit) {
			return false
		}
	}
	return true
}

// selectAll is every descendant element of root the selector matches, in
// document order, at most limit of them.
func (sel cssSelector) selectAll(root *html.Node, limit int) []*html.Node {
	var out []*html.Node
	walk(root, func(n *html.Node) bool {
		if n != root && sel.matches(n) {
			out = append(out, n)
		}
		return len(out) < limit
	})
	return out
}

// selectFirst is the first descendant element of root the selector matches.
func (sel cssSelector) selectFirst(root *html.Node) *html.Node {
	if found := sel.selectAll(root, 1); len(found) > 0 {
		return found[0]
	}
	return nil
}

// nodeText is the text of n and its descendants, leaving out any subtree
// whose root is in skip.
func nodeText(n *html.Node, skip map[*html.Node]bool) string {
	var b strings.Builder
	walkText(n, skip, &b)
	return b.String()
}

func walkText(n *html.Node, skip map[*html.Node]bool, b *strings.Builder) {
	if skip[n] {
		return
	}
	if n.Type == html.TextNode {
		b.WriteString(n.Data)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkText(c, skip, b)
	}
}
