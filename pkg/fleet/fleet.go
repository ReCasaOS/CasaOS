// Package fleet lets one box be the master of the owner's other boxes: each
// member keeps an outbound tunnel to the master, and the master relays an
// allow-listed set of each box's own API routes so one dashboard sees and
// acts on all of them.
package fleet

import (
	"crypto/tls"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/constants"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
)

// Fleet holds both roles of one box. Its func fields are what main wires and
// tests replace.
type Fleet struct {
	Root        string                                 // "/" on a box; a temp dir in tests
	RuntimePath func() string                          // where internal.secret lives
	Now         func() time.Time                       // the clock of what is recorded
	Log         func(msg string, err error)            // one line when a file cannot be read
	Gateway     func() (GatewayInfo, error)            // where this box's gateway listens
	Hostname    func() (string, error)                 // this box's name, said in the hello
	Version     func() string                          // this box's ReCasaOS version, said in the hello
	Offline     func(boxID, name string, offline bool) // the alerts hub's hook: a box gone, or back

	mu      sync.Mutex         // master.json, member.json and tunnels
	tunnels map[string]*tunnel // the live tunnel of each box, by id
	restart chan struct{}      // poked when member.json changes
	m       memberState        // what the member loop reports
	offline map[string]bool    // the boxes an offline alert is up for
	started time.Time          // when the offline watch first ran: its grace period starts here
	local   *http.Client       // how a member reaches its own gateway
}

// New makes a Fleet rooted at root, with the box's defaults.
func New(root string) *Fleet {
	return &Fleet{
		Root:        root,
		RuntimePath: func() string { return constants.DefaultRuntimePath },
		Now:         time.Now,
		Log:         func(msg string, err error) { logger.Error(msg, zap.Error(err)) },
		Gateway: func() (GatewayInfo, error) {
			return ReadGatewayINI("/etc/casaos/gateway.ini", constants.DefaultDataPath)
		},
		Hostname: os.Hostname,
		Version:  func() string { return "" },
		Offline:  func(string, string, bool) {},
		offline:  map[string]bool{},
		tunnels:  map[string]*tunnel{},
		restart:  make(chan struct{}, 1),
		// The box's own gateway over loopback: its self-signed certificate is not
		// for 127.0.0.1, and nothing leaves the box. A redirect is not followed:
		// it is returned as it is, so the internal secret never leaves loopback.
		local: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Transport:     &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}}, //nolint:gosec // see above
		},
	}
}

// Default is this box's fleet, wired by main.
var Default = New("/")

// pokeMember tells the member loop that member.json changed.
func (f *Fleet) pokeMember() {
	select {
	case f.restart <- struct{}{}:
	default:
	}
}
