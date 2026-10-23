package samba

import (
	"net"
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
	if got, want := MountOptions("user", "secret", "4450"), "username=user,password=secret,port=4450"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := MountOptions("user", "secret", ""), "username=user,password=secret"; got != want {
		t.Errorf("without a port: got %q, want %q", got, want)
	}
}
