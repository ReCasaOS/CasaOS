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

// The fleet is wired in main.go, where no test sees it run: its routes are created at the gateway (or every call of the dashboard
// to /v1/fleet gets the static server's 404), its loop runs (or a member never dials its master), and its offline alert reaches the hub.
func TestTheFleetIsWiredIntoTheCore(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "../main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	routed, runs, alerted := false, false, false
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range n.Lhs {
				if i >= len(n.Rhs) {
					break
				}
				if id, ok := lhs.(*ast.Ident); ok && id.Name == "routers" {
					if lit, ok := n.Rhs[i].(*ast.CompositeLit); ok {
						for _, e := range lit.Elts {
							if b, ok := e.(*ast.BasicLit); ok && b.Value == `"/v1/fleet"` {
								routed = true
							}
						}
					}
				}
				if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Offline" && isFleetDefault(sel.X) {
					if r, ok := n.Rhs[i].(*ast.SelectorExpr); ok && r.Sel.Name == "FleetOffline" {
						alerted = true
					}
				}
			}
		case *ast.GoStmt:
			if sel, ok := n.Call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Run" && isFleetDefault(sel.X) {
				runs = true
			}
		}
		return true
	})
	if !routed {
		t.Error(`main does not create the route "/v1/fleet" at the gateway`)
	}
	if !runs {
		t.Error("main does not start fleet.Default.Run")
	}
	if !alerted {
		t.Error("main does not hand the fleet's offline alert to the hub")
	}
}

// isFleetDefault says whether e is fleet.Default.
func isFleetDefault(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Default" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "fleet"
}
