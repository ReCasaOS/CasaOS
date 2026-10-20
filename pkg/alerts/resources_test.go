package alerts

import (
	"strings"
	"testing"
)

const (
	firing   = "resource:alert-firing"
	resolved = "resource:alert-resolved"
)

func resourceProps(kind, item, name, situation string, more ...string) map[string]string {
	p := map[string]string{"item": item, "name": name, "kind": kind, "situation": situation, "source": "app_management"}
	for i := 0; i+1 < len(more); i += 2 {
		p[more[i]] = more[i+1]
	}
	return p
}

func TestEachResourceEventsAlert(t *testing.T) {
	const key = "resource:app-container:app:immich:server:1:cpu_high"
	immich := resourceProps("app-container", "app:immich:server:1", "Immich / server", "cpu_high", "value", "93", "minutes", "10")
	for name, tc := range map[string]struct {
		event      string
		properties map[string]string
		want       alert
		resolved   bool
	}{
		"cpu firing": {firing, immich, alert{key, Resources, "Immich / server: CPU at 93 % for 10 min."}, false},
		"mem firing, no minutes": {
			firing, resourceProps("host", "host", "nas-salon", "mem_high", "value", "91"),
			alert{"resource:host:host:mem_high", Resources, "nas-salon: memory at 91 %."}, false,
		},
		"cpu without a value": {
			firing, resourceProps("host", "host", "nas-salon", "cpu_high", "value", "lots", "minutes", "5"),
			alert{"resource:host:host:cpu_high", Resources, "nas-salon: CPU high for 5 min."}, false,
		},
		"minutes out of range": {
			firing, resourceProps("host", "host", "nas-salon", "cpu_high", "value", "90", "minutes", "500"),
			alert{"resource:host:host:cpu_high", Resources, "nas-salon: CPU at 90 %."}, false,
		},
		"a value that is not plain": {
			firing, resourceProps("host", "host", "nas-salon", "cpu_high", "value", "NaN"),
			alert{"resource:host:host:cpu_high", Resources, "nas-salon: CPU high."}, false,
		},
		"vm crashed": {
			firing, resourceProps("vm", "vm:abc", "web-vm", "vm_crashed"),
			alert{"resource:vm:vm:abc:vm_crashed", Resources, "web-vm crashed."}, false,
		},
		"no name": {
			firing, resourceProps("vm", "vm:abc", "", "vm_crashed"),
			alert{"resource:vm:vm:abc:vm_crashed", Resources, "A resource crashed."}, false,
		},
		"cpu resolved": {resolved, immich, alert{key, Resources, "Resolved: Immich / server: CPU is back to normal."}, true},
		"mem resolved": {
			resolved, resourceProps("host", "host", "nas-salon", "mem_high"),
			alert{"resource:host:host:mem_high", Resources, "Resolved: nas-salon: memory is back to normal."}, true,
		},
		"vm resolved": {
			resolved, resourceProps("vm", "vm:abc", "web-vm", "vm_crashed"),
			alert{"resource:vm:vm:abc:vm_crashed", Resources, "Resolved: web-vm runs again."}, true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, isResolved, ok := fromResource(tc.event, tc.properties)
			if !ok || got != tc.want || isResolved != tc.resolved {
				t.Fatalf("fromResource() = %+v, resolved %v, ok %v; want %+v, resolved %v", got, isResolved, ok, tc.want, tc.resolved)
			}
		})
	}
}

func TestAResourceEventThatIsNotOneIsIgnored(t *testing.T) {
	for name, tc := range map[string]struct {
		event      string
		properties map[string]string
	}{
		"missing item":        {firing, resourceProps("host", "", "nas", "cpu_high")},
		"missing kind":        {firing, resourceProps("", "host", "nas", "cpu_high")},
		"missing situation":   {firing, resourceProps("host", "host", "nas", "")},
		"unknown kind":        {firing, resourceProps("pod", "host", "nas", "cpu_high")},
		"unknown situation":   {firing, resourceProps("host", "host", "nas", "disk_full")},
		"unknown event":       {"resource:alert-maybe", resourceProps("host", "host", "nas", "cpu_high")},
		"resolved, no item":   {resolved, resourceProps("host", "", "nas", "cpu_high")},
		"no properties":       {firing, nil},
		"another source name": {"app:install-error", resourceProps("host", "host", "nas", "cpu_high")},
	} {
		t.Run(name, func(t *testing.T) {
			if a, _, ok := fromResource(tc.event, tc.properties); ok {
				t.Fatalf("fromResource() = %+v, want no alert", a)
			}
		})
	}
}

func TestAHostileNameIsMadeFitForANotification(t *testing.T) {
	multiline, _, ok := fromResource(firing, resourceProps("host", "host", "a\nSECRET=hunter2", "vm_crashed"))
	if !ok || multiline.sentence != "a crashed." {
		t.Fatalf("sentence = %q, want the first line of the name only", multiline.sentence)
	}
	long, _, ok := fromResource(firing, resourceProps("host", "host", strings.Repeat("x", 10000), "cpu_high", "value", "90"))
	if !ok || len(long.sentence) >= 200 {
		t.Fatalf("sentence of %d bytes, want under 200", len(long.sentence))
	}
	// the key is the item, never the name
	if strings.Contains(long.key, "xxx") {
		t.Fatalf("key = %q holds the name", long.key)
	}
}

func TestTheSameResourceKeyIsSentOnceInSixHours(t *testing.T) {
	h, o, _ := newHub(t)
	p := resourceProps("host", "host", "nas", "cpu_high", "value", "95")

	h.Handle(firing, p)
	h.Handle(firing, p)
	h.sending.Wait()

	if got := o.texts(phone); len(got) != 1 {
		t.Fatalf("sent %q, want one message", got)
	}
	if r := h.sent["resource:host:host:cpu_high"]; r == nil || r.repeats != 1 {
		t.Fatalf("record = %+v, want one repeat", r)
	}
}

func TestAResolutionIsSentOnlyAfterItsFiring(t *testing.T) {
	h, o, _ := newHub(t)
	p := resourceProps("host", "host", "nas", "cpu_high", "value", "95")

	h.Handle(resolved, p)
	h.sending.Wait()
	if got := o.texts(phone); len(got) != 0 {
		t.Fatalf("a resolution with no firing sent %q", got)
	}
	h.Handle(firing, p)
	h.Handle(resolved, p)
	h.Handle(resolved, p)
	h.sending.Wait()
	if got := o.texts(phone); len(got) != 2 {
		t.Fatalf("sent %q, want the firing and one resolution", got)
	}
}

func TestResourcesOffSendsNothingNotEvenTheResolution(t *testing.T) {
	h, o, _ := newHub(t)
	c := h.load()
	c.Categories[Resources] = false
	saveConfig(t, h, c)
	p := resourceProps("host", "host", "nas", "cpu_high", "value", "95")

	h.Handle(firing, p)
	h.Handle(resolved, p)
	h.sending.Wait()
	c.Categories[Resources] = true
	saveConfig(t, h, c)
	h.Handle(resolved, p)
	h.sending.Wait()

	if got := o.texts(phone); len(got) != 0 {
		t.Fatalf("sent %q, want nothing", got)
	}
	if len(h.sent) != 0 {
		t.Fatalf("sent = %v, want nothing recorded", h.sent)
	}
}

func TestCPUAndMemoryAlertsAreDroppedWhileMutedButAVMCrashIsNot(t *testing.T) {
	h, o, _ := newHub(t)
	h.Muted = func() bool { return true }

	h.Handle(firing, resourceProps("host", "host", "nas", "cpu_high", "value", "95"))
	h.Handle(firing, resourceProps("host", "host", "nas", "mem_high", "value", "95"))
	h.sending.Wait()
	if got := o.texts(phone); len(got) != 0 || len(h.sent) != 0 {
		t.Fatalf("muted: sent %q, recorded %v, want neither", got, h.sent)
	}
	h.Handle(firing, resourceProps("vm", "vm:abc", "web-vm", "vm_crashed"))
	h.sending.Wait()
	if got := o.texts(phone); len(got) != 1 {
		t.Fatalf("sent %q, want the crash", got)
	}
}
