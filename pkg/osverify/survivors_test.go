package osverify

import (
	"slices"
	"strconv"
	"testing"
)

// Each of the tests below holds one guard of the facts or of the judge to a case that only that guard keeps out.

func TestOnlyTheFirstFiveThousandLinesOfAFileOfTheBackupFolderAreRead(t *testing.T) {
	var same, more string
	for i := 0; i < 5000; i++ {
		same += "/etc/f" + strconv.Itoa(i) + ".dpkg-dist\n"
	}
	more = same + "/etc/one-too-many.dpkg-dist\n"
	w := newWorld(t)
	w.bk[BKDpkgFiles], w.bk[BKDpkgFilesAfter] = same, more
	if got := w.judge(); len(got) != 0 {
		t.Errorf("a line past the cap was read: %+v", got)
	}
	// the last line that is read is
	w.bk[BKDpkgFiles] = same[:len(same)-len("/etc/f"+strconv.Itoa(maxLines-1)+".dpkg-dist\n")]
	if got := w.judge(); len(got) != 1 || got[0].Arg != "/etc/f4999.dpkg-dist" {
		t.Errorf("the last line within the cap was not read: %+v", got)
	}
}

func TestOnlyDockersOwnSourceIsDockersAndTheOthersAreNamed(t *testing.T) {
	for name, tc := range map[string]struct {
		paused []string
		want   []Finding
	}{
		"Docker's":                           {[]string{"docker.list.casaos-paused"}, []Finding{infoF(CodeDockerSourcePaused, "")}},
		"one that begins as Docker's does":   {[]string{"dockerx.list.casaos-paused"}, []Finding{infoF(CodeSourcesLeftPaused, "dockerx.list.casaos-paused")}},
		"one that ends as Docker's does":     {[]string{"not-docker.list.casaos-paused"}, []Finding{infoF(CodeSourcesLeftPaused, "not-docker.list.casaos-paused")}},
		"one that is Docker's and something": {[]string{"docker.list.casaos-paused.bak"}, []Finding{infoF(CodeSourcesLeftPaused, "docker.list.casaos-paused.bak")}},
		"a line of a file that is not":       {[]string{"docker.list:3"}, []Finding{infoF(CodeSourcesLeftPaused, "docker.list:3")}},
	} {
		w := newWorld(t)
		w.after.Paused = tc.paused
		if got := w.judge(); !equalFindings(got, tc.want) {
			t.Errorf("%s: %+v, want %+v", name, got, tc.want)
		}
	}
}

func equalFindings(a, b []Finding) bool {
	return slices.EqualFunc(a, b, func(x, y Finding) bool {
		return x.Code == y.Code && x.Severity == y.Severity && x.Arg == y.Arg && x.Policy == y.Policy && slices.Equal(x.Apps, y.Apps)
	})
}

func TestAUserOfSambaHasANumber(t *testing.T) {
	for name, tc := range map[string]struct {
		out  string
		want int
	}{
		"an empty number":         {"alice::Alice\n", 0},
		"no number":               {"alice:\n", 0},
		"a number":                {"alice:0:\n", 1},
		"a number of digits":      {"alice:65534:Alice\n", 1},
		"a number with a sign":    {"alice:-1:Alice\n", 0},
		"a name that is a number": {"1000:1000:x\n", 1},
	} {
		f := healthyBox(t)
		f.on(tc.out, 0, "pdbedit", "-L")
		if got := take(t, f); got.SambaUsers != tc.want {
			t.Errorf("%s: %d users, want %d", name, got.SambaUsers, tc.want)
		}
	}
}

func TestDockerIsAskedAboutWhenItsServiceRunsOrIsComingUpAndNotWhenItDoesNot(t *testing.T) {
	for state, want := range map[string]string{
		"active": DockerUp, "activating": DockerUp, "reloading": DockerUp,
		"inactive": DockerStopped, "failed": DockerStopped, "deactivating": DockerStopped, "maintenance": DockerStopped,
	} {
		f := healthyBox(t)
		f.on(state+"\n", 0, argvDockerState...)
		f.on("Docker version 24.0.7, build afdd53b\n", 0, "docker", "--version")
		got := take(t, f)
		if got.Docker.State != want {
			t.Errorf("a service that is %s: Docker is %q, want %q", state, got.Docker.State, want)
		}
		asked := slices.ContainsFunc(f.asked, func(argv []string) bool { return slices.Equal(argv, argvDockerInfo) })
		if asked != (want == DockerUp) {
			t.Errorf("a service that is %s: the daemon was asked: %v", state, asked)
		}
	}
}

func TestALineIsPausedByItsBeginningAndNotByWhatItSaysAtItsEnd(t *testing.T) {
	f := healthyBox(t)
	f.set("/etc/apt/sources.list", "deb http://deb.debian.org/debian bookworm main # casaos-paused: not at the start\n"+
		"# casaos-paused: deb http://example.org/x main\n"+
		"  # casaos-paused: deb http://example.org/y main\n"+
		"#casaos-paused: deb http://example.org/z main\n"+
		"# casaos-paused: deb http://example.org/w main\n")
	got := take(t, f)
	if want := []string{"docker.list.casaos-paused", "sources.list:2", "sources.list:5"}; !slices.Equal(got.Paused, want) {
		t.Errorf("paused = %q, want %q", got.Paused, want)
	}
}
