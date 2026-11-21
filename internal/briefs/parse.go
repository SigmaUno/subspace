package briefs

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	MaxDocumentBytes    = 1 << 20
	maxFrontmatterLines = 5000
	maxDepth            = 6
	maxItems            = 1000
)

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type Pos struct {
	Line int `json:"line"`
	Col  int `json:"column"`
}

type ParseError struct {
	Pos
	Msg string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("line %d, column %d: %s", e.Line, e.Col, e.Msg)
}

func errAt(p Pos, format string, a ...any) *ParseError {
	return &ParseError{Pos: p, Msg: fmt.Sprintf(format, a...)}
}

type kind int

const (
	scalarNode kind = iota
	listNode
	mapNode
)

type node struct {
	kind  kind
	pos   Pos
	value string
	plain bool
	items []*node
	keys  []entry
}

type entry struct {
	key string
	pos Pos
	val *node
}

func (n *node) describe() string {
	switch n.kind {
	case listNode:
		return "a list"
	case mapNode:
		return "a mapping"
	}
	return "a scalar"
}

type line struct {
	no     int
	raw    string
	indent int
	text   string
}

func (l line) pos(off int) Pos {
	return Pos{Line: l.no, Col: utf8.RuneCountInString(l.raw[:l.indent+off]) + 1}
}

type parser struct {
	lines []line
	i     int
}

func splitDocument(src []byte) ([]line, string, error) {
	if len(src) > MaxDocumentBytes {
		return nil, "", errAt(Pos{1, 1}, "document is larger than %d bytes", MaxDocumentBytes)
	}
	s := string(src)
	if !utf8.ValidString(s) {
		return nil, "", errAt(offsetPos(s, firstInvalid(s)), "document is not valid UTF-8")
	}
	if i := strings.IndexByte(s, 0); i >= 0 {
		return nil, "", errAt(offsetPos(s, i), "document contains a NUL byte")
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	raw := strings.Split(s, "\n")
	if raw[0] != "---" {
		return nil, "", errAt(Pos{1, 1}, `document must start with a "---" frontmatter line`)
	}
	end := -1
	for i := 1; i < len(raw); i++ {
		if raw[i] == "---" {
			end = i
			break
		}
		if i > maxFrontmatterLines {
			return nil, "", errAt(Pos{i + 1, 1}, "frontmatter is longer than %d lines", maxFrontmatterLines)
		}
	}
	if end < 0 {
		return nil, "", errAt(Pos{1, 1}, `frontmatter is not closed with a "---" line`)
	}
	var lines []line
	for i := 1; i < end; i++ {
		r := raw[i]
		for j, c := range r {
			if c == '\t' {
				return nil, "", errAt(Pos{i + 1, utf8.RuneCountInString(r[:j]) + 1}, "tabs are not allowed in frontmatter")
			}
			if c < 0x20 || c == 0x7f {
				return nil, "", errAt(Pos{i + 1, utf8.RuneCountInString(r[:j]) + 1}, "control character %U is not allowed in frontmatter", c)
			}
		}
		text := strings.TrimRight(r, " ")
		trimmed := strings.TrimLeft(text, " ")
		if trimmed == "" || trimmed[0] == '#' {
			continue
		}
		lines = append(lines, line{no: i + 1, raw: r, indent: len(text) - len(trimmed), text: trimmed})
	}
	return lines, strings.TrimSpace(strings.Join(raw[end+1:], "\n")), nil
}

func firstInvalid(s string) int {
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n <= 1 {
			return i
		}
		i += n
	}
	return len(s)
}

func offsetPos(s string, off int) Pos {
	head := s[:off]
	ln := strings.Count(head, "\n") + 1
	start := strings.LastIndexByte(head, '\n') + 1
	return Pos{Line: ln, Col: utf8.RuneCountInString(head[start:]) + 1}
}

func parseFrontmatter(lines []line) (*node, error) {
	if len(lines) == 0 {
		return &node{kind: mapNode, pos: Pos{2, 1}}, nil
	}
	if lines[0].indent != 0 {
		return nil, errAt(lines[0].pos(0), "unexpected indentation")
	}
	if isItem(lines[0].text) {
		return nil, errAt(lines[0].pos(0), "frontmatter must be a mapping of keys, not a list")
	}
	p := &parser{lines: lines}
	root, err := p.parseMap(0, 0)
	if err != nil {
		return nil, err
	}
	if p.i < len(p.lines) {
		return nil, errAt(p.lines[p.i].pos(0), "unexpected indentation")
	}
	return root, nil
}

func isItem(text string) bool {
	return text == "-" || strings.HasPrefix(text, "- ")
}

func leadingSpaces(s string) int {
	return len(s) - len(strings.TrimLeft(s, " "))
}

func blankOrComment(s string) bool {
	s = strings.TrimLeft(s, " ")
	return s == "" || s[0] == '#'
}

func looksLikeKey(text string) bool {
	k := strings.IndexByte(text, ':')
	return k > 0 && keyPattern.MatchString(text[:k]) && (k+1 == len(text) || text[k+1] == ' ')
}

func clip(s string) string {
	if utf8.RuneCountInString(s) <= 40 {
		return s
	}
	return string([]rune(s)[:40]) + "…"
}

func (p *parser) child(l line, off, indent, depth int, key string) (*node, error) {
	if p.i < len(p.lines) && p.lines[p.i].indent > indent {
		return p.parseBlock(p.lines[p.i].indent, depth)
	}
	if key == "" {
		return nil, errAt(l.pos(off), "missing value for list item")
	}
	if p.i < len(p.lines) && p.lines[p.i].indent == indent && isItem(p.lines[p.i].text) {
		return p.parseList(indent, depth, true)
	}
	return nil, errAt(l.pos(off), "missing value for %q", key)
}

func (p *parser) parseBlock(indent, depth int) (*node, error) {
	if isItem(p.lines[p.i].text) {
		return p.parseList(indent, depth, false)
	}
	return p.parseMap(indent, depth)
}

func (p *parser) parseMap(indent, depth int) (*node, error) {
	first := p.lines[p.i]
	if depth > maxDepth {
		return nil, errAt(first.pos(0), "nesting is deeper than %d levels", maxDepth)
	}
	m := &node{kind: mapNode, pos: first.pos(0)}
	seen := map[string]Pos{}
	for p.i < len(p.lines) {
		l := p.lines[p.i]
		if l.indent < indent {
			break
		}
		if l.indent > indent {
			return nil, errAt(l.pos(0), "unexpected indentation")
		}
		if isItem(l.text) {
			return nil, errAt(l.pos(0), "unexpected list item inside a mapping")
		}
		k := strings.IndexByte(l.text, ':')
		if k < 0 {
			return nil, errAt(l.pos(0), `expected "key: value"`)
		}
		key := l.text[:k]
		if !keyPattern.MatchString(key) {
			return nil, errAt(l.pos(0), "invalid key %q: keys are lowercase letters, digits and underscores", clip(key))
		}
		if k+1 < len(l.text) && l.text[k+1] != ' ' {
			return nil, errAt(l.pos(k+1), `expected a space after ":"`)
		}
		kp := l.pos(0)
		if prev, dup := seen[key]; dup {
			return nil, errAt(kp, "duplicate key %q (first defined on line %d)", key, prev.Line)
		}
		seen[key] = kp
		if len(seen) > maxItems {
			return nil, errAt(kp, "mapping has more than %d keys", maxItems)
		}
		p.i++
		rest := l.text[k+1:]
		var val *node
		var err error
		if blankOrComment(rest) {
			val, err = p.child(l, k+1, indent, depth+1, key)
		} else {
			val, err = inline(l, k+1+leadingSpaces(rest))
		}
		if err != nil {
			return nil, err
		}
		m.keys = append(m.keys, entry{key: key, pos: kp, val: val})
	}
	return m, nil
}

func (p *parser) parseList(indent, depth int, compact bool) (*node, error) {
	first := p.lines[p.i]
	if depth > maxDepth {
		return nil, errAt(first.pos(0), "nesting is deeper than %d levels", maxDepth)
	}
	n := &node{kind: listNode, pos: first.pos(0)}
	for p.i < len(p.lines) {
		l := p.lines[p.i]
		if l.indent < indent {
			break
		}
		if l.indent > indent {
			return nil, errAt(l.pos(0), "unexpected indentation")
		}
		if !isItem(l.text) {
			if compact {
				break
			}
			return nil, errAt(l.pos(0), `expected a list item starting with "- "`)
		}
		if len(n.items) == maxItems {
			return nil, errAt(l.pos(0), "list has more than %d items", maxItems)
		}
		rest := l.text[1:]
		off := 1 + leadingSpaces(rest)
		var item *node
		var err error
		switch {
		case blankOrComment(rest):
			p.i++
			item, err = p.child(l, 1, indent, depth+1, "")
		case looksLikeKey(l.text[off:]):
			p.lines[p.i].indent += off
			p.lines[p.i].text = l.text[off:]
			item, err = p.parseMap(indent+off, depth+1)
		default:
			p.i++
			item, err = inline(l, off)
		}
		if err != nil {
			return nil, err
		}
		n.items = append(n.items, item)
	}
	return n, nil
}

func inline(l line, off int) (*node, error) {
	s := l.text[off:]
	switch s[0] {
	case '[':
		return flowList(l, off)
	case '"', '\'':
		v, n, err := quoted(l, off)
		if err != nil {
			return nil, err
		}
		if err := trailing(l, off+n); err != nil {
			return nil, err
		}
		return &node{kind: scalarNode, pos: l.pos(off), value: v}, nil
	}
	if err := plainStart(l, off); err != nil {
		return nil, err
	}
	v := s
	if i := strings.Index(s, " #"); i >= 0 {
		v = s[:i]
	}
	v = strings.TrimRight(v, " ")
	if err := plainBody(l, off, v); err != nil {
		return nil, err
	}
	return &node{kind: scalarNode, pos: l.pos(off), value: v, plain: true}, nil
}

func plainStart(l line, off int) error {
	s := l.text[off:]
	switch s[0] {
	case '{':
		return errAt(l.pos(off), "flow mappings are not supported")
	case '|', '>':
		return errAt(l.pos(off), "block scalars are not supported; use a quoted string")
	case '&', '*', '!':
		return errAt(l.pos(off), "anchors, aliases and tags are not supported")
	case '%', '@', '`', ']', '}', ',':
		return errAt(l.pos(off), "a plain value cannot start with %q; quote it", s[0])
	case '-', '?', ':':
		if len(s) == 1 || s[1] == ' ' {
			return errAt(l.pos(off), "a plain value cannot start with %q; quote it", s[:1])
		}
	}
	return nil
}

func plainBody(l line, off int, v string) error {
	if i := strings.Index(v, ": "); i >= 0 {
		return errAt(l.pos(off+i), `plain value contains ": "; quote it`)
	}
	if strings.HasSuffix(v, ":") {
		return errAt(l.pos(off+len(v)-1), `plain value ends with ":"; quote it`)
	}
	return nil
}

func trailing(l line, off int) error {
	rest := l.text[off:]
	if rest == "" {
		return nil
	}
	if rest[0] == ' ' && blankOrComment(rest) {
		return nil
	}
	return errAt(l.pos(off), "unexpected text after value")
}

func quoted(l line, off int) (string, int, error) {
	s := l.text[off:]
	q := s[0]
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c == q && q == '\'' && i+1 < len(s) && s[i+1] == '\'':
			b.WriteByte('\'')
			i++
		case c == q:
			return b.String(), i + 1, nil
		case c == '\\' && q == '"':
			if i+1 == len(s) {
				return "", 0, errAt(l.pos(off), "unterminated string")
			}
			i++
			switch s[i] {
			case '"', '\\', '/':
				b.WriteByte(s[i])
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				return "", 0, errAt(l.pos(off+i-1), "unsupported escape sequence \\%c", s[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", 0, errAt(l.pos(off), "unterminated string")
}

func flowList(l line, off int) (*node, error) {
	s := l.text[off:]
	n := &node{kind: listNode, pos: l.pos(off)}
	i := 1 + leadingSpaces(s[1:])
	if i < len(s) && s[i] == ']' {
		return n, trailing(l, off+i+1)
	}
	for {
		i += leadingSpaces(s[i:])
		if i >= len(s) {
			return nil, errAt(l.pos(off), `unterminated list: missing "]"`)
		}
		if len(n.items) == maxItems {
			return nil, errAt(l.pos(off+i), "list has more than %d items", maxItems)
		}
		switch s[i] {
		case '[', '{':
			return nil, errAt(l.pos(off+i), "nested collections are not supported inside [ ]")
		case ',', ']':
			return nil, errAt(l.pos(off+i), "empty list item")
		case '"', '\'':
			v, w, err := quoted(l, off+i)
			if err != nil {
				return nil, err
			}
			n.items = append(n.items, &node{kind: scalarNode, pos: l.pos(off + i), value: v})
			i += w
		default:
			if err := plainStart(l, off+i); err != nil {
				return nil, err
			}
			j := i + strings.IndexAny(s[i:], ",[]{}")
			if j < i {
				return nil, errAt(l.pos(off), `unterminated list: missing "]"`)
			}
			if s[j] != ',' && s[j] != ']' {
				return nil, errAt(l.pos(off+j), "nested collections are not supported inside [ ]")
			}
			v := strings.TrimRight(s[i:j], " ")
			if k := strings.Index(v, " #"); k >= 0 {
				return nil, errAt(l.pos(off+i+k+1), "comments are not allowed inside [ ]")
			}
			if err := plainBody(l, off+i, v); err != nil {
				return nil, err
			}
			n.items = append(n.items, &node{kind: scalarNode, pos: l.pos(off + i), value: v, plain: true})
			i = j
		}
		i += leadingSpaces(s[i:])
		if i >= len(s) {
			return nil, errAt(l.pos(off), `unterminated list: missing "]"`)
		}
		switch s[i] {
		case ',':
			i++
		case ']':
			return n, trailing(l, off+i+1)
		default:
			return nil, errAt(l.pos(off+i), `expected "," or "]"`)
		}
	}
}
