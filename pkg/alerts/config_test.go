package alerts

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func writeConfigFile(t *testing.T, h *Hub, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(h.path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.path(), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTheConfigurationFile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string // "" for no file
		want    Config
	}{
		{"no file: no channel, every category on, 90 %", "", defaults()},
		{"malformed: the defaults, with no channel", `{"channels":[{"id":"a1","name":"Phone","url":"` + phone + `"}],`, defaults()},
		{"a threshold that is not one: the defaults", `{"channels":[{"id":"a1","name":"Phone","url":"` + phone + `"}],"disk_threshold":100}`, defaults()},
		{
			"a category left out is on, one unknown is dropped",
			`{"channels":[{"id":"a1","name":"Phone","url":"` + phone + `"}],"categories":{"apps":false,"weather":true},"disk_threshold":80}`,
			Config{
				Channels:      []Channel{{ID: "a1", Name: "Phone", URL: phone}},
				Categories:    map[string]bool{Backups: true, Disks: true, Updates: true, Apps: false},
				DiskThreshold: 80,
			},
		},
		{"no categories at all: every one on", `{"channels":null,"categories":null,"disk_threshold":95}`, Config{Channels: []Channel{}, Categories: defaults().Categories, DiskThreshold: 95}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newHub(t)
			if err := os.Remove(h.path()); err != nil {
				t.Fatal(err)
			}
			if tc.content != "" {
				writeConfigFile(t, h, tc.content)
			}
			if got := h.load(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("load() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestAPutRewritesAMalformedFile0600(t *testing.T) {
	h, _, _ := newHub(t)
	writeConfigFile(t, h, `{"channels":`)

	if _, err := h.Update(Change{DiskThreshold: new(85)}); err != nil {
		t.Fatal(err)
	}

	if c := h.load(); c.DiskThreshold != 85 || len(c.Channels) != 0 {
		t.Fatalf("alerts.json = %+v, want 85 and no channel", c)
	}
	info, err := os.Stat(h.path())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("alerts.json is %v, want 0600", info.Mode().Perm())
	}
}

// No answer carries a channel's URL: its scheme, and its host when that is a
// server's address. Some services keep their credential there instead.
func TestTheStatusMasksTheURLs(t *testing.T) {
	h, _, _ := newHub(t)
	c := h.load()
	c.Channels = append(c.Channels,
		Channel{ID: "c3", Name: "Telegram", URL: "telegram://123456:ABC-DEF@telegram?chats=@home"},
		Channel{ID: "d4", Name: "Pushbullet", URL: "pushbullet://o.pushbullettoken0123456789"},
		Channel{ID: "e5", Name: "IFTTT", URL: "ifttt://iftttwebhookkey123/?events=box"},
		Channel{ID: "f6", Name: "Notifiarr", URL: "notifiarr://notifiarrapikey789"},
		Channel{ID: "g7", Name: "Pushover", URL: "pushover://shoutrrr:pushoverapitoken@pushoveruserkey/"},
		Channel{ID: "h8", Name: "Slack", URL: "slack://box@slacktokena/slacktokenb/slacktokenc"},
	)
	saveConfig(t, h, c)

	status := h.Status()

	want := []ChannelStatus{
		{ID: "a1", Name: "Phone", Service: "ntfy", Host: "ntfy.sh"},
		{ID: "b2", Name: "Mail", Service: "smtp", Host: "mail.example.com"},
		{ID: "c3", Name: "Telegram", Service: "telegram", Host: "telegram"},
		{ID: "d4", Name: "Pushbullet", Service: "pushbullet"},
		{ID: "e5", Name: "IFTTT", Service: "ifttt"},
		{ID: "f6", Name: "Notifiarr", Service: "notifiarr"},
		{ID: "g7", Name: "Pushover", Service: "pushover"},
		{ID: "h8", Name: "Slack", Service: "slack"},
	}
	if !reflect.DeepEqual(status.Channels, want) {
		t.Fatalf("channels = %+v, want %+v", status.Channels, want)
	}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"box-topic", "user", "pass", "ABC-DEF", "123456", "pushbullettoken", "iftttwebhookkey", "notifiarrapikey", "pushoveruserkey", "slacktoken", `"url"`} {
		if strings.Contains(string(data), secret) {
			t.Errorf("the status holds %q: %s", secret, data)
		}
	}
}

func TestAPutKeepsAStoredURL(t *testing.T) {
	h, _, _ := newHub(t)

	status, err := h.Update(Change{Channels: []Channel{
		{ID: "b2", Name: "My mail"}, // renamed, its URL kept
		{Name: "Gotify", URL: "gotify://gotify.example.com/AbCdEfGhIjKlMnO"}, // new
	}})
	if err != nil {
		t.Fatal(err)
	}

	c := h.load()
	if len(c.Channels) != 2 || c.Channels[0] != (Channel{ID: "b2", Name: "My mail", URL: mail}) {
		t.Fatalf("channels = %+v, want the mail box renamed, its URL kept, the phone removed", c.Channels)
	}
	added := c.Channels[1]
	if added.ID == "" || added.ID == "a1" || added.ID == "b2" || added.URL != "gotify://gotify.example.com/AbCdEfGhIjKlMnO" {
		t.Fatalf("new channel = %+v, want a new id and its URL", added)
	}
	if len(status.Channels) != 2 || status.Channels[1] != (ChannelStatus{ID: added.ID, Name: "Gotify", Service: "gotify", Host: "gotify.example.com"}) {
		t.Fatalf("PUT answered %+v", status.Channels)
	}
}

func TestAPutReplacesAStoredURL(t *testing.T) {
	h, _, _ := newHub(t)

	if _, err := h.Update(Change{Channels: []Channel{{ID: "a1", Name: "Phone", URL: "ntfy://ntfy.example.com/other"}}}); err != nil {
		t.Fatal(err)
	}

	if c := h.load(); !reflect.DeepEqual(c.Channels, []Channel{{ID: "a1", Name: "Phone", URL: "ntfy://ntfy.example.com/other"}}) {
		t.Fatalf("channels = %+v", c.Channels)
	}
}

func TestAPutChangesCategoriesAndThreshold(t *testing.T) {
	h, _, _ := newHub(t)

	status, err := h.Update(Change{Categories: map[string]bool{Disks: false, "weather": true}, DiskThreshold: new(75)})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{Backups: true, Disks: false, Updates: true, Apps: true}
	if !reflect.DeepEqual(status.Categories, want) || status.DiskThreshold != 75 || len(status.Channels) != 2 {
		t.Fatalf("PUT answered %+v, want disks off, 75 %%, the channels untouched", status)
	}
}

func TestAPutThatIsNotOneWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change Change
	}{
		{"a URL Shoutrrr cannot parse", Change{Channels: []Channel{{Name: "Web", URL: "https://example.com/hook"}}}},
		{"an unknown service", Change{Channels: []Channel{{Name: "Pager", URL: "pager://secret-token@example.com"}}}},
		{"a new channel without a URL", Change{Channels: []Channel{{Name: "Phone"}}}},
		{"an unknown id without a URL", Change{Channels: []Channel{{ID: "zz", Name: "Phone"}}}},
		{"a threshold under 50", Change{DiskThreshold: new(49)}},
		{"a threshold over 99", Change{DiskThreshold: new(100)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newHub(t)
			before, err := os.ReadFile(h.path())
			if err != nil {
				t.Fatal(err)
			}

			_, err = h.Update(tc.change)

			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Update() = %v, want ErrInvalid", err)
			}
			if strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("the error holds the URL: %v", err)
			}
			if after, _ := os.ReadFile(h.path()); string(after) != string(before) {
				t.Fatalf("alerts.json was written: %s", after)
			}
		})
	}
}

func TestATestSendsToOneChannelOrAll(t *testing.T) {
	h, o, _ := newHub(t)
	o.fail[mail] = errors.New("535 authentication failed for user")

	results, err := h.Test("")
	if err != nil {
		t.Fatal(err)
	}
	want := []Result{{ID: "a1", OK: true}, {ID: "b2", Error: "535 authentication failed for user"}}
	if !reflect.DeepEqual(results, want) {
		t.Fatalf("Test(\"\") = %+v, want %+v", results, want)
	}
	if got := o.sent; len(got) != 1 || got[0].title != "ReCasaOS · box · test" || got[0].text != "This is a test: alerts from ReCasaOS reach this channel.\nhttp://192.168.1.20" {
		t.Fatalf("sent %+v", got)
	}
	if failure := h.Status().LastFailure; failure == nil || failure.Channel != "Mail" {
		t.Fatalf("last_failure = %+v, want the mail box", failure)
	}

	results, err = h.Test("a1")
	if err != nil || !reflect.DeepEqual(results, []Result{{ID: "a1", OK: true}}) {
		t.Fatalf("Test(a1) = %+v, %v", results, err)
	}
	if _, err := h.Test("zz"); !errors.Is(err, ErrNoChannel) {
		t.Fatalf("Test(zz) = %v, want ErrNoChannel", err)
	}
}
