package osreadiness

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/ossupport"
)

// A HopID names a step from one release to the next, as the table of pkg/ossupport has it: the
// vendor, the release left and the release reached. A check that applies to some hops names them.
type HopID string

// The hops of the decision of 2026-10-11. A hop that the table of pkg/ossupport gains is a HopID
// of the same shape, and the checks that name no hop apply to it.
const (
	HopDebian11to12     HopID = "debian-11-12"
	HopDebian12to13     HopID = "debian-12-13"
	HopUbuntu2204to2404 HopID = "ubuntu-22.04-24.04"
	HopUbuntu2404to2604 HopID = "ubuntu-24.04-26.04"
)

// The hops by vendor, for the checks that are about one family of releases.
var (
	debianHops = []HopID{HopDebian11to12, HopDebian12to13}
	ubuntuHops = []HopID{HopUbuntu2204to2404, HopUbuntu2404to2604}
)

// Release is a release of the operating system, as the report shows it.
type Release struct {
	ID         string `json:"id,omitempty"`
	VersionID  string `json:"version_id,omitempty"`
	Codename   string `json:"codename,omitempty"`
	PrettyName string `json:"pretty_name,omitempty"`
}

// Hop is the step the upgrade would take.
type Hop struct {
	From Release `json:"from"`
	To   Release `json:"to"`

	id HopID
}

// ID is the name of the hop.
func (h Hop) ID() HopID { return h.id }

// Session is what the engine found out about the box before it ran the checks, and gives to every
// check in its context (SessionOf): who the box is and the hop it would take.
type Session struct {
	Identity ossupport.Identity
	// Hop is the step the box would take; nil when the table has none for it.
	Hop *Hop
	// Latest is a box on the newest release the table knows, which has nothing to upgrade to.
	Latest bool
	// ReleaseErr is why os-release could not be read, when it could not; MarkerErr is why a file
	// that tells a variant could not be looked at (it answered something else than "not there").
	ReleaseErr, MarkerErr error
}

type sessionKey struct{}

// SessionOf is the session of the report a check runs in; the zero Session (nothing known) when
// there is none.
func SessionOf(ctx context.Context) Session {
	s, _ := ctx.Value(sessionKey{}).(Session)
	return s
}

func withSession(ctx context.Context, s Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

// HopOf is the id of the box's hop; "" when it has none.
func (s Session) HopOf() HopID {
	if s.Hop == nil {
		return ""
	}
	return s.Hop.id
}

// identifyWithin is identify under the rules of every check: it has the time of a check and what is
// left of the report's, and a panic in it, or no answer in time, is a session that says it could not
// look (the refusals of unsupported_os and vendor_os, that do not know, and say why). The read that
// does not stop for its context is left behind, as the body of a check that does not is: the report
// does not wait for it, and neither does the request that comes next.
func identifyWithin(ctx context.Context, p Probe, timeout time.Duration) Session {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ictx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan Session, 1) // room for the answer of a step that is no longer waited for
	go func() {
		defer func() {
			if recover() != nil {
				done <- Session{ReleaseErr: errPanicked, MarkerErr: errPanicked}
			}
		}()
		done <- identify(ictx, p)
	}()
	select {
	case session := <-done:
		// what was read is what there is, whenever it came: a read that its context cut short says so
		// in the session's errors
		return session
	case <-ictx.Done():
		return Session{ReleaseErr: ictx.Err(), MarkerErr: ictx.Err()}
	}
}

// identify reads who the box is: os-release (and the files that tell a variant), through the
// Probe, and the table of pkg/ossupport for what follows.
func identify(ctx context.Context, p Probe) Session {
	var session Session
	release, err := readOSRelease(ctx, p)
	if err != nil {
		session.ReleaseErr = err
		release = nil
	}
	marker := func(path string) bool {
		_, err := p.Stat(ctx, path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) && session.MarkerErr == nil {
			session.MarkerErr = err
		}
		return err == nil
	}
	report := ossupport.Describe(release, p.Arch(), p.Now(), marker)
	session.Identity = report.Identity
	switch {
	case report.Next != nil:
		session.Hop = &Hop{
			From: Release{ID: report.ID, VersionID: report.VersionID, Codename: report.Codename, PrettyName: report.PrettyName},
			To:   Release{ID: report.Next.ID, VersionID: report.Next.VersionID, Codename: report.Next.Codename, PrettyName: report.Next.PrettyName},
			id:   HopID(fmt.Sprintf("%s-%s-%s", report.ID, report.VersionID, report.Next.VersionID)),
		}
	case report.Known && (report.Support.State == ossupport.Supported || report.Support.State == ossupport.EndingSoon):
		// A release the table knows, that has no release after it, and whose end the table still
		// believes: the newest one. A table that has gone stale, and a release that has ended
		// with no step offered from it (Ubuntu 20.04), are not "latest": nothing is claimed.
		session.Latest = true
	}
	return session
}

// readOSRelease is os-release as a map; /usr/lib/os-release when /etc/os-release is not there.
func readOSRelease(ctx context.Context, p Probe) (map[string]string, error) {
	data, err := p.ReadFile(ctx, "/etc/os-release")
	if errors.Is(err, fs.ErrNotExist) {
		data, err = p.ReadFile(ctx, "/usr/lib/os-release")
	}
	if err != nil {
		return nil, err
	}
	return parseOSRelease(string(data)), nil
}

// parseOSRelease reads the KEY=value lines of os-release(5): values may be quoted, and a line that
// is not KEY=value is nothing.
func parseOSRelease(text string) map[string]string {
	release := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		release[strings.TrimSpace(key)] = value
	}
	return release
}
