package fleet

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

var pollNow = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

// fakeBox answers the routes of one box; a route it does not know is a 404.
type fakeBox struct {
	mu     sync.Mutex
	routes map[string]Response
	err    error // every call fails with it
	calls  []string
}

func (b *fakeBox) call(_ context.Context, _, method, path string) (Response, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, method+" "+path)
	if b.err != nil {
		return Response{}, b.err
	}
	if r, ok := b.routes[path]; ok {
		return r, nil
	}
	return Response{Status: 404, Body: []byte(`{"message":"not found"}`)}, nil
}

func (b *fakeBox) asked(sub string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, c := range b.calls {
		if strings.Contains(c, sub) {
			n++
		}
	}
	return n
}

func ok(body string) Response {
	return Response{Status: 200, ContentType: "application/json", Body: []byte(body)}
}

const am = "/v2/app_management"

func pollFleet(t *testing.T, b *fakeBox) *Fleet {
	f := New(t.TempDir())
	f.call = b.call
	f.Now = func() time.Time { return pollNow }
	return f
}

var testBox = Box{ID: "box1", Name: "Maison"}

// A healthy box: two apps to GDrive, scheduled daily, last slot two hours ago.
func healthyRoutes() map[string]Response {
	return map[string]Response{
		am + "/backup/runs": ok(`{"message":"ok","data":[
			{"app":"casaos-system","destination":"GDrive","stamp":"20261108-100005","started_at":"2026-11-08T10:00:05Z","finished_at":"2026-11-08T10:03:00Z","scheduled":true,"restore":false,"error":"","bytes":1234},
			{"app":"jarvis","destination":"GDrive","stamp":"20261108-100010","started_at":"2026-11-08T10:00:10Z","finished_at":"2026-11-08T10:05:00Z","scheduled":true,"restore":false,"error":""}]}`),
		am + "/backup/schedules": ok(`{"data":[
			{"app":"casaos-system","destination":"GDrive","every":"daily","at":"10:00","keep":7,"enabled":true,"last_run":"2026-11-08T10:00:05Z","last_slot":"2026-11-08T10:00:00Z","next_slot":"2026-11-09T10:00:00Z"},
			{"app":"jarvis","destination":"GDrive","every":"daily","at":"10:00","keep":7,"enabled":true,"last_run":"2026-11-08T10:00:10Z","last_slot":"2026-11-08T10:00:00Z","next_slot":"2026-11-09T10:00:00Z"}]}`),
		am + "/operations": ok(`{"operations":[{"app":"jarvis","kind":"backup"}],"os_upgrade_open":false}`),
		am + "/backup/verifications": ok(`{"data":[
			{"id":"v1","destination":"GDrive","app":"casaos-system","stamp":"20261108-100005","status":"passed","started_at":"2026-11-08T11:00:00Z","finished_at":"2026-11-08T11:02:00Z"}]}`),
		am + "/backup/verification-schedule":      ok(`{"data":{"enabled":true,"every_days":7,"at":"03:00","last_run":"2026-11-07T03:00:00Z"}}`),
		am + "/backup/destinations/GDrive/restic": ok(`{"data":{"available":true,"enabled":false,"kept":false}}`),
	}
}

func TestPollBuildsTheSnapshot(t *testing.T) {
	b := &fakeBox{routes: healthyRoutes()}
	got := pollFleet(t, b).pollBox(context.Background(), testBox)

	if got.BoxID != "box1" || got.Name != "Maison" || !got.Online || got.Error != "" || got.Stale {
		t.Fatalf("box fields: %+v", got)
	}
	if !got.PolledAt.Equal(pollNow) {
		t.Errorf("PolledAt = %v, want %v", got.PolledAt, pollNow)
	}
	if got.State != "ok" || !got.Green || got.Problems != 0 || len(got.Couples) != 2 {
		t.Fatalf("assessment: %+v", got)
	}
	c := got.Couples[0] // casaos-system sorts first
	if c.App != "casaos-system" || c.Destination != "GDrive" || c.State != "ok" || c.Running {
		t.Errorf("couple 0: %+v", c)
	}
	if c.LastOKStamp != "20261108-100005" || !c.NextSlot.Equal(time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("couple 0 dates: %+v", c)
	}
	if c.Verify.State != "passed" {
		t.Errorf("couple 0 verify: %+v", c.Verify)
	}
	if j := got.Couples[1]; j.App != "jarvis" || !j.Running || j.Verify.State != "none" {
		t.Errorf("couple 1: %+v", j)
	}
	vs := got.VerificationSchedule
	if vs == nil || !vs.Enabled || vs.EveryDays != 7 || vs.At != "03:00" || vs.LastRun.IsZero() {
		t.Errorf("verification schedule: %+v", vs)
	}
	if n := b.asked("/restic"); n != 1 {
		t.Errorf("restic asked %d times for one destination", n)
	}
}

func TestPollKeepsTheLastSnapshotWhenTheBoxFails(t *testing.T) {
	b := &fakeBox{routes: healthyRoutes()}
	f := pollFleet(t, b)
	f.storeBackups(f.pollBox(context.Background(), testBox))
	first := f.backups["box1"]

	b.err = ErrOffline
	f.Now = func() time.Time { return pollNow.Add(10 * time.Minute) }
	f.storeBackups(f.pollBox(context.Background(), testBox))

	got := f.backups["box1"]
	if got.Online || got.Error != "box offline" || got.Green {
		t.Errorf("failure fields: online=%v error=%q green=%v", got.Online, got.Error, got.Green)
	}
	if !got.PolledAt.Equal(first.PolledAt) || len(got.Couples) != 2 || got.Couples[0].State != "ok" {
		t.Errorf("the old content was not kept: %+v", got)
	}
}

func TestPollOfflineWithoutAPreviousSnapshot(t *testing.T) {
	b := &fakeBox{err: ErrOffline}
	f := pollFleet(t, b)
	f.storeBackups(f.pollBox(context.Background(), testBox))

	got := f.backups["box1"]
	if got.BoxID != "box1" || got.Name != "Maison" || got.Online || got.Error != "box offline" || got.State != "unknown" || got.Green || !got.PolledAt.IsZero() {
		t.Errorf("%+v", got)
	}
	if out, _ := json.Marshal(got); !strings.Contains(string(out), `"couples":[]`) {
		t.Errorf("couples must be [] in JSON: %s", out)
	}
}

func TestAnOldBoxIsUnknownNotGreen(t *testing.T) {
	r := healthyRoutes()
	delete(r, am+"/backup/verifications") // 404
	delete(r, am+"/backup/verification-schedule")
	delete(r, am+"/backup/destinations/GDrive/restic")
	r[am+"/backup/schedules"] = ok(`{"data":[{"app":"jarvis","destination":"GDrive","every":"daily","at":"10:00","enabled":true,"last_run":"2026-11-08T10:00:10Z"}]}`)
	got := pollFleet(t, &fakeBox{routes: r}).pollBox(context.Background(), testBox)

	if got.Error != "" || !got.Online {
		t.Errorf("an old box is not an error: %+v", got)
	}
	if len(got.Couples) != 1 || got.Couples[0].State != "unknown" || got.Couples[0].Verify.State != "unknown" {
		t.Errorf("couples: %+v", got.Couples)
	}
	if got.State != "unknown" || got.Green || got.VerificationSchedule != nil {
		t.Errorf("box: %+v", got)
	}
}

func TestAnOldMemberRefusesWith403(t *testing.T) {
	r := map[string]Response{}
	for _, p := range []string{"/backup/runs", "/backup/schedules", "/operations"} {
		r[am+p] = Response{Status: 403, Body: []byte(`{"message":"this route is not relayed"}`)}
	}
	b := &fakeBox{routes: healthyRoutes()}
	f := pollFleet(t, b)
	f.storeBackups(f.pollBox(context.Background(), testBox))

	b.routes = r
	f.storeBackups(f.pollBox(context.Background(), testBox))

	got := f.backups["box1"]
	if got.Error != "this box does not support backup monitoring yet: update it" || got.Green || !got.Online {
		t.Errorf("%+v", got)
	}
	if len(got.Couples) != 2 || !got.PolledAt.Equal(pollNow) {
		t.Errorf("the old snapshot was not kept: %+v", got)
	}
}

func TestDestinationWithASpaceHasUnknownResticState(t *testing.T) {
	r := healthyRoutes()
	r[am+"/backup/schedules"] = ok(`{"data":[{"app":"jarvis","destination":"my drive","enabled":true,"last_run":"2026-11-08T10:00:10Z","last_slot":"2026-11-08T10:00:00Z"}]}`)
	r[am+"/backup/runs"] = ok(`{"data":[{"app":"jarvis","destination":"my drive","stamp":"s","started_at":"2026-11-08T10:00:10Z","finished_at":"2026-11-08T10:01:00Z"}]}`)
	r[am+"/backup/verifications"] = ok(`{"data":[]}`)
	b := &fakeBox{routes: r}
	got := pollFleet(t, b).pollBox(context.Background(), testBox)

	if n := b.asked("/restic"); n != 0 {
		t.Errorf("%d restic requests for a name the table refuses", n)
	}
	if len(got.Couples) != 1 || got.Couples[0].Verify.State != "unknown" || got.Couples[0].Restic {
		t.Errorf("%+v", got.Couples)
	}
}

func TestAResticRouteThatFailsLeavesTheStateUnknown(t *testing.T) {
	r := healthyRoutes()
	r[am+"/backup/verifications"] = ok(`{"data":[]}`)
	r[am+"/backup/destinations/GDrive/restic"] = Response{Status: 500, Body: []byte(`{"message":"boom"}`)}
	got := pollFleet(t, &fakeBox{routes: r}).pollBox(context.Background(), testBox)

	if got.Error != "" || got.Couples[0].Verify.State != "unknown" {
		t.Errorf("%+v", got)
	}
}

func TestPollDoesNotLeakSecrets(t *testing.T) {
	r := healthyRoutes()
	r[am+"/backup/destinations/GDrive/restic"] = ok(`{"data":{"available":true,"enabled":true,"kept":true,"password":"hunter2-secret","token":"tok-123","last_check":{"at":"2026-11-08T09:00:00Z","ok":true}}}`)
	r[am+"/backup/runs"] = ok(`{"data":[{"app":"jarvis","destination":"GDrive","stamp":"s","started_at":"2026-11-08T10:00:10Z","finished_at":"2026-11-08T10:01:00Z","remote_token":"tok-456"}]}`)
	got := pollFleet(t, &fakeBox{routes: r}).pollBox(context.Background(), testBox)

	if !got.Couples[0].Restic {
		t.Fatalf("the restic state was not read: %+v", got.Couples[0])
	}
	out, _ := json.Marshal(got)
	for _, secret := range []string{"hunter2", "tok-123", "tok-456", "password"} {
		if strings.Contains(string(out), secret) {
			t.Errorf("%q reached the snapshot: %s", secret, out)
		}
	}
}

func TestPollDoesNotCopyABoxErrorIntoTheError(t *testing.T) {
	r := healthyRoutes()
	r[am+"/backup/runs"] = Response{Status: 500, Body: []byte(`{"message":"open /root/.config/rclone token=abc"}`)}
	got := pollFleet(t, &fakeBox{routes: r}).pollBox(context.Background(), testBox)

	if got.Error == "" || strings.Contains(got.Error, "rclone") || strings.Contains(got.Error, "token") {
		t.Errorf("Error = %q", got.Error)
	}
	r[am+"/backup/runs"] = ok(`not json at all /root/secret`)
	got = pollFleet(t, &fakeBox{routes: r}).pollBox(context.Background(), testBox)
	if got.Error == "" || strings.Contains(got.Error, "secret") {
		t.Errorf("Error = %q", got.Error)
	}
}

func TestPollMakesFewRequests(t *testing.T) {
	r := healthyRoutes()
	r[am+"/backup/schedules"] = ok(`{"data":[
		{"app":"a","destination":"d1","enabled":true},{"app":"a","destination":"d2","enabled":true},
		{"app":"b","destination":"d1","enabled":true},{"app":"b","destination":"d3","enabled":true}]}`)
	b := &fakeBox{routes: r}
	pollFleet(t, b).pollBox(context.Background(), testBox)

	if got, max := len(b.calls), 5+3; got > max {
		t.Errorf("%d requests, at most %d: %v", got, max, b.calls)
	}
	if b.asked("/restic") != 3 {
		t.Errorf("one restic request per distinct destination: %v", b.calls)
	}
}

func TestPollGivesTheWholePassTwentySeconds(t *testing.T) {
	var deadlines []time.Time
	f := pollFleet(t, &fakeBox{routes: healthyRoutes()})
	inner := f.call
	f.call = func(ctx context.Context, boxID, method, path string) (Response, error) {
		d, ok := ctx.Deadline()
		if !ok {
			t.Errorf("no deadline on the request for %s", path)
		}
		deadlines = append(deadlines, d)
		return inner(ctx, boxID, method, path)
	}
	start := time.Now()
	f.pollBox(context.Background(), testBox)

	if len(deadlines) == 0 {
		t.Fatal("no request made")
	}
	for _, d := range deadlines {
		if left := d.Sub(start); left < 18*time.Second || left > 22*time.Second {
			t.Errorf("deadline %v away, want about 20s", left)
		}
		if !d.Equal(deadlines[0]) {
			t.Errorf("one context for the whole pass: %v then %v", deadlines[0], d)
		}
	}
}

func TestAMandatoryRouteThatIsMissingMeansUpdateIt(t *testing.T) {
	for _, route := range []string{"/backup/runs", "/backup/schedules", "/operations"} {
		for _, status := range []int{403, 404} {
			r := healthyRoutes()
			r[am+route] = Response{Status: status, Body: []byte(`{"message":"gone"}`)}
			got := pollFleet(t, &fakeBox{routes: r}).pollBox(context.Background(), testBox)
			if got.Error != "this box does not support backup monitoring yet: update it" || !got.Online {
				t.Errorf("%s %d: %+v", route, status, got)
			}
		}
	}
}

func TestAServerErrorOnAMandatoryRouteIsNotAnOldBox(t *testing.T) {
	for _, route := range []string{"/backup/runs", "/backup/schedules", "/operations"} {
		r := healthyRoutes()
		r[am+route] = Response{Status: 500, Body: []byte(`{"message":"boom"}`)}
		got := pollFleet(t, &fakeBox{routes: r}).pollBox(context.Background(), testBox)
		if strings.Contains(got.Error, "update it") || !strings.Contains(got.Error, "status 500") {
			t.Errorf("%s: Error = %q", route, got.Error)
		}
	}
}

func TestAFailedPollRefreshesTheName(t *testing.T) {
	b := &fakeBox{routes: healthyRoutes()}
	f := pollFleet(t, b)
	f.storeBackups(f.pollBox(context.Background(), testBox))

	b.err = ErrOffline
	f.storeBackups(f.pollBox(context.Background(), Box{ID: "box1", Name: "Chalet"}))

	if got := f.backups["box1"]; got.Name != "Chalet" || len(got.Couples) != 2 {
		t.Errorf("%+v", got)
	}
}

func TestAPollInterruptedByShutdownLeavesTheSnapshotAlone(t *testing.T) {
	b := &fakeBox{routes: healthyRoutes()}
	f := pollFleet(t, b)
	f.storeBackups(f.pollBox(context.Background(), testBox))
	before := f.backups["box1"]

	inner := f.call
	f.call = func(ctx context.Context, boxID, method, path string) (Response, error) {
		if err := ctx.Err(); err != nil {
			return Response{}, err
		}
		return inner(ctx, boxID, method, path)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := f.pollBox(ctx, testBox)
	f.storeBackups(got)

	if got.Error != "interrupted" {
		t.Errorf("Error = %q", got.Error)
	}
	if after := f.backups["box1"]; !after.Online || after.Error != "" || !after.Green || len(after.Couples) != len(before.Couples) {
		t.Errorf("the snapshot changed: %+v", after)
	}
}

// A verifications route that fails for a while says nothing of the restore
// tests: unknown, not "never tested". Only a 404 (an AppManagement without
// them) is a box that has none to tell.
func TestAVerificationsRouteThatFailsLeavesTheStateUnknown(t *testing.T) {
	r := healthyRoutes()
	r[am+"/backup/verifications"] = Response{Status: 500, Body: []byte(`{"message":"boom"}`)}
	got := pollFleet(t, &fakeBox{routes: r}).pollBox(context.Background(), testBox)
	for _, c := range got.Couples {
		if c.Verify.State != "unknown" {
			t.Fatalf("%+v", got.Couples)
		}
	}
	delete(r, am+"/backup/verifications") // 404
	got = pollFleet(t, &fakeBox{routes: r}).pollBox(context.Background(), testBox)
	for _, c := range got.Couples {
		if c.Verify.State != "none" {
			t.Fatalf("%+v", got.Couples)
		}
	}
}
