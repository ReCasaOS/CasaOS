package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startFake(t *testing.T) (sock string, st *Store, client *http.Client) {
	t.Helper()
	dir := t.TempDir()
	sock = filepath.Join(dir, "docker.sock")
	var err error
	st, err = Open(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = Serve(ln, st) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("unix", sock)
		if err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	client = &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
		},
	}
	return sock, st, client
}

func TestPing(t *testing.T) {
	_, _, client := startFake(t)
	res, err := client.Get("http://docker/_ping")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d %s", res.StatusCode, body)
	}
	if string(body) != "OK" {
		t.Fatalf("body %q", body)
	}
	if got := res.Header.Get("API-Version"); got != "1.44" {
		t.Fatalf("API-Version %q", got)
	}
}

func TestVersion(t *testing.T) {
	_, _, client := startFake(t)
	res, err := client.Get("http://docker/version")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var v struct {
		ApiVersion string `json:"ApiVersion"`
	}
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if v.ApiVersion != "1.44" {
		t.Fatalf("ApiVersion %q", v.ApiVersion)
	}
}

func TestVolumesListEmpty(t *testing.T) {
	_, _, client := startFake(t)
	res, err := client.Get("http://docker/v1.44/volumes")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	var body struct {
		Volumes []any `json:"Volumes"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Volumes == nil {
		t.Fatal("Volumes is null")
	}
	if len(body.Volumes) != 0 {
		t.Fatalf("listed %d", len(body.Volumes))
	}
}

func TestUnknownPath501(t *testing.T) {
	_, _, client := startFake(t)
	res, err := client.Get("http://docker/v1.44/no-such-endpoint")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func TestCreateListDownAndPersist(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "docker.sock")
	data := filepath.Join(dir, "data")
	yamlPath := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(yamlPath, []byte("services:\n  web:\n    image: alpine:latest\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	start := func() (*http.Client, func()) {
		st, err := Open(data)
		if err != nil {
			t.Fatal(err)
		}
		ln, err := Listen(sock)
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = Serve(ln, st) }()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			c, err := net.Dial("unix", sock)
			if err == nil {
				_ = c.Close()
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		client := &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
		}}
		return client, func() { _ = ln.Close() }
	}

	client, stop := start()
	body := `{"Image":"alpine:latest","Labels":{"com.docker.compose.project":"demo","com.docker.compose.project.config_files":"` + yamlPath + `","com.docker.compose.service":"web"}}`
	req, err := http.NewRequest(http.MethodPost, "http://docker/v1.44/containers/create?name=demo-web-1", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK {
		t.Fatalf("create status %d", res.StatusCode)
	}

	startReq, _ := http.NewRequest(http.MethodPost, "http://docker/v1.44/containers/demo-web-1/start", nil)
	startRes, err := client.Do(startReq)
	if err != nil {
		t.Fatal(err)
	}
	startRes.Body.Close()

	listRes, err := client.Get("http://docker/v1.44/containers/json?all=1")
	if err != nil {
		t.Fatal(err)
	}
	var listed []struct {
		State  string            `json:"State"`
		Labels map[string]string `json:"Labels"`
		Names  []string          `json:"Names"`
	}
	if err := json.NewDecoder(listRes.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	listRes.Body.Close()
	if len(listed) != 1 {
		t.Fatalf("listed %d", len(listed))
	}
	if listed[0].State != "running" {
		t.Fatalf("state %q", listed[0].State)
	}
	copied := filepath.Join(data, "demo", "docker-compose.yml")
	if _, err := os.Stat(copied); err != nil {
		t.Fatalf("yaml not copied: %v", err)
	}

	stop()
	client, stop = start()
	t.Cleanup(stop)
	listRes, err = client.Get("http://docker/v1.44/containers/json?all=1")
	if err != nil {
		t.Fatal(err)
	}
	listed = nil
	if err := json.NewDecoder(listRes.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	listRes.Body.Close()
	if len(listed) != 1 {
		t.Fatalf("after restart listed %d", len(listed))
	}

	id := strings.TrimPrefix(listed[0].Names[0], "/")
	delReq, _ := http.NewRequest(http.MethodDelete, "http://docker/v1.44/containers/"+id+"?force=1", nil)
	delRes, err := client.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	delRes.Body.Close()
	listRes, err = client.Get("http://docker/v1.44/containers/json?all=1")
	if err != nil {
		t.Fatal(err)
	}
	listed = nil
	if err := json.NewDecoder(listRes.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	listRes.Body.Close()
	if len(listed) != 0 {
		t.Fatalf("after delete listed %d", len(listed))
	}
}

func TestCreateContainerNoListOnComposeCopyFailure(t *testing.T) {
	_, st, client := startFake(t)
	body := `{"Image":"alpine:latest","Labels":{"com.docker.compose.project":"demo","com.docker.compose.project.config_files":"/no/such/docker-compose.yml"}}`
	req, err := http.NewRequest(http.MethodPost, "http://docker/v1.44/containers/create?name=demo-web-1", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("create status %d", res.StatusCode)
	}
	if len(st.List()) != 0 {
		t.Fatalf("store has %d containers after failed create", len(st.List()))
	}
	listRes, err := client.Get("http://docker/v1.44/containers/json?all=1")
	if err != nil {
		t.Fatal(err)
	}
	defer listRes.Body.Close()
	var listed []any
	if err := json.NewDecoder(listRes.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("listed %d after failed create", len(listed))
	}
}

func TestEventsStatus200WhileStreamOpen(t *testing.T) {
	_, _, client := startFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/v1.44/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		res, err := client.Do(req)
		if err != nil {
			errCh <- err
			return
		}
		resCh <- res
	}()
	select {
	case err := <-errCh:
		t.Fatal(err)
	case res := <-resCh:
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status %d", res.StatusCode)
		}
		cancel()
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for /events status line")
	}
}
