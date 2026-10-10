package service

import (
	"runtime"
	"time"

	commonfile "github.com/ReCasaOS/CasaOS-Common/utils/file"
	"github.com/ReCasaOS/CasaOS/pkg/ossupport"
)

// GetSystemOS is the operating system the box runs and how long its security updates last. It reads
// /etc/os-release and nothing else: no apt, no network, no lock.
func (s *systemService) GetSystemOS() ossupport.Report {
	return systemOS(commonfile.ReadOSRelease, runtime.GOARCH, time.Now())
}

// systemOS is GetSystemOS with what it reads and what day it is given. A file that cannot be read
// is an operating system that is not known: the answer says so, and is not an error.
func systemOS(readOSRelease func() (map[string]string, error), arch string, now time.Time) ossupport.Report {
	release, err := readOSRelease()
	if err != nil {
		release = nil
	}
	return ossupport.Describe(release, arch, now)
}
