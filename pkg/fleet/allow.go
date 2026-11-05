package fleet

import (
	"regexp"
	"strings"
	"time"
)

// MaxBody bounds a relayed request or response body, in each direction.
const MaxBody = 4 << 20

// Timeout is how long the master waits for a box to answer one relayed request.
func Timeout(slow bool) time.Duration {
	if slow {
		return 300 * time.Second
	}
	return 60 * time.Second
}

// The table of the routes a master may relay to a box, and nothing else. A
// `{id}` segment matches exactly one non-empty segment; `{action}` one of the
// four VM actions. Extending the table is one row here and one in its test.
var allowed = []struct {
	method, pattern string
	slow            bool
}{
	{"GET", "/v1/sys/version/check", false},
	{"GET", "/v1/sys/os", false},
	{"GET", "/v1/sys/utilization", false},
	{"GET", "/v1/sys/modules", false},
	{"GET", "/v1/sys/autoupdate", false},
	{"GET", "/v1/sys/packages", true},
	{"GET", "/v1/sys/packages/update/status", false},
	{"GET", "/v1/sys/docker/update/status", false},
	{"GET", "/v2/casaos/health/services", false},
	{"GET", "/v2/app_management/compose", false},
	{"GET", "/v2/app_management/web/appgrid", false},
	{"GET", "/v2/app_management/operations", false},
	{"GET", "/v2/app_management/compose/updates", true},
	{"GET", "/v2/app_management/compose/updates/run", false},
	{"GET", "/v2/app_management/logs/apps", false},
	{"GET", "/v2/app_management/logs", false},
	{"GET", "/v2/app_management/logs/events", false},
	{"GET", "/v2/app_management/logs/export", true},
	{"GET", "/v2/virt_management/vms", false},
	{"POST", "/v1/sys/update", false},
	{"POST", "/v1/sys/packages/update", false},
	{"PUT", "/v2/app_management/compose/{id}/status", false},
	{"PATCH", "/v2/app_management/compose/{id}", false},
	{"POST", "/v2/app_management/compose/updates", false},
	{"POST", "/v2/app_management/image-updates", true},
	{"POST", "/v2/virt_management/vms/{id}/{action}", false},
}

var vmActions = map[string]bool{"start": true, "shutdown": true, "reboot": true, "force-off": true}

// Allowed says whether method+path (the path alone, no query) is in the
// table, and whether it is one of the slow routes. The path is compared as it
// is: no decoding, no trimming, no case folding, so the table cannot be
// reached around. A path carrying a character a URL reads as a delimiter or an
// escape (? # % \ or a control byte) is refused outright: an {id} that held one
// would turn a row of the table into another route once a request is built from
// it.
func Allowed(method, path string) (slow, ok bool) {
	if strings.ContainsAny(path, "?#%\\") || strings.IndexFunc(path, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return false, false
	}
	segs := strings.Split(path, "/")
	for _, row := range allowed {
		if row.method == method && match(strings.Split(row.pattern, "/"), segs) {
			return row.slow, true
		}
	}
	return false, false
}

// idSegment is what an {id} may be: one segment, starting alphanumeric, then
// letters, digits, dot, dash or underscore. Never "." or "..", which a path
// resolves away, and never empty.
var idSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func match(pattern, segs []string) bool {
	if len(pattern) != len(segs) {
		return false
	}
	for i, p := range pattern {
		switch p {
		case "{id}":
			if !idSegment.MatchString(segs[i]) || segs[i] == ".." {
				return false
			}
		case "{action}":
			if !vmActions[segs[i]] {
				return false
			}
		default:
			if p != segs[i] {
				return false
			}
		}
	}
	return true
}
