//go:build linux

package hoststats

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
)

// realSource is a SystemSource wired like the product: physical interfaces and
// block devices from /sys, one fixed volume.
func realSource() *SystemSource {
	return &SystemSource{
		Interfaces: func() []string {
			all, _ := os.ReadDir("/sys/class/net")
			virtual, _ := os.ReadDir("/sys/devices/virtual/net")
			skip := map[string]bool{}
			for _, v := range virtual {
				skip[v.Name()] = true
			}
			var names []string
			for _, e := range all {
				if !skip[e.Name()] {
					names = append(names, e.Name())
				}
			}
			return names
		},
		Volumes: func(context.Context) ([]Volume, error) {
			return []Volume{{MountPoint: "/DATA", Size: 1000, Used: 250}}, nil
		},
		IsPhysical: func(name string) bool {
			_, err := os.Stat(filepath.Join("/sys/block", name, "device"))
			return err == nil
		},
		ListEvery: time.Minute,
	}
}

func newCostService(tb testing.TB) *Service {
	svc, err := New(Config{Dir: tb.TempDir(), Source: realSource(), Clock: stats.SystemClock{}})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = svc.Close() })
	return svc
}

func cpuTime(tb testing.TB) time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		tb.Fatal(err)
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func TestOneTickCostsUnderTenMillisecondsOfCPU(t *testing.T) {
	svc := newCostService(t)
	ctx := context.Background()
	if err := svc.Tick(ctx); err != nil { // the first one also lists interfaces, disks and volumes
		t.Fatal(err)
	}
	const ticks = 60
	before := cpuTime(t)
	for range ticks {
		if err := svc.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	per := (cpuTime(t) - before) / ticks
	t.Logf("one tick costs %v of CPU (user+system, mean of %d)", per, ticks)
	if per >= 10*time.Millisecond {
		t.Fatalf("one tick costs %v of CPU, want under 10ms", per)
	}
}

func BenchmarkTick(b *testing.B) {
	svc := newCostService(b)
	ctx := context.Background()
	_ = svc.Tick(ctx)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := svc.Tick(ctx); err != nil {
			b.Fatal(err)
		}
	}
}
