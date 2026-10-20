package hoststats

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type counted struct {
	now                    time.Time
	ifaces, vols, physical int
	names                  []string
	volErr                 error
}

func (c *counted) source(t *testing.T) *SystemSource {
	dir := t.TempDir()
	for _, n := range []string{"sda", "sdb"} {
		if err := os.Mkdir(filepath.Join(dir, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &SystemSource{
		Now:        func() time.Time { return c.now },
		Interfaces: func() []string { c.ifaces++; return c.names },
		Volumes: func(context.Context) ([]Volume, error) {
			c.vols++
			if c.volErr != nil {
				return nil, c.volErr
			}
			return []Volume{{"/DATA", 1000, 250}}, nil
		},
		IsPhysical: func(string) bool { c.physical++; return true },
		ListEvery:  time.Minute,
		blockDir:   dir,
	}
}

func TestTheInterfaceListIsReadOnceAMinute(t *testing.T) {
	c := &counted{now: t0}
	s := c.source(t)
	for range 12 {
		if _, err := s.Read(context.Background()); err != nil {
			t.Fatal(err)
		}
		c.now = c.now.Add(5 * time.Second)
	}
	if c.ifaces != 1 || c.vols != 1 || c.physical != 2 {
		t.Fatalf("after 12 reads: interfaces %d, volumes %d, disk checks %d; want 1, 1, 2", c.ifaces, c.vols, c.physical)
	}
	c.now = t0.Add(61 * time.Second)
	if _, err := s.Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.ifaces != 2 || c.vols != 2 || c.physical != 4 {
		t.Fatalf("after +61 s: interfaces %d, volumes %d, disk checks %d; want 2, 2, 4", c.ifaces, c.vols, c.physical)
	}
}

func TestAVolumesFailureKeepsTheLastGoodListUntilTheNextRefreshThenNone(t *testing.T) {
	c := &counted{now: t0}
	s := c.source(t)
	r, err := s.Read(context.Background())
	if err != nil || len(r.Volumes) != 1 {
		t.Fatalf("first read: %v, %+v", err, r.Volumes)
	}
	c.now = t0.Add(30 * time.Second)
	c.volErr = errors.New("LocalStorage is down")
	if r, _ = s.Read(context.Background()); len(r.Volumes) != 1 {
		t.Fatalf("before the refresh: %+v, want the last good list", r.Volumes)
	}
	c.now = t0.Add(61 * time.Second)
	r, err = s.Read(context.Background())
	if err != nil {
		t.Fatalf("a volumes failure must not fail the read: %v", err)
	}
	if r.Volumes != nil {
		t.Fatalf("after the failed refresh: %+v, want none", r.Volumes)
	}
	if r.MemTotal == 0 || r.Cores == 0 {
		t.Fatalf("the other fields must still be filled: %+v", r)
	}
}

func TestInterfacesNamedByTheShellAreTrimmedAndEmptyOnesDropped(t *testing.T) {
	got := cleanNames([]string{"eth0", "", " wlan0\n", "  "})
	if len(got) != 2 || got[0] != "eth0" || got[1] != "wlan0" {
		t.Fatalf("got %q", got)
	}
}

func TestSystemSourceReadsThisMachine(t *testing.T) {
	s := &SystemSource{Interfaces: func() []string { return nil }}
	r, err := s.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Cores <= 0 || r.MemTotal == 0 || r.CPU.Total <= 0 {
		t.Fatalf("cores %d, memory %d, cpu total %v: want all above 0", r.Cores, r.MemTotal, r.CPU.Total)
	}
}
