package v1

import (
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
)

// A failed login used to be retried at once and forever, after the browser had gone too.
func TestWsSshEndsAtTheFirstFailedLogin(t *testing.T) {
	// WsSsh looks for an ssh binary first: any executable of that name will do.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	// a port nothing listens on: the login is refused at once
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	for _, query := range []string{
		"username=u&password=p&port=" + strconv.Itoa(port),
		"username=u&password=&port=" + strconv.Itoa(port),
	} {
		returned := make(chan struct{})
		e := echo.New()
		e.GET("/v1/sys/wsssh", func(ctx echo.Context) error {
			defer close(returned)
			return WsSsh(ctx)
		})
		server := httptest.NewServer(e)

		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/sys/wsssh?"+query, nil)
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		messages := 0
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
			if messages++; messages > 10 {
				conn.Close()
				server.Close()
				t.Fatalf("%s: the login is retried (%d messages and counting)", query, messages)
			}
		}
		conn.Close()
		select {
		case <-returned:
		case <-time.After(5 * time.Second):
			t.Errorf("%s: WsSsh did not return", query)
		}
		server.Close()
	}
}
