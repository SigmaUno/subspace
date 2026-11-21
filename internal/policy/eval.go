package policy

import (
	"errors"
	"math"
	"sort"
	"strings"

	"github.com/SigmaUno/subspace/internal/conflict"
)

type Value struct {
	t Type
	b bool
	n float64
	s string
	l []Value
}

func Bool(b bool) Value   { return Value{t: TBool, b: b} }
func Num(n float64) Value { return Value{t: TNum, n: n} }
func Str(s string) Value  { return Value{t: TStr, s: s} }

func Strs(xs []string) Value {
	l := make([]Value, len(xs))
	for i, x := range xs {
		l[i] = Str(x)
	}
	return Value{t: TStrList, l: l}
}

func Zero(t Type) Value {
	if t == TStrList || t == TNumList {
		return Value{t: t, l: []Value{}}
	}
	return Value{t: t}
}

func (v Value) Type() Type { return v.t }

func (v Value) Any() any {
	switch v.t {
	case TBool:
		return v.b
	case TNum:
		return v.n
	case TStr:
		return v.s
	case TStrList, TNumList:
		out := make([]any, len(v.l))
		for i, x := range v.l {
			out[i] = x.Any()
		}
		return out
	}
	return nil
}

func FromAny(t Type, x any) (Value, bool) {
	switch t {
	case TBool:
		b, ok := x.(bool)
		return Bool(b), ok
	case TNum:
		f, ok := x.(float64)
		if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
			return Value{}, false
		}
		return Num(f), true
	case TStr:
		s, ok := x.(string)
		return Str(s), ok && len(s) <= maxString
	case TStrList, TNumList:
		xs, ok := x.([]any)
		if !ok || len(xs) > MaxListLength {
			return Value{}, false
		}
		out := Value{t: t, l: make([]Value, 0, len(xs))}
		for _, e := range xs {
			v, ok := FromAny(t.elem(), e)
			if !ok {
				return Value{}, false
			}
			out.l = append(out.l, v)
		}
		return out, true
	}
	return Value{}, false
}

func (v Value) equal(o Value) bool {
	if v.t != o.t {
		return false
	}
	switch v.t {
	case TBool:
		return v.b == o.b
	case TNum:
		return v.n == o.n
	case TStr:
		return v.s == o.s
	}
	if len(v.l) != len(o.l) {
		return false
	}
	for i := range v.l {
		if !v.l[i].equal(o.l[i]) {
			return false
		}
	}
	return true
}

func (v Value) strings() []string {
	if v.t == TStr {
		return []string{v.s}
	}
	out := make([]string, len(v.l))
	for i, x := range v.l {
		out[i] = x.s
	}
	return out
}

type Facts interface {
	Fact(name string) Value
}

var ErrSteps = errors.New("evaluation step limit reached")

type evaluator struct {
	facts Facts
	steps int
	limit int
}

func (e *Expr) Eval(f Facts) (bool, int, error) {
	if e.root == nil {
		return true, 0, nil
	}
	ev := &evaluator{facts: f, limit: MaxSteps}
	v, err := ev.eval(e.root)
	return v.b, ev.steps, err
}

func (ev *evaluator) step(n int) error {
	ev.steps += n
	if ev.steps > ev.limit {
		return ErrSteps
	}
	return nil
}

func (ev *evaluator) eval(n *node) (Value, error) {
	if err := ev.step(1); err != nil {
		return Value{}, err
	}
	switch n.kind {
	case nLit:
		return n.val, nil
	case nFact:
		v := ev.facts.Fact(n.name)
		if v.t != n.typ {
			v = Zero(n.typ)
		}
		if err := ev.step(len(v.l)); err != nil {
			return Value{}, err
		}
		return v, nil
	case nList:
		out := Value{t: n.typ, l: make([]Value, len(n.args))}
		for i, a := range n.args {
			out.l[i] = a.val
		}
		return out, ev.step(len(n.args))
	case nNot:
		x, err := ev.eval(n.args[0])
		return Bool(!x.b), err
	case nAnd, nOr:
		l, err := ev.eval(n.args[0])
		if err != nil {
			return Value{}, err
		}
		if (n.kind == nAnd) != l.b {
			return l, nil
		}
		return ev.eval(n.args[1])
	case nCmp:
		l, r, err := ev.pair(n)
		if err != nil {
			return Value{}, err
		}
		return Bool(compare(n.op, l, r)), nil
	case nIn:
		l, r, err := ev.pair(n)
		if err != nil {
			return Value{}, err
		}
		if r.t == TStr {
			return Bool(strings.Contains(r.s, l.s)), ev.step(len(r.s) / 32)
		}
		if err := ev.step(len(r.l)); err != nil {
			return Value{}, err
		}
		for _, x := range r.l {
			if x.equal(l) {
				return Bool(true), nil
			}
		}
		return Bool(false), nil
	case nCall:
		return ev.call(n)
	}
	return Value{}, errors.New("invalid expression")
}

func (ev *evaluator) pair(n *node) (Value, Value, error) {
	l, err := ev.eval(n.args[0])
	if err != nil {
		return Value{}, Value{}, err
	}
	r, err := ev.eval(n.args[1])
	return l, r, err
}

func compare(op string, l, r Value) bool {
	c := 0
	switch l.t {
	case TNum:
		switch {
		case l.n < r.n:
			c = -1
		case l.n > r.n:
			c = 1
		}
	case TStr:
		c = strings.Compare(l.s, r.s)
	case TBool:
		if l.b != r.b {
			c = 1
		}
	}
	switch op {
	case "==":
		return c == 0
	case "!=":
		return c != 0
	case "<":
		return c < 0
	case "<=":
		return c <= 0
	case ">":
		return c > 0
	case ">=":
		return c >= 0
	}
	return false
}

func (ev *evaluator) call(n *node) (Value, error) {
	args := make([]Value, len(n.args))
	for i, a := range n.args {
		v, err := ev.eval(a)
		if err != nil {
			return Value{}, err
		}
		args[i] = v
	}
	switch n.name {
	case "len":
		if args[0].t == TStr {
			return Num(float64(len([]rune(args[0].s)))), nil
		}
		return Num(float64(len(args[0].l))), nil
	case "lower":
		return Str(strings.ToLower(args[0].s)), ev.step(len(args[0].s) / 32)
	case "contains":
		return Bool(strings.Contains(args[0].s, args[1].s)), ev.step(len(args[0].s) / 32)
	case "any_in":
		if err := ev.step(len(args[0].l) * len(args[1].l)); err != nil {
			return Value{}, err
		}
		for _, x := range args[0].l {
			for _, y := range args[1].l {
				if x.equal(y) {
					return Bool(true), nil
				}
			}
		}
		return Bool(false), nil
	}
	pat := args[1].s
	for _, s := range args[0].strings() {
		var ok bool
		switch n.name {
		case "glob":
			if err := ev.step(1 + len(s)*len(pat)/64); err != nil {
				return Value{}, err
			}
			ok = Glob(pat, s)
		case "overlaps":
			if err := ev.step(1 + len(s)*len(pat)/16); err != nil {
				return Value{}, err
			}
			hit, err := conflict.Overlap([]string{s}, []string{pat})
			ok = err == nil && hit
		case "regex":
			if err := ev.step(1 + len(s)/8); err != nil {
				return Value{}, err
			}
			ok = n.re.MatchString(s)
		case "has_prefix":
			ok = strings.HasPrefix(s, pat)
		case "has_suffix":
			ok = strings.HasSuffix(s, pat)
		}
		if ok {
			return Bool(true), nil
		}
	}
	return Bool(false), ev.step(1)
}

type constFacts struct{}

func (constFacts) Fact(string) Value { return Value{} }

func (e *Expr) Constant() (val, ok bool) {
	if e.root == nil {
		return true, true
	}
	return fold(e.root)
}

func fold(n *node) (val, ok bool) {
	switch n.kind {
	case nLit:
		return n.val.b, n.val.t == TBool
	case nNot:
		v, ok := fold(n.args[0])
		return !v, ok
	case nAnd, nOr:
		l, lok := fold(n.args[0])
		r, rok := fold(n.args[1])
		short := n.kind == nOr
		switch {
		case lok && l == short, rok && r == short:
			return short, true
		case lok && rok:
			return !short, true
		}
		return false, false
	case nCmp:
		a, b := n.args[0], n.args[1]
		if a.kind == nFact && b.kind == nFact && a.name == b.name {
			switch n.op {
			case "==", "<=", ">=":
				return true, true
			}
			return false, true
		}
	}
	if !constant(n) {
		return false, false
	}
	ev := &evaluator{facts: constFacts{}, limit: MaxSteps}
	v, err := ev.eval(n)
	if err != nil || v.t != TBool {
		return false, false
	}
	return v.b, true
}

func constant(n *node) bool {
	if n.kind == nFact {
		return false
	}
	for _, a := range n.args {
		if !constant(a) {
			return false
		}
	}
	return true
}

func sortStrings(xs []string) { sort.Strings(xs) }

func containsStr(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
