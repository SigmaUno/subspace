package store_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"
)

var (
	sqliteOnlyMethods = []string{"Store.Checkpoint", "Store.SetAutocheckpoint", "Store.Vacuum"}
	postgresOnly      = []string{"Store.DatabaseSize", "Store.Restore"}
)

func methods(t *testing.T, dir string) []string {
	t.Helper()
	pkgs, err := parser.ParseDir(token.NewFileSet(), dir, func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Recv == nil || !fd.Name.IsExported() {
					continue
				}
				recv := fd.Recv.List[0].Type
				if star, ok := recv.(*ast.StarExpr); ok {
					recv = star.X
				}
				if id, ok := recv.(*ast.Ident); ok && (id.Name == "Store" || id.Name == "tx") {
					out = append(out, id.Name+"."+fd.Name.Name)
				}
			}
		}
	}
	return out
}

func TestSQLiteAndPostgresExposeTheSameStoreMethods(t *testing.T) {
	lite, pg := methods(t, "sqlite"), methods(t, "postgres")
	for _, m := range lite {
		if !slices.Contains(pg, m) && !slices.Contains(sqliteOnlyMethods, m) {
			t.Errorf("%s is implemented for SQLite only", m)
		}
	}
	for _, m := range pg {
		if !slices.Contains(lite, m) && !slices.Contains(postgresOnly, m) {
			t.Errorf("%s is implemented for Postgres only", m)
		}
	}
}
