package osplan

import (
	"slices"
	"strings"
	"testing"
)

func realOutput(t *testing.T, name string) Output {
	t.Helper()
	rc := strings.TrimSpace(realFile(t, strings.TrimSuffix(name, ".txt")+".rc"))
	return Output{Text: realFile(t, name), ExitCode: map[string]int{"0": 0, "100": 100}[rc]}
}

// estimateOutput is the shape of what apt prints for the Trivial-Only dist-upgrade of the measured box, with the sentences the
// test gives it: the lists and the summary of the real dist-upgrade of the measurement, the two sentences ("Need to get 0 B/140
// MB of archives." and "After this operation, 300 MB of additional disk space will be used." are the form that the hop's own log
// has, step-full.log of the measurement, with sizes of this test's own) and the error that ends it. The guest of the measurement
// was never asked the Trivial-Only run: the REAL output of it is apt 2.2.4's, from a Debian 11 chroot taken to bookworm with its
// real lists, kept as testdata/real/chroot-d11-12-size.txt (TestTheEstimateOfARealTrivialOnlyRunOfApt224).
func estimateOutput(t *testing.T, need, after string) Output {
	t.Helper()
	real := realFile(t, "sandbox-paused-dist-upgrade.txt")
	head, _, _ := strings.Cut(real, "384 upgraded, 75 newly installed, 2 to remove and 0 not upgraded.\n")
	return Output{
		Text:     head + "384 upgraded, 75 newly installed, 2 to remove and 0 not upgraded.\n" + need + "\n" + after + "\n" + trivialOnlyError + "\n",
		ExitCode: 100,
	}
}

func TestTheRealUpdateOfTheMeasurementIsClean(t *testing.T) {
	for _, name := range []string{"sandbox-paused-update.txt", "sandbox-kept-update.txt"} {
		if f := ParseUpdate(realOutput(t, name)); f != nil {
			t.Errorf("%s: %v", name, f)
		}
	}
}

func TestTheRealUpgradeIsParsedAsTheMeasurementSawIt(t *testing.T) {
	run, f := ParseSolver(realOutput(t, "sandbox-paused-upgrade.txt"))
	if f != nil {
		t.Fatal(f)
	}
	if run.Summary != (Summary{Upgraded: 274, New: 0, Remove: 0, NotUpgraded: 111}) {
		t.Errorf("summary = %+v", run.Summary)
	}
	if len(run.Upgraded) != 274 || len(run.New) != 0 || len(run.Removed) != 0 || len(run.KeptBack) != 111 {
		t.Errorf("lists = %d, %d, %d, %d", len(run.Upgraded), len(run.New), len(run.Removed), len(run.KeptBack))
	}
	if !slices.Equal(run.AutoRemovable, []string{"libdns-export1110", "libisc-export1105"}) {
		t.Errorf("autoremovable = %v", run.AutoRemovable)
	}
	if !slices.Contains(run.KeptBack, "apt") || !slices.Contains(run.KeptBack, "systemd") || !slices.Contains(run.Upgraded, "bash") {
		t.Error("the lists lack apt, systemd or bash")
	}
	if len(run.Removals()) != 0 {
		t.Errorf("removals = %v", run.Removals())
	}
}

func TestTheRealDistUpgradeRemovesFuseAndLibsemanageAndNotCron(t *testing.T) {
	run, f := ParseSolver(realOutput(t, "sandbox-paused-dist-upgrade.txt"))
	if f != nil {
		t.Fatal(f)
	}
	if run.Summary != (Summary{Upgraded: 384, New: 75, Remove: 2, NotUpgraded: 0}) {
		t.Errorf("summary = %+v", run.Summary)
	}
	if got := run.Removals(); !slices.Equal(got, []string{"fuse", "libsemanage1"}) {
		t.Errorf("removals = %v", got)
	}
	// apt writes "Remv cron" and then "Inst cron" to order a replacement: cron is upgraded, not removed
	if slices.Contains(run.Removals(), "cron") || !slices.Contains(run.Upgraded, "cron") {
		t.Error("cron is taken for a removal")
	}
	if len(run.New) != 75 || len(run.Upgraded) != 384 || len(run.KeptBack) != 0 {
		t.Errorf("lists = %d new, %d upgraded, %d kept back", len(run.New), len(run.Upgraded), len(run.KeptBack))
	}
	var base *InstLine
	for i := range run.Inst {
		if run.Inst[i].Name == "base-files" {
			base = &run.Inst[i]
		}
	}
	if base == nil || base.From != "11.1+deb11u11" || base.To != "12.4+deb12u15" || base.Tag != "Debian:12.15/oldstable" {
		t.Errorf("base-files = %+v", base)
	}
	for _, in := range run.Inst {
		switch in.Name {
		case "libsemanage2":
			if in.From != "" || in.To != "3.4-1+b5" {
				t.Errorf("libsemanage2 = %+v", in)
			}
		case "python3-urllib3":
			if in.Tag != "Debian:12.15/oldstable, Debian-Security:12/oldstable-security" {
				t.Errorf("python3-urllib3 = %+v", in)
			}
		}
	}
}

func TestTheRealRunsWithDockerKeptParseToo(t *testing.T) {
	up, f := ParseSolver(realOutput(t, "sandbox-kept-upgrade.txt"))
	if f != nil || up.Summary != (Summary{Upgraded: 277, NotUpgraded: 112}) {
		t.Fatalf("upgrade: %v, %+v", f, up)
	}
	dist, f := ParseSolver(realOutput(t, "sandbox-kept-dist-upgrade.txt"))
	if f != nil || dist.Summary != (Summary{Upgraded: 388, New: 77, Remove: 2}) || !slices.Equal(dist.Removals(), []string{"fuse", "libsemanage1"}) {
		t.Fatalf("dist-upgrade: %v, %+v", f, dist)
	}
}

// pieces of an output that parses, to break one at a time
const headLines = "Reading package lists...\nBuilding dependency tree...\nReading state information...\nCalculating upgrade...\n"

func solver(body string) Output { return Output{Text: headLines + body} }

func TestAnOutputThatParsesAndWhatBreaksIt(t *testing.T) {
	good := "The following packages will be upgraded:\n  bash curl\n2 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\nInst bash [5.1-2] (5.2.15-2+b8 Debian:12.15/oldstable [amd64])\nConf bash (5.2.15-2+b8 Debian:12.15/oldstable [amd64])\nInst curl [7.74.0-1.3+deb11u14] (7.88.1-10+deb12u14 Debian:12.15/oldstable [amd64])\nConf curl (7.88.1-10+deb12u14 Debian:12.15/oldstable [amd64])\n"
	if _, f := ParseSolver(solver(good)); f != nil {
		t.Fatalf("the good one: %v", f)
	}
	for name, tc := range map[string]struct {
		out    Output
		code   string
		reason string
	}{
		"no summary":                              {solver("The following packages will be upgraded:\n  bash curl\n"), CodePlanUnparsed, "no_summary"},
		"a summary twice":                         {solver(good + "2 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"), CodePlanUnparsed, "duplicate_summary"},
		"a summary that disagrees with the list":  {solver(strings.Replace(good, "2 upgraded", "3 upgraded", 1)), CodePlanUnparsed, "summary_mismatch"},
		"a count of removals with no section":     {solver(strings.Replace(good, "0 to remove", "1 to remove", 1)), CodePlanUnparsed, "summary_mismatch"},
		"a count of new with no section":          {solver(strings.Replace(good, "0 newly", "4 newly", 1)), CodePlanUnparsed, "summary_mismatch"},
		"a kept back list longer than its count":  {solver(good + "The following packages have been kept back:\n  vim\n"), CodePlanUnparsed, "summary_mismatch"},
		"a count of downgraded with no section":   {solver(strings.Replace(good, "newly installed, 0 to", "newly installed, 2 downgraded, 0 to", 1)), CodePlanUnparsed, "summary_mismatch"},
		"a section twice":                         {solver("The following packages will be upgraded:\n  bash\nThe following packages will be upgraded:\n  curl\n2 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"), CodePlanUnparsed, "duplicate_section"},
		"the removed section twice":               {solver("The following packages will be REMOVED:\n  a\nThe following packages will be REMOVED:\n  b\n0 upgraded, 0 newly installed, 2 to remove and 0 not upgraded.\n"), CodePlanUnparsed, "duplicate_section"},
		"a section that is not known":             {solver("The following packages will be REINSTALLED:\n  bash\n0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"), CodePlanUnparsed, "unknown_line"},
		"a downgraded section the summary lacks":  {solver("The following packages will be DOWNGRADED:\n  bash\n0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\nInst bash [2] (1 x [amd64])\n"), CodePlanUnparsed, "summary_mismatch"},
		"a downgraded name with no Inst line":     {solver("The following packages will be DOWNGRADED:\n  bash\n0 upgraded, 0 newly installed, 1 downgraded, 0 to remove and 0 not upgraded.\n"), CodePlanUnparsed, "inst_mismatch"},
		"a reinstalled count":                     {solver("0 upgraded, 0 newly installed, 1 reinstalled, 0 to remove and 0 not upgraded.\n"), CodePlanUnparsed, "unknown_line"},
		"a name that is not a package's":          {solver("The following packages will be upgraded:\n  bash Curl\n2 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"), CodePlanUnparsed, "bad_name"},
		"a name with a star in the middle":        {solver("The following packages will be upgraded:\n  ba*sh\n1 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"), CodePlanUnparsed, "bad_name"},
		"a name that starts with a dash":          {solver("The following packages will be upgraded:\n  -y\n1 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"), CodePlanUnparsed, "bad_name"},
		"a name with a control character":         {solver("The following packages will be upgraded:\n  ba\x01sh\n1 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"), CodePlanUnparsed, "control_character"},
		"a NUL":                                   {solver("The following packages will be upgraded:\n  bash\x00\n1 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"), CodePlanUnparsed, "control_character"},
		"a line that is too long":                 {solver("The following packages will be upgraded:\n  " + strings.Repeat("a", 5000) + "\n"), CodePlanUnparsed, "line_too_long"},
		"a line that nobody knows":                {solver(good + "Something else happened.\n"), CodePlanUnparsed, "unknown_line"},
		"the summary of apt 2.7":                  {solver("Summary:\n  Upgrading: 2, Installing: 0, Removing: 0, Not Upgrading: 0\n"), CodePlanUnparsed, "unknown_line"},
		"downgrades of another list":              {solver("The following packages will be upgraded:\n  bash\n1 upgraded, 0 newly installed, 1 downgraded, 0 to remove and 0 not upgraded.\n"), CodePlanUnparsed, "summary_mismatch"},
		"an Inst line that is not one":            {solver(good + "Inst bash [5.1-2 (5.2)\n"), CodePlanUnparsed, "bad_inst_line"},
		"an Inst of a package that is in no list": {solver(good + "Inst vim [1] (2 Debian:12.15/oldstable [amd64])\n"), CodePlanUnparsed, "inst_mismatch"},
		"a list without its Inst lines":           {solver("The following packages will be upgraded:\n  bash\n1 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"), CodePlanUnparsed, "inst_mismatch"},
		"a Remv that is not in the removed list":  {solver(good + "Remv vim [2]\n"), CodePlanUnparsed, "removal_mismatch"},
		"a removed list that has no Remv":         {solver("The following packages will be REMOVED:\n  vim\n0 upgraded, 0 newly installed, 1 to remove and 0 not upgraded.\n"), CodePlanUnparsed, "removal_mismatch"},
		"a cut output":                            {Output{Text: solver(good).Text, Truncated: true}, CodePlanUnparsed, "truncated"},
		"an E: line":                              {solver(good + "E: Unable to correct problems, you have held broken packages.\n"), CodeSandboxSolverFailed, ""},
		"an E: line first":                        {Output{Text: "E: The list of sources could not be read.\n"}, CodeSandboxSolverFailed, ""},
		"an exit status":                          {Output{Text: solver(good).Text, ExitCode: 100}, CodeSandboxSolverFailed, ""},
		"an E: line in a cut output":              {Output{Text: "E: Broken.\n", Truncated: true}, CodeSandboxSolverFailed, ""},
	} {
		_, f := ParseSolver(tc.out)
		if f == nil {
			t.Errorf("%s: parsed", name)
			continue
		}
		if f.Code != tc.code || tc.reason != "" && f.Data["reason"] != tc.reason {
			t.Errorf("%s: %s %v, want %s %s", name, f.Code, f.Data["reason"], tc.code, tc.reason)
		}
	}
}

func TestTooManyNamesAreNotParsed(t *testing.T) {
	var b strings.Builder
	b.WriteString("The following packages will be upgraded:\n")
	for i := 0; i < maxNames/5+10; i++ {
		b.WriteString("  a" + strings.Repeat("b", 3) + " c1 c2 c3 c4\n")
	}
	if _, f := ParseSolver(solver(b.String())); f == nil || f.Data["reason"] != "too_many_names" {
		t.Fatalf("failure = %v", f)
	}
}

func TestAPlanThatIsFiftyMegabytesIsNotParsed(t *testing.T) {
	// the seam cuts the output at MaxOutputBytes and says so: what the parser gets is a cut output
	out := Output{Text: strings.Repeat("The following packages will be upgraded:\n", 1000), Truncated: true}
	if _, f := ParseSolver(out); f == nil || f.Code != CodePlanUnparsed {
		t.Fatalf("failure = %v", f)
	}
	if f := ParseUpdate(Output{Text: "Get:1 http://x InRelease\n", Truncated: true}); f == nil || f.Code != CodePlanUnparsed {
		t.Fatalf("update failure = %v", f)
	}
}

func TestTheStarOfAPurgedPackageIsInTheRemovedList(t *testing.T) {
	out := solver("The following packages will be REMOVED:\n  fuse* libsemanage1\n0 upgraded, 0 newly installed, 2 to remove and 0 not upgraded.\nPurg fuse [2.9.9-5]\nRemv libsemanage1 [3.1-1+b2]\n")
	run, f := ParseSolver(out)
	if f != nil {
		t.Fatal(f)
	}
	if !slices.Equal(run.Removals(), []string{"fuse", "libsemanage1"}) {
		t.Errorf("removals = %v: the star is not part of the name", run.Removals())
	}
	// a star anywhere but in the removed list is not apt's
	if _, f := ParseSolver(solver("The following packages will be upgraded:\n  bash*\n1 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\nInst bash [1] (2 x [amd64])\n")); f == nil {
		t.Error("a star in the upgraded list")
	}
}

func TestTheTerminalProgressOfAptAndItsNoteDoNotBreakTheParser(t *testing.T) {
	text := "Reading package lists... 0%\rReading package lists... 100%\rReading package lists... Done\nBuilding dependency tree... Done\nReading state information... Done\nCalculating upgrade... Done\n" +
		"NOTE: This is only a simulation!\n      apt-get needs root privileges for real execution.\n      Keep also in mind that locking is deactivated,\n      so don't depend on the relevance to the real current situation!\n" +
		"The following packages will be upgraded:\n  bash\n1 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\nInst bash [1] (2 Debian:12.15/oldstable [amd64])\nConf bash (2 Debian:12.15/oldstable [amd64])\n"
	if _, f := ParseSolver(Output{Text: text}); f != nil {
		t.Fatal(f)
	}
}

func TestTheFailureOfAnUpdateSaysWhatWentWrongInAFewSafeLines(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		exit       int
		kind       string
	}{
		{"a key that is missing", "Get:1 http://deb.debian.org/debian bookworm InRelease [151 kB]\nErr:1 http://deb.debian.org/debian bookworm InRelease\n  The following signatures couldn't be verified because the public key is not available: NO_PUBKEY 0E98404D386FA1D9\nW: GPG error: http://deb.debian.org/debian bookworm InRelease: The following signatures couldn't be verified because the public key is not available: NO_PUBKEY 0E98404D386FA1D9\nE: The repository 'http://deb.debian.org/debian bookworm InRelease' is not signed.\n", 100, KindNoPubkey},
		{"a suite that is not there", "Err:2 http://deb.debian.org/debian bullseye-backports InRelease\n  404  Not Found [IP: 151.101.2.132 80]\nE: The repository 'http://deb.debian.org/debian bullseye-backports Release' does not have a Release file.\n", 100, KindNotFound},
		{"a host that does not exist", "Err:1 http://deb.example.invalid/debian bookworm InRelease\n  Could not resolve 'deb.example.invalid'\nW: Failed to fetch http://deb.example.invalid/debian/dists/bookworm/InRelease  Could not resolve 'deb.example.invalid'\nE: Some index files failed to download. They have been ignored, or old ones used instead.\n", 100, KindNetwork},
		{"a network that is down", "Err:1 http://deb.debian.org/debian bookworm InRelease\n  Temporary failure resolving 'deb.debian.org'\nE: Failed.\n", 100, KindNetwork},
		{"a connection that fails", "Err:1 http://x.example/debian bookworm InRelease\n  Connection failed [IP: 1.2.3.4 80]\n", 100, KindNetwork},
		{"a release that changed", "E: Repository 'http://deb.debian.org/debian bookworm InRelease' changed its 'Version' value from '12.14' to '12.15'\nN: This must be accepted explicitly before updates for this repository can be applied.\n", 100, KindReleaseChange},
		{"a release file that is not there", "E: The repository 'http://x.example/debian bookworm Release' does not have a Release file.\n", 100, KindReleaseChange},
		{"something else", "E: Could not open file /x - open (2: No such file or directory)\n", 100, KindOther},
		{"a warning with a good status", "Reading package lists...\nW: Skipping acquire of configured file 'main/binary-i386/Packages' as repository doesn't support architecture 'i386'\n", 0, KindOther},
		{"a status and no word", "Reading package lists...\n", 100, KindOther},
	} {
		f := ParseUpdate(Output{Text: tc.text, ExitCode: tc.exit})
		if f == nil || f.Code != CodeSandboxUpdateFailed || f.Data["kind"] != tc.kind {
			t.Errorf("%s: %v", tc.name, f)
			continue
		}
		lines := strings.Split(f.Detail, "\n")
		if len(lines) > 6 {
			t.Errorf("%s: %d lines", tc.name, len(lines))
		}
		for _, l := range lines {
			if len(l) > 200 || l == "" {
				t.Errorf("%s: line %q", tc.name, l)
			}
			for i := 0; i < len(l); i++ {
				if l[i] < 0x20 || l[i] > 0x7e {
					t.Errorf("%s: %q is not printable ASCII", tc.name, l)
				}
			}
			if strings.Contains(l, "/debian/dists") || strings.Contains(l, "151.101") && strings.Contains(l, "http://deb.debian.org/") {
				t.Errorf("%s: a URL with its path: %q", tc.name, l)
			}
		}
	}
}

func TestADetailKeepsOnlyTheSchemeAndTheHostOfAnAddress(t *testing.T) {
	f := ParseUpdate(Output{Text: "E: Failed to fetch https://user:pw@secret.example.com/path/to/x?token=abc123#frag  404  Not Found\nW: accents é and \x01 and \xff\n" + strings.Repeat("E: x\n", 20), ExitCode: 100})
	if f == nil {
		t.Fatal("no failure")
	}
	if strings.Contains(f.Detail, "user") || strings.Contains(f.Detail, "pw") || strings.Contains(f.Detail, "token") || strings.Contains(f.Detail, "/path") || strings.Contains(f.Detail, "abc123") {
		t.Errorf("detail = %q", f.Detail)
	}
	if !strings.Contains(f.Detail, "https://secret.example.com") {
		t.Errorf("detail = %q", f.Detail)
	}
	if n := len(strings.Split(f.Detail, "\n")); n != 6 {
		t.Errorf("%d lines", n)
	}
	long := ParseUpdate(Output{Text: "E: " + strings.Repeat("y", 900) + "\n", ExitCode: 100})
	if len(long.Detail) != 200 || !strings.HasSuffix(long.Detail, "...") {
		t.Errorf("detail of %d bytes", len(long.Detail))
	}
}

func TestTheEstimateReadsTheSentencesOfApt(t *testing.T) {
	summary := Summary{Upgraded: 384, New: 75, Remove: 2}
	for _, tc := range []struct {
		name, need, after string
		download, extra   int64
	}{
		{"the form of the measured hop", "Need to get 0 B/140 MB of archives.", "After this operation, 300 MB of additional disk space will be used.", 140e6, 300e6},
		{"the whole at once", "Need to get 182 MB of archives.", "After this operation, 480 MB of additional disk space will be used.", 182e6, 480e6},
		{"with a decimal", "Need to get 56.8 MB of archives.", "After this operation, 12.9 MB of additional disk space will be used.", 56.8e6, 12.9e6},
		{"in kilobytes", "Need to get 9492 kB of archives.", "After this operation, 3288 B of additional disk space will be used.", 9492e3, 3288},
		{"in gigabytes", "Need to get 1.5 GB of archives.", "After this operation, 2.4 GB of additional disk space will be used.", 1.5e9, 2.4e9},
		{"with a thousands separator", "Need to get 1,234 kB/2,345 kB of archives.", "After this operation, 1,000 MB of additional disk space will be used.", 2345e3, 1000e6},
		{"some already fetched", "Need to get 40.0 kB/140 MB of archives.", "After this operation, 300 MB of additional disk space will be used.", 140e6, 300e6},
		{"space that is freed", "Need to get 10.2 MB of archives.", "After this operation, 25.1 MB disk space will be freed.", 10.2e6, -25.1e6},
		{"nothing to fetch", "Need to get 0 B of archives.", "After this operation, 0 B of additional disk space will be used.", 0, 0},
	} {
		est := ParseEstimate(estimateOutput(t, tc.need, tc.after), summary, testDir)
		if !est.Known || *est.DownloadBytes != tc.download || *est.ExtraBytes != tc.extra {
			t.Errorf("%s: %+v (%v, %v)", tc.name, est, est.DownloadBytes, est.ExtraBytes)
		}
	}
}

func TestTheEstimateIsUnknownForAnythingElse(t *testing.T) {
	summary := Summary{Upgraded: 384, New: 75, Remove: 2}
	need, after := "Need to get 140 MB of archives.", "After this operation, 300 MB of additional disk space will be used."
	good := estimateOutput(t, need, after)
	if !ParseEstimate(good, summary, testDir).Known {
		t.Fatal("the good one is not known")
	}
	with := func(f func(Output) Output) Output { return f(good) }
	for name, out := range map[string]Output{
		"another exit status": with(func(o Output) Output { o.ExitCode = 0; return o }),
		"exit 1":              with(func(o Output) Output { o.ExitCode = 1; return o }),
		"a cut output":        with(func(o Output) Output { o.Truncated = true; return o }),
		"no error line":       with(func(o Output) Output { o.Text = strings.Replace(o.Text, trivialOnlyError+"\n", "", 1); return o }),
		"another error":       with(func(o Output) Output { o.Text += "E: Something else.\n"; return o }),
		"another error alone": with(func(o Output) Output {
			o.Text = strings.Replace(o.Text, trivialOnlyError, "E: Broken packages", 1)
			return o
		}),
		"no need line":               with(func(o Output) Output { o.Text = strings.Replace(o.Text, need+"\n", "", 1); return o }),
		"no after line":              with(func(o Output) Output { o.Text = strings.Replace(o.Text, after+"\n", "", 1); return o }),
		"the need line twice":        with(func(o Output) Output { o.Text = strings.Replace(o.Text, need+"\n", need+"\n"+need+"\n", 1); return o }),
		"another sentence":           estimateOutput(t, "Need to get lots of archives.", after),
		"a unit that is not apt's":   estimateOutput(t, "Need to get 140 MiB of archives.", after),
		"a size with a sign":         estimateOutput(t, "Need to get -140 MB of archives.", after),
		"the after of another shape": estimateOutput(t, need, "After this operation, 300 MB will be used."),
		"a size that is not one":     estimateOutput(t, "Need to get 1e9 MB of archives.", after),
		"a size that is absurd":      estimateOutput(t, "Need to get 99999 TB of archives.", after),
		"a control character":        with(func(o Output) Output { o.Text += "\x01"; return o }),
		"no summary": with(func(o Output) Output {
			o.Text = strings.Replace(o.Text, "384 upgraded, 75 newly installed, 2 to remove and 0 not upgraded.\n", "", 1)
			return o
		}),
		"another transaction": with(func(o Output) Output { o.Text = strings.Replace(o.Text, "384 upgraded", "383 upgraded", 1); return o }),
		// the second normal end is the sandbox's own cache and nothing else (TestTheEstimateThatApt224EndsInLowRoomIsStillAnEstimate)
		"the room of another directory": with(func(o Output) Output {
			o.Text = strings.Replace(o.Text, trivialOnlyError, "E: You don't have enough free space in /var/cache/apt/archives/.", 1)
			return o
		}),
		"the room of another sandbox": with(func(o Output) Output {
			o.Text = strings.Replace(o.Text, trivialOnlyError, lowRoomError("/var/tmp/casaos-os-plan.999"), 1)
			return o
		}),
		"the room of the lists of the sandbox": with(func(o Output) Output {
			o.Text = strings.Replace(o.Text, trivialOnlyError, "E: You don't have enough free space in "+testDir+"/lists/.", 1)
			return o
		}),
		"the room of the sandbox, and the trivial end": with(func(o Output) Output { o.Text += lowRoomError(testDir) + "\n"; return o }),
		"the room of the sandbox twice": with(func(o Output) Output {
			o.Text = strings.Replace(o.Text, trivialOnlyError, lowRoomError(testDir)+"\n"+lowRoomError(testDir), 1)
			return o
		}),
		"the room of the sandbox, with another status": with(func(o Output) Output {
			o.Text, o.ExitCode = strings.Replace(o.Text, trivialOnlyError, lowRoomError(testDir), 1), 1
			return o
		}),
		"the room of the sandbox, with the sentence cut": with(func(o Output) Output {
			o.Text = strings.Replace(o.Text, trivialOnlyError, strings.TrimSuffix(lowRoomError(testDir), "."), 1)
			return o
		}),
		"the room of the sandbox, but no sizes": with(func(o Output) Output {
			o.Text = strings.Replace(strings.Replace(o.Text, trivialOnlyError, lowRoomError(testDir), 1), need+"\n", "", 1)
			return o
		}),
	} {
		if est := ParseEstimate(out, summary, testDir); est.Known || est.DownloadBytes != nil || est.ExtraBytes != nil {
			t.Errorf("%s: known %+v", name, est)
		}
	}
	if (ParseEstimate(Output{}, summary, testDir) != Estimates{}) {
		t.Error("an empty output is known")
	}
}

// The real text of the estimate: apt 2.2.4 in a Debian 11 chroot (92 packages, taken to bookworm with the real lists of the
// mirror), asked the Trivial-Only dist-upgrade through the argv of the plan, with the environment of the sandbox. It ends with
// status 100 and its own error, after the two sentences; it is the output of the same transaction as the dist-upgrade of the
// same chroot, whose summary it repeats.
func TestTheEstimateOfARealTrivialOnlyRunOfApt224(t *testing.T) {
	dist, f := ParseSolver(realOutput(t, "chroot-d11-12-dist-upgrade.txt"))
	if f != nil {
		t.Fatal(f)
	}
	if dist.Summary != (Summary{Upgraded: 92, New: 19, Remove: 1, NotUpgraded: 0}) {
		t.Fatalf("summary = %+v", dist.Summary)
	}
	size := realOutput(t, "chroot-d11-12-size.txt")
	if size.ExitCode != 100 || !strings.HasSuffix(size.Text, "\nE: Trivial Only specified but this is not a trivial operation.\n") {
		t.Fatalf("the real run did not end as the plan expects: %d, %q", size.ExitCode, size.Text[max(0, len(size.Text)-120):])
	}
	est := ParseEstimate(size, dist.Summary, testDir)
	if !est.Known || est.DownloadBytes == nil || est.ExtraBytes == nil || *est.DownloadBytes != 44_300_000 || *est.ExtraBytes != 56_500_000 {
		t.Errorf("estimate = %+v (%v, %v): apt said 44.3 MB to get and 56.5 MB of additional space", est, est.DownloadBytes, est.ExtraBytes)
	}
	// the same text for another transaction is not its estimate
	if est := ParseEstimate(size, Summary{Upgraded: 93, New: 19, Remove: 1}, testDir); est.Known {
		t.Errorf("an estimate for another transaction: %+v", est)
	}
}

// The real text of the estimate when the file system of the sandbox's cache has less room than the download: apt 2.2.4 in the same Debian 11 chroot, with a tmpfs of 20 MB on the sandbox's cache/archives, checks the room after it
// printed the two sentences and before it stops at the trivial operation, and ends with its own line, status 100. The sizes are
// the same as the trivial end's, and they are known. (The same chroot, the same lists: its simulation is the one of
// chroot-d11-12-dist-upgrade.txt.)
func TestTheEstimateThatApt224EndsInLowRoomIsStillAnEstimate(t *testing.T) {
	dist, f := ParseSolver(realOutput(t, "chroot-d11-12-dist-upgrade.txt"))
	if f != nil {
		t.Fatal(f)
	}
	const dir = "/var/tmp/casaos-os-plan.LowRoom"
	size := realOutput(t, "chroot-d11-12-lowroom-size.txt")
	if size.ExitCode != 100 || !strings.HasSuffix(size.Text, "\n"+lowRoomError(dir)+"\n") || strings.Contains(size.Text, trivialOnlyError) {
		t.Fatalf("the real run did not end as the plan expects: %d, %q", size.ExitCode, size.Text[max(0, len(size.Text)-120):])
	}
	est := ParseEstimate(size, dist.Summary, dir)
	if !est.Known || est.DownloadBytes == nil || est.ExtraBytes == nil || *est.DownloadBytes != 44_300_000 || *est.ExtraBytes != 56_600_000 {
		t.Errorf("estimate = %+v (%v, %v): apt said 44.3 MB to get and 56.6 MB of additional space", est, est.DownloadBytes, est.ExtraBytes)
	}
	// it is the line of THIS sandbox's cache: the directory is part of it
	if est := ParseEstimate(size, dist.Summary, testDir); est.Known {
		t.Errorf("the room of another sandbox's cache: %+v", est)
	}
	if est := ParseEstimate(size, Summary{Upgraded: 93, New: 19, Remove: 1}, dir); est.Known {
		t.Errorf("an estimate for another transaction: %+v", est)
	}
}

// The real text of the estimate of a box with nothing to do: apt 2.2.4 in a Debian 11 chroot with an empty list of sources (a sandbox as the plan makes it, the same files and
// environment), asked the Trivial-Only dist-upgrade. It has no operation to stop at: it ends with status 0, no error and no sizes, after a real dist-upgrade of nothing (the status
// file of dpkg was the same afterwards). The sizes are known, and they are zero.
func TestTheEstimateOfABoxWithNothingToDoIsKnownAndZero(t *testing.T) {
	dist, f := ParseSolver(realOutput(t, "chroot-d11-nothing-dist-upgrade.txt"))
	if f != nil {
		t.Fatal(f)
	}
	if dist.Summary != (Summary{}) {
		t.Fatalf("summary = %+v", dist.Summary)
	}
	size := realOutput(t, "chroot-d11-nothing-size.txt")
	if size.ExitCode != 0 || strings.Contains(size.Text, "E:") || strings.Contains(size.Text, "Need to get") {
		t.Fatalf("the real run did not end as the plan expects: %d, %q", size.ExitCode, size.Text)
	}
	est := ParseEstimate(size, dist.Summary, testDir)
	if !est.Known || est.DownloadBytes == nil || est.ExtraBytes == nil || *est.DownloadBytes != 0 || *est.ExtraBytes != 0 || est.DownloadBytes == est.ExtraBytes {
		t.Errorf("estimate = %+v (%v, %v)", est, est.DownloadBytes, est.ExtraBytes)
	}
	// the same text for another transaction is not its estimate, and no other end with status 0 is
	summary := Summary{}
	for name, out := range map[string]Output{
		"the sizes were printed too": {Text: size.Text + "Need to get 0 B of archives.\nAfter this operation, 0 B of additional disk space will be used.\n"},
		"an error":                   {Text: size.Text + "E: Something else.\n"},
		"the trivial error":          {Text: size.Text + trivialOnlyError + "\n"},
		"pending work of dpkg":       {Text: size.Text + "1 not fully installed or removed.\n"},
		"another transaction":        {Text: strings.Replace(size.Text, "0 upgraded", "1 upgraded", 1)},
		"another count of the packages that are not upgraded": {Text: strings.Replace(size.Text, "0 not upgraded", "3 not upgraded", 1)},
		"only the sentence of the download":                   {Text: size.Text + "Need to get 0 B of archives.\n"},
		"only the sentence of the disk":                       {Text: size.Text + "After this operation, 0 B of additional disk space will be used.\n"},
		"no summary":                                          {Text: "Reading package lists...\n"},
		"the summary twice":                                   {Text: size.Text + "0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"},
		"a cut output":                                        {Text: size.Text, Truncated: true},
		"another status":                                      {Text: size.Text, ExitCode: 1},
		"a control character":                                 {Text: size.Text + "\x01"},
	} {
		if est := ParseEstimate(out, summary, testDir); est.Known || est.DownloadBytes != nil || est.ExtraBytes != nil {
			t.Errorf("%s: known %+v", name, est)
		}
	}
	// something upgraded, installed, removed or downgraded is an operation, and apt stops at it: a text that says so and ends with status 0 is no estimate, whatever the simulation said
	for name, tc := range map[string]struct {
		line string
		sum  Summary
	}{
		"an upgrade":  {"1 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.", Summary{Upgraded: 1}},
		"an install":  {"0 upgraded, 1 newly installed, 0 to remove and 0 not upgraded.", Summary{New: 1}},
		"a removal":   {"0 upgraded, 0 newly installed, 1 to remove and 0 not upgraded.", Summary{Remove: 1}},
		"a downgrade": {"0 upgraded, 0 newly installed, 1 downgraded, 0 to remove and 0 not upgraded.", Summary{Downgraded: 1}},
	} {
		out := Output{Text: "Reading package lists...\nBuilding dependency tree...\nReading state information...\nCalculating upgrade...\n" + tc.line + "\n"}
		if est := ParseEstimate(out, tc.sum, testDir); est.Known {
			t.Errorf("%s with no error: known %+v", name, est)
		}
	}
	// packages that are not upgraded and no operation: a dist-upgrade of nothing, and the sizes are zero all the same
	held := Output{Text: "Reading package lists...\nCalculating upgrade...\n0 upgraded, 0 newly installed, 0 to remove and 2 not upgraded.\n"}
	if est := ParseEstimate(held, Summary{NotUpgraded: 2}, testDir); !est.Known || *est.DownloadBytes != 0 || *est.ExtraBytes != 0 {
		t.Errorf("packages that are not upgraded: %+v", est)
	}
}

// The usual text of a box with nothing to upgrade is not the bare summary of zeros: apt 2.2.4 in the same Debian 11 chroot, with one package installed by itself that nothing needs any more
// (testdata/real/chroot-d11-autoremove-one-nothing-*), prints the section of the packages that are no longer required above the same summary, and ends with status 0 and no sizes. The
// status-0 end is decided by the summary and by what must not be in the text, and not by every line being one that the parser classifies: a section that is no operation (this one, a
// warning, the packages that are kept back) leaves the sizes known, and zero.
func TestTheEstimateOfABoxWithNothingToUpgradeAndOnePackageNoLongerRequiredIsKnownAndZero(t *testing.T) {
	dist, f := ParseSolver(realOutput(t, "chroot-d11-autoremove-one-nothing-dist-upgrade.txt"))
	if f != nil {
		t.Fatal(f)
	}
	if dist.Summary != (Summary{}) || !slices.Equal(dist.AutoRemovable, []string{"osp-b"}) {
		t.Fatalf("the simulation: summary %+v, no longer required %v", dist.Summary, dist.AutoRemovable)
	}
	size := realOutput(t, "chroot-d11-autoremove-one-nothing-size.txt")
	if size.ExitCode != 0 || !strings.Contains(size.Text, "The following package was automatically installed and is no longer required:\n  osp-b\n") || strings.Contains(size.Text, "E:") || strings.Contains(size.Text, "Need to get") {
		t.Fatalf("the real run did not end as the plan expects: %d, %q", size.ExitCode, size.Text)
	}
	if est := ParseEstimate(size, dist.Summary, testDir); !est.Known || est.DownloadBytes == nil || est.ExtraBytes == nil || *est.DownloadBytes != 0 || *est.ExtraBytes != 0 {
		t.Errorf("estimate = %+v", est)
	}
	// the other things that apt prints above that summary and that are no operation: the sizes stay known and zero
	for name, tc := range map[string]struct {
		text string
		sum  Summary
	}{
		"a warning":                   {"W: Some warning that apt prints.\n" + size.Text, Summary{}},
		"packages that are kept back": {strings.Replace(size.Text, "0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.", "The following packages have been kept back:\n  osp-c\n0 upgraded, 0 newly installed, 0 to remove and 1 not upgraded.", 1), Summary{NotUpgraded: 1}},
	} {
		if est := ParseEstimate(Output{Text: tc.text}, tc.sum, testDir); !est.Known || *est.DownloadBytes != 0 || *est.ExtraBytes != 0 {
			t.Errorf("%s: %+v", name, est)
		}
	}
	// and what is an operation, or work that dpkg has pending, still is not
	for name, text := range map[string]string{
		"pending work of dpkg":   size.Text + "1 not fully installed or removed.\n",
		"an error of apt":        size.Text + "E: Something else.\n",
		"another transaction":    strings.Replace(size.Text, "0 upgraded", "1 upgraded", 1),
		"the sizes were printed": size.Text + "Need to get 0 B of archives.\nAfter this operation, 0 B of additional disk space will be used.\n",
	} {
		if est := ParseEstimate(Output{Text: text}, Summary{}, testDir); est.Known {
			t.Errorf("%s: known %+v", name, est)
		}
	}
}
