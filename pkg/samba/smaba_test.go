package samba

import (
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A host that accepts the connection and never answers gives up after the timeout: the remount at
// boot asks every saved connection in turn, and one silent host held it, and the storages, forever.
func TestASilentHostTimesOut(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
		}
	}()
	previous := timeout
	timeout = 300 * time.Millisecond
	t.Cleanup(func() { timeout = previous })
	host, port, _ := net.SplitHostPort(listener.Addr().String())

	done := make(chan error, 1)
	go func() {
		_, err := GetSambaSharesList(host, port, "user", "password")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a host that never answered gave a share list")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting for a host that never answers")
	}
}

// A share is mounted at /mnt/<host>/<name>: a server's "..", "." or a name with a "/" would mount
// somewhere else.
func TestASharedNameThatLeavesTheHostsFolderIsSkipped(t *testing.T) {
	for _, name := range []string{"..", ".", "", "../etc", "a/b", "/"} {
		if !unsafeShare(name) {
			t.Errorf("%q is kept", name)
		}
	}
	for _, name := range []string{"Photos", "my share", "..hidden", "IPC$"} {
		if unsafeShare(name) {
			t.Errorf("%q is skipped", name)
		}
	}
}

// The port of the connection reaches the mount, which went to 445 whatever the connection said.
func TestTheMountOptionsCarryThePort(t *testing.T) {
	if got, err := MountOptions("user", "secret", "4450"); err != nil || got != "username=user,password=secret,port=4450" {
		t.Errorf("got %q, %v", got, err)
	}
	if got, err := MountOptions("user", "secret", ""); err != nil || got != "username=user,password=secret" {
		t.Errorf("without a port: got %q, %v", got, err)
	}
	// written as a number: the kernel reads "0445" as octal
	if got, err := MountOptions("user", "secret", "0445"); err != nil || got != "username=user,password=secret,port=445" {
		t.Errorf("0445: got %q, %v", got, err)
	}
}

// A password reaches the mount whole, whatever commas it holds: the kernel splits the options at each
// comma, and the password "se,cret,uid=0" mounted the share as root's with the password "se". A user name
// or a port that would add an option is refused.
func TestAPasswordCannotAddAMountOption(t *testing.T) {
	for _, password := range []string{"se,cret,uid=0", ",uid=0", "a,,b,", "x=y"} {
		options, err := MountOptions("user", password, "445")
		if err != nil {
			t.Fatal(err)
		}
		if got, want := splitLikeTheKernel(options), map[string]string{"username": "user", "password": password, "port": "445"}; !reflect.DeepEqual(got, want) {
			t.Errorf("%q: the kernel reads %v from %q", password, got, options)
		}
	}
	for _, bad := range [][2]string{{"user,uid=0", "445"}, {"user\nuid=0", "445"}, {"user\x00", "445"}, {"user", "445,uid=0"}, {"user", "smb"}} {
		if options, err := MountOptions(bad[0], "secret", bad[1]); err == nil {
			t.Errorf("user %q, port %q: %q", bad[0], bad[1], options)
		}
	}
}

// splitLikeTheKernel reads mount options the way the cifs module does: split at each comma, a doubled
// comma being a comma within the value (smb3_fs_context_parse_monolithic; the older parser did the same
// for the password).
func splitLikeTheKernel(options string) map[string]string {
	got := map[string]string{}
	tokens := strings.Split(options, ",")
	for i := 0; i < len(tokens); i++ {
		option := tokens[i]
		for i+2 < len(tokens) && tokens[i+1] == "" {
			option += "," + tokens[i+2]
			i += 2
		}
		if i+2 == len(tokens) && tokens[i+1] == "" { // a doubled comma at the very end
			option += ","
			i++
		}
		key, value, _ := strings.Cut(option, "=")
		got[key] = value
	}
	return got
}
