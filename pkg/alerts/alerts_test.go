package alerts

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
)

func TestMain(m *testing.M) {
	// The hub logs through CasaOS-Common's logger, nil until initialised.
	logger.LogInitConsoleOnly()
	os.Exit(m.Run())
}

// message is one send to one channel.
type message struct {
	url, title, text string
}

// outbox records what a hub sends, and fails the sends to the URLs in fail.
type outbox struct {
	mu   sync.Mutex
	sent []message
	fail map[string]error
}

func (o *outbox) send(rawURL, title, text string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.fail[rawURL]; err != nil {
		return err
	}
	o.sent = append(o.sent, message{rawURL, title, text})
	return nil
}

// texts is what reached rawURL, in order.
func (o *outbox) texts(rawURL string) []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var texts []string
	for _, m := range o.sent {
		if m.url == rawURL {
			texts = append(texts, m.text)
		}
	}
	return texts
}

const (
	phone = "ntfy://ntfy.sh/box-topic"
	mail  = "smtp://user:pass@mail.example.com:587/?from=box@example.com&to=me@example.com"
)

// newHub is a box named "box" whose dashboard is at http://192.168.1.20, at
// 06:00 on 2026-09-25 by a clock the test moves, with two channels: a phone
// and a mail box.
func newHub(t *testing.T) (*Hub, *outbox, *time.Time) {
	t.Helper()
	o := &outbox{fail: map[string]error{}}
	now := time.Date(2026, 9, 25, 6, 0, 0, 0, time.UTC)
	h := New(t.TempDir())
	h.Now = func() time.Time { return now }
	h.Hostname = func() (string, error) { return "box", nil }
	h.Address = func() string { return "http://192.168.1.20" }
	h.Send = o.send
	h.RuntimePath = func() string { return t.TempDir() }
	saveConfig(t, h, Config{
		Channels:      []Channel{{ID: "a1", Name: "Phone", URL: phone}, {ID: "b2", Name: "Mail", URL: mail}},
		Categories:    map[string]bool{Backups: true, Disks: true, Updates: true, Apps: true},
		DiskThreshold: 90,
	})
	return h, o, &now
}

func saveConfig(t *testing.T, h *Hub, c Config) {
	t.Helper()
	if err := h.save(c); err != nil {
		t.Fatal(err)
	}
}

// raise raises an alert and waits for its sends.
func raise(h *Hub, a alert) {
	h.raise(a)
	h.sending.Wait()
}

func resolve(h *Hub, a alert) {
	h.resolve(a)
	h.sending.Wait()
}

var crashed = alert{key: "app:immich:runtime", category: Apps, sentence: "immich stopped unexpectedly."}

func TestAnAlertGoesToEveryChannelWithItsTitleAndTheDashboardsAddress(t *testing.T) {
	h, o, _ := newHub(t)

	raise(h, crashed)

	want := message{phone, "ReCasaOS · box · apps", "immich stopped unexpectedly.\nhttp://192.168.1.20"}
	if len(o.sent) != 2 || (o.sent[0] != want && o.sent[1] != want) {
		t.Fatalf("sent %+v, want %+v and the same to the mail box", o.sent, want)
	}
	if got := o.texts(mail); len(got) != 1 || got[0] != want.text {
		t.Fatalf("the mail box got %q", got)
	}
}

func TestWithoutTheDashboardsAddressTheMessageIsTheSentence(t *testing.T) {
	h, o, _ := newHub(t)
	h.Address = func() string { return "" }

	raise(h, crashed)

	if got := o.texts(phone); !reflect.DeepEqual(got, []string{"immich stopped unexpectedly."}) {
		t.Fatalf("sent %q", got)
	}
}

// A key already sent is quiet for six hours; what came back meanwhile is
// counted in the next message.
func TestAKeyAlreadySentIsCountedForSixHours(t *testing.T) {
	h, o, now := newHub(t)
	at := func(clock string) {
		t.Helper()
		parsed, err := time.Parse("15:04", clock)
		if err != nil {
			t.Fatal(err)
		}
		*now = time.Date(2026, 9, 25, parsed.Hour(), parsed.Minute(), 0, 0, time.UTC)
		raise(h, crashed)
	}

	at("06:00")
	at("07:00")
	at("08:00")
	at("11:59")
	if got := o.texts(phone); len(got) != 1 {
		t.Fatalf("sent %d messages before six hours, want 1: %q", len(got), got)
	}

	at("12:00")
	want := "immich stopped unexpectedly. It happened 4 times since 06:00.\nhttp://192.168.1.20"
	if got := o.texts(phone); len(got) != 2 || got[1] != want {
		t.Fatalf("sent %q, want a second message %q", got, want)
	}

	at("13:00")
	at("18:00")
	want = "immich stopped unexpectedly. It happened 2 times since 12:00.\nhttp://192.168.1.20"
	if got := o.texts(phone); len(got) != 3 || got[2] != want {
		t.Fatalf("sent %q, want a third message %q", got, want)
	}

	at("23:59")
	if got := o.texts(phone); len(got) != 3 {
		t.Fatalf("sent %q, want nothing more before 00:00", got)
	}
}

func TestADifferentKeyIsNotHeldBack(t *testing.T) {
	h, o, _ := newHub(t)

	raise(h, crashed)
	raise(h, alert{key: "app:immich:install", category: Apps, sentence: "Installing immich failed."})

	if got := o.texts(phone); len(got) != 2 {
		t.Fatalf("sent %q, want both", got)
	}
}

func TestNothingIsSentWithoutAChannelOrWithItsCategoryOff(t *testing.T) {
	h, o, _ := newHub(t)
	c := h.load()
	c.Categories[Apps] = false
	saveConfig(t, h, c)

	raise(h, crashed)
	raise(h, alert{key: "disk:WD-1:smart", category: Disks, sentence: "Disk sdb fails its SMART check."})

	if got := o.texts(phone); !reflect.DeepEqual(got, []string{"Disk sdb fails its SMART check.\nhttp://192.168.1.20"}) {
		t.Fatalf("sent %q, want the disk alert only", got)
	}

	c.Channels = nil
	saveConfig(t, h, c)
	raise(h, alert{key: "disk:WD-2:smart", category: Disks, sentence: "Disk sdc fails its SMART check."})
	if len(o.sent) != 2 {
		t.Fatalf("sent %+v with no channel", o.sent)
	}
	// Not sent, so not on record: once there is a channel, it goes at once.
	saveConfig(t, h, defaultsWith(Channel{ID: "a1", Name: "Phone", URL: phone}))
	raise(h, alert{key: "disk:WD-2:smart", category: Disks, sentence: "Disk sdc fails its SMART check."})
	if got := o.texts(phone); len(got) != 2 {
		t.Fatalf("sent %q, want the alert once there is a channel", got)
	}
}

func defaultsWith(channels ...Channel) Config {
	c := defaults()
	c.Channels = channels
	return c
}

func TestAResolutionIsSentOnlyAfterItsAlert(t *testing.T) {
	h, o, now := newHub(t)
	healthy := alert{key: crashed.key, category: Apps, sentence: "Resolved: immich runs normally again."}

	resolve(h, healthy)
	if len(o.sent) != 0 {
		t.Fatalf("sent %+v for a condition never raised", o.sent)
	}

	raise(h, crashed)
	*now = now.Add(20 * time.Minute)
	resolve(h, healthy)
	resolve(h, healthy)
	want := []string{"immich stopped unexpectedly.\nhttp://192.168.1.20", "Resolved: immich runs normally again.\nhttp://192.168.1.20"}
	if got := o.texts(phone); !reflect.DeepEqual(got, want) {
		t.Fatalf("sent %q, want %q", got, want)
	}

	// Back within the six hours, it is counted and quiet, and so is its second
	// resolution: a condition that flaps sends an alert and a resolution every
	// six hours at most.
	*now = now.Add(20 * time.Minute)
	raise(h, crashed)
	resolve(h, healthy)
	raise(h, crashed)
	if got := o.texts(phone); !reflect.DeepEqual(got, want) {
		t.Fatalf("sent %q, want nothing more within the six hours", got)
	}

	// Past them, it is sent with its count, and its resolution goes however
	// late it comes.
	*now = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	raise(h, crashed)
	*now = time.Date(2026, 9, 25, 20, 0, 0, 0, time.UTC)
	resolve(h, healthy)
	resolve(h, healthy)
	want = append(want, "immich stopped unexpectedly. It happened 3 times since 06:00.\nhttp://192.168.1.20", "Resolved: immich runs normally again.\nhttp://192.168.1.20")
	if got := o.texts(phone); !reflect.DeepEqual(got, want) {
		t.Fatalf("sent %q, want %q", got, want)
	}
}

// A channel that fails is logged and kept for the dashboard, without its URL,
// and holds up neither the source nor the other channels.
func TestAFailingChannelIsKeptAndNeverBlocks(t *testing.T) {
	h, o, _ := newHub(t)
	release := make(chan struct{})
	h.Send = func(rawURL, title, text string) error {
		if rawURL == mail {
			<-release
			return &url.Error{Op: "Post", URL: "https://user:pass@mail.example.com/secret-path", Err: errors.New("connection refused")}
		}
		return o.send(rawURL, title, text)
	}

	returned := make(chan struct{})
	go func() { h.raise(crashed); close(returned) }()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("raise waited for a channel")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(o.texts(phone)) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(o.texts(phone)) != 1 {
		t.Fatal("the phone waited for the mail box")
	}
	if h.Status().LastFailure != nil {
		t.Fatal("a failure before any failed")
	}

	close(release)
	h.sending.Wait()
	got := h.Status().LastFailure
	want := &Failure{At: time.Date(2026, 9, 25, 6, 0, 0, 0, time.UTC), Channel: "Mail", Error: `Post "https://mail.example.com": connection refused`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("last_failure = %+v, want %+v", got, want)
	}
}

func TestRedactKeepsTheChannelsSecretsOut(t *testing.T) {
	telegram := "telegram://123456:ABCDEF-token@telegram?chats=@home"
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{
			"a request's address, Telegram's token in its path",
			&url.Error{Op: "Post", URL: "https://api.telegram.org/bot123456:ABCDEF-token/sendMessage", Err: errors.New("i/o timeout")},
			`Post "https://api.telegram.org": i/o timeout`,
		},
		{"the channel's URL", errors.New("creating sender for URLs [" + telegram + "]: bad chat"), "creating sender for URLs [<url>]: bad chat"},
		{"its password", errors.New("server said: ABCDEF-token is revoked"), "server said: <secret> is revoked"},
		{"a long answer", errors.New(strings.Repeat("x", 400)), strings.Repeat("x", 300) + "…"},
		{"its chat, a query value", errors.New("chat @home-family not found"), "chat @home-family not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := redact(tc.err, telegram); got != tc.want {
				t.Fatalf("redact() = %q, want %q", got, tc.want)
			}
		})
	}

	// Shoutrrr repeats a token wherever the URL kept it: a path, a query, or the
	// host of a service that keeps its key there; a server's address stays readable.
	for rawURL, tc := range map[string]struct{ err, want string }{
		"pushbullet://o.AbCdEf123456/device":              {"pushbullet: bad token o.AbCdEf123456", "pushbullet: bad token <secret>"},
		"ntfy://ntfy.example.com/my-secret-topic":         {"POST https://ntfy.example.com/my-secret-topic: 403", "POST https://ntfy.example.com/<secret>: 403"},
		"gotify://gotify.example.com/AppTokenXyz?title=x": {"gotify.example.com said AppTokenXyz is unknown", "gotify.example.com said <secret> is unknown"},
		"discord://WebhookToken123@123456789":             {"discord 401 for WebhookToken123 on 123456789", "discord 401 for <secret> on <secret>"},
	} {
		if got := redact(errors.New(tc.err), rawURL); got != tc.want {
			t.Errorf("redact(%q) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// send is Shoutrrr's: an ntfy URL with ?scheme=http reaches a local server,
// as in the install check, with the title and the message.
func TestShoutrrrSendsToNtfy(t *testing.T) {
	got := make(chan *http.Request, 1)
	bodies := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- r
		bodies <- string(body)
		_, _ = w.Write([]byte(`{"id":"x","time":1,"event":"message","topic":"box-topic"}`))
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")

	if err := send("ntfy://"+host+"/box-topic?scheme=http", "ReCasaOS · box · test", "It works."); err != nil {
		t.Fatal(err)
	}

	r := <-got
	body := <-bodies
	if r.URL.Path != "/box-topic" || !strings.Contains(body, "It works.") || !strings.Contains(body+r.Header.Get("Title")+r.Header.Get("X-Title"), "ReCasaOS · box · test") {
		t.Fatalf("ntfy got %s %s, title %q, body %q", r.Method, r.URL.Path, r.Header.Get("Title"), body)
	}
}

func TestShoutrrrSendFailsOnAServerDown(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	err := send("ntfy://"+strings.TrimPrefix(server.URL, "http://")+"/box-topic?scheme=http", "t", "m")
	if err == nil {
		t.Fatal("send() = nil with nobody listening")
	}
}

func TestShoutrrrValidatesURLs(t *testing.T) {
	for _, valid := range []string{phone, "ntfy://127.0.0.1:8080/topic?scheme=http", mail, "telegram://123456:ABC-DEF@telegram?chats=@home", "gotify://gotify.example.com/AbCdEfGhIjKlMnO"} {
		if err := validate(valid); err != nil {
			t.Errorf("validate(%q) = %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "not a url", "https://example.com", "nope://x", "telegram://telegram"} {
		if validate(invalid) == nil {
			t.Errorf("validate(%q) = nil", invalid)
		}
	}
}
