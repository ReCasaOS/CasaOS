package osverify

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// withCommandBound shortens the time that each question to the machine has, for a test.
func withCommandBound(t *testing.T, d time.Duration) {
	t.Helper()
	was := commandBound
	commandBound = d
	t.Cleanup(func() { commandBound = was })
}

func TestTheTimeOfEachQuestionIsTheOneOfTheConstants(t *testing.T) {
	if commandBound != osrun.DefaultTiming.FactCommand || commandBound <= 0 || commandBound >= osrun.DefaultTiming.FactCommand*2 {
		t.Errorf("commandBound = %v", commandBound)
	}
}

func TestAQuestionThatHangsCostsItsOwnTimeAndNoFactAfterIt(t *testing.T) {
	withCommandBound(t, 80*time.Millisecond)
	f := healthyBox(t)
	f.hang("pdbedit", "-L")
	begin := time.Now()
	got := TakeWith(context.Background(), f, healthyResolver)
	if took := time.Since(begin); took > 5*time.Second {
		t.Errorf("the facts took %v", took)
	}
	if !slices.Equal(got.Unknown, []string{FactSamba}) {
		t.Fatalf("unknown = %v, want only what the hung command would have said", got.Unknown)
	}
	// What is taken after it is taken: the mounts, the route, the units, Docker and its containers, the sources.
	if len(got.Mergerfs) == 0 || got.Route != "eth0" || len(got.Units) == 0 || got.Docker.State != DockerUp || len(got.Docker.Containers) != 4 || len(got.Paused) == 0 {
		t.Errorf("a hung command blinded what came after it: %+v", got)
	}
}

func TestAFileThatDoesNotAnswerWhenItIsReadCostsItsOwnTimeToo(t *testing.T) {
	withCommandBound(t, 80*time.Millisecond)
	f := healthyBox(t)
	f.hangRead("/proc/mounts")
	got := TakeWith(context.Background(), f, healthyResolver)
	if !slices.Equal(got.Unknown, []string{FactMounts}) || got.Route != "eth0" || got.Docker.State != DockerUp {
		t.Errorf("%+v", got)
	}
}

func TestAToolThatDidNotAnswerInItsTimeIsNotAskedAgain(t *testing.T) {
	withCommandBound(t, 60*time.Millisecond)
	f := healthyBox(t)
	f.hang("systemctl", "is-system-running")
	got := TakeWith(context.Background(), f, healthyResolver)
	if !got.Knows(FactKernel) || !got.Knows(FactSamba) || !got.Knows(FactMounts) {
		t.Errorf("%+v", got)
	}
	if got.Knows(FactSystem) {
		t.Errorf("the state of the system is known from a command that did not answer: %+v", got)
	}
	// systemctl hung once: the units and Docker's service are not asked of it again, and are unknown, not down.
	if got.Knows(FactUnits) && len(got.Units) > 0 {
		for unit, state := range got.Units {
			if state != unitUnknown {
				t.Errorf("%s is %q though systemctl did not answer", unit, state)
			}
		}
	}
	if got.Knows(FactDocker) {
		t.Errorf("Docker is known from a systemctl that did not answer: %+v", got.Docker)
	}
	asked := 0
	for _, argv := range f.asked {
		if argv[0] == "systemctl" {
			asked++
		}
	}
	if asked != 1 {
		t.Errorf("systemctl was asked %d times after it hung once", asked)
	}
}

func TestTheTimeOfAllTheQuestionsEndsWithTheContextAndWhatWasNotAskedIsUnknown(t *testing.T) {
	f := healthyBox(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := TakeWith(ctx, f, healthyResolver)
	if len(got.Unknown) != len(allFacts) {
		t.Errorf("unknown = %v", got.Unknown)
	}
	if len(f.asked) != 0 {
		t.Errorf("a context that had ended was asked: %v", f.asked)
	}
}

func TestThePoolsAndTheMountsOfTheBox(t *testing.T) {
	for name, tc := range map[string]struct {
		table  string
		pools  map[string][]string
		mounts []string
	}{
		"a pool of two disks":                          {"a:b /DATA/pool fuse.mergerfs rw 0 0\n/dev/sdb1 /mnt/a ext4 rw 0 0\n", map[string][]string{"/DATA/pool": {"a", "b"}}, []string{"/DATA/pool", "/mnt/a"}},
		"the modes of the branches":                    {"/mnt/a=RW:/mnt/b=NC:/mnt/c=RO /p fuse.mergerfs rw 0 0\n", map[string][]string{"/p": {"/mnt/a", "/mnt/b", "/mnt/c"}}, []string{"/p"}},
		"a branch that has an equals sign in its name": {"/mnt/a=b:/mnt/c /p fuse.mergerfs rw 0 0\n", map[string][]string{"/p": {"/mnt/a=b", "/mnt/c"}}, []string{"/p"}},
		"a pattern":                   {"/mnt/disk* /p fuse.mergerfs rw 0 0\n", map[string][]string{"/p": {"/mnt/disk*"}}, []string{"/p"}},
		"the escapes of a path":       {"/m\\040n:/x /p\\040q fuse.mergerfs rw 0 0\n", map[string][]string{"/p q": {"/m n", "/x"}}, []string{"/p q"}},
		"a branch twice":              {"/a:/a:/b /p fuse.mergerfs rw 0 0\n", map[string][]string{"/p": {"/a", "/b"}}, []string{"/p"}},
		"the file systems of data":    {"d /1 ext4 rw 0 0\nd /2 xfs rw 0 0\nd /3 btrfs rw 0 0\nd /4 vfat rw 0 0\nd /5 ntfs3 rw 0 0\nd /6 fuseblk rw 0 0\nh:/x /7 nfs4 rw 0 0\n//h/s /8 cifs rw 0 0\nz /9 zfs rw 0 0\n", nil, []string{"/1", "/2", "/3", "/4", "/5", "/6", "/7", "/8", "/9"}},
		"what is not data":            {"p /proc proc rw 0 0\ns /sys sysfs rw 0 0\nt /run tmpfs rw 0 0\no /var/lib/docker/overlay2/x/merged overlay rw 0 0\nc /sys/fs/cgroup cgroup2 rw 0 0\nl /snap/core/1 squashfs ro 0 0\nu /mnt/remote fuse.sshfs rw 0 0\nf /srv fuse rw 0 0\n", nil, nil},
		"a mount twice":               {"d /1 ext4 rw 0 0\nd /1 ext4 rw 0 0\n", nil, []string{"/1"}},
		"a path that cannot be shown": {"d /a\\012b ext4 rw 0 0\nd /ok ext4 rw 0 0\n", nil, []string{"/ok"}},
		"a line that is short":        {"d /1\next4\nd /2 ext4 rw 0 0\n", nil, []string{"/2"}},
	} {
		f := healthyBox(t)
		f.set("/proc/mounts", tc.table)
		got := take(t, f)
		if !got.Knows(FactMounts) || !slices.Equal(got.Mounts, tc.mounts) {
			t.Errorf("%s: mounts %q, want %q", name, got.Mounts, tc.mounts)
		}
		if len(got.Pools) != len(tc.pools) {
			t.Errorf("%s: pools %v, want %v", name, got.Pools, tc.pools)
		}
		for pool, branches := range tc.pools {
			if !slices.Equal(got.Pools[pool], branches) {
				t.Errorf("%s: the branches of %q are %q, want %q", name, pool, got.Pools[pool], branches)
			}
		}
	}
}

func TestThePoolsAreBoundedAndTheMountsToo(t *testing.T) {
	var table string
	for i := 0; i < maxMounts+50; i++ {
		table += "d /m" + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676%26)) + " ext4 rw 0 0\n"
	}
	f := healthyBox(t)
	f.set("/proc/mounts", table)
	if got := take(t, f); len(got.Mounts) != maxMounts {
		t.Errorf("%d mounts are kept", len(got.Mounts))
	}
	table = ""
	for i := 0; i < maxPools+10; i++ {
		table += "/a /p" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + " fuse.mergerfs rw 0 0\n"
	}
	f.set("/proc/mounts", table)
	if got := take(t, f); len(got.Pools) != maxPools || len(got.Mergerfs) < maxPools {
		t.Errorf("%d pools are kept", len(got.Pools))
	}
	var branches string
	for i := 0; i < maxPoolBranches+20; i++ {
		branches += "/b" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ":"
	}
	f.set("/proc/mounts", branches+"/z /p fuse.mergerfs rw 0 0\n")
	if got := take(t, f); len(got.Pools["/p"]) != maxPoolBranches {
		t.Errorf("%d branches are kept", len(got.Pools["/p"]))
	}
}

func TestTheMountsOfBranchesAreTheBranchesThatAreMountsAndWhatAPatternMatches(t *testing.T) {
	mounts := []string{"/", "/mnt/disk1", "/mnt/disk2", "/mnt/data", "/mnt/disk10"}
	for name, tc := range map[string]struct {
		branches []string
		want     []string
	}{
		"a branch that is a mount":   {[]string{"/mnt/disk1"}, []string{"/mnt/disk1"}},
		"a branch that is a folder":  {[]string{"/mnt/disk1/media"}, nil},
		"a pattern":                  {[]string{"/mnt/disk*"}, []string{"/mnt/disk1", "/mnt/disk10", "/mnt/disk2"}},
		"a pattern that matches one": {[]string{"/mnt/disk?"}, []string{"/mnt/disk1", "/mnt/disk2"}},
		"a pattern of a set":         {[]string{"/mnt/disk[1]"}, []string{"/mnt/disk1"}},
		"a pattern that is not one":  {[]string{"/mnt/disk[1"}, nil},
		"a pattern that goes deeper": {[]string{"/mnt/*/x"}, nil},
		"a branch and a pattern":     {[]string{"/mnt/data", "/mnt/disk?"}, []string{"/mnt/data", "/mnt/disk1", "/mnt/disk2"}},
		"a branch that is not there": {[]string{"/mnt/gone"}, nil},
	} {
		got := branchMounts(tc.branches, mounts)
		slices.Sort(got)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}
