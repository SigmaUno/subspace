package store

import (
	"encoding/json"
	"strings"
	"time"
)

type Scanner interface {
	Scan(dest ...any) error
}

type ColumnKind int

const (
	KindText ColumnKind = iota
	KindNullText
	KindInt
	KindMillis
	KindReal
	KindBool
	KindJSON
)

type ColumnDef struct {
	Name    string
	Kind    ColumnKind
	Default string
}

func (c ColumnDef) Constraint() string {
	if c.Kind == KindNullText {
		return ""
	}
	def := c.Default
	if def == "" {
		switch c.Kind {
		case KindText:
			def = "''"
		case KindBool:
			def = "FALSE"
		case KindJSON:
			def = "'[]'"
		default:
			def = "0"
		}
	}
	return " NOT NULL DEFAULT " + def
}

type Column[T any] struct {
	ColumnDef
	expr   string
	fixed  bool
	set    string
	value  func(*T) (any, error)
	target func(*T) (any, func() error)
}

func (c Column[T]) immutable() Column[T] {
	c.fixed = true
	return c
}

func (c Column[T]) withSet(format string) Column[T] {
	c.set = format
	return c
}

func (c Column[T]) withDefault(v string) Column[T] {
	c.Default = v
	return c
}

func (c Column[T]) writable() bool { return c.expr == "" }

func (c Column[T]) updatable() bool { return c.writable() && !c.fixed }

func field[T, V any](name string, kind ColumnKind, f func(*T) *V) Column[T] {
	return Column[T]{
		ColumnDef: ColumnDef{Name: name, Kind: kind},
		value:     func(x *T) (any, error) { return *f(x), nil },
		target:    func(x *T) (any, func() error) { return f(x), nil },
	}
}

func codec[T, V any](name string, kind ColumnKind, enc func(*T) (any, error), dec func(*T, V) error) Column[T] {
	return Column[T]{
		ColumnDef: ColumnDef{Name: name, Kind: kind},
		value:     enc,
		target: func(x *T) (any, func() error) {
			v := new(V)
			return v, func() error { return dec(x, *v) }
		},
	}
}

func computed[T, V any](expr string, dec func(*T, V)) Column[T] {
	c := codec(expr, KindText, nil, func(x *T, v V) error { dec(x, v); return nil })
	c.expr = expr
	return c
}

func optText[T any](name string, f func(*T) **string) Column[T] {
	return codec(name, KindText,
		func(x *T) (any, error) { return Str(*f(x)), nil },
		func(x *T, v string) error { *f(x) = strPtr(v); return nil })
}

func timeCol[T any](name string, f func(*T) *time.Time) Column[T] {
	return codec(name, KindMillis,
		func(x *T) (any, error) { return Millis(*f(x)), nil },
		func(x *T, v int64) error { *f(x) = FromMillis(v); return nil })
}

func optTime[T any](name string, f func(*T) **time.Time) Column[T] {
	return codec(name, KindMillis,
		func(x *T) (any, error) { return MillisPtr(*f(x)), nil },
		func(x *T, v int64) error { *f(x) = fromMillisPtr(v); return nil })
}

func list[T any](name string, f func(*T) *[]string) Column[T] {
	return codec(name, KindJSON,
		func(x *T) (any, error) { return listJSON(*f(x)) },
		func(x *T, v string) (err error) { *f(x), err = parseList(v); return err })
}

type Table[T any] struct {
	Name string
	Cols []Column[T]
}

type Schema interface {
	Table() string
	Defs() []ColumnDef
}

func (t *Table[T]) Table() string { return t.Name }

func (t *Table[T]) Defs() []ColumnDef {
	var out []ColumnDef
	for _, c := range t.Cols {
		if c.writable() {
			out = append(out, c.ColumnDef)
		}
	}
	return out
}

func (t *Table[T]) Select(alias string) string {
	names := make([]string, len(t.Cols))
	for i, c := range t.Cols {
		switch {
		case !c.writable():
			names[i] = c.expr
		case alias != "":
			names[i] = alias + "." + c.Name
		default:
			names[i] = c.Name
		}
	}
	return strings.Join(names, ", ")
}

func (t *Table[T]) Insert(ph func(int) string, extra ...string) string {
	var names, vals []string
	for _, c := range t.Cols {
		if c.writable() {
			names = append(names, c.Name)
			vals = append(vals, ph(len(vals)+1))
		}
	}
	for _, n := range extra {
		names = append(names, n)
		vals = append(vals, ph(len(vals)+1))
	}
	return `INSERT INTO ` + t.Name + `(` + strings.Join(names, ", ") + `) VALUES(` + strings.Join(vals, ",") + `)`
}

func (t *Table[T]) Update(ph func(int) string) string {
	var sets []string
	for _, c := range t.Cols[1:] {
		if !c.updatable() {
			continue
		}
		p := ph(len(sets) + 1)
		if c.set != "" {
			p = strings.ReplaceAll(c.set, "?", p)
		}
		sets = append(sets, c.Name+`=`+p)
	}
	return `UPDATE ` + t.Name + ` SET ` + strings.Join(sets, ", ") + ` WHERE ` + t.Cols[0].Name + `=` + ph(len(sets)+1)
}

func (t *Table[T]) InsertArgs(x *T) ([]any, error) {
	return t.args(x, Column[T].writable, nil)
}

func (t *Table[T]) UpdateArgs(x *T) ([]any, error) {
	key, err := t.Cols[0].value(x)
	if err != nil {
		return nil, err
	}
	return t.args(x, Column[T].updatable, key)
}

func (t *Table[T]) args(x *T, keep func(Column[T]) bool, key any) ([]any, error) {
	out := make([]any, 0, len(t.Cols)+1)
	for i, c := range t.Cols {
		if !keep(c) || (key != nil && i == 0) {
			continue
		}
		v, err := c.value(x)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if key != nil {
		out = append(out, key)
	}
	return out, nil
}

func (t *Table[T]) Scan(s Scanner) (*T, error) {
	x := new(T)
	dest := make([]any, len(t.Cols))
	var finish []func() error
	for i, c := range t.Cols {
		d, f := c.target(x)
		dest[i] = d
		if f != nil {
			finish = append(finish, f)
		}
	}
	if err := s.Scan(dest...); err != nil {
		return nil, err
	}
	for _, f := range finish {
		if err := f(); err != nil {
			return nil, err
		}
	}
	return x, nil
}

func Millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func NullMillis(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixMilli()
}

func Flag(b bool) int {
	if b {
		return 1
	}
	return 0
}

func MillisPtr(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return Millis(*t)
}

func FromMillis(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

func fromMillisPtr(v int64) *time.Time {
	if v == 0 {
		return nil
	}
	t := time.UnixMilli(v).UTC()
	return &t
}

func Str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func listJSON(v []string) (string, error) {
	if v == nil {
		v = []string{}
	}
	b, err := json.Marshal(v)
	return string(b), err
}

func parseList(s string) ([]string, error) {
	out := []string{}
	if s == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}
