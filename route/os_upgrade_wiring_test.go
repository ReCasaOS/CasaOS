package route

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// calledIn lists the names of the methods and functions that a function of ../main.go calls, wherever in its body.
func calledIn(t *testing.T, function string) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "../main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range file.Decls {
		fn, ok := declaration.(*ast.FuncDecl)
		if !ok || fn.Name.Name != function {
			continue
		}
		names := map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				switch f := call.Fun.(type) {
				case *ast.SelectorExpr:
					names[f.Sel.Name] = true
				case *ast.Ident:
					names[f.Name] = true
				}
			}
			return true
		})
		return names
	}
	t.Fatalf("../main.go has no function %s", function)
	return nil
}

// calledAtTheTopOf lists the calls that are statements of the body of a function of ../main.go itself, and not in a branch, a loop or a closure of it: a call that is there runs
// every time the function does.
func calledAtTheTopOf(t *testing.T, function string) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "../main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range file.Decls {
		fn, ok := declaration.(*ast.FuncDecl)
		if !ok || fn.Name.Name != function {
			continue
		}
		names := map[string]bool{}
		for _, statement := range fn.Body.List {
			stmt, ok := statement.(*ast.ExprStmt)
			if !ok {
				continue
			}
			if call, ok := stmt.X.(*ast.CallExpr); ok {
				if f, ok := call.Fun.(*ast.SelectorExpr); ok {
					names[f.Sel.Name] = true
				}
			}
		}
		return names
	}
	t.Fatalf("../main.go has no function %s", function)
	return nil
}

// The upgrade of the operating system is wired in main.go and nowhere a test can see it run: the loop that looks at an open upgrade starts when the core is ready and ends first when the
// core is told to stop, the owner is told through the alerts, and the alerts keep quiet about the containers while the upgrade holds the machine. A line lost from main.go would leave
// every other test green and the product with an upgrade that nothing unwinds, checks or tells.
func TestTheUpgradeOfTheOperatingSystemIsWiredIntoTheCore(t *testing.T) {
	inMain := calledIn(t, "main")
	for _, want := range []string{"StartSystemOSUpgradeSettle", "SetOSUpgradeNotifier", "OSUpgradeNotice", "OSUpgradeMuted", "DockerUpdateMuted"} {
		if !inMain[want] {
			t.Errorf("main does not call %s", want)
		}
	}
	if !calledIn(t, "stopThePlanWhenTheCoreIsToldToStop")["StopSystemOSUpgradeSettle"] {
		t.Error("the core told to stop does not end the loop that looks at the upgrade")
	}
	// and the two that matter most are not behind a condition: a loop that starts only sometimes is a product with an upgrade that nothing watches
	if !calledAtTheTopOf(t, "main")["StartSystemOSUpgradeSettle"] {
		t.Error("main starts the loop that looks at the upgrade only under a condition, in a loop or in a closure")
	}
	if !calledAtTheTopOf(t, "stopThePlanWhenTheCoreIsToldToStop")["StopSystemOSUpgradeSettle"] {
		t.Error("the core told to stop ends the loop that looks at the upgrade only under a condition")
	}
}
