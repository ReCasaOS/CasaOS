package osreadiness

import (
	"context"
	"io/fs"
	"reflect"
	"strings"
	"testing"
)

// fstab is a machine whose /etc/fstab has the lines.
func fstab(lines ...string) func(*fakeProbe) {
	return func(f *fakeProbe) { f.set(fstabPath, "# /etc/fstab\n"+strings.Join(lines, "\n")+"\n") }
}

func disks(paths ...string) func(*fakeProbe) {
	return func(f *fakeProbe) {
		for _, path := range paths {
			f.set(path, "")
		}
	}
}

func TestFstabUnresolved(t *testing.T) {
	runScenarios(t, "fstab_unresolved", []scenario{
		{name: "the root by its label, which is there", severity: OK},
		{name: "a UUID that is there", setup: after(fstab("UUID=1234-ABCD /data ext4 defaults 0 2"), disks("/dev/disk/by-uuid/1234-ABCD")), severity: OK},
		{name: "a UUID that is not", setup: fstab("UUID=1234-ABCD /data ext4 defaults 0 2"), severity: Refuse, data: D{"mounts": []string{"/data"}, "count": 1}},
		{name: "a UUID in the other case does not resolve either", setup: after(fstab("UUID=1234-abcd /data ext4 defaults 0 2"), disks("/dev/disk/by-uuid/1234-ABCD")), severity: Refuse},
		{name: "a UUID in quotes", setup: after(fstab(`UUID="1234-ABCD" /data ext4 defaults 0 2`), disks("/dev/disk/by-uuid/1234-ABCD")), severity: OK},
		{name: "a PARTUUID is lowercased, as systemd does", setup: after(fstab("PARTUUID=6A5C7D5E-01 /data ext4 defaults 0 2"), disks("/dev/disk/by-partuuid/6a5c7d5e-01")), severity: OK},
		{name: "a PARTUUID that is not there", setup: fstab("PARTUUID=6a5c7d5e-01 /data ext4 defaults 0 2"), severity: Refuse},
		{name: "a label with a blank, as fstab and udev write it", setup: after(fstab(`LABEL=My\040Disk /mnt/disk ext4 defaults 0 2`), disks(`/dev/disk/by-label/My\x20Disk`)), severity: OK},
		{name: "a label that is not there", setup: fstab("LABEL=Backup /mnt/backup ext4 defaults 0 2"), severity: Refuse, data: D{"mounts": []string{"/mnt/backup"}}},
		{name: "a label of a disk whose label is another", setup: fstab("LABEL=cloudimg /mnt/x ext4 defaults 0 2"), severity: Refuse},
		{name: "a PARTLABEL that is there", setup: after(fstab("PARTLABEL=data /data ext4 defaults 0 2"), disks("/dev/disk/by-partlabel/data")), severity: OK},
		{name: "no /dev/disk/by-partlabel at all", setup: fstab("PARTLABEL=data /data ext4 defaults 0 2"), severity: Refuse},
		{name: "a device path that is there", setup: after(fstab("/dev/sdb1 /data ext4 defaults 0 2"), disks("/dev/sdb1")), severity: OK},
		{name: "a device path that is not", setup: fstab("/dev/sdb1 /data ext4 defaults 0 2"), severity: Refuse},
		{name: "a device that is a link", setup: after(fstab("/dev/mapper/vg-data /data ext4 defaults 0 2"), func(f *fakeProbe) {
			f.link("/dev/mapper/vg-data", "../dm-1")
			f.set("/dev/dm-1", "")
		}), severity: OK},
		{name: "a path that goes back up out of /dev is no device's", setup: fstab("/dev/../nowhere /data ext4 defaults 0 2"), severity: OK},
		{name: "nofail", setup: fstab("UUID=1234-ABCD /data ext4 defaults,nofail 0 2"), severity: OK},
		{name: "noauto", setup: fstab("UUID=1234-ABCD /data ext4 noauto,user 0 0"), severity: OK},
		{name: "mounted when it is first used", setup: fstab("UUID=1234-ABCD /data ext4 defaults,x-systemd.automount 0 0"), severity: OK},
		{name: "a swap that is not there does not stop the start", setup: fstab("UUID=1234-ABCD none swap sw 0 0"), severity: OK},
		{name: "a network share, a tmpfs, a bind: nothing to resolve", setup: fstab("nas:/export /mnt/nas nfs defaults 0 0", "//nas/share /mnt/s cifs defaults 0 0", "tmpfs /tmp tmpfs defaults 0 0", "/srv/x /mnt/x none bind 0 0", "proc /proc proc defaults 0 0"), severity: OK},
		{name: "comments and lines that are no entries", setup: fstab("# UUID=1234-ABCD /data ext4 defaults 0 2", "", "   ", "UUID=1234-ABCD /data", "UUID=1234-ABCD"), severity: OK},
		{name: "three fields: the options are the defaults", setup: fstab("UUID=1234-ABCD /data ext4"), severity: Refuse},
		{name: "an id that is not a file name", setup: fstab("UUID=../../etc/passwd /data ext4 defaults 0 2"), severity: Refuse},
		{name: "an id that is a file name that is not an id", setup: after(fstab("UUID=a/b /data ext4 defaults 0 2"), disks("/dev/disk/by-uuid/a/b")), severity: Refuse},
		{name: "two that are missing, once each, sorted", setup: fstab("UUID=1111-1111 /mnt/b ext4 defaults 0 2", "UUID=2222-2222 /mnt/a ext4 defaults 0 2", "UUID=1111-1111 /mnt/b ext4 defaults 0 2"), severity: Refuse, data: D{"mounts": []string{"/mnt/a", "/mnt/b"}, "count": 3}},
		{name: "a mount in a user's directory says not whose", setup: fstab("UUID=1111-1111 /home/alice/nas ext4 defaults 0 2", "UUID=2222-2222 /media/bob/usb ext4 defaults 0 2"), severity: Refuse, data: D{"mounts": []string{"/home/_/nas", "/media/_/usb"}}},
		{name: "a mount point that is not a path of the alphabet", setup: fstab(`UUID=1111-1111 /mnt/my\040disk ext4 defaults 0 2`), severity: Refuse, data: D{"mounts": []string{}, "count": 1}},
		{name: "no /etc/fstab", setup: func(f *fakeProbe) { f.remove(fstabPath) }, severity: OK},
		{name: "an /etc/fstab that cannot be read", setup: func(f *fakeProbe) {
			f.fail(fstabPath, &fs.PathError{Op: "open", Path: fstabPath, Err: fs.ErrPermission})
		}, severity: Refuse, unknown: ReasonUnreadable},
		{name: "an /etc/fstab that is too large", setup: func(f *fakeProbe) { f.set(fstabPath, strings.Repeat("# x\n", MaxFileBytes)) }, severity: Refuse, unknown: ReasonTooLarge},
		{name: "a device that cannot be looked at", setup: after(fstab("/dev/sdb1 /data ext4 defaults 0 2"), func(f *fakeProbe) {
			f.fail("/dev/sdb1", &fs.PathError{Op: "stat", Path: "/dev/sdb1", Err: fs.ErrPermission})
		}), severity: Refuse, unknown: ReasonUnreadable},
		{name: "a directory of labels that cannot be listed", setup: after(fstab("LABEL=x /data ext4 defaults 0 2"), func(f *fakeProbe) {
			f.fail("/dev/disk/by-label", &fs.PathError{Op: "open", Path: "/dev/disk/by-label", Err: fs.ErrPermission})
		}), severity: Refuse, unknown: ReasonUnreadable},
	})
}

func TestFstabNetwork(t *testing.T) {
	verify := func(text string, exit int) func(*fakeProbe) {
		return func(f *fakeProbe) { f.on(text, exit, argvVerify...) }
	}
	runScenarios(t, "fstab_network", []scenario{
		{name: "nothing to look at", severity: OK},
		{name: "an NFS share with no _netdev", setup: fstab("nas:/export /mnt/nas nfs defaults 0 0"), severity: Warn, data: D{"mounts": []string{"/mnt/nas"}, "count": 1, "verify_errors": 0, "verify_warnings": 0}, action: ActionNone},
		{name: "NFS 4 and CIFS", setup: fstab("nas:/a /mnt/a nfs4 defaults 0 0", "//nas/b /mnt/b cifs credentials=/root/c 0 0"), severity: Warn, data: D{"mounts": []string{"/mnt/a", "/mnt/b"}, "count": 2}},
		{name: "an NFS share with _netdev", setup: fstab("nas:/export /mnt/nas nfs defaults,_netdev 0 0"), severity: OK},
		{name: "an NFS share with nofail", setup: fstab("nas:/export /mnt/nas nfs defaults,nofail 0 0"), severity: OK},
		{name: "an NFS share that is not mounted at start", setup: fstab("nas:/export /mnt/nas nfs noauto 0 0", "nas:/b /mnt/b nfs x-systemd.automount 0 0"), severity: OK},
		{name: "a share in a user's directory", setup: fstab("nas:/export /home/alice/nas nfs defaults 0 0"), severity: Warn, data: D{"mounts": []string{"/home/_/nas"}}},
		{name: "a local disk is not a share", setup: fstab("UUID=x /data ext4 defaults 0 2"), severity: OK},
		{name: "a share that is commented out", setup: fstab("#nas:/export /mnt/nas nfs defaults 0 0", "   # nas:/b /mnt/b nfs defaults 0 0"), severity: OK},
		{name: "findmnt finds a warning", setup: verify("/boot/efi\n   [W] UUID=1234-ABCD translation failed\n\n0 parse errors, 0 errors, 1 warning\n", 64), severity: Warn, data: D{"verify_errors": 0, "verify_warnings": 1}},
		{name: "findmnt finds an error and two warnings", setup: verify("/data\n   [E] unreachable on boot required source: UUID=x\n   [W] non-bind mount source /dev/x does not exist\n   [W] option x is deprecated\n", 1), severity: Warn, data: D{"verify_errors": 1, "verify_warnings": 2}},
		{name: "findmnt finds a fatal error", setup: verify("   [F] cannot read /etc/fstab\n", 1), severity: Warn, data: D{"verify_errors": 1}},
		{name: "findmnt and a share", setup: after(fstab("nas:/export /mnt/nas nfs defaults 0 0"), verify("   [W] x\n", 64)), severity: Warn, data: D{"count": 1, "verify_warnings": 1}},
		{name: "a mark that is in the middle of a line, and marks of nothing", setup: verify("note [W] this is not a finding\n[X] nor this\nSuccess, no errors or warnings detected\n", 0), severity: OK},
		{name: "findmnt that says only its exit status", setup: verify("", 0), severity: OK},
		{name: "findmnt that fails and says something else", setup: verify("findmnt: cannot open /etc/fstab: Permission denied\n", 1), severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "no findmnt", setup: func(f *fakeProbe) { f.onError(ErrToolMissing, argvVerify...) }, severity: Refuse, unknown: ReasonToolMissing},
		{name: "findmnt that does not answer", setup: func(f *fakeProbe) { f.onError(context.DeadlineExceeded, argvVerify...) }, severity: Refuse, unknown: ReasonTimeout},
		{name: "no /etc/fstab: findmnt is asked all the same", setup: func(f *fakeProbe) { f.remove(fstabPath) }, severity: OK},
		{name: "an /etc/fstab that cannot be read", setup: func(f *fakeProbe) {
			f.fail(fstabPath, &fs.PathError{Op: "open", Path: fstabPath, Err: fs.ErrPermission})
		}, severity: Refuse, unknown: ReasonUnreadable},
	})
}

func TestFstabIsReadAsMountReadsIt(t *testing.T) {
	text := "# comment\n\n" +
		"UUID=1111  /mnt/a  ext4  defaults,nofail  0 2\n" +
		"\tLABEL=My\\040Disk\t/mnt/my\\040disk\text4\n" +
		"/dev/sda1 / ext4 errors=remount-ro 0 1\r\n" +
		"two fields\n" +
		"x\\134y /\\011tab nfs _netdev\n"
	got := parseFstab(text)
	want := []fstabEntry{
		{"UUID=1111", "/mnt/a", "ext4", []string{"defaults", "nofail"}},
		{"LABEL=My Disk", "/mnt/my disk", "ext4", []string{"defaults"}},
		{"/dev/sda1", "/", "ext4", []string{"errors=remount-ro"}},
		{`x\y`, "/\ttab", "nfs", []string{"_netdev"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries = %+v\nwant      %+v", got, want)
	}
	for in, out := range map[string]string{
		`a\040b`: "a b", `a\0`: `a\0`, `a\09`: `a\09`, `a\777`: `a\777`, `\`: `\`, `a\040`: "a ", `\040\040`: "  ", `plain`: "plain", `\08a`: `\08a`,
	} {
		if got := fstabUnescape(in); got != out {
			t.Errorf("fstabUnescape(%q) = %q, want %q", in, got, out)
		}
	}
	for in, out := range map[string]string{
		`My\x20Disk`: "My Disk", `a\x2fb`: "a/b", `a\x`: `a\x`, `a\xZZb`: `a\xZZb`, `\x41`: "A", `plain`: "plain", `a\x4`: `a\x4`,
	} {
		if got := udevDecode(in); got != out {
			t.Errorf("udevDecode(%q) = %q, want %q", in, got, out)
		}
	}
}

func TestAMountPointIsShownWithoutTheNameOfAUser(t *testing.T) {
	for in, want := range map[string]string{
		"/data": "/data", "/": "/", "/mnt/nas": "/mnt/nas", "/home/alice/nas": "/home/_/nas", "/home/alice": "/home/_", "/media/bob/usb/x": "/media/_/usb/x",
		"/run/media/carol/disk": "/run/media/_/disk", "/home//alice//nas/": "/home/_/nas", "/home/../data": "/data", "/mnt/with space": "", "/mnt/<b>": "", "relative": "", "": "",
		"/mnt/" + strings.Repeat("a", 200): "",
	} {
		if got := maskedPath(in); got != want {
			t.Errorf("maskedPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVerifyFindingsAreCountedByTheirMark(t *testing.T) {
	for name, tc := range map[string]struct {
		out            Output
		errs, warnings int
		ok             bool
	}{
		"clean":                       {Output{Text: "Success, no errors or warnings detected\n"}, 0, 0, true},
		"clean and an exit status":    {Output{Text: "Success, no errors or warnings detected\n", ExitCode: 1}, 0, 0, true},
		"findings":                    {Output{Text: "/a\n   [W] one\n   [W] two\n   [E] three\n", ExitCode: 1}, 1, 2, true},
		"windows line ends":           {Output{Text: "   [W] one\r\n   [E] two\r\n"}, 1, 1, true},
		"unindented marks":            {Output{Text: "[W] one\n"}, 0, 1, true},
		"a mark with nothing after":   {Output{Text: "   [W]\n", ExitCode: 1}, 0, 0, false},
		"nothing, with a good status": {Output{}, 0, 0, true},
		"nothing, with a bad status":  {Output{ExitCode: 1}, 0, 0, false},
		"a message and a bad status":  {Output{Text: "findmnt: failed\n", ExitCode: 1}, 0, 0, false},
		"binary":                      {Output{Text: "\x00\xff[W]\x00\n"}, 0, 0, true},
	} {
		errs, warnings, ok := verifyFindings(tc.out)
		if errs != tc.errs || warnings != tc.warnings || ok != tc.ok {
			t.Errorf("%s: verifyFindings = %d, %d, %v; want %d, %d, %v", name, errs, warnings, ok, tc.errs, tc.warnings, tc.ok)
		}
	}
}
