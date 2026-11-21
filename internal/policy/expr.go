package policy

import (
	"fmt"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/SigmaUno/subspace/internal/conflict"
)

const (
	MaxSource     = 2048
	MaxNodes      = 256
	MaxDepth      = 32
	MaxSteps      = 5000
	MaxListLength = 64
	MaxRegex      = 200
	maxRegexInsts = 2000
	maxString     = 512
)

type Type uint8

const (
	TBool Type = iota + 1
	TNum
	TStr
	TStrList
	TNumList
)

func (t Type) String() string {
	switch t {
	case TBool:
		return "bool"
	case TNum:
		return "number"
	case TStr:
		return "string"
	case TStrList:
		return "list<string>"
	case TNumList:
		return "list<number>"
	}
	return "invalid"
}

func (t Type) elem() Type {
	switch t {
	case TStrList:
		return TStr
	case TNumList:
		return TNum
	}
	return 0
}

func (t Type) list() bool { return t == TStrList || t == TNumList }

type tokKind uint8

const (
	tEOF tokKind = iota
	tIdent
	tStr
	tNum
	tOp
)

type token struct {
	kind tokKind
	text string
	pos  int
}

type ExprError struct {
	Pos     int
	Message string
}

func (e *ExprError) Error() string { return fmt.Sprintf("at %d: %s", e.Pos, e.Message) }

func errAt(pos int, format string, a ...any) *ExprError {
	return &ExprError{Pos: pos, Message: fmt.Sprintf(format, a...)}
}

var twoCharOps = []string{"==", "!=", "<=", ">=", "&&", "||"}

func lex(src string) ([]token, error) {
	var out []token
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '"':
			s, n, err := lexString(src, i)
			if err != nil {
				return nil, err
			}
			out = append(out, token{tStr, s, i})
			i = n
		case c >= '0' && c <= '9':
			j := i
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.' || src[j] == '_') {
				j++
			}
			out = append(out, token{tNum, src[i:j], i})
			i = j
		case c == '_' || c < utf8.RuneSelf && unicode.IsLetter(rune(c)):
			j := i
			for j < len(src) && (src[j] == '_' || src[j] == '.' || src[j] < utf8.RuneSelf && (unicode.IsLetter(rune(src[j])) || unicode.IsDigit(rune(src[j])))) {
				j++
			}
			out = append(out, token{tIdent, src[i:j], i})
			i = j
		default:
			op := ""
			for _, o := range twoCharOps {
				if strings.HasPrefix(src[i:], o) {
					op = o
					break
				}
			}
			if op == "" && strings.ContainsRune("<>!()[],-", rune(c)) {
				op = string(c)
			}
			if op == "" {
				return nil, errAt(i, "unexpected character %q", rune(c))
			}
			out = append(out, token{tOp, op, i})
			i += len(op)
		}
	}
	return append(out, token{tEOF, "", len(src)}), nil
}

func lexString(src string, start int) (string, int, error) {
	var sb strings.Builder
	i := start + 1
	for i < len(src) {
		c := src[i]
		switch c {
		case '"':
			if sb.Len() > maxString {
				return "", 0, errAt(start, "string literal longer than %d bytes", maxString)
			}
			return sb.String(), i + 1, nil
		case '\\':
			if i+1 >= len(src) {
				return "", 0, errAt(i, "unterminated escape")
			}
			switch src[i+1] {
			case '"', '\\':
				sb.WriteByte(src[i+1])
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			default:
				return "", 0, errAt(i, "unknown escape \\%c", src[i+1])
			}
			i += 2
		default:
			if c < 0x20 {
				return "", 0, errAt(i, "control character in string")
			}
			sb.WriteByte(c)
			i++
		}
	}
	return "", 0, errAt(start, "unterminated string")
}

type nodeKind uint8

const (
	nLit nodeKind = iota + 1
	nFact
	nList
	nNot
	nAnd
	nOr
	nCmp
	nIn
	nCall
)

type node struct {
	kind nodeKind
	pos  int
	op   string
	val  Value
	name string
	args []*node
	typ  Type
	re   *regexp.Regexp
}

type parser struct {
	toks  []token
	i     int
	nodes int
	depth int
}

func (p *parser) peek() token { return p.toks[p.i] }

func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tEOF {
		p.i++
	}
	return t
}

func (p *parser) isOp(s string) bool {
	t := p.peek()
	return t.kind == tOp && t.text == s
}

func (p *parser) isWord(s string) bool {
	t := p.peek()
	return t.kind == tIdent && t.text == s
}

func (p *parser) mk(k nodeKind, pos int) (*node, error) {
	p.nodes++
	if p.nodes > MaxNodes {
		return nil, errAt(pos, "expression has more than %d nodes", MaxNodes)
	}
	return &node{kind: k, pos: pos}, nil
}

func (p *parser) enter(pos int) error {
	p.depth++
	if p.depth > MaxDepth {
		return errAt(pos, "expression nests deeper than %d", MaxDepth)
	}
	return nil
}

func (p *parser) leave() { p.depth-- }

func parse(src string) (*node, error) {
	if len(src) > MaxSource {
		return nil, errAt(0, "expression longer than %d bytes", MaxSource)
	}
	if !utf8.ValidString(src) {
		return nil, errAt(0, "expression is not valid UTF-8")
	}
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	n, err := p.or()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tEOF {
		return nil, errAt(t.pos, "unexpected %q", t.text)
	}
	return n, nil
}

func (p *parser) or() (*node, error) {
	return p.chain(nOr, "||", "or", p.and)
}

func (p *parser) and() (*node, error) {
	return p.chain(nAnd, "&&", "and", p.unary)
}

func (p *parser) chain(k nodeKind, sym, word string, sub func() (*node, error)) (*node, error) {
	l, err := sub()
	if err != nil {
		return nil, err
	}
	for p.isOp(sym) || p.isWord(word) {
		t := p.next()
		r, err := sub()
		if err != nil {
			return nil, err
		}
		n, err := p.mk(k, t.pos)
		if err != nil {
			return nil, err
		}
		n.args = []*node{l, r}
		l = n
	}
	return l, nil
}

func (p *parser) unary() (*node, error) {
	t := p.peek()
	if p.isOp("!") || p.isWord("not") {
		if err := p.enter(t.pos); err != nil {
			return nil, err
		}
		defer p.leave()
		p.next()
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		n, err := p.mk(nNot, t.pos)
		if err != nil {
			return nil, err
		}
		n.args = []*node{x}
		return n, nil
	}
	return p.comparison()
}

var cmpOps = map[string]bool{"==": true, "!=": true, "<": true, "<=": true, ">": true, ">=": true}

func (p *parser) comparison() (*node, error) {
	l, err := p.primary()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	switch {
	case t.kind == tOp && cmpOps[t.text]:
		p.next()
		r, err := p.primary()
		if err != nil {
			return nil, err
		}
		n, err := p.mk(nCmp, t.pos)
		if err != nil {
			return nil, err
		}
		n.op, n.args = t.text, []*node{l, r}
		return n, nil
	case p.isWord("in"):
		p.next()
		return p.membership(l, t.pos, false)
	case p.isWord("not") && p.toks[p.i+1].kind == tIdent && p.toks[p.i+1].text == "in":
		p.next()
		p.next()
		return p.membership(l, t.pos, true)
	}
	return l, nil
}

func (p *parser) membership(l *node, pos int, negate bool) (*node, error) {
	r, err := p.primary()
	if err != nil {
		return nil, err
	}
	n, err := p.mk(nIn, pos)
	if err != nil {
		return nil, err
	}
	n.args = []*node{l, r}
	if !negate {
		return n, nil
	}
	neg, err := p.mk(nNot, pos)
	if err != nil {
		return nil, err
	}
	neg.args = []*node{n}
	return neg, nil
}

func (p *parser) primary() (*node, error) {
	t := p.next()
	if err := p.enter(t.pos); err != nil {
		return nil, err
	}
	defer p.leave()
	switch t.kind {
	case tStr:
		n, err := p.mk(nLit, t.pos)
		if err != nil {
			return nil, err
		}
		n.val = Str(t.text)
		return n, nil
	case tNum:
		f, err := strconv.ParseFloat(strings.ReplaceAll(t.text, "_", ""), 64)
		if err != nil || strings.HasSuffix(t.text, ".") {
			return nil, errAt(t.pos, "invalid number %q", t.text)
		}
		n, err := p.mk(nLit, t.pos)
		if err != nil {
			return nil, err
		}
		n.val = Num(f)
		return n, nil
	case tIdent:
		switch t.text {
		case "true", "false":
			n, err := p.mk(nLit, t.pos)
			if err != nil {
				return nil, err
			}
			n.val = Bool(t.text == "true")
			return n, nil
		case "and", "or", "not", "in":
			return nil, errAt(t.pos, "unexpected keyword %q", t.text)
		}
		if p.isOp("(") {
			return p.call(t)
		}
		if strings.HasPrefix(t.text, ".") || strings.HasSuffix(t.text, ".") || strings.Contains(t.text, "..") {
			return nil, errAt(t.pos, "invalid name %q", t.text)
		}
		n, err := p.mk(nFact, t.pos)
		if err != nil {
			return nil, err
		}
		n.name = t.text
		return n, nil
	case tOp:
		switch t.text {
		case "(":
			x, err := p.or()
			if err != nil {
				return nil, err
			}
			if !p.isOp(")") {
				return nil, errAt(p.peek().pos, "expected )")
			}
			p.next()
			return x, nil
		case "[":
			return p.list(t.pos)
		case "!":
			p.i--
			return p.unary()
		case "-":
			num := p.next()
			if num.kind != tNum {
				return nil, errAt(t.pos, "- must be followed by a number")
			}
			f, err := strconv.ParseFloat(strings.ReplaceAll(num.text, "_", ""), 64)
			if err != nil || strings.HasSuffix(num.text, ".") {
				return nil, errAt(num.pos, "invalid number %q", num.text)
			}
			n, err := p.mk(nLit, t.pos)
			if err != nil {
				return nil, err
			}
			n.val = Num(-f)
			return n, nil
		}
	case tEOF:
		return nil, errAt(t.pos, "unexpected end of expression")
	}
	return nil, errAt(t.pos, "unexpected %q", t.text)
}

func (p *parser) list(pos int) (*node, error) {
	n, err := p.mk(nList, pos)
	if err != nil {
		return nil, err
	}
	if p.isOp("]") {
		p.next()
		return n, nil
	}
	for {
		x, err := p.primary()
		if err != nil {
			return nil, err
		}
		n.args = append(n.args, x)
		if len(n.args) > MaxListLength {
			return nil, errAt(pos, "list literal longer than %d", MaxListLength)
		}
		if p.isOp("]") {
			p.next()
			return n, nil
		}
		if !p.isOp(",") {
			return nil, errAt(p.peek().pos, "expected , or ]")
		}
		p.next()
	}
}

func (p *parser) call(name token) (*node, error) {
	p.next()
	n, err := p.mk(nCall, name.pos)
	if err != nil {
		return nil, err
	}
	n.name = name.text
	if p.isOp(")") {
		p.next()
		return n, nil
	}
	for {
		x, err := p.or()
		if err != nil {
			return nil, err
		}
		n.args = append(n.args, x)
		if p.isOp(")") {
			p.next()
			return n, nil
		}
		if !p.isOp(",") {
			return nil, errAt(p.peek().pos, "expected , or )")
		}
		p.next()
	}
}

type FactLookup func(name string) (Type, bool)

type checker struct {
	facts   FactLookup
	used    map[string]bool
	unknown []string
}

func (c *checker) check(n *node) error {
	for _, a := range n.args {
		if err := c.check(a); err != nil {
			return err
		}
	}
	switch n.kind {
	case nLit:
		n.typ = n.val.Type()
	case nFact:
		t, ok := c.facts(n.name)
		if !ok {
			c.unknown = append(c.unknown, n.name)
			return errAt(n.pos, "unknown fact %q", n.name)
		}
		c.used[n.name] = true
		n.typ = t
	case nList:
		n.typ = TStrList
		for i, a := range n.args {
			if a.kind != nLit || (a.typ != TStr && a.typ != TNum) {
				return errAt(a.pos, "list literals may only contain string or number literals")
			}
			if i == 0 && a.typ == TNum {
				n.typ = TNumList
			}
			if a.typ != n.typ.elem() {
				return errAt(a.pos, "list literal mixes strings and numbers")
			}
		}
	case nNot:
		if n.args[0].typ != TBool {
			return errAt(n.pos, "not needs a bool, got %s", n.args[0].typ)
		}
		n.typ = TBool
	case nAnd, nOr:
		for _, a := range n.args {
			if a.typ != TBool {
				return errAt(a.pos, "and/or need bools, got %s", a.typ)
			}
		}
		n.typ = TBool
	case nCmp:
		l, r := n.args[0].typ, n.args[1].typ
		if l != r {
			return errAt(n.pos, "cannot compare %s with %s", l, r)
		}
		if l.list() {
			return errAt(n.pos, "cannot compare lists with %s; use in or any_in", n.op)
		}
		if n.op != "==" && n.op != "!=" && l == TBool {
			return errAt(n.pos, "%s needs numbers or strings", n.op)
		}
		n.typ = TBool
	case nIn:
		l, r := n.args[0].typ, n.args[1].typ
		switch {
		case r.list() && r.elem() == l:
		case r == TStr && l == TStr:
		case len(n.args[1].args) == 0 && n.args[1].kind == nList && (l == TStr || l == TNum):
		default:
			return errAt(n.pos, "%s in %s is not supported", l, r)
		}
		n.typ = TBool
	case nCall:
		return c.call(n)
	}
	return nil
}

func (c *checker) call(n *node) error {
	argc := func(k int) error {
		if len(n.args) != k {
			return errAt(n.pos, "%s takes %d arguments", n.name, k)
		}
		return nil
	}
	strOrList := func(a *node) error {
		if a.typ != TStr && a.typ != TStrList {
			return errAt(a.pos, "%s needs a string or list<string>, got %s", n.name, a.typ)
		}
		return nil
	}
	literal := func(a *node) error {
		if a.kind != nLit || a.typ != TStr {
			return errAt(a.pos, "%s needs a string literal pattern", n.name)
		}
		return nil
	}
	switch n.name {
	case "glob", "overlaps", "regex":
		if err := argc(2); err != nil {
			return err
		}
		if err := strOrList(n.args[0]); err != nil {
			return err
		}
		if err := literal(n.args[1]); err != nil {
			return err
		}
		pat := n.args[1].val.s
		switch n.name {
		case "glob":
			if err := checkGlob(pat); err != nil {
				return errAt(n.args[1].pos, "%v", err)
			}
		case "overlaps":
			if err := checkGlob(pat); err != nil {
				return errAt(n.args[1].pos, "%v", err)
			}
			if _, err := conflict.Normalize(pat); err != nil {
				return errAt(n.args[1].pos, "%v", err)
			}
		case "regex":
			re, err := compileRegex(pat)
			if err != nil {
				return errAt(n.args[1].pos, "%v", err)
			}
			n.re = re
		}
	case "has_prefix", "has_suffix":
		if err := argc(2); err != nil {
			return err
		}
		if err := strOrList(n.args[0]); err != nil {
			return err
		}
		if n.args[1].typ != TStr {
			return errAt(n.args[1].pos, "%s needs a string prefix", n.name)
		}
	case "contains":
		if err := argc(2); err != nil {
			return err
		}
		if n.args[0].typ != TStr || n.args[1].typ != TStr {
			return errAt(n.pos, "contains needs two strings")
		}
	case "any_in":
		if err := argc(2); err != nil {
			return err
		}
		l, r := n.args[0].typ, n.args[1].typ
		if !l.list() || !r.list() || (l != r && (n.args[1].kind != nList || len(n.args[1].args) != 0)) {
			return errAt(n.pos, "any_in needs two lists of the same type")
		}
	case "len":
		if err := argc(1); err != nil {
			return err
		}
		if a := n.args[0].typ; a != TStr && !a.list() {
			return errAt(n.pos, "len needs a string or list")
		}
		n.typ = TNum
		return nil
	case "lower":
		if err := argc(1); err != nil {
			return err
		}
		if n.args[0].typ != TStr {
			return errAt(n.pos, "lower needs a string")
		}
		n.typ = TStr
		return nil
	default:
		return errAt(n.pos, "unknown function %q", n.name)
	}
	n.typ = TBool
	return nil
}

func compileRegex(pat string) (*regexp.Regexp, error) {
	if len(pat) > MaxRegex {
		return nil, fmt.Errorf("regex longer than %d bytes", MaxRegex)
	}
	re, err := syntax.Parse(pat, syntax.Perl)
	if err != nil {
		return nil, fmt.Errorf("invalid regex: %v", err)
	}
	if regexSize(re) > maxRegexInsts {
		return nil, fmt.Errorf("regex is too complex")
	}
	prog, err := syntax.Compile(re.Simplify())
	if err != nil {
		return nil, fmt.Errorf("invalid regex: %v", err)
	}
	if len(prog.Inst) > maxRegexInsts {
		return nil, fmt.Errorf("regex is too complex")
	}
	return regexp.Compile(pat)
}

func regexSize(re *syntax.Regexp) int {
	n := 1 + len(re.Rune)
	for _, sub := range re.Sub {
		n += regexSize(sub)
		if n > maxRegexInsts {
			return n
		}
	}
	if re.Op == syntax.OpRepeat {
		reps := re.Max
		if reps < 0 {
			reps = re.Min + 1
		}
		n *= max(reps, 1)
	}
	return n
}

type Expr struct {
	Source string
	root   *node
	Facts  []string
}

func Compile(src string, facts FactLookup) (*Expr, error) {
	if len(src) > MaxSource {
		return nil, errAt(0, "expression longer than %d bytes", MaxSource)
	}
	if strings.TrimSpace(src) == "" {
		return &Expr{Source: src}, nil
	}
	root, err := parse(src)
	if err != nil {
		return nil, err
	}
	c := &checker{facts: facts, used: map[string]bool{}}
	if err := c.check(root); err != nil {
		return nil, err
	}
	if root.typ != TBool {
		return nil, errAt(0, "expression must be a bool, got %s", root.typ)
	}
	e := &Expr{Source: src, root: root}
	for f := range c.used {
		e.Facts = append(e.Facts, f)
	}
	sortStrings(e.Facts)
	return e, nil
}

func UnknownFacts(src string, facts FactLookup) []string {
	root, err := parse(src)
	if err != nil {
		return nil
	}
	var out []string
	var walk func(n *node)
	walk = func(n *node) {
		if n.kind == nFact {
			if _, ok := facts(n.name); !ok && !containsStr(out, n.name) {
				out = append(out, n.name)
			}
		}
		for _, a := range n.args {
			walk(a)
		}
	}
	walk(root)
	return out
}
