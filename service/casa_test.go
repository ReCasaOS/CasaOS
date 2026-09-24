package service

import (
	"testing"

	"github.com/ReCasaOS/CasaOS/common"
	"github.com/ReCasaOS/CasaOS/pkg/config"
)

func TestParseReleaseVersion(t *testing.T) {
	tests := []struct {
		name        string
		payload     string
		version     string
		changeLog   string
		publishedAt string
	}{
		{name: "release manifest", payload: `{"version":"v0.4.20","change_log":"Fork updater"}`, version: "v0.4.20", changeLog: "Fork updater"},
		{name: "release manifest with its publication time", payload: `{"version":"v0.5.8","change_log":"Notes","published_at":"2026-09-21T10:00:00Z"}`, version: "v0.5.8", changeLog: "Notes", publishedAt: "2026-09-21T10:00:00Z"},
		// A publication time that is not one never costs the button its release.
		{name: "release manifest with a malformed publication time", payload: `{"version":"v0.5.8","change_log":"Notes","published_at":"soon"}`, version: "v0.5.8", changeLog: "Notes", publishedAt: "soon"},
		{name: "legacy API", payload: `{"data":{"version":"0.4.15","change_log":"Upstream"}}`, version: "0.4.15", changeLog: "Upstream"},
		{name: "GitHub API", payload: `{"tag_name":"v0.4.20","body":"Release body"}`, version: "v0.4.20", changeLog: "Release body"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := parseReleaseVersion(test.payload)
			if got.Version != test.version || got.ChangeLog != test.changeLog || got.PublishedAt != test.publishedAt {
				t.Fatalf("parseReleaseVersion() = %#v, want version %q, changelog %q and published_at %q", got, test.version, test.changeLog, test.publishedAt)
			}
		})
	}
}

func TestResolveUpdateVersionURL(t *testing.T) {
	original := config.ServerInfo.UpdateVersionUrl
	t.Cleanup(func() { config.ServerInfo.UpdateVersionUrl = original })

	config.ServerInfo.UpdateVersionUrl = "https://example.com/version.json"
	if got := resolveUpdateVersionURL(); got != "https://example.com/version.json" {
		t.Fatalf("resolveUpdateVersionURL() = %q", got)
	}

	config.ServerInfo.UpdateVersionUrl = "not a URL"
	if got := resolveUpdateVersionURL(); got != common.FORK_VERSION_URL {
		t.Fatalf("resolveUpdateVersionURL() fallback = %q", got)
	}
}

// The install check serves its own version.json over loopback; plain HTTP
// anywhere else is refused, and the configured URL answers.
func TestTheInstallCheckOverridesTheVersionURL(t *testing.T) {
	original := config.ServerInfo.UpdateVersionUrl
	t.Cleanup(func() { config.ServerInfo.UpdateVersionUrl = original })
	config.ServerInfo.UpdateVersionUrl = "https://example.com/version.json"

	for _, tc := range []struct{ override, want string }{
		{"http://127.0.0.1:8123/version.json", "http://127.0.0.1:8123/version.json"},
		{"https://releases.example.org/version.json", "https://releases.example.org/version.json"},
		{"http://192.168.1.20:8123/version.json", "https://example.com/version.json"},
		{"http://example.org/version.json", "https://example.com/version.json"},
		{"", "https://example.com/version.json"},
	} {
		t.Setenv("CASAOS_AUTOUPDATE_VERSION_URL", tc.override)
		if got := resolveUpdateVersionURL(); got != tc.want {
			t.Errorf("with the override %q, resolveUpdateVersionURL() = %q, want %q", tc.override, got, tc.want)
		}
	}
}
