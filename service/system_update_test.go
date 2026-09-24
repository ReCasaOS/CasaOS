package service

import (
	"reflect"
	"testing"

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

func TestShellQuote(t *testing.T) {
	got := shellQuote("https://example.com/a'b")
	want := `'https://example.com/a'"'"'b'`
	if got != want {
		t.Fatalf("shellQuote() = %q, want %q", got, want)
	}
}

func TestDetachedUpdateCommand(t *testing.T) {
	got := detachedUpdateCommand("https://example.com/a'b", "/var/log/casaos/upgrade.log")
	want := `set -o pipefail; exec >> '/var/log/casaos/upgrade.log' 2>&1; curl -fsSL 'https://example.com/a'"'"'b' | /bin/bash`
	if got != want {
		t.Fatalf("detachedUpdateCommand() = %q, want %q", got, want)
	}
}

func TestDetachedUpdateArgs(t *testing.T) {
	got := detachedUpdateArgs("https://example.com/install.sh", "/var/log/casaos/upgrade.log", "v0.4.20")
	want := []string{
		"--quiet",
		"--collect",
		"--unit=casaos-update",
		"--property=Type=exec",
		"--setenv=CASAOS_INSTALLER_DETACHED=1",
		"--description=CasaOS update v0.4.20",
		"/bin/bash",
		"-c",
		"set -o pipefail; exec >> '/var/log/casaos/upgrade.log' 2>&1; curl -fsSL 'https://example.com/install.sh' | /bin/bash",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("detachedUpdateArgs() = %#v, want %#v", got, want)
	}
}
