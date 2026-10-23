package route

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// The core's routes are created at the gateway in main.go, where no test sees them run: they wait
// for a gateway that is down at start like the core's first look at it does, instead of dying.
func TestTheRoutesWaitForTheGateway(t *testing.T) {
	if !calledIn(t, "main")["RetryGateway"] {
		t.Error("main creates its routes without waiting for the gateway")
	}
	// and the registration of the event types stops at the same deadline as the waits for the gateway
	file, err := parser.ParseFile(token.NewFileSet(), "../main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	deadline := false
	for _, declaration := range file.Decls {
		if fn, ok := declaration.(*ast.FuncDecl); ok && fn.Name.Name == "main" {
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if s, ok := n.(*ast.SelectorExpr); ok && s.Sel.Name == "StartDeadline" {
					deadline = true
				}
				return true
			})
		}
	}
	if !deadline {
		t.Error("main registers its event types on a clock of its own")
	}
}
