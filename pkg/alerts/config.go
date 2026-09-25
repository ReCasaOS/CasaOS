package alerts

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/utils/file"
)

// ConfigFile holds the channels, the categories and the disk threshold (root,
// 0600: the URLs carry tokens).
const ConfigFile = "/var/lib/casaos/alerts.json"

var (
	// ErrInvalid is a PUT body that is not one: a URL Shoutrrr cannot parse, a
	// new channel without a URL, an unknown channel id, a threshold outside 50
	// to 99. Nothing is written.
	ErrInvalid = errors.New("invalid alerts settings")
	// ErrNoChannel is a test of a channel id that is not one.
	ErrNoChannel = errors.New("no such channel")
)

// Config is alerts.json.
type Config struct {
	Channels      []Channel       `json:"channels"`
	Categories    map[string]bool `json:"categories"`
	DiskThreshold int             `json:"disk_threshold"` // a percentage, 50 to 99
}

// Channel is where alerts go: a Shoutrrr URL, never answered back.
type Channel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// defaults is a box that never chose: no channel, so nothing is sent, every
// category on, a storage full above 90 %.
func defaults() Config {
	c := Config{Channels: []Channel{}, Categories: map[string]bool{}, DiskThreshold: 90}
	for _, name := range categories {
		c.Categories[name] = true
	}
	return c
}

func (h *Hub) path() string {
	return filepath.Join(h.Root, ConfigFile)
}

// load reads alerts.json. No file is the defaults; a file that cannot be read
// or parsed, or whose threshold is not one, is the defaults too, with no
// channel: nothing is sent. The next PUT rewrites it. A category the file
// leaves out is on.
func (h *Hub) load() Config {
	c := defaults()
	data, err := os.ReadFile(h.path())
	if err != nil || json.Unmarshal(data, &c) != nil || c.DiskThreshold < 50 || c.DiskThreshold > 99 {
		return defaults()
	}
	on := make(map[string]bool, len(categories))
	for _, name := range categories {
		value, set := c.Categories[name]
		on[name] = value || !set
	}
	c.Categories = on
	if c.Channels == nil {
		c.Channels = []Channel{}
	}
	return c
}

// save writes alerts.json atomically and 0600, like autoupdate.json.
func (h *Hub) save(c Config) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return file.WriteFileAtomic(h.path(), data)
}

// Status is what GET and PUT /v1/sys/alerts answer in data: the channels
// without their URLs, which never leave the box's disk.
type Status struct {
	Channels      []ChannelStatus `json:"channels"`
	Categories    map[string]bool `json:"categories"`
	DiskThreshold int             `json:"disk_threshold"`
	LastFailure   *Failure        `json:"last_failure"`
}

// ChannelStatus is a channel as the dashboard sees it: service is the URL's
// scheme, host its host part (the ntfy server, the SMTP host), enough to
// recognise it.
type ChannelStatus struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Service string `json:"service"`
	Host    string `json:"host"`
}

// Failure is the last send that failed, since the core started.
type Failure struct {
	At      time.Time `json:"at"`
	Channel string    `json:"channel"` // its name
	Error   string    `json:"error"`
}

// Status reads alerts.json.
func (h *Hub) Status() Status {
	c := h.load()
	s := Status{Channels: make([]ChannelStatus, 0, len(c.Channels)), Categories: c.Categories, DiskThreshold: c.DiskThreshold}
	for _, channel := range c.Channels {
		status := ChannelStatus{ID: channel.ID, Name: channel.Name}
		if u, err := url.Parse(channel.URL); err == nil {
			status.Service, status.Host = u.Scheme, u.Hostname()
		}
		s.Channels = append(s.Channels, status)
	}
	h.mu.Lock()
	s.LastFailure = h.lastFailure
	h.mu.Unlock()
	return s
}

// Change is PUT /v1/sys/alerts's body, each field optional, others ignored.
// Channels replaces the list: an entry with an id and no url keeps its stored
// URL, a new entry needs a url.
type Change struct {
	Channels      []Channel       `json:"channels"`
	Categories    map[string]bool `json:"categories"`
	DiskThreshold *int            `json:"disk_threshold"`
}

// Update applies c, rewriting a malformed alerts.json, and answers the new
// status. A body that is not one is ErrInvalid, and nothing is written.
func (h *Hub) Update(change Change) (Status, error) {
	h.modifying.Lock()
	defer h.modifying.Unlock()
	c := h.load()
	if change.Channels != nil {
		channels, err := merge(c.Channels, change.Channels)
		if err != nil {
			return Status{}, err
		}
		c.Channels = channels
	}
	for name, on := range change.Categories {
		if _, known := c.Categories[name]; known {
			c.Categories[name] = on
		}
	}
	if t := change.DiskThreshold; t != nil {
		if *t < 50 || *t > 99 {
			return Status{}, fmt.Errorf("%w: disk_threshold is a percentage, 50 to 99", ErrInvalid)
		}
		c.DiskThreshold = *t
	}
	if err := h.save(c); err != nil {
		return Status{}, err
	}
	return h.Status(), nil
}

// merge is the channels given, each with its URL: the one given, checked by
// Shoutrrr, or the one stored under its id.
func merge(stored, given []Channel) ([]Channel, error) {
	urls := make(map[string]string, len(stored))
	for _, c := range stored {
		urls[c.ID] = c.URL
	}
	merged := make([]Channel, 0, len(given))
	for _, c := range given {
		storedURL, known := urls[c.ID]
		switch {
		case c.URL != "":
			if err := validate(c.URL); err != nil {
				return nil, fmt.Errorf("%w: channel %q: %s", ErrInvalid, c.Name, redact(err, c.URL))
			}
			if !known {
				c.ID = newID()
			}
		case c.ID == "":
			return nil, fmt.Errorf("%w: channel %q: a new channel needs a url", ErrInvalid, c.Name)
		case !known:
			return nil, fmt.Errorf("%w: no channel %q", ErrInvalid, c.ID)
		default:
			c.URL = storedURL
		}
		merged = append(merged, c)
	}
	return merged, nil
}

func newID() string {
	raw := make([]byte, 8)
	_, _ = rand.Read(raw) // never fails
	return hex.EncodeToString(raw)
}

// Result is one channel's answer to a test.
type Result struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// Test sends a test message to the channel id, or to every channel when id is
// "", and answers per channel once each has answered, in 15 seconds at most.
func (h *Hub) Test(id string) ([]Result, error) {
	var chosen []Channel
	for _, c := range h.load().Channels {
		if id == "" || c.ID == id {
			chosen = append(chosen, c)
		}
	}
	if id != "" && len(chosen) == 0 {
		return nil, ErrNoChannel
	}
	title, message := h.compose("test", "This is a test: alerts from ReCasaOS reach this channel.")
	results := make([]Result, len(chosen))
	var each sync.WaitGroup
	for i, c := range chosen {
		each.Add(1)
		go func() {
			defer each.Done()
			results[i] = Result{ID: c.ID, OK: true}
			if err := h.sendTo(c, title, message); err != nil {
				results[i] = Result{ID: c.ID, Error: err.Error()}
			}
		}()
	}
	each.Wait()
	return results, nil
}
