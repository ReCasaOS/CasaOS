package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/common"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/patrickmn/go-cache"
)

// a box's channel file under a temporary folder, a fresh cache, the distribution's own feed
func channelBox(t *testing.T) {
	t.Helper()
	path, cachePrevious, urlPrevious := UpdateChannelPath, Cache, config.ServerInfo.UpdateVersionUrl
	UpdateChannelPath = filepath.Join(t.TempDir(), "update-channel")
	Cache = cache.New(time.Minute, time.Minute)
	config.ServerInfo.UpdateVersionUrl = ""
	t.Setenv(versionURLEnv, "")
	t.Cleanup(func() {
		UpdateChannelPath, Cache, config.ServerInfo.UpdateVersionUrl = path, cachePrevious, urlPrevious
	})
}

type fixedCasa struct{ release model.Version }

func (f fixedCasa) GetCasaosVersion() model.Version   { return f.release }
func (f fixedCasa) FetchCasaosVersion() model.Version { return f.release }

const releaseList = `[
	{"tag_name":"v0.5.190","prerelease":true,"draft":true,"body":"draft","published_at":"2026-11-24T20:00:00Z"},
	{"tag_name":"v0.5.189","prerelease":true,"draft":false,"body":"next","published_at":"2026-11-24T19:00:00Z"},
	{"tag_name":"v0.5.188","prerelease":false,"draft":false,"body":"last","published_at":"2026-11-24T18:00:00Z"},
	{"tag_name":"v0.5.180","prerelease":true,"draft":false,"body":"old","published_at":"2026-11-20T18:00:00Z"}
]`

func TestTheChannelIsReleaseUntilPrereleaseIsChosen(t *testing.T) {
	channelBox(t)
	if got := UpdateChannel(); got != ChannelRelease {
		t.Fatalf("no file: %q", got)
	}
	if err := SetUpdateChannel(ChannelPrerelease); err != nil || UpdateChannel() != ChannelPrerelease {
		t.Fatalf("prerelease: %v %q", err, UpdateChannel())
	}
	if err := SetUpdateChannel(ChannelRelease); err != nil || UpdateChannel() != ChannelRelease {
		t.Fatalf("back to release: %v %q", err, UpdateChannel())
	}
	if _, err := os.Stat(UpdateChannelPath); !os.IsNotExist(err) {
		t.Fatalf("the release channel left a file: %v", err)
	}
	if err := SetUpdateChannel("beta"); err != ErrUnknownChannel {
		t.Fatalf("beta: %v", err)
	}
	if err := os.WriteFile(UpdateChannelPath, []byte("garbage"), 0o600); err != nil || UpdateChannel() != ChannelRelease {
		t.Fatalf("an unreadable channel is release: %v %q", err, UpdateChannel())
	}
}

func TestThePrereleaseChannelOffersANewerPrereleaseByItsTag(t *testing.T) {
	channelBox(t)
	Cache.Set("casa_releases:"+common.FORK_RELEASES_API, releaseList, time.Minute)
	casa := fixedCasa{release: model.Version{Version: "v0.5.188", ChangeLog: "last"}}

	if got := OfferedCasaosVersion(casa, false); got.Version != "v0.5.188" || got.Prerelease {
		t.Fatalf("release channel: %+v", got)
	}

	if err := SetUpdateChannel(ChannelPrerelease); err != nil {
		t.Fatal(err)
	}
	got := OfferedCasaosVersion(casa, false)
	if got.Version != "v0.5.189" || !got.Prerelease || got.ChangeLog != "next" {
		t.Fatalf("prerelease channel: %+v, want v0.5.189 (the draft left out)", got)
	}

	// once a release is newer than every pre-release, the release is offered
	casa.release = model.Version{Version: "v0.5.191"}
	if got := OfferedCasaosVersion(casa, false); got.Version != "v0.5.191" || got.Prerelease {
		t.Fatalf("release newer than the pre-releases: %+v", got)
	}

	// a feed set in config has no pre-releases to read
	casa.release = model.Version{Version: "v0.5.188"}
	config.ServerInfo.UpdateVersionUrl = "https://example.com/version.json"
	if got := OfferedCasaosVersion(casa, false); got.Version != "v0.5.188" || got.Prerelease {
		t.Fatalf("own feed: %+v", got)
	}
}

func TestNoPrereleaseInAListIsNothing(t *testing.T) {
	if got := newestPrerelease(`[{"tag_name":"v1.0.0","prerelease":false}]`); got.Version != "" {
		t.Fatalf("%+v", got)
	}
	if got := newestPrerelease(""); got.Version != "" {
		t.Fatalf("%+v", got)
	}
}
