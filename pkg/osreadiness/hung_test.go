package osreadiness

import (
	"context"
	"slices"
	"testing"
	"time"
)

// A tool that does not answer (a Docker daemon that hangs, a disk that does not read, a mount of a
// network share that is gone) must cost the report its own time and no more: the check that asked is
// a refusal that says it did not answer, and the others are as they are.

// hang makes the commands never answer, until their check's time ends.
func (f *fakeProbe) hang(commands ...[]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, argv := range commands {
		f.cmds[key(argv...)] = fakeCmd{block: make(chan struct{})}
	}
}

// quick is the table with a short time for each check.
func quick(timeout time.Duration) []Check {
	checks := Checks()
	for i := range checks {
		checks[i].Timeout = timeout
	}
	return checks
}

// hungCodes are the checks that ask the commands that hang in these tests, and so cannot answer.
var hungCommands = [][]string{
	argvDockerInfo, aptCheckArgs, argvSmart("/dev/sda"), argvLsblk("/dev/sda1"), argvAudit, argvForeign, argvContainer,
}

var hungCodes = []string{
	// docker info
	"docker_swarm", "docker_down", "containers_policy", "containers_ports",
	// apt-get check, dpkg --audit
	"broken_dpkg",
	// smartctl, lsblk
	"smart_failed", "root_luks",
	// dpkg --print-foreign-architectures
	"foreign_arch",
	// systemd-detect-virt --container
	"container",
}

func TestACheckWhoseToolHangsEndsAtItsTimeAndTheOthersAreUnaffected(t *testing.T) {
	f := newBox(t)
	f.hang(hungCommands...)
	started := time.Now()
	report := Run(context.Background(), f, Options{Checks: quick(150 * time.Millisecond), Budget: 10 * time.Second})
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("the report took %v for checks of 150ms", elapsed)
	}
	for _, finding := range report.Checks {
		hung := slices.Contains(hungCodes, finding.Code)
		switch {
		case hung && (finding.Severity != Refuse || finding.Data["unknown"] != true || finding.Data["reason"] != ReasonTimeout):
			t.Errorf("%s asked a tool that hangs, and says %+v", finding.Code, finding)
		case !hung && finding.Data["unknown"] == true:
			t.Errorf("%s did not ask a tool that hangs, and does not know: %+v", finding.Code, finding)
		}
	}
	for _, code := range hungCodes {
		findingOf(t, report, code)
	}
	if report.State != StateBlocked {
		t.Errorf("state = %s: a check that did not answer is a refusal", report.State)
	}
}

// the same box when the tools do not stop for the end of their time (a process in an
// uninterruptible wait, which a kill does not reach): the report ends all the same
type stuckProbe struct {
	*fakeProbe
	stuck map[string]chan struct{}
}

func (p *stuckProbe) Run(ctx context.Context, argv ...string) (Output, error) {
	if release, ok := p.stuck[key(argv...)]; ok {
		<-release // does not look at its context at all
	}
	return p.fakeProbe.Run(ctx, argv...)
}

func TestACheckWhoseToolIgnoresItsContextEndsAtItsTimeToo(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	probe := &stuckProbe{fakeProbe: newBox(t), stuck: map[string]chan struct{}{}}
	for _, argv := range hungCommands {
		probe.stuck[key(argv...)] = release
	}
	started := time.Now()
	report := Run(context.Background(), probe, Options{Checks: quick(150 * time.Millisecond), Budget: 10 * time.Second})
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("the report took %v for checks of 150ms", elapsed)
	}
	for _, code := range hungCodes {
		if finding := findingOf(t, report, code); finding.Data["reason"] != ReasonTimeout || finding.Severity != Refuse {
			t.Errorf("%s = %+v", code, finding)
		}
	}
}

// When the whole report runs out of time, what is not done is a refusal that says so, whatever the
// time of each check: and the report is made, in the time it has and not a second more.
func TestTheReportEndsAtItsBudgetWhateverTheTimeOfEachCheck(t *testing.T) {
	f := newBox(t)
	f.hang(hungCommands...)
	started := time.Now()
	// five seconds a check, 300ms for the whole, and more hung checks than the four that run at once
	report := Run(context.Background(), f, Options{Checks: quick(5 * time.Second), Budget: 300 * time.Millisecond})
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("the report took %v for a budget of 300ms", elapsed)
	}
	for _, code := range hungCodes {
		finding := findingOf(t, report, code)
		if finding.Severity != Refuse || finding.Data["unknown"] != true || finding.Data["reason"] != ReasonNotFinished {
			t.Errorf("%s = %+v, want a refusal that did not finish", code, finding)
		}
	}
	if len(report.Checks) != len(slices.DeleteFunc(Checks(), func(c Check) bool { return len(c.Hops) > 0 && !slices.Contains(c.Hops, HopDebian11to12) })) {
		t.Errorf("the report has %d findings", len(report.Checks))
	}
	if report.State != StateBlocked || report.Counts.Refuse < len(hungCodes) {
		t.Errorf("state %s, counts %+v", report.State, report.Counts)
	}
}

// A report that asks for a tool that is not on the machine at all is as quick as one that does not,
// and says what it could not do for want of it.
func TestAToolThatIsNotThereIsAnAnswerAtOnce(t *testing.T) {
	f := newBox(t)
	for _, argv := range hungCommands {
		f.onError(ErrToolMissing, argv...)
	}
	started := time.Now()
	report := Run(context.Background(), f, Options{})
	if time.Since(started) > 2*time.Second {
		t.Fatalf("the report took %v", time.Since(started))
	}
	// no systemd-detect-virt is a machine that is looked at in other ways
	if got := findingOf(t, report, "container"); got.Severity != OK {
		t.Errorf("container = %+v", got)
	}
	// a service of Docker that runs and a docker command that is not there is a daemon that cannot be asked
	for _, code := range []string{"docker_swarm", "docker_down", "containers_policy", "containers_ports"} {
		if got := findingOf(t, report, code); got.Severity != Refuse || got.Data["reason"] != ReasonToolMissing {
			t.Errorf("%s = %+v", code, got)
		}
	}
	for _, code := range []string{"broken_dpkg", "foreign_arch", "root_luks", "smart_failed"} {
		if got := findingOf(t, report, code); got.Severity != Refuse || got.Data["reason"] != ReasonToolMissing {
			t.Errorf("%s = %+v", code, got)
		}
	}
}
