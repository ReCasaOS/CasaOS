package osreadiness

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestInstalledPackagesAreTheOnesThatAreOnTheBox(t *testing.T) {
	f := newBox(t)
	f.on(""+
		"ii \tlibc6\tamd64\t2.31-13+deb11u8\n"+
		"hi \tdocker-ce\tamd64\t5:28.0.4-1~debian.11~bullseye\n"+
		"iF \thalf-done\tamd64\t1\n"+
		"rc \tremoved\tamd64\t1\n"+
		"un \tnever\t\t\n"+
		"pn \tpurged\tamd64\t1\n"+
		"dpkg-query: no packages found matching nothing\n"+
		"ii \tUPPER\tamd64\t1\n"+
		"ii \tonly-three\tamd64\n"+
		"ii \t<b>x</b>\tamd64\t1\n"+
		"ii  \tspaced\ti386\t1.0\r\n", 1, argvQuery("libc6", "docker-ce")...)
	got, err := installedPackages(context.Background(), f, "libc6", "docker-ce")
	if err != nil {
		t.Fatal(err)
	}
	want := []installedPackage{
		{"libc6", "amd64", "2.31-13+deb11u8", false}, {"docker-ce", "amd64", "5:28.0.4-1~debian.11~bullseye", true}, {"half-done", "amd64", "1", false}, {"spaced", "i386", "1.0", false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("packages = %+v\nwant      %+v", got, want)
	}
}

func TestInstalledPackagesFailWhenTheToolDoes(t *testing.T) {
	f := newBox(t)
	f.on("", 2, argvQuery("x")...)
	if _, err := installedPackages(context.Background(), f, "x"); !errors.Is(err, errToolFailed) {
		t.Fatalf("err = %v", err)
	}
	f.onError(ErrToolMissing, argvQuery("y")...)
	if _, err := installedPackages(context.Background(), f, "y"); !errors.Is(err, ErrToolMissing) {
		t.Fatalf("err = %v", err)
	}
	// patterns that match nothing are not a failure: dpkg-query says so with an exit status of 1
	if packages, err := installedPackages(context.Background(), f, "nothing-registered"); err != nil || len(packages) != 0 {
		t.Fatalf("packages = %v, err = %v", packages, err)
	}
}

func TestMountInfoIsReadAsProcSaysIt(t *testing.T) {
	text := "" +
		"21 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw,errors=remount-ro\n" +
		"22 21 0:5 / /dev rw,nosuid shared:2 - devtmpfs udev rw,size=1,nr_inodes=2\n" +
		"36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 master:2 - ext3 /dev/root rw,errors=continue\n" +
		"37 21 253:0 / /data rw - xfs /dev/mapper/vg-data rw\n" +
		"broken\n" +
		"1 2 3 / /x - \n"
	got := parseMountInfo(text)
	want := []mount{
		{"8:1", "/", "ext4", "/dev/sda1"},
		{"0:5", "/dev", "devtmpfs", "udev"},
		{"98:0", "/mnt2", "ext3", "/dev/root"},
		{"253:0", "/data", "xfs", "/dev/mapper/vg-data"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mounts = %+v\nwant    %+v", got, want)
	}
	if root, ok := rootMount(got); !ok || root.MajorMinor != "8:1" {
		t.Fatalf("root = %+v, %v", root, ok)
	}
	// the mount on top is the last one of the file
	top := append(got, mount{"0:1", "/", "overlay", "overlay"})
	if root, _ := rootMount(top); root.FSType != "overlay" {
		t.Fatalf("root = %+v", root)
	}
	if _, ok := rootMount(got[1:2]); ok {
		t.Fatal("a root where there is none")
	}
}
