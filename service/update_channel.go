package service

import (
	"errors"
	"os"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/common"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/utils/file"
	"github.com/ReCasaOS/CasaOS/pkg/utils/httper"
	"github.com/ReCasaOS/CasaOS/pkg/utils/version"
	"github.com/tidwall/gjson"
)

// The update channel of a box: "release", the default, offers the releases only; "prerelease"
// also offers a pre-release newer than the last release, to whoever presses Update. The night's
// automatic update never reads it: it installs releases only (GetCasaosVersion, autoupdate).
const (
	ChannelRelease    = "release"
	ChannelPrerelease = "prerelease"
)

// UpdateChannelPath holds the channel chosen; no file is the release channel.
var UpdateChannelPath = "/var/lib/casaos/update-channel"

// ErrUnknownChannel is a channel that is neither of the two.
var ErrUnknownChannel = errors.New("the update channel is release or prerelease")

// UpdateChannel is the channel the box follows; anything unreadable is the release channel.
func UpdateChannel() string {
	data, err := os.ReadFile(UpdateChannelPath)
	if err == nil && strings.TrimSpace(string(data)) == ChannelPrerelease {
		return ChannelPrerelease
	}
	return ChannelRelease
}

// SetUpdateChannel keeps the channel chosen.
func SetUpdateChannel(channel string) error {
	switch channel {
	case ChannelRelease:
		if err := os.Remove(UpdateChannelPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	case ChannelPrerelease:
		return file.WriteFileAtomic(UpdateChannelPath, []byte(ChannelPrerelease+"\n"))
	}
	return ErrUnknownChannel
}

// OfferedCasaosVersion is the version the dashboard offers: the last release, or on the
// pre-release channel a pre-release newer than it (Prerelease set). fresh reads past the
// 20-minute caches, for "check for updates". Pre-releases are read from the distribution's
// own release list only: a feed set in config or by the install check has none.
func OfferedCasaosVersion(casa CasaService, fresh bool) model.Version {
	release := casa.GetCasaosVersion()
	if fresh {
		release = casa.FetchCasaosVersion()
	}
	if UpdateChannel() != ChannelPrerelease || resolveUpdateVersionURL() != common.FORK_VERSION_URL {
		return release
	}
	pre := newestPrerelease(releasesList(fresh))
	if pre.Version != "" && (release.Version == "" || version.IsVersionNewer(pre.Version, release.Version)) {
		return pre
	}
	return release
}

// releasesList is the distribution's GitHub release list (30 newest), cached 20 minutes
// like version.json; "" when it cannot be read.
func releasesList(fresh bool) string {
	key := "casa_releases:" + common.FORK_RELEASES_API
	if !fresh {
		if list, ok := Cache.Get(key); ok {
			if payload, ok := list.(string); ok {
				return payload
			}
		}
	}
	payload := httper.Get(common.FORK_RELEASES_API, map[string]string{
		"Accept":     "application/vnd.github+json",
		"User-Agent": "CasaOS-Fork-Updater",
	})
	if !gjson.Valid(payload) || !gjson.Parse(payload).IsArray() {
		return ""
	}
	Cache.Set(key, payload, time.Minute*20)
	return payload
}

// newestPrerelease is the newest pre-release of a GitHub release list, drafts left out.
func newestPrerelease(payload string) model.Version {
	var newest model.Version
	if payload == "" {
		return newest
	}
	gjson.Parse(payload).ForEach(func(_, release gjson.Result) bool {
		tag := release.Get("tag_name").String()
		if release.Get("draft").Bool() || !release.Get("prerelease").Bool() || tag == "" {
			return true
		}
		if newest.Version == "" || version.IsVersionNewer(tag, newest.Version) {
			newest = model.Version{
				Version:     tag,
				ChangeLog:   release.Get("body").String(),
				PublishedAt: release.Get("published_at").String(),
				Prerelease:  true,
			}
		}
		return true
	})
	return newest
}
