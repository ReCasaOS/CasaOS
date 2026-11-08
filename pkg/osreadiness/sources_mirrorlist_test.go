package osreadiness

import (
	"context"
	"io/fs"
	"testing"
)

// Debian's cloud images of Debian 12 and 13 name their archive by a list of mirrors on the machine
// (/etc/apt/mirrors/debian.list), and apt keeps no Release file under a name that can be derived of it:
// the list itself says whose the source is.
func TestAListOfDebiansMirrorsIsTheDistribution(t *testing.T) {
	const uri = "mirror+file:///etc/apt/mirrors/debian.list"
	for _, tc := range []struct {
		name            string
		list            string
		missing         bool
		distro, decided bool
	}{
		{"the cloud image's own", "https://deb.debian.org/debian\n", false, true, true},
		{"the security list, with a comment", "# the security archive\nhttps://deb.debian.org/debian-security\n", false, true, true},
		{"two of Debian's hosts, with what apt reads of them", "http://deb.debian.org/debian\tpriority:1\nhttp://ftp.fr.debian.org/debian\n", false, true, true},
		{"someone else's mirrors only", "https://mirror.example.edu/debian\n", false, false, true},
		{"Debian's and someone else's", "https://deb.debian.org/debian\nhttps://mirror.example.edu/debian\n", false, false, false},
		{"an address with a user", "https://u:p@deb.debian.org/debian\n", false, false, true},
		{"an empty list", "# nothing\n\n", false, false, false},
		{"a list that is not there", "", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBox(t)
			if tc.missing {
				f.fail("/etc/apt/mirrors/debian.list", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrNotExist})
			} else {
				f.set("/etc/apt/mirrors/debian.list", tc.list)
			}
			distro, decided, err := NewDistroDecider(f).Suite(context.Background(), uri, "bookworm")
			if err != nil || distro != tc.distro || decided != tc.decided {
				t.Fatalf("decider: distro, decided, err = %v, %v, %v; want %v, %v", distro, decided, err, tc.distro, tc.decided)
			}
			if got := marked(t, f, uri, "bookworm"); got != (tc.distro && tc.decided) {
				t.Fatalf("report: distro = %v", got)
			}
		})
	}
}

// Only the lists of /etc/apt/mirrors are read: another file, or a list on the network, is not decided by the plan.
func TestOnlyTheListsOfEtcAptMirrorsAreRead(t *testing.T) {
	f := newBox(t)
	f.set("/srv/debian.list", "https://deb.debian.org/debian\n")
	for _, uri := range []string{"mirror+file:///srv/debian.list", "mirror+file:///etc/apt/mirrors/../../srv/debian.list", "mirror+http://mirrors.example.org/mirrors.txt"} {
		if _, decided, _ := NewDistroDecider(f).Suite(context.Background(), uri, "bookworm"); decided {
			t.Errorf("%s is decided", uri)
		}
		if IsMirrorList(uri) {
			t.Errorf("%s is taken for a list of /etc/apt/mirrors", uri)
		}
	}
}

func TestReadDeb822SaysWhereEachFieldIs(t *testing.T) {
	text := "# a comment\nTypes: deb\nSuites: bookworm\n  bookworm-updates\n# inside\n  bookworm-backports\n\n\nTypes: deb-src\nURIs: x\n"
	stanzas, ok := ReadDeb822(text)
	if !ok || len(stanzas) != 2 {
		t.Fatalf("%v %+v", ok, stanzas)
	}
	s := stanzas[0]
	if s.First != 2 || s.Last != 6 || len(s.Fields) != 2 || s.Fields[1].Key != "Suites" || s.Fields[1].First != 3 || s.Fields[1].Last != 6 ||
		len(s.Fields[1].Lines) != 3 || s.Fields[1].Lines[2] != "bookworm-backports" {
		t.Errorf("%+v", s)
	}
	if t2 := stanzas[1]; t2.First != 9 || t2.Last != 10 || t2.Fields[1].Key != "URIs" {
		t.Errorf("%+v", t2)
	}
	for _, bad := range []string{" Types: deb\n", "Types deb\n", ": deb\n", "Types: deb\n\n  x\n"} {
		if _, ok := ReadDeb822(bad); ok {
			t.Errorf("%q is read", bad)
		}
	}
}
