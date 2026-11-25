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
	"strconv"
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
	// the online catalogue only: [modules] OnlineCatalogue = false, and the file is not read
	catalogueOff = "off"
)

type catalogueState struct {
	cat    osmodule.Catalogue
	ok     bool
	err    string
	detail string
	// online is what became of the online file; fromOnline says, by name, which entries of cat come from it.
	online     OnlineInfo
	fromOnline map[string]bool
}

// readCatalogueFile reads and parses the file at path, with the same refusals for both catalogues. A catalogue that does not pass is no catalogue at all.
func readCatalogueFile(path string, parse func([]byte) (osmodule.Catalogue, error)) catalogueState {
	raw, err := readRegular(path, osmodule.MaxCatalogueBytes)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return catalogueState{err: catalogueNone}
	case errors.Is(err, errTooLarge):
		return catalogueState{err: catalogueInvalid, detail: "catalogue " + osmodule.ReasonTooLarge}
	case err != nil:
		return catalogueState{err: catalogueUnreadable}
	}
	return checkCatalogue(raw, parse)
}

// checkCatalogue parses the bytes of a catalogue: what a file is read with, and what a fetched one has to pass before it is kept.
func checkCatalogue(raw []byte, parse func([]byte) (osmodule.Catalogue, error)) catalogueState {
	cat, err := parse(raw)
	var bad *osmodule.Invalid
	if errors.As(err, &bad) {
		return catalogueState{err: catalogueInvalid, detail: bad.Field + " " + bad.Reason}
	}
	if err != nil {
		return catalogueState{err: catalogueInvalid}
	}
	// a module of the system image is not one to download: the catalogue of an image-built host, read by the wrong provider
	for i, e := range cat.Modules {
		if e.Source == osmodule.SourceImage {
			return catalogueState{err: catalogueInvalid, detail: "modules." + strconv.Itoa(i) + ".source " + osmodule.ReasonImageOnly}
		}
	}
	return catalogueState{cat: cat, ok: true}
}

// loadCatalogue reads the catalogue of this release (the file the installer put at /usr/share/casaos/modules) and the one the box fetched (/var/lib/casaos/modules), and gives the
// merge of the two. Only when CASAOS_MODULE_TEST is exactly "1" do CASAOS_MODULE_CATALOGUE and CASAOS_MODULE_ONLINE_CATALOGUE name other files. An online file that does not pass
// counts for nothing and online says why; ok is true as soon as one of the two is good; with env.OnlineCatalogueOff the online file is not read at all. Nothing here reads the network.
func loadCatalogue(env Env) catalogueState {
	path, onlinePath := env.Roots.catalogue(), env.Roots.OnlineCatalogue()
	if env.testMode() {
		if p := env.Getenv("CASAOS_MODULE_CATALOGUE"); p != "" {
			path = p
		}
		if p := env.Getenv("CASAOS_MODULE_ONLINE_CATALOGUE"); p != "" {
			onlinePath = p
		}
	}
	shipped := readCatalogueFile(path, osmodule.ParseCatalogue)
	if env.OnlineCatalogueOff {
		return catalogueState{cat: shipped.cat, ok: shipped.ok, err: shipped.err, detail: shipped.detail, online: OnlineInfo{Error: catalogueOff}}
	}
	on := readCatalogueFile(onlinePath, osmodule.ParseOnlineCatalogue)
	res := catalogueState{cat: shipped.cat, ok: shipped.ok, err: shipped.err, detail: shipped.detail, online: OnlineInfo{Error: on.err}}
	if !on.ok {
		return res
	}
	res.online = OnlineInfo{OK: true, Serial: on.cat.Serial, Generated: on.cat.Generated}
	res.ok, res.err, res.detail = true, "", ""
	// the checks that say no are those of the system this box is (the os reads no network and no context)
	bx := &box{ctx: context.Background(), env: &env}
	osr := bx.OS()
	sys := osmodule.System{OSID: osr.ID, OSVersion: osr.Version, OSFrom: osr.From, Arch: env.Arch, ReCasaOS: env.Version}
	accepts := func(e osmodule.Entry) bool {
		for _, c := range osmodule.SystemChecks(e, sys) {
			if !c.OK {
				return false
			}
		}
		return true
	}
	res.cat, res.fromOnline = osmodule.Merge(shipped.cat, on.cat, accepts)
	return res
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

// PlanUpdateOf is the plan of updating the installed module called name to the tag of the catalogue: the same, and nothing written.
func PlanUpdateOf(ctx context.Context, env Env, name string) osmodule.PlanResult {
	env = env.withDefaults()
	return osmodule.PlanUpdate(loadCatalogue(env).cat, name, &box{ctx: ctx, env: &env})
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
	// Online is what became of the catalogue the box fetched; nil only for a caller that does not read it (the image provider).
	Online *OnlineInfo `json:"online,omitempty"`
}

// OnlineInfo says whether the online catalogue was read: its serial and date when it was, and if not why ("none", "unreadable", "invalid", or "off" when the setting turns it off).
type OnlineInfo struct {
	OK        bool   `json:"ok"`
	Serial    int64  `json:"serial,omitempty"`
	Generated string `json:"generated,omitempty"`
	Error     string `json:"error,omitempty"`
}

// A ListModule is a module of the catalogue or one that is installed. Catalogue says whether the catalogue of this release has it.
type ListModule struct {
	Name        string     `json:"name"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Tier        string     `json:"tier"`
	Tag         string     `json:"tag"`
	Catalogue   bool       `json:"catalogue"`
	Source      string     `json:"source"` // "shipped" | "online": the catalogue the entry was taken from
	Installed   *Installed `json:"installed"`
	System      SystemInfo `json:"system"`
	Broken      string     `json:"broken,omitempty"`
	// Repository is the "<owner>/<repo>" an entry of schema 3 comes from; Foreign says it is not one of ReCasaOS.
	Repository string `json:"repository,omitempty"`
	Foreign    bool   `json:"foreign,omitempty"`
	// AutoUpdate is there for a module that is foreign and installed: whether its owner lets the night update it from this repository (AutoAllowed). A module of ReCasaOS follows the
	// general switch.
	AutoUpdate *bool `json:"auto_update,omitempty"`
	// Withdrawn is there when the version that is installed is one the online catalogue withdrew.
	Withdrawn *WithdrawnInfo `json:"withdrawn,omitempty"`
	// (install.sh reads a module by patterns, from the key name to the key update: name stays first and update last)
	// Update is there for a module that is installed, that the catalogue has and whose installed version can be read; Available says the catalogue's tag is strictly later.
	Update *UpdateInfo `json:"update,omitempty"`
}

// UpdateInfo says whether the catalogue has a later version of an installed module: never one it withdrew.
type UpdateInfo struct {
	Available bool   `json:"available"`
	Installed string `json:"installed"`
	Latest    string `json:"latest"`
}

// WithdrawnInfo is the installed version of a module that the catalogue withdrew, and why, as text to show.
type WithdrawnInfo struct {
	Tag    string `json:"tag"`
	Reason string `json:"reason"`
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

// updateInfo is what list says of the update of a module: nothing when the installed version cannot be read, and available only when the catalogue's tag is strictly later (compared as
// numbers: v0.1.10 is later than v0.1.9).
func updateInfo(installed *string, latest string) *UpdateInfo {
	if installed == nil {
		return nil
	}
	// (both are tags: the installed one was read as one, and the catalogue is not taken whole when one of its tags is not)
	c, _ := osmodule.CompareTags(*installed, latest)
	return &UpdateInfo{Available: c < 0, Installed: *installed, Latest: latest}
}

// withdrawnInfo is what list says of a module whose installed version the catalogue withdrew: nothing when the installed version cannot be read or is not withdrawn.
func withdrawnInfo(cat osmodule.Catalogue, name string, installed *string) *WithdrawnInfo {
	if installed == nil {
		return nil
	}
	for _, w := range cat.Withdrawn {
		if w.Name == name && w.Tag == *installed {
			return &WithdrawnInfo{Tag: w.Tag, Reason: w.Reason}
		}
	}
	return nil
}

// List is the catalogue of this release joined with what is installed.
func List(ctx context.Context, env Env) (ListResult, error) {
	env = env.withDefaults()
	cs := loadCatalogue(env)
	res := ListResult{Catalogue: CatalogueInfo{OK: cs.ok, Error: cs.err, Detail: cs.detail, Online: &cs.online}, Modules: []ListModule{}}
	bx := &box{ctx: ctx, env: &env}
	installed, broken, err := bx.InstalledManifests()
	if err != nil {
		return res, err
	}
	osr := bx.OS()
	sys := osmodule.System{OSID: osr.ID, OSVersion: osr.Version, OSFrom: osr.From, Arch: env.Arch, ReCasaOS: env.Version}
	listed, allowed := map[string]bool{}, AutoAllowed(env.Roots)
	for _, e := range cs.cat.Modules {
		m := ListModule{Name: e.Name, Title: e.Manifest.Title, Description: e.Manifest.Description, Tier: e.Tier, Tag: e.Tag, Catalogue: true, Source: "shipped", System: SystemInfo{Reasons: []string{}},
			Repository: e.Repository, Foreign: e.Foreign()}
		for _, c := range osmodule.SystemChecks(e, sys) {
			if !c.OK {
				m.System.Reasons = append(m.System.Reasons, c.Code)
			}
		}
		m.System.Supported = len(m.System.Reasons) == 0
		if cs.fromOnline[e.Name] {
			m.Source = "online"
		}
		if _, ok := installed[e.Name]; ok {
			m.Installed = &Installed{Version: installedVersion(env, e.Name)}
			m.Update = updateInfo(m.Installed.Version, e.Tag)
			// a version the catalogue withdrew is offered to nobody
			if m.Update != nil && cs.cat.IsWithdrawn(e) {
				m.Update.Available = false
			}
			m.Withdrawn = withdrawnInfo(cs.cat, e.Name, m.Installed.Version)
			if m.Foreign {
				// a yes is for the repository it was given for, not for any module of that name
				repo, there := allowed[e.Name]
				on := there && repo == e.Repository
				m.AutoUpdate = &on
			}
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
		version := installedVersion(env, name)
		res.Modules = append(res.Modules, ListModule{
			Name: name, Title: mf.Title, Description: mf.Description,
			Installed: &Installed{Version: version}, System: SystemInfo{Reasons: []string{"catalogue"}}, Withdrawn: withdrawnInfo(cs.cat, name, version),
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
