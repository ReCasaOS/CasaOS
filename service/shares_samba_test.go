package service

import (
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/config"
	model2 "github.com/ReCasaOS/CasaOS/service/model"
)

func TestValidateSambaUsername(t *testing.T) {
	valid := []string{
		"alice",
		"a",
		"_service",
		"casa-share1",
		"abcdefghijklmnopqrstuvwxyz01234", // 31 characters, the limit
	}

	for _, name := range valid {
		if err := ValidateSambaUsername(name); err != nil {
			t.Errorf("ValidateSambaUsername(%q) = %v, want nil", name, err)
		}
	}

	// Everything here would reach useradd, smbpasswd and an smb.conf line.
	invalid := []string{
		"",
		"Alice",                            // uppercase is not portable
		"1alice",                           // must not start with a digit
		"-alice",                           // must not start with a hyphen
		"al ice",                           // whitespace
		"alice;rm -rf /",                   // command separator
		"alice$(id)",                       // command substitution
		"alice`id`",                        // command substitution, backticks
		"alice&&id",                        // command chaining
		"alice|id",                         // pipe
		"alice\nroot",                      // newline, would inject an smb.conf line
		"alice\troot",                      // tab
		"../root",                          // path traversal
		"root ",                            // trailing space
		"abcdefghijklmnopqrstuvwxyz012345", // 32 characters, one too many
	}

	for _, name := range invalid {
		if err := ValidateSambaUsername(name); err == nil {
			t.Errorf("ValidateSambaUsername(%q) = nil, want an error", name)
		}
	}
}

func TestSambaSectionAnonymous(t *testing.T) {
	section := sambaSection(model2.SharesDBModel{Path: "/DATA/Media"})

	// An empty Username is what every share created before this feature carries.
	// Its section has to stay exactly as it was, or an upgrade changes behaviour
	// for shares nobody asked to change.
	for _, want := range []string{
		"[Media]",
		"path = /DATA/Media",
		"guest ok = Yes",
		"public = Yes",
		"create mask = 0777",
		"directory mask = 0777",
		"force user = root",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("anonymous section is missing %q:\n%s", want, section)
		}
	}
}

func TestSambaSectionAuthenticated(t *testing.T) {
	section := sambaSection(model2.SharesDBModel{Path: "/DATA/Private", Username: "alice"})

	for _, want := range []string{
		"[Private]",
		"guest ok = No",
		"public = No",
		"valid users = alice",
		"create mask = 0660",
		"directory mask = 0770",
		"force user = alice",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("authenticated section is missing %q:\n%s", want, section)
		}
	}

	// Each of these alone re-opens the share, which is the failure mode that
	// looks like success: the UI says private, the wire says otherwise.
	for _, unwanted := range []string{
		"guest ok = Yes",
		"public = Yes",
		"force user = root",
		"mask = 0777",
	} {
		if strings.Contains(section, unwanted) {
			t.Errorf("authenticated section still contains %q:\n%s", unwanted, section)
		}
	}
}

func TestSambaSectionNamesTheDirectory(t *testing.T) {
	section := sambaSection(model2.SharesDBModel{Path: "/DATA/Media/Movies"})
	if !strings.HasPrefix(strings.TrimSpace(section), "[Movies]") {
		t.Fatalf("section should be named after the directory, got:\n%s", section)
	}
}

func TestSambaSectionTimeMachine(t *testing.T) {
	for _, share := range []model2.SharesDBModel{
		{Path: "/DATA/Backups", TimeMachine: true},
		{Path: "/DATA/Backups", Username: "alice", TimeMachine: true},
	} {
		section := sambaSection(share)
		for _, want := range []string{"vfs objects = catia fruit streams_xattr", "fruit:time machine = yes"} {
			if !strings.Contains(section, want) {
				t.Errorf("time machine section (username %q) is missing %q:\n%s", share.Username, want, section)
			}
		}
	}

	// A share without the flag must load no VFS module: "vfs objects" on a host
	// without vfs_fruit installed is what took every share down in the upstream
	// issue thread.
	for _, share := range []model2.SharesDBModel{
		{Path: "/DATA/Media"},
		{Path: "/DATA/Private", Username: "alice"},
	} {
		section := sambaSection(share)
		for _, unwanted := range []string{"vfs objects", "fruit:"} {
			if strings.Contains(section, unwanted) {
				t.Errorf("plain section (username %q) contains %q:\n%s", share.Username, unwanted, section)
			}
		}
	}
}

func TestIncludesShareConfig(t *testing.T) {
	for _, c := range []struct {
		config string
		want   bool
	}{
		{"[global]\n   include = /etc/samba/smb.casa.conf\n", true},
		{"[global]\ninclude=/etc/samba/smb.casa.conf", true},
		{"[global]\n\tInclude =  /etc/samba/smb.casa.conf  \n", true},
		{"[global]\n# include = /etc/samba/smb.casa.conf\n", false},
		{"[global]\ninclude = /etc/samba/other.conf\n", false},
		{"", false},
	} {
		if got := includesShareConfig([]byte(c.config)); got != c.want {
			t.Errorf("includesShareConfig(%q) = %v, want %v", c.config, got, c.want)
		}
	}
}

// external: CasaOS never writes smb.conf (no template, no guest-mapping flip)
func TestExternalMainConfigIsNeverWritten(t *testing.T) {
	previous := config.ServerInfo.SambaMainConfig
	config.ServerInfo.SambaMainConfig = "external"
	t.Cleanup(func() { config.ServerInfo.SambaMainConfig = previous })

	if (&sharesStruct{}).initSambaConfig() {
		t.Error("initSambaConfig wrote smb.conf in external mode")
	}
	if changed, err := syncGuestMapping(true); changed || err != nil {
		t.Errorf("syncGuestMapping in external mode = %v, %v; want false, nil", changed, err)
	}
}
