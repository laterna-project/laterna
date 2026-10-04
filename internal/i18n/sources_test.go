package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The code and the catalogs must agree. Every domain error (domain.NotFound, domain.Invalid...) and
// every text (domain.T, i18n.Text) names a catalog key and gives it all the params its message
// expects, and every catalog key is used somewhere. The test reads the repository sources, so a
// forgotten or misspelled key does not get through.
func TestSourcesMatchCatalogs(t *testing.T) {
	root := filepath.Join("..", "..")
	errorKinds := []string{"NotFound", "Invalid", "Conflict", "Unauthenticated", "Forbidden", "Precondition", "TooManyAttempts", "Busy"}
	used := map[string]bool{}
	fset := token.NewFileSet()
	calls := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
		if d.IsDir() {
			switch {
			case rel == "tools", rel == "bin", rel == "internal/api/gen", rel == "internal/store/sqlc", strings.HasPrefix(d.Name(), ".") && path != root:
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			// Any string equal to a catalog key counts as a use (a key stored in a field, or passed
			// to Template).
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil && Has(s) {
					used[s] = true
				}
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			pkg, name := calledFunc(call)
			var prefix string
			args := call.Args
			switch {
			case pkg == "domain" && slices.Contains(errorKinds, name):
				prefix = "error."
			case pkg == "domain" && name == "T", pkg == "" && name == "T" && strings.HasPrefix(rel, "internal/domain/"):
			case pkg == "i18n" && name == "Text":
				if len(args) > 0 {
					args = args[1:]
				}
			default:
				return true
			}
			pos := fset.Position(call.Pos())
			where := rel + ":" + strconv.Itoa(pos.Line)
			if len(args) == 0 {
				return true // the function declaration itself, or a call without a key
			}
			key, ok := stringLit(args[0])
			if !ok {
				// The constructors themselves pass on the key they were given.
				if rel != "internal/domain/errors.go" && rel != "internal/i18n/i18n.go" {
					t.Errorf("%s: the key of %s.%s must be a string literal", where, pkg, name)
				}
				return true
			}
			calls++
			key = prefix + key
			used[key] = true
			tmpl, ok := catalogs[English][key]
			if !ok {
				t.Errorf("%s: %q is not in the catalog", where, key)
				return true
			}
			// The remaining arguments are name/value pairs; a lone value is the list.
			var names []string
			list := false
			for i := 1; i < len(args); i++ {
				if n, ok := stringLit(args[i]); ok && i+1 < len(args) {
					names = append(names, n)
					i++
					continue
				}
				list = true
			}
			for _, want := range Placeholders(tmpl) {
				switch {
				case want == listParam && !list:
					t.Errorf("%s: %q expects a list", where, key)
				case want != listParam && !slices.Contains(names, want):
					t.Errorf("%s: %q expects param %q (given: %v)", where, key, want, names)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls < 300 {
		t.Fatalf("%d calls found: the sources were not read", calls)
	}
	for _, key := range Keys() {
		// Units are built by formatParam ("unit." + name).
		if !used[key] && !strings.HasPrefix(key, "unit.") {
			t.Errorf("%q is in the catalog but never used", key)
		}
	}
	for _, unit := range []string{"second", "minute", "hour", "day", "byte", "kib", "mib", "gib"} {
		if !Has("unit." + unit) {
			t.Errorf("unit %q missing from the catalog", unit)
		}
	}
}

// calledFunc returns the package and name of the called function ("domain", "NotFound").
func calledFunc(call *ast.CallExpr) (pkg, name string) {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		if x, ok := fun.X.(*ast.Ident); ok {
			return x.Name, fun.Sel.Name
		}
	case *ast.Ident:
		return "", fun.Name
	}
	return "", ""
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}
