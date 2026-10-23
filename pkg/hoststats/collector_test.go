package hoststats

import (
	"fmt"
	"strings"
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
		Volumes: []Volume{{"/", 1000, 250}},
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
	for _, k := range []string{"memory/rss", "memory/cache", "memory/swap", "memory/limit", "alert/mem_high", "cpu/vcpus", "fs/free_percent:/"} {
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

// Only the root filesystem gets a series: the store never frees one, and every mount point ever seen
// took one of the 48 for good. The disks alerts watch every storage.
func TestOnlyTheRootFilesystemIsRecorded(t *testing.T) {
	var c Collector
	s := c.Sample(reading(t0, func(r *Reading) {
		r.Volumes = []Volume{{"/DATA", 1000, 500}, {"/", 1000, 250}, {"/media/usb", 100, 1}, {"/mnt/192.168.1.5/Photos", 100, 1}}
	}))
	var fs []string
	for k := range s {
		if strings.HasPrefix(k, "fs/") {
			fs = append(fs, k)
		}
	}
	if len(fs) != 1 || s["fs/free_percent:/"] != 75 {
		t.Fatalf("got %v = %v", fs, s["fs/free_percent:/"])
	}
	for _, root := range []Volume{{"/", 0, 0}, {"/", 100, 200}} {
		if s := c.Sample(reading(t0, func(r *Reading) { r.Volumes = []Volume{root} })); has(s, "fs/free_percent:/") {
			t.Errorf("%+v: got %v", root, s["fs/free_percent:/"])
		}
	}
}

func TestUnreadableDiskAndVolumesGiveNoKeysButTheRestRemains(t *testing.T) {
	var c Collector
	c.Sample(reading(t0, func(r *Reading) { r.Disk, r.Volumes = nil, nil }))
	s := c.Sample(reading(t0.Add(time.Second), func(r *Reading) { r.Disk, r.Volumes = nil, nil; r.CPU = CPUTimes{1, 4} }))
	if has(s, "disk/read") || has(s, "fs/free_percent:/") || !has(s, "memory/rss") || !has(s, "cpu/total") {
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

func TestAtMostEightInterfacesSortedAndLongNamesSkipped(t *testing.T) {
	nets := map[string]NetCounters{strings.Repeat("x", 48): {}}
	for i := 19; i >= 0; i-- {
		nets[fmt.Sprintf("n%02d", i)] = NetCounters{}
	}
	var c Collector
	c.Sample(reading(t0, func(r *Reading) { r.Nets = nets }))
	s := c.Sample(reading(t0.Add(time.Second), func(r *Reading) { r.Nets = nets }))
	n := 0
	for k := range s {
		if strings.HasPrefix(k, "network/rx:") {
			n++
			if k > "network/rx:n07" {
				t.Errorf("not the first sorted: %s", k)
			}
		}
	}
	if n != 8 || has(s, "network/rx:"+strings.Repeat("x", 48)) {
		t.Fatalf("got %d nics", n)
	}
}

func TestTheWorstCaseStaysUnder48Series(t *testing.T) {
	nets := map[string]NetCounters{}
	for i := 0; i < 20; i++ {
		nets[fmt.Sprintf("n%02d", i)] = NetCounters{}
	}
	var vols []Volume
	for i := 29; i >= 0; i-- {
		vols = append(vols, Volume{fmt.Sprintf("/m%02d", i), 100, 1})
	}
	vols = append(vols, Volume{"/", 100, 1}, Volume{"/" + strings.Repeat("l", 48), 100, 1})
	var c Collector
	mut := func(r *Reading) {
		r.Nets, r.Volumes = nets, vols
		r.CPU = CPUTimes{1, 4}
		r.Disk = &DiskCounters{}
	}
	c.Sample(reading(t0, mut))
	s := c.Sample(reading(t0.Add(time.Second), func(r *Reading) {
		mut(r)
		r.CPU = CPUTimes{2, 8}
	}))
	fs := 0
	for k := range s {
		if strings.HasPrefix(k, "fs/") {
			fs++
		}
		if len(k) > 63 {
			t.Errorf("key too long: %s", k)
		}
	}
	if fs != 1 || !has(s, "fs/free_percent:/") {
		t.Fatalf("got %d mounts", fs)
	}
	if len(s) > 48 || len(s) != 12+24+1 {
		t.Fatalf("series = %d", len(s))
	}
}

func TestZeroCoresGivesNoCPUKeys(t *testing.T) {
	var c Collector
	z := func(b, tot float64) func(*Reading) {
		return func(r *Reading) { r.Cores = 0; r.CPU = CPUTimes{b, tot} }
	}
	c.Sample(reading(t0, z(1, 4)))
	s := c.Sample(reading(t0.Add(time.Second), z(2, 8)))
	for _, k := range []string{"cpu/total", "cpu/vcpus", "alert/cpu_high"} {
		if has(s, k) {
			t.Errorf("unexpected %s", k)
		}
	}
}

func TestNegativeCPUDeltasGiveNoCPUKeys(t *testing.T) {
	for _, cur := range []CPUTimes{{99, 404}, {101, 396}} {
		var c Collector
		c.Sample(reading(t0, func(r *Reading) { r.CPU = CPUTimes{100, 400} }))
		s := c.Sample(reading(t0.Add(time.Second), func(r *Reading) { r.CPU = cur }))
		if has(s, "cpu/total") || has(s, "alert/cpu_high") {
			t.Fatalf("%v: got %v", cur, s)
		}
	}
}

func TestABackwardDiskCounterDropsOnlyThatRate(t *testing.T) {
	var c Collector
	c.Sample(reading(t0, func(r *Reading) { r.Disk = &DiskCounters{ReadBytes: 9000, WriteBytes: 0} }))
	s := c.Sample(reading(t0.Add(time.Second), func(r *Reading) { r.Disk = &DiskCounters{ReadBytes: 100, WriteBytes: 50} }))
	if has(s, "disk/read") || s["disk/write"] != 50 {
		t.Fatalf("got %v", s)
	}
}
