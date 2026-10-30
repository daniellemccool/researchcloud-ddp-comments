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
var identityNames = map[string]bool{
	"REMOTE_USER":  true,
	"X-Desk-Roles": true,
	"X-Desk-Gate":  true,
}

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
	return err == nil && identityNames[s]
}
