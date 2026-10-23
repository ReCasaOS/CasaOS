//go:build !linux

package service

import (
	"context"
	"errors"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
)

// Run: a plan is made on a Linux host, which has the process groups and the apt it runs.
func (s *osPlanSandbox) Run(context.Context, []string) (osplan.Output, error) {
	return osplan.Output{}, errors.New("a plan is made on Linux only")
}
