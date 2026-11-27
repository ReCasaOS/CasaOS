package apiwidgets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const otherSecret = "0ther-Xk2-secret"

// testHub is a hub in a directory of its own, whose client admits the loopback
// where httptest listens.
func testHub(t *testing.T) *Hub {
	t.Helper()
	h := New(t.TempDir())
	h.client = func(insecureTLS bool) *http.Client { return newClient(insecureTLS, anywhere) }
	return h
}

// widgetRecipe is a recipe with one input, sent in a header; edit changes it.
func widgetRecipe(t *testing.T, edit func(m map[string]any)) json.RawMessage {
	t.Helper()
	m := map[string]any{
		"recasaos_widget": 1,
		"name":            "Box",
		"path":            testPath,
		"query":           map[string]any{"fixed": "a"},
		"headers":         map[string]any{"X-Api-Key": map[string]any{"input": "k"}},
		"inputs":          []any{map[string]any{"id": "k", "label": "Key"}},
		"rows":            []any{map[string]any{"label": "N", "value": "/n"}},
		"refresh":         300,
	}
	if edit != nil {
		edit(m)
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func filled() map[string]string { return map[string]string{"k": testSecret} }

func mustCreate(t *testing.T, h *Hub, base string) View {
	t.Helper()
	v, err := h.Create(Change{Base: base, Recipe: widgetRecipe(t, nil), Secrets: filled()})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func mustCard(t *testing.T, h *Hub, id string) Card {
	t.Helper()
	card, err := h.Card(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return card
}

func readConfig(t *testing.T, h *Hub) string {
	t.Helper()
	data, err := os.ReadFile(h.path())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func answer(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }
}

// held is a server that stops inside each request until release is called, and
// says on started that one came in.
func held(t *testing.T) (srv *httptest.Server, started chan struct{}, release func(), hits func() int32) {
	t.Helper()
	started = make(chan struct{}, 100)
	gate := make(chan struct{})
	srv, n := served(t, func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-gate
		w.Write([]byte(`{"n": 1}`))
	})
	release = sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release) // before the server closes, which waits for its requests
	return srv, started, release, n.Load
}

// arrived waits for a request to reach a held server.
func arrived(t *testing.T, started chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("no request reached the server")
	}
}

func TestCreateWritesAPrivateFile(t *testing.T) {
	h := testHub(t)
	if got, _ := json.Marshal(h.List()); string(got) != "[]" {
		t.Errorf("no file: the list is %s", got)
	}
	v := mustCreate(t, h, "http://nas.lan:8080/api")
	if !regexp.MustCompile(`^[a-z0-9]{8}$`).MatchString(v.ID) {
		t.Errorf("id %q", v.ID)
	}
	if v.Base != "http://nas.lan:8080/api" || v.Recipe.Name != "Box" || v.Recipe.Refresh != 300 || !v.InputsSet["k"] || len(v.InputsSet) != 1 {
		t.Errorf("view %+v", v)
	}
	info, err := os.Stat(h.path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode %o, want 600", info.Mode().Perm())
	}
	if list := h.List(); len(list) != 1 || list[0].ID != v.ID {
		t.Errorf("list %+v", list)
	}
	// another hub on the same directory reads the same widget
	if list := New(h.Root).List(); len(list) != 1 || list[0].ID != v.ID || !list[0].InputsSet["k"] {
		t.Errorf("read again: %+v", list)
	}
}

func TestViewJSON(t *testing.T) {
	h := testHub(t)
	v, err := h.Create(Change{Base: "http://h", Recipe: widgetRecipe(t, func(m map[string]any) {
		delete(m, "headers")
		delete(m, "inputs")
	})})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	data, _ := json.Marshal(v)
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "base", "insecure_tls", "recipe", "inputs_set", "error"} {
		if _, ok := got[key]; !ok {
			t.Errorf("%s is missing in %s", key, data)
		}
	}
	if len(got) != 6 || string(got["inputs_set"]) != "{}" || string(got["error"]) != `""` {
		t.Errorf("view %s", data)
	}

	var c Change
	body := `{"base": "http://h", "insecure_tls": true, "recipe": {"a": 1}, "secrets": {"k": "v"}}`
	if err := json.Unmarshal([]byte(body), &c); err != nil || c.Base != "http://h" || !c.InsecureTLS || string(c.Recipe) != `{"a": 1}` || c.Secrets["k"] != "v" {
		t.Errorf("change %+v, %v", c, err)
	}
}

func TestAtMostTwelveWidgets(t *testing.T) {
	h := testHub(t)
	ids := map[string]bool{}
	for range maxWidgets {
		ids[mustCreate(t, h, "http://h").ID] = true
	}
	if len(ids) != 12 {
		t.Errorf("%d different ids for 12 widgets", len(ids))
	}
	_, err := h.Create(Change{Base: "http://h", Recipe: widgetRecipe(t, nil), Secrets: filled()})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "12") {
		t.Errorf("the 13th widget: %v", err)
	}
	if len(h.List()) != 12 {
		t.Errorf("%d widgets", len(h.List()))
	}
}

func TestCreateNeedsEveryInput(t *testing.T) {
	h := testHub(t)
	for _, secrets := range []map[string]string{nil, {}, {"k": ""}, {"other": "x"}} {
		_, err := h.Create(Change{Base: "http://h", Recipe: widgetRecipe(t, nil), Secrets: secrets})
		if !errors.Is(err, ErrInvalid) || errors.Is(err, ErrSecretAgain) || !strings.Contains(err.Error(), `"k"`) {
			t.Errorf("secrets %v: %v", secrets, err)
		}
	}
	if _, err := os.Stat(h.path()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused widget wrote the file: %v", err)
	}
}

func TestCreateRefused(t *testing.T) {
	h := testHub(t)
	for _, base := range []string{"", "nas.lan", "ftp://h", "http://localhost:8080", "http://u:" + otherSecret + "@h", "http://h?key=" + otherSecret, "http://" + strings.Repeat("a", maxBase)} {
		_, err := h.Create(Change{Base: base, Recipe: widgetRecipe(t, nil), Secrets: filled()})
		if !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), otherSecret) {
			t.Errorf("base %q: %v", base, err)
		}
	}
	for _, secret := range []string{"a\nb", "a\rb", "a\x00b", strings.Repeat("s", maxSecret+1)} {
		_, err := h.Create(Change{Base: "http://h", Recipe: widgetRecipe(t, nil), Secrets: map[string]string{"k": secret}})
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), `"k"`) || strings.Contains(err.Error(), secret) {
			t.Errorf("a secret of %d bytes: %v", len(secret), err)
		}
	}
	for _, recipe := range []string{``, `{}`, `{"recasaos_widget": 1, "name": "x", "path": "/x", "rows": []}`} {
		if _, err := h.Create(Change{Base: "http://h", Recipe: json.RawMessage(recipe)}); !errors.Is(err, ErrInvalid) {
			t.Errorf("recipe %q: %v", recipe, err)
		}
	}
	if len(h.List()) != 0 {
		t.Errorf("list %+v", h.List())
	}
}

// What leaves the hub, and every error it gives, is searched for the secret.
func TestNoSecretLeaves(t *testing.T) {
	status := http.StatusOK
	srv, _ := served(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(`{"n": 1}`))
	})
	h := testHub(t)
	v := mustCreate(t, h, srv.URL)
	ctx := context.Background()

	var out []any
	var errs []error
	keep := func(value any, err error) {
		out = append(out, value)
		if err != nil {
			errs = append(errs, err)
		}
	}
	out = append(out, v, h.List())
	keep(h.Card(ctx, v.ID))
	keep(h.Test(ctx, v.ID, Change{Base: srv.URL, Recipe: widgetRecipe(t, nil)}))
	keep(h.Test(ctx, "", Change{Base: srv.URL, Recipe: widgetRecipe(t, nil), Secrets: filled()}))
	keep(h.Update(v.ID, Change{Base: srv.URL, Recipe: widgetRecipe(t, func(m map[string]any) { m["name"] = "Other" })}))
	keep(h.Update(v.ID, Change{Base: srv.URL + "/v2", Recipe: widgetRecipe(t, nil)}))
	keep(h.Update(v.ID, Change{Base: "http://u:p@h", Recipe: widgetRecipe(t, nil), Secrets: filled()}))
	keep(h.Test(ctx, v.ID, Change{Base: srv.URL, InsecureTLS: true, Recipe: widgetRecipe(t, nil)}))
	keep(h.Create(Change{Base: srv.URL, Recipe: widgetRecipe(t, func(m map[string]any) {
		m["inputs"] = []any{map[string]any{"id": "k", "label": "Key"}, map[string]any{"id": "k2", "label": "Two"}}
	}), Secrets: filled()}))
	status = http.StatusInternalServerError
	keep(h.Test(ctx, v.ID, Change{Base: srv.URL, Recipe: widgetRecipe(t, func(m map[string]any) { m["name"] = "Other" })}))
	h.Now = func() time.Time { return time.Now().Add(time.Hour) }
	keep(h.Card(ctx, v.ID))
	out = append(out, h.List())
	keep(nil, h.Delete(v.ID))

	if len(errs) != 5 {
		t.Errorf("%d errors, want 5: %v", len(errs), errs)
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, e := range errs {
		text += "\n" + e.Error() + "\n" + fmt.Sprintf("%+v %#v", e, e)
	}
	if strings.Contains(text, testSecret) {
		t.Errorf("the secret is in what the hub gave:\n%s", text)
	}
	if !strings.Contains(text, "the address answered 500") || !strings.Contains(text, `"inputs_set":{"k":true}`) {
		t.Errorf("the search read the wrong text:\n%s", text)
	}
}

// A secret in the query is in the address, which a network error quotes.
func TestNoSecretLeavesInAFailedRead(t *testing.T) {
	srv, _ := served(t, answer(`{}`))
	base := srv.URL
	srv.Close() // nothing listens there any more

	h := testHub(t)
	recipe := widgetRecipe(t, func(m map[string]any) {
		delete(m, "headers")
		m["query"] = map[string]any{"api_key": map[string]any{"input": "k"}}
	})
	v, err := h.Create(Change{Base: base, Recipe: recipe, Secrets: filled()})
	if err != nil {
		t.Fatal(err)
	}
	card := mustCard(t, h, v.ID)
	_, err = h.Test(context.Background(), v.ID, Change{Base: base, Recipe: recipe})
	if err == nil || card.Error == "" || h.List()[0].Error != card.Error {
		t.Fatalf("card %+v, view %+v, test %v", card, h.List()[0], err)
	}
	discreet(t, err, base)
	shown, _ := json.Marshal(card)
	views, _ := json.Marshal(h.List())
	said := string(shown) + err.Error() + fmt.Sprint(err)
	if strings.Contains(said+string(views), testSecret) || strings.Contains(said, "api_key=") || strings.Contains(said, "hidden-path") {
		t.Errorf("a failed read tells the request: %s %s", said, views)
	}
}

func TestUpdateKeepsTheSecret(t *testing.T) {
	var key string
	srv, _ := served(t, func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("X-Api-Key")
		w.Write([]byte(`{"n": 1, "m": 2}`))
	})
	h := testHub(t)
	v := mustCreate(t, h, srv.URL)

	edits := []func(m map[string]any){
		func(m map[string]any) { m["name"] = "Renamed" },
		func(m map[string]any) {
			m["name"] = "Renamed"
			m["rows"] = []any{map[string]any{"label": "M", "value": "/m"}}
			m["refresh"] = 60
			m["icon"] = "mdi-nas"
			m["inputs"] = []any{map[string]any{"id": "k", "label": "Another label"}}
		},
	}
	for i, edit := range edits {
		// an empty secret is one that is not given
		for _, secrets := range []map[string]string{nil, {"k": ""}} {
			got, err := h.Update(v.ID, Change{Base: srv.URL, Recipe: widgetRecipe(t, edit), Secrets: secrets})
			if err != nil {
				t.Fatalf("edit %d: %v", i, err)
			}
			if got.ID != v.ID || got.Recipe.Name != "Renamed" || !got.InputsSet["k"] {
				t.Errorf("edit %d: view %+v", i, got)
			}
		}
		if card := mustCard(t, h, v.ID); card.Error != "" || key != testSecret || card.Title != "Renamed" {
			t.Errorf("edit %d: card %+v, key %q", i, card, key)
		}
	}
	if list := h.List(); len(list) != 1 || list[0].Recipe.Rows[0].Label != "M" || list[0].Recipe.Refresh != 60 {
		t.Errorf("list %+v", list)
	}

	// a secret that is given replaces the one kept
	if _, err := h.Update(v.ID, Change{Base: srv.URL, Recipe: widgetRecipe(t, nil), Secrets: map[string]string{"k": otherSecret}}); err != nil {
		t.Fatal(err)
	}
	if mustCard(t, h, v.ID); key != otherSecret {
		t.Errorf("key %q after a new secret", key)
	}
	if _, err := h.Update("nope0000", Change{Base: srv.URL, Recipe: widgetRecipe(t, nil), Secrets: filled()}); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown widget: %v", err)
	}
}

// requestChanges is every way to send the kept secret somewhere else.
func requestChanges(t *testing.T, base string) map[string]Change {
	recipe := func(edit func(m map[string]any)) json.RawMessage { return widgetRecipe(t, edit) }
	same := recipe(nil)
	input := map[string]any{"input": "k"}
	return map[string]Change{
		"base":         {Base: base + "/elsewhere", Recipe: same},
		"insecure_tls": {Base: base, InsecureTLS: true, Recipe: same},
		"path":         {Base: base, Recipe: recipe(func(m map[string]any) { m["path"] = "/elsewhere" })},
		"query value":  {Base: base, Recipe: recipe(func(m map[string]any) { m["query"] = map[string]any{"fixed": "b"} })},
		"query name":   {Base: base, Recipe: recipe(func(m map[string]any) { m["query"] = map[string]any{"other": "a"} })},
		"query input":  {Base: base, Recipe: recipe(func(m map[string]any) { m["query"] = map[string]any{"fixed": "a", "api_key": input} })},
		"header name":  {Base: base, Recipe: recipe(func(m map[string]any) { m["headers"] = map[string]any{"X-Other": input} })},
		"header added": {Base: base, Recipe: recipe(func(m map[string]any) { m["headers"] = map[string]any{"X-Api-Key": input, "X-More": "x"} })},
		"header to basic": {Base: base, Recipe: recipe(func(m map[string]any) {
			delete(m, "headers")
			m["basic"] = map[string]any{"user": "admin", "password": input}
		})},
		"basic added": {Base: base, Recipe: recipe(func(m map[string]any) { m["basic"] = map[string]any{"user": "admin", "password": input} })},
	}
}

func TestChangedRequestAsksTheSecretAgain(t *testing.T) {
	srv, hits := served(t, answer(`{"n": 1}`))
	h := testHub(t)
	v := mustCreate(t, h, srv.URL)
	before := readConfig(t, h)

	for name, c := range requestChanges(t, srv.URL) {
		for _, secrets := range []map[string]string{nil, {"k": ""}} {
			c.Secrets = secrets
			if _, err := h.Update(v.ID, c); !errors.Is(err, ErrSecretAgain) || !errors.Is(err, ErrInvalid) {
				t.Errorf("%s: update gave %v", name, err)
			}
			if _, err := h.Test(context.Background(), v.ID, c); !errors.Is(err, ErrSecretAgain) || !errors.Is(err, ErrInvalid) {
				t.Errorf("%s: test gave %v", name, err)
			}
		}
	}
	if readConfig(t, h) != before {
		t.Error("a refused update changed the file")
	}
	if hits.Load() != 0 {
		t.Errorf("a refused test sent %d requests", hits.Load())
	}

	// given again, each of them is saved
	for name, c := range requestChanges(t, srv.URL) {
		if name == "base" {
			c.Base = srv.URL + "/api"
		}
		c.Secrets = map[string]string{"k": otherSecret}
		if _, err := h.Update(v.ID, c); err != nil {
			t.Errorf("%s, the secret given: %v", name, err)
		}
		// back to the first request, which is a change too
		if _, err := h.Update(v.ID, Change{Base: srv.URL, Recipe: widgetRecipe(t, nil)}); !errors.Is(err, ErrSecretAgain) {
			t.Errorf("%s, going back: %v", name, err)
		}
		if _, err := h.Update(v.ID, Change{Base: srv.URL, Recipe: widgetRecipe(t, nil), Secrets: filled()}); err != nil {
			t.Fatal(err)
		}
	}
}

// When the request changed, an input that is given does not excuse the others.
func TestChangedRequestAsksEveryInput(t *testing.T) {
	two := func(path string) json.RawMessage {
		return widgetRecipe(t, func(m map[string]any) {
			m["path"] = path
			m["headers"] = map[string]any{"X-Api-Key": map[string]any{"input": "k"}, "X-Other": map[string]any{"input": "k2"}}
			m["inputs"] = []any{map[string]any{"id": "k", "label": "Key"}, map[string]any{"id": "k2", "label": "Two"}}
		})
	}
	h := testHub(t)
	both := map[string]string{"k": testSecret, "k2": otherSecret}
	v, err := h.Create(Change{Base: "http://h", Recipe: two("/a"), Secrets: both})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Update(v.ID, Change{Base: "http://h", Recipe: two("/b"), Secrets: filled()}); !errors.Is(err, ErrSecretAgain) {
		t.Errorf("one input of two: %v", err)
	}
	if _, err := h.Update(v.ID, Change{Base: "http://h", Recipe: two("/b"), Secrets: both}); err != nil {
		t.Errorf("both inputs: %v", err)
	}
	// the same request: one may be replaced and the other kept
	if _, err := h.Update(v.ID, Change{Base: "http://h", Recipe: two("/b"), Secrets: map[string]string{"k2": "new-one"}}); err != nil {
		t.Errorf("one replaced: %v", err)
	}
	if file := readConfig(t, h); !strings.Contains(file, testSecret) || !strings.Contains(file, "new-one") || strings.Contains(file, otherSecret) {
		t.Errorf("the file holds %s", file)
	}
}

func TestSameRequest(t *testing.T) {
	a := stored{Base: "http://h", Recipe: Recipe{Path: "/x"}}
	b := stored{Base: "http://h", Recipe: Recipe{Path: "/x", Name: "other", Query: map[string]Value{}, Headers: map[string]Value{}, Refresh: 60}}
	if !sameRequest(a, b) {
		t.Error("no query and an empty one are the same request")
	}
	withBasic := func(user, input string) stored {
		return stored{Base: "http://h", Recipe: Recipe{Path: "/x", Basic: &Basic{User: Value{Text: user}, Password: Value{Input: input}}}}
	}
	if !sameRequest(withBasic("admin", "k"), withBasic("admin", "k")) {
		t.Error("the same basic is the same request")
	}
	for _, other := range []stored{withBasic("root", "k"), withBasic("admin", "k2"), a} {
		if sameRequest(withBasic("admin", "k"), other) || sameRequest(other, withBasic("admin", "k")) {
			t.Errorf("%+v is another request", other.Recipe.Basic)
		}
	}
}

func TestTestWithAnID(t *testing.T) {
	var key string
	srv, hits := served(t, func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("X-Api-Key")
		w.Write([]byte(`{"n": 12345678901234567890}` + "\n"))
	})
	h := testHub(t)
	v := mustCreate(t, h, srv.URL)
	before := readConfig(t, h)
	ctx := context.Background()

	// a recipe still being built has no row yet
	draft := widgetRecipe(t, func(m map[string]any) { m["rows"] = []any{} })
	got, err := h.Test(ctx, v.ID, Change{Base: srv.URL, Recipe: draft})
	if err != nil {
		t.Fatal(err)
	}
	if key != testSecret {
		t.Errorf("the kept secret was not sent: %q", key)
	}
	if !bytes.Contains(got.Response, []byte("12345678901234567890")) || got.Card.Link != srv.URL || got.Card.Title != "Box" || len(got.Card.Rows) != 0 {
		t.Errorf("result %s, %+v", got.Response, got.Card)
	}
	if data, err := json.Marshal(got); err != nil || !strings.HasPrefix(string(data), `{"response":{"n":12345678901234567890},"card":{`) {
		t.Errorf("result as JSON: %s, %v", data, err)
	}

	got, err = h.Test(ctx, v.ID, Change{Base: srv.URL, Recipe: widgetRecipe(t, nil), Secrets: map[string]string{"k": otherSecret}})
	if err != nil || key != otherSecret || len(got.Card.Rows) != 1 || got.Card.Rows[0].Value.Kind != "text" {
		t.Errorf("a secret given: %v, key %q, card %+v", err, key, got.Card)
	}

	// nothing is written and nothing is kept
	if readConfig(t, h) != before {
		t.Error("a test changed the file")
	}
	if len(h.cache) != 0 || hits.Load() != 2 {
		t.Errorf("%d readings kept, %d requests", len(h.cache), hits.Load())
	}
	if mustCard(t, h, v.ID); hits.Load() != 3 || key != testSecret {
		t.Errorf("the card after a test: %d requests, key %q", hits.Load(), key)
	}

	if _, err := h.Test(ctx, "", Change{Base: srv.URL, Recipe: draft}); !errors.Is(err, ErrInvalid) || errors.Is(err, ErrSecretAgain) || !strings.Contains(err.Error(), `"k"`) {
		t.Errorf("no id and no secret: %v", err)
	}
	if _, err := h.Test(ctx, "", Change{Base: srv.URL, Recipe: draft, Secrets: filled()}); err != nil {
		t.Errorf("no id, the secret given: %v", err)
	}
	if _, err := h.Test(ctx, "nope0000", Change{Base: srv.URL, Recipe: draft, Secrets: filled()}); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown widget: %v", err)
	}
	if n := hits.Load(); n != 4 {
		t.Errorf("%d requests, want 4", n)
	}
}

func TestTestFailure(t *testing.T) {
	srv, _ := served(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) })
	h := testHub(t)
	got, err := h.Test(context.Background(), "", Change{Base: srv.URL, Recipe: widgetRecipe(t, nil), Secrets: filled()})
	if err == nil || err.Error() != "the address answered 403" || errors.Is(err, ErrInvalid) || got.Response != nil {
		t.Errorf("got %+v, %v", got, err)
	}
	discreet(t, err, srv.URL)
}

func TestRemovedInputLosesItsSecret(t *testing.T) {
	h := testHub(t)
	v, err := h.Create(Change{Base: "http://h", Secrets: map[string]string{"k": testSecret, "k2": otherSecret, "unknown": "never-kept"},
		Recipe: widgetRecipe(t, func(m map[string]any) {
			m["headers"] = map[string]any{"X-Api-Key": map[string]any{"input": "k"}, "X-Other": map[string]any{"input": "k2"}}
			m["inputs"] = []any{map[string]any{"id": "k", "label": "Key"}, map[string]any{"id": "k2", "label": "Two"}}
		})})
	if err != nil {
		t.Fatal(err)
	}
	if file := readConfig(t, h); !strings.Contains(file, testSecret) || !strings.Contains(file, otherSecret) || strings.Contains(file, "never-kept") {
		t.Fatalf("the file holds %s", file)
	}
	got, err := h.Update(v.ID, Change{Base: "http://h", Recipe: widgetRecipe(t, nil), Secrets: map[string]string{"k": testSecret, "k2": otherSecret}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.InputsSet) != 1 || !got.InputsSet["k"] {
		t.Errorf("inputs_set %v", got.InputsSet)
	}
	if file := readConfig(t, h); !strings.Contains(file, testSecret) || strings.Contains(file, otherSecret) {
		t.Errorf("the secret of the input that went is still in the file: %s", file)
	}
}

func TestBrokenFileIsSetAside(t *testing.T) {
	valid := func(t *testing.T) string {
		h := testHub(t)
		mustCreate(t, h, "http://h")
		return readConfig(t, h)
	}
	for name, content := range map[string]string{
		"not JSON":         `{"widgets": [{"id": "abcdefgh", "secrets": {"k": "` + testSecret,
		"an invalid id":    strings.Replace(valid(t), `"id":"`, `"id":"../`, 1),
		"a refused recipe": strings.Replace(valid(t), `"refresh":300`, `"refresh":5`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			h := testHub(t)
			if err := os.MkdirAll(filepath.Dir(h.path()), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(h.path(), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if got, _ := json.Marshal(h.List()); string(got) != "[]" {
				t.Errorf("the list is %s", got)
			}
			aside := h.path() + ".broken"
			copied, err := os.ReadFile(aside)
			if err != nil || string(copied) != content {
				t.Fatalf("the copy holds %q, %v", copied, err)
			}
			if info, _ := os.Stat(aside); info.Mode().Perm() != 0o600 {
				t.Errorf("mode of the copy %o, want 600", info.Mode().Perm())
			}

			// copied once: a list that is polled does not write it again
			if err := os.WriteFile(aside, []byte("marker"), 0o600); err != nil {
				t.Fatal(err)
			}
			h.List()
			if _, err := h.Card(context.Background(), "abcdefgh"); !errors.Is(err, ErrNotFound) {
				t.Errorf("card: %v", err)
			}
			if _, err := h.Update("abcdefgh", Change{Base: "http://h", Recipe: widgetRecipe(t, nil), Secrets: filled()}); !errors.Is(err, ErrNotFound) {
				t.Errorf("update: %v", err)
			}
			if err := h.Delete("abcdefgh"); !errors.Is(err, ErrNotFound) {
				t.Errorf("delete: %v", err)
			}
			if got, _ := os.ReadFile(aside); string(got) != "marker" {
				t.Errorf("the copy was written again: %q", got)
			}
			if readConfig(t, h) != content {
				t.Error("the broken file was overwritten before a widget was created")
			}

			// a widget created starts a new file; the copy stays
			v := mustCreate(t, h, "http://h")
			if list := h.List(); len(list) != 1 || list[0].ID != v.ID {
				t.Errorf("list %+v", list)
			}
			if got, _ := os.ReadFile(aside); string(got) != "marker" {
				t.Errorf("the copy after a create: %q", got)
			}

			// broken again, it is set aside again
			if err := os.WriteFile(h.path(), []byte("again"), 0o600); err != nil {
				t.Fatal(err)
			}
			if len(h.List()) != 0 {
				t.Errorf("list %+v", h.List())
			}
			if got, _ := os.ReadFile(aside); string(got) != "again" {
				t.Errorf("the second copy holds %q", got)
			}
		})
	}
}

func TestCardIsKeptRefreshSeconds(t *testing.T) {
	srv, hits := served(t, answer(`{"n": 42}`))
	h := testHub(t)
	now := time.Unix(1_800_000_000, 0)
	h.Now = func() time.Time { return now }
	v := mustCreate(t, h, srv.URL+"/")

	first := mustCard(t, h, v.ID)
	if first.Link != srv.URL+"/" || first.Title != "Box" || first.Error != "" || first.Refresh != 300 ||
		len(first.Rows) != 1 || first.Rows[0].Label != "N" || first.Rows[0].Value.Text != "42" {
		t.Errorf("card %+v", first)
	}
	// the card tells the base and no other address
	if data, _ := json.Marshal(first); strings.Count(string(data), "127.0.0.1") != 1 || strings.Contains(string(data), "hidden-path") {
		t.Errorf("card %s", data)
	}
	now = now.Add(299 * time.Second)
	if again := mustCard(t, h, v.ID); hits.Load() != 1 || again.Rows[0].Value.Text != "42" {
		t.Errorf("%d requests in less than refresh seconds", hits.Load())
	}
	now = now.Add(time.Second)
	if mustCard(t, h, v.ID); hits.Load() != 2 {
		t.Errorf("%d requests after refresh seconds, want 2", hits.Load())
	}
	if _, err := h.Card(context.Background(), "nope0000"); !errors.Is(err, ErrNotFound) || len(h.cache) != 1 {
		t.Errorf("an unknown widget: %v, %d readings kept", err, len(h.cache))
	}
}

func TestConcurrentCardsShareOneRequest(t *testing.T) {
	srv, started, release, hits := held(t)
	h := testHub(t)
	v := mustCreate(t, h, srv.URL)

	cards := make([]Card, 10)
	var wg sync.WaitGroup
	for i := range cards {
		wg.Go(func() { cards[i], _ = h.Card(context.Background(), v.ID) })
	}
	arrived(t, started)
	release()
	wg.Wait()
	if hits() != 1 {
		t.Errorf("%d requests for 10 cards at once", hits())
	}
	for i, card := range cards {
		if card.Error != "" || len(card.Rows) != 1 || card.Rows[0].Value.Text != "1" {
			t.Errorf("card %d: %+v", i, card)
		}
	}
}

func TestFailedCardIsKeptSixtySeconds(t *testing.T) {
	status := http.StatusInternalServerError
	srv, hits := served(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(`{"n": 1}`))
	})
	h := testHub(t)
	now := time.Unix(1_800_000_000, 0)
	h.Now = func() time.Time { return now }
	v := mustCreate(t, h, srv.URL) // refresh 300: longer than a failure is kept

	card := mustCard(t, h, v.ID)
	if card.Error != "the address answered 500" || card.Title != "Box" || card.Link != srv.URL || card.Layout != "rows" ||
		card.Refresh != 300 || card.Rows == nil || len(card.Rows) != 0 || card.Status != nil {
		t.Errorf("card %+v", card)
	}
	if list := h.List(); list[0].Error != "the address answered 500" {
		t.Errorf("the view's error: %q", list[0].Error)
	}
	now = now.Add(59 * time.Second)
	if again := mustCard(t, h, v.ID); hits.Load() != 1 || again.Error != card.Error {
		t.Errorf("%d requests before 60 s", hits.Load())
	}
	status = http.StatusOK
	now = now.Add(time.Second)
	if again := mustCard(t, h, v.ID); hits.Load() != 2 || again.Error != "" || len(again.Rows) != 1 {
		t.Errorf("%d requests after 60 s, card %+v", hits.Load(), again)
	}
	if list := h.List(); list[0].Error != "" {
		t.Errorf("the view's error after a good read: %q", list[0].Error)
	}
}

func TestUpdateAndDeleteEmptyTheCache(t *testing.T) {
	srv, hits := served(t, answer(`{"n": 1}`))
	h := testHub(t)
	v := mustCreate(t, h, srv.URL)

	mustCard(t, h, v.ID)
	if _, err := h.Update(v.ID, Change{Base: srv.URL, Recipe: widgetRecipe(t, func(m map[string]any) { m["name"] = "Renamed" })}); err != nil {
		t.Fatal(err)
	}
	if card := mustCard(t, h, v.ID); hits.Load() != 2 || card.Title != "Renamed" {
		t.Errorf("after an update: %d requests, title %q", hits.Load(), card.Title)
	}
	if err := h.Delete(v.ID); err != nil {
		t.Fatal(err)
	}
	if len(h.cache) != 0 {
		t.Errorf("%d readings kept by a delete", len(h.cache))
	}
	if _, err := h.Card(context.Background(), v.ID); !errors.Is(err, ErrNotFound) || len(h.cache) != 0 || hits.Load() != 2 {
		t.Errorf("after a delete: %v, %d readings kept, %d requests", err, len(h.cache), hits.Load())
	}
	if len(h.List()) != 0 {
		t.Errorf("list %+v", h.List())
	}
	if err := h.Delete(v.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted twice: %v", err)
	}
}

// A read that started before an update must not fill the cache after it.
func TestReadStartedBeforeAnUpdateIsNotKept(t *testing.T) {
	srv, started, release, hits := held(t)
	h := testHub(t)
	v := mustCreate(t, h, srv.URL)

	var early, waiting Card
	var wg sync.WaitGroup
	wg.Go(func() { early, _ = h.Card(context.Background(), v.ID) })
	arrived(t, started)
	out := h.cache[v.ID]
	if _, err := h.Update(v.ID, Change{Base: srv.URL, Recipe: widgetRecipe(t, func(m map[string]any) { m["name"] = "Renamed" })}); err != nil {
		t.Fatal(err)
	}
	// asked after the update, while the early read is still out
	wg.Go(func() { waiting, _ = h.Card(context.Background(), v.ID) })
	arrived(t, started)
	release()
	wg.Wait()
	if early.Title != "Box" || waiting.Title != "Renamed" || hits() != 2 {
		t.Errorf("titles %q and %q, %d requests", early.Title, waiting.Title, hits())
	}
	if card := mustCard(t, h, v.ID); card.Title != "Renamed" || hits() != 2 {
		t.Errorf("the card kept is %q, %d requests", card.Title, hits())
	}
	// the early read is not kept, and who waited for it goes to the new one
	if out.filled || h.cache[v.ID] == out {
		t.Errorf("the early read was kept: %+v", out.card)
	}
	if _, replaced, err := h.read(context.Background(), v.ID, out); !replaced || err != nil || hits() != 2 {
		t.Errorf("a wait on the early read: replaced %v, %v, %d requests", replaced, err, hits())
	}
}

func TestReadStartedBeforeADeleteIsNotKept(t *testing.T) {
	srv, started, release, hits := held(t)
	h := testHub(t)
	v := mustCreate(t, h, srv.URL)

	var wg sync.WaitGroup
	wg.Go(func() { h.Card(context.Background(), v.ID) })
	arrived(t, started)
	out := h.cache[v.ID]
	if err := h.Delete(v.ID); err != nil {
		t.Fatal(err)
	}
	release()
	wg.Wait()
	if out.filled || len(h.cache) != 0 {
		t.Errorf("the early read was kept: %+v, %d readings", out.card, len(h.cache))
	}
	if _, err := h.Card(context.Background(), v.ID); !errors.Is(err, ErrNotFound) || len(h.cache) != 0 || hits() != 1 {
		t.Errorf("after a delete: %v, %d readings kept, %d requests", err, len(h.cache), hits())
	}
}

// A caller that left must not leave its failure to the others for 60 s.
func TestCardOfACallerThatLeftIsNotKept(t *testing.T) {
	srv, hits := served(t, answer(`{"n": 1}`))
	h := testHub(t)
	v := mustCreate(t, h, srv.URL)

	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if card, err := h.Card(gone, v.ID); err != nil || card.Error == "" {
		t.Errorf("a caller that left: %+v, %v", card, err)
	}
	if card := mustCard(t, h, v.ID); card.Error != "" || hits.Load() != 1 {
		t.Errorf("the next caller: %+v, %d requests", card, hits.Load())
	}
	if list := h.List(); list[0].Error != "" {
		t.Errorf("the view's error: %q", list[0].Error)
	}
}

// New, untouched, reads through the real rule: the box itself is refused.
func TestNewHubIsGuarded(t *testing.T) {
	srv, hits := served(t, answer(`{"n": 1}`))
	h := New(t.TempDir())
	v := mustCreate(t, h, srv.URL)
	if card := mustCard(t, h, v.ID); card.Error != refusedMsg || len(card.Rows) != 0 {
		t.Errorf("card %+v", card)
	}
	_, err := h.Test(context.Background(), v.ID, Change{Base: srv.URL, Recipe: widgetRecipe(t, nil)})
	if !errors.Is(err, errRefused) {
		t.Errorf("test: %v", err)
	}
	// a hub that was not made by New is guarded too
	bare := &Hub{Root: h.Root, Now: time.Now, cache: map[string]*reading{}}
	if card := mustCard(t, bare, v.ID); card.Error != refusedMsg {
		t.Errorf("a bare hub: %+v", card)
	}
	if hits.Load() != 0 {
		t.Errorf("the loopback server received %d requests", hits.Load())
	}
	if Default.Root != "/" || Default.client != nil {
		t.Errorf("Default is on %q", Default.Root)
	}
}

func TestPreview(t *testing.T) {
	h := New(t.TempDir())
	h.client = func(bool) *http.Client {
		t.Error("a preview asked for a client")
		return nil
	}
	draft := `{"recasaos_widget": 1, "name": "Draft", "path": "/x", "headers": {"X-Api-Key": {"input": "k"}}, "inputs": [{"id": "k", "label": "Key"}]}`
	card, err := h.Preview([]byte(draft), []byte(`{"n": 1}`))
	if err != nil || card.Title != "Draft" || len(card.Rows) != 0 || card.Link != "" {
		t.Errorf("a draft: %+v, %v", card, err)
	}
	card, err = h.Preview(widgetRecipe(t, nil), []byte(` {"n": 12345678901234567890} `+"\n"))
	if err != nil || len(card.Rows) != 1 || card.Rows[0].Value.Text != "12345678901234567890" {
		t.Errorf("a number is read as it is written: %+v, %v", card, err)
	}

	big := `{"n": "` + strings.Repeat("a", maxAnswer) + `"}`
	for name, response := range map[string]string{"trailing data": `{"n": 1} {"n": 2}`, "not JSON": `<html>`, "empty": ``, "over 256 KiB": big} {
		if _, err := h.Preview(widgetRecipe(t, nil), []byte(response)); !errors.Is(err, ErrInvalid) || len(err.Error()) > 200 {
			t.Errorf("%s: %v", name, err)
		}
	}
	atLimit := `{"n": "` + strings.Repeat("a", maxAnswer-9) + `"}`
	if _, err := h.Preview(widgetRecipe(t, nil), []byte(atLimit)); len(atLimit) != maxAnswer || err != nil {
		t.Errorf("256 KiB exactly (%d): %v", len(atLimit), err)
	}
	if _, err := h.Preview([]byte(`{"recasaos_widget": 2}`), []byte(`{}`)); !errors.Is(err, ErrInvalid) {
		t.Errorf("a refused recipe: %v", err)
	}
	if _, err := os.Stat(h.path()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a preview wrote the file: %v", err)
	}
}
