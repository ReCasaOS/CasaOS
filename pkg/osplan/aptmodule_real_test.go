package osplan

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// The two commands of the plan of a module against what the real apt says (testdata/realmodule, made by capture.sh in Debian 11 and Debian 12 chroots, apt 2.2.4 and 2.6.1, against
// a repository of throwaway packages): each case is the output of "apt-get -s install" and of "apt-get --print-uris install" for the same names, with the status apt ended with.

func realModule(t *testing.T, name string) Output {
	t.Helper()
	text, err := os.ReadFile(filepath.Join("testdata", "realmodule", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	rc, err := os.ReadFile(filepath.Join("testdata", "realmodule", name+".rc"))
	if err != nil {
		t.Fatal(err)
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(rc)))
	if err != nil {
		t.Fatal(err)
	}
	return Output{Text: strings.ReplaceAll(string(text), "\r\n", "\n"), ExitCode: code}
}

func realModuleCase(t *testing.T, label, name string) (sim, estimate Output) {
	t.Helper()
	return realModule(t, label+"-"+name+"-sim"), realModule(t, label+"-"+name+"-estimate")
}

func TestTheRealOutputOfAptForEachCaseOfAModuleIsReadAsWhatHappens(t *testing.T) {
	type want struct {
		asked                     []string
		install, upgrade          []string
		removes, downgrades, held []string
		download, extra           map[string]int64 // by label: the archives differ in size between the two (the compression of dpkg-deb)
		failure                   string
		detail                    []string
	}
	both := func(n int64) map[string]int64 { return map[string]int64{"d11": n, "d12": n} }
	for name, w := range map[string]want{
		// one named package that installs by itself: the case in which apt counts the transaction as trivial
		"trivial":    {asked: []string{"rvm-solo"}, install: []string{"rvm-solo"}, download: both(13_200), extra: both(12_300)},
		"nontrivial": {asked: []string{"rvm-main"}, install: []string{"rvm-dep", "rvm-main"}, download: both(351_000), extra: both(348_000)},
		"removal":    {asked: []string{"rvm-newer"}, install: []string{"rvm-newer"}, removes: []string{"rvm-old"}, download: map[string]int64{"d11": 7_020, "d12": 7_016}, extra: both(-75_800)},
		"downgrade":  {asked: []string{"rvm-down"}, downgrades: []string{"rvm-down"}, download: both(21_500), extra: both(-30_700)},
		"hold":       {asked: []string{"rvm-held"}, upgrade: []string{"rvm-held"}, held: []string{"rvm-held"}, download: both(10_100), extra: both(4096)},
		"nothing":    {asked: []string{"rvm-done"}, download: both(0), extra: both(0)},
		"mixed":      {asked: []string{"rvm-solo", "rvm-done"}, install: []string{"rvm-solo"}, download: both(13_200), extra: both(12_300)},
		// a package that is installed as a dependency: with the list of the sandbox apt says nothing of it; with the box's it marks it as installed by hand and says so
		"auto":        {asked: []string{"rvm-autodep"}, download: both(0), extra: both(0)},
		"auto-marked": {asked: []string{"rvm-autodep"}, download: both(0), extra: both(0)},
		// what cannot be done
		"unmet-held":    {asked: []string{"rvm-needs"}, failure: "apt_failed", detail: []string{"unmet dependencies", "rvm-needs : Depends: rvm-lib (>= 2) but 1.0 is to be installed", "Unable to correct problems"}},
		"unmet-missing": {asked: []string{"rvm-bad"}, failure: "apt_failed", detail: []string{"unmet dependencies", "rvm-bad : Depends: rvm-nothere but it is not installable"}},
		"virtual-two":   {asked: []string{"rvm-virt2"}, failure: "apt_failed", detail: []string{"has no installation candidate"}},
		"unknown":       {asked: []string{"rvm-unknown"}, failure: "apt_failed", detail: []string{"Unable to locate package rvm-unknown"}},
		// what apt takes for something else than the name it was given
		"regex":       {asked: []string{"rvm.lean"}, failure: "package_not_exact", detail: []string{"rvm.lean"}},
		"regexplus":   {asked: []string{"rvm+clean"}, failure: "package_not_exact", detail: []string{"rvm+clean"}},
		"virtual-one": {asked: []string{"rvm-virt"}, failure: "package_not_exact", detail: []string{"rvm-virt"}},
	} {
		for _, label := range []string{"d11", "d12"} {
			t.Run(label+"/"+name, func(t *testing.T) {
				sim, est := realModuleCase(t, label, name)
				res := ParseModuleSimulation(sim, est, w.asked)
				if res.Failure != w.failure {
					t.Fatalf("failure %q (%s), want %q", res.Failure, res.Detail, w.failure)
				}
				for _, part := range w.detail {
					if !strings.Contains(res.Detail, part) {
						t.Errorf("the detail %q lacks %q", res.Detail, part)
					}
				}
				if w.failure != "" {
					if len(res.ToInstall)+len(res.ToUpgrade) != 0 || res.Known {
						t.Errorf("an answer with a failure: %+v", res)
					}
					return
				}
				for field, pair := range map[string][2][]string{
					"install": {res.ToInstall, w.install}, "upgrade": {res.ToUpgrade, w.upgrade}, "removes": {res.Removes, w.removes},
					"downgrades": {res.Downgrades, w.downgrades}, "held": {res.Held, w.held},
				} {
					if !slices.Equal(pair[0], pair[1]) && (len(pair[0]) != 0 || len(pair[1]) != 0) {
						t.Errorf("%s = %v, want %v", field, pair[0], pair[1])
					}
				}
				if !res.Known || res.DownloadBytes != w.download[label] || res.ExtraBytes != w.extra[label] {
					t.Errorf("sizes: known %v, %d and %d, want %d and %d", res.Known, res.DownloadBytes, res.ExtraBytes, w.download[label], w.extra[label])
				}
			})
		}
	}
}

// the names in the block of unmet dependencies are the ones that can be held: what the real apt says, and not the words it says them with
func TestTheNamesInTheBlockOfUnmetDependenciesAreNamesAndNothingElse(t *testing.T) {
	for _, label := range []string{"d11", "d12"} {
		for name, want := range map[string][]string{
			"unmet-held":    {"rvm-needs", "rvm-lib"},
			"unmet-missing": {"rvm-bad", "rvm-nothere"},
			"trivial":       nil,
			"nothing":       nil,
		} {
			sim, _ := realModuleCase(t, label, name)
			if got := UnmetNames(sim.Text); !slices.Equal(got, want) {
				t.Errorf("%s %s: %q, want %q", label, name, got, want)
			}
		}
	}
}

func TestTheNamesInTheBlockOfUnmetDependenciesAreReadFromTheBlockOnly(t *testing.T) {
	text := progressLines + `Some packages could not be installed.

The following packages have unmet dependencies:
 pkg-a : Depends: libfoo1 (>= 2.0) but 1.5 is to be installed or
                  libfoo2 but it is not going to be installed
         Breaks: libc6:i386 (< 2.36) but 2.31 is to be installed
 pkg-b : PreDepends: other-1.2 but it is not installable
 Pkg-C : Depends: UPPER
E: Unable to correct problems, you have held broken packages.
 libvirt0 is on a line that follows the end of the block.
libvirt0 and held are words of another line.
`
	want := []string{"pkg-a", "libfoo1", "libfoo2", "libc6", "pkg-b", "other-1.2"}
	if got := UnmetNames(text); !slices.Equal(got, want) {
		t.Errorf("%q, want %q", got, want)
	}
	for _, none := range []string{"", progressLines, "The following packages have unmet dependencies:\n", "pkg-a : Depends: x\n", "The following NEW packages will be installed:\n  pkg-a\n"} {
		if got := UnmetNames(none); len(got) != 0 {
			t.Errorf("%q: %q", none, got)
		}
	}
}

// solverOf is what ParseSolver reads of a simulation of an install, once the lists that an upgrade does not print are taken out.
func solverOf(t *testing.T, sim Output) *SolverRun {
	t.Helper()
	text, _, _, f := withoutInstallNotes(sim.Text)
	if f != nil {
		t.Fatal(f)
	}
	run, f := ParseSolver(Output{Text: text, ExitCode: sim.ExitCode})
	if f != nil {
		t.Fatal(f)
	}
	return run
}

// Every real estimate ends with status 0 and says what it would do: it is not the end of a refusal, as the estimate of the upgrade is, and the transaction it describes is the one of
// the simulation next to it.
func TestTheRealEstimatesEndWithStatusZero(t *testing.T) {
	for _, label := range []string{"d11", "d12"} {
		for _, name := range []string{"trivial", "nontrivial", "removal", "downgrade", "hold", "nothing", "mixed"} {
			sim, est := realModuleCase(t, label, name)
			if sim.ExitCode != 0 || est.ExitCode != 0 {
				t.Errorf("%s %s: status %d and %d", label, name, sim.ExitCode, est.ExitCode)
			}
			run := solverOf(t, sim)
			got := ParseURIEstimate(est, run.Summary)
			if !got.Known || got.DownloadBytes == nil || got.ExtraBytes == nil {
				t.Errorf("%s %s: sizes not known: %+v", label, name, got)
			}
			// a status that is not 0, or an end that is the old one, is not the end of this command
			bad := est
			bad.ExitCode = 100
			if ParseURIEstimate(bad, run.Summary).Known {
				t.Errorf("%s %s: sizes from a status 100", label, name)
			}
			// the same text next to the summary of another transaction is no answer
			other := run.Summary
			other.NotUpgraded++
			if ParseURIEstimate(est, other).Known {
				t.Errorf("%s %s: sizes for another transaction", label, name)
			}
		}
		// the sentences of the old estimate, as the upgrade has them, are not understood by this one and the reverse
		sim, est := realModuleCase(t, label, "nontrivial")
		run := solverOf(t, sim)
		old := Output{Text: strings.Replace(est.Text, "'http", "E: Trivial Only specified but this is not a trivial operation.\n'http", 1), ExitCode: 100}
		if ParseURIEstimate(old, run.Summary).Known {
			t.Error("the end of the old estimate is read by the new one")
		}
		if ParseEstimate(est, run.Summary, testDir).Known {
			t.Errorf("%s: the estimate of the upgrade reads a status 0 that has work in it", label)
		}
	}
}

// Why the argv is the one it is: what the earlier one, with the trivial-only option, did to the same box. It installed the package: the maintainer scripts ran, as root, with no word of
// "installed" in the answer of the plan, which asked for the sizes only. The estimate now leaves the box as it was.
func TestTheEstimateThatApt224AndApt261InstalledWithIsWhatTheNewOneIsNot(t *testing.T) {
	for _, label := range []string{"d11", "d12"} {
		old := realModule(t, label+"-trivial-trivialonly")
		if old.ExitCode != 0 || !strings.Contains(old.Text, "Unpacking rvm-solo (1.0)") || !strings.Contains(old.Text, "Setting up rvm-solo (1.0)") {
			t.Errorf("%s: the run with the trivial-only option: status %d\n%s", label, old.ExitCode, old.Text)
		}
		dpkg, err := os.ReadFile(filepath.Join("testdata", "realmodule", label+"-trivial-trivialonly-dpkg.txt"))
		if err != nil || !strings.HasPrefix(string(dpkg), "ii  rvm-solo") {
			t.Errorf("%s: dpkg after it: %q %v", label, dpkg, err)
		}
		// the new one, on the same box before that run: nothing installed, nothing unpacked, nothing set up
		_, est := realModuleCase(t, label, "trivial")
		for _, word := range []string{"Unpacking", "Setting up", "Selecting previously", "Preparing to unpack", "Get:"} {
			if strings.Contains(est.Text, word) {
				t.Errorf("%s: the estimate says %q", label, word)
			}
		}
		after, err := os.ReadFile(filepath.Join("testdata", "realmodule", label+"-dpkg-l-after.txt"))
		if err != nil || strings.Contains(string(after), "rvm-solo") {
			t.Errorf("%s: dpkg after the estimates: %q %v", label, after, err)
		}
	}
}

// The estimates at the edges that the real ones do not reach.
func TestTheEstimateByAddressesAtItsEdges(t *testing.T) {
	sum := Summary{New: 1}
	text := "Reading package lists...\nThe following NEW packages will be installed:\n  a\n0 upgraded, 1 newly installed, 0 to remove and 0 not upgraded.\n"
	need := "Need to get 5 kB of archives.\n"
	used := "After this operation, 7 kB of additional disk space will be used.\n"
	uri := "'http://x/a_1_all.deb' a_1_all.deb 5000 SHA256:00\n"
	known := func(o Output) bool { return ParseURIEstimate(o, sum).Known }
	if !known(Output{Text: text + need + used + uri}) {
		t.Fatal("the base case is not known")
	}
	if got := ParseURIEstimate(Output{Text: text + need + used + uri}, sum); *got.DownloadBytes != 5000 || *got.ExtraBytes != 7000 {
		t.Errorf("%d %d", *got.DownloadBytes, *got.ExtraBytes)
	}
	for name, o := range map[string]Output{
		"no addresses are needed":      {Text: text + need + used},
		"the sizes in the other order": {Text: text + used + need},
	} {
		if !known(o) {
			t.Errorf("%s: not known", name)
		}
	}
	for name, o := range map[string]Output{
		"a status of 100":           {Text: text + need + used + uri, ExitCode: 100},
		"a status of 1":             {Text: text + need + used + uri, ExitCode: 1},
		"a cut output":              {Text: text + need + used + uri, Truncated: true},
		"only the first line":       {Text: text + need},
		"only the second line":      {Text: text + used},
		"an error":                  {Text: text + need + used + "E: x\n"},
		"a warning on the room":     {Text: text + need + used + "E: You don't have enough free space in /var/tmp/casaos-os-plan.1/cache/archives/.\n"},
		"two summaries":             {Text: text + text + need + used},
		"no summary":                {Text: need + used + uri},
		"a summary for an upgrade":  {Text: strings.Replace(text, "1 newly installed", "1 upgraded, 0 newly installed", 1) + need + used},
		"a control character":       {Text: text + need + used + "\x1b[2J" + uri},
		"a line too long to be one": {Text: text + need + used + "'" + strings.Repeat("a", 5000) + "\n"},
		"an empty output":           {},
	} {
		if known(o) {
			t.Errorf("%s: known", name)
		}
	}
	// nothing to do is a status 0 with the summary of zeros, whatever else is there, and nothing to do is not sizes of a transaction that has something to do
	zero := Summary{}
	nothing := "x is already the newest version (1).\n0 upgraded, 0 newly installed, 0 to remove and 2 not upgraded.\n"
	if got := ParseURIEstimate(Output{Text: nothing}, Summary{NotUpgraded: 2}); !got.Known || *got.DownloadBytes != 0 || *got.ExtraBytes != 0 {
		t.Errorf("nothing to do: %+v", got)
	}
	if ParseURIEstimate(Output{Text: nothing}, zero).Known {
		t.Error("nothing to do for a summary that does not match")
	}
	if ParseURIEstimate(Output{Text: text}, sum).Known {
		t.Error("something to do and no sizes")
	}
	if ParseURIEstimate(Output{Text: nothing + "E: x\n"}, Summary{NotUpgraded: 2}).Known {
		t.Error("nothing to do and an error")
	}
}

// What apt says is a regular expression when no package has the name is not a package of the name: the names that were asked for must be the ones in the plan, word for word.
func TestEveryNameThatWasAskedForIsInThePlanWordForWord(t *testing.T) {
	sim := progressLines + "The following NEW packages will be installed:\n  modclean other\n0 upgraded, 2 newly installed, 0 to remove and 0 not upgraded.\nInst modclean (1.0 Debian:12 [all])\nInst other (1.0 Debian:12 [all])\nConf modclean (1.0 Debian:12 [all])\nConf other (1.0 Debian:12 [all])\n"
	for _, c := range []struct {
		name  string
		asked []string
		ok    bool
	}{
		{"both, as named", []string{"modclean", "other"}, true},
		{"one of them", []string{"other"}, true},
		{"a name with a dot that is read as a pattern", []string{"mod.lean", "other"}, false},
		{"a name with a plus that is read as a pattern", []string{"mod+clean"}, false},
		{"a name that is a package's by another name", []string{"virtual-thing"}, false},
		{"a name that is the start of the one that is installed", []string{"mod"}, false},
		{"a name that is the end of the one that is installed", []string{"clean"}, false},
		{"a name that is the one installed with something after it", []string{"modclean2"}, false},
		{"one that is right and one that is not", []string{"other", "mod.lean"}, false},
	} {
		res := ParseModuleSimulation(out(sim, 0), out("", 0), c.asked)
		switch {
		case c.ok && res.Failure != "":
			t.Errorf("%s: %q %s", c.name, res.Failure, res.Detail)
		case !c.ok && res.Failure != "package_not_exact":
			t.Errorf("%s: %q %s", c.name, res.Failure, res.Detail)
		case !c.ok && (len(res.ToInstall) != 0 || res.Known):
			t.Errorf("%s: an answer with the refusal: %+v", c.name, res)
		}
	}
	// the name that is already the newest counts as there, and so does one that is upgraded and one that is downgraded
	newest := progressLines + "pkg-a is already the newest version (1.0).\n0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"
	if res := ParseModuleSimulation(out(newest, 0), out(newest, 0), []string{"pkg-a"}); res.Failure != "" {
		t.Errorf("already the newest: %q", res.Failure)
	}
	if res := ParseModuleSimulation(out(newest, 0), out(newest, 0), []string{"pkg.a"}); res.Failure != "package_not_exact" {
		t.Errorf("already the newest, by a pattern: %q", res.Failure)
	}
	up := progressLines + "The following packages will be upgraded:\n  pkg-a\n1 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\nInst pkg-a [1.0] (2.0 Debian:12 [all])\nConf pkg-a (2.0 Debian:12 [all])\n"
	if res := ParseModuleSimulation(out(up, 0), out("", 0), []string{"pkg-a"}); res.Failure != "" {
		t.Errorf("upgraded: %q", res.Failure)
	}
	if res := ParseModuleSimulation(out(up, 0), out("", 0), []string{"pkg-b"}); res.Failure != "package_not_exact" {
		t.Errorf("upgraded, and another name: %q", res.Failure)
	}
	// a package that is only removed is not one that was installed
	rm := progressLines + "The following packages will be REMOVED:\n  pkg-a\n0 upgraded, 0 newly installed, 1 to remove and 0 not upgraded.\nRemv pkg-a [1.0]\n"
	if res := ParseModuleSimulation(out(rm, 0), out("", 0), []string{"pkg-a"}); res.Failure != "package_not_exact" {
		t.Errorf("only removed: %q", res.Failure)
	}
	// a list that is not a list of packages is no list to ask about
	for _, bad := range [][]string{nil, {}, {"Pkg"}, {"pkg-a", "pkg-a"}, {"-y"}} {
		if res := ParseModuleSimulation(out(newest, 0), out(newest, 0), bad); res.Failure != "apt_failed" {
			t.Errorf("the list %q: %q", bad, res.Failure)
		}
	}
	// the detail says which names, and as text that is safe to show
	res := ParseModuleSimulation(out(sim, 0), out("", 0), []string{"mod.lean", "mod+clean", "other"})
	if res.Detail != "package_not_exact: mod.lean mod+clean" {
		t.Errorf("detail %q", res.Detail)
	}
	if osmodule.RefusedPackageNotExact != "package_not_exact" {
		t.Error("the code")
	}
}

// When a package cannot be installed because of a hold or a conflict, apt says it in one line that is no help, and the block above it names what could not be met.
func TestTheUnmetDependenciesOfAptAreInTheDetailOfTheFailure(t *testing.T) {
	block := "Some packages could not be installed. This may mean that you have\nrequested an impossible situation.\nThe following information may help to resolve the situation:\n\n" +
		"The following packages have unmet dependencies:\n pkg-a : Depends: pkg-b (>= 2) but 1.0 is to be installed\n         Depends: pkg-c but it is not installable\n pkg-d : Conflicts: pkg-e\n"
	end := "E: Unable to correct problems, you have held broken packages.\n"
	res := ParseModuleSimulation(out(progressLines+block+end, 100), out("", 0), []string{"pkg-a"})
	want := "unmet dependencies: pkg-a : Depends: pkg-b (>= 2) but 1.0 is to be installed; Depends: pkg-c but it is not installable; pkg-d : Conflicts: pkg-e (sandbox_solver_failed: E: Unable to correct problems, you have held broken packages.)"
	if res.Failure != "apt_failed" || res.Detail != want {
		t.Errorf("failure %q\n detail %q\n   want %q", res.Failure, res.Detail, want)
	}
	// the cause is first, so that it is seen where the detail is cut
	if !strings.HasPrefix(res.Detail, "unmet dependencies: pkg-a") {
		t.Errorf("%q", res.Detail)
	}
	// no block, no words about one
	res = ParseModuleSimulation(out(progressLines+end, 100), out("", 0), []string{"pkg-a"})
	if res.Failure != "apt_failed" || strings.Contains(res.Detail, "unmet") {
		t.Errorf("%+v", res)
	}
	// a block that is only the header, and one that goes on for ever, are bounded and safe to show
	many := "The following packages have unmet dependencies:\n" + strings.Repeat(" pkg-a : Depends: \x1b[31m"+strings.Repeat("x", 500)+"\n", 40)
	res = ParseModuleSimulation(out(progressLines+many+end, 100), out("", 0), []string{"pkg-a"})
	if res.Failure != "apt_failed" || strings.Count(res.Detail, "pkg-a : Depends:") != maxDetailLines || strings.ContainsFunc(res.Detail, func(r rune) bool { return r < ' ' || r > '~' }) {
		t.Errorf("%d lines, %q", strings.Count(res.Detail, "pkg-a : Depends:"), res.Detail)
	}
	if len(res.Detail) > maxDetailLines*(maxDetailChars+2)+200 {
		t.Errorf("a detail of %d characters", len(res.Detail))
	}
	res = ParseModuleSimulation(out(progressLines+"The following packages have unmet dependencies:\n"+end, 100), out("", 0), []string{"pkg-a"})
	if res.Failure != "apt_failed" || strings.Contains(res.Detail, "unmet") {
		t.Errorf("an empty block: %q", res.Detail)
	}
	// a line of the block that is not indented is not a line of the block
	res = ParseModuleSimulation(out(progressLines+"The following packages have unmet dependencies:\n pkg-a : Depends: x\nnot indented\n pkg-b : y\n"+end, 100), out("", 0), []string{"pkg-a"})
	if strings.Contains(res.Detail, "pkg-b") || !strings.Contains(res.Detail, "pkg-a : Depends: x") {
		t.Errorf("%q", res.Detail)
	}
	// ...and a failure that is not about packages does not borrow one: the block of a run that ended with status 0, and went on to say something that is not understood
	res = ParseModuleSimulation(out(progressLines+block+"Everything is fine.\n", 0), out("", 0), []string{"pkg-a"})
	if res.Failure != "apt_failed" || strings.Contains(res.Detail, "unmet") || !strings.Contains(res.Detail, "unknown_line") {
		t.Errorf("%+v", res)
	}
	res = ParseModuleSimulation(out(progressLines+block, 100), out("", 0), []string{"pkg-a"})
	if res.Failure != "apt_failed" {
		t.Errorf("%+v", res)
	}
}
