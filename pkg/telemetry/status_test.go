package telemetry

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"
)

// unused is an endpoint these tests never send to.
const unused = "http://127.0.0.1:9/unused"

func TestStatusIsTheStateAndTheHeartbeatAsItWouldBeSent(t *testing.T) {
	tel, _ := fixture(t, unused)
	status := tel.Status()
	if !status.Enabled || status.NoticeSeen {
		t.Fatalf("Status() = %+v, want enabled and notice not seen with no telemetry.json", status)
	}
	if status.Preview.Event != "heartbeat" || !reflect.DeepEqual(status.Preview.Properties, tel.Properties()) {
		t.Fatalf("preview = %+v, want the heartbeat's properties", status.Preview)
	}
}

func TestThePreviewIsBuiltWhenStatisticsAreOff(t *testing.T) {
	tel, _ := fixture(t, unused)
	saveState(t, tel, State{Enabled: false})
	status := tel.Status()
	if status.Enabled || status.Preview.Properties["distribution"] != "v0.5.0" {
		t.Fatalf("Status() = %+v, want disabled with a preview of v0.5.0", status)
	}
}

func TestThePreviewIsWhatIsSent(t *testing.T) {
	c, endpoint := newCapture(t, http.StatusOK)
	tel, _ := fixture(t, endpoint)

	tel.Check(context.Background()) // no telemetry.json: a heartbeat is due

	requests := c.all()
	if len(requests) != 1 {
		t.Fatalf("%d requests, want one heartbeat", len(requests))
	}
	if want := asJSON(t, tel.Status().Preview.Properties); !reflect.DeepEqual(requests[0].body["properties"], want) {
		t.Fatalf("properties sent =\n%v\npreview\n%v", requests[0].body["properties"], want)
	}
}

func TestUpdateChangesEnabledAndNoticeSeenAndNothingElse(t *testing.T) {
	tel, _ := fixture(t, unused)
	lastSent := testNow.Add(-time.Hour)
	saveState(t, tel, State{Enabled: true, ID: "kept-id", LastSent: lastSent})
	off, seen := false, true

	status, err := tel.Update(&off, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.NoticeSeen || status.Preview.Event != "heartbeat" {
		t.Fatalf("Update(enabled=false) = %+v", status)
	}
	if s := tel.load(); s.Enabled || s.NoticeSeen || s.ID != "kept-id" || !s.LastSent.Equal(lastSent) {
		t.Fatalf("state after Update(enabled=false) = %+v", s)
	}

	status, err = tel.Update(nil, &seen)
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || !status.NoticeSeen {
		t.Fatalf("Update(notice_seen=true) = %+v", status)
	}
	if s := tel.load(); s.Enabled || !s.NoticeSeen || s.ID != "kept-id" || !s.LastSent.Equal(lastSent) {
		t.Fatalf("state after Update(notice_seen=true) = %+v", s)
	}

	if _, err := tel.Update(nil, nil); err != nil {
		t.Fatal(err)
	}
	if s := tel.load(); s.Enabled || !s.NoticeSeen || s.ID != "kept-id" || !s.LastSent.Equal(lastSent) {
		t.Fatalf("state after an empty Update = %+v", s)
	}
}

func TestUpdateRewritesAMalformedState(t *testing.T) {
	tel, root := fixture(t, unused)
	write(t, root, StateFile, "{")
	seen := true

	if _, err := tel.Update(nil, &seen); err != nil {
		t.Fatal(err)
	}

	onDisk := stateOnDisk(t, root)
	if onDisk["enabled"] != false || onDisk["notice_seen"] != true {
		t.Fatalf("telemetry.json = %v, want disabled (it was malformed) and notice seen", onDisk)
	}
}
