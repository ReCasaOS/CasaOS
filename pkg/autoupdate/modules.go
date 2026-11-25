package autoupdate

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"go.uber.org/zap"
)

// Modules is what the night can do with the optional modules; main wires it to the service.
type Modules interface {
	// List is the installed modules that have an update. An error, or a
	// catalogue that cannot be read, is no list: nothing is changed.
	List(ctx context.Context) ([]ModuleUpdate, error)
	// Update updates a module to the tag List gave and waits for its job to end.
	Update(ctx context.Context, name string) (Outcome, string)
}

// Outcome is how a module's update ended.
type Outcome int

const (
	Updated    Outcome = iota // the job ran and ended well
	Failed                    // the job started and ended badly, or still ran at the wait's bound
	NotStarted                // refused before anything began: busy, plan refused or timed out, could not launch
)

// ModuleUpdate is an installed module and the later tag the catalogue has. Foreign is a module written outside
// ReCasaOS, and Allowed says the owner of the box lets the night update it: a foreign module that is not allowed is
// left alone.
type ModuleUpdate struct {
	Name, Installed, Latest string
	Foreign, Allowed        bool
}

// checkModules is the modules' part of a round, with the release's switch and
// window. A module written outside ReCasaOS takes part only when its owner
// allowed it. Each tag is first seen while the switch is on, in or out of the
// window, and is installed minAge later, inside the window, once: a failure
// waits for another tag, a refusal before anything began does not count. One module at a time, each to the end of its job; the
// round stops as soon as the box is busy or the window closes.
func (a *AutoUpdate) checkModules(ctx context.Context) {
	if a.Modules == nil || !a.load().Enabled {
		return
	}
	list, err := a.Modules.List(ctx)
	if err != nil {
		logger.Info("automatic update: the modules cannot be listed", zap.Error(err))
		return
	}
	// a module the owner did not allow is not even seen: no trace in autoupdate.json, and its wait starts at his yes
	list = slices.DeleteFunc(list, func(m ModuleUpdate) bool { return m.Foreign && !m.Allowed })
	var due []ModuleUpdate
	if err := a.modify(func(s *State) bool {
		var changed bool
		due, changed = s.seeModules(list, a.Now())
		return changed
	}); err != nil {
		logger.Info("automatic update: cannot save autoupdate.json", zap.Error(err))
		return
	}
	var updated, failed []string
	for _, m := range due {
		if !a.mayUpdateModule() {
			break
		}
		// saved before the job, as a release's attempt is: a core restarted in
		// the middle of the job does not try that tag again
		tried, at := false, a.Now().UTC().Truncate(time.Second)
		if err := a.modify(func(s *State) bool {
			seen, ok := s.Modules[m.Name]
			if !ok || seen.Tag != m.Latest || !seen.Tried.IsZero() {
				return false
			}
			seen.Tried = at
			s.Modules[m.Name], tried = seen, true
			return true
		}); err != nil || !tried {
			logger.Info("automatic update: not updating the module", zap.String("module", m.Name), zap.Error(err))
			break
		}
		logger.Info("automatic update: updating a module", zap.String("module", m.Name), zap.String("from", m.Installed), zap.String("to", m.Latest))
		outcome, detail := a.Modules.Update(ctx, m.Name)
		change := m.Name + " " + m.Installed + " → " + m.Latest
		switch outcome {
		case Updated:
			updated = append(updated, change)
			continue
		case NotStarted:
			// nothing began: the tag is not spent, and the next round tries it again
			logger.Info("automatic update: a module update was not started", zap.String("module", m.Name), zap.String("to", m.Latest), zap.String("detail", detail))
			if err := a.modify(func(s *State) bool {
				seen, ok := s.Modules[m.Name]
				if !ok || seen.Tag != m.Latest || !seen.Tried.Equal(at) {
					return false
				}
				seen.Tried = time.Time{}
				s.Modules[m.Name] = seen
				return true
			}); err != nil {
				logger.Info("automatic update: cannot save autoupdate.json", zap.Error(err))
			}
			continue
		}
		logger.Info("automatic update: a module update failed", zap.String("module", m.Name), zap.String("to", m.Latest), zap.String("detail", detail))
		if detail != "" {
			change += " (" + detail + ")"
		}
		failed = append(failed, change)
	}
	if a.NotifyModules != nil && len(updated)+len(failed) > 0 {
		a.NotifyModules(modulesSentence(updated, failed))
	}
}

// seeModules records the first sight of each listed tag and returns the
// modules due: seen minAge ago and not tried, by name. Only a tag newer than
// the one recorded starts its own wait: an older one (the shipped catalogue
// while the online one cannot be read), or the same version spelled otherwise,
// keeps the record and is not due, so a catalogue that falls back neither
// restarts the wait nor clears a try.
// Modules no longer listed are kept for the same reason.
func (s *State) seeModules(list []ModuleUpdate, now time.Time) (due []ModuleUpdate, changed bool) {
	for _, m := range list {
		seen, ok := s.Modules[m.Name]
		if ok && seen.Tag != m.Latest {
			if c, comparable := osmodule.CompareVersions(m.Latest, seen.Tag); comparable && c <= 0 {
				continue
			}
		}
		if !ok || seen.Tag != m.Latest {
			if s.Modules == nil {
				s.Modules = map[string]ModuleSeen{}
			}
			seen = ModuleSeen{Tag: m.Latest, FirstSeen: now.UTC().Truncate(time.Second)}
			s.Modules[m.Name], changed = seen, true
		}
		if seen.Tried.IsZero() && !now.Before(seen.FirstSeen.Add(minAge)) {
			due = append(due, m)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].Name < due[j].Name })
	return due, changed
}

// mayUpdateModule is whether a module's job may start now: on, inside the
// window, and nothing else updating (ReCasaOS, the packages, Docker, the
// operating system, another module). Asked before each module.
func (a *AutoUpdate) mayUpdateModule() bool {
	now, s := a.Now(), a.load()
	return s.Enabled && !s.window().earliest(now, time.Time{}).After(now) &&
		(s.Last == nil || s.Last.Result != resultRunning) &&
		!a.unitActive() && !(a.Busy != nil && a.Busy())
}

// modulesSentence is the round's alert: what was updated and what failed.
func modulesSentence(updated, failed []string) string {
	var parts []string
	if len(updated) > 0 {
		parts = append(parts, "Modules updated: "+strings.Join(updated, ", ")+".")
	}
	if len(failed) > 0 {
		parts = append(parts, "Module updates failed: "+strings.Join(failed, ", ")+".")
	}
	return strings.Join(parts, " ")
}
