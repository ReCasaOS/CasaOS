package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
	"time"
)

// ErrOtherNetwork is returned by Net.Up when the box is already connected to a
// private network other than the one asked for. The box is left untouched.
var ErrOtherNetwork = errors.New("this box is already in another private network")

// ErrNoPrivateNet is returned when the box has no private-network driver.
var ErrNoPrivateNet = errors.New("the private network is not available on this box")

// ErrNoSystemctl: tailscale is there, but not systemctl, which starts tailscaled.
var ErrNoSystemctl = errors.New("the private network cannot be started on this box: systemctl was not found")

// defaultControlURL is what the tailscale client reports when no login server
// was ever set.
const defaultControlURL = "https://controlplane.tailscale.com"

// netTimeout bounds one Net call, also under a caller's longer deadline.
const netTimeout = 45 * time.Second

// NetState is what the box's private network looks like right now.
type NetState struct {
	Connected bool
	Addr      string // this box's address on the private network
	// LoginServer is the coordination server the box is logged in to, without a
	// trailing slash, connected or not (a stopped tailscale is still in its
	// network); empty when the box is in none.
	LoginServer string
	// RelayUnreachable: connected, with an address, but the coordination server
	// does not answer (tailscale reports this node offline). Links already made
	// may carry on; nothing new can join.
	RelayUnreachable bool
	// NodeID is this node's id at the coordination server (Headscale's numeric id, as a
	// decimal string); empty when not running.
	NodeID string
}

// Net drives the box's private network.
type Net interface {
	State(ctx context.Context) (NetState, error)
	// Up joins the network of loginServer. It returns ErrOtherNetwork if the
	// box is already in another one, leaving it as it was. On any error, Up has
	// undone what it did itself: the caller has nothing to take down.
	Up(ctx context.Context, loginServer, authKey, hostname string) error
	// Down leaves the network and stops the daemon. Only for a network this box
	// joined itself, the one of relay: a login to any other server is left alone
	// (an empty relay logs out whatever the box is logged in to). It returns nil
	// only once the box is logged out of that network, or was in none.
	Down(ctx context.Context, relay string) error
}

// PrivateNet returns the box's private-network driver, or ErrNoPrivateNet when
// Fleet.Net is nil.
func (f *Fleet) PrivateNet() (Net, error) {
	if f.Net == nil {
		return nil, ErrNoPrivateNet
	}
	return f.Net, nil
}

// tailscale is Net over the system tailscaled, driven by the tailscale command.
type tailscale struct {
	run       func(ctx context.Context, args ...string) ([]byte, error) // the tailscale command: its stdout
	systemctl func(ctx context.Context, args ...string) error           // starts and stops the tailscaled unit
}

// NewTailscale returns a Net over the tailscale command. A nil run executes
// the real command.
func NewTailscale(run func(ctx context.Context, args ...string) ([]byte, error)) Net {
	if run == nil {
		run = func(ctx context.Context, args ...string) ([]byte, error) { return runCmd(ctx, "tailscale", args...) }
	}
	return &tailscale{run: run, systemctl: func(ctx context.Context, args ...string) error {
		_, err := runCmd(ctx, "systemctl", args...)
		return err
	}}
}

// runCmd returns stdout only; a failure carries the tail of stderr, never the
// arguments (one of them may be a key).
func runCmd(ctx context.Context, name string, args ...string) ([]byte, error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		tail := strings.TrimSpace(errb.String())
		if len(tail) > 200 {
			tail = tail[len(tail)-200:]
		}
		return out.Bytes(), fmt.Errorf("%s %s: %w: %s", name, args[0], err, tail)
	}
	return out.Bytes(), nil
}

func withNetTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, netTimeout) // the sooner of the two deadlines
}

// State reads `tailscale status --json`. A daemon that is stopped, not
// logged in or not running is not an error: it is just not connected. A
// missing command or unreadable output is.
func (t *tailscale) State(ctx context.Context) (NetState, error) {
	ctx, cancel := withNetTimeout(ctx)
	defer cancel()
	out, err := t.run(ctx, "status", "--json")
	if errors.Is(err, exec.ErrNotFound) || ctx.Err() != nil {
		return NetState{}, firstErr(ctx.Err(), err)
	}
	var st struct {
		BackendState string
		TailscaleIPs []string
		Self         struct {
			ID           string
			TailscaleIPs []string
			Online       *bool // absent on a client that does not say
		}
	}
	if jerr := json.Unmarshal(out, &st); jerr != nil {
		if err != nil { // a non-zero exit with no JSON: tailscaled is not running
			return NetState{}, nil
		}
		return NetState{}, errors.New("tailscale status: unreadable output")
	}
	switch st.BackendState {
	case "", "NoState": // no daemon state at all
		return NetState{}, nil
	}
	// Any other state (Running, Stopped, Starting, NeedsLogin...) may be logged in
	// somewhere. The status JSON does not carry the coordination server; the prefs
	// do. A box whose server cannot be read is an error: it must not look like a
	// free one.
	praw, err := t.run(ctx, "debug", "prefs")
	var prefs struct {
		ControlURL string
		LoggedOut  bool
	}
	if err != nil || json.Unmarshal(praw, &prefs) != nil {
		return NetState{}, errors.New("tailscale debug prefs: cannot read the login server")
	}
	// NeedsLogin is both a box logged out and one whose node key expired, or whose
	// login was never finished: the second kind is still in a network, the owner's
	// perhaps, and is not free to take. The prefs tell them apart: LoggedOut is what
	// `tailscale logout` and a daemon never used leave (with no ControlURL, or the
	// old one when the logout could not reach its server: Stopped then).
	if prefs.LoggedOut {
		return NetState{}, nil
	}
	if prefs.ControlURL == "" {
		prefs.ControlURL = defaultControlURL
	}
	ns := NetState{LoginServer: trimURL(prefs.ControlURL)}
	if st.BackendState == "Running" {
		ns.Connected, ns.Addr = true, pickAddr(append(st.Self.TailscaleIPs, st.TailscaleIPs...))
		ns.RelayUnreachable = st.Self.Online != nil && !*st.Self.Online
		ns.NodeID = st.Self.ID
	}
	return ns, nil
}

// Up starts tailscaled and joins loginServer. It does nothing if the box is
// already joined there, and refuses if it is in another network. The no-op
// cannot tell two fleets on one relay apart: it is for joining the same fleet
// again, and a caller switching fleets (ChangeRelay, Join to another relay)
// calls Down first.
func (t *tailscale) Up(ctx context.Context, loginServer, authKey, hostname string) error {
	if loginServer == "" || authKey == "" {
		return errors.New("tailscale up: a login server and a key are needed")
	}
	if err := validJoinKey(authKey); err != nil { // checked again here: the key goes on tailscale's command line
		return err
	}
	ctx, cancel := withNetTimeout(ctx)
	defer cancel()
	st, err := t.State(ctx)
	if err != nil {
		return err
	}
	if st.LoginServer != "" && !sameURL(st.LoginServer, loginServer) {
		return ErrOtherNetwork
	}
	if st.Connected && st.Addr != "" {
		return nil
	}
	restore, err := t.start(ctx)
	if errors.Is(err, ErrNoSystemctl) {
		return err
	}
	if err != nil {
		return redact(err, authKey)
	}
	// A stopped daemon answered nothing: only now can it say it is logged in
	// elsewhere (tailscaled is Type=notify: started means answering).
	st, err = t.State(ctx)
	switch {
	case err != nil:
		restore()
		return err
	case st.LoginServer != "" && !sameURL(st.LoginServer, loginServer):
		restore()
		return ErrOtherNetwork
	case st.Connected && st.Addr != "":
		return nil
	}
	args := []string{"up", "--login-server=" + loginServer, "--authkey=" + authKey,
		"--accept-dns=false", "--accept-routes=false", "--timeout=30s"}
	if hostname != "" {
		args = append(args, "--hostname="+hostname)
	}
	if _, err := t.run(ctx, args...); err != nil {
		if st.LoginServer == "" { // logged in nowhere before: a half-done login goes
			c, cancel := cleanupCtx(ctx)
			t.run(c, "logout")
			cancel()
		}
		restore()
		return redact(err, authKey)
	}
	return nil
}

// start starts tailscaled and returns what puts it back as it was: nothing
// when it was running already; else stopped again, and disabled again unless
// it was enabled. Best-effort: a failure there leaves only a daemon running.
func (t *tailscale) start(ctx context.Context) (restore func(), err error) {
	switch err := t.sys(ctx, "is-active", "--quiet", "tailscaled"); {
	case err == nil:
		return func() {}, nil
	case errors.Is(err, ErrNoSystemctl):
		return nil, err
	}
	undo := []string{"disable", "--now", "tailscaled"}
	if t.sys(ctx, "is-enabled", "--quiet", "tailscaled") == nil {
		undo = []string{"stop", "tailscaled"}
	}
	restore = func() {
		c, cancel := cleanupCtx(ctx)
		defer cancel()
		t.sys(c, undo...)
	}
	if err := t.sys(ctx, "enable", "--now", "tailscaled"); err != nil {
		restore() // enabled, perhaps, but not started
		return nil, err
	}
	return restore, nil
}

// sys runs systemctl; a missing one is ErrNoSystemctl, never exec.ErrNotFound,
// which says tailscale is missing.
func (t *tailscale) sys(ctx context.Context, args ...string) error {
	err := t.systemctl(ctx, args...)
	if errors.Is(err, exec.ErrNotFound) {
		return ErrNoSystemctl
	}
	return err
}

// cleanupCtx is for undoing: it outlives ctx's cancellation and deadline.
func cleanupCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
}

// Down starts tailscaled, logs out and stops it. A stopped tailscaled cannot
// log out, and its login would stay on disk for the next Up to find, so it is
// started first; the login it then shows is read, and left alone (the daemon
// put back as it was) unless it is relay's, since a stopped daemon hid whose it
// was. It fails when the box is still logged in afterwards, which Up would take
// for the box being in the new network already; a failed stop alone does not.
func (t *tailscale) Down(ctx context.Context, relay string) error {
	ctx, cancel := withNetTimeout(ctx)
	defer cancel()
	restore, serr := t.start(ctx) // nil on a failure: a daemon that stays down reads as logged out
	if relay != "" {
		st, err := t.State(ctx)
		if err != nil || (st.LoginServer != "" && !sameURL(st.LoginServer, relay)) {
			if restore != nil {
				restore()
			}
			return err // nil: another login, not ours to log out; else whose it is stays unknown
		}
	}
	_, lerr := t.run(ctx, "logout") // fails when already logged out
	if lerr != nil {
		if st, err := t.State(ctx); serr != nil || err != nil || st.LoginServer != "" {
			t.sys(ctx, "disable", "--now", "tailscaled")
			return fmt.Errorf("still logged in to the private network: %w", lerr)
		}
	}
	t.sys(ctx, "disable", "--now", "tailscaled") // a daemon left running is harmless
	return nil
}

func trimURL(s string) string { return strings.TrimRight(s, "/") }

// pickAddr prefers the IPv4 (100.x) address.
func pickAddr(ips []string) string {
	for _, s := range ips {
		if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
			return s
		}
	}
	if len(ips) > 0 {
		return ips[0]
	}
	return ""
}

// redact rewrites err without the key; the error chain is dropped on purpose
// then. An error that does not quote the key is kept as it is, so the caller
// can still tell it (ErrNoSystemctl, ErrOtherNetwork).
func redact(err error, key string) error {
	if key == "" || !strings.Contains(err.Error(), key) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), key, "***"))
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
