package v1

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/apiwidgets"
	"github.com/labstack/echo/v4"
)

const widgetSecret = "s3cr3t-Zq9"

// useWidgetsFixture points the handlers at a box under a temporary root.
func useWidgetsFixture(t *testing.T) *apiwidgets.Hub {
	t.Helper()
	original := apiwidgets.Default
	apiwidgets.Default = apiwidgets.New(t.TempDir())
	t.Cleanup(func() { apiwidgets.Default = original })
	return apiwidgets.Default
}

// widgetChange is the body of a create: one input, sent in a header; edit changes
// the recipe.
func widgetChange(t *testing.T, base string, secret string, edit func(recipe map[string]any)) string {
	t.Helper()
	recipe := map[string]any{
		"recasaos_widget": 1,
		"name":            "Box",
		"path":            "/hidden-path/v1",
		"headers":         map[string]any{"X-Api-Key": map[string]any{"input": "k"}},
		"inputs":          []any{map[string]any{"id": "k", "label": "Key"}},
		"rows":            []any{map[string]any{"label": "N", "value": "/n"}},
		"refresh":         300,
	}
	if edit != nil {
		edit(recipe)
	}
	change := map[string]any{"base": base, "recipe": recipe}
	if secret != "" {
		change["secrets"] = map[string]string{"k": secret}
	}
	data, err := json.Marshal(change)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type widgetReply struct {
	code    int
	message string
	data    json.RawMessage
	body    string
}

func callWidgets(t *testing.T, handler echo.HandlerFunc, method, id, body string) widgetReply {
	t.Helper()
	request := httptest.NewRequest(method, "/v1/sys/widgets", strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	if id != "" {
		ctx.SetParamNames("id")
		ctx.SetParamValues(id)
	}
	if err := handler(ctx); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	var response struct {
		Success int             `json:"success"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v\n%s", err, recorder.Body.String())
	}
	if response.Success != recorder.Code {
		t.Fatalf("success = %d, status = %d", response.Success, recorder.Code)
	}
	return widgetReply{recorder.Code, response.Message, response.Data, recorder.Body.String()}
}

func createWidget(t *testing.T, base string) apiwidgets.View {
	t.Helper()
	got := callWidgets(t, PostAPIWidget, http.MethodPost, "", widgetChange(t, base, widgetSecret, nil))
	if got.code != http.StatusOK {
		t.Fatalf("create = %d %s", got.code, got.body)
	}
	var view apiwidgets.View
	if err := json.Unmarshal(got.data, &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func listWidgets(t *testing.T) []apiwidgets.View {
	t.Helper()
	got := callWidgets(t, GetAPIWidgets, http.MethodGet, "", "")
	if got.code != http.StatusOK {
		t.Fatalf("list = %d %s", got.code, got.body)
	}
	var list struct {
		Widgets []apiwidgets.View `json:"widgets"`
	}
	if err := json.Unmarshal(got.data, &list); err != nil || list.Widgets == nil {
		t.Fatalf("list data %s: %v", got.data, err)
	}
	return list.Widgets
}

func TestAPIWidgetsCreateListUpdateDelete(t *testing.T) {
	useWidgetsFixture(t)
	if got := callWidgets(t, GetAPIWidgets, http.MethodGet, "", ""); got.code != http.StatusOK || string(got.data) != `{"widgets":[]}` {
		t.Fatalf("an empty list = %d %s", got.code, got.data)
	}

	created := callWidgets(t, PostAPIWidget, http.MethodPost, "", widgetChange(t, "http://h", widgetSecret, nil))
	var view apiwidgets.View
	if err := json.Unmarshal(created.data, &view); err != nil {
		t.Fatalf("create = %d %s", created.code, created.body)
	}
	if created.code != http.StatusOK || !widgetIDPattern.MatchString(view.ID) || view.Base != "http://h" || !view.InputsSet["k"] || view.Recipe.Name != "Box" {
		t.Errorf("create = %d %s", created.code, created.body)
	}
	if strings.Contains(created.body, widgetSecret) {
		t.Errorf("the answer of a create tells the secret: %s", created.body)
	}
	if list := listWidgets(t); len(list) != 1 || list[0].ID != view.ID {
		t.Errorf("list %+v", list)
	}

	// the same request, the input left out: its secret is kept
	renamed := widgetChange(t, "http://h", "", func(recipe map[string]any) { recipe["name"] = "Renamed" })
	updated := callWidgets(t, PutAPIWidget, http.MethodPut, view.ID, renamed)
	if updated.code != http.StatusOK || !strings.Contains(string(updated.data), `"name":"Renamed"`) || !strings.Contains(string(updated.data), `"k":true`) {
		t.Errorf("update = %d %s", updated.code, updated.body)
	}

	// another address, the input left out: every input again, and the dashboard can tell
	moved := callWidgets(t, PutAPIWidget, http.MethodPut, view.ID, widgetChange(t, "http://other", "", nil))
	if moved.code != http.StatusBadRequest || string(moved.data) != `{"secrets_again":true}` || !strings.Contains(moved.message, "filled again") {
		t.Errorf("an update to another address without the secret = %d %s", moved.code, moved.body)
	}
	if list := listWidgets(t); len(list) != 1 || list[0].Base != "http://h" {
		t.Errorf("list after a refused update %+v", list)
	}

	if got := callWidgets(t, DeleteAPIWidget, http.MethodDelete, view.ID, ""); got.code != http.StatusOK {
		t.Errorf("delete = %d %s", got.code, got.body)
	}
	if list := listWidgets(t); len(list) != 0 {
		t.Errorf("list after a delete %+v", list)
	}
}

func TestAPIWidgetsUnknownID(t *testing.T) {
	hub := useWidgetsFixture(t)
	createWidget(t, "http://h")
	change := widgetChange(t, "http://h", widgetSecret, nil)
	// no widget has the first; the others are not ids at all, and never reach the hub
	for _, id := range []string{"zzzzzzzz", "ABCDEFGH", "abc", "abcdefghi", "../../etc"} {
		for name, got := range map[string]widgetReply{
			"update": callWidgets(t, PutAPIWidget, http.MethodPut, id, change),
			"delete": callWidgets(t, DeleteAPIWidget, http.MethodDelete, id, ""),
			"card":   callWidgets(t, GetAPIWidgetCard, http.MethodGet, id, ""),
		} {
			if got.code != http.StatusNotFound || got.message != "no such widget" {
				t.Errorf("%s %q = %d %s", name, id, got.code, got.body)
			}
		}
	}
	// the id is judged first: a body that would be a 400 is not even read
	if got := callWidgets(t, PutAPIWidget, http.MethodPut, "../../etc", "{"); got.code != http.StatusNotFound {
		t.Errorf("update of what is not an id, with a body that is not JSON = %d %s", got.code, got.body)
	}
	withID := strings.Replace(change, "{", `{"id":"zzzzzzzz",`, 1)
	if got := callWidgets(t, PostAPIWidgetTest, http.MethodPost, "", withID); got.code != http.StatusNotFound {
		t.Errorf("test with an unknown id = %d %s", got.code, got.body)
	}
	if list := hub.List(); len(list) != 1 {
		t.Errorf("list %+v", list)
	}
}

func TestAPIWidgetsInvalid(t *testing.T) {
	useWidgetsFixture(t)
	tooOften := widgetChange(t, "http://h", widgetSecret, func(recipe map[string]any) { recipe["refresh"] = 5 })
	for name, handler := range map[string]echo.HandlerFunc{"create": PostAPIWidget, "test": PostAPIWidgetTest} {
		// the field is named
		if got := callWidgets(t, handler, http.MethodPost, "", tooOften); got.code != http.StatusBadRequest || !strings.Contains(got.message, "refresh") || string(got.data) != "null" {
			t.Errorf("%s with a refused recipe = %d %s", name, got.code, got.body)
		}
		// what is not JSON is not quoted
		got := callWidgets(t, handler, http.MethodPost, "", `{"base": "http://h", "secrets": {"k": "`+widgetSecret)
		if got.code != http.StatusBadRequest || got.message != "the body is not JSON" || strings.Contains(got.body, widgetSecret) {
			t.Errorf("%s with a body that is not JSON = %d %s", name, got.code, got.body)
		}
	}
	// the box's own loopback, written as an address, is refused before anything is read
	if got := callWidgets(t, PostAPIWidget, http.MethodPost, "", widgetChange(t, "http://127.0.0.1:1", widgetSecret, nil)); got.code != http.StatusBadRequest || !strings.Contains(got.message, "refused") {
		t.Errorf("create at the loopback = %d %s", got.code, got.body)
	}
	if list := listWidgets(t); len(list) != 0 {
		t.Errorf("list %+v", list)
	}
}

// widgetServer listens on an address of this machine that is not its loopback,
// which a widget is never read from, and says what key each request carried.
func widgetServer(t *testing.T, handler http.HandlerFunc) (base string, keys func() []string) {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu   sync.Mutex
		seen []string
	)
	for _, addr := range addrs {
		ip, ok := addr.(*net.IPNet)
		if !ok || ip.IP.To4() == nil || !ip.IP.IsGlobalUnicast() {
			continue
		}
		listener, err := net.Listen("tcp", net.JoinHostPort(ip.IP.String(), "0"))
		if err != nil {
			continue
		}
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen = append(seen, r.Header.Get("X-Api-Key"))
			mu.Unlock()
			handler(w, r)
		}))
		srv.Listener.Close()
		srv.Listener = listener
		srv.Start()
		t.Cleanup(srv.Close)
		return srv.URL, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return slices.Clone(seen)
		}
	}
	t.Skip("this machine has no address but its loopback")
	return "", nil
}

func TestAPIWidgetsTestAndCard(t *testing.T) {
	useWidgetsFixture(t)
	base, keys := widgetServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"n": 42}`)) })

	tested := callWidgets(t, PostAPIWidgetTest, http.MethodPost, "", widgetChange(t, base, widgetSecret, nil))
	var result apiwidgets.TestResult
	if err := json.Unmarshal(tested.data, &result); err != nil {
		t.Fatalf("test = %d %s", tested.code, tested.body)
	}
	if tested.code != http.StatusOK || string(result.Response) != `{"n":42}` || len(result.Card.Rows) != 1 || result.Card.Rows[0].Value.Text != "42" {
		t.Errorf("test = %d %s", tested.code, tested.body)
	}
	if list := listWidgets(t); len(list) != 0 {
		t.Errorf("a test wrote a widget: %+v", list)
	}

	view := createWidget(t, base)
	// with the id of the widget, the input left out takes its secret
	again := strings.Replace(widgetChange(t, base, "", nil), "{", `{"id":"`+view.ID+`",`, 1)
	if got := callWidgets(t, PostAPIWidgetTest, http.MethodPost, "", again); got.code != http.StatusOK {
		t.Errorf("test with the id of a widget = %d %s", got.code, got.body)
	}
	got := callWidgets(t, GetAPIWidgetCard, http.MethodGet, view.ID, "")
	var card apiwidgets.Card
	if err := json.Unmarshal(got.data, &card); err != nil {
		t.Fatalf("card = %d %s", got.code, got.body)
	}
	if got.code != http.StatusOK || card.Error != "" || card.Link != base || len(card.Rows) != 1 || card.Rows[0].Value.Text != "42" {
		t.Errorf("card = %d %s", got.code, got.body)
	}
	if sent := keys(); !slices.Equal(sent, []string{widgetSecret, widgetSecret, widgetSecret}) {
		t.Errorf("the address was sent %q", sent)
	}
	for _, body := range []string{tested.body, got.body} {
		if strings.Contains(body, widgetSecret) {
			t.Errorf("an answer tells the secret: %s", body)
		}
	}
}

// A read that fails is the owner's to see: 400 with its reason in test, and a card
// that says so, answered 200, on the dashboard.
func TestAPIWidgetsFailedRead(t *testing.T) {
	useWidgetsFixture(t)
	base, _ := widgetServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) })
	address, _ := url.Parse(base)

	tested := callWidgets(t, PostAPIWidgetTest, http.MethodPost, "", widgetChange(t, base, widgetSecret, nil))
	if tested.code != http.StatusBadRequest || tested.message != "the address answered 403" || string(tested.data) != "null" {
		t.Errorf("test = %d %s", tested.code, tested.body)
	}
	for _, hidden := range []string{widgetSecret, address.Hostname(), "hidden-path"} {
		if strings.Contains(tested.body, hidden) {
			t.Errorf("the answer of a failed test tells %q: %s", hidden, tested.body)
		}
	}

	view := createWidget(t, base)
	got := callWidgets(t, GetAPIWidgetCard, http.MethodGet, view.ID, "")
	var card apiwidgets.Card
	if err := json.Unmarshal(got.data, &card); err != nil {
		t.Fatalf("card = %d %s", got.code, got.body)
	}
	if got.code != http.StatusOK || card.Error != "the address answered 403" || card.Title != "Box" || len(card.Rows) != 0 {
		t.Errorf("card = %d %s", got.code, got.body)
	}
	if strings.Contains(got.body, widgetSecret) {
		t.Errorf("the card tells the secret: %s", got.body)
	}
}

func TestAPIWidgetsPreview(t *testing.T) {
	useWidgetsFixture(t)
	body := func(response string) string {
		var change struct {
			Recipe json.RawMessage `json:"recipe"`
		}
		if err := json.Unmarshal([]byte(widgetChange(t, "", "", nil)), &change); err != nil {
			t.Fatal(err)
		}
		return `{"recipe":` + string(change.Recipe) + `,"response":` + response + `}`
	}
	got := callWidgets(t, PostAPIWidgetPreview, http.MethodPost, "", body(`{"n": 42}`))
	var card apiwidgets.Card
	if err := json.Unmarshal(got.data, &card); err != nil {
		t.Fatalf("preview = %d %s", got.code, got.body)
	}
	if got.code != http.StatusOK || card.Title != "Box" || len(card.Rows) != 1 || card.Rows[0].Value.Text != "42" {
		t.Errorf("preview = %d %s", got.code, got.body)
	}

	// an answer over the 32 KiB of the other routes, and under the 256 KiB of an answer
	large := `{"n": 42, "pad": "` + strings.Repeat("a", 200<<10) + `"}`
	if got := callWidgets(t, PostAPIWidgetPreview, http.MethodPost, "", body(large)); got.code != http.StatusOK {
		t.Errorf("preview of a 200 KiB answer = %d %s", got.code, got.message)
	}
	if got := callWidgets(t, PostAPIWidgetPreview, http.MethodPost, "", `{"recipe": {"recasaos_widget": 2}, "response": {}}`); got.code != http.StatusBadRequest || got.message == "" {
		t.Errorf("preview of a refused recipe = %d %s", got.code, got.body)
	}
	if got := callWidgets(t, PostAPIWidgetPreview, http.MethodPost, "", `{"recipe": `); got.code != http.StatusBadRequest || got.message != "the body is not JSON" {
		t.Errorf("preview of a body that is not JSON = %d %s", got.code, got.body)
	}
	if list := listWidgets(t); len(list) != 0 {
		t.Errorf("a preview wrote a widget: %+v", list)
	}
}

// A body over the limit is refused whole: it is never read in part.
func TestAPIWidgetsBodyLimit(t *testing.T) {
	useWidgetsFixture(t)
	view := createWidget(t, "http://h")
	// valid JSON whose first 32 KiB would be a change if the rest were dropped
	padded := func(size int) string {
		change := widgetChange(t, "http://h", widgetSecret, nil)
		return strings.TrimSuffix(change, "}") + `,"pad":"` + strings.Repeat("a", size) + `"}`
	}
	for name, got := range map[string]widgetReply{
		"create":  callWidgets(t, PostAPIWidget, http.MethodPost, "", padded(32<<10)),
		"update":  callWidgets(t, PutAPIWidget, http.MethodPut, view.ID, padded(32<<10)),
		"test":    callWidgets(t, PostAPIWidgetTest, http.MethodPost, "", padded(32<<10)),
		"preview": callWidgets(t, PostAPIWidgetPreview, http.MethodPost, "", padded(320<<10)),
	} {
		if got.code != http.StatusRequestEntityTooLarge || got.message != "the body is too large" {
			t.Errorf("%s over the limit = %d %s", name, got.code, got.message)
		}
	}
	// under it, the same body is taken: what it holds besides a change is not read
	if got := callWidgets(t, PostAPIWidget, http.MethodPost, "", padded(30<<10)); got.code != http.StatusOK {
		t.Errorf("create under the limit = %d %s", got.code, got.message)
	}
	if list := listWidgets(t); len(list) != 2 {
		t.Errorf("list %+v", list)
	}
}

// An error that is not the caller's is answered 500 with a text written here: the
// hub's may name a file.
func TestAPIWidgetsInternalError(t *testing.T) {
	hub := useWidgetsFixture(t)
	file := filepath.Join(hub.Root, apiwidgets.ConfigFile)
	// a broken file that cannot be set aside: something stands where it would go
	if err := os.MkdirAll(filepath.Join(file+".broken", "in the way"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := callWidgets(t, PostAPIWidget, http.MethodPost, "", widgetChange(t, "http://h", widgetSecret, nil))
	if got.code != http.StatusInternalServerError || got.message != "the widgets could not be read or written" || string(got.data) != "null" {
		t.Errorf("create = %d %s", got.code, got.body)
	}
	if strings.Contains(got.body, hub.Root) || strings.Contains(got.body, widgetSecret) {
		t.Errorf("the answer tells too much: %s", got.body)
	}
}
