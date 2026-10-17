package modulecmd

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestOnlyTheModuleWordRightAfterTheProgramReachesTheAssistant(t *testing.T) {
	yes := [][]string{
		{"casaos", "module"},
		{"casaos", "module", "list"},
		{"/usr/bin/casaos", "module", "install", "virt", "--yes"},
		{"casaos", "module", "-c", "x"},
	}
	for _, args := range yes {
		rest, ok := Dispatch(args)
		if !ok || !slices.Equal(rest, args[2:]) {
			t.Errorf("%q: %q %v", args, rest, ok)
		}
	}
	no := [][]string{
		nil, {}, {"casaos"},
		{"casaos", "-c", "module"}, {"casaos", "-c", "x", "module", "list"}, {"casaos", "-v"}, {"casaos", "-db", "module"},
		{"casaos", "--module"}, {"casaos", "-module"}, {"casaos", "-module=list"}, {"casaos", "Module"}, {"casaos", "MODULE"}, {"casaos", "modules"}, {"casaos", "mod"}, {"casaos", "module "},
		{"casaos", " module"}, {"casaos", "module\x00"}, {"casaos", "module=list"}, {"casaos", "modul"}, {"casaos", ""}, {"casaos", "./module"}, {"module"}, {"module", "list"},
		{"casaos", "list", "module"}, {"casaos", "os-upgrade", "module"},
	}
	for _, args := range no {
		if rest, ok := Dispatch(args); ok || rest != nil {
			t.Errorf("%q reaches the assistant: %q", args, rest)
		}
	}
}

// no other shape reaches the module code: every command line whose second word is not exactly "module" is the core's, whatever else is in it
func TestNoOtherShapeIsTheAssistants(t *testing.T) {
	words := []string{"module", "Module", "-module", "--module", "list", "install", "-c", "x", "", "modules", "mod", "module\n", "\tmodule"}
	var walk func(prefix []string, depth int)
	walk = func(prefix []string, depth int) {
		args := append([]string{"casaos"}, prefix...)
		rest, ok := Dispatch(args)
		if want := len(prefix) > 0 && prefix[0] == "module"; ok != want {
			t.Errorf("%q: %v", args, ok)
		} else if ok && !slices.Equal(rest, prefix[1:]) {
			t.Errorf("%q: rest %q", args, rest)
		}
		if depth == 0 {
			return
		}
		for _, w := range words {
			walk(append(slices.Clone(prefix), w), depth-1)
		}
	}
	walk(nil, 3)
}

// the core's main reads the dispatch before it parses its flags, so that a start of the core without the word is what it was
func TestTheCoreDispatchesBeforeItParsesItsFlags(t *testing.T) {
	raw, err := os.ReadFile("../../main.go")
	if err != nil {
		t.Skip("the source of the core is not here")
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	initAt := strings.Index(text, "func init() {")
	call := strings.Index(text, "modulecmd.Dispatch(os.Args)")
	parse := strings.Index(text, "flag.Parse()")
	if initAt < 0 || call < initAt || parse < call {
		t.Fatalf("init %d, dispatch %d, flag.Parse %d", initAt, call, parse)
	}
	// the dispatch is the first thing the init does, and it ends the process
	if head := text[initAt:parse]; !strings.Contains(head, "os.Exit(modulecmd.Run(") {
		t.Errorf("before flag.Parse:\n%s", head)
	}
	// nothing else of the core knows the assistant: the dispatch, the run and the production wiring
	if n := strings.Count(text, "modulecmd."); n != 3 {
		t.Errorf("the core mentions the assistant %d times", n)
	}
}
