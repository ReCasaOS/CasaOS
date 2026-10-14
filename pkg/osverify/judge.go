package osverify

import (
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// How serious a finding is. An error is a machine that is not what the upgrade promised: the run ends in a failed check.
// A warning is something that the owner should look at, and the run ends as succeeded with findings. Information is
// listed and changes no state: a Docker box normally keeps Docker's source paused, and that is not a problem.
const (
	SeverityError = "error"
	SeverityWarn  = "warn"
	SeverityInfo  = "info"
)

// The findings, which are a closed list: the page has a sentence for each of them, and a test says that every one of them
// can be made.
const (
	// Errors.
	CodeKernelUnexpected = "kernel_unexpected" // the machine runs another kernel than the one the upgrade installed
	CodeCasaosUnitDown   = "casaos_unit_down"  // a unit of ReCasaOS that ran before the upgrade does not run now
	CodeDockerDown       = "docker_down"       // Docker ran, and does not now
	CodeMountsMissing    = "mounts_missing"    // a mergerfs pool that was mounted is not
	CodeDpkgNotQuiet     = "dpkg_not_quiet"    // dpkg has a package that is not whole
	CodeSystemNotReady   = "system_not_ready"  // systemd is still starting, or is stopping
	// Warnings.
	CodeUnitsFailedNew          = "units_failed_new"          // units that fail now and did not fail before
	CodePortsMissing            = "ports_missing"             // ports that were listened on and are not
	CodeSambaUsersChanged       = "samba_users_changed"       // Samba does not hold the users it held
	CodeRouteMissing            = "route_missing"             // the machine had a default route and has none
	CodeDNSMissing              = "dns_missing"               // the resolver answered and does not
	CodeDockerVersionChanged    = "docker_version_changed"    // the daemon is not the one of before
	CodeDockerSourceUnreachable = "docker_source_unreachable" // Docker's repository could not be asked, and its source stays paused
	CodeContainerNotReturned    = "container_not_returned"    // a container that has a restart policy ran before, and does not now
	CodeFactsIncomplete         = "facts_incomplete"          // what the machine is now could not be taken in full: what was not taken is not judged
	// Information.
	CodeDockerSourceNotPublished = "docker_source_not_published" // Docker publishes nothing for the new release yet
	CodeDockerSourcePaused       = "docker_source_paused"        // Docker's source is still paused
	CodeDpkgDistNew              = "dpkg_dist_new"               // files that dpkg left beside the ones the owner edited, since the upgrade began
	CodeSourcesLeftPaused        = "sources_left_paused"         // sources of apt that the upgrade paused and that are still paused
	CodeChangedSinceSnapshot     = "changed_since_snapshot"      // something that was there when the hop began, and was not there when the owner asked for the restart
)

// Codes are the findings, errors first, in the order that Judge lists them.
var Codes = []string{
	CodeKernelUnexpected, CodeCasaosUnitDown, CodeDockerDown, CodeMountsMissing, CodeDpkgNotQuiet, CodeSystemNotReady,
	CodeUnitsFailedNew, CodePortsMissing, CodeSambaUsersChanged, CodeRouteMissing, CodeDNSMissing, CodeDockerVersionChanged,
	CodeDockerSourceUnreachable, CodeContainerNotReturned, CodeFactsIncomplete,
	CodeDockerSourceNotPublished, CodeDockerSourcePaused, CodeDpkgDistNew, CodeSourcesLeftPaused, CodeChangedSinceSnapshot,
}

var severities = map[string]string{
	CodeKernelUnexpected: SeverityError, CodeCasaosUnitDown: SeverityError, CodeDockerDown: SeverityError, CodeMountsMissing: SeverityError,
	CodeDpkgNotQuiet: SeverityError, CodeSystemNotReady: SeverityError,
	CodeUnitsFailedNew: SeverityWarn, CodePortsMissing: SeverityWarn, CodeSambaUsersChanged: SeverityWarn, CodeRouteMissing: SeverityWarn,
	CodeDNSMissing: SeverityWarn, CodeDockerVersionChanged: SeverityWarn, CodeDockerSourceUnreachable: SeverityWarn, CodeContainerNotReturned: SeverityWarn,
	CodeFactsIncomplete:          SeverityWarn,
	CodeDockerSourceNotPublished: SeverityInfo, CodeDockerSourcePaused: SeverityInfo, CodeDpkgDistNew: SeverityInfo, CodeSourcesLeftPaused: SeverityInfo,
	CodeChangedSinceSnapshot: SeverityInfo,
}

// SeverityOf is how serious a finding is, "" for a code that is not one.
func SeverityOf(code string) string { return severities[code] }

// Finding is one thing that the judge found. Arg says what it is about, in one token or a short text that can be shown as it
// is: a unit, a port, a path, a name, a kernel. Where it says that something is not what it was, it is "expected>found"
// (the kernel that was to run and the one that runs, the users that Samba held and holds, the version of Docker before and
// now). Apps are the containers that a missing mount point puts at risk, and Policy is the restart policy of a container
// that did not return: each is there for one code only.
type Finding struct {
	Code     string   `json:"code"`
	Severity string   `json:"severity"`
	Arg      string   `json:"arg,omitempty"`
	Apps     []string `json:"apps,omitempty"`
	Policy   string   `json:"policy,omitempty"`
}

// Summarize counts what changes the state of a run: the errors and the warnings. Information does not count, and is not
// in what the reducer is given.
func Summarize(findings []Finding) osrun.VerifyResult {
	var result osrun.VerifyResult
	for _, finding := range findings {
		switch finding.Severity {
		case SeverityError:
			result.Errors++
		case SeverityWarn:
			result.Warnings++
		}
	}
	return result
}

// The files that the blocks of the hop leave in the backup folder and that the judge reads. Block 2 writes down the state of
// the machine before the hop (kernel is written by block 7, the note about Docker's source by block 8), and block 8 writes
// the same lists again after the restart.
const (
	BKKernel           = "kernel"
	BKListening        = "listening.txt"
	BKListeningAfter   = "listening-after.txt"
	BKFailedUnits      = "failed-units.txt"
	BKFailedUnitsAfter = "failed-units-after.txt"
	BKCasaosUnits      = "casaos-units.txt"
	BKContainers       = "containers.txt"
	BKDpkgFiles        = "dpkg-files.txt"
	BKDpkgFilesAfter   = "dpkg-files-after.txt"
	BKDockerSourceNote = "docker-source-note"
)

// BKNames are the files of the backup folder that the judge reads.
var BKNames = []string{
	BKKernel, BKListening, BKListeningAfter, BKFailedUnits, BKFailedUnitsAfter, BKCasaosUnits, BKContainers,
	BKDpkgFiles, BKDpkgFilesAfter, BKDockerSourceNote,
}

// BKFiles are those files, by name, as they were read. A file that is not there is not in it: a list that was not written
// is not an empty list, and nothing is concluded from it.
type BKFiles map[string]string

// The words of the note that block 8 leaves when it could not put Docker's source back.
const (
	noteUnreachable  = "unreachable"
	noteNoCurl       = "no_curl"
	noteNotPublished = "not_published"
)

// maxPerCode is the most findings of one code: a box that lost a hundred ports has told what it has to tell.
const maxPerCode = 100

// maxLines is the most lines that are read of a file of the backup folder.
const maxLines = 5000

// ephemeralPort is where the range of the ports that a machine hands out to whoever asks begins (net.ipv4.ip_local_port_range
// has 32768 to 60999 by default): a service that listens there got its port from the kernel, and it is another one after a
// restart, so a port that is gone from there is no port that was lost.
const ephemeralPort = 32768

var (
	unitNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._:\\-]{0,127}$`)
	containerLine   = regexp.MustCompile(`^/?([A-Za-z0-9][A-Za-z0-9_.-]{0,127}) policy=([a-z-]{0,32}) state=([a-z]{1,20})$`)
	// upStates are the states of a unit that is up: running, or running and reading its configuration again.
	upStates = []string{"active", "reloading"}
)

// bkLines are the lines of a file of the backup folder, without their ends and without the empty ones, and whether the file
// is there at all.
func bkLines(bk BKFiles, name string) (lines []string, ok bool) {
	text, ok := bk[name]
	if !ok {
		return nil, false
	}
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
			if len(lines) == maxLines {
				break
			}
		}
	}
	return lines, true
}

// clean makes what comes from the box into text that can be shown: no control character, at most 200 bytes.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
	if len(s) > 200 {
		s = s[:200]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

type judge struct {
	before, after Facts
	// restart are the facts of the machine when the owner asked for its restart, which may be long after the hop: the zero Facts
	// when they were not taken. changed are the things that were not there then and were there when the hop began.
	restart Facts
	changed []string
	bk      BKFiles
	out     []Finding
	count   map[string]int
}

// add lists a finding, and gives it back so that what is particular to its code can be put in it; nil when the code has
// already as many findings as it is given.
func (j *judge) add(code, arg string, apps ...string) *Finding {
	if j.count[code] >= maxPerCode {
		return nil
	}
	j.count[code]++
	finding := Finding{Code: code, Severity: severities[code], Arg: clean(arg)}
	if len(apps) > 0 {
		finding.Apps = apps
	}
	j.out = append(j.out, finding)
	return &j.out[len(j.out)-1]
}

// Options are what the judge is told besides the facts of before and after and the files of the backup folder.
type Options struct {
	// Restart are the facts that were taken when the owner asked for the restart. The machine waits for it, sometimes for days,
	// and the owner may stop an app or a unit of ReCasaOS meanwhile: what was running when the hop began and was not when the
	// restart was asked for is what the owner changed, and is told as that (changed_since_snapshot), not as what the restart did.
	// The zero Facts say that none were taken.
	Restart Facts
}

// Judge compares the machine after the upgrade with the machine before it, and with what the blocks of the hop wrote down
// of it, and says what is not as it was. The facts of before are those that Take took when the hop was started, or the zero
// Facts when they are lost; what the facts of after do not know is not judged, and what is not judged is said (facts_incomplete)
// where it was known before. The findings are listed with the errors first, then the warnings, then the information, each in
// the order of Codes and, within a code, by name. It is never nil.
func Judge(before, after Facts, bk BKFiles) []Finding { return JudgeWith(before, after, bk, Options{}) }

// JudgeWith is Judge with the options.
func JudgeWith(before, after Facts, bk BKFiles, opts Options) []Finding {
	j := &judge{before: before, after: after, restart: opts.Restart, bk: bk, out: []Finding{}, count: map[string]int{}}
	j.kernel()
	j.casaosUnits()
	j.dockerDown()
	j.mounts()
	j.dpkgQuiet()
	j.systemReady()
	j.failedUnits()
	j.ports()
	j.samba()
	j.route()
	j.dns()
	j.dockerVersion()
	j.dockerSource()
	j.containers()
	j.dpkgFiles()
	j.paused()
	j.incomplete()
	j.addChanged()
	rank := func(code string) int { return slices.Index(Codes, code) }
	sort.SliceStable(j.out, func(a, b int) bool { return rank(j.out[a].Code) < rank(j.out[b].Code) })
	return j.out
}

func (j *judge) kernel() {
	lines, _ := bkLines(j.bk, BKKernel)
	if len(lines) == 0 || !kernelPattern.MatchString(lines[0]) || !j.after.Knows(FactKernel) {
		return
	}
	if want := lines[0]; j.after.Kernel != want {
		j.add(CodeKernelUnexpected, want+">"+j.after.Kernel)
	}
}

// casaosUnitStates are the units that block 2 wrote down, with the state it saw them in (the second column of systemctl
// list-units).
func casaosUnitStates(bk BKFiles) map[string]string {
	lines, _ := bkLines(bk, BKCasaosUnits)
	states := map[string]string{}
	for _, line := range lines {
		if fields := strings.Fields(line); len(fields) == 2 && unitNamePattern.MatchString(fields[0]) {
			states[fields[0]] = fields[1]
		}
	}
	return states
}

func (j *judge) casaosUnits() {
	if !j.after.Knows(FactUnits) {
		return
	}
	before := casaosUnitStates(j.bk)
	for _, unit := range sortedKeys(before) {
		if before[unit] != "active" || slices.Contains(transientUnits, unit) {
			continue
		}
		// a unit that has no file any more is a unit that does not run, and one that systemd could not be asked about is not said down
		// (it is said that it could not be: incomplete)
		state := j.after.Units[unit]
		if state != unitUnknown && !slices.Contains(upStates, state) {
			// a unit that the owner had stopped by the time they asked for the restart did not stop because of it
			if j.restart.Knows(FactUnits) && j.restart.Units[unit] == "inactive" {
				j.changed = append(j.changed, "unit:"+unit)
				continue
			}
			j.add(CodeCasaosUnitDown, unit)
		}
	}
}

// bkRunning are the containers that block 2 saw running, by name, with their restart policy.
func (j *judge) bkRunning() map[string]string {
	lines, _ := bkLines(j.bk, BKContainers)
	running := map[string]string{}
	for _, line := range lines {
		if m := containerLine.FindStringSubmatch(line); m != nil && m[3] == "running" {
			running[m[1]] = m[2]
		}
	}
	return running
}

func (j *judge) dockerDown() {
	if !j.after.Knows(FactDocker) || j.after.Docker.State == DockerUp {
		return
	}
	// Docker ran if the facts of before say so; when they cannot say (they were lost, or Docker could not be asked) the list that
	// block 2 wrote does, if it has containers that ran.
	ran := len(j.bkRunning()) > 0
	if j.before.Knows(FactDocker) {
		ran = j.before.Docker.State == DockerUp
	}
	if ran {
		j.add(CodeDockerDown, j.after.Docker.State)
	}
}

// appsUnder are the containers of the facts of before that have mounted a directory of the pool at mount point into
// themselves.
func (j *judge) appsUnder(mountPoint string) []string {
	var apps []string
	if !j.before.Knows(FactContainers) {
		return nil
	}
	for _, c := range j.before.Docker.Containers {
		for _, bind := range c.Binds {
			if bind == mountPoint || strings.HasPrefix(bind, strings.TrimSuffix(mountPoint, "/")+"/") {
				apps = append(apps, c.Name)
				break
			}
		}
	}
	return apps
}

// within says whether a path is the other or below it.
func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/")
}

// appsOn are the containers of the facts of before that have a directory mounted into themselves that depends on this mount
// point: one at it, below it (the mount is where it is kept) or above it (it is a mount of a part of what is mounted).
func (j *judge) appsOn(mountPoint string) []string {
	var apps []string
	if !j.before.Knows(FactContainers) {
		return nil
	}
	for _, c := range j.before.Docker.Containers {
		if slices.ContainsFunc(c.Binds, func(bind string) bool { return within(bind, mountPoint) || within(mountPoint, bind) }) {
			apps = append(apps, c.Name)
		}
	}
	return apps
}

// The mounts that matter, and that are not there after the restart, are told: a pool that is not mounted, a pool that has lost
// a branch, a disk that is a branch of a pool, and any mount that an app has a directory in (its files and its configuration are
// then written into the empty directory that is left). A mount that no pool and no app depends on is not the machine's to
// miss: the owner mounts and unmounts what they like.
func (j *judge) mounts() {
	if !j.before.Knows(FactMounts) || !j.after.Knows(FactMounts) {
		return
	}
	apps := map[string][]string{}
	say := func(mountPoint string, found []string) {
		apps[mountPoint] = append(apps[mountPoint], found...)
	}
	for _, mountPoint := range j.before.Mergerfs {
		if !slices.Contains(j.after.Mergerfs, mountPoint) {
			say(mountPoint, j.appsUnder(mountPoint))
		}
	}
	var branchOf = map[string][]string{}
	for _, pool := range sortedKeys(j.before.Pools) {
		for _, branch := range branchMounts(j.before.Pools[pool], j.before.Mounts) {
			branchOf[branch] = append(branchOf[branch], pool)
		}
		if !slices.Contains(j.after.Mergerfs, pool) {
			continue
		}
		for _, branch := range j.before.Pools[pool] {
			if !slices.Contains(j.after.Pools[pool], branch) {
				say(branch, j.appsUnder(pool))
			}
		}
	}
	for _, mountPoint := range j.before.Mounts {
		if mountPoint == "/" || slices.Contains(j.after.Mounts, mountPoint) {
			continue
		}
		found := j.appsOn(mountPoint)
		if len(found) == 0 && len(branchOf[mountPoint]) == 0 {
			continue
		}
		say(mountPoint, found)
		for _, pool := range branchOf[mountPoint] {
			say(mountPoint, j.appsUnder(pool))
		}
	}
	for _, mountPoint := range sortedKeys(apps) {
		found := slices.Clone(apps[mountPoint])
		slices.Sort(found)
		j.add(CodeMountsMissing, mountPoint, slices.Compact(found)...)
	}
}

func (j *judge) dpkgQuiet() {
	if j.after.Knows(FactDpkg) && !j.after.DpkgQuiet {
		j.add(CodeDpkgNotQuiet, "")
	}
}

// systemReady says whether systemd's word for the state of the system is one of a system that has started: running, or
// degraded, which is a system that has started with a unit that failed (the units that fail are told of apart).
func systemReady(state string) bool { return state == "running" || state == "degraded" }

func (j *judge) systemReady() {
	if j.after.Knows(FactSystem) && !systemReady(j.after.System) {
		j.add(CodeSystemNotReady, j.after.System)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// The lists of the units that fail and of the ports that are listened on: before, they are the ones that block 2 wrote down at
// the hop; after, they are the machine's now when it could be asked, and what block 8 wrote down after the restart when it could
// not (a look that is made again, a long time after the check, reads the machine and not a list that is old).
func (j *judge) failedUnits() {
	before, okBefore := bkLines(j.bk, BKFailedUnits)
	after, okAfter := bkLines(j.bk, BKFailedUnitsAfter)
	if j.after.Knows(FactFailed) {
		after, okAfter = j.after.Failed, true
	}
	if !okBefore || !okAfter {
		return
	}
	for _, unit := range newLines(before, after, unitNamePattern.MatchString) {
		j.add(CodeUnitsFailedNew, unit)
	}
}

// newLines are the lines of after that are not in before and that pass valid, once each and sorted.
func newLines(before, after []string, valid func(string) bool) []string {
	old := map[string]bool{}
	for _, line := range before {
		old[line] = true
	}
	var fresh []string
	for _, line := range after {
		if !old[line] && valid(line) {
			old[line] = true
			fresh = append(fresh, line)
		}
	}
	slices.Sort(fresh)
	return fresh
}

// portsOf are the ports of the lines of ss -Hltn that a service asked for (the local address, "0.0.0.0:22", "[::]:22",
// "*:80"), once each.
func portsOf(lines []string) map[int]bool {
	ports := map[int]bool{}
	for _, line := range lines {
		colon := strings.LastIndex(line, ":")
		if colon < 0 {
			continue
		}
		if port, err := strconv.Atoi(line[colon+1:]); err == nil && port >= 1 && port <= 65535 {
			ports[port] = true
		}
	}
	return ports
}

func (j *judge) ports() {
	before, okBefore := bkLines(j.bk, BKListening)
	after, okAfter := bkLines(j.bk, BKListeningAfter)
	if j.after.Knows(FactListening) {
		after, okAfter = j.after.Listening, true
	}
	if !okBefore || !okAfter {
		return
	}
	now := portsOf(after)
	var gone []int
	for port := range portsOf(before) {
		if port < ephemeralPort && !now[port] {
			gone = append(gone, port)
		}
	}
	slices.Sort(gone)
	for _, port := range gone {
		j.add(CodePortsMissing, strconv.Itoa(port))
	}
}

func (j *judge) samba() {
	if !j.before.Knows(FactSamba) || !j.after.Knows(FactSamba) {
		return
	}
	if j.before.SambaUsers != j.after.SambaUsers {
		j.add(CodeSambaUsersChanged, strconv.Itoa(j.before.SambaUsers)+">"+strconv.Itoa(j.after.SambaUsers))
	}
}

func (j *judge) route() {
	if j.before.Knows(FactRoute) && j.before.Route != "" && j.after.Knows(FactRoute) && j.after.Route == "" {
		j.add(CodeRouteMissing, j.before.Route)
	}
}

func (j *judge) dns() {
	if j.before.Knows(FactDNS) && j.before.DNS && j.after.Knows(FactDNS) && !j.after.DNS {
		j.add(CodeDNSMissing, "")
	}
}

// engineChanged is the Arg of a Docker whose version is the same and whose engine is not: its data directory was made again.
const engineChanged = "engine_id"

func (j *judge) dockerVersion() {
	b, a := j.before.Docker, j.after.Docker
	if !j.before.Knows(FactDocker) || !j.after.Knows(FactDocker) || b.State != DockerUp || a.State != DockerUp {
		return
	}
	switch {
	case b.Version != "" && a.Version != "" && b.Version != a.Version:
		j.add(CodeDockerVersionChanged, b.Version+">"+a.Version)
	case b.EngineID != "" && a.EngineID != "" && b.EngineID != a.EngineID:
		j.add(CodeDockerVersionChanged, engineChanged)
	}
}

func (j *judge) dockerSource() {
	lines, _ := bkLines(j.bk, BKDockerSourceNote)
	if len(lines) == 0 {
		return
	}
	switch lines[0] {
	case noteUnreachable, noteNoCurl:
		j.add(CodeDockerSourceUnreachable, lines[0])
	case noteNotPublished:
		j.add(CodeDockerSourceNotPublished, "")
	}
}

// NotReturned are the containers that block 2 saw running before the hop, that Docker does not list as running now, with
// their restart policy, sorted by name. It is empty when Docker does not answer now (docker_down says that) or when the
// containers cannot be listed. A container that is in the list and has no restart policy was stopped by the restart and
// does not start by itself: the page says so, and it is not a finding.
func NotReturned(bk BKFiles, after Facts) []dockerpkg.NotReturned {
	gone := []dockerpkg.NotReturned{}
	if !after.Knows(FactDocker) || after.Docker.State != DockerUp || !after.Knows(FactContainers) {
		return gone
	}
	lines, _ := bkLines(bk, BKContainers)
	running := map[string]bool{}
	for _, c := range after.Docker.Containers {
		running[c.Name] = true
	}
	seen := map[string]bool{}
	for _, line := range lines {
		m := containerLine.FindStringSubmatch(line)
		if m == nil || m[3] != "running" || running[m[1]] || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		gone = append(gone, dockerpkg.NotReturned{Name: m[1], RestartPolicy: m[2]})
	}
	slices.SortFunc(gone, func(a, b dockerpkg.NotReturned) int { return strings.Compare(a.Name, b.Name) })
	return gone
}

// stoppedBeforeRestart are the containers that ran when block 2 wrote them down and did not run when the owner asked for the
// restart: the restart is not what stopped them.
func (j *judge) stoppedBeforeRestart() map[string]bool {
	stopped := map[string]bool{}
	r := j.restart
	if !r.Knows(FactDocker) || !r.Knows(FactContainers) || r.Docker.State != DockerUp {
		return stopped
	}
	running := map[string]bool{}
	for _, c := range r.Docker.Containers {
		running[c.Name] = true
	}
	for name := range j.bkRunning() {
		if !running[name] {
			stopped[name] = true
		}
	}
	return stopped
}

func (j *judge) containers() {
	stopped := j.stoppedBeforeRestart()
	for _, c := range NotReturned(j.bk, j.after) {
		if c.RestartPolicy != "" && c.RestartPolicy != "no" {
			if stopped[c.Name] {
				j.changed = append(j.changed, "container:"+c.Name)
				continue
			}
			if finding := j.add(CodeContainerNotReturned, c.Name); finding != nil {
				finding.Policy = c.RestartPolicy
			}
		}
	}
}

// incomplete says what of the machine could not be taken and was to be judged: a fact that the facts of before knew, or that
// the files of block 2 are the reference of, and that the facts of after do not have (a command that did not answer, a time that
// ran out), and a file of the backup folder that is there and cannot be read. What was not taken is not judged, and a machine
// that was judged on part of what it is is not said to be well on the strength of it: the owner is told, and can ask again.
func (j *judge) incomplete() {
	if !j.after.Taken {
		j.add(CodeFactsIncomplete, "all")
		return
	}
	var missing []string
	need := func(fact string, because bool) {
		if because && !j.after.Knows(fact) && !slices.Contains(missing, fact) {
			missing = append(missing, fact)
		}
	}
	kernel, _ := bkLines(j.bk, BKKernel)
	_, hasFailed := bkLines(j.bk, BKFailedUnits)
	_, hasListening := bkLines(j.bk, BKListening)
	running := len(j.bkRunning()) > 0
	dockerBefore := j.before.Knows(FactDocker) && j.before.Docker.State == DockerUp || !j.before.Knows(FactDocker) && running
	need(FactKernel, len(kernel) > 0 && kernelPattern.MatchString(kernel[0]))
	need(FactSystem, true)
	need(FactDpkg, true)
	need(FactUnits, len(casaosUnitStates(j.bk)) > 0)
	need(FactSources, true)
	need(FactFailed, hasFailed)
	need(FactListening, hasListening)
	need(FactSamba, j.before.Knows(FactSamba))
	need(FactMounts, j.before.Knows(FactMounts) && len(j.before.Mounts)+len(j.before.Mergerfs) > 0)
	need(FactRoute, j.before.Knows(FactRoute) && j.before.Route != "")
	need(FactDNS, j.before.Knows(FactDNS) && j.before.DNS)
	need(FactDocker, dockerBefore)
	need(FactContainers, dockerBefore && j.after.Docker.State == DockerUp && (running || j.before.Knows(FactContainers) && len(j.before.Docker.Containers) > 0))
	// a unit that was running and that systemd could not be asked about
	if j.after.Knows(FactUnits) {
		for unit, state := range casaosUnitStates(j.bk) {
			if state == "active" && !slices.Contains(transientUnits, unit) && j.after.Units[unit] == unitUnknown && !slices.Contains(missing, FactUnits) {
				missing = append(missing, FactUnits)
			}
		}
	}
	for _, fact := range j.after.Unknown {
		if strings.HasPrefix(fact, UnreadablePrefix) && !slices.Contains(missing, fact) {
			missing = append(missing, fact)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		j.add(CodeFactsIncomplete, strings.Join(missing, ","))
	}
}

// addChanged tells what the owner changed between the hop and the restart, once each and sorted.
func (j *judge) addChanged() {
	slices.Sort(j.changed)
	for _, what := range slices.Compact(j.changed) {
		j.add(CodeChangedSinceSnapshot, what)
	}
}

func (j *judge) dpkgFiles() {
	before, okBefore := bkLines(j.bk, BKDpkgFiles)
	after, okAfter := bkLines(j.bk, BKDpkgFilesAfter)
	if !okBefore || !okAfter {
		return
	}
	for _, path := range newLines(before, after, func(string) bool { return true }) {
		j.add(CodeDpkgDistNew, path)
	}
}

// dockerSourceFile is the file that Docker's source is set aside as.
const dockerSourceFile = "docker.list" + pausedSuffix

func (j *judge) paused() {
	if !j.after.Knows(FactSources) {
		return
	}
	for _, entry := range j.after.Paused {
		if entry == dockerSourceFile {
			j.add(CodeDockerSourcePaused, "")
		} else {
			j.add(CodeSourcesLeftPaused, entry)
		}
	}
}
