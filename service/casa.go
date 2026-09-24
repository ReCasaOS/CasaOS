package service

import (
	json2 "encoding/json"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/common"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/ReCasaOS/CasaOS/pkg/utils/httper"
	"github.com/tidwall/gjson"
)

type CasaService interface {
	GetCasaosVersion() model.Version
	FetchCasaosVersion() model.Version
}

type casaService struct{}

// The install check serves a version.json and an installer of its own and
// points the updates at them through casaos.service's environment. Never set
// on a box.
const (
	versionURLEnv   = "CASAOS_AUTOUPDATE_VERSION_URL"
	installerURLEnv = "CASAOS_AUTOUPDATE_INSTALLER_URL"
)

func validHTTPSURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != ""
}

// installCheckURL is the URL the install check sets in env: HTTPS, or plain
// HTTP to a loopback address, where the check serves it; "" otherwise.
func installCheckURL(env string) string {
	value := strings.TrimSpace(os.Getenv(env))
	parsed, err := url.ParseRequestURI(value)
	if validHTTPSURL(value) || (err == nil && parsed.Scheme == "http" && net.ParseIP(parsed.Hostname()).IsLoopback()) {
		return value
	}
	return ""
}

func resolveUpdateVersionURL() string {
	if value := installCheckURL(versionURLEnv); value != "" {
		return value
	}
	value := strings.TrimSpace(config.ServerInfo.UpdateVersionUrl)
	if validHTTPSURL(value) {
		return value
	}
	return common.FORK_VERSION_URL
}

func parseReleaseVersion(payload string) model.Version {
	var release model.Version
	if err := json2.Unmarshal([]byte(payload), &release); err == nil && release.Version != "" {
		return release
	}

	data := gjson.Get(payload, "data")
	if data.Exists() {
		_ = json2.Unmarshal([]byte(data.String()), &release)
		if release.Version != "" {
			return release
		}
	}

	return model.Version{
		Version:   gjson.Get(payload, "tag_name").String(),
		ChangeLog: gjson.Get(payload, "body").String(),
	}
}

/**
 * @description: get remote version
 * @return {model.Version}
 */
func (o *casaService) GetCasaosVersion() model.Version {
	if result, ok := Cache.Get("casa_version:" + resolveUpdateVersionURL()); ok {
		if dataStr, ok := result.(string); ok {
			return parseReleaseVersion(dataStr)
		}
	}
	return o.FetchCasaosVersion()
}

// FetchCasaosVersion reads version.json now, past the 20-minute cache, and
// caches what it read: the automatic update's check must see a release
// published since the dashboard last looked.
func (o *casaService) FetchCasaosVersion() model.Version {
	versionURL := resolveUpdateVersionURL()
	v := httper.Get(versionURL, map[string]string{
		"Accept":     "application/json",
		"User-Agent": "CasaOS-Fork-Updater",
	})
	version := parseReleaseVersion(v)

	if len(version.Version) > 0 {
		Cache.Set("casa_version:"+versionURL, v, time.Minute*20)
	}

	return version
}

func NewCasaService() CasaService {
	return &casaService{}
}
