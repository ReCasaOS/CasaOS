package fleet

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestEnableRemoteJoinsAlone(t *testing.T) {
	f, n, r, _ := relayMaster(t)
	if err := f.EnableRemote(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	rs, ok := f.loadRelay()
	if !ok || !rs.Remote || rs.FleetID != "fl1" || rs.NetAddr != "100.64.0.7" || rs.Relay != r.URL {
		t.Fatalf("%+v %v", rs, ok)
	}
	if exists(t, f.masterPath()) {
		t.Fatal("master.json written")
	}
	if !n.called("up " + r.URL + " " + testJoinKey + " maison") {
		t.Fatalf("calls: %v", n.calls)
	}
	if role := f.Status().Role; role != "none" {
		t.Fatalf("role %q", role)
	}
}

func TestEnableRemoteIdempotent(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	for i := 0; i < 2; i++ {
		if err := f.EnableRemote(context.Background(), r.URL+"/"); err != nil {
			t.Fatal(err)
		}
	}
	if c := r.count(&r.creates); c != 1 {
		t.Fatalf("creates %d", c)
	}
}

func TestEnableRemoteConcurrentCreatesOneFleet(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = f.EnableRemote(context.Background(), r.URL) }()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || r.count(&r.creates) != 1 {
		t.Fatalf("%v, creates %d", errs, r.count(&r.creates))
	}
}

func TestEnableRemoteRefusesMember(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	if err := f.saveMember(MemberState{MasterURL: "https://other.example", BoxID: "b", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := f.EnableRemote(context.Background(), r.URL); !errors.Is(err, errRemoteMember) || r.count(&r.creates) != 0 {
		t.Fatalf("%v, creates %d", err, r.count(&r.creates))
	}
}

func TestEnableRemoteOnRelayMasterIsNoop(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	if err := f.EnableMasterRelay(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	if err := f.EnableRemote(context.Background(), r.URL); err != nil || r.count(&r.creates) != 1 {
		t.Fatalf("%v, creates %d", err, r.count(&r.creates))
	}
}

func TestEnableRemoteRefusesAdvancedMaster(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	if err := f.EnableMaster("https://box.example"); err != nil {
		t.Fatal(err)
	}
	if err := f.EnableRemote(context.Background(), r.URL); !errors.Is(err, errRemoteAdvanced) || r.count(&r.creates) != 0 {
		t.Fatalf("%v, creates %d", err, r.count(&r.creates))
	}
}

func TestEnableRemoteOtherRelay(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	if err := f.EnableRemote(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	if err := f.EnableRemote(context.Background(), "https://other.example"); !errors.Is(err, errRemoteOtherRelay) {
		t.Fatal(err)
	}
}

func TestEnableRemoteRefusesLoopbackGateway(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	f.Gateway = func() (GatewayInfo, error) { return GatewayInfo{LoopbackOnly: true}, nil }
	if err := f.EnableRemote(context.Background(), r.URL); !errors.Is(err, errLoopbackGateway) || r.count(&r.creates) != 0 {
		t.Fatalf("%v, creates %d", err, r.count(&r.creates))
	}
}

func TestEnableRemoteRollsBackWhenUpFails(t *testing.T) {
	f, n, r, _ := relayMaster(t)
	n.upErr = errors.New("tailscale up: boom")
	if err := f.EnableRemote(context.Background(), r.URL); err == nil {
		t.Fatal("no error")
	}
	if r.count(&r.deletes) != 1 || exists(t, f.relayPath()) || n.called("down") {
		t.Fatalf("deletes %d, calls %v", r.count(&r.deletes), n.calls)
	}
}

func TestEnableRemoteRollsBackWhenNoAddress(t *testing.T) {
	f, n, r, _ := relayMaster(t)
	n.after = NetState{Connected: true} // up, but never an address
	oldWait, oldStep := addrWait, addrStep
	t.Cleanup(func() { addrWait, addrStep = oldWait, oldStep })
	addrWait, addrStep = 20*time.Millisecond, 5*time.Millisecond
	if err := f.EnableRemote(context.Background(), r.URL); err == nil {
		t.Fatal("no error")
	}
	if r.count(&r.deletes) != 1 || !n.called("down") || exists(t, f.relayPath()) {
		t.Fatalf("deletes %d, calls %v", r.count(&r.deletes), n.calls)
	}
}

func TestEnableRemoteFinishesInterruptedEnable(t *testing.T) {
	f, n, r, _ := relayMaster(t)
	if err := f.saveRelay(RelayState{Relay: r.URL, FleetID: "fl1", Secret: testSecret, Remote: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.EnableRemote(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	rs, _ := f.loadRelay()
	if r.count(&r.deletes) != 1 || r.count(&r.creates) != 1 || rs.NetAddr != "100.64.0.7" || !n.called("up ") {
		t.Fatalf("%+v deletes %d creates %d", rs, r.count(&r.deletes), r.count(&r.creates))
	}
}

func TestEnableRemoteSelfEnrolledMaster(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	if err := f.EnableMasterRelay(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.AddBox("this box", "", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.loadMember(); !ok {
		t.Fatal("self-enrolment left no member.json: the test proves nothing")
	}
	if err := f.EnableRemote(context.Background(), r.URL); err != nil || r.count(&r.creates) != 1 {
		t.Fatalf("%v, creates %d", err, r.count(&r.creates))
	}
}

func TestEnableRemoteSelfEnrolledAdvancedMaster(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	if err := f.EnableMaster("https://box.example"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.AddBox("this box", "", true); err != nil {
		t.Fatal(err)
	}
	if err := f.EnableRemote(context.Background(), r.URL); !errors.Is(err, errRemoteAdvanced) {
		t.Fatal(err)
	}
}

func TestEnableRemoteRefusesAnotherNetwork(t *testing.T) {
	f, n, r, _ := relayMaster(t)
	n.before = NetState{LoginServer: "https://controlplane.tailscale.com"}
	if err := f.EnableRemote(context.Background(), r.URL); !errors.Is(err, ErrOtherNetwork) || r.count(&r.creates) != 0 {
		t.Fatalf("%v, creates %d", err, r.count(&r.creates))
	}
}

func TestDisableRemote(t *testing.T) {
	f, n, r, _ := relayMaster(t)
	if err := f.EnableRemote(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	if err := f.DisableRemote(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.count(&r.deletes) != 1 || len(n.downs) != 1 || n.downs[0] != r.URL || exists(t, f.relayPath()) {
		t.Fatalf("deletes %d, downs %v", r.count(&r.deletes), n.downs)
	}
}

func TestDisableRemoteRefusesMaster(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	if err := f.EnableMasterRelay(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	if err := f.DisableRemote(context.Background()); !errors.Is(err, errRemoteIsMaster) || r.count(&r.deletes) != 0 {
		t.Fatalf("%v, deletes %d", err, r.count(&r.deletes))
	}
}
