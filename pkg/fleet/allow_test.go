package fleet

import (
	"testing"
	"time"
)

func TestAllowedTable(t *testing.T) {
	for _, c := range []struct {
		method, path string
		slow, ok     bool
	}{
		{"GET", "/v1/sys/version/check", false, true},
		{"GET", "/v1/sys/os", false, true},
		{"GET", "/v1/sys/utilization", false, true},
		{"GET", "/v1/sys/modules", false, true},
		{"GET", "/v1/sys/autoupdate", false, true},
		{"GET", "/v1/sys/packages", true, true},
		{"GET", "/v1/sys/packages/update/status", false, true},
		{"GET", "/v1/sys/docker/update/status", false, true},
		{"GET", "/v2/casaos/health/services", false, true},
		{"GET", "/v2/app_management/compose", false, true},
		{"GET", "/v2/app_management/web/appgrid", false, true},
		{"GET", "/v2/app_management/operations", false, true},
		{"GET", "/v2/app_management/compose/updates", true, true},
		{"GET", "/v2/app_management/compose/updates/run", false, true},
		{"GET", "/v2/app_management/logs/apps", false, true},
		{"GET", "/v2/app_management/logs", false, true},
		{"GET", "/v2/app_management/logs/events", false, true},
		{"GET", "/v2/app_management/logs/export", true, true},
		{"GET", "/v2/virt_management/vms", false, true},
		{"POST", "/v1/sys/update", false, true},
		{"POST", "/v1/sys/packages/update", false, true},
		{"PUT", "/v2/app_management/compose/plex/status", false, true},
		{"PATCH", "/v2/app_management/compose/plex", false, true},
		{"POST", "/v2/app_management/compose/updates", false, true},
		{"POST", "/v2/app_management/image-updates", true, true},
		{"POST", "/v2/virt_management/vms/0f1e-2d3c/start", false, true},
		{"POST", "/v2/virt_management/vms/0f1e-2d3c/force-off", false, true},
		{"GET", "/v2/app_management/backup/runs", false, true},
		{"GET", "/v2/app_management/backup/schedules", false, true},
		{"GET", "/v2/app_management/backup/verifications", false, true},
		{"GET", "/v2/app_management/backup/verification-schedule", false, true},
		{"POST", "/v2/app_management/backup/schedules/run", false, true},
		{"POST", "/v2/app_management/backup/verifications", false, true},
		{"POST", "/v2/app_management/backup/verifications/ABC123/cancel", false, true},
		{"PUT", "/v2/app_management/backup/verification-schedule", false, true},
		{"GET", "/v2/app_management/backup/destinations/GDrive/restic", false, true},
		{"POST", "/v2/app_management/backup/destinations/GDrive/restic/check", false, true},
		// refused
		{"GET", "/v2/app_management/logs/settings", false, false},
		{"PUT", "/v2/app_management/logs/settings", false, false},
		{"GET", "/v2/app_management/logs/stream", false, false},
		{"GET", "/v1/sys/update", false, false},
		{"DELETE", "/v2/app_management/compose/plex", false, false},
		{"GET", "/v1/file/content", false, false},
		{"GET", "/v1/users/current", false, false},
		{"POST", "/v2/virt_management/vms/0f1e-2d3c/reset", false, false},
		{"POST", "/v2/virt_management/vms/a/b/start", false, false},
		{"GET", "/v2/app_management/compose/plex/logs", false, false},
		{"POST", "/v1/sys/modules/virt/install", false, false},
		{"GET", "/v2/app_management/backup/destinations/GDrive/restic/password", false, false},
		{"PUT", "/v2/app_management/backup/destinations/GDrive/restic", false, false},
		{"DELETE", "/v2/app_management/backup/destinations/GDrive/restic", false, false},
		{"POST", "/v2/app_management/backup/destinations/GDrive/restic/enable", false, false},
		{"GET", "/v2/app_management/backup/destinations", false, false},
		{"GET", "/v2/app_management/backup/destinations/GDrive/runs", false, false},
		{"POST", "/v2/app_management/backup/restore", false, false},
		{"GET", "/v2/app_management/backup/browse", false, false},
		{"DELETE", "/v2/app_management/backup/runs", false, false},
		{"PUT", "/v2/app_management/backup/schedules", false, false},
		{"POST", "/v2/app_management/backup/signin", false, false},
		{"GET", "/v2/app_management/backup/destinations/my%20drive/restic", false, false},
		{"GET", "/v2/app_management/backup/destinations/../restic", false, false},
	} {
		slow, ok := Allowed(c.method, c.path)
		if slow != c.slow || ok != c.ok {
			t.Errorf("%s %s: slow %v ok %v, want %v %v", c.method, c.path, slow, ok, c.slow, c.ok)
		}
	}
}

// Nothing is normalised: a path is matched as it is, so the table cannot be reached around.
func TestAllowedNormalisesNothing(t *testing.T) {
	for _, path := range []string{"/v1/sys/os/", "//v1/sys/os", "/v1/sys/../sys/os", "/v1/sys%2Fos", "/V1/sys/os", "/v1/sys/os?x=1"} {
		if _, ok := Allowed("GET", path); ok {
			t.Errorf("%q passed", path)
		}
	}
}

func TestTimeout(t *testing.T) {
	if Timeout(false) != 60*time.Second || Timeout(true) != 300*time.Second {
		t.Fatal("timeouts")
	}
}

// An {id} is one plain segment: no character a URL reads as something else (?, #, %), no
// dot segment a server cleans away, nothing a shell or a path would split on. Otherwise a
// row of the table turns into another route once the member builds its request.
func TestAnIDIsOnePlainSegment(t *testing.T) {
	for _, id := range []string{"x?", "x#", "x%3F", "x%2F..", "..", ".", `x\y`, "x y", "x;y", "?", "#"} {
		for _, c := range []struct{ method, path string }{
			{"PUT", "/v2/app_management/compose/" + id + "/status"},
			{"PATCH", "/v2/app_management/compose/" + id},
			{"POST", "/v2/virt_management/vms/" + id + "/start"},
		} {
			if _, ok := Allowed(c.method, c.path); ok {
				t.Errorf("%s %q passed", c.method, c.path)
			}
		}
	}
	for _, path := range []string{"/v2/app_management/backup/verifications//cancel", "/v2/app_management/backup/verifications/a/b/cancel", "/v2/app_management/backup/destinations//restic", "/v2/app_management/backup/destinations/a/b/restic/check"} {
		if _, ok := Allowed("POST", path); ok {
			t.Errorf("%q passed", path)
		}
		if _, ok := Allowed("GET", path); ok {
			t.Errorf("GET %q passed", path)
		}
	}
	for _, id := range []string{"plex", "big-bear-ayon", "my_app", "0f1e-2d3c", "vm.1", "Plex", "a"} {
		if _, ok := Allowed("PATCH", "/v2/app_management/compose/"+id); !ok {
			t.Errorf("%q refused", id)
		}
	}
}
