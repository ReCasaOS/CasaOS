package osreadiness

import "testing"

func TestVersionsAreOrderedAsDpkgOrdersThem(t *testing.T) {
	// each line is in increasing order: every version is less than the ones after it
	for _, chain := range [][]string{
		{"1.0~rc1", "1.0~rc2", "1.0", "1.0+b1", "1.0.1", "1.1", "1.10", "2.0"},
		{"1.0~~", "1.0~", "1.0", "1.0a"},
		{"1.0", "1:0.5", "1:1.0", "2:0.1"},
		{"2021.1.1", "2021.1.1+deb11u1", "2021.1.1+deb11u2", "2021.1.2", "2023.3+deb12u1", "2023.3+deb12u2", "2025.1"},
		{"1:9.2p1-1", "1:9.2p1-2", "1:9.2p1-2+deb12u3", "1:9.2p1-2+deb12u7", "1:9.2p1-2+deb12u10", "1:9.2p1-3", "1:9.3p1-1", "1:10.0p1-1"},
		{"5.10.0-9", "5.10.0-35", "5.10.0-100", "5.11.0-1", "6.1.0-13"},
		{"1.0-1", "1.0-1ubuntu1", "1.0-2"},
		{"a", "b", "z"},
		{"1.2.3", "1.2.10"},
	} {
		for i := range chain {
			for j := range chain {
				want := 0
				switch {
				case i < j:
					want = -1
				case i > j:
					want = 1
				}
				if got := compareVersions(chain[i], chain[j]); got != want {
					t.Errorf("compareVersions(%q, %q) = %d, want %d", chain[i], chain[j], got, want)
				}
			}
		}
	}
	for _, pair := range [][2]string{{"1.0", "1.0-0"}, {"1.01", "1.1"}, {"1:1", "1:1"}, {"0:1.0", "1.0"}, {"1.0", "1.00"}} {
		if got := compareVersions(pair[0], pair[1]); got != 0 {
			t.Errorf("compareVersions(%q, %q) = %d, want 0", pair[0], pair[1], got)
		}
	}
}

func TestKernelReleasesAreParsedAndComparedByFlavour(t *testing.T) {
	for release, want := range map[string]kernelRelease{
		"5.10.0-35-amd64":       {"5.10.0-35", "amd64"},
		"6.1.0-13-cloud-amd64":  {"6.1.0-13", "cloud-amd64"},
		"5.15.0-91-generic":     {"5.15.0-91", "generic"},
		"6.8.0-1015-aws":        {"6.8.0-1015", "aws"},
		"6.5.0-0.deb12.4-amd64": {"6.5.0-0.deb12.4", "amd64"},
		"5.10.0-35-amd64\n":     {"5.10.0-35", "amd64"},
	} {
		got, ok := parseKernel(release)
		if !ok || got != want {
			t.Errorf("parseKernel(%q) = %+v, %v; want %+v", release, got, ok, want)
		}
	}
	for _, release := range []string{"", "5.15.0-custom", "6.1-custom", "custom", "5.10.0", "5.10.0-35", "-amd64"} {
		if got, ok := parseKernel(release); ok {
			t.Errorf("parseKernel(%q) = %+v", release, got)
		}
	}
	a, _ := parseKernel("5.10.0-36-amd64")
	b, _ := parseKernel("5.10.0-35-amd64")
	c, _ := parseKernel("5.10.0-99-cloud-amd64")
	if !newerKernel(a, b) || newerKernel(b, a) || newerKernel(a, a) {
		t.Error("the order of kernels of a flavour is wrong")
	}
	if newerKernel(c, a) || newerKernel(a, c) {
		t.Error("kernels of two flavours are compared")
	}
}
