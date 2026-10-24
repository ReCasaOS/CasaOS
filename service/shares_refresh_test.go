package service

import (
	"errors"
	"reflect"
	"testing"

	model2 "github.com/ReCasaOS/CasaOS/service/model"
)

func sambaConfig(shares ...model2.SharesDBModel) []byte {
	config := ""
	for _, share := range shares {
		config += sambaSection(share)
	}
	return []byte(config)
}

func TestChangedSambaShares(t *testing.T) {
	media := model2.SharesDBModel{Path: "/DATA/Media", Username: "bob"}
	docs := model2.SharesDBModel{Path: "/DATA/Docs"}
	old := model2.SharesDBModel{Path: "/DATA/Old"}
	before := sambaConfig(media, docs, old)

	mediaAlice := media
	mediaAlice.Username = "alice"
	after := sambaConfig(mediaAlice, docs, model2.SharesDBModel{Path: "/DATA/New"})

	// Media: account changed; Old: gone; Docs: unchanged; New: added (no sessions)
	if got, want := changedSambaShares(before, after), []string{"Media", "Old"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("changedSambaShares() = %v, want %v", got, want)
	}
	if got := changedSambaShares(before, before); len(got) != 0 {
		t.Fatalf("unchanged configuration: %v", got)
	}
	if got := changedSambaShares(nil, after); len(got) != 0 {
		t.Fatalf("first configuration: %v", got)
	}
}

func TestTimeMachineSharesChanged(t *testing.T) {
	media := model2.SharesDBModel{Path: "/DATA/Media"}
	backups := model2.SharesDBModel{Path: "/DATA/Backups", Username: "bob", TimeMachine: true}
	mediaBob := media
	mediaBob.Username = "bob"
	backupsAlice := backups
	backupsAlice.Username = "alice"

	for _, c := range []struct {
		name          string
		before, after []byte
		want          bool
	}{
		{"added", sambaConfig(media), sambaConfig(media, backups), true},
		{"removed", sambaConfig(media, backups), sambaConfig(media), true},
		{"other edits", sambaConfig(media, backups), sambaConfig(mediaBob, backupsAlice), false},
		{"none", sambaConfig(media), sambaConfig(media), false},
	} {
		if got := timeMachineSharesChanged(c.before, c.after); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRefreshSamba(t *testing.T) {
	media := model2.SharesDBModel{Path: "/DATA/Media"}
	mediaBob := model2.SharesDBModel{Path: "/DATA/Media", Username: "bob"}
	backups := model2.SharesDBModel{Path: "/DATA/Backups", TimeMachine: true}

	for _, c := range []struct {
		name          string
		mainChanged   bool
		before, after []byte
		reloadErr     error
		wantRestarts  int
		wantReloads   [][]string
	}{
		{"share edit reloads, closes the edited share", false, sambaConfig(media), sambaConfig(mediaBob), nil, 0, [][]string{{"Media"}}},
		{"share added reloads, closes nothing", false, sambaConfig(media), sambaConfig(media, model2.SharesDBModel{Path: "/DATA/Docs"}), nil, 0, [][]string{{}}},
		{"smb.conf changed restarts", true, sambaConfig(media), sambaConfig(mediaBob), nil, 1, nil},
		{"Time Machine set changed restarts", false, sambaConfig(media), sambaConfig(media, backups), nil, 1, nil},
		{"reload fails: restart", false, sambaConfig(media), sambaConfig(mediaBob), errors.New("smbd not running"), 1, [][]string{{"Media"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			restarts := 0
			var reloads [][]string
			s := &sharesStruct{
				restartSMBD: func() error { restarts++; return nil },
				reloadSMBD: func(closeShares []string) error {
					reloads = append(reloads, closeShares)
					return c.reloadErr
				},
			}
			if err := s.refreshSamba(c.mainChanged, c.before, c.after); err != nil {
				t.Fatal(err)
			}
			if restarts != c.wantRestarts {
				t.Errorf("restarts = %d, want %d", restarts, c.wantRestarts)
			}
			if !reflect.DeepEqual(reloads, c.wantReloads) {
				t.Errorf("reloads = %v, want %v", reloads, c.wantReloads)
			}
		})
	}
}
