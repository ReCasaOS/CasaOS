package modulecmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// The three commands that only read: list, plan and status. Each is one JSON object on the standard output and nothing else, and none writes anything on the box.

// the codes of a catalogue that is not there to be used
const (
	catalogueNone       = "none"
	catalogueUnreadable = "unreadable"
	catalogueInvalid    = "invalid"
)

type catalogueState struct {
	cat    osmodule.Catalogue
	ok     bool
	err    string
	detail string
}

// loadCatalogue reads the catalogue of this release: the file the installer put at /usr/share/casaos/modules, or, only when CASAOS_MODULE_TEST is exactly "1", the file that
// CASAOS_MODULE_CATALOGUE names. A catalogue that does not pass is no catalogue at all.
func loadCatalogue(env Env) catalogueState {
	path := env.Roots.catalogue()
	if env.testMode() {
		if p := env.Getenv("CASAOS_MODULE_CATALOGUE"); p != "" {
			path = p
		}
	}
	raw, err := readRegular(path, osmodule.MaxCatalogueBytes)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return catalogueState{err: catalogueNone}
	case errors.Is(err, errTooLarge):
		return catalogueState{err: catalogueInvalid, detail: "catalogue " + osmodule.ReasonTooLarge}
	case err != nil:
		return catalogueState{err: catalogueUnreadable}
	}
	cat, err := osmodule.ParseCatalogue(raw)
	var bad *osmodule.Invalid
	if errors.As(err, &bad) {
		return catalogueState{err: catalogueInvalid, detail: bad.Field + " " + bad.Reason}
	}
	if err != nil {
		return catalogueState{err: catalogueInvalid}
	}
	return catalogueState{cat: cat, ok: true}
}

func printJSON(w io.Writer, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(raw, '\n'))
	return err
}

// ---- plan --------------------------------------------------------------------------------------------------------------------------------------------------------------------

// PlanOf is the plan of installing the module called name: what the box can say of it, and nothing written.
func PlanOf(ctx context.Context, env Env, name string) osmodule.PlanResult {
	env = env.withDefaults()
	return osmodule.Plan(loadCatalogue(env).cat, name, &box{ctx: ctx, env: &env})
}

// ---- list --------------------------------------------------------------------------------------------------------------------------------------------------------------------

// A ListResult is the answer of list.
type ListResult struct {
	Catalogue CatalogueInfo `json:"catalogue"`
	Modules   []ListModule  `json:"modules"`
}

// CatalogueInfo says whether the catalogue of this release was read, and if not why: "none" (there is none), "unreadable" or "invalid" with the place in Detail.
type CatalogueInfo struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// A ListModule is a module of the catalogue or one that is installed. Catalogue says whether the catalogue of this release has it.
type ListModule struct {
	Name        string     `json:"name"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Tier        string     `json:"tier"`
	Tag         string     `json:"tag"`
	Catalogue   bool       `json:"catalogue"`
	Installed   *Installed `json:"installed"`
	System      SystemInfo `json:"system"`
	Broken      string     `json:"broken,omitempty"`
}

// Installed is what the box has of a module: its version, null when the box does not know it.
type Installed struct {
	Version *string `json:"version"`
}

// SystemInfo says whether this system can have the module, and the checks (by code) that say no.
type SystemInfo struct {
	Supported bool     `json:"supported"`
	Reasons   []string `json:"reasons"`
}

var tagRe = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// installedVersion is the tag the module was installed at, nil when it cannot be told.
func installedVersion(env Env, name string) *string {
	raw, err := readRegular(env.Roots.version(name), 64)
	if err != nil {
		return nil
	}
	v := strings.TrimSpace(string(raw))
	if !tagRe.MatchString(v) {
		return nil
	}
	return &v
}

// List is the catalogue of this release joined with what is installed.
func List(ctx context.Context, env Env) (ListResult, error) {
	env = env.withDefaults()
	cs := loadCatalogue(env)
	res := ListResult{Catalogue: CatalogueInfo{OK: cs.ok, Error: cs.err, Detail: cs.detail}, Modules: []ListModule{}}
	bx := &box{ctx: ctx, env: &env}
	installed, broken, err := bx.InstalledManifests()
	if err != nil {
		return res, err
	}
	osID, osVersion := bx.OS()
	sys := osmodule.System{OSID: osID, OSVersion: osVersion, Arch: env.Arch, ReCasaOS: env.Version}
	listed := map[string]bool{}
	for _, e := range cs.cat.Modules {
		m := ListModule{Name: e.Name, Title: e.Manifest.Title, Description: e.Manifest.Description, Tier: e.Tier, Tag: e.Tag, Catalogue: true, System: SystemInfo{Reasons: []string{}}}
		for _, c := range osmodule.SystemChecks(e, sys) {
			if !c.OK {
				m.System.Reasons = append(m.System.Reasons, c.Code)
			}
		}
		m.System.Supported = len(m.System.Reasons) == 0
		if _, ok := installed[e.Name]; ok {
			m.Installed = &Installed{Version: installedVersion(env, e.Name)}
		} else if slices.Contains(broken, e.Name) {
			m.Broken = "manifest_invalid"
		}
		res.Modules = append(res.Modules, m)
		listed[e.Name] = true
	}
	// what is installed and the catalogue no longer has, and what is there and cannot be read
	for _, name := range sortedManifestKeys(installed) {
		if listed[name] {
			continue
		}
		mf := installed[name]
		res.Modules = append(res.Modules, ListModule{
			Name: name, Title: mf.Title, Description: mf.Description,
			Installed: &Installed{Version: installedVersion(env, name)}, System: SystemInfo{Reasons: []string{"catalogue"}},
		})
	}
	slices.Sort(broken)
	for _, name := range broken {
		if !listed[name] && nameRe.MatchString(name) {
			res.Modules = append(res.Modules, ListModule{Name: name, System: SystemInfo{Reasons: []string{"catalogue"}}, Broken: "manifest_invalid"})
		}
	}
	return res, nil
}

func sortedManifestKeys(m map[string]osmodule.Manifest) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// ---- status ------------------------------------------------------------------------------------------------------------------------------------------------------------------

// A StatusResult is the answer of status for one module.
type StatusResult struct {
	Name      string     `json:"name"`
	Installed bool       `json:"installed"`
	Version   *string    `json:"version"`
	Unit      UnitState  `json:"unit"`
	Route     RouteState `json:"route"`
}

// UnitState is what systemd says of the unit of the module.
type UnitState struct {
	Active  bool `json:"active"`
	Enabled bool `json:"enabled"`
}

// RouteState says whether the gateway answers on the route of the module.
type RouteState struct {
	Answers bool `json:"answers"`
}

// StatusOf is the state of one module. A module that is not installed (no manifest that reads) is not active, not enabled and does not answer.
func StatusOf(ctx context.Context, env Env, name string) (StatusResult, error) {
	env = env.withDefaults()
	bx := &box{ctx: ctx, env: &env}
	installed, _, err := bx.InstalledManifests()
	if err != nil {
		return StatusResult{}, err
	}
	res := StatusResult{Name: name}
	m, ok := installed[name]
	if !ok {
		return res, nil
	}
	res.Installed, res.Version = true, installedVersion(env, name)
	res.Unit = UnitState{Active: unitSays(ctx, env, "is-active", m.Unit, "active"), Enabled: unitSays(ctx, env, "is-enabled", m.Unit, "enabled")}
	if env.Gateway != nil {
		if answers, err := env.Gateway.Answers(ctx, m.GatewayRoute); err == nil {
			res.Route.Answers = answers
		}
	}
	return res, nil
}

// unitSays asks systemctl about a unit and says whether its answer is exactly the word.
func unitSays(ctx context.Context, env Env, verb, unit, word string) bool {
	out, err := env.Runner.Run(ctx, Cmd{Argv: []string{"systemctl", verb, unit}, Timeout: 10 * time.Second})
	return err == nil && strings.TrimSpace(out.Text) == word
}

// statusAll is the state of every module that is installed.
func statusAll(ctx context.Context, env Env) (map[string][]StatusResult, error) {
	env = env.withDefaults()
	installed, _, err := (&box{ctx: ctx, env: &env}).InstalledManifests()
	if err != nil {
		return nil, err
	}
	out := []StatusResult{}
	for _, name := range sortedManifestKeys(installed) {
		s, err := StatusOf(ctx, env, name)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return map[string][]StatusResult{"modules": out}, nil
}

func fprintln(w io.Writer, a ...any) { _, _ = fmt.Fprintln(w, a...) }
