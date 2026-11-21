package store_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
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
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
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
