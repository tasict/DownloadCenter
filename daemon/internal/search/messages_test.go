package search

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"downloadcenter/internal/i18n"
)

// Every message the package gives callers is translated: validation
// messages (calls of invalid), failure reasons and API messages (constants
// named Reason*, msg*, label*) and the exported errors; and phpbox's
// reasons and package messages (Reason*, Msg*), which the API passes on.
func TestMessagesTranslated(t *testing.T) {
	fset := token.NewFileSet()
	notTest := func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }
	pkgs, err := parser.ParseDir(fset, ".", notTest, 0)
	if err != nil {
		t.Fatal(err)
	}
	box, err := parser.ParseDir(fset, "../phpbox", notTest, 0)
	if err != nil {
		t.Fatal(err)
	}
	internal := map[string]bool{"ErrEmptyQuery": true} // never shown
	msgs := map[string]bool{}
	lit := func(e ast.Expr) (string, bool) {
		if b, ok := e.(*ast.BasicLit); ok && b.Kind == token.STRING {
			s, err := strconv.Unquote(b.Value)
			return s, err == nil
		}
		return "", false
	}
	files := []*ast.File{}
	for _, f := range pkgs["search"].Files {
		files = append(files, f)
	}
	for _, f := range box["phpbox"].Files {
		ast.Inspect(f, func(n ast.Node) bool {
			if x, ok := n.(*ast.ValueSpec); ok {
				for i, name := range x.Names {
					if i < len(x.Values) && (strings.HasPrefix(name.Name, "Reason") || strings.HasPrefix(name.Name, "Msg")) {
						if s, ok := lit(x.Values[i]); ok {
							msgs[s] = true
						}
					}
				}
			}
			return true
		})
	}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "invalid" && len(x.Args) > 0 {
					if s, ok := lit(x.Args[0]); ok {
						msgs[s] = true
					}
				}
			case *ast.ValueSpec:
				for i, name := range x.Names {
					if i >= len(x.Values) {
						continue
					}
					n := name.Name
					if s, ok := lit(x.Values[i]); ok && (strings.HasPrefix(n, "Reason") || strings.HasPrefix(n, "msg") || strings.HasPrefix(n, "label")) {
						msgs[s] = true
					}
					if c, ok := x.Values[i].(*ast.CallExpr); ok && strings.HasPrefix(n, "Err") && !internal[n] {
						if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "New" && len(c.Args) == 1 {
							if s, ok := lit(c.Args[0]); ok {
								msgs[s] = true
							}
						}
					}
				}
			}
			return true
		})
	}
	if len(msgs) < 40 {
		t.Fatalf("only %d messages found", len(msgs))
	}
	ph := regexp.MustCompile(`\{\d\}`)
	for m := range msgs {
		sample := ph.ReplaceAllString(m, "x1")
		for _, lang := range []string{"TCH", "JPN", "GER", "THA"} {
			if got := i18n.T(lang, sample); got == sample {
				t.Errorf("%s: %q is not translated", lang, m)
			}
		}
	}
}
