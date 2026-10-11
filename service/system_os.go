package service

import (
	"os"
	"runtime"
	"time"

	commonfile "github.com/ReCasaOS/CasaOS-Common/utils/file"
	"github.com/ReCasaOS/CasaOS/pkg/ossupport"
)

// GetSystemOS is the operating system the box runs and how long its security updates last. It reads
// /etc/os-release, and looks for the files that tell a variant (Raspberry Pi OS, Proxmox...) from the
// plain distribution: no apt, no network, no lock.
func (s *systemService) GetSystemOS() ossupport.Report {
	return systemOS(readOSRelease, markerExists, runtime.GOARCH, time.Now())
}

// What GetSystemOS reads, as vars, so that a test can say what is on the box: os-release, and the
// files that tell a variant.
var (
	readOSRelease = commonfile.ReadOSRelease
	markerExists  = pathExists
)

// pathExists says whether path exists on this box, whatever it is.
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// systemOS is GetSystemOS with what it reads and what day it is given. A file that cannot be read
// is an operating system that is not known: the answer says so, and is not an error.
func systemOS(read func() (map[string]string, error), exists func(path string) bool, arch string, now time.Time) ossupport.Report {
	release, err := read()
	if err != nil {
		release = nil
	}
	return ossupport.Describe(release, arch, now, exists)
}
