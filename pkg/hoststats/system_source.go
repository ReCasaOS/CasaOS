package hoststats

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"
)

// SystemSource reads this machine. The interface list comes from a shell
// script, the whole-disk list from /sys and the volumes from LocalStorage: all
// three are kept ListEvery (1 min when zero), never read per tick, and never awaited
// longer than refreshWait. Read is called by one goroutine at a time.
type SystemSource struct {
	Now        func() time.Time                            // nil: time.Now
	Interfaces func() []string                             // the same filter as /v1/sys/utilization
	Volumes    func(ctx context.Context) ([]Volume, error) // nil: none
	IsPhysical func(name string) bool                      // /sys/block/<name>/device exists; nil: every block device
	ListEvery  time.Duration

	blockDir  string        // tests; default /sys/block
	mu        sync.Mutex    // the lists below, written by the refresh goroutine
	running   chan struct{} // non-nil while a refresh runs
	listedAt  time.Time
	nets      []string
	wholeDisk []string
	volumes   []Volume
}

// cleanNames trims the names the shell printed and drops the empty ones.
func cleanNames(in []string) []string {
	var out []string
	for _, n := range in {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// refreshWait is how long a Read waits for the lists it started; a variable so
// tests can shorten it.
var refreshWait = 10 * time.Second

// refresh starts a re-read of the three lists when the last one is ListEvery
// old and none is still running, and waits for it at most refreshWait. A
// refresh that is late (Interfaces runs a shell script and has no context)
// never holds a Read: the last good interface and disk lists are used, the
// volumes are none, and the late refresh lands when it ends. At most one
// refresh, so one goroutine, exists at a time.
func (s *SystemSource) refresh(now time.Time) {
	every := s.ListEvery
	if every <= 0 {
		every = time.Minute
	}
	s.mu.Lock()
	if s.running != nil || (!s.listedAt.IsZero() && now.Sub(s.listedAt) < every) {
		s.mu.Unlock()
		return
	}
	s.listedAt = now
	done := make(chan struct{})
	s.running = done
	s.mu.Unlock()
	go s.reread(done)

	t := time.NewTimer(refreshWait)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		s.mu.Lock()
		s.volumes = nil // never stale data shown as live
		s.mu.Unlock()
	}
}

func (s *SystemSource) reread(done chan struct{}) {
	var nets, disks []string
	var volumes []Volume
	if s.Interfaces != nil {
		nets = cleanNames(s.Interfaces())
	}
	dir := s.blockDir
	if dir == "" {
		dir = "/sys/block"
	}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if s.IsPhysical == nil || s.IsPhysical(e.Name()) {
				disks = append(disks, e.Name())
			}
		}
	}
	if s.Volumes != nil {
		ctx, cancel := context.WithTimeout(context.Background(), refreshWait)
		if v, err := s.Volumes(ctx); err == nil {
			volumes = v // a failure leaves none
		}
		cancel()
	}
	s.mu.Lock()
	s.nets, s.wholeDisk, s.volumes = nets, disks, volumes
	s.running = nil
	s.mu.Unlock()
	close(done)
}

// Read takes one reading. The CPU, memory and swap are required; the network,
// disk and volumes are left out when unreadable.
func (s *SystemSource) Read(ctx context.Context) (Reading, error) {
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	s.refresh(now)
	s.mu.Lock()
	nets, wholeDisk, volumes := s.nets, s.wholeDisk, s.volumes
	s.mu.Unlock()
	r := Reading{At: now, Volumes: volumes}

	times, err := cpu.TimesWithContext(ctx, false)
	if err != nil {
		return Reading{}, err
	}
	if len(times) > 0 {
		t := times[0]
		total := t.Total() - t.Guest - t.GuestNice // gopsutil's own busy rule
		r.CPU = CPUTimes{Busy: total - t.Idle - t.Iowait, Total: total}
	}
	if r.Cores, err = cpu.CountsWithContext(ctx, true); err != nil {
		return Reading{}, err
	}
	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return Reading{}, err
	}
	r.MemTotal, r.MemUsed, r.MemCached = vm.Total, vm.Used, vm.Cached+vm.Buffers
	sw, err := mem.SwapMemoryWithContext(ctx)
	if err != nil {
		return Reading{}, err
	}
	r.SwapUsed = sw.Used

	if len(nets) > 0 {
		if counters, err := net.IOCountersWithContext(ctx, true); err == nil {
			r.Nets = map[string]NetCounters{}
			for _, c := range counters {
				for _, n := range nets {
					if c.Name == n {
						r.Nets[n] = NetCounters{RxBytes: c.BytesRecv, TxBytes: c.BytesSent, Dropped: c.Dropin + c.Dropout}
					}
				}
			}
		}
	}
	if len(wholeDisk) > 0 {
		if counters, err := disk.IOCountersWithContext(ctx, append([]string(nil), wholeDisk...)...); err == nil {
			d := &DiskCounters{}
			for _, c := range counters {
				d.ReadBytes += c.ReadBytes
				d.WriteBytes += c.WriteBytes
				d.ReadOps += c.ReadCount
				d.WriteOps += c.WriteCount
			}
			r.Disk = d
		}
	}
	return r, nil
}
