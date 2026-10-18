package modulecmd

import (
	"context"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
)

// the file the commands keep apt's list of the automatically installed packages in is in the sandbox, empty, before either of them runs
type checkingSandbox struct {
	*fakeSandbox
	t       *testing.T
	checked int
}

func (s *checkingSandbox) Run(ctx context.Context, argv []string) (osplan.Output, error) {
	s.checked++
	if content, ok := s.content[osplan.ModuleStatesFile(s.dir)]; !ok || content != "" {
		s.t.Errorf("the list of the automatically installed packages when %q runs: %q (written: %v)", argv[len(argv)-1], content, ok)
	}
	return s.fakeSandbox.Run(ctx, argv)
}

func TestTheSimulationHasItsOwnListOfAutomaticallyInstalledPackages(t *testing.T) {
	f := newFixture(t)
	cs := &checkingSandbox{fakeSandbox: f.sandbox, t: t}
	f.env.Sandbox = func() osplan.Sandbox { return cs }
	if got := f.json("plan", "virt"); got["refused"] != nil {
		t.Fatalf("refused: %v", got["refused"])
	}
	if cs.checked != 2 {
		t.Errorf("the commands that were run: %d", cs.checked)
	}
}
