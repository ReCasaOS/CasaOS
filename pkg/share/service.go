package share

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	ErrTOS        = errors.New("the terms of service of Let's Encrypt must be accepted")
	ErrNameSet    = errors.New("this box already has a public name")
	ErrUnknownApp = errors.New("this app is not installed")
	ErrNoRelay    = errors.New("remote access is off: public sharing goes through its relay")
	ErrNoPort     = errors.New("this app has no web port to share")
)

// ErrLabelTaken is a Share refused because another shared app has the same address.
type ErrLabelTaken struct{ Other string }

func (e ErrLabelTaken) Error() string {
	return fmt.Sprintf("%s is already shared at the same address", e.Other)
}

const revokedText = "the tunnel was revoked by the relay operator: share an app again once the operator has restored it"

// Service shares the apps of this box: its name at the relay, the Gateway's host routes, the tunnel and
// the certificate.
type Service struct {
	Root        string
	RuntimePath func() string
	Relay       *Relay
	Gateway     Gateway
	Apps        Apps
	ACME        *ACME
	Geo         *GeoFetcher
	BoxKey      func() (ed25519.PrivateKey, error)
	RelayURL    func() (string, bool)
	Dialer      *websocket.Dialer // nil → Dial's default
	Log         func(msg string, err error)
	Now         func() time.Time
	Alert       func(key, sentence string, problem bool) // nil: no alerts; pkg/alerts' ShareGuardProblem

	// The waits, set by New; tests shorten them.
	backoffMin, backoffMax time.Duration // tunnel redial backoff
	replacedWait           time.Duration // after a 4409
	longSession            time.Duration // a session this long resets the backoff
	retryMin, tick         time.Duration // resync retry after a failure, and resync period
	probeEvery             time.Duration // relay probe of Status

	ops      chan struct{} // held by the operations that change the shares, across their HTTP calls
	kick     chan struct{} // wakes the tunnel loop
	resyncCh chan struct{} // wakes the resync loop
	geoCh    chan struct{} // wakes the DB-IP loop: a protection setting changed

	mu           sync.Mutex // guards share.json and what follows, never held across a network call
	relayReason  string     // "", "relay" or "master": the last answer of the relay
	relayAt      time.Time  // when the relay last answered or was probed, by Now
	connected    bool
	tunnelErr    string
	revoked      bool               // a 4403: no dial until a Share
	cancelTunnel context.CancelFunc // ends the tunnel being dialled or served
	running      bool               // a Run is under way
	guard        *GuardState        // the one copy of share-guard.json, loaded at first use
	guardReset   bool               // share-guard.json was unusable at load: every shared app was closed
	waveApps     map[string]bool    // the apps a wave alert was raised for at the last resync
}

// Default is the box's own; main wires its funcs.
var Default = New("/")

// New keeps the state and the certificate under root.
func New(root string) *Service {
	s := &Service{Root: root, Relay: &Relay{},
		backoffMin: time.Second, backoffMax: 5 * time.Minute, replacedWait: 30 * time.Second, longSession: time.Minute,
		retryMin: 10 * time.Second, tick: 10 * time.Minute, probeEvery: time.Minute,
		ops: make(chan struct{}, 1), kick: make(chan struct{}, 1), resyncCh: make(chan struct{}, 1), geoCh: make(chan struct{}, 1)}
	s.ACME = &ACME{TXT: s.Relay,
		KeyFile:     filepath.Join(root, "var/lib/casaos/tls/share.key"),
		CertFile:    filepath.Join(root, "var/lib/casaos/tls/share.crt"),
		AccountFile: filepath.Join(root, "var/lib/casaos/fleet/acme-account.json")}
	s.Geo = &GeoFetcher{Dir: filepath.Join(root, "var/lib/casaos/geo"), BaseURL: geoBaseURL, Now: s.now}
	runtimePath := func() string {
		if s.RuntimePath == nil {
			return ""
		}
		return s.RuntimePath()
	}
	s.Gateway, s.Apps = Gateway{RuntimePath: runtimePath}, Apps{RuntimePath: runtimePath}
	return s
}

// Status is what the API answers.
type Status struct {
	Supported bool        `json:"supported"`
	Reason    string      `json:"reason"` // remote-access, gateway, relay, master
	Name      string      `json:"name"`
	Host      string      `json:"host"`
	Domain    string      `json:"domain"`
	TOSURL    string      `json:"tos_url"`
	Apps      []AppStatus `json:"apps"`
	Cert      CertView    `json:"cert"`
	Tunnel    TunnelView  `json:"tunnel"`
}

type AppStatus struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Shared bool   `json:"shared"`
	URL    string `json:"url"`
	Port   string `json:"port"`
	Scheme string `json:"scheme"`
	Error  string `json:"error"`
}

type CertView struct {
	NotAfter *time.Time `json:"not_after"`
	Pending  bool       `json:"pending"`
	Error    string     `json:"error"`
}

type TunnelView struct {
	Connected bool   `json:"connected"`
	Error     string `json:"error"`
}

func (s *Service) Status(ctx context.Context) Status {
	st := Status{TOSURL: TOSURL, Apps: []AppStatus{}}
	if s.ACME != nil {
		c := s.ACME.Status()
		if !c.NotAfter.IsZero() {
			st.Cert.NotAfter = &c.NotAfter
		}
		st.Cert.Pending, st.Cert.Error = c.Pending, c.Error
	}
	if _, ok := s.relayURL(); !ok {
		st.Reason = "remote-access"
		return st
	}
	if _, err := s.Gateway.ShareAddr(); err != nil {
		st.Reason = "gateway"
	} else {
		s.probe(ctx)
		s.mu.Lock()
		st.Reason = s.relayReason
		s.mu.Unlock()
	}
	st.Supported = st.Reason == ""
	state := s.read()
	st.Name, st.Host, st.Domain = state.Name, state.Host, state.Domain
	s.mu.Lock()
	st.Tunnel = TunnelView{Connected: s.connected, Error: s.tunnelErr}
	s.mu.Unlock()
	st.Apps = s.appStatus(ctx, state)
	return st
}

// probe asks the relay what it is when its last answer is a minute old.
func (s *Service) probe(ctx context.Context) {
	s.mu.Lock()
	due := s.relayAt.IsZero() || s.now().Sub(s.relayAt) >= s.probeEvery
	if due {
		s.relayAt = s.now()
	}
	s.mu.Unlock()
	if !due || s.Relay == nil || s.Relay.Signed == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := s.Relay.Status(ctx)
	s.noteRelay(err)
}

// noteRelay remembers what an answer of the relay says of its support; an unreachable relay says nothing.
func (s *Service) noteRelay(err error) {
	reason := ""
	switch {
	case errors.Is(err, ErrRelayNoShare):
		reason = "relay"
	case errors.Is(err, ErrWaitMaster):
		reason = "master"
	case err == nil, errors.Is(err, ErrNoName), errors.Is(err, ErrNameTaken), errors.Is(err, ErrNameReserved),
		errors.Is(err, ErrNameRevoked), errors.Is(err, ErrNameQuarantine):
	default:
		return
	}
	s.mu.Lock()
	s.relayReason, s.relayAt = reason, s.now()
	s.mu.Unlock()
}

// appStatus is every installed app, and the shared ones AppManagement does not list (or when it does not answer).
func (s *Service) appStatus(ctx context.Context, state State) []AppStatus {
	installed, _ := s.Apps.List(ctx)
	owner := map[string]string{} // label → the shared app that has it
	for name := range state.Apps {
		if l, err := Label(name); err == nil {
			owner[l] = name
		}
	}
	url := func(label string) string { return "https://" + label + "." + state.Host }
	out := []AppStatus{}
	for _, in := range installed {
		a := AppStatus{Name: in.Name, Port: in.Port, Scheme: in.Scheme}
		_, a.Shared = state.Apps[in.Name]
		label, err := Label(in.Name)
		switch {
		case err != nil:
			a.Error = err.Error()
		case in.Port == "":
			a.Error = ErrNoPort.Error()
		case !a.Shared && owner[label] != "":
			a.Error = ErrLabelTaken{Other: owner[label]}.Error()
		}
		a.Label = label
		if a.Shared {
			a.URL = url(label)
		}
		out = append(out, a)
	}
	for name, app := range state.Apps {
		if !slices.ContainsFunc(installed, func(i Installed) bool { return i.Name == name }) {
			label, _ := Label(name)
			out = append(out, AppStatus{Name: name, Label: label, Shared: true, URL: url(label), Port: app.Port, Scheme: app.Scheme})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SetName claims name at the relay and saves it. The tunnel waits for an app to be shared.
func (s *Service) SetName(ctx context.Context, name string, acceptTOS bool) error {
	if _, ok := s.relayURL(); !ok {
		return ErrNoRelay
	}
	if err := ValidName(name); err != nil {
		return err
	}
	if !acceptTOS {
		return ErrTOS
	}
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()
	if st := s.read(); st.Name != "" {
		_, err := s.Relay.Status(ctx)
		s.noteRelay(err)
		if !errors.Is(err, ErrNoName) {
			return ErrNameSet
		}
		s.deleteHosts(ctx, st) // the relay forgot the name: nothing else would let the owner claim one again
		s.clear()
	}
	info, err := s.Relay.ClaimName(ctx, name)
	s.noteRelay(err)
	if err != nil {
		return err
	}
	err = s.update(func(st *State) bool {
		st.Name, st.Host, st.Domain, st.TOSAcceptedAt = info.Name, info.Host, info.Domain, s.now()
		return true
	})
	if err != nil {
		s.log("share: the relay gave the name but it cannot be saved", err)
	}
	return err
}

// Share publishes an installed app at https://<label>.<host>.
func (s *Service) Share(ctx context.Context, composeName string) error {
	if _, ok := s.relayURL(); !ok {
		return ErrNoRelay
	}
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()
	st := s.read()
	if st.Name == "" {
		return ErrNoName
	}
	installed, err := s.Apps.List(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(installed, func(a Installed) bool { return a.Name == composeName })
	if i < 0 {
		return ErrUnknownApp
	}
	app := installed[i]
	if app.Port == "" {
		return ErrNoPort
	}
	label, err := Label(composeName)
	if err != nil {
		return err
	}
	for other := range st.Apps {
		if l, _ := Label(other); l == label && other != composeName {
			return ErrLabelTaken{Other: other}
		}
	}
	_, wasShared := st.Apps[composeName]
	st.Apps[composeName] = App{Port: app.Port, Scheme: app.Scheme} // st is this call's copy: the config lists the new host
	if err := s.dropStrays(ctx, st); err != nil {
		return err
	}
	if err := s.pushGuardFor(ctx, st); err != nil && (!errors.Is(err, ErrGatewayNoGuard) || s.isCode(composeName)) {
		s.wakeResync() // brings back the code hosts the failed push took off
		return err
	}
	host := appHost(label, st.Host)
	if err := s.Gateway.SetHost(ctx, host, target(app)); err != nil {
		return err
	}
	s.mu.Lock()
	s.revoked, s.tunnelErr = false, "" // the owner acted: dial again
	s.mu.Unlock()
	reset := false
	err = s.update(func(cur *State) bool {
		if cur.Name != st.Name { // reset meanwhile
			reset = true
			return false
		}
		cur.Apps[composeName] = App{Port: app.Port, Scheme: app.Scheme}
		return true
	})
	if reset || (err != nil && !wasShared) { // the host must not outlive a share that did not happen
		if derr := s.Gateway.DeleteHost(ctx, host); derr != nil && !errors.Is(derr, ErrGatewayNoShare) {
			s.log("share: cannot delete a host route", derr)
		}
	}
	if reset {
		return ErrNoName
	}
	if s.ACME != nil {
		s.ACME.Wake()
	}
	return err
}

// Unshare stops publishing an app; an app that is not shared is fine.
func (s *Service) Unshare(ctx context.Context, composeName string) error {
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()
	return s.unshareLocked(ctx, composeName)
}

// unshareLocked takes the app from every person and pushes that before the host goes. The app's own settings go
// last: until the host is gone and share.json says so, a code app must stay a code app.
func (s *Service) unshareLocked(ctx context.Context, composeName string) error {
	st := s.read()
	if _, ok := st.Apps[composeName]; !ok {
		return nil
	}
	s.mu.Lock()
	if g, err := s.guardLocked(); err == nil { // else an unsaved reset: no people left
		for i := range g.People {
			g.People[i].Apps = slices.DeleteFunc(g.People[i].Apps, func(a string) bool { return a == composeName })
		}
		s.saveGuardLocked()
	}
	s.mu.Unlock()
	if err := s.pushGuardFor(ctx, st); err != nil && !errors.Is(err, ErrGatewayNoGuard) {
		s.log("share: cannot push the protection config", err) // the host goes all the same
		s.wakeResync()
	}
	if label, err := Label(composeName); err == nil {
		if err := s.Gateway.DeleteHost(ctx, appHost(label, st.Host)); err != nil && !errors.Is(err, ErrGatewayNoShare) {
			return err
		}
	}
	if err := s.update(func(cur *State) bool {
		if _, ok := cur.Apps[composeName]; !ok {
			return false
		}
		delete(cur.Apps, composeName)
		return true
	}); err != nil {
		return err
	}
	s.mu.Lock()
	if g, err := s.guardLocked(); err == nil {
		delete(g.Apps, composeName)
		s.saveGuardLocked()
	}
	s.mu.Unlock()
	return nil
}

// StopAll unshares every app; the name stays.
func (s *Service) StopAll(ctx context.Context) error {
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()
	var errs []error
	for name := range s.read().Apps {
		errs = append(errs, s.unshareLocked(ctx, name))
	}
	return errors.Join(errs...)
}

// Reset forgets the sharing of a box leaving its relay: the host routes are deleted first (local and fast),
// then the name is released, best effort, then the tunnel stopped and the state cleared. The certificate
// stays. It runs while the fleet holds its relay lock: it only calls the relay's Signed and the Gateway.
func (s *Service) Reset(ctx context.Context) {
	st := s.read()
	if st.Name == "" {
		return
	}
	s.deleteHosts(ctx, st)
	if s.Relay != nil && s.Relay.Signed != nil {
		err := s.Relay.ReleaseName(ctx)
		s.noteRelay(err)
		if err != nil && !errors.Is(err, ErrNoName) {
			s.log("share: the relay did not release the name", err)
		}
	}
	s.clear()
}

// deleteHosts deletes the host route of every app st shares, best effort.
func (s *Service) deleteHosts(ctx context.Context, st State) {
	for name := range st.Apps {
		if label, err := Label(name); err == nil {
			if err := s.Gateway.DeleteHost(ctx, appHost(label, st.Host)); err != nil && !errors.Is(err, ErrGatewayNoShare) {
				s.log("share: cannot delete a host route", err)
			}
		}
	}
}

// clear forgets the name and the shares, and stops the tunnel.
func (s *Service) clear() {
	s.mu.Lock()
	if err := clearState(s.statePath()); err != nil {
		s.log("share: cannot clear the state", err)
	}
	s.revoked, s.tunnelErr = false, ""
	s.mu.Unlock()
	s.changed()
}

// OnAppEvent follows AppManagement (Hub.OnAppEvent). It returns at once.
func (s *Service) OnAppEvent(name string, properties map[string]string) {
	switch name {
	case "app:uninstall-end":
		app := properties["app:name"]
		if app == "" {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := s.Unshare(ctx, app); err != nil {
				s.log("share: cannot unshare an uninstalled app", err) // the next resync drops it
			}
		}()
	case "app:install-end", "app:apply-changes-end":
		s.wakeResync()
	}
}

func (s *Service) wakeResync() {
	select {
	case s.resyncCh <- struct{}{}:
	default:
	}
}

// Run keeps the host routes, the tunnel and the certificate until ctx is done. Without a name and an app
// shared, nothing goes out. A second Run while one is under way returns at once.
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	already := s.running
	s.running = true
	s.mu.Unlock()
	if already {
		return
	}
	defer func() { s.mu.Lock(); s.running = false; s.mu.Unlock() }()
	loops := []func(context.Context){s.resyncLoop, s.tunnelLoop, s.geoLoop}
	if s.ACME != nil {
		loops = append(loops, func(ctx context.Context) { s.ACME.Loop(ctx, s.wildcard, time.Hour) })
	}
	var wg sync.WaitGroup
	for _, loop := range loops {
		wg.Add(1)
		go func() { defer wg.Done(); loop(ctx) }()
	}
	wg.Wait()
}

func (s *Service) wildcard() string {
	if st := s.read(); st.Name != "" && st.Host != "" && len(st.Apps) > 0 {
		return "*." + st.Host
	}
	return ""
}

// resyncLoop resyncs at once, then every tick and on events; a failure is retried sooner.
func (s *Service) resyncLoop(ctx context.Context) {
	retry := s.retryMin
	for {
		wait := s.tick
		if err := s.resync(ctx); err != nil && ctx.Err() == nil {
			s.log("share: the Gateway or AppManagement does not answer yet", err)
			wait, retry = retry, min(retry*2, s.tick)
		} else {
			retry = s.retryMin
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-s.resyncCh:
			t.Stop()
		}
	}
}

// resync unshares the apps that are gone, pushes the protection config, then registers every shared app's host
// again with its current target (the Gateway forgets its hosts at each restart). When the push fails, the hosts of
// code apps stay off the Gateway and the error is returned, to retry sooner. An error leaves the state as it is.
func (s *Service) resync(ctx context.Context) error {
	if len(s.read().Apps) == 0 {
		return nil
	}
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()
	st := s.read()
	if st.Name == "" {
		return nil
	}
	installed, err := s.Apps.List(ctx)
	if err != nil {
		return err
	}
	if len(installed) == 0 { // AppManagement answers an empty list while it loads at boot: not an uninstall
		return errors.New("AppManagement lists no app yet")
	}
	for name := range st.Apps {
		if i := slices.IndexFunc(installed, func(a Installed) bool { return a.Name == name }); i < 0 || installed[i].Port == "" {
			if err := s.unshareLocked(ctx, name); err != nil {
				return err
			}
		}
	}
	st = s.read()
	if err := s.dropStrays(ctx, st); err != nil {
		return err
	}
	pushErr := s.pushGuardFor(ctx, st)
	s.guardAlerts(ctx, st)
	for name, was := range st.Apps {
		i := slices.IndexFunc(installed, func(a Installed) bool { return a.Name == name })
		if i < 0 || installed[i].Port == "" {
			continue
		}
		in := installed[i]
		label, err := Label(name)
		if err != nil {
			continue // never shared under a bad label
		}
		if pushErr != nil && s.isCode(name) {
			continue // the failed push took its host off
		}
		if err := s.Gateway.SetHost(ctx, appHost(label, st.Host), target(in)); err != nil {
			return err
		}
		if now := (App{Port: in.Port, Scheme: in.Scheme}); now != was {
			if err := s.update(func(cur *State) bool {
				if _, ok := cur.Apps[name]; !ok {
					return false
				}
				cur.Apps[name] = now
				return true
			}); err != nil {
				return err
			}
		}
	}
	if errors.Is(pushErr, ErrGatewayNoGuard) {
		return nil // an older Gateway: its open hosts as before, nothing to retry
	}
	return pushErr
}

const guardResetSentence = "The protection settings of shared apps could not be read: every shared app now asks for an access code. Check the people and the apps in Remote access."

// guardAlerts says what the Gateway reports of the protection: ban waves, a missing country file, and a guard file
// that was reset. It runs after each push; a Gateway that cannot be asked says nothing.
func (s *Service) guardAlerts(ctx context.Context, st State) {
	if s.Alert == nil {
		return
	}
	if s.GuardReset() {
		s.Alert("share:guard-reset", guardResetSentence, true)
	}
	gw, supported, err := s.Gateway.GuardStatus(ctx)
	if err != nil || !supported {
		return
	}
	s.mu.Lock()
	g, gerr := s.guardLocked()
	needGeo := false
	if gerr == nil && g.Protection {
		for name := range st.Apps {
			needGeo = needGeo || Effective(g.Box, g.Apps[name].Override).Countries.Mode != "none"
		}
	}
	was := s.waveApps
	s.waveApps = map[string]bool{}
	for _, w := range gw.Waves {
		s.waveApps[w.App] = true
	}
	s.mu.Unlock()
	for _, w := range gw.Waves {
		s.Alert("share:wave:"+w.App, fmt.Sprintf("The shared app %s is under attack: %d addresses were banned in 10 minutes.", w.App, w.Bans), true)
	}
	for app := range was {
		if !s.waveNow(app) {
			s.Alert("share:wave:"+app, "Resolved: the attack on the shared app "+app+" has calmed down.", false)
		}
	}
	if needGeo && !gw.Geo.Country {
		s.Alert("share:geo", "Country filtering of shared apps is not working: the country database could not be downloaded.", true)
	} else {
		s.Alert("share:geo", "Resolved: country filtering of shared apps works again.", false)
	}
}

func (s *Service) waveNow(app string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waveApps[app]
}

// tunnelLoop keeps the tunnel up while a name and an app are shared.
func (s *Service) tunnelLoop(ctx context.Context) {
	backoff := s.backoffMin
	var lastDial time.Time
	for {
		s.mu.Lock()
		wanted := s.wantedLocked()
		if !wanted && !s.revoked {
			s.tunnelErr = ""
		}
		s.mu.Unlock()
		if !wanted {
			select {
			case <-ctx.Done():
				return
			case <-s.kick:
			}
			continue
		}
		if !sleep(ctx, time.Second-time.Since(lastDial), nil) { // a box signature is never reused within a second
			return
		}
		lastDial = time.Now()
		select { // a kick from before this dial is spent: only a later one ends the backoff
		case <-s.kick:
		default:
		}
		if !sleep(ctx, s.tunnelOnce(ctx, &backoff), s.kick) { // a Share or SetName dials again at once
			return
		}
	}
}

// tunnelOnce dials and serves one tunnel, and returns how long to wait before the next.
func (s *Service) tunnelOnce(ctx context.Context, backoff *time.Duration) time.Duration {
	s.mu.Lock()
	if !s.wantedLocked() {
		s.mu.Unlock()
		return 0
	}
	sctx, cancel := context.WithCancel(ctx)
	s.cancelTunnel = cancel
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		s.cancelTunnel, s.connected = nil, false
		s.mu.Unlock()
	}()

	fail := func(err error, retryAfter time.Duration) time.Duration {
		s.setTunnelErr("the tunnel is down: " + err.Error())
		wait := time.Duration(float64(*backoff) * (0.8 + 0.4*rand.Float64()))
		*backoff = min(*backoff*2, s.backoffMax)
		return max(wait, retryAfter)
	}
	relayURL, ok := s.relayURL()
	if !ok {
		return fail(ErrNoRelay, 0)
	}
	key, err := s.boxKey()
	if err != nil {
		return fail(err, 0)
	}
	if _, err := s.Gateway.ShareAddr(); err != nil {
		return fail(err, 0)
	}
	session, err := Dial(sctx, relayURL, key, s.Dialer)
	if err != nil {
		if sctx.Err() != nil {
			return 0
		}
		var de *DialError
		retryAfter := time.Duration(0)
		if errors.As(err, &de) {
			retryAfter = de.RetryAfter
		}
		return fail(err, retryAfter)
	}
	s.mu.Lock()
	s.connected, s.tunnelErr = true, ""
	s.mu.Unlock()
	start := time.Now()
	code := Serve(sctx, session, s.Gateway.ShareAddr)
	s.mu.Lock()
	s.connected = false
	s.mu.Unlock()
	if time.Since(start) >= s.longSession {
		*backoff = s.backoffMin
	}
	switch {
	case sctx.Err() != nil: // stopped here: unshared, reset or shut down
		return 0
	case code == 4403:
		s.mu.Lock()
		s.revoked, s.tunnelErr = true, revokedText
		s.mu.Unlock()
		return 0
	case code == 4409:
		s.setTunnelErr("another connection of this box replaced the tunnel")
		sleep(ctx, s.replacedWait, nil) // not cut short by a kick: the other connection would be replaced in turn
		return 0
	}
	return fail(fmt.Errorf("closed by the relay (code %d)", code), 0)
}

func (s *Service) setTunnelErr(text string) { s.mu.Lock(); s.tunnelErr = text; s.mu.Unlock() }

// wantedLocked is whether the tunnel should run: a name, an app shared, no revocation.
func (s *Service) wantedLocked() bool {
	st := s.readLocked()
	return st.Name != "" && len(st.Apps) > 0 && !s.revoked
}

// changed wakes the tunnel loop, and ends the tunnel at once when it is no longer wanted.
func (s *Service) changed() {
	s.mu.Lock()
	if s.cancelTunnel != nil && !s.wantedLocked() {
		s.cancelTunnel()
	}
	s.mu.Unlock()
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *Service) statePath() string { return filepath.Join(s.Root, "var/lib/casaos/fleet", StateFile) }

func (s *Service) guardPath() string { return filepath.Join(s.Root, "var/lib/casaos/fleet", GuardFile) }

// guardLocked is the protection state, loaded at first use and kept; s.mu is held. A file that cannot be read gives
// every shared app access "code" and an error, writes nothing and keeps nothing: the next call reads again. A file
// read but unusable closes every shared app the same way (defaults would open the protected ones), and that reset is
// written over the file before anything else; its bytes are kept aside only then. When it cannot be written, the
// reset comes back with an error and is not kept: the file stays, so the next start resets again rather than finding
// no file.
func (s *Service) guardLocked() (*GuardState, error) {
	if s.guard != nil {
		return s.guard, nil
	}
	closeAll := func(g *GuardState) {
		for name := range s.readLocked().Apps {
			g.Apps[name] = GuardApp{Access: "code"}
		}
	}
	path := s.guardPath()
	data, err := readGuardFile(path)
	g, corrupt := newGuard(), false
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil: // perhaps passing: never lose the owner's settings over it
		closeAll(&g)
		return &g, fmt.Errorf("the protection settings cannot be read: %w", err)
	default:
		g, corrupt = parseGuard(data)
	}
	if corrupt {
		s.guardReset = true
		closeAll(&g)
		if err := saveGuardFile(path, g); err != nil {
			return &g, fmt.Errorf("the protection settings were reset but cannot be saved: %w", err)
		}
		if err := os.WriteFile(fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix()), data, 0o600); err != nil {
			s.log("share: cannot keep a copy of the unusable protection settings", err)
		}
		s.guard = &g
		return s.guard, nil
	}
	s.guard = &g
	s.saveGuardLocked() // a first start's secret is kept from now on
	return s.guard, nil
}

// readGuardFile is os.ReadFile; tests make it fail.
var readGuardFile = os.ReadFile

// saveGuardFile is saveGuard; tests make it fail.
var saveGuardFile = saveGuard

func (s *Service) saveGuardLocked() {
	if err := saveGuardFile(s.guardPath(), *s.guard); err != nil {
		s.log("share: cannot save the protection settings", err)
	}
}

// GuardReset is whether share-guard.json was unusable at load, so every shared app was closed.
func (s *Service) GuardReset() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.guardLocked()
	return s.guardReset
}

// isCode is whether app asks for a code; with an unsaved reset, every shared app does.
func (s *Service) isCode(app string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, _ := s.guardLocked()
	return accessOf(g.Apps[app]) == "code"
}

// pushGuard sends the Gateway the protection config of what is shared; the ops lock is held.
func (s *Service) pushGuard(ctx context.Context) error { return s.pushGuardFor(ctx, s.read()) }

// pushGuardFor sends the config for the apps st shares. A Gateway without the guard gives ErrGatewayNoGuard. Any
// failure, that one and an unsaved reset too (nothing is pushed then), takes the hosts of st's code apps off the
// Gateway, which cannot ask for their code.
func (s *Service) pushGuardFor(ctx context.Context, st State) error {
	s.mu.Lock()
	g, err := s.guardLocked()
	cfg := buildGatewayGuard(st, *g)
	s.mu.Unlock()
	if err == nil {
		if err = s.Gateway.SetGuard(ctx, cfg); err == nil {
			return nil
		}
	}
	for host, a := range cfg.Apps {
		if a.Access == "code" {
			if err := s.Gateway.DeleteHost(ctx, host); err != nil && !errors.Is(err, ErrGatewayNoShare) {
				s.log("share: cannot take a protected app off the Gateway", err)
			}
		}
	}
	return err
}

// dropStrays deletes the Gateway's hosts under the box's share domain that st does not share. A Reset or a new
// name deletes hosts best effort, and a host left behind would drop out of the pushed config, so be served open.
// It runs before the push, so such a host is never served while missing from it.
func (s *Service) dropStrays(ctx context.Context, st State) error {
	domain := st.Domain
	if domain == "" {
		domain = st.Host
	}
	suffix := "." + strings.TrimSuffix(strings.ToLower(domain), ".")
	if suffix == "." {
		return nil
	}
	hosts, err := s.Gateway.Hosts(ctx)
	if errors.Is(err, ErrGatewayNoShare) {
		return nil
	}
	if err != nil {
		return err
	}
	shared := map[string]bool{}
	for name := range st.Apps {
		if label, err := Label(name); err == nil {
			shared[appHost(label, st.Host)] = true
		}
	}
	for _, h := range hosts {
		if strings.HasSuffix(strings.ToLower(h), suffix) && !shared[strings.ToLower(h)] {
			if err := s.Gateway.DeleteHost(ctx, h); err != nil && !errors.Is(err, ErrGatewayNoShare) {
				return err
			}
		}
	}
	return nil
}

// buildGatewayGuard is the Gateway's config for the apps st shares, open ones too (an absent host would keep the
// access cookie unstripped), each under its effective policy; people keep only the shared apps.
func buildGatewayGuard(st State, g GuardState) GatewayGuardConfig {
	cfg := GatewayGuardConfig{Secret: g.Secret, Protection: g.Protection, Apps: map[string]GatewayGuardApp{}, People: []GatewayPerson{}}
	for name := range st.Apps {
		label, err := Label(name)
		if err != nil {
			continue // never shared under a bad label
		}
		a := g.Apps[name]
		cfg.Apps[appHost(label, st.Host)] = GatewayGuardApp{App: name, Access: accessOf(a), Policy: Effective(g.Box, a.Override)}
	}
	for _, p := range g.People {
		apps := []string{}
		for _, name := range p.Apps {
			if _, ok := st.Apps[name]; ok {
				apps = append(apps, name)
			}
		}
		cfg.People = append(cfg.People, GatewayPerson{ID: p.ID, Gen: p.Gen, CodeHash: p.CodeHash, Apps: apps})
	}
	return cfg
}

// accessOf is "open" when unset, and "code" for anything but "open".
func accessOf(a GuardApp) string {
	if a.Access == "" || a.Access == "open" {
		return "open"
	}
	return "code"
}

// appHost is the host the Gateway serves an app on, normalised as the Gateway keys it.
func appHost(label, host string) string {
	return strings.TrimSuffix(strings.ToLower(label+"."+host), ".")
}

func (s *Service) read() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked()
}

// readLocked is the saved state, with an Apps map; nothing saved is the zero State.
func (s *Service) readLocked() State {
	st, _ := loadState(s.statePath())
	if st.Apps == nil {
		st.Apps = map[string]App{}
	}
	return st
}

// update saves what change makes of the state, unless it returns false.
func (s *Service) update(change func(*State) bool) error {
	s.mu.Lock()
	st := s.readLocked()
	if !change(&st) {
		s.mu.Unlock()
		return nil
	}
	err := saveState(s.statePath(), st)
	s.mu.Unlock()
	s.changed()
	return err
}

// lock takes the operations' turn, or gives up with ctx.
func (s *Service) lock(ctx context.Context) error {
	select {
	case s.ops <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) unlock() { <-s.ops }

func (s *Service) relayURL() (string, bool) {
	if s.RelayURL == nil {
		return "", false
	}
	return s.RelayURL()
}

func (s *Service) boxKey() (ed25519.PrivateKey, error) {
	if s.BoxKey == nil {
		return nil, errors.New("no box key")
	}
	return s.BoxKey()
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log(msg string, err error) {
	if s.Log != nil {
		s.Log(msg, err)
	}
}

func target(a Installed) string { return a.Scheme + "://127.0.0.1:" + a.Port }

// sleep waits d, or less when wake (nil: never) receives; false once ctx is done.
func sleep(ctx context.Context, d time.Duration, wake <-chan struct{}) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	case <-wake:
		return true
	}
}
