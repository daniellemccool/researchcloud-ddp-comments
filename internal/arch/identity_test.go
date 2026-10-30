package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// identityNames are the identity inputs that only internal/access may read
// (ADR-0003).
var identityNames = []string{"REMOTE_USER", "X-Desk-Roles", "X-Desk-Gate"}

// excludedDirs are the directories the rule does not apply to, relative to
// the repository root.
var excludedDirs = []string{"internal/access", "internal/arch"}

func TestIdentityHeadersReadOnlyInAccess(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	var violations []string

	for _, top := range []string{"cmd", "internal"} {
		dir := filepath.Join(root, top)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				for _, ex := range excludedDirs {
					if rel == ex {
						return filepath.SkipDir
					}
				}
				return nil
			}
			if !strings.HasSuffix(rel, ".go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			violations = append(violations, findIdentityReads(fset, file)...)
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", top, err)
		}
	}

	for _, v := range violations {
		t.Error(v)
	}
}

// findIdentityReads reports every string literal in file that names an
// identity input, noting the Get calls among them.
func findIdentityReads(fset *token.FileSet, file *ast.File) []string {
	var out []string
	getCalls := map[*ast.BasicLit]bool{}

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Get" {
			return true
		}
		if lit, ok := call.Args[0].(*ast.BasicLit); ok && isIdentityLiteral(lit) {
			getCalls[lit] = true
		}
		return true
	})

	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || !isIdentityLiteral(lit) {
			return true
		}
		what := "string literal"
		if getCalls[lit] {
			what = "Get call with"
		}
		out = append(out, fset.Position(lit.Pos()).String()+": "+what+" "+lit.Value+
			" (identity is read only in internal/access, ADR-0003)")
		return true
	})
	return out
}

func isIdentityLiteral(lit *ast.BasicLit) bool {
	if lit.Kind != token.STRING {
		return false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return false
	}
	// HTTP header names are case-insensitive, so every casing counts.
	for _, name := range identityNames {
		if strings.EqualFold(s, name) {
			return true
		}
	}
	return false
}

func TestFindIdentityReads(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string // substrings, one per expected violation
	}{
		{"get REMOTE_USER", `h.Get("REMOTE_USER")`, []string{`Get call with "REMOTE_USER"`}},
		{"header get roles", `r.Header.Get("X-Desk-Roles")`, []string{`Get call with "X-Desk-Roles"`}},
		{"lowercase gate", `r.Header.Get("x-desk-gate")`, []string{`Get call with "x-desk-gate"`}},
		{"uppercase gate", `r.Header.Get("X-DESK-GATE")`, []string{`Get call with "X-DESK-GATE"`}},
		{"const", `const name = "X-Desk-Gate"`, []string{`string literal "X-Desk-Gate"`}},
		{"raw string", "var name = `REMOTE_USER`", []string{"string literal `REMOTE_USER`"}},
		{"clean", `r.Header.Get("Accept")`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The parser does not type-check, so the receivers need no types.
			src := "package x\n\nfunc f() {\n\t_ = " + c.body + "\n}\n"
			if strings.HasPrefix(c.body, "const") || strings.HasPrefix(c.body, "var") {
				src = "package x\n\n" + c.body + "\n"
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "x.go", src, 0)
			if err != nil {
				t.Fatal(err)
			}
			got := findIdentityReads(fset, file)
			if len(got) != len(c.want) {
				t.Fatalf("violations = %v, want %d", got, len(c.want))
			}
			for i, w := range c.want {
				if !strings.Contains(got[i], w) || !strings.Contains(got[i], "x.go:") {
					t.Errorf("violation %q lacks %q or file:line", got[i], w)
				}
			}
		})
	}
}
