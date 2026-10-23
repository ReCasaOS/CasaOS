// Package hoststats turns raw host readings into a stats.Sample.
package hoststats

import (
	"sort"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
)

// Item is the stats item id of the host.
const Item = "host"

type CPUTimes struct{ Busy, Total float64 }

type NetCounters struct{ RxBytes, TxBytes, Dropped uint64 }

type DiskCounters struct{ ReadBytes, WriteBytes, ReadOps, WriteOps uint64 }

type Volume struct {
	MountPoint string
	Size, Used uint64
}

type Reading struct {
	At       time.Time
	CPU      CPUTimes
	Cores    int
	MemTotal uint64
	MemUsed  uint64
	// MemCached and SwapUsed are bytes.
	MemCached, SwapUsed uint64
	Nets                map[string]NetCounters // physical interfaces only
	Disk                *DiskCounters          // nil: unreadable
	Volumes             []Volume               // nil: unreadable or none
}

// Collector keeps the previous Reading to compute rates. Not safe for concurrent use.
type Collector struct {
	prev    Reading
	hasPrev bool
}

// Sample converts r into a Sample; a value that cannot be computed is absent.
func (c *Collector) Sample(r Reading) stats.Sample {
	s := stats.Sample{}
	if r.Cores > 0 {
		s["cpu/vcpus"] = float64(r.Cores)
	}
	s["memory/rss"] = float64(r.MemUsed)
	s["memory/cache"] = float64(r.MemCached)
	s["memory/swap"] = float64(r.SwapUsed)
	s["memory/limit"] = float64(r.MemTotal)
	if r.MemTotal > 0 {
		s["alert/mem_high"] = float64(r.MemUsed) / float64(r.MemTotal) * 100
	}
	// the root filesystem only: a series per mount point ever seen used up the store's slots for good,
	// and the disks alerts watch every storage
	for _, v := range r.Volumes {
		if v.MountPoint == "/" && v.Size > 0 && v.Used <= v.Size {
			s["fs/free_percent:/"] = float64(v.Size-v.Used) / float64(v.Size) * 100
		}
	}

	if p := c.prev; c.hasPrev {
		if secs := r.At.Sub(p.At).Seconds(); secs > 0 {
			rate := func(key string, cur, old uint64) {
				if cur >= old {
					s[key] = float64(cur-old) / secs
				}
			}
			for _, name := range keptNets(r.Nets) {
				cur := r.Nets[name]
				if old, ok := p.Nets[name]; ok {
					rate("network/rx:"+name, cur.RxBytes, old.RxBytes)
					rate("network/tx:"+name, cur.TxBytes, old.TxBytes)
					rate("network/dropped:"+name, cur.Dropped, old.Dropped)
				}
			}
			if r.Disk != nil && p.Disk != nil {
				rate("disk/read", r.Disk.ReadBytes, p.Disk.ReadBytes)
				rate("disk/write", r.Disk.WriteBytes, p.Disk.WriteBytes)
				rate("disk/iops_read", r.Disk.ReadOps, p.Disk.ReadOps)
				rate("disk/iops_write", r.Disk.WriteOps, p.Disk.WriteOps)
			}
			dBusy, dTotal := r.CPU.Busy-p.CPU.Busy, r.CPU.Total-p.CPU.Total
			if r.Cores > 0 && dBusy >= 0 && dTotal > 0 {
				pct := dBusy / dTotal * 100
				s["cpu/total"] = pct * float64(r.Cores)
				s["alert/cpu_high"] = pct
			}
		}
	}
	c.prev, c.hasPrev = r, true
	return s
}

// The store allows 48 series per item and never frees one: 12 fixed keys,
// 3 per interface and 1 for the root filesystem, so at most 12 + 8*3 + 1 = 37.
const (
	maxNets    = 8
	maxKeyLen  = 63
	netKeyLong = "network/dropped:"
)

// keptNets returns the interface names that get series: sorted, short enough, first maxNets.
func keptNets(nets map[string]NetCounters) []string {
	var names []string
	for n := range nets {
		if len(netKeyLong)+len(n) <= maxKeyLen {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if len(names) > maxNets {
		names = names[:maxNets]
	}
	return names
}

// wholeDisks keeps the names isPhysical accepts, in order.
func wholeDisks(names []string, isPhysical func(name string) bool) []string {
	var out []string
	for _, n := range names {
		if isPhysical(n) {
			out = append(out, n)
		}
	}
	return out
}
