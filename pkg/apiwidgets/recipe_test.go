package apiwidgets

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// the design's example, as written there
const immich = `{
  "recasaos_widget": 1,
  "name": "Immich",
  "icon": "media-outline",
  "path": "/api/server/statistics",
  "query": {},
  "headers": { "x-api-key": { "input": "api_key" } },
  "inputs": [ { "id": "api_key", "label": "API key" } ],
  "base_hint": "http://IP:2283",
  "layout": "figures",
  "rows": [
    { "label": "Photos", "value": "/photos", "format": "number" },
    { "label": "Videos", "value": "/videos", "format": "number" },
    { "label": "Storage", "value": "/usage", "format": "bytes" }
  ],
  "refresh": 3600
}`

// a list of the root array, with every other optional field
const list = `{
  "recasaos_widget": 1,
  "name": "Releases",
  "path": "/repos/a/b/releases",
  "query": { "per_page": "5", "t": { "input": "pw" } },
  "basic": { "user": "me", "password": { "input": "pw" } },
  "inputs": [ { "id": "pw", "label": "Password" } ],
  "layout": "rows",
  "rows": [ { "each": "", "label": "/name", "value": "/published_at", "format": "date", "limit": 5 } ],
  "status": { "value": "/0/draft", "rules": [
    { "op": "eq", "to": false, "level": "ok", "text": "UP" },
    { "op": "ne", "to": "", "level": "info" },
    { "op": "gt", "to": 0, "level": "danger" } ] },
  "refresh": 60
}`

func TestParseRecipe(t *testing.T) {
	for name, text := range map[string]string{"immich": immich, "list": list} {
		r, err := ParseRecipe([]byte(text), false)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var want, got any
		if err := json.Unmarshal([]byte(text), &want); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s: round trip gave %s", name, out)
		}
	}
}

func TestParseRecipeDefaults(t *testing.T) {
	r, err := ParseRecipe([]byte(`{"recasaos_widget":1,"name":"A","path":"/"}`), true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Refresh != 300 || r.Layout != "rows" || r.Rows == nil || len(r.Rows) != 0 {
		t.Errorf("got %+v", r)
	}
}

func TestParseRecipeRefused(t *testing.T) {
	row := func() map[string]any { return map[string]any{"label": "L", "value": "/a"} }
	rule := func() map[string]any { return map[string]any{"op": "eq", "to": "x", "level": "ok"} }
	rows := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = row()
		}
		return out
	}
	// one valid recipe, then one change to it
	base := func() map[string]any {
		return map[string]any{"recasaos_widget": 1, "name": "A", "path": "/a", "rows": rows(1)}
	}
	set := func(key string, v any) func(map[string]any) {
		return func(m map[string]any) { m[key] = v }
	}
	inRow := func(kv ...any) func(map[string]any) {
		return func(m map[string]any) {
			r := row()
			for i := 0; i < len(kv); i += 2 {
				r[kv[i].(string)] = kv[i+1]
			}
			m["rows"] = []any{r}
		}
	}
	inRule := func(key string, v any) func(map[string]any) {
		r := rule()
		r[key] = v
		return inRow("rules", []any{r})
	}
	obj := func(k string, v any) map[string]any { return map[string]any{k: v} }
	in := func(id string) map[string]any { return map[string]any{"id": id, "label": "K"} }
	long := strings.Repeat("é", 41)

	type refusal struct {
		name   string
		change func(map[string]any)
		field  string // the message must name it
	}
	cases := []refusal{
		{"unknown at root", set("extra", 1), "extra"},
		{"unknown in row", inRow("colour", "red"), "colour"},
		{"unknown in rule", inRule("blink", true), "blink"},
		{"unknown beside input", set("headers", obj("A", map[string]any{"input": "a", "x": 1})), "x"},
		{"version", set("recasaos_widget", 2), "recasaos_widget"},
		{"too big", set("base_hint", strings.Repeat("a", 16<<10)), "16 KiB"},
		{"host header", set("headers", obj("Host", "h")), "Host"},
		{"connection header", set("headers", obj("connection", "close")), "connection"},
		{"content-length header", set("headers", obj("Content-Length", "1")), "Content-Length"},
		{"transfer-encoding header", set("headers", obj("Transfer-Encoding", "chunked")), "Transfer-Encoding"},
		{"header name", set("headers", obj("Host ", "h")), "headers"},
		{"header twice", set("headers", map[string]any{"X-A": "1", "x-a": "2"}), "headers"},
		{"fixed Authorization", set("headers", obj("Authorization", "Bearer z")), "Authorization"},
		{"fixed x-api-key", set("headers", obj("x-api-key", "z")), "x-api-key"},
		{"fixed token", set("query", obj("token", "z")), "token"},
		{"fixed client_secret", set("query", obj("client_secret", "z")), "client_secret"},
		{"fixed Password", set("query", obj("Password", "z")), "Password"},
		{"fixed basic password", set("basic", map[string]any{"user": "u", "password": "z"}), "basic.password"},
		{"absent input", set("headers", obj("X-A", obj("input", "absent"))), "absent"},
		{"absent input in basic", set("basic", map[string]any{"user": obj("input", "absent")}), "absent"},
		{"empty input", set("query", obj("a", obj("input", ""))), "input"},
		{"null value", set("query", obj("a", nil)), "input"},
		{"number value", set("query", obj("a", 1)), "input"},
		{"input twice", set("inputs", []any{in("k"), in("k")}), "inputs"},
		{"input id capital", set("inputs", []any{in("K")}), "inputs"},
		{"input id digit first", set("inputs", []any{in("1k")}), "inputs"},
		{"input id long", set("inputs", []any{in(strings.Repeat("a", 33))}), "inputs"},
		{"input label", set("inputs", []any{map[string]any{"id": "k", "label": ""}}), "inputs[0].label"},
		{"9 inputs", set("inputs", []any{in("a"), in("b"), in("c"), in("d"), in("e"), in("f"), in("g"), in("h"), in("i")}), "inputs"},
		{"9 headers", set("headers", map[string]any{"a": "", "b": "", "c": "", "d": "", "e": "", "f": "", "g": "", "h": "", "i": ""}), "headers"},
		{"9 query", set("query", map[string]any{"a": "", "b": "", "c": "", "d": "", "e": "", "f": "", "g": "", "h": "", "i": ""}), "query"},
		{"control in name", set("name", "a\nb"), "name"},
		{"control in header value", set("headers", obj("X-A", "a\r\nb")), "X-A"},
		{"control in query value", set("query", obj("a", "\x00")), "query"},
		{"control in query name", set("query", obj("a\n", "b")), "query"},
		{"no name", set("name", ""), "name"},
		{"long name", set("name", long), "name"},
		{"long icon", set("icon", long), "icon"},
		{"long base_hint", set("base_hint", strings.Repeat("a", 121)), "base_hint"},
		{"layout", set("layout", "grid"), "layout"},
		{"no label", inRow("label", ""), "rows[0].label"},
		{"long label", inRow("label", long), "rows[0].label"},
		{"long unit", inRow("unit", strings.Repeat("u", 13)), "rows[0].unit"},
		{"format", inRow("format", "html"), "format"},
		{"op", inRule("op", "like"), "op"},
		{"level", inRule("level", "fatal"), "level"},
		{"lt a text", inRow("rules", []any{map[string]any{"op": "lt", "to": "3", "level": "ok"}}), "rules[0].to"},
		{"to an object", inRule("to", obj("a", 1)), "rules[0].to"},
		{"long rule text", inRule("text", long), "rules[0].text"},
		{"4 rules", inRow("rules", []any{rule(), rule(), rule(), rule()}), "rules"},
		{"9 rows", set("rows", rows(9)), "rows"},
		{"5 figures", func(m map[string]any) { m["layout"], m["rows"] = "figures", rows(5) }, "rows"},
		{"bar on text", inRow("bar", true), "bar"},
		{"limit 0 in list", inRow("each", "/a", "label", "/n"), "limit"},
		{"limit 9", inRow("each", "/a", "label", "/n", "limit", 9), "limit"},
		{"limit -1", inRow("each", "/a", "label", "/n", "limit", -1), "limit"},
		{"limit without each", inRow("limit", 2), "limit"},
		{"each pointer", inRow("each", "a", "limit", 1), "rows[0].each"},
		{"list label pointer", inRow("each", "", "limit", 1), "rows[0].label"},
		{"value pointer", inRow("value", "photos"), "rows[0].value"},
		{"value escape", inRow("value", "/a~2"), "rows[0].value"},
		{"value trailing tilde", inRow("value", "/a~"), "rows[0].value"},
		{"status pointer", set("status", obj("value", "x")), "status.value"},
		{"status format", set("status", obj("format", "html")), "status.format"},
		{"status label", set("status", obj("label", "L")), "label"},
		{"refresh 59", set("refresh", 59), "refresh"},
		{"refresh 3601", set("refresh", 3601), "refresh"},
		{"no rows", set("rows", []any{}), "rows"},
	}
	for _, p := range []string{"http://x/a", "//x", "/a/../b", "/a?x=1", "/a#f", `/a\b`, "/%2e%2e", "a", "", "/%2F%2Fx", "/a%3Fb", "/%zz", "/a\nb", "/a%00"} {
		cases = append(cases, refusal{"path " + p, set("path", p), "path"})
	}

	whole, _ := json.Marshal(base())
	if _, err := ParseRecipe(whole, false); err != nil {
		t.Fatalf("base recipe: %v", err)
	}
	check := func(name string, data []byte, draft bool, field string) {
		_, err := ParseRecipe(data, draft)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), field) {
			t.Errorf("%s: got %v, want ErrInvalid naming %q", name, err, field)
		}
	}
	for _, c := range cases {
		m := base()
		c.change(m)
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		check(c.name, data, false, c.field)
	}
	check("trailing data", append(whole, " {}"...), false, "after")
	check("trailing junk", append(whole, " x"...), false, "after")
	check("not an object", []byte(`[]`), false, "")
	check("9 rows in a draft", []byte(`{"recasaos_widget":1,"name":"A","path":"/","rows":[{},{},{},{},{},{},{},{},{}]}`), true, "rows")
}

func TestIsPointer(t *testing.T) {
	for s, want := range map[string]bool{
		"": true, "/": true, "/a/0/b": true, "/a~0b~1c": true,
		"a": false, "/a~2": false, "/a~": false, "~0": false,
	} {
		if isPointer(s) != want {
			t.Errorf("isPointer(%q) = %v", s, !want)
		}
	}
}

// the other side of the bounds: the most that is still accepted
func TestParseRecipeAtTheLimits(t *testing.T) {
	name, unit := strings.Repeat("é", 40), strings.Repeat("é", 12)
	row := `{"label":"` + name + `","value":"/a","unit":"` + unit + `","format":"percent","bar":true}`
	each := `{"each":"/a","label":"","value":"","limit":8,"format":"ratio","bar":true}`
	for layout, rows := range map[string][]string{
		"rows":    {row, row, row, row, row, row, row, each},
		"figures": {row, row, row, each},
	} {
		for _, refresh := range []string{"60", "3600"} {
			text := `{"recasaos_widget":1,"name":"` + name + `","path":"/","layout":"` + layout +
				`","rows":[` + strings.Join(rows, ",") + `],"refresh":` + refresh + `}`
			if _, err := ParseRecipe([]byte(text), false); err != nil {
				t.Errorf("%s, refresh %s: %v", layout, refresh, err)
			}
		}
	}
}
