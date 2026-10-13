//go:build !linux

package service

import (
	"context"
	"errors"
	"io/fs"

	"github.com/ReCasaOS/CasaOS/pkg/osplan"
)

// Run: a plan is made on a Linux host, which has the process groups and the apt it runs.
func (s *osPlanSandbox) Run(context.Context, []string) (osplan.Output, error) {
	return osplan.Output{}, errors.New("a plan is made on Linux only")
}

// ownerAndDevice: nothing is known of who owns a file where plans are not made, so no directory is taken for the core's.
func ownerAndDevice(fs.FileInfo) (uid, dev uint64, ok bool) { return 0, 0, false }
