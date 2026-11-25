package osmodule

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The catalogue of a release of ReCasaOS: the module versions that this release has been tested with, installed with it at /usr/share/casaos/modules/catalogue.json. Nothing that a module's
// author publishes reaches a box by itself: the box installs what the catalogue names, and only the archive whose sha256 the catalogue gives. The file is read as data and as a whole: if one
// rule is broken anywhere in it there is no module at all and an error, never the entries that happened to pass.

// CatalogueSchema is the schema of the catalogue a release ships; OnlineCatalogueSchema is the one of the catalogue published online, which is schema 1 plus a serial and the date it was
// generated. CommunityCatalogueSchema is schema 2 opened to modules of other owners: each entry says the repository it comes from, every archive is a copy that ReCasaOS hosts in a release
// of its own (ReCasaOS/modules, tag m-<name>-<tag>), never the author's, and the catalogue lists the versions that were withdrawn.
const (
	CatalogueSchema          = 1
	OnlineCatalogueSchema    = 2
	CommunityCatalogueSchema = 3
)

// the bounds of a catalogue
const (
	// MaxCatalogueBytes is the most of a catalogue that is read.
	MaxCatalogueBytes = 1 << 20
	// MaxCatalogueModules is the most entries in a catalogue.
	MaxCatalogueModules = 64
	// maxURLBytes is the longest address of an archive.
	maxURLBytes = 512
	// MaxWithdrawn is the most versions withdrawn in a catalogue of schema 3.
	MaxWithdrawn = 64
	// maxReasonRunes is the longest reason of a version withdrawn, in characters.
	maxReasonRunes = 200
)

// recasaosOwner is the owner on GitHub of the modules of ReCasaOS: an entry of another owner is never core.
const recasaosOwner = "ReCasaOS"

// the tiers of a module: core is maintained here and proven on a real guest, community is proven in CI against a test double and said so to whoever installs it.
const (
	TierCore      = "core"
	TierCommunity = "community"
)

// the sources of a module: an archive the box downloads (the default, no source), or the system image that has the module already (an image-built host, [server] ImageManaged: the
// image provider of the core starts and stops its unit, and the module assistant refuses such an entry)
const (
	SourceArchive = ""
	SourceImage   = "image"
)

// A Catalogue is the list of the modules a release of ReCasaOS can install.
//
// Serial and Generated are those of an online catalogue (schema 2 or 3) and empty in a shipped one: Serial grows with each publication, Generated is an RFC 3339 UTC date. Withdrawn is
// that of schema 3, nil in the others.
type Catalogue struct {
	Schema    int         `json:"schema"`
	Serial    int64       `json:"serial,omitempty"`
	Generated string      `json:"generated,omitempty"`
	Modules   []Entry     `json:"modules"`
	Withdrawn []Withdrawn `json:"withdrawn,omitempty"`
}

// A Withdrawn is a version of a module that its catalogue says not to use any more, and why, as text to show.
type Withdrawn struct {
	Name   string `json:"name"`
	Tag    string `json:"tag"`
	Reason string `json:"reason"`
}

// catalogueWire is the text of a catalogue of any schema: the head fields of schemas 2 and 3 are kept raw so that a wrong one is named, not just unreadable.
type catalogueWire struct {
	Schema    int             `json:"schema"`
	Serial    json.RawMessage `json:"serial"`
	Generated json.RawMessage `json:"generated"`
	Withdrawn json.RawMessage `json:"withdrawn"`
	Modules   []Entry         `json:"modules"`
}

// An Entry is a module at a version: where its release is (the repository, the tag and the commit it was built from), the archive of each architecture with the sha256 it must have, and
// the manifest of the module, which the archive holds too: the box needs it before it has the archive (to say whether the system is supported, which packages the module needs, what to show
// of it), and the release build that writes the catalogue reads it from the archive, so that the two are the same text.
//
// Source is SourceImage for a module the system image has: no assets then (nothing is downloaded), the rest as for an archive. Repository is "<owner>/<repo>" in schema 3, empty in the
// others (a module of ReCasaOS).
type Entry struct {
	Name       string   `json:"name"`
	Component  string   `json:"component"`
	Repository string   `json:"repository,omitempty"`
	Tier       string   `json:"tier"`
	Tag        string   `json:"tag"`
	Commit     string   `json:"commit"`
	Source     string   `json:"source,omitempty"`
	Assets     Assets   `json:"assets"`
	Manifest   Manifest `json:"manifest"`
}

// Assets are the archives of an entry, by architecture; an architecture with no archive is a pointer that is nil.
type Assets struct {
	AMD64 *Asset `json:"amd64,omitempty"`
	ARM64 *Asset `json:"arm64,omitempty"`
}

// An Asset is an archive: where it is and the sha256 of its bytes.
type Asset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

var (
	tagRe     = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
	commitRe  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	archiveRe = regexp.MustCompile(`^linux-(amd64|arm64)-casaos-[a-z-]+-(v[0-9.]+)\.tar\.gz$`)
	// the owner of a repository on GitHub: letters, digits and inner hyphens, 39 at most
	ownerRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
)

// ParseCatalogue reads a catalogue of a release (schema 1) and checks everything in it. The error is an *Invalid that names the place; the Catalogue is empty whenever there is an error.
func ParseCatalogue(raw []byte) (Catalogue, error) { return parseCatalogue(raw, CatalogueSchema) }

// ParseOnlineCatalogue reads an online catalogue of schema 2 or 3, each by its rules: those of ParseCatalogue, and a serial of 1 or more and the date it was generated; in schema 3 also
// the repository of each entry, archives only in the mirror of ReCasaOS, and the list of the versions withdrawn.
func ParseOnlineCatalogue(raw []byte) (Catalogue, error) {
	return parseCatalogue(raw, OnlineCatalogueSchema, CommunityCatalogueSchema)
}

func parseCatalogue(raw []byte, schemas ...int) (Catalogue, error) {
	if len(raw) > MaxCatalogueBytes {
		return Catalogue{}, invalid("catalogue", ReasonTooLarge)
	}
	var w catalogueWire
	if err := decodeStrict(raw, &w); err != nil {
		return Catalogue{}, invalid("catalogue", ReasonUnreadable)
	}
	c := Catalogue{Schema: w.Schema, Modules: w.Modules}
	// outside schema 3 its keys are unknown ones: refused as such, before anything else, as they always were
	if c.Schema != CommunityCatalogueSchema && (w.Withdrawn != nil || hasRepository(raw)) {
		return Catalogue{}, invalid("catalogue", ReasonUnreadable)
	}
	switch {
	case !slices.Contains(schemas, c.Schema):
		return Catalogue{}, invalid("schema", ReasonSchema)
	case c.Schema == CatalogueSchema && (w.Serial != nil || w.Generated != nil):
		return Catalogue{}, invalid("catalogue", ReasonUnreadable)
	}
	if c.Schema != CatalogueSchema {
		if err := c.readOnlineHead(w); err != nil {
			return Catalogue{}, err
		}
	}
	switch {
	case c.Modules == nil:
		return Catalogue{}, invalid("modules", ReasonMissing)
	case len(c.Modules) > MaxCatalogueModules:
		return Catalogue{}, invalid("modules", ReasonTooMany)
	}
	seen := map[string]bool{}
	for i, e := range c.Modules {
		if err := e.validate("modules."+strconv.Itoa(i), c.Schema); err != nil {
			return Catalogue{}, err
		}
		if seen[e.Name] {
			return Catalogue{}, invalid("modules."+strconv.Itoa(i)+".name", ReasonValue)
		}
		seen[e.Name] = true
	}
	if c.Schema == CommunityCatalogueSchema {
		if err := c.readWithdrawn(w.Withdrawn); err != nil {
			return Catalogue{}, err
		}
	}
	return c, nil
}

// hasRepository says whether an entry of a catalogue that decodeStrict took has the key repository, whatever its value (an empty text, null).
func hasRepository(raw []byte) bool {
	var keys struct {
		Modules []map[string]json.RawMessage `json:"modules"`
	}
	_ = json.Unmarshal(raw, &keys)
	return slices.ContainsFunc(keys.Modules, func(m map[string]json.RawMessage) bool {
		_, ok := m["repository"]
		return ok
	})
}

// readWithdrawn sets and checks the versions withdrawn of a catalogue of schema 3: a list, empty or of 64 at most, of the name and the tag of a module, a pair once, and a reason of 1 to
// 200 characters judged as the title of a manifest is (printable, and something a person sees).
func (c *Catalogue) readWithdrawn(raw json.RawMessage) error {
	if raw == nil {
		return invalid("withdrawn", ReasonMissing)
	}
	if err := decodeStrict(raw, &c.Withdrawn); err != nil || c.Withdrawn == nil {
		return invalid("withdrawn", ReasonValue)
	}
	if len(c.Withdrawn) > MaxWithdrawn {
		return invalid("withdrawn", ReasonTooMany)
	}
	seen := map[Withdrawn]bool{}
	for i, v := range c.Withdrawn {
		at := "withdrawn." + strconv.Itoa(i)
		pair := Withdrawn{Name: v.Name, Tag: v.Tag}
		switch n := utf8.RuneCountInString(v.Reason); {
		case !nameRe.MatchString(v.Name):
			return invalid(at+".name", ReasonValue)
		case !tagRe.MatchString(v.Tag):
			return invalid(at+".tag", ReasonValue)
		case n < 1 || n > maxReasonRunes || !printable(v.Reason) || !shows(v.Reason):
			return invalid(at+".reason", ReasonValue)
		case seen[pair]:
			return invalid(at, ReasonValue)
		}
		seen[pair] = true
	}
	return nil
}

// readOnlineHead sets and checks the serial (a whole number of 1 or more) and the date (RFC 3339 in UTC, "Z").
func (c *Catalogue) readOnlineHead(w catalogueWire) error {
	if w.Serial == nil {
		return invalid("serial", ReasonMissing)
	}
	if err := json.Unmarshal(w.Serial, &c.Serial); err != nil || c.Serial < 1 || bytes.ContainsAny(w.Serial, ".eE") {
		return invalid("serial", ReasonValue)
	}
	if w.Generated == nil {
		return invalid("generated", ReasonMissing)
	}
	if err := json.Unmarshal(w.Generated, &c.Generated); err != nil {
		return invalid("generated", ReasonValue)
	}
	if t, err := time.Parse(time.RFC3339, c.Generated); err != nil || !strings.HasSuffix(c.Generated, "Z") || t.IsZero() {
		return invalid("generated", ReasonValue)
	}
	return nil
}

// validate checks an entry of a catalogue of schema; at is the place of it in the catalogue, for the error.
func (e Entry) validate(at string, schema int) error {
	for _, c := range []struct {
		field string
		ok    bool
	}{
		{"name", nameRe.MatchString(e.Name)},
		{"component", componentRe.MatchString(e.Component)},
		{"tier", e.Tier == TierCore || e.Tier == TierCommunity},
		{"tag", tagRe.MatchString(e.Tag)},
		{"commit", commitRe.MatchString(e.Commit)},
	} {
		if !c.ok {
			return invalid(at+"."+c.field, ReasonValue)
		}
	}
	if schema == CommunityCatalogueSchema {
		owner, repo, _ := strings.Cut(e.Repository, "/")
		switch {
		case e.Repository == "":
			return invalid(at+".repository", ReasonMissing)
		case !ownerRe.MatchString(owner) || repo != e.Component:
			return invalid(at+".repository", ReasonValue)
		case e.Foreign() && e.Tier != TierCommunity:
			return invalid(at+".tier", ReasonValue)
		}
	}
	switch e.Source {
	case SourceImage:
		// in the image: nothing to download
		if e.Assets.AMD64 != nil || e.Assets.ARM64 != nil {
			return invalid(at+".assets", ReasonValue)
		}
	case SourceArchive:
		if e.Assets.AMD64 == nil && e.Assets.ARM64 == nil {
			return invalid(at+".assets", ReasonMissing)
		}
	default:
		return invalid(at+".source", ReasonValue)
	}
	for _, a := range []struct {
		arch  string
		asset *Asset
	}{{"amd64", e.Assets.AMD64}, {"arm64", e.Assets.ARM64}} {
		if a.asset != nil && !e.validAsset(a.arch, *a.asset, schema) {
			return invalid(at+".assets."+a.arch, ReasonValue)
		}
	}
	if err := validate(e.Manifest, e.Name); err != nil {
		var bad *Invalid
		if errors.As(err, &bad) {
			return invalid(at+".manifest."+bad.Field, bad.Reason)
		}
		return err
	}
	if e.Manifest.Component != e.Component {
		return invalid(at+".manifest.component", ReasonValue)
	}
	return nil
}

// validAsset says whether an archive is exactly what the catalogue allows for this entry and architecture: the release of the entry's own repository, under the organisation, at the entry's
// tag (schemas 1 and 2), or the release m-<name>-<tag> of the mirror ReCasaOS/modules and only it (schema 3: the box never downloads from an author), over https from github.com, named for
// this architecture and this tag, and nothing more (no port, no user, no query, no fragment, no other directory, no escape), with a sha256.
func (e Entry) validAsset(arch string, a Asset, schema int) bool {
	release := "https://github.com/ReCasaOS/" + e.Component + "/releases/download/" + e.Tag + "/"
	if schema == CommunityCatalogueSchema {
		release = "https://github.com/ReCasaOS/modules/releases/download/m-" + e.Name + "-" + e.Tag + "/"
	}
	archive, ok := strings.CutPrefix(a.URL, release)
	if !ok || len(a.URL) > maxURLBytes || !sha256Re.MatchString(a.SHA256) {
		return false
	}
	m := archiveRe.FindStringSubmatch(archive)
	return m != nil && m[1] == arch && m[2] == e.Tag
}

// Foreign says whether the entry comes from a repository that is not one of ReCasaOS: its owner is not exactly ReCasaOS. An entry of schema 1 or 2 has no repository and is of ReCasaOS.
func (e Entry) Foreign() bool {
	owner, _, _ := strings.Cut(e.Repository, "/")
	return e.Repository != "" && owner != recasaosOwner
}

// Asset is the archive of the entry for an architecture ("amd64", "arm64"), and whether there is one.
func (e Entry) Asset(arch string) (Asset, bool) {
	switch {
	case arch == "amd64" && e.Assets.AMD64 != nil:
		return *e.Assets.AMD64, true
	case arch == "arm64" && e.Assets.ARM64 != nil:
		return *e.Assets.ARM64, true
	}
	return Asset{}, false
}

// Find is the entry of a module by its name.
func (c Catalogue) Find(name string) (Entry, bool) {
	for _, e := range c.Modules {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}
