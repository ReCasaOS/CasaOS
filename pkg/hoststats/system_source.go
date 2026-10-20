package hoststats

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"
)

// SystemSource reads this machine. The interface list comes from a shell
// script, the whole-disk list from /sys and the volumes from LocalStorage: all
// three are kept ListEvery (1 min when zero), never read per tick. Not safe for
// concurrent use: the Service ticks one at a time.
type SystemSource struct {
	Now        func() time.Time                            // nil: time.Now
	Interfaces func() []string                             // the same filter as /v1/sys/utilization
	Volumes    func(ctx context.Context) ([]Volume, error) // nil: none
	IsPhysical func(name string) bool                      // /sys/block/<name>/device exists; nil: every block device
	ListEvery  time.Duration

	blockDir  string // tests; default /sys/block
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

// refresh re-reads the three lists when the last read is ListEvery old. A
// failed volumes read leaves none: stale data is never shown as live.
func (s *SystemSource) refresh(ctx context.Context, now time.Time) {
	every := s.ListEvery
	if every <= 0 {
		every = time.Minute
	}
	if !s.listedAt.IsZero() && now.Sub(s.listedAt) < every {
		return
	}
	s.listedAt = now
	s.nets = nil
	if s.Interfaces != nil {
		s.nets = cleanNames(s.Interfaces())
	}
	s.wholeDisk = nil
	dir := s.blockDir
	if dir == "" {
		dir = "/sys/block"
	}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if s.IsPhysical == nil || s.IsPhysical(e.Name()) {
				s.wholeDisk = append(s.wholeDisk, e.Name())
			}
		}
	}
	s.volumes = nil
	if s.Volumes != nil {
		if v, err := s.Volumes(ctx); err == nil {
			s.volumes = v
		}
	}
}

// Read takes one reading. The CPU, memory and swap are required; the network,
// disk and volumes are left out when unreadable.
func (s *SystemSource) Read(ctx context.Context) (Reading, error) {
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	s.refresh(ctx, now)
	r := Reading{At: now, Volumes: s.volumes}

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

	if len(s.nets) > 0 {
		if counters, err := net.IOCountersWithContext(ctx, true); err == nil {
			r.Nets = map[string]NetCounters{}
			for _, c := range counters {
				for _, n := range s.nets {
					if c.Name == n {
						r.Nets[n] = NetCounters{RxBytes: c.BytesRecv, TxBytes: c.BytesSent, Dropped: c.Dropin + c.Dropout}
					}
				}
			}
		}
	}
	if len(s.wholeDisk) > 0 {
		if counters, err := disk.IOCountersWithContext(ctx, append([]string(nil), s.wholeDisk...)...); err == nil {
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
