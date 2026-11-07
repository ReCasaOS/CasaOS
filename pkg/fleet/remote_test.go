package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
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
	if err := f.EnableRemote(context.Background(), r.URL); !errors.Is(err, errRemoteLoopback) || r.count(&r.creates) != 0 {
		t.Fatalf("%v, creates %d", err, r.count(&r.creates))
	}
}

func TestEnableRemoteWithoutHTTPS(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	f.Gateway = func() (GatewayInfo, error) { return GatewayInfo{Public: mustURL(t, "http://127.0.0.1:80")}, nil }
	if err := f.EnableRemote(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	if rs, ok := f.loadRelay(); !ok || !remoteAlone(rs, false) {
		t.Fatalf("%+v %v", rs, ok)
	}
}

func TestEnableRemoteRefusesRelayWithoutDevices(t *testing.T) {
	f, n, r, _ := relayMaster(t)
	r.set(func(r *fakeRelay) { r.noDevices = true })
	if err := f.EnableRemote(context.Background(), r.URL); !errors.Is(err, ErrRelayNoDevices) {
		t.Fatal(err)
	}
	if r.count(&r.deletes) != 1 || n.called("up ") || exists(t, f.relayPath()) {
		t.Fatalf("deletes %d, calls %v", r.count(&r.deletes), n.calls)
	}
}

func TestEnableMasterRelayDoesNotAskForDevices(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	r.set(func(r *fakeRelay) { r.noDevices = true })
	if err := f.EnableMasterRelay(context.Background(), r.URL); err != nil || r.count(&r.listings) != 0 {
		t.Fatalf("%v, listings %d", err, r.count(&r.listings))
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

func TestDisableRemoteKeepsRelayJSONWhenDownFails(t *testing.T) {
	f, n, r, _ := relayMaster(t)
	if err := f.EnableRemote(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("tailscale logout: boom")
	n.mu.Lock()
	n.downErr = boom
	n.mu.Unlock()
	err := f.DisableRemote(context.Background())
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "cannot leave the remote access network") {
		t.Fatal(err)
	}
	if rs, ok := f.loadRelay(); !ok || !rs.Remote || r.count(&r.deletes) != 1 {
		t.Fatalf("%+v %v, deletes %d", rs, ok, r.count(&r.deletes))
	}
}

func TestDisableRemoteLeavesAMembersNetwork(t *testing.T) {
	f, n, r, _ := relayMaster(t)
	if err := f.EnableRemote(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	if err := f.saveMember(MemberState{MasterURL: "https://other.example", BoxID: "b", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := f.DisableRemote(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.count(&r.deletes) != 1 || n.called("down") || exists(t, f.relayPath()) {
		t.Fatalf("deletes %d, calls %v", r.count(&r.deletes), n.calls)
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

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestEnableMasterRelayReusesRemoteNetwork(t *testing.T) {
	f, n, r, _ := relayMaster(t)
	n.after.LoginServer = r.URL // the network the relay announced
	if err := f.EnableRemote(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	if err := f.EnableMasterRelay(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	ups := 0
	for _, c := range n.calls {
		if strings.HasPrefix(c, "up ") {
			ups++
		}
	}
	if r.count(&r.creates) != 1 || ups != 1 || r.count(&r.deletes) != 0 || n.called("down") {
		t.Fatalf("creates %d, deletes %d, calls %v", r.count(&r.creates), r.count(&r.deletes), n.calls)
	}
	m := readJSONFile(t, f.masterPath())
	if m["relay"] != r.URL || m["fleet_id"] != "fl1" || m["net_addr"] != "100.64.0.7" {
		t.Fatalf("%v", m)
	}
	if rs, ok := f.loadRelay(); !ok || !rs.Remote {
		t.Fatalf("%+v %v", rs, ok)
	}
}

func TestEnableMasterRelayRemoteOtherRelay(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	if err := f.EnableRemote(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	err := f.EnableMasterRelay(context.Background(), "https://other.example")
	if err == nil || !strings.Contains(err.Error(), "remote access runs over another relay") || exists(t, f.masterPath()) || r.count(&r.creates) != 1 {
		t.Fatalf("%v", err)
	}
}

func TestEnableMasterRelayRemoteNetworkDown(t *testing.T) {
	f, n, r, _ := relayMaster(t)
	if err := f.EnableRemote(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	n.up, n.before = false, NetState{}
	n.mu.Unlock()
	if err := f.EnableMasterRelay(context.Background(), r.URL); !errors.Is(err, errRelayNotUp) || exists(t, f.masterPath()) {
		t.Fatalf("%v", err)
	}
	if _, ok := f.loadRelay(); !ok || r.count(&r.creates) != 1 {
		t.Fatal("relay.json lost or a new fleet")
	}
}

func TestEnableMasterAdvancedRefusedWhileRemoteOn(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	if err := f.EnableRemote(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	err := f.EnableMaster("https://box.example")
	if err == nil || !strings.Contains(err.Error(), "this box has remote access over a relay") || exists(t, f.masterPath()) {
		t.Fatalf("%v", err)
	}
}

func TestJoinRefusesWhileRemoteOn(t *testing.T) {
	f, n, r, _ := relayMaster(t)
	if err := f.EnableRemote(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	before := len(n.calls)
	for _, c := range []Code{
		{MasterURL: "https://master.example", BoxID: "b", Key: "k"},
		{MasterURL: "https://master.example", BoxID: "b", Key: "k", Relay: r.URL, JoinKey: testJoinKey, Fleet: "fl9"},
	} {
		if err := f.Join(c.String()); !errors.Is(err, ErrRemoteOn) {
			t.Fatalf("%v", err)
		}
	}
	if rs, ok := f.loadRelay(); !ok || !rs.Remote || exists(t, f.memberPath()) || exists(t, f.netOwnPath()) || len(n.calls) != before || len(n.downs) != 0 {
		t.Fatalf("changed: %+v, calls %v", rs, n.calls)
	}
}

func TestDisableMasterAfterRemoteDeletesFleet(t *testing.T) {
	f, n, r, _ := relayMaster(t)
	n.after.LoginServer = r.URL // the network the relay announced
	if err := f.EnableRemote(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	if err := f.EnableMasterRelay(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	if err := f.DisableMaster(); err != nil {
		t.Fatal(err)
	}
	if r.count(&r.deletes) != 1 || exists(t, f.relayPath()) {
		t.Fatalf("deletes %d", r.count(&r.deletes))
	}
}

func TestDisableMasterOffKeepsRemote(t *testing.T) {
	f, n, r, _ := relayMaster(t)
	if err := f.EnableRemote(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	if err := f.DisableMaster(); err != nil {
		t.Fatal(err)
	}
	rs, ok := f.loadRelay()
	if r.count(&r.deletes) != 0 || n.called("down") || !ok || !rs.Remote || rs.NetAddr == "" {
		t.Fatalf("deletes %d, calls %v, relay.json %+v %v", r.count(&r.deletes), n.calls, rs, ok)
	}
}

func TestDisableRemoteCleansUnfinishedEnable(t *testing.T) {
	f, n, r, _ := relayMaster(t)
	if err := f.EnableRemote(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	rs, _ := f.loadRelay()
	rs.NetAddr = "" // as an enable that did not finish leaves it
	if err := f.saveRelay(rs); err != nil {
		t.Fatal(err)
	}
	n.calls = nil
	if err := f.DisableRemote(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.count(&r.deletes) != 1 || !n.called("down") || exists(t, f.relayPath()) {
		t.Fatalf("deletes %d, calls %v", r.count(&r.deletes), n.calls)
	}
}

func remoteAloneBox(t *testing.T) (*Fleet, *fakeRelay) {
	t.Helper()
	f, _, r, _ := relayMaster(t)
	if err := f.EnableRemote(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	return f, r
}

func TestNewDeviceCodeAlone(t *testing.T) {
	f, r := remoteAloneBox(t)
	c, err := f.NewDeviceCode(context.Background())
	if err != nil || c.Code != "ABCD-2345" || r.count(&r.codes) != 1 {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestNewDeviceCodeMaster(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	if err := f.EnableMasterRelay(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	if c, err := f.NewDeviceCode(context.Background()); err != nil || c.Code != "ABCD-2345" { // the fake needs relay.json's Bearer secret
		t.Fatalf("%+v %v", c, err)
	}
}

func TestNewDeviceCodeRemoteOff(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	if _, err := f.NewDeviceCode(context.Background()); !errors.Is(err, ErrRemoteOff) || r.count(&r.codes) != 0 {
		t.Fatal(err)
	}
	if err := f.EnableMaster("https://box.example"); err != nil { // advanced master: no relay
		t.Fatal(err)
	}
	if _, err := f.NewDeviceCode(context.Background()); !errors.Is(err, ErrRemoteOff) {
		t.Fatal(err)
	}
}

func TestNewDeviceCodeMemberRefused(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	if err := f.saveMember(MemberState{MasterURL: "https://other.example", BoxID: "b", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.NewDeviceCode(context.Background()); !errors.Is(err, errRemoteMember) || r.count(&r.codes) != 0 {
		t.Fatal(err)
	}
}

func TestNewDeviceCodeGoneAlone(t *testing.T) {
	f, r := remoteAloneBox(t)
	r.set(func(r *fakeRelay) { r.gone = true })
	_, err := f.NewDeviceCode(context.Background())
	if !errors.Is(err, ErrRemoteGone) || !errors.Is(err, ErrRelayFleetGone) {
		t.Fatal(err)
	}
}

func TestNewDeviceCodeGoneMaster(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	if err := f.EnableMasterRelay(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	r.set(func(r *fakeRelay) { r.gone = true })
	if _, err := f.NewDeviceCode(context.Background()); !errors.Is(err, ErrFleetGone) {
		t.Fatal(err)
	}
}

func TestDevicesAndRemove(t *testing.T) {
	f, r := remoteAloneBox(t)
	ds, err := f.Devices(context.Background())
	if err != nil || len(ds) != 1 || ds[0].ID != "5" || ds[0].IP != "100.64.0.20" {
		t.Fatalf("%+v %v", ds, err)
	}
	if err := f.RemoveDevice(context.Background(), "5"); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.devRemoved) != 1 || r.devRemoved[0] != "5" {
		t.Fatalf("%v", r.devRemoved)
	}
}

func TestRemoteViewMasterMapsBoxes(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	ctx := context.Background()
	if err := f.EnableMasterRelay(ctx, r.URL); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.AddBox("this box", "", true); err != nil {
		t.Fatal(err)
	}
	st, _ := f.snapshot()
	box := Box{ID: "abc", Name: "garage"}
	other := Box{ID: "def", Name: "attic"}
	st.Boxes = append(st.Boxes, box, other)
	if err := f.saveMaster(st); err != nil {
		t.Fatal(err)
	}
	r.set(func(r *fakeRelay) {
		r.nodes = []map[string]any{{"hostname": "b-abc", "ip": "100.64.0.8", "online": true}}
	})
	v, err := f.RemoteView(ctx)
	if err != nil || v.Relay != r.URL || len(v.Devices) != 1 || len(v.Boxes) != 3 {
		t.Fatalf("%+v %v", v, err)
	}
	got := map[string]RemoteBox{}
	for _, b := range v.Boxes {
		got[b.Name] = b
	}
	if b := got["this box"]; b.Addr != "100.64.0.7" || !b.Online {
		t.Fatalf("self %+v", b)
	}
	if b := got["garage"]; b.Addr != "100.64.0.8" || !b.Online || b.ID != "abc" {
		t.Fatalf("garage %+v", b)
	}
	if b := got["attic"]; b.Addr != "" || b.Online {
		t.Fatalf("attic %+v", b)
	}
}

func TestRemoteViewAlone(t *testing.T) {
	f, _ := remoteAloneBox(t)
	v, err := f.RemoteView(context.Background())
	if err != nil || v.Addr != "100.64.0.7" || len(v.Devices) != 1 || v.Boxes != nil {
		t.Fatalf("%+v %v", v, err)
	}
}

func TestStatusRemoteAlone(t *testing.T) {
	f, n, r := func() (*Fleet, *fakeNet, *fakeRelay) {
		f, n, r, _ := relayMaster(t)
		if err := f.EnableRemote(context.Background(), r.URL); err != nil {
			t.Fatal(err)
		}
		return f, n, r
	}()
	n.mu.Lock()
	n.after = NetState{Connected: true, Addr: "100.64.0.7", LoginServer: r.URL}
	n.mu.Unlock()
	asked := r.count(&r.listings) + r.count(&r.codes) // EnableRemote's own check of the device routes
	s := f.Status()
	if s.Remote == nil || !s.Remote.Alone || s.Remote.Addr != "100.64.0.7" || s.Remote.Relay != r.URL || !s.Remote.Connected || s.Remote.Error != "" {
		t.Fatalf("%+v", s.Remote)
	}
	if r.count(&r.listings)+r.count(&r.codes) != asked {
		t.Fatal("Status asked the relay")
	}
}

func TestStatusRemoteAloneOffNetwork(t *testing.T) {
	f, n, _ := func() (*Fleet, *fakeNet, *fakeRelay) {
		f, n, r, _ := relayMaster(t)
		if err := f.EnableRemote(context.Background(), r.URL); err != nil {
			t.Fatal(err)
		}
		return f, n, r
	}()
	n.mu.Lock()
	n.up, n.before = false, NetState{}
	n.mu.Unlock()
	s := f.Status()
	if s.Remote == nil || s.Remote.Connected || s.Remote.Error != "this box is not on its remote access network right now; after a restart, wait a minute. If this lasts, turn remote access off and on again (your devices will have to be added again)" {
		t.Fatalf("%+v", s.Remote)
	}
}

func TestStatusRemoteMaster(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	if err := f.EnableMasterRelay(context.Background(), r.URL); err != nil {
		t.Fatal(err)
	}
	s := f.Status()
	if s.Remote == nil || s.Remote.Alone || s.Remote.Relay != r.URL || s.Remote.Addr != "100.64.0.7" {
		t.Fatalf("%+v", s.Remote)
	}
}

func TestStatusNoRemote(t *testing.T) {
	f, _, _, _ := relayMaster(t)
	if s := f.Status(); s.Remote != nil {
		t.Fatalf("%+v", s.Remote)
	}
	if err := f.EnableMaster("https://box.example"); err != nil {
		t.Fatal(err)
	}
	if s := f.Status(); s.Remote != nil {
		t.Fatalf("advanced master: %+v", s.Remote)
	}
}

func TestRemoteViewBoxURLs(t *testing.T) {
	f, _, r, _ := relayMaster(t)
	ctx := context.Background()
	if err := f.EnableMasterRelay(ctx, r.URL); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.AddBox("this box", "", true); err != nil {
		t.Fatal(err)
	}
	f.Gateway = func() (GatewayInfo, error) { return GatewayInfo{Public: mustURL(t, "https://127.0.0.1:8443")}, nil }
	st, _ := f.snapshot()
	st.Boxes = append(st.Boxes,
		Box{ID: "a", Name: "a", WebScheme: "http", WebPort: "8080"},
		Box{ID: "b", Name: "b", WebScheme: "http", WebPort: "80"},
		Box{ID: "c", Name: "c", WebScheme: "https", WebPort: "443"},
		Box{ID: "d", Name: "d"},
		Box{ID: "h", Name: "h", WebScheme: "http", WebPort: "00080"},
		Box{ID: "e", Name: "e", WebScheme: "http", WebPort: "8080"},
		Box{ID: "g", Name: "g", WebScheme: "http", WebPort: "8080"}, // offline: no address
	)
	if err := f.saveMaster(st); err != nil {
		t.Fatal(err)
	}
	r.set(func(r *fakeRelay) {
		r.nodes = []map[string]any{
			{"hostname": "b-a", "ip": "100.64.0.8", "online": true},
			{"hostname": "b-b", "ip": "100.64.0.8", "online": true},
			{"hostname": "b-c", "ip": "100.64.0.8", "online": true},
			{"hostname": "b-d", "ip": "100.64.0.8", "online": true},
			{"hostname": "b-h", "ip": "100.64.0.9", "online": true},
			{"hostname": "b-e", "ip": "fd7a::8", "online": true},
		}
	})
	v, err := f.RemoteView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, b := range v.Boxes {
		got[b.Name] = b.URL
	}
	want := map[string]string{
		"this box": "https://100.64.0.7:8443",
		"a":        "http://100.64.0.8:8080",
		"b":        "http://100.64.0.8",
		"c":        "https://100.64.0.8",
		"d":        "",
		"h":        "http://100.64.0.9",
		"e":        "http://[fd7a::8]:8080",
		"g":        "",
	}
	for n, u := range want {
		if got[n] != u {
			t.Errorf("%s: url %q, want %q", n, got[n], u)
		}
	}
}
