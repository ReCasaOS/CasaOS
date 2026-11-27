// Package apiwidgets keeps the dashboard widgets that show values read from a JSON
// address. A recipe says what to read and how to show it, and can be shared: it
// never chooses the host, and its secrets are inputs that stay on the box.
package apiwidgets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	recipeVersion  = 1
	maxRecipeSize  = 16 << 10
	maxText        = 40 // a displayed text, in characters
	maxUnit        = 12
	maxBaseHint    = 120
	maxPairs       = 8 // inputs, headers, query parameters
	maxRows        = 8
	maxFigures     = 4
	maxRules       = 3
	maxLimit       = 8
	minRefresh     = 60
	maxRefresh     = 3600
	defaultRefresh = 300
)

// ErrInvalid is a recipe that is refused; the message names the field.
var ErrInvalid = errors.New("invalid widget")

var (
	inputID    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	headerName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$") // an RFC 9110 token

	formats = []string{"", "text", "number", "bytes", "percent", "ratio", "duration", "date", "count"}
	ops     = []string{"eq", "ne", "lt", "gt"}
	levels  = []string{"ok", "info", "warning", "danger"}

	// headers the HTTP client owns
	reservedHeaders = []string{"host", "connection", "content-length", "transfer-encoding"}
	// a header or parameter whose name holds one of these carries a secret
	secretNames = []string{"authorization", "key", "token", "secret", "password"}
)

// Value is a header, query or basic value: a fixed text, or the input that fills it.
type Value struct{ Text, Input string }

type inputRef struct {
	Input string `json:"input"`
}

// MarshalJSON writes "text" or {"input":"id"}.
func (v Value) MarshalJSON() ([]byte, error) {
	if v.Input != "" {
		return json.Marshal(inputRef{v.Input})
	}
	return json.Marshal(v.Text)
}

// UnmarshalJSON is as strict as the rest of the recipe: the decoder's own
// strictness does not reach here.
func (v *Value) UnmarshalJSON(data []byte) error {
	switch {
	case bytes.HasPrefix(data, []byte(`"`)):
		*v = Value{}
		return json.Unmarshal(data, &v.Text)
	case bytes.HasPrefix(data, []byte(`{`)):
		var ref inputRef
		if err := decodeStrict(data, &ref); err != nil {
			return err
		}
		if ref.Input != "" {
			*v = Value{Input: ref.Input}
			return nil
		}
	}
	return errors.New(`a header, query or basic value is a text or {"input": id}`)
}

// Basic is HTTP basic authentication.
type Basic struct {
	User     Value `json:"user"`
	Password Value `json:"password"`
}

// Input is a secret the box asks for.
type Input struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Rule gives a level, and maybe another text, to a value; the first true one wins.
type Rule struct {
	Op    string `json:"op"`
	To    any    `json:"to"`
	Level string `json:"level"`
	Text  string `json:"text,omitempty"`
}

// RowSpec is one line of the card. With Each, it is one line per element of that
// array, and Label and Value are pointers inside the element.
type RowSpec struct {
	Label  string  `json:"label"`
	Value  string  `json:"value"`
	Each   *string `json:"each,omitempty"` // "" is the root, so presence is what counts
	Format string  `json:"format,omitempty"`
	Unit   string  `json:"unit,omitempty"`
	Rules  []Rule  `json:"rules,omitzero"`
	Limit  int     `json:"limit,omitempty"`
	Bar    bool    `json:"bar,omitempty"`
}

// StatusSpec fills the card's chip.
type StatusSpec struct {
	Value  string `json:"value"`
	Format string `json:"format,omitempty"`
	Unit   string `json:"unit,omitempty"`
	Rules  []Rule `json:"rules,omitzero"`
}

// Recipe is the shareable half of a widget.
type Recipe struct {
	Version  int              `json:"recasaos_widget"`
	Name     string           `json:"name"`
	Icon     string           `json:"icon,omitempty"`
	Path     string           `json:"path"`
	Query    map[string]Value `json:"query,omitzero"`
	Headers  map[string]Value `json:"headers,omitzero"`
	Basic    *Basic           `json:"basic,omitempty"`
	Inputs   []Input          `json:"inputs,omitzero"`
	BaseHint string           `json:"base_hint,omitempty"`
	Layout   string           `json:"layout"`
	Rows     []RowSpec        `json:"rows"`
	Status   *StatusSpec      `json:"status,omitempty"`
	Refresh  int              `json:"refresh"`
}

// ParseRecipe reads and checks a recipe. A draft, one still being built, may have
// no row yet. No refresh is 300 seconds, no layout is "rows".
func ParseRecipe(data []byte, draft bool) (Recipe, error) {
	if len(data) > maxRecipeSize {
		return Recipe{}, fmt.Errorf("%w: the recipe is over 16 KiB", ErrInvalid)
	}
	var r Recipe
	if err := decodeStrict(data, &r); err != nil {
		return Recipe{}, fmt.Errorf("%w: %s", ErrInvalid, err)
	}
	if r.Refresh == 0 {
		r.Refresh = defaultRefresh
	}
	if r.Layout == "" {
		r.Layout = "rows"
	}
	if r.Rows == nil {
		r.Rows = []RowSpec{}
	}
	if err := r.check(draft); err != nil {
		return Recipe{}, fmt.Errorf("%w: %s", ErrInvalid, err)
	}
	return r, nil
}

// decodeStrict reads one JSON value, with no unknown field, no key written twice
// and nothing after it.
func decodeStrict(data []byte, into any) error {
	if err := checkKeys(json.NewDecoder(bytes.NewReader(data)), "", reflect.TypeOf(into)); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(into); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("unexpected data after the JSON value")
	}
	return nil
}

// checkKeys reads the next JSON value the way t will receive it. encoding/json
// matches a key whatever its case and lets the last of two win: here a key of a
// struct is its JSON tag, exactly, and no object writes a key twice. A value of
// another shape than t is left for the decoder to refuse.
func checkKeys(d *json.Decoder, at string, t reflect.Type) error {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeFor[Value]() {
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return err
		}
		if err := checkKeys(json.NewDecoder(bytes.NewReader(raw)), at, reflect.TypeFor[inputRef]()); err != nil {
			return err
		}
		if new(Value).UnmarshalJSON(raw) != nil {
			return fmt.Errorf(`%s must be a text or {"input": id}`, at)
		}
		return nil
	}
	tok, err := d.Token()
	if err != nil {
		return err
	}
	switch tok {
	case json.Delim('['):
		var elem reflect.Type
		if t != nil && t.Kind() == reflect.Slice {
			elem = t.Elem()
		}
		for i := 0; d.More(); i++ {
			if err := checkKeys(d, fmt.Sprintf("%s[%d]", at, i), elem); err != nil {
				return err
			}
		}
		_, err = d.Token()
	case json.Delim('{'):
		where := ""
		if at != "" {
			where = at + ": "
		}
		seen := map[string]bool{}
		for d.More() {
			tok, err := d.Token()
			key, ok := tok.(string)
			if err != nil || !ok {
				return errors.Join(err, errors.New("a key is expected"))
			}
			if seen[key] {
				return fmt.Errorf("%s%q is written twice", where, key)
			}
			seen[key] = true
			var elem reflect.Type
			switch {
			case t == nil:
			case t.Kind() == reflect.Map:
				elem = t.Elem()
			case t.Kind() == reflect.Struct:
				for i := range t.NumField() {
					if tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); tag == key {
						elem = t.Field(i).Type
					}
				}
				if elem == nil {
					return fmt.Errorf("%sunknown field %q", where, key)
				}
			}
			if err := checkKeys(d, strings.TrimPrefix(at+"."+key, "."), elem); err != nil {
				return err
			}
		}
		_, err = d.Token()
	}
	return err
}

func (r Recipe) check(draft bool) error {
	if r.Version != recipeVersion {
		return fmt.Errorf("recasaos_widget must be %d", recipeVersion)
	}
	if err := errors.Join(
		checkText("name", r.Name, 1, maxText),
		checkText("icon", r.Icon, 0, maxText),
		checkText("base_hint", r.BaseHint, 0, maxBaseHint),
		checkPath(r.Path),
	); err != nil {
		return err
	}

	if len(r.Inputs) > maxPairs {
		return fmt.Errorf("inputs: at most %d", maxPairs)
	}
	ids := map[string]bool{}
	for i, in := range r.Inputs {
		if !inputID.MatchString(in.ID) || ids[in.ID] {
			return fmt.Errorf("inputs[%d].id %q is not a valid id, or is used twice", i, in.ID)
		}
		ids[in.ID] = true
		if err := checkText(fmt.Sprintf("inputs[%d].label", i), in.Label, 1, maxText); err != nil {
			return err
		}
	}
	if err := checkPairs("headers", r.Headers, ids); err != nil {
		return err
	}
	if err := checkPairs("query", r.Query, ids); err != nil {
		return err
	}
	if r.Basic != nil {
		if err := checkValue("basic.user", r.Basic.User, ids); err != nil {
			return err
		}
		if err := checkValue("basic.password", r.Basic.Password, ids); err != nil {
			return err
		}
		if r.Basic.Password.Text != "" {
			return errors.New("basic.password is a secret: it must be an input, not a fixed text")
		}
	}

	most := maxRows
	switch r.Layout {
	case "rows":
	case "figures":
		most = maxFigures
	default:
		return fmt.Errorf("layout %q is not rows or figures", r.Layout)
	}
	if len(r.Rows) > most {
		return fmt.Errorf("rows: at most %d in layout %s", most, r.Layout)
	}
	if len(r.Rows) == 0 && !draft {
		return errors.New("rows: at least one is needed")
	}
	for i, row := range r.Rows {
		if err := row.check(fmt.Sprintf("rows[%d]", i)); err != nil {
			return err
		}
	}
	if s := r.Status; s != nil {
		if !isPointer(s.Value) {
			return fmt.Errorf("status.value %q is not a JSON pointer", s.Value)
		}
		if err := checkShown("status", s.Format, s.Unit, s.Rules); err != nil {
			return err
		}
	}
	if r.Refresh < minRefresh || r.Refresh > maxRefresh {
		return fmt.Errorf("refresh must be %d to %d seconds", minRefresh, maxRefresh)
	}
	return nil
}

func (row RowSpec) check(at string) error {
	if !isPointer(row.Value) {
		return fmt.Errorf("%s.value %q is not a JSON pointer", at, row.Value)
	}
	if row.Each == nil {
		if err := checkText(at+".label", row.Label, 1, maxText); err != nil {
			return err
		}
		if row.Limit != 0 {
			return fmt.Errorf("%s.limit is only for a row with each", at)
		}
	} else {
		if !isPointer(*row.Each) {
			return fmt.Errorf("%s.each %q is not a JSON pointer", at, *row.Each)
		}
		if !isPointer(row.Label) {
			return fmt.Errorf("%s.label %q is not a JSON pointer", at, row.Label)
		}
		if row.Limit < 1 || row.Limit > maxLimit {
			return fmt.Errorf("%s.limit must be 1 to %d", at, maxLimit)
		}
	}
	if row.Bar && row.Format != "percent" && row.Format != "ratio" {
		return fmt.Errorf("%s.bar needs the format percent or ratio", at)
	}
	return checkShown(at, row.Format, row.Unit, row.Rules)
}

// checkShown is what a row and the status share.
func checkShown(at, format, unit string, rules []Rule) error {
	if !slices.Contains(formats, format) {
		return fmt.Errorf("%s.format %q is unknown", at, format)
	}
	if err := checkText(at+".unit", unit, 0, maxUnit); err != nil {
		return err
	}
	if len(rules) > maxRules {
		return fmt.Errorf("%s.rules: at most %d", at, maxRules)
	}
	for i, rule := range rules {
		at := fmt.Sprintf("%s.rules[%d]", at, i)
		if !slices.Contains(ops, rule.Op) {
			return fmt.Errorf("%s.op %q is unknown", at, rule.Op)
		}
		if !slices.Contains(levels, rule.Level) {
			return fmt.Errorf("%s.level %q is unknown", at, rule.Level)
		}
		switch to := rule.To.(type) {
		case float64:
		case nil, bool, string:
			if rule.Op == "lt" || rule.Op == "gt" {
				return fmt.Errorf("%s.to must be a number for %s", at, rule.Op)
			}
			if s, ok := to.(string); ok && strings.ContainsFunc(s, unicode.IsControl) {
				return fmt.Errorf("%s.to holds a control character", at)
			}
		default:
			return fmt.Errorf("%s.to must be a single value", at)
		}
		if err := checkText(at+".text", rule.Text, 0, maxText); err != nil {
			return err
		}
	}
	return nil
}

func checkText(field, s string, least, most int) error {
	if n := utf8.RuneCountInString(s); n < least || n > most {
		return fmt.Errorf("%s must be %d to %d characters", field, least, most)
	}
	if strings.ContainsFunc(s, unicode.IsControl) {
		return fmt.Errorf("%s holds a control character", field)
	}
	return nil
}

// checkPairs checks the headers or the query parameters.
func checkPairs(field string, pairs map[string]Value, ids map[string]bool) error {
	if len(pairs) > maxPairs {
		return fmt.Errorf("%s: at most %d", field, maxPairs)
	}
	seen := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(pairs)) {
		lower := strings.ToLower(name)
		if name == "" || strings.ContainsFunc(name, unicode.IsControl) {
			return fmt.Errorf("%s: %q is not a name", field, name)
		}
		if field == "headers" {
			if !headerName.MatchString(name) || seen[lower] {
				return fmt.Errorf("headers: %q is not a header name, or is set twice", name)
			}
			if slices.Contains(reservedHeaders, lower) {
				return fmt.Errorf("headers: %s cannot be set", name)
			}
			seen[lower] = true
		}
		v := pairs[name]
		if err := checkValue(field+"."+name, v, ids); err != nil {
			return err
		}
		if v.Input == "" && slices.ContainsFunc(secretNames, func(s string) bool { return strings.Contains(lower, s) }) {
			return fmt.Errorf("%s.%s is a secret: it must be an input, not a fixed text", field, name)
		}
	}
	return nil
}

func checkValue(field string, v Value, ids map[string]bool) error {
	if v.Input != "" && !ids[v.Input] {
		return fmt.Errorf("%s: input %q is not in inputs", field, v.Input)
	}
	if strings.ContainsFunc(v.Text, unicode.IsControl) {
		return fmt.Errorf("%s holds a control character", field)
	}
	return nil
}

// checkPath keeps the host out of the recipe's reach: the path goes after the
// owner's address, as written or percent-decoded.
func checkPath(path string) error {
	decoded, err := url.PathUnescape(path)
	for _, p := range []string{path, decoded} {
		if err != nil || !strings.HasPrefix(p, "/") || strings.Contains(p, "//") || strings.Contains(p, "..") ||
			strings.ContainsAny(p, `?#\`) || strings.ContainsFunc(p, unicode.IsControl) {
			return errors.New(`path must start with "/", without scheme, host, query, fragment, "//", ".." or "\"`)
		}
	}
	// Go writes another path than the one given when a character in it should be
	// escaped and is not, and then "%2F" leaves as "/"
	if (&url.URL{Path: decoded, RawPath: path}).EscapedPath() != path {
		return errors.New("path must be written percent-encoded: a space is %20, an accented letter its UTF-8 bytes")
	}
	return nil
}

// isPointer says whether s is a JSON Pointer (RFC 6901).
func isPointer(s string) bool {
	if s != "" && s[0] != '/' {
		return false
	}
	// once the two escapes are gone, no "~" may be left
	return !strings.Contains(strings.NewReplacer("~0", "", "~1", "").Replace(s), "~")
}
