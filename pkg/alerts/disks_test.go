package alerts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/external"
)

var (
	system = disk{Name: "mmcblk0", Model: "System", Serial: "0x1234", SmartStatus: "unavailable"}
	wd     = disk{Name: "sdb", Model: "WDC WD40EFRX", Serial: "WD-1", SmartStatus: "passed"}
	data   = storage{MountPoint: "/DATA", Label: "Data", Size: "1000", Used: "500"}
)

func keys(alerts []alert) []string {
	var keys []string
	for _, a := range alerts {
		keys = append(keys, a.key)
	}
	return keys
}

func TestWhatAPollShows(t *testing.T) {
	failing := wd
	failing.SmartStatus = "failed"
	full := data
	full.Used = "931"
	for _, tc := range []struct {
		name              string
		previous, current []disk
		volumes           []storage
		raise, resolve    []string
	}{
		{"all well: every condition cleared", nil, []disk{system, wd}, []storage{data},
			nil, []string{"disk:0x1234:missing", "disk:WD-1:smart", "disk:WD-1:missing", "storage:/DATA:full"}},
		{"a disk fails its SMART check", []disk{system, wd}, []disk{system, failing}, nil,
			[]string{"disk:WD-1:smart"}, []string{"disk:0x1234:missing", "disk:WD-1:missing"}},
		{"a disk seen at the previous poll is gone", []disk{system, wd}, []disk{system}, nil,
			[]string{"disk:WD-1:missing"}, []string{"disk:0x1234:missing"}},
		{"a disk without a serial goes by its name", []disk{{Name: "sdc"}}, []disk{system}, nil,
			[]string{"disk:sdc:missing"}, []string{"disk:0x1234:missing"}},
		{"a storage above the threshold", nil, []disk{system}, []storage{full, {MountPoint: "/media/usb", Label: "USB", Size: "1000", Used: "900"}},
			[]string{"storage:/DATA:full"}, []string{"disk:0x1234:missing", "storage:/media/usb:full"}},
		{"a storage without a size or a mount point is left alone", nil, []disk{system}, []storage{{MountPoint: "/x", Size: "0", Used: "0"}, {Size: "10", Used: "10"}, {MountPoint: "/y", Size: "", Used: "1"}},
			nil, []string{"disk:0x1234:missing"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raise, resolve := diskAlerts(tc.previous, tc.current, tc.volumes, 90)
			if !reflect.DeepEqual(keys(raise), tc.raise) || !reflect.DeepEqual(keys(resolve), tc.resolve) {
				t.Fatalf("raise %v, resolve %v; want %v, %v", keys(raise), keys(resolve), tc.raise, tc.resolve)
			}
			for _, a := range append(raise, resolve...) {
				if a.category != Disks {
					t.Fatalf("%s is in %s, want disks", a.key, a.category)
				}
			}
		})
	}

	raise, _ := diskAlerts([]disk{wd}, []disk{failing}, []storage{full}, 90)
	sentences := []string{raise[0].sentence, raise[1].sentence}
	want := []string{
		"Disk sdb (WDC WD40EFRX) fails its SMART check: copy its data elsewhere and replace it.",
		"Storage Data is 93 % full, above 90 %.",
	}
	if !reflect.DeepEqual(sentences, want) {
		t.Fatalf("sentences %q, want %q", sentences, want)
	}
}

// localStorageFixture plays the gateway's management API and LocalStorage,
// both answering internal requests only, with the disks and storages set.
type localStorageFixture struct {
	mu       sync.Mutex
	disks    []disk
	storages []storage
	down     bool
}

func newLocalStorage(t *testing.T, runtimePath string) *localStorageFixture {
	t.Helper()
	if err := external.WriteInternalSecret(runtimePath); err != nil {
		t.Fatal(err)
	}
	secret := external.InternalAuthorization(runtimePath)
	f := &localStorageFixture{}
	result := func(w http.ResponseWriter, data any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": 200, "message": "ok", "data": data})
	}
	localStorage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Header.Get("Authorization") != secret:
			w.WriteHeader(http.StatusUnauthorized)
		case f.down:
			w.WriteHeader(http.StatusBadGateway)
		case r.URL.Path == "/v1/disks":
			result(w, map[string]any{"disks": f.disks, "avail": []any{}})
		case r.URL.Path == "/v1/storage" && r.URL.Query().Get("system") == "show":
			result(w, []any{map[string]any{"disk_name": "all", "children": f.storages}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(localStorage.Close)
	management := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != secret || r.URL.Path != "/v1/gateway/routes" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]string{
			{"path": "/v1/sys", "target": "http://127.0.0.1:1"},
			{"path": "/v1/disks", "target": localStorage.URL},
		})
	}))
	t.Cleanup(management.Close)
	if err := os.WriteFile(filepath.Join(runtimePath, external.ManagementURLFilename), []byte(management.URL+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *localStorageFixture) set(disks []disk, storages []storage, down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disks, f.storages, f.down = disks, storages, down
}

// Each poll asks LocalStorage as the dashboard does; what it could not ask
// concludes nothing.
func TestPollingLocalStorage(t *testing.T) {
	h, o, _ := newHub(t)
	runtimePath := t.TempDir()
	h.RuntimePath = func() string { return runtimePath }
	f := newLocalStorage(t, runtimePath)
	poll := func(disks []disk, storages []storage, down bool) {
		t.Helper()
		f.set(disks, storages, down)
		h.pollDisks(context.Background())
		h.sending.Wait()
	}
	failing := wd
	failing.SmartStatus = "failed"
	full := data
	full.Used = "950"
	address := "\nhttp://192.168.1.20"

	poll([]disk{system, failing}, []storage{full}, false)
	poll(nil, nil, true)                         // LocalStorage down: no disk is gone
	poll([]disk{}, []storage{data}, false)       // an answer without a disk: nothing is concluded
	poll([]disk{system}, []storage{data}, false) // sdb gone, /DATA emptied
	poll([]disk{system, wd}, []storage{data}, false)

	got := o.texts(phone)
	want := []string{
		"Disk sdb (WDC WD40EFRX) fails its SMART check: copy its data elsewhere and replace it." + address,
		"Storage Data is 95 % full, above 90 %." + address,
		"Resolved: storage Data is back under 90 %." + address,
		"Disk sdb (WDC WD40EFRX) is gone: it was there an hour ago." + address,
		"Resolved: disk sdb (WDC WD40EFRX) passes its SMART check again." + address,
		"Resolved: disk sdb (WDC WD40EFRX) is back." + address,
	}
	slices.Sort(got) // each alert is sent on its own
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sent %q, want %q", got, want)
	}
}

func TestNoPollWithoutTheGateway(t *testing.T) {
	h, o, _ := newHub(t)
	h.seen = []disk{wd}

	h.pollDisks(context.Background()) // no management.url in the runtime path
	h.sending.Wait()

	if len(o.sent) != 0 || !reflect.DeepEqual(h.seen, []disk{wd}) {
		t.Fatalf("sent %+v, seen %+v: want nothing concluded", o.sent, h.seen)
	}
}
