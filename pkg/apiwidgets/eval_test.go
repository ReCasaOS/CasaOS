package apiwidgets

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// doc decodes an answer the way the reader will: numbers kept as written.
func doc(t *testing.T, s string) any {
	t.Helper()
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// one evaluates a single row and returns it.
func one(t *testing.T, answer string, row RowSpec) Row {
	t.Helper()
	card := Evaluate(Recipe{Layout: "rows", Rows: []RowSpec{row}}, doc(t, answer))
	if len(card.Rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(card.Rows))
	}
	return card.Rows[0]
}

func num(s Shown) float64 {
	if s.Number == nil {
		return -12345.678 // no number
	}
	return *s.Number
}

func each(s string) *string { return &s }

func TestEvaluateValues(t *testing.T) {
	for _, c := range []struct {
		name, answer string
		row          RowSpec
		kind         string
		number       float64
		text         string
	}{
		{"escaped pointer", `{"a/b":{"c~d":1}}`, RowSpec{Value: "/a~1b/c~0d", Format: "number"}, "number", 1, ""},
		{"~01 is ~1, not /", `{"~1":7}`, RowSpec{Value: "/~01", Format: "number"}, "number", 7, ""},
		{"array index", `{"a":[10,20]}`, RowSpec{Value: "/a/1", Format: "number"}, "number", 20, ""},
		{"array index with a zero in front", `{"a":[10,20]}`, RowSpec{Value: "/a/01", Format: "number"}, "missing", 0, ""},
		{"array index with a sign", `{"a":[10,20]}`, RowSpec{Value: "/a/+1", Format: "number"}, "missing", 0, ""},
		{"array index out of range", `{"a":[10,20]}`, RowSpec{Value: "/a/2", Format: "number"}, "missing", 0, ""},
		{"the root", `5`, RowSpec{Value: "", Format: "number"}, "number", 5, ""},
		{"bytes from a text", `{"v":"84825604096"}`, RowSpec{Value: "/v", Format: "bytes"}, "bytes", 84825604096, ""},
		{"text as number", `{"v":"abc"}`, RowSpec{Value: "/v", Format: "number"}, "missing", 0, ""},
		{"boolean as number", `{"v":true}`, RowSpec{Value: "/v", Format: "number"}, "missing", 0, ""},
		{"NaN as number", `{"v":"NaN"}`, RowSpec{Value: "/v", Format: "number"}, "missing", 0, ""},
		{"too large a number", `{"v":1e999}`, RowSpec{Value: "/v", Format: "number"}, "missing", 0, ""},
		{"absent pointer", `{"v":1}`, RowSpec{Value: "/w", Format: "text"}, "missing", 0, ""},
		{"object as text", `{"v":{}}`, RowSpec{Value: "/v", Format: "text"}, "missing", 0, ""},
		{"array as text", `{"v":[1]}`, RowSpec{Value: "/v", Format: "text"}, "missing", 0, ""},
		{"null as text", `{"v":null}`, RowSpec{Value: "/v", Format: "text"}, "missing", 0, ""},
		{"text", `{"v":"up"}`, RowSpec{Value: "/v", Format: "text"}, "text", 0, "up"},
		{"no format is text", `{"v":"up"}`, RowSpec{Value: "/v"}, "text", 0, "up"},
		{"number as text", `{"v":1.50}`, RowSpec{Value: "/v", Format: "text"}, "text", 0, "1.50"},
		{"boolean as text", `{"v":false}`, RowSpec{Value: "/v"}, "text", 0, "false"},
		{"unknown format", `{"v":"up"}`, RowSpec{Value: "/v", Format: "nope"}, "missing", 0, ""},
		{"ratio", `{"v":0.42}`, RowSpec{Value: "/v", Format: "ratio"}, "percent", 42, ""},
		{"ratio too large to multiply", `{"v":1e308}`, RowSpec{Value: "/v", Format: "ratio"}, "missing", 0, ""},
		{"percent over 100", `{"v":140}`, RowSpec{Value: "/v", Format: "percent"}, "percent", 140, ""},
		{"duration", `{"v":3600}`, RowSpec{Value: "/v", Format: "duration"}, "duration", 3600, ""},
		{"date from ISO 8601", `{"v":"2026-11-27T10:00:00Z"}`, RowSpec{Value: "/v", Format: "date"}, "date", 1795773600, ""},
		{"date with an offset", `{"v":"2026-11-27T11:00:00+01:00"}`, RowSpec{Value: "/v", Format: "date"}, "date", 1795773600, ""},
		{"date without a zone", `{"v":"2026-11-27T10:00:00"}`, RowSpec{Value: "/v", Format: "date"}, "date", 1795773600, ""},
		{"date, day only", `{"v":"2026-11-27"}`, RowSpec{Value: "/v", Format: "date"}, "date", 1795737600, ""},
		{"date from an integer", `{"v":1795773600}`, RowSpec{Value: "/v", Format: "date"}, "date", 1795773600, ""},
		{"date from other text", `{"v":"yesterday"}`, RowSpec{Value: "/v", Format: "date"}, "missing", 0, ""},
		{"count of an array", `{"v":[1,2,3]}`, RowSpec{Value: "/v", Format: "count"}, "number", 3, ""},
		{"count of an object", `{"v":{"a":1}}`, RowSpec{Value: "/v", Format: "count"}, "missing", 0, ""},
	} {
		got := one(t, c.answer, c.row).Value
		if got.Kind != c.kind || got.Text != c.text {
			t.Errorf("%s: got %s %q, want %s %q", c.name, got.Kind, got.Text, c.kind, c.text)
		}
		if has := got.Number != nil; has != (c.kind != "missing" && c.kind != "text") || has && num(got) != c.number {
			t.Errorf("%s: got the number %v, want %v", c.name, num(got), c.number)
		}
	}

	// the unit goes with the value, and the label is the recipe's
	row := one(t, `{"v":21.5}`, RowSpec{Label: "Room", Value: "/v", Format: "number", Unit: "°C"})
	if row.Label != "Room" || row.Value.Unit != "°C" || row.Level != "" || row.Bar != nil {
		t.Errorf("got %+v, want Room in °C with no level and no bar", row)
	}
	if row := one(t, `{}`, RowSpec{Label: "Room", Value: "/v", Format: "number", Unit: "°C"}); row.Value != (Shown{Kind: "missing"}) {
		t.Errorf("got %+v, want only missing", row.Value)
	}
}

func TestEvaluateBar(t *testing.T) {
	for _, c := range []struct {
		name, answer, format string
		bar                  float64
	}{
		{"ratio", `{"v":0.42}`, "ratio", 42},
		{"percent over 100", `{"v":140}`, "percent", 100},
		{"percent under 0", `{"v":-5}`, "percent", 0},
	} {
		row := one(t, c.answer, RowSpec{Value: "/v", Format: c.format, Bar: true})
		if row.Bar == nil || *row.Bar != c.bar {
			t.Errorf("%s: got the bar %v, want %v", c.name, row.Bar, c.bar)
		}
	}
	if row := one(t, `{"v":50}`, RowSpec{Value: "/v", Format: "percent"}); row.Bar != nil {
		t.Error("a bar nobody asked for")
	}
	if row := one(t, `{"v":"x"}`, RowSpec{Value: "/v", Format: "percent", Bar: true}); row.Bar != nil {
		t.Error("a bar for a missing value")
	}
}

func TestEvaluateRules(t *testing.T) {
	n := json.Number("90")
	for _, c := range []struct {
		name, answer string
		format       string
		rules        []Rule
		level, text  string
	}{
		{"gt", `{"v":95}`, "number", []Rule{{Op: "gt", To: 90.0, Level: "danger"}}, "danger", ""},
		{"gt, equal", `{"v":90}`, "number", []Rule{{Op: "gt", To: 90.0, Level: "danger"}}, "", ""},
		{"lt", `{"v":5}`, "number", []Rule{{Op: "lt", To: 90.0, Level: "ok"}}, "ok", ""},
		{"to as json.Number", `{"v":95}`, "number", []Rule{{Op: "gt", To: n, Level: "danger"}}, "danger", ""},
		{"the first true one wins", `{"v":95}`, "number", []Rule{
			{Op: "gt", To: 99.0, Level: "danger"}, {Op: "gt", To: 90.0, Level: "warning"}, {Op: "gt", To: 0.0, Level: "ok"},
		}, "warning", ""},
		{"on the raw value, before the format", `{"v":0.42}`, "ratio", []Rule{{Op: "gt", To: 40.0, Level: "danger"}, {Op: "gt", To: 0.4, Level: "warning"}}, "warning", ""},
		{"on the size, for count", `{"v":[1,2,3]}`, "count", []Rule{{Op: "eq", To: 3.0, Level: "ok"}}, "ok", ""},
		{"text replaces", `{"v":1}`, "number", []Rule{{Op: "eq", To: 1.0, Level: "ok", Text: "UP"}}, "ok", "UP"},
		{"lt on text", `{"v":"abc"}`, "text", []Rule{{Op: "lt", To: 5.0, Level: "ok"}}, "", ""},
		{"gt on text", `{"v":"abc"}`, "text", []Rule{{Op: "gt", To: 5.0, Level: "ok"}}, "", ""},
		{"gt on a number written as text", `{"v":"95"}`, "number", []Rule{{Op: "gt", To: 90.0, Level: "danger"}}, "danger", ""},
		{"gt with a text to", `{"v":95}`, "number", []Rule{{Op: "gt", To: "90", Level: "danger"}}, "", ""},
		{"eq on text", `{"v":"running"}`, "text", []Rule{{Op: "eq", To: "running", Level: "ok"}}, "ok", ""},
		{"ne on text", `{"v":"stopped"}`, "text", []Rule{{Op: "ne", To: "running", Level: "danger"}}, "danger", ""},
		{"ne, equal", `{"v":"running"}`, "text", []Rule{{Op: "ne", To: "running", Level: "danger"}}, "", ""},
		{"eq on a boolean", `{"v":true}`, "text", []Rule{{Op: "eq", To: false, Level: "danger"}, {Op: "eq", To: true, Level: "ok", Text: "Yes"}}, "ok", "Yes"},
		{"eq on null", `{"v":null}`, "text", []Rule{{Op: "eq", To: nil, Level: "warning", Text: "None"}}, "warning", "None"},
		{"eq across types", `{"v":"1"}`, "text", []Rule{{Op: "eq", To: true, Level: "ok"}}, "", ""},
		{"ne across types", `{"v":"x"}`, "text", []Rule{{Op: "ne", To: true, Level: "info"}}, "info", ""},
		{"an absent pointer has no rule", `{}`, "text", []Rule{{Op: "ne", To: "running", Level: "danger", Text: "DOWN"}}, "", ""},
		{"an object matches nothing but ne", `{"v":{}}`, "text", []Rule{{Op: "eq", To: nil, Level: "ok"}}, "", ""},
		{"an unknown op", `{"v":1}`, "number", []Rule{{Op: "ge", To: 1.0, Level: "ok"}}, "", ""},
	} {
		row := one(t, c.answer, RowSpec{Value: "/v", Format: c.format, Rules: c.rules})
		if row.Level != c.level {
			t.Errorf("%s: got the level %q, want %q", c.name, row.Level, c.level)
		}
		if c.text != "" && row.Value != (Shown{Kind: "text", Text: c.text}) {
			t.Errorf("%s: got %+v, want the text %q", c.name, row.Value, c.text)
		}
		if c.text == "" && row.Value.Kind == "text" && c.format != "text" {
			t.Errorf("%s: the value became a text", c.name)
		}
	}
	// the bar stays the number's when a rule changes the text
	row := one(t, `{"v":0.95}`, RowSpec{Value: "/v", Format: "ratio", Bar: true, Rules: []Rule{{Op: "gt", To: 0.9, Level: "danger", Text: "Full"}}})
	if row.Value.Text != "Full" || row.Bar == nil || *row.Bar != 95 {
		t.Errorf("got %+v, want Full with a bar at 95", row)
	}
}

func TestEvaluateStatus(t *testing.T) {
	answer := doc(t, `{"state":"running","load":0.42}`)
	if card := Evaluate(Recipe{Layout: "rows"}, answer); card.Status != nil {
		t.Errorf("got the status %+v, want none", card.Status)
	}
	card := Evaluate(Recipe{Status: &StatusSpec{Value: "/state", Rules: []Rule{{Op: "eq", To: "running", Level: "ok", Text: "UP"}}}}, answer)
	if card.Status == nil || card.Status.Level != "ok" || card.Status.Value != (Shown{Kind: "text", Text: "UP"}) {
		t.Errorf("got the status %+v, want UP, ok", card.Status)
	}
	card = Evaluate(Recipe{Status: &StatusSpec{Value: "/load", Format: "ratio", Unit: "x"}}, answer)
	if s := card.Status; s == nil || s.Level != "" || s.Value.Kind != "percent" || num(s.Value) != 42 || s.Value.Unit != "x" {
		t.Errorf("got the status %+v, want 42 percent", s)
	}
	card = Evaluate(Recipe{Status: &StatusSpec{Value: "/nope"}}, answer)
	if card.Status == nil || card.Status.Value.Kind != "missing" {
		t.Errorf("got the status %+v, want a missing value", card.Status)
	}
}

func TestEvaluateCard(t *testing.T) {
	card := Evaluate(Recipe{Name: "Immich", Icon: "media-outline", Layout: "figures", Refresh: 600}, nil)
	if card.Title != "Immich" || card.Icon != "media-outline" || card.Layout != "figures" || card.Refresh != 600 ||
		card.Link != "" || card.Error != "" || card.More != 0 {
		t.Errorf("got %+v", card)
	}
	data, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"title":"Immich","icon":"media-outline","layout":"figures","rows":[],"more":0,"refresh":600}`; string(data) != want {
		t.Errorf("got  %s\nwant %s", data, want)
	}
	bar := 42.0
	data, err = json.Marshal(Card{
		Status: &Status{Value: Shown{Kind: "text", Text: "UP"}, Level: "ok"},
		Rows:   []Row{{Label: "CPU", Value: Shown{Kind: "percent", Number: &bar, Unit: "u"}, Level: "info", Bar: &bar}},
		Link:   "http://nas", Error: "no answer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"title":"","layout":"","status":{"value":{"kind":"text","text":"UP"},"level":"ok"},` +
		`"rows":[{"label":"CPU","value":{"kind":"percent","number":42,"unit":"u"},"level":"info","bar":42}],` +
		`"more":0,"link":"http://nas","refresh":0,"error":"no answer"}`; string(data) != want {
		t.Errorf("got  %s\nwant %s", data, want)
	}
}

func TestEvaluateLists(t *testing.T) {
	eleven := `[{"n":"a","v":1},{"n":"b","v":2},{"n":"c","v":3},{"n":"d","v":4},{"n":"e","v":5},{"n":"f","v":6},` +
		`{"n":"g","v":7},{"n":"h","v":8},{"n":"i","v":9},{"n":"j","v":10},{"n":"k","v":11}]`
	list := RowSpec{Each: each(""), Label: "/n", Value: "/v", Format: "number"}
	simple := RowSpec{Label: "Total", Value: "/3/v", Format: "number"}

	// the root array, element by element
	card := Evaluate(Recipe{Rows: []RowSpec{list}}, doc(t, `[{"n":"a","v":1},{"n":"b","v":2}]`))
	if len(card.Rows) != 2 || card.More != 0 || card.Rows[1].Label != "b" || num(card.Rows[1].Value) != 2 {
		t.Errorf("root array: got %+v, more %d", card.Rows, card.More)
	}
	// an array inside the answer, with rules on each element
	inside := RowSpec{Each: each("/items"), Label: "/n", Value: "/v", Format: "number", Limit: 8, Rules: []Rule{{Op: "gt", To: 1.0, Level: "warning"}}}
	card = Evaluate(Recipe{Rows: []RowSpec{inside}}, doc(t, `{"items":[{"n":"a","v":1},{"n":"b","v":2}]}`))
	if len(card.Rows) != 2 || card.Rows[0].Level != "" || card.Rows[1].Level != "warning" {
		t.Errorf("inner array: got %+v", card.Rows)
	}

	for _, c := range []struct {
		name   string
		layout string
		rows   []RowSpec
		limit  int
		shown  int
		more   int
		last   string
	}{
		{"11 elements, no limit", "rows", []RowSpec{list}, 0, 8, 3, "h"},
		{"11 elements, limit 5", "rows", []RowSpec{list}, 5, 5, 6, "e"},
		{"11 elements, a limit out of range", "rows", []RowSpec{list}, -3, 8, 3, "h"},
		{"11 elements, figures", "figures", []RowSpec{list}, 8, 4, 7, "d"},
		{"two rows then a list", "rows", []RowSpec{simple, simple, list}, 8, 8, 5, "f"},
		{"two rows then a list, figures", "figures", []RowSpec{simple, simple, list}, 8, 4, 9, "b"},
		{"a list then two rows", "rows", []RowSpec{list, simple, simple}, 7, 8, 5, "Total"},
		{"a full list then two rows", "rows", []RowSpec{list, simple, simple}, 8, 8, 5, "h"},
		{"five rows in figures", "figures", []RowSpec{simple, simple, simple, simple, simple}, 0, 4, 1, "Total"},
		{"no layout is rows", "", []RowSpec{list}, 8, 8, 3, "h"},
	} {
		rows := make([]RowSpec, len(c.rows))
		for i, row := range c.rows {
			if row.Each != nil {
				row.Limit = c.limit
			}
			rows[i] = row
		}
		card := Evaluate(Recipe{Layout: c.layout, Rows: rows}, doc(t, eleven))
		if len(card.Rows) != c.shown || card.More != c.more {
			t.Errorf("%s: got %d rows and %d more, want %d and %d", c.name, len(card.Rows), card.More, c.shown, c.more)
			continue
		}
		if last := card.Rows[len(card.Rows)-1].Label; last != c.last {
			t.Errorf("%s: the last row is %q, want %q", c.name, last, c.last)
		}
	}

	// not an array: one row that says so
	for _, answer := range []string{`{"items":{"a":1}}`, `{"items":"x"}`, `{"items":null}`, `{}`} {
		card := Evaluate(Recipe{Rows: []RowSpec{inside}}, doc(t, answer))
		if len(card.Rows) != 1 || card.Rows[0].Label != "—" || card.Rows[0].Value != (Shown{Kind: "missing"}) || card.More != 0 {
			t.Errorf("each on %s: got %+v, more %d", answer, card.Rows, card.More)
		}
	}
	// an empty array: no row
	if card := Evaluate(Recipe{Rows: []RowSpec{inside}}, doc(t, `{"items":[]}`)); len(card.Rows) != 0 || card.Rows == nil || card.More != 0 {
		t.Errorf("empty array: got %+v, more %d", card.Rows, card.More)
	}
	// elements that are not what the row expects
	card = Evaluate(Recipe{Rows: []RowSpec{list}}, doc(t, `[1,"x",null,{"n":7,"v":"2"},{"n":{},"v":[]},{"n":" \n ","v":3}]`))
	want := []struct {
		label, kind string
	}{{"—", "missing"}, {"—", "missing"}, {"—", "missing"}, {"7", "number"}, {"—", "missing"}, {"—", "number"}}
	if len(card.Rows) != len(want) {
		t.Fatalf("odd elements: got %d rows, want %d", len(card.Rows), len(want))
	}
	for i, w := range want {
		if card.Rows[i].Label != w.label || card.Rows[i].Value.Kind != w.kind {
			t.Errorf("odd element %d: got %q %s, want %q %s", i, card.Rows[i].Label, card.Rows[i].Value.Kind, w.label, w.kind)
		}
	}
	// an array of scalars, each read at its own root
	card = Evaluate(Recipe{Rows: []RowSpec{{Each: each("/tags"), Label: "", Value: "", Limit: 8}}}, doc(t, `{"tags":["a","b"]}`))
	if len(card.Rows) != 2 || card.Rows[1].Label != "b" || card.Rows[1].Value.Text != "b" {
		t.Errorf("scalars: got %+v", card.Rows)
	}
}

func TestEvaluateCleansText(t *testing.T) {
	long := strings.Repeat("é", 60)
	for _, c := range []struct{ name, in, want string }{
		{"60 accented characters", long, strings.Repeat("é", 39) + "…"},
		{"40 characters stay", long[:80], strings.Repeat("é", 40)},
		{"markup stays text, controls and direction marks go", "<img src=x>\u202e\u0000ok", "<img src=x>ok"},
		{"entities are not decoded", "a &lt; b &amp; c", "a &lt; b &amp; c"},
		{"spaces collapse", "  a\t\n b\u00a0\u2028c  ", "a b c"},
		{"other format characters", "a\u200b\u200d\ufeff\u2066b\u2069\u00ad", "ab"},
		{"an escape sequence", "\x1b[31mred", "[31mred"},
	} {
		answer := map[string]any{"v": c.in, "items": []any{map[string]any{"n": c.in, "v": json.Number("1")}}}
		card := Evaluate(Recipe{
			Name:   c.in,
			Rows:   []RowSpec{{Label: c.in, Value: "/v"}, {Each: each("/items"), Label: "/n", Value: "/v", Limit: 1}},
			Status: &StatusSpec{Value: "/v", Rules: []Rule{{Op: "ne", To: nil, Level: "ok", Text: c.in}}},
		}, answer)
		for where, got := range map[string]string{
			"value": card.Rows[0].Value.Text, "label": card.Rows[0].Label, "list label": card.Rows[1].Label,
			"title": card.Title, "rule text": card.Status.Value.Text,
		} {
			if got != c.want {
				t.Errorf("%s, %s: got %q, want %q", c.name, where, got, c.want)
			}
			if n := utf8.RuneCountInString(got); n > 40 {
				t.Errorf("%s, %s: %d characters", c.name, where, n)
			}
		}
	}
	// the unit is the recipe's own text, and cleaned all the same
	if got := one(t, `{"v":1}`, RowSpec{Value: "/v", Format: "number", Unit: "k" + string(rune(0x202e)) + "m"}).Value.Unit; got != "km" {
		t.Errorf("got the unit %q, want km", got)
	}
	// a number written at length is cut like any text
	if got := one(t, `{"v":1`+strings.Repeat("0", 60)+`}`, RowSpec{Value: "/v"}).Value.Text; utf8.RuneCountInString(got) != 40 || !strings.HasSuffix(got, "…") {
		t.Errorf("got %q, want 40 characters ending with …", got)
	}
}

// Whatever the answer holds, the card is built and can be sent.
func TestEvaluateOddDocuments(t *testing.T) {
	deep := strings.Repeat(`{"a":[`, 2000) + `1` + strings.Repeat(`]}`, 2000)
	pointer := strings.Repeat("/a/0", 2000)
	var rows []RowSpec
	for _, format := range append(formats, "nope") {
		for _, value := range []string{"", "/a", "/a/0", "/a/b/c", "/0", "/-", "/a/-1", "/a/99999999999999999999", "no slash", "/~", pointer} {
			rules := []Rule{
				{Op: "lt", To: 1.0, Level: "ok"}, {Op: "gt", To: "x", Level: "ok"}, {Op: "eq", To: nil, Level: "ok"},
				{Op: "ne", To: []any{1}, Level: "ok"}, {Op: "eq", To: map[string]any{}, Level: "ok"}, {Op: "", Level: "ok"},
			}
			rows = append(rows,
				RowSpec{Label: "x", Value: value, Format: format, Rules: rules, Bar: true},
				RowSpec{Each: each(value), Label: value, Value: value, Format: format, Rules: rules, Bar: true, Limit: 99},
			)
		}
	}
	answers := []any{nil, true, 1, 1.5, int64(3), "x", []string{"a"}, map[string]int{"a": 1}, struct{}{}, []any{nil, nil}}
	for _, s := range []string{
		`null`, `true`, `0`, `-1.5e300`, `1e999`, `""`, `"x"`, `[]`, `{}`, `[[]]`, `[null]`, `[1,"a",true,null,{},[]]`,
		`{"a":null}`, `{"a":[]}`, `{"a":{}}`, `{"a":"x"}`, `{"a":[null]}`, `{"a":["NaN","Inf","-Inf","1e999",1e999,-1e999]}`,
		`{"a":{"b":{"c":[1]}}}`, `{"a":[{"a":[{"a":"2026-13-45T99:99:99Z"}]}]}`, `{"":{"":1}}`, `{"a":[1e308]}`, deep,
	} {
		answers = append(answers, doc(t, s))
		var plain any // decoded without UseNumber
		if err := json.Unmarshal([]byte(s), &plain); err == nil {
			answers = append(answers, plain)
		}
	}
	for i, answer := range answers {
		for _, layout := range []string{"rows", "figures", "", "nope"} {
			for from := 0; from < len(rows); from += 7 {
				r := Recipe{Layout: layout, Rows: rows[from:min(from+7, len(rows))], Status: &StatusSpec{Value: rows[from].Value, Format: rows[from].Format, Rules: rows[from].Rules}}
				card := Evaluate(r, answer)
				if _, err := json.Marshal(card); err != nil {
					t.Fatalf("answer %d, rows from %d: the card cannot be sent: %v", i, from, err)
				}
				if most := map[bool]int{true: 4, false: 8}[layout == "figures"]; len(card.Rows) > most || card.More < 0 {
					t.Fatalf("answer %d, rows from %d: %d rows and %d more", i, from, len(card.Rows), card.More)
				}
			}
		}
	}
	// the deep value is reached
	if got := one(t, deep, RowSpec{Value: pointer, Format: "number"}).Value; num(got) != 1 {
		t.Errorf("got %+v at the bottom, want 1", got)
	}
	if card := Evaluate(Recipe{}, nil); card.Rows == nil || len(card.Rows) != 0 {
		t.Errorf("got the rows %#v, want an empty list", card.Rows)
	}
}
