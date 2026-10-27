package alerts

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// raid is an array as LocalStorage answers it; members are "disk:state".
func raid(name, level, state string, disks, missing int, members ...string) raidArray {
	a := raidArray{Name: name, Level: level, State: state, Disks: disks, Missing: missing}
	for _, m := range members {
		disk, s, _ := strings.Cut(m, ":")
		a.Members = append(a.Members, raidMember{Disk: disk, State: s})
	}
	return a
}

func TestWhatTheArraysShow(t *testing.T) {
	for _, tc := range []struct {
		name           string
		arrays         []raidArray
		raise, resolve []string
		raiseSentences []string
	}{
		{"a healthy array clears both", []raidArray{raid("md0", "raid1", "ok", 2, 0, "sdb:active", "sdc:active")},
			nil, []string{"raid:md0:degraded", "raid:md0:inactive"}, nil},
		{"a check or a resync is not a fault", []raidArray{raid("md0", "raid1", "checking", 2, 0), raid("md1", "raid5", "resyncing", 3, 0)},
			nil, []string{"raid:md0:degraded", "raid:md0:inactive", "raid:md1:degraded", "raid:md1:inactive"}, nil},
		{"a disk gone", []raidArray{raid("md0", "raid1", "degraded", 2, 1, "sdb:active")},
			[]string{"raid:md0:degraded"}, []string{"raid:md0:inactive"},
			[]string{"RAID md0 (RAID1) is degraded: 1 of its 2 disks is missing. Replace it before another disk fails."}},
		{"two disks gone", []raidArray{raid("md0", "raid6", "degraded", 4, 2)},
			[]string{"raid:md0:degraded"}, []string{"raid:md0:inactive"},
			[]string{"RAID md0 (RAID6) is degraded: 2 of its 4 disks are missing. Replace them before another disk fails."}},
		{"a disk failed", []raidArray{raid("md0", "raid1", "degraded", 2, 1, "sdb:active", "sdc:faulty")},
			[]string{"raid:md0:degraded"}, []string{"raid:md0:inactive"},
			[]string{"RAID md0 (RAID1) is degraded: disk sdc has failed. Replace it before another disk fails."}},
		{"a spare stood in for a failed disk", []raidArray{raid("md0", "raid1", "degraded", 2, 0, "sdb:active", "sdc:faulty", "sdd:active")},
			[]string{"raid:md0:degraded"}, []string{"raid:md0:inactive"},
			[]string{"Disk sdc has failed in RAID md0 (RAID1); a spare took its place. Replace the failed disk."}},
		{"a rebuild is still degraded", []raidArray{raid("md0", "raid5", "rebuilding", 3, 1, "sdb:active", "sdc:active", "sdd:rebuilding")},
			[]string{"raid:md0:degraded"}, []string{"raid:md0:inactive"},
			[]string{"RAID md0 (RAID5) is degraded: 1 of its 3 disks is missing. Replace it before another disk fails."}},
		{"an array that does not run", []raidArray{raid("md0", "raid1", "inactive", 2, 0)},
			[]string{"raid:md0:inactive"}, nil,
			[]string{"RAID md0 (RAID1) is not running: its data cannot be reached."}},
		{"no array, nothing to say", nil, nil, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raise, resolve := raidAlerts(tc.arrays)
			if !reflect.DeepEqual(keys(raise), tc.raise) || !reflect.DeepEqual(keys(resolve), tc.resolve) {
				t.Fatalf("raise %v, resolve %v; want %v, %v", keys(raise), keys(resolve), tc.raise, tc.resolve)
			}
			var sentences []string
			for _, a := range raise {
				sentences = append(sentences, a.sentence)
				if a.category != Disks {
					t.Fatalf("%s is in %s, want disks", a.key, a.category)
				}
			}
			if !reflect.DeepEqual(sentences, tc.raiseSentences) {
				t.Fatalf("sentences %q, want %q", sentences, tc.raiseSentences)
			}
		})
	}
}

// A poll asks LocalStorage; a rebuild that ends says the array is complete
// again, and a LocalStorage too old to answer says nothing.
func TestPollingTheArrays(t *testing.T) {
	h, o, _ := newHub(t)
	runtimePath := t.TempDir()
	h.RuntimePath = func() string { return runtimePath }
	f := newLocalStorage(t, runtimePath)
	poll := func(arrays []raidArray) {
		t.Helper()
		f.mu.Lock()
		f.raid = arrays
		f.mu.Unlock()
		h.pollRAID(context.Background())
		h.sending.Wait()
	}
	address := "\nhttp://192.168.1.20"

	poll(nil) // an older LocalStorage: 404
	poll([]raidArray{raid("md0", "raid1", "degraded", 2, 1, "sdb:active")})
	poll([]raidArray{raid("md0", "raid1", "rebuilding", 2, 1, "sdb:active", "sdc:rebuilding")})
	poll([]raidArray{raid("md0", "raid1", "ok", 2, 0, "sdb:active", "sdc:active")})

	got := o.texts(phone)
	want := []string{
		"RAID md0 (RAID1) is degraded: 1 of its 2 disks is missing. Replace it before another disk fails." + address,
		"Resolved: RAID md0 (RAID1) is complete again." + address,
	}
	slices.Sort(got)
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sent %q, want %q", got, want)
	}
}
