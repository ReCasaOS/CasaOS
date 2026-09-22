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

// VERSION is this component's own version. The release build stamps it with the tag
// it builds, in .github/workflows/release.yml:
// -X github.com/ReCasaOS/CasaOS/common.VERSION=${RELEASE_TAG#v}.
//
// It is a var for that reason. As a constant it stayed at the version the fork was
// taken from, so every box answered `casaos -v` with 0.4.15. The first fix stamped
// it in .goreleaser.yaml, which no release uses: the value then came from this
// default, which happened to equal the tag it shipped with, and the next release
// showed the previous number. So the default names nothing: a build that is not
// stamped says 0.0.0-dev, and the install check compares `casaos -v` with the tag
// the distribution pins.
//
// Not to be confused with FORK_RELEASE_VERSION above, which is the DISTRIBUTION this
// binary ships in. The two move on their own: a distribution release need not ship
// this component.
var VERSION = "0.0.0-dev"
