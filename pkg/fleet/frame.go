package fleet

import "time"

// frame is one message of the tunnel: the member's hello, the master's req,
// the member's res. Bodies travel base64 (encoding/json does it for []byte).
// Only the master sends req frames; a member only ever sends hello and res.
type frame struct {
	T         string            `json:"t"` // "hello" | "req" | "res"
	ID        string            `json:"id,omitempty"`
	Hostname  string            `json:"hostname,omitempty"`   // hello
	Version   string            `json:"version,omitempty"`    // hello
	WebScheme string            `json:"web_scheme,omitempty"` // hello: the dashboard's scheme
	WebPort   string            `json:"web_port,omitempty"`   // hello: and port
	BoxKey    string            `json:"box_key,omitempty"`    // hello: the box's public key, base64url raw ed25519; only over a relay
	NodeID    string            `json:"node_id,omitempty"`    // hello: the box's node id on the relay's private network
	Method    string            `json:"method,omitempty"`     // req
	Path      string            `json:"path,omitempty"`       // req
	Query     string            `json:"query,omitempty"`      // req
	Headers   map[string]string `json:"headers,omitempty"`    // req and res
	Body      []byte            `json:"body,omitempty"`       // req and res
	Status    int               `json:"status,omitempty"`     // res
}

// maxFrame bounds one message: a MaxBody body in base64, and room for the rest.
const maxFrame = MaxBody/3*4 + 64<<10

const (
	pingEvery   = 30 * time.Second
	readWait    = 60 * time.Second
	writeWait   = 10 * time.Second
	helloWait   = 10 * time.Second
	maxInFlight = 16
	maxName     = 255 // a hostname or version from a hello is cut here
)
