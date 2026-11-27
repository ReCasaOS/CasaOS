package apiwidgets

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Shown is a value ready for the card. The browser writes numbers in the reader's
// language, so only a text comes written.
type Shown struct {
	Kind   string   `json:"kind"` // text, number, bytes, percent, duration, date (Unix seconds) or missing
	Text   string   `json:"text,omitempty"`
	Number *float64 `json:"number,omitempty"`
	Unit   string   `json:"unit,omitempty"`
}

// Row is one line of the card. Bar is 0 to 100.
type Row struct {
	Label string   `json:"label"`
	Value Shown    `json:"value"`
	Level string   `json:"level,omitempty"`
	Bar   *float64 `json:"bar,omitempty"`
}

// Status is the card's chip.
type Status struct {
	Value Shown  `json:"value"`
	Level string `json:"level,omitempty"`
}

// Card is what the dashboard draws. More counts the rows left out.
type Card struct {
	Title   string  `json:"title"`
	Icon    string  `json:"icon,omitempty"`
	Layout  string  `json:"layout"`
	Status  *Status `json:"status,omitempty"`
	Rows    []Row   `json:"rows"`
	More    int     `json:"more"`
	Link    string  `json:"link,omitempty"`
	Refresh int     `json:"refresh"`
	Error   string  `json:"error,omitempty"`
}

// Evaluate reads a recipe's values in doc, a JSON answer decoded with UseNumber.
// Nothing in the answer is an error: what cannot be read is shown as missing.
// Link and Error are left to the caller.
func Evaluate(r Recipe, doc any) Card {
	card := Card{Title: clean(r.Name), Icon: r.Icon, Layout: r.Layout, Rows: []Row{}, Refresh: r.Refresh}
	if s := r.Status; s != nil {
		raw, found := lookup(doc, s.Value)
		value, level, _ := show(raw, found, s.Format, s.Unit, s.Rules)
		card.Status = &Status{value, level}
	}
	most := maxRows
	if r.Layout == "figures" {
		most = maxFigures
	}
	add := func(spec RowSpec, label string, raw any, found bool) {
		if len(card.Rows) >= most {
			card.More++
			return
		}
		if label = clean(label); label == "" {
			label = "—"
		}
		row := Row{Label: label}
		var bar *float64
		if row.Value, row.Level, bar = show(raw, found, spec.Format, spec.Unit, spec.Rules); spec.Bar {
			row.Bar = bar
		}
		card.Rows = append(card.Rows, row)
	}
	for _, spec := range r.Rows {
		if spec.Each == nil {
			raw, found := lookup(doc, spec.Value)
			add(spec, spec.Label, raw, found)
			continue
		}
		at, _ := lookup(doc, *spec.Each)
		list, ok := at.([]any)
		if !ok {
			add(spec, "", nil, false)
			continue
		}
		limit := spec.Limit
		if limit < 1 || limit > maxLimit {
			limit = maxLimit
		}
		limit = min(limit, len(list))
		for _, element := range list[:limit] {
			label, _ := lookup(element, spec.Label)
			name, _ := plain(label)
			raw, found := lookup(element, spec.Value)
			add(spec, name, raw, found)
		}
		card.More += len(list) - limit
	}
	return card
}

// show is a value in its format, the level its rules give it, and its share of
// 100 when it is a percentage. The rules look at the value as it was read.
func show(raw any, found bool, format, unit string, rules []Rule) (shown Shown, level string, bar *float64) {
	shown = Shown{Kind: "missing"}
	unit = clean(unit)
	number := func(kind string, f float64, ok bool) {
		if ok && finite(f) {
			shown = Shown{Kind: kind, Number: &f, Unit: unit}
		}
	}
	switch format {
	case "", "text":
		if s, ok := plain(raw); ok {
			shown = Shown{Kind: "text", Text: clean(s), Unit: unit}
		}
	case "number", "bytes", "percent", "duration":
		f, ok := toNumber(raw)
		number(format, f, ok)
	case "ratio":
		f, ok := toNumber(raw)
		number("percent", f*100, ok)
	case "date":
		f, ok := toDate(raw)
		number("date", f, ok)
	case "count":
		if list, ok := raw.([]any); ok {
			raw = float64(len(list)) // what a rule on a count compares
			number("number", float64(len(list)), true)
		}
	}
	if shown.Kind == "percent" {
		bar = new(min(max(*shown.Number, 0), 100))
	}
	if !found {
		return shown, "", bar
	}
	for _, rule := range rules {
		if !rule.matches(raw) {
			continue
		}
		if text := clean(rule.Text); text != "" {
			shown = Shown{Kind: "text", Text: text}
		}
		return shown, rule.Level, bar
	}
	return shown, "", bar
}

// matches compares a value as it was read. lt and gt are for numbers only.
func (rule Rule) matches(raw any) bool {
	switch rule.Op {
	case "eq":
		return same(raw, rule.To)
	case "ne":
		return !same(raw, rule.To)
	case "lt", "gt":
		a, okA := toNumber(raw)
		b, okB := float(rule.To)
		return okA && okB && (a < b) == (rule.Op == "lt") && a != b
	}
	return false
}

func same(raw, to any) bool {
	switch to := to.(type) {
	case nil:
		return raw == nil
	case bool:
		b, ok := raw.(bool)
		return ok && b == to
	case string:
		s, ok := raw.(string)
		return ok && s == to
	case float64, json.Number:
		a, okA := toNumber(raw)
		b, okB := float(to)
		return okA && okB && a == b
	}
	return false
}

// lookup follows a JSON Pointer (RFC 6901) in a decoded answer.
func lookup(doc any, pointer string) (any, bool) {
	if pointer == "" {
		return doc, true
	}
	if pointer[0] != '/' {
		return nil, false
	}
	for token := range strings.SplitSeq(pointer[1:], "/") {
		switch at := doc.(type) {
		case map[string]any:
			var ok bool
			if doc, ok = at[unescape.Replace(token)]; !ok {
				return nil, false
			}
		case []any:
			// an index is written one way only: no sign, no zero in front
			i, err := strconv.Atoi(token)
			if err != nil || i < 0 || i >= len(at) || strconv.Itoa(i) != token {
				return nil, false
			}
			doc = at[i]
		default:
			return nil, false
		}
	}
	return doc, true
}

// in one pass, so that "~01" is "~1"
var unescape = strings.NewReplacer("~1", "/", "~0", "~")

// plain is a single value as text.
func plain(v any) (string, bool) {
	switch v := v.(type) {
	case string:
		return v, true
	case json.Number:
		return string(v), true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(v), true
	}
	return "", false
}

// toNumber is a number, or a text that writes one.
func toNumber(v any) (float64, bool) {
	if s, ok := v.(string); ok {
		v = json.Number(strings.TrimSpace(s))
	}
	return float(v)
}

func float(v any) (float64, bool) {
	switch v := v.(type) {
	case float64:
		return v, finite(v)
	case json.Number:
		f, err := strconv.ParseFloat(string(v), 64)
		return f, err == nil && finite(f)
	}
	return 0, false
}

// finite keeps out what JSON cannot write.
func finite(f float64) bool { return !math.IsInf(f, 0) && !math.IsNaN(f) }

// toDate is Unix seconds, from an ISO 8601 date or from seconds.
func toDate(v any) (float64, bool) {
	if s, ok := v.(string); ok {
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", time.DateOnly} {
			if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
				return float64(t.Unix()), true
			}
		}
	}
	return toNumber(v)
}

// clean is s as one line of plain text, as the feeds do for a title, but with no
// markup read: a "<" stays a "<". Control and format characters go, among them
// the ones that turn the direction of the text.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp):
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxText {
		s = string([]rune(s)[:maxText-1]) + "…"
	}
	return s
}
