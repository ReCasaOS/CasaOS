package hoststats

import (
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func reading(at time.Time, mut func(*Reading)) Reading {
	r := Reading{
		At:       at,
		Cores:    4,
		MemTotal: 1000, MemUsed: 500, MemCached: 100, SwapUsed: 10,
		Nets:    map[string]NetCounters{"eth0": {}},
		Disk:    &DiskCounters{},
		Volumes: []Volume{{"/DATA", 1000, 250}},
	}
	if mut != nil {
		mut(&r)
	}
	return r
}

func has(s stats.Sample, k string) bool { _, ok := s[k]; return ok }

func TestCPUIsPercentOfOneCoreSummedOverCores(t *testing.T) {
	var c Collector
	c.Sample(reading(t0, func(r *Reading) { r.CPU = CPUTimes{100, 400} }))
	s := c.Sample(reading(t0.Add(5*time.Second), func(r *Reading) { r.CPU = CPUTimes{101, 404} }))
	if s["cpu/total"] != 100 || s["cpu/vcpus"] != 4 || s["alert/cpu_high"] != 25 {
		t.Fatalf("got %v", s)
	}
}

func TestTheFirstReadingHasOnlyWhatNeedsNoPreviousOne(t *testing.T) {
	var c Collector
	s := c.Sample(reading(t0, nil))
	for _, k := range []string{"memory/rss", "memory/cache", "memory/swap", "memory/limit", "alert/mem_high", "cpu/vcpus", "fs/free_percent:/DATA"} {
		if !has(s, k) {
			t.Errorf("missing %s", k)
		}
	}
	for _, k := range []string{"cpu/total", "alert/cpu_high", "network/rx:eth0", "network/tx:eth0", "network/dropped:eth0", "disk/read", "disk/write", "disk/iops_read", "disk/iops_write"} {
		if has(s, k) {
			t.Errorf("unexpected %s", k)
		}
	}
	if s["alert/mem_high"] != 50 || s["memory/limit"] != 1000 {
		t.Fatalf("got %v", s)
	}
}

func TestNoElapsedCPUTimeIsAbsentNotZero(t *testing.T) {
	var c Collector
	c.Sample(reading(t0, func(r *Reading) { r.CPU = CPUTimes{100, 400} }))
	s := c.Sample(reading(t0.Add(5*time.Second), func(r *Reading) { r.CPU = CPUTimes{100, 400} }))
	if has(s, "cpu/total") || has(s, "alert/cpu_high") {
		t.Fatalf("got %v", s)
	}
}

func TestRatesArePerSecond(t *testing.T) {
	var c Collector
	c.Sample(reading(t0, nil))
	s := c.Sample(reading(t0.Add(5*time.Second), func(r *Reading) {
		r.Nets = map[string]NetCounters{"eth0": {RxBytes: 5000, TxBytes: 2500, Dropped: 10}}
		r.Disk = &DiskCounters{ReadBytes: 50000, WriteBytes: 25000, ReadOps: 100, WriteOps: 50}
	}))
	want := map[string]float64{
		"network/rx:eth0": 1000, "network/tx:eth0": 500, "network/dropped:eth0": 2,
		"disk/read": 10000, "disk/write": 5000, "disk/iops_read": 20, "disk/iops_write": 10,
	}
	for k, v := range want {
		if s[k] != v || !has(s, k) {
			t.Errorf("%s = %v, want %v", k, s[k], v)
		}
	}
}

func TestACounterThatGoesBackwardsDropsOnlyThatRateOnce(t *testing.T) {
	net := func(rx, tx uint64) func(*Reading) {
		return func(r *Reading) { r.Nets = map[string]NetCounters{"eth0": {RxBytes: rx, TxBytes: tx}} }
	}
	var c Collector
	c.Sample(reading(t0, net(9000, 0)))
	s := c.Sample(reading(t0.Add(time.Second), net(100, 10)))
	if has(s, "network/rx:eth0") || !has(s, "network/tx:eth0") {
		t.Fatalf("got %v", s)
	}
	s = c.Sample(reading(t0.Add(2*time.Second), net(200, 20)))
	if s["network/rx:eth0"] != 100 {
		t.Fatalf("got %v", s)
	}
}

func TestAClockThatDoesNotAdvanceGivesNoRates(t *testing.T) {
	for _, at := range []time.Time{t0, t0.Add(-time.Second)} {
		var c Collector
		c.Sample(reading(t0, nil))
		s := c.Sample(reading(at, func(r *Reading) {
			r.CPU = CPUTimes{1, 4}
			r.Nets = map[string]NetCounters{"eth0": {RxBytes: 100}}
			r.Disk = &DiskCounters{ReadBytes: 100}
		}))
		for k := range s {
			if k == "cpu/total" || k == "alert/cpu_high" || len(k) > 8 && (k[:8] == "network/" || k[:5] == "disk/") {
				t.Errorf("unexpected %s", k)
			}
		}
		if !has(s, "memory/rss") {
			t.Fatalf("got %v", s)
		}
	}
}

func TestAnInterfaceThatAppearsHasNoRateUntilItsSecondReading(t *testing.T) {
	two := func(r *Reading) { r.Nets = map[string]NetCounters{"eth0": {}, "wlan0": {RxBytes: 100}} }
	var c Collector
	c.Sample(reading(t0, nil))
	s := c.Sample(reading(t0.Add(time.Second), two))
	if has(s, "network/rx:wlan0") || !has(s, "network/rx:eth0") {
		t.Fatalf("got %v", s)
	}
	s = c.Sample(reading(t0.Add(2*time.Second), func(r *Reading) {
		r.Nets = map[string]NetCounters{"eth0": {}, "wlan0": {RxBytes: 300}}
	}))
	if s["network/rx:wlan0"] != 200 {
		t.Fatalf("got %v", s)
	}
}

func TestAnInterfaceThatDisappearsLeavesNoKey(t *testing.T) {
	var c Collector
	c.Sample(reading(t0, nil))
	s := c.Sample(reading(t0.Add(time.Second), func(r *Reading) { r.Nets = nil }))
	if has(s, "network/rx:eth0") {
		t.Fatalf("got %v", s)
	}
}

func TestFilesystemFreePercent(t *testing.T) {
	var c Collector
	s := c.Sample(reading(t0, func(r *Reading) {
		r.Volumes = []Volume{
			{"/DATA", 1000, 250}, {"/zero", 0, 0}, {"/over", 100, 200},
			{"/proc", 100, 1}, {"/sys/fs/cgroup", 100, 1}, {"/dev/shm", 100, 1},
			{"/run/user/1000", 100, 1}, {"/snap/core/1", 100, 1},
			{"/var/lib/docker/overlay2/x/merged", 100, 1},
		}
	}))
	if s["fs/free_percent:/DATA"] != 75 {
		t.Fatalf("got %v", s)
	}
	n := 0
	for k := range s {
		if len(k) > 16 && k[:16] == "fs/free_percent:" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("got %v", s)
	}
}

func TestUnreadableDiskAndVolumesGiveNoKeysButTheRestRemains(t *testing.T) {
	var c Collector
	c.Sample(reading(t0, func(r *Reading) { r.Disk, r.Volumes = nil, nil }))
	s := c.Sample(reading(t0.Add(time.Second), func(r *Reading) { r.Disk, r.Volumes = nil, nil; r.CPU = CPUTimes{1, 4} }))
	if has(s, "disk/read") || has(s, "fs/free_percent:/DATA") || !has(s, "memory/rss") || !has(s, "cpu/total") {
		t.Fatalf("got %v", s)
	}
}

func TestWholeDisksKeepsPhysicalOnes(t *testing.T) {
	got := wholeDisks([]string{"sda", "sda1", "loop0", "nvme0n1", "dm-0", "zram0"},
		func(n string) bool { return n == "sda" || n == "nvme0n1" })
	if len(got) != 2 || got[0] != "sda" || got[1] != "nvme0n1" {
		t.Fatalf("got %v", got)
	}
}
