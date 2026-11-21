package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type mutant struct {
	Pkg  string
	Rel  string
	Line int
	Desc string
	src  []byte
}

func (m *mutant) String() string { return fmt.Sprintf("%s:%d: %s", m.Rel, m.Line, m.Desc) }

type site struct {
	pos   token.Pos
	desc  string
	apply func()
	undo  func()
}

type sourceFile struct {
	pkg, rel string
	fset     *token.FileSet
	file     *ast.File
	sites    []site
}

func (s *sourceFile) render(i int) (*mutant, error) {
	st := s.sites[i]
	st.apply()
	defer st.undo()
	var buf bytes.Buffer
	cfg := printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 8}
	if err := cfg.Fprint(&buf, s.fset, s.file); err != nil {
		return nil, err
	}
	return &mutant{Pkg: s.pkg, Rel: s.rel, Line: s.fset.Position(st.pos).Line, Desc: st.desc, src: buf.Bytes()}, nil
}

type listedPackage struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	Export     string
	DepOnly    bool
}

func loadPackages(root string, pkgs []string, files *regexp.Regexp) ([]*sourceFile, error) {
	args := []string{"list", "-deps", "-export", "-json=ImportPath,Dir,GoFiles,Export,DepOnly"}
	for _, p := range pkgs {
		args = append(args, "./"+filepath.ToSlash(filepath.Clean(p)))
	}
	cmd := exec.Command("go", args...) //nolint:gosec // package directories chosen by the operator
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %v: %s", err, stderr.String())
	}
	exports := map[string]string{}
	var targets []listedPackage
	dec := json.NewDecoder(bytes.NewReader(raw))
	for dec.More() {
		var lp listedPackage
		if err := dec.Decode(&lp); err != nil {
			return nil, err
		}
		exports[lp.ImportPath] = lp.Export
		if !lp.DepOnly {
			targets = append(targets, lp)
		}
	}
	var out []*sourceFile
	for _, lp := range targets {
		sfs, err := parsePackage(root, lp, exports, files)
		if err != nil {
			return nil, err
		}
		out = append(out, sfs...)
	}
	return out, nil
}

func parsePackage(root string, lp listedPackage, exports map[string]string, filter *regexp.Regexp) ([]*sourceFile, error) {
	rel, err := filepath.Rel(root, lp.Dir)
	if err != nil {
		return nil, err
	}
	pkg := filepath.ToSlash(rel)
	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(lp.GoFiles))
	for _, name := range lp.GoFiles {
		src, err := os.ReadFile(filepath.Join(lp.Dir, name)) //nolint:gosec // listed by go list
		if err != nil {
			return nil, err
		}
		f, err := parser.ParseFile(fset, path.Join(pkg, name), src, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}}
	lookup := func(path string) (io.ReadCloser, error) {
		e := exports[path]
		if e == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(e) //nolint:gosec // export data listed by go list
	}
	conf := types.Config{Importer: importer.ForCompiler(fset, "gc", lookup)}
	if _, err := conf.Check(lp.ImportPath, fset, files, info); err != nil {
		return nil, fmt.Errorf("type-check %s: %w", lp.ImportPath, err)
	}
	var out []*sourceFile
	for i, f := range files {
		rel := path.Join(pkg, lp.GoFiles[i])
		if filter != nil && !filter.MatchString(rel) {
			continue
		}
		if ast.IsGenerated(f) {
			continue
		}
		out = append(out, &sourceFile{pkg: pkg, rel: rel, fset: fset, file: f, sites: collect(f, info)})
	}
	return out, nil
}

func collect(f *ast.File, info *types.Info) []site {
	c := &collector{info: info}
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			c.stack = c.stack[:len(c.stack)-1]
			return true
		}
		if isLogCall(n) {
			return false
		}
		c.visit(n)
		c.stack = append(c.stack, n)
		return true
	})
	sort.SliceStable(c.sites, func(i, j int) bool { return c.sites[i].pos < c.sites[j].pos })
	return c.sites
}

type collector struct {
	info  *types.Info
	stack []ast.Node
	sites []site
}

func (c *collector) add(pos token.Pos, desc string, apply, undo func()) {
	c.sites = append(c.sites, site{pos: pos, desc: desc, apply: apply, undo: undo})
}

var opMutations = map[token.Token][]token.Token{
	token.EQL:  {token.NEQ},
	token.NEQ:  {token.EQL},
	token.LSS:  {token.GEQ, token.LEQ},
	token.LEQ:  {token.GTR, token.LSS},
	token.GTR:  {token.LEQ, token.GEQ},
	token.GEQ:  {token.LSS, token.GTR},
	token.LAND: {token.LOR},
	token.LOR:  {token.LAND},
}

func (c *collector) visit(n ast.Node) {
	switch n := n.(type) {
	case *ast.BinaryExpr:
		c.binary(n)
	case *ast.BasicLit:
		c.intLit(n)
	case *ast.IfStmt:
		c.ifCond(n)
	case *ast.ForStmt:
		if n.Cond != nil && !isComparison(n.Cond) {
			c.negate(&n.Cond)
		}
	case *ast.CallExpr:
		c.swapArgs(n)
	case *ast.BlockStmt:
		c.stmtList(n.List, c.funcBody(n))
	case *ast.CaseClause:
		c.stmtList(n.Body, nil)
	case *ast.CommClause:
		c.stmtList(n.Body, nil)
	}
}

func (c *collector) binary(e *ast.BinaryExpr) {
	old := e.Op
	for _, op := range opMutations[old] {
		c.add(e.OpPos, fmt.Sprintf("replace %s with %s", old, op), func() { e.Op = op }, func() { e.Op = old })
	}
}

func (c *collector) intLit(l *ast.BasicLit) {
	if l.Kind != token.INT {
		return
	}
	v, err := strconv.ParseInt(l.Value, 0, 64)
	if err != nil {
		return
	}
	old := l.Value
	repl := []int64{v + 1}
	if v > 0 {
		repl = append(repl, v-1)
	}
	for _, r := range repl {
		s := strconv.FormatInt(r, 10)
		c.add(l.Pos(), fmt.Sprintf("change constant %s to %s", old, s), func() { l.Value = s }, func() { l.Value = old })
	}
}

func (c *collector) ifCond(s *ast.IfStmt) {
	if isErrCheck(s.Cond) {
		old := s.Cond
		c.add(s.Cond.Pos(), "skip error check", func() { s.Cond = &ast.Ident{NamePos: old.Pos(), Name: "false"} }, func() { s.Cond = old })
		return
	}
	if !isComparison(s.Cond) {
		c.negate(&s.Cond)
	}
}

func (c *collector) negate(cond *ast.Expr) {
	old := *cond
	neg := &ast.UnaryExpr{OpPos: old.Pos(), Op: token.NOT, X: &ast.ParenExpr{Lparen: old.Pos(), X: old, Rparen: old.End()}}
	c.add(old.Pos(), "negate condition", func() { *cond = neg }, func() { *cond = old })
}

func (c *collector) swapArgs(call *ast.CallExpr) {
	for i := 0; i+1 < len(call.Args); i++ {
		if isFormat(call.Args[i]) {
			return
		}
		a, b := call.Args[i], call.Args[i+1]
		if !c.sameType(a, b) || exprString(a) == exprString(b) {
			continue
		}
		c.add(a.Pos(), fmt.Sprintf("swap arguments %s and %s", exprString(a), exprString(b)),
			func() { call.Args[i], call.Args[i+1] = b, a },
			func() { call.Args[i], call.Args[i+1] = a, b })
	}
}

func (c *collector) stmtList(list []ast.Stmt, fn *ast.FuncType) {
	for i, s := range list {
		if fn != nil && i == len(list)-1 {
			continue
		}
		var desc string
		switch s := s.(type) {
		case *ast.ReturnStmt:
			if returnsError(c.enclosingFunc()) && len(s.Results) > 0 && !isNil(s.Results[len(s.Results)-1]) {
				desc = "remove error return"
			}
		case *ast.AssignStmt:
			if s.Tok == token.ASSIGN && anyErrIdent(s.Lhs) {
				desc = "remove error assignment"
			}
		}
		if desc == "" {
			continue
		}
		old := s
		empty := &ast.EmptyStmt{Semicolon: s.Pos(), Implicit: true}
		c.add(s.Pos(), desc, func() { list[i] = empty }, func() { list[i] = old })
	}
}

func (c *collector) funcBody(b *ast.BlockStmt) *ast.FuncType {
	if len(c.stack) == 0 {
		return nil
	}
	switch p := c.stack[len(c.stack)-1].(type) {
	case *ast.FuncDecl:
		if p.Body == b {
			return p.Type
		}
	case *ast.FuncLit:
		if p.Body == b {
			return p.Type
		}
	}
	return nil
}

func (c *collector) enclosingFunc() *ast.FuncType {
	for i := len(c.stack) - 1; i >= 0; i-- {
		switch p := c.stack[i].(type) {
		case *ast.FuncDecl:
			return p.Type
		case *ast.FuncLit:
			return p.Type
		}
	}
	return nil
}

func returnsError(ft *ast.FuncType) bool {
	if ft == nil || ft.Results == nil || len(ft.Results.List) == 0 {
		return false
	}
	id, ok := ft.Results.List[len(ft.Results.List)-1].Type.(*ast.Ident)
	return ok && id.Name == "error"
}

func isComparison(e ast.Expr) bool {
	b, ok := e.(*ast.BinaryExpr)
	if !ok {
		return false
	}
	switch b.Op {
	case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
		return true
	}
	return false
}

func isErrCheck(e ast.Expr) bool {
	b, ok := e.(*ast.BinaryExpr)
	return ok && b.Op == token.NEQ && isErrIdent(b.X) && isNil(b.Y)
}

func isErrIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && (id.Name == "err" || strings.HasSuffix(id.Name, "Err") || strings.HasSuffix(id.Name, "err"))
}

func anyErrIdent(es []ast.Expr) bool {
	for _, e := range es {
		if isErrIdent(e) {
			return true
		}
	}
	return false
}

func isNil(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

func isFormat(e ast.Expr) bool {
	l, ok := e.(*ast.BasicLit)
	return ok && l.Kind == token.STRING && strings.Contains(l.Value, "%")
}

func (c *collector) sameType(a, b ast.Expr) bool {
	ta, tb := c.info.TypeOf(a), c.info.TypeOf(b)
	if ta == nil || tb == nil || isNil(a) || isNil(b) {
		return false
	}
	return types.Identical(ta, tb)
}

var logMethods = map[string]bool{
	"Debug": true, "Info": true, "Warn": true, "Error": true,
	"DebugContext": true, "InfoContext": true, "WarnContext": true, "ErrorContext": true,
	"Log": true, "LogAttrs": true, "Print": true, "Printf": true, "Println": true,
}

func isLogCall(n ast.Node) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !logMethods[sel.Sel.Name] {
		return false
	}
	return strings.Contains(strings.ToLower(exprString(sel.X)), "log")
}

func exprString(e ast.Expr) string { return types.ExprString(e) }
