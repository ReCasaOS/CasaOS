package service

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/ReCasaOS/CasaOS/pkg/sqlite"
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
		"write list = alice",
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

func TestSambaSectionMembers(t *testing.T) {
	section := sambaSection(model2.SharesDBModel{
		Path:     "/DATA/Family",
		Username: "alice",
		Members:  "alice:rw bob:ro carol:rw",
	})

	for _, want := range []string{
		"read only = Yes",
		"valid users = alice bob carol",
		"write list = alice carol",
		"force user = alice",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("members section is missing %q:\n%s", want, section)
		}
	}
}

func TestSambaSectionReadOnlyMembersOnly(t *testing.T) {
	section := sambaSection(model2.SharesDBModel{Path: "/DATA/Photos", Username: "alice", Members: "bob:ro"})

	// An empty "write list =" line would be harmless, but no line at all is what
	// says nobody writes.
	if strings.Contains(section, "write list") {
		t.Errorf("a share with read-only members only has a write list:\n%s", section)
	}

	if !strings.Contains(section, "valid users = bob\n") {
		t.Errorf("the read-only member is not let in:\n%s", section)
	}
}

func TestShareMembersRoundTrip(t *testing.T) {
	members := []model2.ShareMember{{Username: "alice"}, {Username: "bob", ReadOnly: true}}
	share := model2.SharesDBModel{Members: model2.FormatMembers(members)}

	if share.Members != "alice:rw bob:ro" {
		t.Fatalf("unexpected column value %q", share.Members)
	}

	got := share.MemberList()
	if len(got) != 2 || got[0] != members[0] || got[1] != members[1] {
		t.Fatalf("members did not survive the column: %+v", got)
	}
}

func TestShareMembersLegacyRow(t *testing.T) {
	got := (&model2.SharesDBModel{Username: "alice"}).MemberList()
	if len(got) != 1 || got[0] != (model2.ShareMember{Username: "alice"}) {
		t.Fatalf("a row from before members should give its account write access, got %+v", got)
	}

	if got := (&model2.SharesDBModel{}).MemberList(); len(got) != 0 {
		t.Fatalf("a guest share has no members, got %+v", got)
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

// external with the include missing: a delete is refused before the row goes, or the share would come back once the include is added
func TestADeleteRefusedForAMissingIncludeKeepsTheShare(t *testing.T) {
	previous := config.ServerInfo.SambaMainConfig
	config.ServerInfo.SambaMainConfig = "external"
	t.Cleanup(func() { config.ServerInfo.SambaMainConfig = previous })
	if checkExternalMainConfig() == nil {
		t.Skip("this host's smb.conf includes smb.casa.conf")
	}

	db := sqlite.GetDb(t.TempDir())
	share := model2.SharesDBModel{Name: "Media", Path: "/DATA/Media"}
	if err := db.Create(&share).Error; err != nil {
		t.Fatal(err)
	}
	s := &sharesStruct{db: db}

	if err := s.DeleteShare(strconv.Itoa(int(share.ID))); err == nil {
		t.Error("DeleteShare succeeded without the include")
	}
	if err := s.DeleteShareByPath("/DATA"); err == nil {
		t.Error("DeleteShareByPath succeeded without the include")
	}
	var count int64
	db.Model(&model2.SharesDBModel{}).Count(&count)
	if count != 1 {
		t.Errorf("%d shares left, want the refused one kept", count)
	}
}

func TestMergeDuplicateShares(t *testing.T) {
	kept, removed := mergeDuplicateShares([]model2.SharesDBModel{
		{ID: 1, Path: "/DATA/AppData", Username: "pippo", Members: "pippo:rw"},
		{ID: 2, Path: "/DATA/Downloads", Username: "pippo", Members: "pippo:ro"},
		{ID: 3, Path: "/DATA/Downloads", Username: "pippo", Members: "pippo:rw anna:ro"},
		{ID: 4, Path: "/DATA/Downloads", Username: "pippo", Members: "pippo:ro", TimeMachine: true},
	})

	if len(removed) != 2 || removed[0] != 3 || removed[1] != 4 {
		t.Fatalf("the newer duplicates should go, got %v", removed)
	}

	if len(kept) != 2 {
		t.Fatalf("one row per folder should stay, got %+v", kept)
	}

	downloads := kept[1]
	if downloads.ID != 2 || downloads.Members != "pippo:rw anna:ro" || !downloads.TimeMachine || downloads.Username != "pippo" {
		t.Errorf("the oldest row should gain what the others gave, got %+v", downloads)
	}
}

func TestMergeDuplicateSharesNothingToDo(t *testing.T) {
	_, removed := mergeDuplicateShares([]model2.SharesDBModel{{ID: 1, Path: "/DATA/A"}, {ID: 2, Path: "/DATA/B"}})
	if len(removed) != 0 {
		t.Errorf("distinct folders are left alone, got %v", removed)
	}
}

func TestValidateNewSharePathRefusesTheSystemDisk(t *testing.T) {
	// /mnt and its folders sit on the system disk in any container or CI
	// runner; only a disk mounted there may be shared.
	if _, err := os.Stat("/mnt"); err != nil {
		t.Skip("no /mnt here")
	}

	if _, err := ValidateNewSharePath("/mnt"); err != ErrSharePathNotOnDisk {
		t.Errorf("/mnt itself should be refused, got %v", err)
	}
}

// An older guest row merged with a protected one: the share becomes protected,
// and its folder, still open to everybody, is handed to the owner.
func TestMergeDuplicateSharesProtectsAGuestFolder(t *testing.T) {
	before := []model2.SharesDBModel{
		{ID: 1, Path: "/DATA/Media", Anonymous: true},
		{ID: 2, Path: "/DATA/Media", Username: "pippo", Members: "pippo:rw"},
		{ID: 3, Path: "/DATA/Docs", Username: "anna", Members: "anna:rw"},
		{ID: 4, Path: "/DATA/Docs", Username: "anna", Members: "anna:rw pippo:ro"},
		{ID: 5, Path: "/DATA/Public", Anonymous: true},
		{ID: 6, Path: "/DATA/Public", Anonymous: true},
	}

	kept, _ := mergeDuplicateShares(before)
	got := newlyProtected(before, kept)

	if len(got) != 1 || got[0].Path != "/DATA/Media" || got[0].Username != "pippo" || got[0].Anonymous {
		t.Fatalf("only /DATA/Media changed from guest to protected, got %+v", got)
	}
}

// A disk mounted on /mnt itself is a disk: what is on it may be shared. The
// system disk is the one / is on.
func TestValidateNewSharePathComparesWithTheSystemDisk(t *testing.T) {
	if _, err := os.Stat("/mnt"); err != nil {
		t.Skip("no /mnt here")
	}

	_, err := ValidateNewSharePath("/mnt")
	if sameFilesystem("/mnt", "/") {
		if err != ErrSharePathNotOnDisk {
			t.Errorf("/mnt on the system disk should be refused, got %v", err)
		}
	} else if err == ErrSharePathNotOnDisk {
		t.Errorf("a disk mounted on /mnt itself should be accepted")
	}
}
