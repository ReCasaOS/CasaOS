// Package alerts tells the owner, on their phone or by mail, that something
// needs them: a backup that failed, a disk failing or full, an app that
// crashed, an automatic update that failed or paused. Its channels are
// Shoutrrr service URLs (ntfy://, telegram://, smtp://, ...) kept in
// alerts.json, and every alert goes to every channel.
//
// The alerts come from AppManagement's events on the message bus, from an
// hourly look at LocalStorage's disks and storages, and from pkg/autoupdate,
// in-process. An alert already sent is not sent again for six hours, even
// when it cleared and came back meanwhile, and a condition that clears sends
// one "resolved" message, only if its alert was sent and once per alert sent.
// That memory is the core's: a restart forgets it, and at worst a reminder
// comes early.
package alerts

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/ReCasaOS/CasaOS/pkg/utils/version"
	"github.com/nicholas-fedor/shoutrrr/pkg/router"
	"github.com/nicholas-fedor/shoutrrr/pkg/types"
	"go.uber.org/zap"
)

const (
	// dedupeWindow is how long an alert already sent stays quiet.
	dedupeWindow = 6 * time.Hour
	// sendTimeout bounds each channel's send.
	sendTimeout = 15 * time.Second
)

// The categories of an alert, each on unless the owner turns it off.
const (
	Backups = "backups"
	Disks   = "disks"
	Updates = "updates"
	Apps    = "apps"
)

var categories = []string{Backups, Disks, Updates, Apps}

// alert is one condition: its key is what dedupe and resolution go by, its
// sentence says what and where, and never anything more sensitive than an
// app, destination or disk name.
type alert struct {
	key      string
	category string
	sentence string
}

// record is an alert that was sent: when, how often it came back since, and
// whether its resolution was sent. It outlives the resolution, so that a
// condition that flaps stays quiet for the six hours.
type record struct {
	at       time.Time
	repeats  int
	resolved bool
}

// Hub is the alerts of one box.
type Hub struct {
	// Root is where alerts.json is kept: "/" on a box, a temporary directory
	// in tests.
	Root string
	// RuntimePath is where the other services leave their addresses and this
	// boot's secret.
	RuntimePath func() string
	Now         func() time.Time
	Hostname    func() (string, error)
	// Address is the dashboard's address, or "" when the core does not know it.
	Address func() string
	// Send delivers one message to one channel URL.
	Send func(rawURL, title, message string) error
	// Latest is the newest release as the update button reads it, Current the
	// installed one, and AutoUpdates whether automatic updates are on: with
	// them off, a newer release is an alert of its own.
	Latest      func() string
	Current     func() string
	AutoUpdates func() bool

	modifying   sync.Mutex // serialises Update's read-modify-writes of alerts.json
	mu          sync.Mutex // guards sent and lastFailure
	sent        map[string]*record
	lastFailure *Failure

	// Read and written by Run's goroutine only.
	seen           []disk    // the disks of the previous poll, nil before the first
	releaseChecked time.Time // the last look for a newer release
	announced      string    // the release last announced

	sending sync.WaitGroup // the sends in flight, which tests wait for
}

// Default is the box's own: the API answers from it and main runs its sources.
var Default = New("/")

// New keeps alerts.json under root and sends through Shoutrrr. Address,
// Latest and AutoUpdates are left to the caller.
func New(root string) *Hub {
	return &Hub{
		Root:        root,
		RuntimePath: func() string { return config.CommonInfo.RuntimePath },
		Now:         time.Now,
		Hostname:    os.Hostname,
		Address:     func() string { return "" },
		Send:        send,
		Latest:      func() string { return "" },
		Current:     version.CurrentVersion,
		AutoUpdates: func() bool { return true },
		sent:        map[string]*record{},
	}
}

// Run follows the message bus until ctx is done, and looks at the disks and
// for a newer release five minutes after the start, then every hour.
func (h *Hub) Run(ctx context.Context) {
	go h.followBus(ctx)
	timer := time.NewTimer(5 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
		case <-ctx.Done():
			return
		}
		h.pollDisks(ctx)
		h.checkRelease()
		timer.Reset(time.Hour)
	}
}

// raise sends a, unless its category is off, there is no channel, or it was
// sent less than six hours ago, resolved since or not: then it is counted, and
// the next message says how often it came back. It reports whether a is now
// on record as sent.
func (h *Hub) raise(a alert) bool {
	c := h.load()
	if len(c.Channels) == 0 || !c.Categories[a.category] {
		return false
	}
	now := h.Now()
	h.mu.Lock()
	previous := h.sent[a.key]
	if previous != nil && now.Sub(previous.at) < dedupeWindow {
		previous.repeats++
		h.mu.Unlock()
		return true
	}
	h.sent[a.key] = &record{at: now}
	h.mu.Unlock()
	sentence := a.sentence
	if previous != nil && previous.repeats > 0 {
		sentence += fmt.Sprintf(" It happened %d times since %s.", previous.repeats+1, previous.at.Format("15:04"))
	}
	h.deliver(c.Channels, a.category, sentence)
	return true
}

// resolve sends one "resolved" message for key, only if its alert was sent,
// and once per alert sent.
func (h *Hub) resolve(a alert) {
	h.mu.Lock()
	r := h.sent[a.key]
	pending := r != nil && !r.resolved
	if pending {
		r.resolved = true
	}
	h.mu.Unlock()
	if !pending {
		return
	}
	if c := h.load(); len(c.Channels) > 0 && c.Categories[a.category] {
		h.deliver(c.Channels, a.category, a.sentence)
	}
}

// deliver sends a message to every channel, each on its own, and returns at
// once: a source never waits for a channel.
func (h *Hub) deliver(channels []Channel, category, sentence string) {
	h.sending.Add(1)
	go func() {
		defer h.sending.Done()
		title, message := h.compose(category, sentence)
		var each sync.WaitGroup
		for _, channel := range channels {
			each.Add(1)
			go func() {
				defer each.Done()
				_ = h.sendTo(channel, title, message)
			}()
		}
		each.Wait()
	}()
}

// compose is the title, "ReCasaOS · <hostname> · <category>", and the message:
// the sentence, then the dashboard's address when the core knows it.
func (h *Hub) compose(category, sentence string) (title, message string) {
	hostname, err := h.Hostname()
	if err != nil || hostname == "" {
		hostname = "unknown host"
	}
	message = sentence
	if address := h.Address(); address != "" {
		message += "\n" + address
	}
	return "ReCasaOS · " + hostname + " · " + category, message
}

// sendTo sends to one channel. A failure is logged and kept for the
// dashboard, its URL and password taken out.
func (h *Hub) sendTo(c Channel, title, message string) error {
	err := h.Send(c.URL, title, message)
	if err == nil {
		return nil
	}
	reason := redact(err, c.URL)
	logger.Info("alerts: cannot send", zap.String("channel", c.Name), zap.String("error", reason))
	h.mu.Lock()
	h.lastFailure = &Failure{At: h.Now().UTC().Truncate(time.Second), Channel: c.Name, Error: reason}
	h.mu.Unlock()
	return errors.New(reason)
}

// send is the Shoutrrr send of one message to one channel, 15 seconds at most.
func send(rawURL, title, message string) error {
	sender, err := router.NewWithOptions(nil, types.SenderOptions{Timeout: sendTimeout}, rawURL)
	if err != nil {
		return err
	}
	params := types.Params{}
	params.SetTitle(title)
	return sender.Send(message, &params)[0]
}

// validate reports whether Shoutrrr can make a sender of rawURL.
func validate(rawURL string) error {
	_, err := router.NewWithOptions(nil, types.SenderOptions{}, rawURL)
	return err
}

// redact is err's text without the channel's URL or password, and with the
// address of a request that failed cut to its host: some services carry their
// token in it, as Telegram does in its path. At most 300 characters.
func redact(err error, rawURL string) string {
	text := err.Error()
	var failed *url.Error
	if errors.As(err, &failed) {
		if u, parseErr := url.Parse(failed.URL); parseErr == nil {
			text = strings.ReplaceAll(text, failed.URL, u.Scheme+"://"+u.Host)
		}
	}
	if rawURL != "" {
		text = strings.ReplaceAll(text, rawURL, "<url>")
	}
	for _, part := range secretParts(rawURL) {
		text = strings.ReplaceAll(text, part, "<secret>")
	}
	return truncate(text, 300)
}

// secretParts is every piece of a channel URL that may be a token: the user
// and password, each path segment and each query value, decoded and as
// written, and the host of a service that keeps a credential there (not
// serverHosts), when at least 6 characters long. Shoutrrr repeats some of them
// in its own errors (a bot token in a path, an API key as the host).
func secretParts(rawURL string) []string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	parts := []string{}
	if !serverHosts[u.Scheme] {
		parts = append(parts, u.Host, u.Hostname())
	}
	if u.User != nil {
		parts = append(parts, u.User.Username())
		if password, ok := u.User.Password(); ok {
			parts = append(parts, password)
		}
	}
	for _, segment := range strings.Split(u.EscapedPath(), "/") {
		if unescaped, err := url.PathUnescape(segment); err == nil {
			parts = append(parts, segment, unescaped)
		}
	}
	for _, values := range u.Query() {
		parts = append(parts, values...)
	}
	secrets := []string{}
	for _, part := range parts {
		if len(part) >= 6 && !slices.Contains(secrets, part) {
			secrets = append(secrets, part)
		}
	}
	// longest first, so a token is not half-replaced by a shorter piece of it
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	return secrets
}

// truncate is the first n characters of text, "…" ending a cut.
func truncate(text string, n int) string {
	if runes := []rune(text); len(runes) > n {
		return string(runes[:n]) + "…"
	}
	return text
}
