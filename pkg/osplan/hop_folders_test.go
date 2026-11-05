package osplan

import (
	"strings"
	"testing"
)

// A box that the beta took from Debian 11 to 12 keeps the folder of that hop, with every mark of it and no file hop: the blocks of the next hop are not stopped by it, and
// stop as they always did on a folder of their own hop.
func TestABlockOfAnotherHopIgnoresTheFoldersOfTheHopBefore(t *testing.T) {
	hop := withTestHop(t)
	rb := buildFor(t, planFor(t, hop))
	every := typed(typed(typed(rb, "TYPED_HOSTNAME", "testbox"), "ACCEPT_REMOVALS", "fuse"), "CONFIRM", "I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON")
	const other = "others/casaos-os-upgrade-p1-0123456789ab"
	bookworm := func(b *shellBox) {
		setupVerify(t, b)
		b.write("os-release", "PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nVERSION_CODENAME=bookworm\nID=debian\n")
	}

	// the folder of the hop before, ended: block 0 runs, and marks its own folder with its hop
	b := newShellBox(t)
	bookworm(b)
	for _, f := range []string{"begun-upgrade", "done-minimal", "done-after", "apt-orig/sources.list"} {
		b.write(other+"/"+f, "")
	}
	if out, code := b.run(every, "verify"); code != 0 || strings.Contains(out, "the hop is over") || strings.Contains(out, "another plan of this machine") {
		t.Fatalf("block 0 next to the ended folder of the hop before: exit %d, %q", code, out)
	}
	if got := b.read("bk/hop"); got != "debian-12-13\n" {
		t.Errorf("the file hop of the folder is %q", got)
	}

	// a file hop that is empty is no folder of this hop either
	b = newShellBox(t)
	bookworm(b)
	b.write(other+"/hop", "")
	b.write(other+"/begun-upgrade", "")
	if out, code := b.run(every, "verify"); code != 0 {
		t.Errorf("block 0 next to a folder whose file hop is empty: exit %d, %q", code, out)
	}

	// the folder of a plan of this hop that ended stops every block, as it always did
	b = newShellBox(t)
	bookworm(b)
	b.write(other+"/hop", "debian-12-13\n")
	b.write(other+"/done-after", "")
	b.write("bk/done-verify", "")
	if out, code := b.run(every, "timers"); code == 0 || !strings.Contains(out, "STOP: the hop is over for this machine: the plan whose folder is ./"+other+" took it to the end") {
		t.Errorf("block 1 next to an ended folder of its own hop: exit %d, %q", code, out)
	}
}

func TestTheBlocksOf11To12WriteNoHopFile(t *testing.T) {
	_, rb := realRunbook(t)
	every := typed(rb, "TYPED_HOSTNAME", "testbox")
	b := newShellBox(t)
	setupVerify(t, b)
	if out, code := b.run(every, "verify"); code != 0 {
		t.Fatalf("block 0: exit %d, %q", code, out)
	}
	if b.exists("bk/hop") {
		t.Error("the first hop wrote a file hop in its folder")
	}
}
