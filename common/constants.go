package common

const (
	SERVICENAME          = "casaos"
	BODY                 = " "
	RANW_NAME            = "IceWhale-RemoteAccess"
	FORK_RELEASE_VERSION = "v0.4.95"
	FORK_RELEASE_FILE    = "/var/lib/casaos/fork-release"
	FORK_VERSION_URL     = "https://github.com/ReCasaOS/CasaOS-Install/releases/latest/download/version.json"
	FORK_UPDATE_URL      = "https://github.com/ReCasaOS/CasaOS-Install/releases/latest/download/install.sh"
)

// VERSION is this component's own version, and the release build stamps it with the
// tag it is building: `-X github.com/ReCasaOS/CasaOS/common.VERSION={{.Version}}`.
//
// It is a var for exactly that reason. As a constant it stayed at the version the
// fork was taken from, so every box answered `casaos -v` with 0.4.15 while running a
// binary built years later -- and the line at the end of an install read "ReCasaOS
// v0.4.15, distribution v0.4.94". The value below is what an unstamped build says:
// the tag this file was last released under, not a promise about the binary.
//
// Not to be confused with FORK_RELEASE_VERSION above, which is the DISTRIBUTION this
// binary ships in. The two move on their own: a distribution release need not ship
// this component.
var VERSION = "0.4.56"
