package service

import (
	"reflect"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/common"
	"github.com/ReCasaOS/CasaOS/pkg/config"
)

func TestResolveUpdateInstallerURL(t *testing.T) {
	original := config.ServerInfo.UpdateUrl
	t.Cleanup(func() { config.ServerInfo.UpdateUrl = original })

	config.ServerInfo.UpdateUrl = "https://example.com/install.sh"
	if got := resolveUpdateInstallerURL(); got != "https://example.com/install.sh" {
		t.Fatalf("resolveUpdateInstallerURL() = %q", got)
	}

	config.ServerInfo.UpdateUrl = "http://example.com/install.sh"
	if got := resolveUpdateInstallerURL(); got != common.FORK_UPDATE_URL {
		t.Fatalf("resolveUpdateInstallerURL() fallback = %q", got)
	}
}

// The install check serves its own installer over loopback; plain HTTP
// anywhere else is refused, and the release's installer answers.
func TestTheInstallCheckOverridesTheInstallerURL(t *testing.T) {
	original := config.ServerInfo.UpdateUrl
	t.Cleanup(func() { config.ServerInfo.UpdateUrl = original })
	config.ServerInfo.UpdateUrl = ""

	for _, tc := range []struct{ override, want string }{
		{"http://127.0.0.1:8123/install.sh", "http://127.0.0.1:8123/install.sh"},
		{"https://releases.example.org/install.sh", "https://releases.example.org/install.sh"},
		{"http://10.0.0.2:8123/install.sh", common.FORK_UPDATE_URL},
		{"", common.FORK_UPDATE_URL},
	} {
		t.Setenv("CASAOS_AUTOUPDATE_INSTALLER_URL", tc.override)
		if got := resolveUpdateInstallerURL(); got != tc.want {
			t.Errorf("with the override %q, resolveUpdateInstallerURL() = %q, want %q", tc.override, got, tc.want)
		}
	}
}

func TestAReleaseUpdateUsesItsOwnInstaller(t *testing.T) {
	original := config.ServerInfo.UpdateUrl
	t.Cleanup(func() { config.ServerInfo.UpdateUrl = original })
	config.ServerInfo.UpdateUrl = ""
	t.Setenv("CASAOS_AUTOUPDATE_INSTALLER_URL", "")

	if got, want := resolveReleaseInstallerURL("v0.5.70"), common.FORK_RELEASE_BASE+"v0.5.70/install.sh"; got != want {
		t.Errorf("resolveReleaseInstallerURL() = %q, want %q", got, want)
	}
	// the address the setup writes into casaos.conf is the default one, not an installer of its own
	config.ServerInfo.UpdateUrl = common.FORK_UPDATE_URL
	if got, want := resolveReleaseInstallerURL("v0.5.70"), common.FORK_RELEASE_BASE+"v0.5.70/install.sh"; got != want {
		t.Errorf("resolveReleaseInstallerURL() with the default in config = %q, want %q", got, want)
	}
	// an installer named by config stays
	config.ServerInfo.UpdateUrl = "https://example.com/install.sh"
	if got := resolveReleaseInstallerURL("v0.5.70"); got != "https://example.com/install.sh" {
		t.Errorf("resolveReleaseInstallerURL() with a configured installer = %q", got)
	}
}

func TestSettledReleaseIsTheNewestOldEnoughStableOne(t *testing.T) {
	list := `[
		{"tag_name":"v0.5.73","published_at":"2026-11-03T07:44:56Z"},
		{"tag_name":"v0.5.72","published_at":"2026-11-02T20:49:26Z"},
		{"tag_name":"v0.5.69","published_at":"2026-11-01T08:23:38Z","prerelease":true},
		{"tag_name":"v0.5.68","published_at":"2026-10-31T15:06:58Z","draft":true},
		{"tag_name":"v0.5.67","published_at":"2026-10-31T15:47:12Z"},
		{"tag_name":"v0.5.66","published_at":"2026-10-31T12:38:20Z"}]`
	before, _ := time.Parse(time.RFC3339, "2026-11-01T12:00:00Z")
	if got := pickSettled(list, before); got.Version != "v0.5.67" || got.PublishedAt != "2026-10-31T15:47:12Z" {
		t.Errorf("pickSettled() = %+v, want v0.5.67", got)
	}
	if got := pickSettled(list, time.Time{}); got.Version != "" {
		t.Errorf("pickSettled() with nothing old enough = %+v", got)
	}
}

func TestShellQuote(t *testing.T) {
	got := shellQuote("https://example.com/a'b")
	want := `'https://example.com/a'"'"'b'`
	if got != want {
		t.Fatalf("shellQuote() = %q, want %q", got, want)
	}
}

func TestDetachedUpdateCommand(t *testing.T) {
	got := detachedUpdateCommand("/var/lib/casaos/update/install-v0.4.20.sh", "/var/log/casaos/upgrade.log")
	want := `exec >> '/var/log/casaos/upgrade.log' 2>&1; exec /bin/bash '/var/lib/casaos/update/install-v0.4.20.sh'`
	if got != want {
		t.Fatalf("detachedUpdateCommand() = %q, want %q", got, want)
	}
}

func TestDetachedUpdateArgs(t *testing.T) {
	got := detachedUpdateArgs("/var/lib/casaos/update/install-v0.4.20.sh", "/var/log/casaos/upgrade.log", "v0.4.20")
	want := []string{
		"--quiet",
		"--collect",
		"--unit=casaos-update",
		"--property=Type=exec",
		"--setenv=CASAOS_INSTALLER_DETACHED=1",
		"--description=CasaOS update v0.4.20",
		"/bin/bash",
		"-c",
		"exec >> '/var/log/casaos/upgrade.log' 2>&1; exec /bin/bash '/var/lib/casaos/update/install-v0.4.20.sh'",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("detachedUpdateArgs() = %#v, want %#v", got, want)
	}
}
