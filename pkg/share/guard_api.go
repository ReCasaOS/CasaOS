package share

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ErrGuard is a refusal the contract names: gateway_unsupported, bad_name, not_shared, not_found,
// unknown_app:<name>, bad_policy:<field>. Its message is the code.
type ErrGuard struct{ Code string }

func (e ErrGuard) Error() string { return e.Code }

// ErrGuardPush is a change that is saved but that the Gateway has not taken: the resync pushes it again.
var ErrGuardPush = errors.New("the change is saved, but the Gateway has not taken it yet: it will be sent again")

// ErrRelayPush is a relay setting that is saved but that the relay has not taken: the resync sends it again.
var ErrRelayPush = errors.New("the change is saved, but the relay has not taken it yet: it will be sent again")

var errUnsupported = ErrGuard{"gateway_unsupported"}

// GuardView is the body of GET /v1/share/guard.
type GuardView struct {
	Supported  bool                    `json:"supported"`
	Reason     string                  `json:"reason"`
	Reset      bool                    `json:"reset"`
	Protection bool                    `json:"protection"`
	Box        Policy                  `json:"box"`
	Apps       map[string]GuardAppView `json:"apps"` // the shared apps only
	People     []PersonView            `json:"people"`
	Geo        GuardGeo                `json:"geo"`
	Waves      []GuardWave             `json:"waves"`
	Relay      GuardRelay              `json:"relay"`
}

// GuardRelay is the relay's part of the protection: the settings are the owner's (the file), the numbers the relay's
// last answer, zero while it has none or has no such routes.
type GuardRelay struct {
	Supported        bool       `json:"supported"`
	Filter           bool       `json:"filter"`
	ShareBans        bool       `json:"share_bans"`
	ShareBansAt      *time.Time `json:"share_bans_at"`
	CommunitySize    int        `json:"community_size"`
	ContributedMonth int        `json:"contributed_month"`
}

type GuardAppView struct {
	Access    string          `json:"access"`
	Override  *PolicyOverride `json:"override"`
	Effective Policy          `json:"effective"`
}

// PersonView is a person without the hash of the code.
type PersonView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Apps       []string   `json:"apps"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type GuardGeo struct {
	Country   bool       `json:"country"`
	ASN       bool       `json:"asn"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	Error     string     `json:"error"`
}

// GuardView is the protection as the dashboard shows it. It takes no ops lock; a Gateway that cannot be asked gives
// the view without its last uses and waves. A guard file that cannot be read is an error, not a view of the placeholder.
func (s *Service) GuardView(ctx context.Context) (GuardView, error) {
	st := s.read()
	s.mu.Lock()
	g, err := s.guardLocked()
	if err != nil {
		s.mu.Unlock()
		return GuardView{}, err
	}
	v := GuardView{Reason: "", Reset: !g.ResetAt.IsZero(), Protection: g.Protection, Box: g.Box, Apps: map[string]GuardAppView{},
		People: make([]PersonView, 0, len(g.People)), Waves: []GuardWave{}}
	for name := range st.Apps {
		a := g.Apps[name]
		v.Apps[name] = GuardAppView{Access: accessOf(a), Override: a.Override, Effective: Effective(g.Box, a.Override)}
	}
	v.Relay = GuardRelay{Supported: s.relayOK != nil && *s.relayOK, Filter: g.relayFilter(), ShareBans: g.ShareBans,
		CommunitySize: s.relayInfo.CommunitySize, ContributedMonth: s.relayInfo.ContributedMonth}
	if !g.ShareBansAt.IsZero() {
		at := g.ShareBansAt
		v.Relay.ShareBansAt = &at
	}
	people := slices.Clone(g.People)
	for i := range people {
		people[i].Apps = slices.Clone(people[i].Apps) // unshareLocked edits the saved ones in place
	}
	s.mu.Unlock()
	gw, supported, gerr := s.Gateway.GuardStatus(ctx)
	v.Supported = supported
	if !supported {
		v.Reason = errUnsupported.Code
	}
	if gerr != nil {
		gw = GuardGatewayStatus{}
	}
	for _, p := range people {
		v.People = append(v.People, personView(p, st, gw.PeopleLastUsed))
	}
	v.Geo = GuardGeo{Country: gw.Geo.Country, ASN: gw.Geo.ASN} // what the Gateway loaded
	gs := s.Geo.Status()
	v.Geo.Error = gs.Error
	if !gs.UpdatedAt.IsZero() {
		v.Geo.UpdatedAt = &gs.UpdatedAt
	}
	if gw.Waves != nil {
		v.Waves = gw.Waves
	}
	return v, nil
}

// personView keeps the shared apps only: an app that is not shared any more is not shown.
func personView(p Person, st State, lastUsed map[string]time.Time) PersonView {
	v := PersonView{ID: p.ID, Name: p.Name, Apps: []string{}, CreatedAt: p.CreatedAt}
	for _, a := range p.Apps {
		if _, ok := st.Apps[a]; ok {
			v.Apps = append(v.Apps, a)
		}
	}
	if t, ok := lastUsed[p.ID]; ok && !t.IsZero() {
		v.LastUsedAt = &t
	}
	return v
}

// guardWrite runs a change of the guard state under the ops lock: change edits a copy, which is saved and then
// kept, so memory never runs ahead of the disk (change replaces nested slices, never edits them). The push follows.
// A Gateway without the guard refuses the whole write, before the save; one that refuses the config (404 or 400)
// undoes it; one that fails the push leaves the change saved and asks the resync to send it again. A write the
// Gateway takes ends a reset of the guard file.
func (s *Service) guardWrite(ctx context.Context, change func(g *GuardState, st State) error) error {
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()
	if _, supported, _ := s.Gateway.GuardStatus(ctx); !supported {
		return errUnsupported
	}
	st := s.read()
	s.mu.Lock()
	g, err := s.guardLocked()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	prev := *g
	next := prev
	next.Apps, next.People = maps.Clone(prev.Apps), slices.Clone(prev.People)
	if err := change(&next, st); err != nil {
		s.mu.Unlock()
		return err
	}
	if err := s.storeGuardLocked(next); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("the protection settings cannot be saved: %w", err)
	}
	s.mu.Unlock()
	select { // protection or a countries mode may have just come on: fetch the files now
	case s.geoCh <- struct{}{}:
	default:
	}
	err = s.pushGuardFor(ctx, st)
	var refused ErrGuard
	switch {
	case err == nil:
		s.endReset()
		return nil
	case errors.Is(err, ErrGatewayNoGuard), errors.As(err, &refused): // the Gateway changed under us, or refused: the change does not stay
		s.mu.Lock()
		defer s.mu.Unlock()
		if serr := s.storeGuardLocked(prev); serr != nil {
			return fmt.Errorf("the protection settings cannot be saved: %w", serr) // the disk keeps the change: so does memory
		}
		if refused.Code != "" {
			s.wakeResync() // the Gateway kept the previous config: the resync brings back the code hosts the push took off
			return refused
		}
		return errUnsupported
	}
	s.log("share: cannot push the protection config", err)
	s.wakeResync()
	return ErrGuardPush
}

// endReset clears a saved reset of the guard file, and resolves its alert.
func (s *Service) endReset() {
	s.mu.Lock()
	g, err := s.guardLocked()
	if err != nil || g.ResetAt.IsZero() {
		s.mu.Unlock()
		return
	}
	next := *g
	next.ResetAt = time.Time{}
	err = s.storeGuardLocked(next)
	s.mu.Unlock()
	if err != nil {
		s.log("share: cannot save the protection settings", err) // the reset stays: the next write tries again
		return
	}
	if s.Alert != nil {
		s.Alert("share:guard-reset", "Resolved: the protection settings of shared apps were saved again.", false)
	}
}

func (s *Service) SetProtection(ctx context.Context, on bool) (GuardView, error) {
	if err := s.guardWrite(ctx, func(g *GuardState, _ State) error { g.Protection = on; return nil }); err != nil {
		return GuardView{}, err
	}
	return s.GuardView(ctx)
}

// SetBoxPolicy replaces the box's policy; absent lists are empty ones.
func (s *Service) SetBoxPolicy(ctx context.Context, p Policy) (GuardView, error) {
	p.RulesOff, p.Allow, p.Block, p.Countries.List = union(p.RulesOff, nil), union(p.Allow, nil), union(p.Block, nil), union(p.Countries.List, nil)
	if err := s.guardWrite(ctx, func(g *GuardState, _ State) error {
		if err := p.Validate(); err != nil {
			return ErrGuard{err.Error()}
		}
		g.Box = p
		return nil
	}); err != nil {
		return GuardView{}, err
	}
	return s.GuardView(ctx)
}

// SetAppGuard sets who may enter a shared app ("open" or "code") and its own settings (nil: follow the box).
func (s *Service) SetAppGuard(ctx context.Context, name, access string, o *PolicyOverride) (GuardView, error) {
	if err := s.guardWrite(ctx, func(g *GuardState, st State) error {
		if _, ok := st.Apps[name]; !ok {
			return ErrGuard{"not_shared"}
		}
		if access != "open" && access != "code" {
			return ErrGuard{"bad_policy:access"}
		}
		if err := Effective(g.Box, o).Validate(); err != nil {
			return ErrGuard{err.Error()}
		}
		if o != nil {
			c := *o
			c.RulesOff, c.Allow, c.Block = union(c.RulesOff, nil), union(c.Allow, nil), union(c.Block, nil)
			if c.Countries != nil {
				cc := Countries{Mode: c.Countries.Mode, List: union(c.Countries.List, nil)}
				c.Countries = &cc
			}
			o = &c
		}
		g.Apps[name] = GuardApp{Access: access, Override: o}
		return nil
	}); err != nil {
		return GuardView{}, err
	}
	return s.GuardView(ctx)
}

// validPerson is the trimmed name and the apps without repeats, or the contract's refusal.
func validPerson(name string, apps []string, st State) (string, []string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 40 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", nil, ErrGuard{"bad_name"}
	}
	for _, a := range apps {
		if _, ok := st.Apps[a]; !ok {
			return "", nil, ErrGuard{"unknown_app:" + a}
		}
	}
	return name, union(apps, nil), nil
}

// AddPerson adds a person with a first code, which this answer is the only place to show.
func (s *Service) AddPerson(ctx context.Context, name string, apps []string) (PersonView, string, error) {
	var p Person
	code := NewCode()
	err := s.guardWrite(ctx, func(g *GuardState, st State) error {
		name, apps, err := validPerson(name, apps, st)
		if err != nil {
			return err
		}
		id := newPersonID()
		for slices.ContainsFunc(g.People, func(o Person) bool { return o.ID == id }) {
			id = newPersonID()
		}
		p = Person{ID: id, Name: name, Apps: apps, CodeHash: CodeHash(g.Secret, code), Gen: 1, CreatedAt: s.now().UTC().Truncate(time.Second)}
		g.People = append(g.People, p)
		return nil
	})
	if err != nil && !errors.Is(err, ErrGuardPush) { // saved all the same: the resync sends it, and the code must not be lost
		return PersonView{}, "", err
	}
	return personView(p, s.read(), nil), code, nil
}

// changePerson applies edit to the person id.
func (s *Service) changePerson(ctx context.Context, id string, edit func(p *Person, g *GuardState, st State) error) (Person, error) {
	var out Person
	err := s.guardWrite(ctx, func(g *GuardState, st State) error {
		i := slices.IndexFunc(g.People, func(p Person) bool { return p.ID == id })
		if i < 0 {
			return ErrGuard{"not_found"}
		}
		p := g.People[i] // a copy: the saved one is replaced, not edited
		if err := edit(&p, g, st); err != nil {
			return err
		}
		g.People[i], out = p, p
		return nil
	})
	return out, err
}

func (s *Service) UpdatePerson(ctx context.Context, id, name string, apps []string) (PersonView, error) {
	p, err := s.changePerson(ctx, id, func(p *Person, _ *GuardState, st State) error {
		var err error
		p.Name, p.Apps, err = validPerson(name, apps, st)
		return err
	})
	if err != nil {
		return PersonView{}, err
	}
	gw, _, _ := s.Gateway.GuardStatus(ctx) // zero when it cannot be asked
	return personView(p, s.read(), gw.PeopleLastUsed), nil
}

// NewPersonCode replaces a person's code: the old one stops working, and the new one is shown here only.
func (s *Service) NewPersonCode(ctx context.Context, id string) (string, error) {
	code := NewCode()
	_, err := s.changePerson(ctx, id, func(p *Person, g *GuardState, _ State) error {
		p.CodeHash, p.Gen = CodeHash(g.Secret, code), p.Gen+1
		return nil
	})
	if err != nil && !errors.Is(err, ErrGuardPush) { // as AddPerson: the saved code is shown
		return "", err
	}
	return code, nil
}

func (s *Service) DeletePerson(ctx context.Context, id string) error {
	return s.guardWrite(ctx, func(g *GuardState, _ State) error {
		g.People = slices.DeleteFunc(g.People, func(p Person) bool { return p.ID == id })
		return nil
	})
}

// GuardEvents is the Gateway's journal, newest first, filtered by compose name and reason.
func (s *Service) GuardEvents(ctx context.Context, app, reason string, limit int) (json.RawMessage, error) {
	out, err := s.Gateway.GuardEvents(ctx, app, reason, limit)
	return out, asGuardErr(err)
}

// GuardBans is the Gateway's active bans.
func (s *Service) GuardBans(ctx context.Context) (json.RawMessage, error) {
	out, err := s.Gateway.GuardBans(ctx)
	return out, asGuardErr(err)
}

// Unban lifts the ban on key, as the router gave it (unescaped once): the Gateway call escapes it again.
func (s *Service) Unban(ctx context.Context, key string) error {
	return asGuardErr(s.Gateway.Unban(ctx, key))
}

func asGuardErr(err error) error {
	if errors.Is(err, ErrGatewayNoGuard) {
		return errUnsupported
	}
	return err
}

// SetRelay changes the relay's two settings: filtering, and sharing this box's bans (which needs the owner's consent).
// A nil one is left as it is. The change is saved, then sent to the relay at once; a relay without the routes (too old)
// undoes it, and one that cannot take it yet (failing, or waiting for the master) leaves it for the resync to send.
func (s *Service) SetRelay(ctx context.Context, filter, shareBans *bool, consent bool) (GuardView, error) {
	if filter == nil && shareBans == nil {
		return GuardView{}, ErrGuard{"bad_request"}
	}
	if shareBans != nil && *shareBans && !consent {
		return GuardView{}, ErrGuard{"consent_required"}
	}
	if _, ok := s.relayURL(); !ok {
		return GuardView{}, ErrNoRelay
	}
	if err := s.setRelayLocked(ctx, filter, shareBans); err != nil {
		return GuardView{}, err
	}
	if s.wakeReports != nil {
		s.wakeReports()
	}
	return s.GuardView(ctx)
}

func (s *Service) setRelayLocked(ctx context.Context, filter, shareBans *bool) error {
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()
	s.mu.Lock()
	if s.relayOK != nil && !*s.relayOK {
		s.mu.Unlock()
		return ErrGuard{"relay_unsupported"}
	}
	g, err := s.guardLocked()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	prev := *g
	next := prev
	if filter != nil {
		next.RelayFilter = filter
	}
	if shareBans != nil {
		switch {
		case *shareBans && !next.ShareBans:
			next.ShareBansAt = s.now().UTC()
		case !*shareBans:
			next.ShareBansAt = time.Time{}
		}
		next.ShareBans = *shareBans
	}
	if err := s.storeGuardLocked(next); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("the protection settings cannot be saved: %w", err)
	}
	s.mu.Unlock()
	switch err := s.pushRelay(ctx); {
	case err == nil:
		return nil
	case errors.Is(err, ErrRelayNoShare): // as on a relay known old: the change does not stay
		s.mu.Lock()
		defer s.mu.Unlock()
		if serr := s.storeGuardLocked(prev); serr != nil {
			return fmt.Errorf("the protection settings cannot be saved: %w", serr)
		}
		return ErrGuard{"relay_unsupported"}
	case errors.Is(err, ErrWaitMaster):
		return ErrWaitMaster
	default:
		s.log("share: cannot send the relay protection settings", err)
		s.wakeResync()
		return ErrRelayPush
	}
}

// RelayRefusals is what the relay refused at its door for this box's name over a week; a relay without the route gives [].
func (s *Service) RelayRefusals(ctx context.Context) (json.RawMessage, error) {
	if _, ok := s.relayURL(); !ok || s.Relay.Signed == nil {
		return nil, ErrNoRelay
	}
	out, err := s.Relay.Refusals(ctx)
	if errors.Is(err, ErrRelayNoShare) {
		return json.RawMessage("[]"), nil
	}
	return out, err
}
