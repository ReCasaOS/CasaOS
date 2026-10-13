package osplan

import (
	"slices"
	"strings"
	"testing"
)

// The outputs of apt 2.2.4 in a Debian 11 chroot (testdata/real/chroot-d11-*), made for these tests: the warning about the
// removal of an essential package, a downgrade (a pin of a priority over 1000), the same on a package that is on hold, and the
// updates that cannot reach their mirror. They are apt's text, byte for byte, with the status it ended with.

func parseReal(t *testing.T, name string) *SolverRun {
	t.Helper()
	run, f := ParseSolver(realOutput(t, name))
	if f != nil {
		t.Fatalf("%s: %v", name, f)
	}
	return run
}

func sorted(list []string) []string {
	out := slices.Clone(list)
	slices.Sort(out)
	return out
}

func TestTheRealWarningOfAptAboutEssentialPackagesIsParsed(t *testing.T) {
	for _, tc := range []struct {
		file             string
		essential, going []string
	}{
		// the simplest: the names, and the list of the removed that has one more
		{"chroot-d11-remove-login.txt", []string{"login", "util-linux"}, []string{"login", "mount", "util-linux"}},
		// "(due to ...)", a list of the removed that is wrapped
		{"chroot-d11-remove-libpam-modules.txt", []string{"apt", "adduser", "login", "libpam-runtime", "libpam-modules", "util-linux"},
			[]string{"adduser", "apt", "apt-utils", "libpam-modules", "libpam-runtime", "login", "mount", "passwd", "util-linux"}},
		// the names are wrapped over four lines, with "(due to ...)" on most of them
		{"chroot-d11-remove-dpkg.txt", []string{"apt", "adduser", "dash", "dpkg", "debconf", "grep", "gzip", "init-system-helpers", "perl-base", "login", "libpam0g", "libpam-runtime", "libpam-modules", "util-linux"},
			[]string{"adduser", "apt", "apt-utils", "ca-certificates", "dash", "debconf", "dpkg", "grep", "gzip", "init-system-helpers", "libgssapi-krb5-2", "libkrb5-3", "libnsl2", "libpam-modules",
				"libpam-modules-bin", "libpam-runtime", "libpam0g", "libssl1.1", "libtirpc3", "login", "mount", "openssl", "passwd", "perl-base", "tzdata", "util-linux"}},
	} {
		run := parseReal(t, tc.file)
		if !slices.Equal(sorted(run.Essential), sorted(tc.essential)) {
			t.Errorf("%s: essential = %v, want %v", tc.file, sorted(run.Essential), sorted(tc.essential))
		}
		if !slices.Equal(sorted(run.Removals()), tc.going) || run.Summary.Remove != len(tc.going) {
			t.Errorf("%s: removals = %v (%d), want %v", tc.file, sorted(run.Removals()), run.Summary.Remove, tc.going)
		}
	}
}

func TestTheWarningOfAptAboutEssentialPackagesIsReadOrRefusedAsItIs(t *testing.T) {
	first := "WARNING: The following essential packages will be removed.\n"
	second := "This should NOT be done unless you know exactly what you are doing!\n"
	removed := "The following packages will be REMOVED:\n  login mount\n"
	tail := "0 upgraded, 0 newly installed, 2 to remove and 0 not upgraded.\nRemv mount [1]\nRemv login [1]\n"
	ok := func(name, names string) *SolverRun {
		t.Helper()
		run, f := ParseSolver(solver(removed + first + second + names + tail))
		if f != nil {
			t.Fatalf("%s: %v", name, f)
		}
		return run
	}
	// one name, two, with and without a reason, with a reason that is cut over two lines, followed by a blank line
	if run := ok("one", "  login\n"); !slices.Equal(run.Essential, []string{"login"}) {
		t.Errorf("one: %v", run.Essential)
	}
	if run := ok("two with a reason", "  login mount (due to login)\n"); !slices.Equal(run.Essential, []string{"login", "mount"}) {
		t.Errorf("two with a reason: %v", run.Essential)
	}
	if run := ok("a reason over two lines", "  login mount (due to\n  login)\n"); !slices.Equal(run.Essential, []string{"login", "mount"}) {
		t.Errorf("a reason over two lines: %v", run.Essential)
	}
	if run := ok("a blank line after", "  login\n\n"); !slices.Equal(run.Essential, []string{"login"}) {
		t.Errorf("a blank line after: %v", run.Essential)
	}
	// the list of the removed that follows the warning is still read as a list of the removed
	run, f := ParseSolver(solver(first + second + "  login\nThe following packages will be REMOVED:\n  login mount\n" + tail))
	if f != nil || !slices.Equal(run.Removed, []string{"login", "mount"}) || !slices.Equal(run.Essential, []string{"login"}) {
		t.Errorf("the removed after the warning: %v, %+v", f, run)
	}
	for name, tc := range map[string]struct {
		text   string
		reason string
	}{
		"the second line is another":     {removed + first + "Please do not.\n  login\n" + tail, "essential_block"},
		"no second line":                 {removed + first, "essential_block"},
		"no second line before the rest": {removed + first + tail, "essential_block"},
		"the warning twice":              {removed + first + second + "  login\n" + first + second + "  mount\n" + tail, "duplicate_section"},
		"a name that is not a package's": {removed + first + second + "  Login\n" + tail, "bad_name"},
		"a name that is not removed":     {removed + first + second + "  login vim\n" + tail, "essential_not_removed"},
		"a reason that does not close":   {removed + first + second + "  login (due to mount\n" + tail, "bad_name"},
		"a reason with parentheses":      {removed + first + second + "  login (due to (mount))\n" + tail, "bad_name"},
		"a name with a star":             {removed + first + second + "  login*\n" + tail, "bad_name"},
	} {
		_, f := ParseSolver(solver(tc.text))
		if f == nil || f.Code != CodePlanUnparsed || f.Data["reason"] != tc.reason {
			t.Errorf("%s: %v, want %s", name, f, tc.reason)
		}
		// what is not a name is said as the line, and the name that is not removed as that name
		switch tc.reason {
		case "bad_name":
			if _, ok := f.Data["line"].(string); !ok || !strings.Contains(f.Detail, f.Data["line"].(string)) {
				t.Errorf("%s: the name that is not one is not said: %+v", name, f)
			}
		case "essential_not_removed":
			if f.Data["line"] != "vim" {
				t.Errorf("%s: %+v", name, f)
			}
		}
	}
	// the line that is not understood is said (the first of the warning that is another one), sanitised
	_, f = ParseSolver(solver(removed + first + "This should be done http://u:p@x.example/a?b=c \xff\n" + tail))
	if f == nil || f.Data["reason"] != "essential_block" || !strings.Contains(f.Detail, "http://x.example") || strings.Contains(f.Detail, "u:p") || strings.Contains(f.Detail, "b=c") || f.Data["line"] == nil {
		t.Errorf("failure = %+v", f)
	}
}

func TestADowngradeIsParsedAsAptSaysItAndTheHeldOneIsNotKeptBackByName(t *testing.T) {
	for _, name := range []string{"chroot-d11-downgrade-upgrade.txt", "chroot-d11-downgrade-dist-upgrade.txt"} {
		run := parseReal(t, name)
		if run.Summary != (Summary{Downgraded: 1}) || !slices.Equal(run.Downgraded, []string{"docker-ce"}) || len(run.Upgraded)+len(run.New)+len(run.KeptBack)+len(run.Removed) != 0 {
			t.Errorf("%s: %+v", name, run)
		}
		if len(run.Inst) != 1 || run.Inst[0] != (InstLine{Name: "docker-ce", From: "2", To: "1", Tag: "localhost"}) {
			t.Errorf("%s: inst = %+v", name, run.Inst)
		}
	}
	// a package on hold whose candidate is a downgrade: apt counts it as not upgraded and does not name it (measured, apt 2.2.4)
	for _, name := range []string{"chroot-d11-downgrade-held-upgrade.txt", "chroot-d11-downgrade-held-dist-upgrade.txt"} {
		run := parseReal(t, name)
		if run.Summary != (Summary{NotUpgraded: 1}) || len(run.KeptBack) != 0 || len(run.Downgraded) != 0 {
			t.Errorf("%s: %+v", name, run)
		}
	}
	// and the estimate of the transaction that has a downgrade in it
	dist := parseReal(t, "chroot-d11-downgrade-dist-upgrade.txt")
	est := ParseEstimate(realOutput(t, "chroot-d11-downgrade-size.txt"), dist.Summary, testDir)
	if !est.Known || *est.DownloadBytes != 724 || *est.ExtraBytes != 0 {
		t.Errorf("estimate = %+v", est)
	}
	if ParseEstimate(realOutput(t, "chroot-d11-downgrade-size.txt"), Summary{}, testDir).Known {
		t.Error("the estimate of a transaction that has a downgrade is the estimate of one that has none")
	}
}

func TestTheNotUpgradedCountMayBeMoreThanTheNamesOfTheKeptBackListAndNeverLess(t *testing.T) {
	two := "The following packages have been kept back:\n  bash curl\n0 upgraded, 0 newly installed, 0 to remove and 3 not upgraded.\n"
	run, f := ParseSolver(solver(two))
	if f != nil || run.Summary.NotUpgraded != 3 || len(run.KeptBack) != 2 {
		t.Fatalf("three not upgraded and two named: %v, %+v", f, run)
	}
	if _, f := ParseSolver(solver(strings.Replace(two, "3 not upgraded", "2 not upgraded", 1))); f != nil {
		t.Errorf("as many as are named: %v", f)
	}
	if _, f := ParseSolver(solver(strings.Replace(two, "3 not upgraded", "1 not upgraded", 1))); f == nil || f.Data["reason"] != "summary_mismatch" || f.Data["section"] != "kept_back" {
		t.Errorf("more named than counted: %v", f)
	}
}

func TestTheNLinesAndTheWLinesOfASimulationAreNotAProblem(t *testing.T) {
	text := "N: Ignoring file 'x.list.save' in directory '/etc/apt/sources.list.d/' as it has an invalid filename extension\nW: Target Packages (main/binary-amd64/Packages) is configured multiple times\n" +
		"The following packages will be upgraded:\n  bash\nN: a note in the middle\n1 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\nW: and a warning after\nInst bash [1] (2 Debian:12.15/oldstable [amd64])\n"
	if _, f := ParseSolver(solver(text)); f != nil {
		t.Fatal(f)
	}
	// what has only the look of one is not
	if _, f := ParseSolver(solver(strings.Replace(text, "N: a note", "N:a note", 1))); f == nil || f.Data["reason"] != "unknown_line" {
		t.Errorf("N: without its space: %v", f)
	}
	if _, f := ParseSolver(solver(strings.Replace(text, "W: and", " W: and", 1))); f == nil {
		t.Error("an indented warning")
	}
}

func TestALineThatIsNotUnderstoodIsSaidInTheDetailAndSanitised(t *testing.T) {
	good := "The following packages will be upgraded:\n  bash\n1 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\nInst bash [1] (2 Debian:12.15/oldstable [amd64])\n"
	_, f := ParseSolver(solver(good + "Something else happened to https://u:p@x.example/a?b=c \xff\xfe here\n"))
	if f == nil || f.Code != CodePlanUnparsed || f.Data["reason"] != "unknown_line" {
		t.Fatalf("failure = %+v", f)
	}
	want := "Something else happened to https://x.example ?? here"
	if !strings.HasSuffix(f.Detail, ": "+want) || f.Data["line"] != want {
		t.Errorf("detail = %q, line = %v", f.Detail, f.Data["line"])
	}
	long := "A line of apt's that goes on " + strings.Repeat("and on ", 100)
	_, f = ParseSolver(solver(good + long + "\n"))
	if f == nil || len(f.Data["line"].(string)) > maxDetailChars {
		t.Errorf("a long line: %+v", f)
	}
	// the other lines that were not understood are said too
	for name, tc := range map[string]struct{ text, reason, line string }{
		"an Inst line":  {good + "Inst bash [5.1-2 (5.2)\n", "bad_inst_line", "Inst bash [5.1-2 (5.2)"},
		"a Remv line":   {good + "Remv -x [1]\n", "bad_remv_line", "Remv -x [1]"},
		"the warning's": {"WARNING: The following essential packages will be removed.\nNot that.\n", "essential_block", "Not that."},
	} {
		_, f := ParseSolver(solver(tc.text))
		if f == nil || f.Data["reason"] != tc.reason || f.Data["line"] != tc.line {
			t.Errorf("%s: %+v", name, f)
		}
	}
}

// What an update says is printable ASCII, whatever the bytes of apt's line are: the UTF-8 of a mirror's name or of a message
// that a locale translated is not kept.
func TestTheDetailOfAnUpdateHasNoByteThatIsNotPrintableAscii(t *testing.T) {
	f := ParseUpdate(Output{Text: "E: dépôt € \xff\xfe\n", ExitCode: 100})
	if f == nil || f.Detail != "E: d??p??t ??? ??" {
		t.Fatalf("failure = %+v", f)
	}
	for i := 0; i < len(f.Detail); i++ {
		if c := f.Detail[i]; c < 0x20 || c > 0x7e {
			t.Errorf("byte %#x in %q", c, f.Detail)
		}
	}
	// and so is the detail of a failed simulation
	_, f = ParseSolver(Output{Text: "E: Impossible dépôt\n", ExitCode: 100})
	if f == nil || f.Code != CodeSandboxSolverFailed || f.Detail != "E: Impossible d??p??t" {
		t.Errorf("solver failure = %+v", f)
	}
}

func TestASummaryWithANumberThatIsNotOneOfAptIsNotParsed(t *testing.T) {
	for _, text := range []string{
		"99999999 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n",
		"0 upgraded, 99999999 newly installed, 0 to remove and 0 not upgraded.\n",
		"0 upgraded, 0 newly installed, 99999999 downgraded, 0 to remove and 0 not upgraded.\n",
		"0 upgraded, 0 newly installed, 99999999 to remove and 0 not upgraded.\n",
		"0 upgraded, 0 newly installed, 0 to remove and 99999999 not upgraded.\n",
	} {
		if _, f := ParseSolver(solver(text)); f == nil || f.Data["reason"] != "bad_summary" {
			t.Errorf("%q: %v", text, f)
		}
	}
	if sum, ok := summaryOf("1 upgraded, 2 newly installed, 3 downgraded, 4 to remove and 5 not upgraded."); !ok || sum != (Summary{Upgraded: 1, New: 2, Downgraded: 3, Remove: 4, NotUpgraded: 5}) {
		t.Errorf("summary = %+v, %v", sum, ok)
	}
	if sum, ok := summaryOf("1 upgraded, 2 newly installed, 4 to remove and 5 not upgraded."); !ok || sum != (Summary{Upgraded: 1, New: 2, Remove: 4, NotUpgraded: 5}) {
		t.Errorf("summary = %+v, %v", sum, ok)
	}
}

// ---- what went wrong in an update ------------------------------------------------------------------------------------

func TestTheRealUpdatesThatCannotReachTheirMirrorAreTheNetwork(t *testing.T) {
	for _, name := range []string{"chroot-d11-update-proxy-refused.txt", "chroot-d11-update-proxy-timeout.txt", "chroot-d11-update-unreachable.txt", "chroot-d11-update-unresolved.txt"} {
		out := realOutput(t, name)
		if out.ExitCode != 100 {
			t.Fatalf("%s: apt ended with %d", name, out.ExitCode)
		}
		f := ParseUpdate(out)
		if f == nil || f.Code != CodeSandboxUpdateFailed || f.Data["kind"] != KindNetwork {
			t.Errorf("%s: %+v", name, f)
			continue
		}
		if strings.Contains(f.Detail, "/dists/") {
			t.Errorf("%s: a path in the detail: %q", name, f.Detail)
		}
	}
}

// what apt says when it cannot reach a host, each as apt's own words, with none of the words of the others in it
func TestWhatAptSaysWhenItCannotReachAHostIsTheNetwork(t *testing.T) {
	for _, say := range []string{
		"Could not resolve 'deb.example.invalid'",
		"Temporary failure resolving 'deb.example.invalid'",
		"Could not connect to deb.debian.org:80 (151.101.2.132).",
		"connect (101: Network is unreachable)",
		"connect (113: No route to host)",
		"Operation timed out",
		"Failed to connect to deb.debian.org port 80",
		"Connection failed [IP: 151.101.2.132 80]",
	} {
		text := "Err:1 http://deb.debian.org/debian bookworm InRelease\n  " + say + "\nE: Some index files failed to download. They have been ignored, or old ones used instead.\n"
		if f := ParseUpdate(Output{Text: text, ExitCode: 100}); f == nil || f.Data["kind"] != KindNetwork {
			t.Errorf("%q: %+v", say, f)
		}
	}
}

func TestEachWordOfAptThatMeansTheNetworkIsOneWhateverItsCase(t *testing.T) {
	for _, w := range networkWords {
		// the word alone must make it so: none of the others is in the sample, and neither is a 404
		for _, other := range networkWords {
			if other != w && strings.Contains(w, other) {
				t.Fatalf("%q has %q in it: one of the two is not needed", w, other)
			}
		}
		for _, say := range []string{strings.ToUpper(w[:1]) + w[1:], strings.ToUpper(w), w} {
			text := "Err:1 http://deb.debian.org/debian bookworm InRelease\n  " + say + " [IP: 1.2.3.4 80]\nE: Some index files failed to download.\n"
			if f := ParseUpdate(Output{Text: text, ExitCode: 100}); f == nil || f.Data["kind"] != KindNetwork {
				t.Errorf("%q: %+v", say, f)
			}
		}
	}
	// what is not the network: the cases that come first, and what says nothing of it
	for text, kind := range map[string]string{
		"Err:1 http://x/y\n  404  Not Found [IP: 1.2.3.4 80]\n":                                                      KindNotFound,
		"Err:1 http://x/y\n  Not Found, and then the connection was closed\n":                                        KindNotFound,
		"W: GPG error: x: NO_PUBKEY 0E98404D386FA1D9 and a connection\n":                                             KindNoPubkey,
		"E: Repository 'http://x bookworm InRelease' changed its 'Version' value after a connection\n":               KindReleaseChange,
		"E: The repository 'http://x bookworm Release' does not have a Release file.\n":                              KindReleaseChange,
		"E: The repository 'http://x bookworm InRelease' is not signed.\n":                                           KindOther,
		"E: Could not open file /var/lib/apt/lists/partial/x - open (28: No space left on device)\n":                 KindOther,
		"E: Failed to fetch http://x/y  Hash Sum mismatch\n  Hashes of expected file:\n  Hashes of received file:\n": KindOther,
	} {
		if f := ParseUpdate(Output{Text: text, ExitCode: 100}); f == nil || f.Data["kind"] != kind {
			t.Errorf("%q: %+v, want %s", text, f, kind)
		}
	}
}

// ---- the sizes -------------------------------------------------------------------------------------------------------

func TestACommaThatDoesNotGroupByThreeIsNotAThousandsSeparator(t *testing.T) {
	summary := Summary{Upgraded: 384, New: 75, Remove: 2}
	after := "After this operation, 300 MB of additional disk space will be used."
	for _, tc := range []struct {
		need  string
		bytes int64
	}{
		{"Need to get 1,234 kB of archives.", 1234e3},
		{"Need to get 12,345,678 B of archives.", 12345678},
		{"Need to get 1,234.5 MB of archives.", 1234.5e6},
		{"Need to get 999 B of archives.", 999},
		{"Need to get 0 B/1,234,567 B of archives.", 1234567},
	} {
		if est := ParseEstimate(estimateOutput(t, tc.need, after), summary, testDir); !est.Known || *est.DownloadBytes != tc.bytes {
			t.Errorf("%q: %+v", tc.need, est)
		}
	}
	for _, need := range []string{
		"Need to get 56,8 MB of archives.",
		"Need to get 1,2 MB of archives.",
		"Need to get 1,23 kB of archives.",
		"Need to get 12,34 MB of archives.",
		"Need to get 1234,567 kB of archives.",
		"Need to get 1,234,56 kB of archives.",
		"Need to get 1,,234 kB of archives.",
		"Need to get ,5 MB of archives.",
		"Need to get 1, kB of archives.",
		"Need to get 0 B/56,8 MB of archives.",
	} {
		if est := ParseEstimate(estimateOutput(t, need, after), summary, testDir); est.Known || est.DownloadBytes != nil {
			t.Errorf("%q: known: %+v", need, est)
		}
	}
	// the same in the sentence of the extra space
	for _, extra := range []string{"After this operation, 12,3 MB of additional disk space will be used.", "After this operation, 12,3 MB disk space will be freed."} {
		if est := ParseEstimate(estimateOutput(t, "Need to get 1 MB of archives.", extra), summary, testDir); est.Known {
			t.Errorf("%q: known: %+v", extra, est)
		}
	}
	if n, ok := parseSize("1,234 kB"); !ok || n != 1234e3 {
		t.Errorf("parseSize = %d, %v", n, ok)
	}
	if _, ok := parseSize("1,23 kB"); ok {
		t.Error("parseSize reads a decimal comma")
	}
}

// ---- what apt prints for one package, for nothing, and for a package that dpkg left half done ----------

// Real apt 2.2.4 in the Debian 11 chroot: a package b that a was the only reason for, when a 2 drops its Depends on b. With one
// name the section has its singular header (ngettext), and its hint says "it": a box whose upgrade leaves exactly one package
// that is no longer required was not planned at all.
func TestTheSectionOfOneNameNoLongerRequiredIsParsedAsTheRealAptPrintsIt(t *testing.T) {
	for _, name := range []string{"chroot-d11-autoremove-one-upgrade.txt", "chroot-d11-autoremove-one-dist-upgrade.txt"} {
		if !strings.Contains(realFile(t, name), "The following package was automatically installed and is no longer required:\n  osp-b\nUse 'apt autoremove' to remove it.\n") {
			t.Fatalf("%s is not the real text that this test is about", name)
		}
		run := parseReal(t, name)
		if !slices.Equal(run.AutoRemovable, []string{"osp-b"}) || !slices.Equal(run.Upgraded, []string{"osp-a"}) || run.Summary != (Summary{Upgraded: 1}) {
			t.Errorf("%s: %+v", name, run)
		}
	}
	// the two headers are one section: they cannot both be there
	both := "The following package was automatically installed and is no longer required:\n  a\nThe following packages were automatically installed and are no longer required:\n  b c\n0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"
	if _, f := ParseSolver(Output{Text: both}); f == nil || f.Code != CodePlanUnparsed || f.Data["reason"] != "duplicate_section" {
		t.Errorf("the singular and the plural: %+v", f)
	}
}

// Real apt 2.2.4: a package left half configured by dpkg (a postinst that failed) makes apt add a count line after its summary, in
// both simulations. The plan is not made, and the box is told what to do about its dpkg state, not that apt was not understood.
func TestAHalfConfiguredPackageIsSaidAsDpkgsStateAndNotAsAnAptThatWasNotUnderstood(t *testing.T) {
	for _, name := range []string{"chroot-d11-half-configured-upgrade.txt", "chroot-d11-half-configured-dist-upgrade.txt"} {
		if !strings.Contains(realFile(t, name), "\n1 not fully installed or removed.\n") {
			t.Fatalf("%s is not the real text that this test is about", name)
		}
		run, f := ParseSolver(realOutput(t, name))
		if run != nil || f == nil || f.Code != CodePlanUnparsed || f.Data["reason"] != "not_fully_installed" || f.Data["line"] != "1 not fully installed or removed." {
			t.Fatalf("%s: %+v, %+v", name, run, f)
		}
		if !strings.Contains(f.Detail, "dpkg --configure -a") || !strings.Contains(f.Detail, "not fully installed or removed") || strings.Contains(f.Detail, "not understood") {
			t.Errorf("%s: detail = %q", name, f.Detail)
		}
	}
	// whatever the number, and nothing that only looks like it
	for line, want := range map[string]string{
		"12 not fully installed or removed.": "not_fully_installed",
		"0 not fully installed or removed.":  "not_fully_installed",
		"1 not fully installed or removed":   "unknown_line",
		"a not fully installed or removed.":  "unknown_line",
		" 1 not fully installed or removed.": "unknown_line",
	} {
		_, f := ParseSolver(Output{Text: "0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n" + line + "\n"})
		if f == nil || f.Code != CodePlanUnparsed || f.Data["reason"] != want {
			t.Errorf("%q: %+v, want %s", line, f, want)
		}
	}
}
