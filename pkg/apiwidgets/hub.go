package apiwidgets

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/utils/file"
)

// ConfigFile holds the widgets and their secrets (root, 0600).
const ConfigFile = "/var/lib/casaos/api_widgets.json"

const (
	maxWidgets  = 12
	maxBase     = 2048
	maxSecret   = 4 << 10
	failureKept = 60 * time.Second
	idLetters   = "abcdefghijklmnopqrstuvwxyz0123456789"
)

var (
	// ErrNotFound is an id no widget has.
	ErrNotFound = errors.New("no such widget")
	// ErrSecretAgain is a change of what is sent, or of where, that came without
	// every secret: the ones kept were given for another request.
	ErrSecretAgain = fmt.Errorf("%w: the address or the request changed, every input must be filled again", ErrInvalid)

	widgetID = regexp.MustCompile(`^[a-z0-9]{8}$`)
)

// Change is what creates or replaces a widget. A secret left out, or empty, is one
// that is not given.
type Change struct {
	Base        string            `json:"base"`
	InsecureTLS bool              `json:"insecure_tls"`
	Recipe      json.RawMessage   `json:"recipe"`
	Secrets     map[string]string `json:"secrets"`
}

// View is a widget as it is shown: of its secrets, only whether each is filled.
type View struct {
	ID          string          `json:"id"`
	Base        string          `json:"base"`
	InsecureTLS bool            `json:"insecure_tls"`
	Recipe      Recipe          `json:"recipe"`
	InputsSet   map[string]bool `json:"inputs_set"`
	// why the last read failed, as long as that card is the one kept
	Error string `json:"error"`
}

// TestResult is what the address answered and the card the recipe makes of it.
type TestResult struct {
	Response json.RawMessage `json:"response"`
	Card     Card            `json:"card"`
}

// stored is a widget in ConfigFile.
type stored struct {
	ID          string            `json:"id"`
	Base        string            `json:"base"`
	InsecureTLS bool              `json:"insecure_tls"`
	Recipe      Recipe            `json:"recipe"`
	Secrets     map[string]string `json:"secrets"`
}

type config struct {
	Widgets []stored `json:"widgets"`
}

// reading is the card kept for a widget. mu is held for the time of a read, so
// that there is one at a time; the rest is under the hub's lock.
type reading struct {
	mu     sync.Mutex
	card   Card
	at     time.Time
	filled bool
}

// Hub is the API widgets of one box.
type Hub struct {
	Root string
	Now  func() time.Time

	// allowed is the rule of the addresses a widget may be read from; nil is the
	// real one (guard). Tests set it to reach the loopback.
	allowed func(netip.AddrPort) bool

	mu    sync.Mutex
	cache map[string]*reading
	// what load found: a file that gives no widget, whether its copy was tried,
	// and whether there is one
	broken, triedAside, setAside bool
}

// Default is the box's own.
var Default = New("/")

// New keeps api_widgets.json under root and reads through the guarded client.
func New(root string) *Hub {
	return &Hub{Root: root, Now: time.Now, cache: map[string]*reading{}}
}

func (h *Hub) path() string { return filepath.Join(h.Root, ConfigFile) }

func (h *Hub) rule() func(netip.AddrPort) bool {
	if h.allowed != nil {
		return h.allowed
	}
	return guard
}

func (h *Hub) httpClient(insecureTLS bool) *http.Client {
	return newClient(insecureTLS, h.rule())
}

// load reads the widgets; h.mu is held. A file that cannot be read, or that holds
// anything a widget cannot be, gives no widget and is left as it is. What could
// be read of it is copied beside it, once; save sees to the rest.
func (h *Hub) load() []stored {
	data, readErr := os.ReadFile(h.path())
	h.broken = false
	if errors.Is(readErr, os.ErrNotExist) {
		return nil
	}
	var c config
	err := readErr
	if err == nil {
		err = json.Unmarshal(data, &c)
	}
	if err == nil && slices.ContainsFunc(c.Widgets, func(w stored) bool {
		return !widgetID.MatchString(w.ID) || w.Recipe.check(false) != nil
	}) {
		err = ErrInvalid
	}
	if err != nil {
		h.broken = true
		if !h.triedAside && readErr == nil {
			h.triedAside = true
			// 0600: it holds secrets
			h.setAside = file.WriteFileAtomic(h.path()+".broken", data) == nil
		}
		return nil
	}
	return c.Widgets
}

// save writes the widgets, 0600; h.mu is held, and load was called under it. It
// never writes over a broken file that has no copy: that file is moved aside
// first, and when it cannot be, nothing is written.
func (h *Hub) save(widgets []stored) error {
	data, err := json.Marshal(config{Widgets: widgets})
	if err != nil {
		return err
	}
	if h.broken && !h.setAside {
		if err := os.Rename(h.path(), h.path()+".broken"); err != nil {
			return errors.New("the widgets file cannot be read and could not be set aside: nothing was written")
		}
	}
	if err := file.WriteFileAtomic(h.path(), data); err != nil {
		return err
	}
	h.broken, h.triedAside, h.setAside = false, false, false
	return nil
}

func find(widgets []stored, id string) int {
	return slices.IndexFunc(widgets, func(w stored) bool { return w.ID == id })
}

// view is w without its secrets; h.mu is held.
func (h *Hub) view(w stored) View {
	v := View{ID: w.ID, Base: w.Base, InsecureTLS: w.InsecureTLS, Recipe: w.Recipe, InputsSet: map[string]bool{}}
	for _, in := range w.Recipe.Inputs {
		v.InputsSet[in.ID] = w.Secrets[in.ID] != ""
	}
	if e := h.cache[w.ID]; e != nil {
		v.Error = e.card.Error
	}
	return v
}

// sameRequest says whether two widgets send the same thing to the same place: the
// address, the certificate setting, the path, the query, the headers and basic,
// with their names, their fixed values and the input at each place. Only then may
// a secret given for one be sent by the other.
func sameRequest(a, b stored) bool {
	ra, rb := a.Recipe, b.Recipe
	return a.Base == b.Base && a.InsecureTLS == b.InsecureTLS && ra.Path == rb.Path &&
		maps.Equal(ra.Query, rb.Query) && maps.Equal(ra.Headers, rb.Headers) &&
		(ra.Basic == nil) == (rb.Basic == nil) && (ra.Basic == nil || *ra.Basic == *rb.Basic)
}

// build checks a change and makes the widget of it. old is the widget it
// replaces: its secrets are kept for the inputs left out when the request is the
// same, and when it is not, every input must come again. The secrets of inputs
// the recipe no longer has are dropped. No error tells a secret.
func (h *Hub) build(c Change, old *stored, draft bool) (stored, error) {
	r, err := ParseRecipe(c.Recipe, draft)
	if err != nil {
		return stored{}, err
	}
	if len(c.Base) > maxBase {
		return stored{}, fmt.Errorf("%w: the address is too long", ErrInvalid)
	}
	next := stored{Base: c.Base, InsecureTLS: c.InsecureTLS, Recipe: r, Secrets: map[string]string{}}
	same := old != nil && sameRequest(*old, next)
	for _, in := range r.Inputs {
		secret := c.Secrets[in.ID]
		if secret == "" && same {
			secret = old.Secrets[in.ID]
		}
		switch {
		case secret == "" && old != nil && !same:
			return stored{}, ErrSecretAgain
		case secret == "":
			return stored{}, fmt.Errorf("%w: the input %q is not filled", ErrInvalid, in.ID)
		case len(secret) > maxSecret || strings.ContainsAny(secret, "\r\n\x00"):
			return stored{}, fmt.Errorf("%w: the input %q is over 4 KiB, or holds a line break or a NUL character", ErrInvalid, in.ID)
		}
		next.Secrets[in.ID] = secret
	}
	u, err := requestURL(next.Base, r, next.Secrets)
	if err != nil {
		return stored{}, fmt.Errorf("%w: %s", ErrInvalid, reason(err))
	}
	// an address written as one is judged now; a name, when it is read
	if addr, err := netip.ParseAddr(u.Hostname()); err == nil && !h.rule()(netip.AddrPortFrom(addr, 0)) {
		return stored{}, fmt.Errorf("%w: %s", ErrInvalid, errRefused)
	}
	return next, nil
}

// newID is 8 characters a-z0-9. It names a widget and protects nothing: the
// slight preference of % for the first letters does not matter.
func newID() string {
	id := make([]byte, 8)
	rand.Read(id) // never fails
	for i, b := range id {
		id[i] = idLetters[int(b)%len(idLetters)]
	}
	return string(id)
}

// List is the widgets, in the order they were created.
func (h *Hub) List() []View {
	h.mu.Lock()
	defer h.mu.Unlock()
	views := []View{}
	for _, w := range h.load() {
		views = append(views, h.view(w))
	}
	return views
}

// Create adds a widget. Every input of its recipe needs its secret.
func (h *Hub) Create(c Change) (View, error) {
	next, err := h.build(c, nil, false)
	if err != nil {
		return View{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	widgets := h.load()
	if len(widgets) >= maxWidgets {
		return View{}, fmt.Errorf("%w: at most %d widgets", ErrInvalid, maxWidgets)
	}
	for next.ID = newID(); find(widgets, next.ID) >= 0; next.ID = newID() {
	}
	if err := h.save(append(widgets, next)); err != nil {
		return View{}, err
	}
	return h.view(next), nil
}

// Update replaces a widget, and forgets its card.
func (h *Hub) Update(id string, c Change) (View, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	widgets := h.load()
	i := find(widgets, id)
	if i < 0 {
		return View{}, ErrNotFound
	}
	next, err := h.build(c, &widgets[i], false)
	if err != nil {
		return View{}, err
	}
	next.ID = id
	widgets[i] = next
	if err := h.save(widgets); err != nil {
		return View{}, err
	}
	delete(h.cache, id)
	return h.view(next), nil
}

// Delete removes a widget, its secrets and its card.
func (h *Hub) Delete(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	widgets := h.load()
	i := find(widgets, id)
	if i < 0 {
		return ErrNotFound
	}
	if err := h.save(slices.Delete(widgets, i, i+1)); err != nil {
		return err
	}
	delete(h.cache, id)
	return nil
}

// Card is the card of a widget: read when it is asked for, kept refresh seconds,
// or 60 s when the read failed (Error then says why, and there is no row). There
// is one read at a time for a widget; who asks meanwhile waits and gets its card.
func (h *Hub) Card(ctx context.Context, id string) (Card, error) {
	for {
		h.mu.Lock()
		e := h.cache[id]
		if e == nil {
			if find(h.load(), id) < 0 {
				h.mu.Unlock()
				return Card{}, ErrNotFound
			}
			e = &reading{}
			h.cache[id] = e
		}
		h.mu.Unlock()

		e.mu.Lock()
		card, replaced, err := h.read(ctx, id, e)
		e.mu.Unlock()
		if !replaced {
			return card, err
		}
		// the widget changed while this call waited: its card is another one now
	}
}

// read answers the card e keeps, or reads it; e.mu is held. replaced is an entry
// that an update or a delete took out while the caller waited for it.
func (h *Hub) read(ctx context.Context, id string, e *reading) (card Card, replaced bool, err error) {
	h.mu.Lock()
	if h.cache[id] != e {
		h.mu.Unlock()
		return Card{}, true, nil
	}
	widgets := h.load()
	i := find(widgets, id)
	if i < 0 {
		delete(h.cache, id)
		h.mu.Unlock()
		return Card{}, false, ErrNotFound
	}
	w := widgets[i]
	kept := time.Duration(w.Recipe.Refresh) * time.Second
	if e.card.Error != "" {
		kept = failureKept
	}
	if e.filled && h.Now().Sub(e.at) < kept {
		defer h.mu.Unlock()
		return e.card, false, nil
	}
	h.mu.Unlock()

	_, doc, err := fetch(ctx, h.httpClient(w.InsecureTLS), w.Base, w.Recipe, w.Secrets)
	if err != nil {
		r := w.Recipe
		card = Evaluate(Recipe{Name: r.Name, Icon: r.Icon, Layout: r.Layout, Refresh: r.Refresh}, nil)
		card.Error = reason(err)
	} else {
		card = Evaluate(w.Recipe, doc)
	}
	card.Link = w.Base

	h.mu.Lock()
	defer h.mu.Unlock()
	// not after an update or a delete, and not the failure of a caller that left
	if h.cache[id] == e && ctx.Err() == nil {
		e.card, e.at, e.filled = card, h.Now(), true
	}
	return card, false, nil
}

// Test reads the address of a change and answers what it gave, with the card the
// recipe, which may still be a draft, makes of it. With the id of a widget, the
// inputs left out take its secrets, by the rule of Update. Nothing is written and
// nothing is kept. An error of the read shows its reason only.
func (h *Hub) Test(ctx context.Context, id string, c Change) (TestResult, error) {
	var old *stored
	if id != "" {
		h.mu.Lock()
		widgets := h.load()
		h.mu.Unlock()
		i := find(widgets, id)
		if i < 0 {
			return TestResult{}, ErrNotFound
		}
		old = &widgets[i]
	}
	next, err := h.build(c, old, true)
	if err != nil {
		return TestResult{}, err
	}
	raw, doc, err := fetch(ctx, h.httpClient(next.InsecureTLS), next.Base, next.Recipe, next.Secrets)
	if err != nil {
		return TestResult{}, err
	}
	card := Evaluate(next.Recipe, doc)
	card.Link = next.Base
	return TestResult{Response: raw, Card: card}, nil
}

// Preview is the card a recipe, which may still be a draft, makes of an answer
// the caller already has. It sends nothing and needs no secret.
func (h *Hub) Preview(recipe []byte, response []byte) (Card, error) {
	r, err := ParseRecipe(recipe, true)
	if err != nil {
		return Card{}, err
	}
	if len(response) > maxAnswer {
		return Card{}, fmt.Errorf("%w: the answer is over 256 KiB", ErrInvalid)
	}
	doc, err := decode(response)
	if err != nil {
		return Card{}, fmt.Errorf("%w: %s", ErrInvalid, reason(err))
	}
	return Evaluate(r, doc), nil
}
