// Package feeds keeps the RSS and Atom feeds the owner chose for the dashboard's
// footer (CasaOS#180) and their latest headlines. A box whose owner never saved
// the settings shows the distribution's own releases (DefaultFeed); turned off, or
// with no feed, it fetches nothing. Reading the headlines never
// waits on the network: it answers what is cached and, when that is older than
// refreshEvery, starts a refresh in the background. Feeds are fetched by the box,
// never toward an address of the box itself or of its networks (guard.go).
package feeds

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/utils/file"
)

// ConfigFile holds the switch and the feeds (root, 0600: a private feed's URL can
// carry its token).
const ConfigFile = "/var/lib/casaos/feeds.json"

const (
	maxFeeds        = 10
	maxURLLength    = 2048
	maxItemsPerFeed = 10
	refreshEvery    = 30 * time.Minute
)

// DefaultFeed is what a box whose owner never saved the settings shows: the
// distribution's releases.
const DefaultFeed = "https://github.com/ReCasaOS/CasaOS-Install/releases.atom"

// ErrInvalid is a PUT body that is not one: too many feeds, or one that is not an
// http(s) URL. Nothing is written.
var ErrInvalid = errors.New("invalid feeds settings")

// Config is feeds.json.
type Config struct {
	Enabled bool     `json:"enabled"`
	Feeds   []string `json:"feeds"`
}

// Item is one headline: its title as plain text, an http(s) link, and the feed it
// came from.
type Item struct {
	Title  string `json:"title"`
	Link   string `json:"link"`
	Source string `json:"source"`
}

// Status is what GET and PUT /v1/sys/feeds answer: the settings and the cached
// headlines of the feeds set now, in their order.
type Status struct {
	Enabled bool     `json:"enabled"`
	Feeds   []string `json:"feeds"`
	Items   []Item   `json:"items"`
	// why the last read of a feed failed, by feed; absent for a feed read fine or not read yet
	Errors map[string]string `json:"errors,omitempty"`
}

type cached struct {
	items []Item
	at    time.Time // the last try, good or not: a feed that fails waits refreshEvery too
	err   string    // why the last try failed, "" when it did not
}

// Hub is the feeds of one box.
type Hub struct {
	Root   string
	Now    func() time.Time
	Client *http.Client

	mu         sync.Mutex
	cache      map[string]cached
	refreshing bool
	refreshed  sync.WaitGroup // the refresh in flight, which tests wait for
}

// Default is the box's own.
var Default = New("/")

// New keeps feeds.json under root and fetches through the guarded client.
func New(root string) *Hub {
	return &Hub{Root: root, Now: time.Now, Client: guardedClient(), cache: map[string]cached{}}
}

func (h *Hub) path() string { return filepath.Join(h.Root, ConfigFile) }

// load reads feeds.json. No file is on with DefaultFeed; a file that cannot be read
// or parsed is off with no feed, until the next PUT rewrites it.
func (h *Hub) load() Config {
	c := Config{Feeds: []string{}}
	data, err := os.ReadFile(h.path())
	if errors.Is(err, os.ErrNotExist) {
		return Config{Enabled: true, Feeds: []string{DefaultFeed}}
	}
	if err != nil || json.Unmarshal(data, &c) != nil {
		return Config{Feeds: []string{}}
	}
	if c.Feeds == nil {
		c.Feeds = []string{}
	}
	return c
}

// Status answers the settings and the cached headlines, and starts a refresh when
// they are due. Off, or with no feed, it starts nothing.
func (h *Hub) Status() Status {
	c := h.load()
	s := Status{Enabled: c.Enabled, Feeds: c.Feeds, Items: []Item{}}

	h.mu.Lock()
	defer h.mu.Unlock()
	due := false
	for _, feed := range c.Feeds {
		entry, ok := h.cache[feed]
		if !ok || h.Now().Sub(entry.at) >= refreshEvery {
			due = true
		}
		s.Items = append(s.Items, entry.items...)
		if entry.err != "" {
			if s.Errors == nil {
				s.Errors = map[string]string{}
			}
			s.Errors[feed] = entry.err
		}
	}
	if c.Enabled && due && !h.refreshing {
		h.refreshing = true
		h.refreshed.Add(1)
		go h.refresh(c.Feeds)
	}
	return s
}

// Update replaces the settings. Feeds are trimmed, and a feed given twice is kept
// once. More than maxFeeds, or a feed that is not an http(s) URL, is ErrInvalid.
func (h *Hub) Update(c Config) (Status, error) {
	feeds := []string{}
	seen := map[string]bool{}
	for _, raw := range c.Feeds {
		feed := strings.TrimSpace(raw)
		if err := validURL(feed); err != nil {
			return Status{}, fmt.Errorf("%w: %s", ErrInvalid, err)
		}
		if !seen[feed] {
			seen[feed] = true
			feeds = append(feeds, feed)
		}
	}
	if len(feeds) > maxFeeds {
		return Status{}, fmt.Errorf("%w: at most %d feeds", ErrInvalid, maxFeeds)
	}

	data, err := json.Marshal(Config{Enabled: c.Enabled, Feeds: feeds})
	if err != nil {
		return Status{}, err
	}
	if err := file.WriteFileAtomic(h.path(), data); err != nil {
		return Status{}, err
	}

	h.mu.Lock()
	for feed := range h.cache {
		if !seen[feed] {
			delete(h.cache, feed)
		}
	}
	h.mu.Unlock()
	return h.Status(), nil
}

func validURL(raw string) error {
	if raw == "" || len(raw) > maxURLLength {
		return errors.New("a feed is empty or too long")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return fmt.Errorf("%q is not an http(s) address", raw)
	}
	return nil
}

// refresh fetches every feed at once. A feed that fails keeps its last headlines.
func (h *Hub) refresh(feeds []string) {
	defer h.refreshed.Done()
	var wg sync.WaitGroup
	results := make([][]Item, len(feeds))
	errs := make([]error, len(feeds))
	for i, feed := range feeds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = fetch(h.Client, feed)
		}()
	}
	wg.Wait()

	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.Now()
	for i, feed := range feeds {
		entry := h.cache[feed]
		entry.at = now
		entry.err = ""
		if errs[i] == nil {
			entry.items = results[i]
		} else {
			entry.err = reason(errs[i])
		}
		h.cache[feed] = entry
	}
	h.refreshing = false
}

// reason is what the settings show of a failed read: the box's network refused, or
// the error with the feed's address left out (the owner sees which feed it is).
func reason(err error) string {
	if errors.Is(err, errRefused) {
		return "refused: the box reads feeds from the Internet only, not from itself or its own network"
	}
	var failed *url.Error
	if errors.As(err, &failed) {
		err = failed.Err
	}
	text := err.Error()
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}
