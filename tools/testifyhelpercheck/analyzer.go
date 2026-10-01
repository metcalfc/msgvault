package testifyhelpercheck

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strings"
	"time"

	"golang.org/x/tools/go/analysis"
)

var Analyzer = &analysis.Analyzer{
	Name: "testifyhelpercheck",
	Doc:  "checks that sub-second testify polling budgets are named",
	Run:  run,
}

func run(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		if !strings.HasSuffix(pass.Fset.Position(file.Pos()).Filename, "_test.go") {
			continue
		}

		checkPollingBudgets(pass, file)
	}

	return nil, nil //nolint:nilnil // go/analysis Run returns (nil result, nil error): this analyzer produces no result fact
}

const pollingBudgetDiagnostic = "%s budget %s is a bare literal below 1s; synchronize in-process work or name a retained integration budget"

var pollingMethods = map[string]struct{}{
	"Eventually": {}, "Eventuallyf": {}, "EventuallyWithT": {},
	"EventuallyWithTf": {}, "Never": {}, "Neverf": {},
}

func checkPollingBudgets(pass *analysis.Pass, file *ast.File) {
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		obj := pass.TypesInfo.Uses[sel.Sel]
		fn, ok := obj.(*types.Func)
		if !ok {
			return true
		}
		if _, ok := pollingMethods[fn.Name()]; !ok || fn.Pkg() == nil {
			return true
		}
		path := fn.Pkg().Path()
		if path != "github.com/stretchr/testify/assert" && path != "github.com/stretchr/testify/require" {
			return true
		}
		index := 2
		if signature, ok := fn.Type().(*types.Signature); ok && signature.Recv() != nil {
			index = 1
		}
		if len(call.Args) <= index {
			return true
		}
		value, ok := bareDuration(pass, call.Args[index])
		if !ok || value >= int64(timeSecond) {
			return true
		}
		assertion := strings.TrimPrefix(path, "github.com/stretchr/testify/") + "." + fn.Name()
		pass.Reportf(call.Args[index].Pos(), pollingBudgetDiagnostic, assertion, time.Duration(value))
		return true
	})
}

const timeSecond = 1_000_000_000

func bareDuration(pass *analysis.Pass, expr ast.Expr) (int64, bool) {
	if _, ok := pass.TypesInfo.Types[expr]; !ok {
		return 0, false
	}
	var allowed func(ast.Expr) bool
	allowed = func(node ast.Expr) bool {
		switch n := node.(type) {
		case *ast.BasicLit:
			return n.Kind == token.INT || n.Kind == token.FLOAT
		case *ast.SelectorExpr:
			ident, ok := n.X.(*ast.Ident)
			if !ok {
				return false
			}
			obj, ok := pass.TypesInfo.Uses[ident].(*types.PkgName)
			return ok && obj.Imported().Path() == "time"
		case *ast.BinaryExpr:
			return allowed(n.X) && allowed(n.Y)
		case *ast.ParenExpr:
			return allowed(n.X)
		case *ast.UnaryExpr:
			return (n.Op == token.ADD || n.Op == token.SUB) && allowed(n.X)
		default:
			return false
		}
	}
	if !allowed(expr) {
		return 0, false
	}
	value := pass.TypesInfo.Types[expr].Value
	if value == nil {
		return 0, false
	}
	if value.Kind() == constant.Float {
		value = constant.ToInt(value)
	}
	if value.Kind() != constant.Int {
		return 0, false
	}
	integer, ok := constant.Int64Val(value)
	return integer, ok
}
