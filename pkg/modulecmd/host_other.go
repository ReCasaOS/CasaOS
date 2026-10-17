//go:build !linux

package modulecmd

import (
	"context"
	"errors"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
)

// Off Linux the assistant runs nothing: the host refuses, the runner refuses and there is no sandbox. The package compiles here so that its rules can be tested.
func newHost() Host { return noHost{} }

type noRunner struct{}

func (noRunner) Run(context.Context, Cmd) (Output, error) { return Output{}, ErrUnsupported }

func newRunner(Roots) Runner { return noRunner{} }

func newSandbox(Roots) func() osplan.Sandbox { return nil }

var _ = errors.New
