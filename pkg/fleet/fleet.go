// Package fleet lets one box be the master of the owner's other boxes: each
// member keeps an outbound tunnel to the master, and the master relays an
// allow-listed set of each box's own API routes so one dashboard sees and
// acts on all of them.
package fleet

import (
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/constants"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
)

// Fleet holds both roles of one box. Its func fields are what main wires and
// tests replace.
type Fleet struct {
	Root        string             // "/" on a box; a temp dir in tests
	RuntimePath func() string      // where internal.secret lives
	Now         func() time.Time   // the clock
	Log         func(msg string, err error)
}

// New makes a Fleet rooted at root, with the box's defaults.
func New(root string) *Fleet {
	return &Fleet{
		Root:        root,
		RuntimePath: func() string { return constants.DefaultRuntimePath },
		Now:         time.Now,
		Log:         func(msg string, err error) { logger.Error(msg, zap.Error(err)) },
	}
}

// Default is this box's fleet, wired by main.
var Default = New("/")
