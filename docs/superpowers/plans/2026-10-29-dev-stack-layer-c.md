# Local process stack layer C Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The unprivileged local stack talks to a record-keeping fake Docker Engine and a fixture `lsblk`, never the host `/var/run/docker.sock`, and never mergerfs/FUSE.

**Architecture:** `dev/fakedocker` serves Engine HTTP on `$DEVROOT/run/docker.sock`. LocalStorage reads `CASAOS_LSBLK_FILE` and no-ops disk tools when `CASAOS_FAKE_STORAGE=1`. AppManagement relocates `gitAppsDataRoot` via `CASAOS_GIT_APPS_DATA`. `stack.sh` always overwrites `DOCKER_HOST` to the stack socket.

**Tech Stack:** Go (as in each repo's `go.mod`), bash. No new Go or npm modules. Fake Engine is stdlib `net/http` over a unix socket.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-10-29-dev-stack-layer-c-design.md`
- Work in the existing checkouts (`/home/ikanat/GIT/CasaOS`, `CasaOS-AppManagement`, `CasaOS-LocalStorage`). Sibling `../CasaOS-*` paths. Do not use a git worktree.
- If CasaOS-LocalStorage is not on `feat/dev-stack`, create that branch from its current HEAD. Do not switch CasaOS or AppManagement off `feat/dev-stack`.
- Linux for `stack.sh` and unix-socket tests. Other `go test` run anywhere unix sockets work.
- No root. Do not create `/DATA` or `/var/lib/casaos`. No FUSE, mergerfs, or user-namespace bind. `EnableMergerFS=false` stays.
- Never fall back to `/var/run/docker.sock` or a parent `DOCKER_HOST`.
- No new npm or Go module dependencies (do not add compose v5 to the CasaOS module).
- Combined README/CHANGELOG is out of scope.
- Do not commit unless the user asks. Skip every Commit step.

## File map

| File | Responsibility |
|---|---|
| CasaOS `dev/fakedocker/main.go` | flags `-socket` `-data`; `Listen` + `Serve` |
| CasaOS `dev/fakedocker/store.go` | JSON state, compose YAML copy, containers/networks/volumes |
| CasaOS `dev/fakedocker/server.go` | Engine HTTP subset; unknown path 501 |
| CasaOS `dev/writeconf.sh` | `fake-disks.json`, `data/AppData` |
| CasaOS `dev/stack.sh` | wipe stack docker.sock, export env, spawn fake-docker |
| LocalStorage `pkg/utils/command/command_helper.go` | `CASAOS_LSBLK_FILE`, `CASAOS_FAKE_STORAGE` |
| AppManagement `service/datapath.go` | `ApplyGitAppsDataRoot` |
| AppManagement `main.go` | call it after `ApplyDataPath` |
| AppManagement `service/fakedocker_compose_test.go` | compose v5 `Up` against sibling fake |

---

### Task 1: fake-docker ping, version, 501

**Files:**
- Create: `/home/ikanat/GIT/CasaOS/dev/fakedocker/main.go`
- Create: `/home/ikanat/GIT/CasaOS/dev/fakedocker/store.go`
- Create: `/home/ikanat/GIT/CasaOS/dev/fakedocker/server.go`
- Test: `/home/ikanat/GIT/CasaOS/dev/fakedocker/server_test.go`

**Interfaces:**
- Consumes: nothing
- Produces: `func Open(dir string) (*Store, error)`; `func Listen(socket string) (net.Listener, error)`; `func Serve(l net.Listener, st *Store) error`; unix Engine HTTP; `GET /_ping` → `OK` with header `API-Version: 1.44`; `GET /version` and `GET /v1.44/version` JSON; unknown path **501**. `flag.Parse` only in `main`, never `init`.

Work in `/home/ikanat/GIT/CasaOS`. Package `main` in `dev/fakedocker` (required for `go run ./dev/fakedocker`). Do not call `flag.Parse` in `init` (CasaOS `package main` tests already break on that).

- [ ] **Step 1: Write the failing test**

Create `dev/fakedocker/server_test.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./dev/fakedocker/ -count=1`

Expected: FAIL, `Open` / `Listen` / `Serve` undefined.

- [ ] **Step 3: Write minimal implementation**

`dev/fakedocker/store.go`:

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type Container struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	Status  string            `json:"status"`
	Labels  map[string]string `json:"labels"`
	Project string            `json:"project"`
}

type Network struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}

type Volume struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}

type snapshot struct {
	Containers map[string]Container `json:"containers"`
	Networks   map[string]Network   `json:"networks"`
	Volumes    map[string]Volume    `json:"volumes"`
	Seq        int                  `json:"seq"`
}

type Store struct {
	mu         sync.Mutex
	dir        string
	containers map[string]Container
	networks   map[string]Network
	volumes    map[string]Volume
	seq        int
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	st := &Store{
		dir:        dir,
		containers: map[string]Container{},
		networks:   map[string]Network{},
		volumes:    map[string]Volume{},
	}
	raw, err := os.ReadFile(st.path())
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return nil, err
	}
	var snap snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	if snap.Containers != nil {
		st.containers = snap.Containers
	}
	if snap.Networks != nil {
		st.networks = snap.Networks
	}
	if snap.Volumes != nil {
		st.volumes = snap.Volumes
	}
	st.seq = snap.Seq
	return st, nil
}

func (s *Store) path() string { return filepath.Join(s.dir, "state.json") }

func (s *Store) saveLocked() error {
	snap := snapshot{Containers: s.containers, Networks: s.networks, Volumes: s.volumes, Seq: s.seq}
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(), raw, 0o600)
}
```

`dev/fakedocker/server.go`:

```go
package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"
)

func Listen(socket string) (net.Listener, error) {
	_ = os.Remove(socket)
	return net.Listen("unix", socket)
}

func Serve(l net.Listener, st *Store) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/_ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", "1.44")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("OK"))
	})
	h := &engine{st: st}
	mux.Handle("/", h)
	return http.Serve(l, mux)
}

type engine struct {
	st *Store
}

func stripVersion(path string) string {
	p := strings.TrimPrefix(path, "/")
	if strings.HasPrefix(p, "v1.") {
		if i := strings.Index(p, "/"); i >= 0 {
			return p[i:]
		}
		return "/"
	}
	return "/" + p
}

func (e *engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := stripVersion(r.URL.Path)
	if r.Method == http.MethodGet && (path == "/version" || r.URL.Path == "/version") {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"Version":    "27.0.0-fakedocker",
			"ApiVersion": "1.44",
			"Os":         "linux",
			"Arch":       "amd64",
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": "not implemented: " + r.Method + " " + r.URL.Path})
}
```

`dev/fakedocker/main.go`:

```go
package main

import (
	"flag"
	"log"
	"os"
)

func main() {
	socket := flag.String("socket", "", "unix socket path")
	data := flag.String("data", "", "state directory")
	flag.Parse()
	if *socket == "" || *data == "" {
		log.Fatal("usage: fakedocker -socket PATH -data DIR")
	}
	st, err := Open(*data)
	if err != nil {
		log.Fatal(err)
	}
	ln, err := Listen(*socket)
	if err != nil {
		log.Fatal(err)
	}
	if err := Serve(ln, st); err != nil {
		log.Println(err)
		os.Exit(1)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./dev/fakedocker/ -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

Skip unless the user asks.

---

### Task 2: fake-docker records, list, down, persist

**Files:**
- Modify: `/home/ikanat/GIT/CasaOS/dev/fakedocker/store.go`
- Modify: `/home/ikanat/GIT/CasaOS/dev/fakedocker/server.go`
- Modify: `/home/ikanat/GIT/CasaOS/dev/fakedocker/server_test.go`

**Interfaces:**
- Consumes: `Open`, `Listen`, `Serve` from Task 1
- Produces: Engine `POST /containers/create`, `GET /containers/json`, `GET /containers/{id}/json`, `POST /containers/{id}/start|stop`, `DELETE /containers/{id}`; compose YAML copied to `<data>/<project>/docker-compose.yml` when create labels include `com.docker.compose.project` and `com.docker.compose.project.config_files`; process restart with the same `-data` still lists the project.

Work in `/home/ikanat/GIT/CasaOS`.

- [ ] **Step 1: Write the failing test**

Append to `server_test.go`:

```go
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
```

Add `"os"` and `"strings"` to the test imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./dev/fakedocker/ -run TestCreateListDownAndPersist -count=1`

Expected: FAIL (create 501).

- [ ] **Step 3: Write minimal implementation**

Extend `Store` with (all methods lock `s.mu` and call `saveLocked` on mutation):

```go
func (s *Store) nextIDLocked() string {
	s.seq++
	return fmt.Sprintf("%012d", s.seq)
}

func (s *Store) CreateContainer(name, image string, labels map[string]string) (Container, error)
func (s *Store) Start(id string) error   // Status = "running"; match by ID or Name
func (s *Store) Stop(id string) error    // Status = "exited"
func (s *Store) Remove(id string) error
func (s *Store) Get(id string) (Container, bool)
func (s *Store) List() []Container
func (s *Store) copyComposeYAMLLocked(labels map[string]string) error
```

`CreateContainer`: if `name == ""`, use the id. `Status` starts as `"created"`. Project = `labels["com.docker.compose.project"]`. Call `copyComposeYAMLLocked`: if project and `com.docker.compose.project.config_files` are set, mkdir `filepath.Join(s.dir, project)` and copy the first comma-separated path to `docker-compose.yml`.

In `engine.ServeHTTP`, handle after version:

| method | path | result |
|---|---|---|
| GET | `/info` | JSON `{"ID":"fakedocker","NCPU":1,"MemTotal":1,"OSType":"linux","Architecture":"x86_64","ServerVersion":"27.0.0-fakedocker"}` |
| GET | `/containers/json` | JSON array of `{Id,Names,Image,State,Status,Labels}` (`State` is `Status`; `Names` is `["/"+Name]`) |
| POST | `/containers/create` | decode `Image` + `Labels`; query `name`; 201 `{"Id":"...","Warnings":[]}` |
| GET | `/containers/{id}/json` | inspect JSON with `Id`, `Name`, `State.Status`, `State.Running`, `Config.Image`, `Config.Labels`, empty `HostConfig`/`NetworkSettings` |
| POST | `/containers/{id}/start` | 204 |
| POST | `/containers/{id}/stop` | 204 |
| DELETE | `/containers/{id}` | 204 |
| POST | `/containers/{id}/wait` | hold until `r.Context()` done, then `{"StatusCode":0}` (do **not** return immediately or compose thinks the container exited) |
| GET | `/images/{name}/json` | 200 `{"Id":"sha256:"+name,"RepoTags":[name]}` — `{name}` may contain `/` and `:`; use `strings.TrimPrefix(path, "/images/")` then `strings.TrimSuffix(..., "/json")` |
| POST | `/images/create` | 200 stream `{"status":"Pull complete"}\n` (no registry) |
| GET | `/networks` | `{"Networks": map or list of recorded networks}` — use `[]` of `{Id,Name,Labels}` if easier, matching Engine: `GET /networks` returns an array |
| POST | `/networks/create` | 201 `{"Id":"...","Warning":""}`; body `Name`, `Labels` |
| GET | `/networks/{id}` | inspect `{Id,Name,Labels,Containers:{}}` |
| DELETE | `/networks/{id}` | 204 |
| GET | `/volumes/{name}` | 200 or 404 `{"message":"..."}` with Engine not-found shape |
| POST | `/volumes/create` | 201 `{Name,Labels}` |
| DELETE | `/volumes/{name}` | 204 |
| GET | `/events` | 200 `Content-Type: application/json`; keep the body open until `r.Context()` done (write nothing) |

Lookup `{id}` by exact ID, exact Name, or unique prefix.

404 for missing container/network/volume: `{"message":"No such ..."}` status 404.

Keep the 501 default last.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./dev/fakedocker/ -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

Skip unless the user asks.

---

### Task 3: compose v5 Up against the fake

**Files:**
- Modify: `/home/ikanat/GIT/CasaOS/dev/fakedocker/server.go` (only if the discovery test reports 501/decode errors)
- Test: `/home/ikanat/GIT/CasaOS-AppManagement/service/fakedocker_compose_test.go`

**Interfaces:**
- Consumes: `go run ./dev/fakedocker -socket -data` from Task 1–2
- Produces: compose v5 `Up` of a one-service named-image file succeeds with no registry pull; `List` shows the project `running`; YAML exists under the fake `-data/<project>/`.

Work in `/home/ikanat/GIT/CasaOS-AppManagement` for the test. Grow CasaOS `dev/fakedocker` until it passes. Do not add compose v5 to the CasaOS module.

- [ ] **Step 1: Write the failing test**

Create `service/fakedocker_compose_test.go` (`package service_test`):

```go
package service_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/docker/compose/v5/pkg/api"
)

func TestComposeUpAgainstFakeDocker(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("Caller")
	}
	amRoot := filepath.Join(filepath.Dir(thisFile), "..")
	casaos := filepath.Join(amRoot, "..", "CasaOS")
	fakeDir := filepath.Join(casaos, "dev", "fakedocker")
	if _, err := os.Stat(fakeDir); err != nil {
		t.Skip("sibling CasaOS/dev/fakedocker missing")
	}

	dir := t.TempDir()
	sock := filepath.Join(dir, "docker.sock")
	data := filepath.Join(dir, "data")
	cmd := exec.Command("go", "run", "./dev/fakedocker", "-socket", sock, "-data", data)
	cmd.Dir = casaos
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("unix", sock)
		if err == nil {
			_ = c.Close()
			break
		}
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			t.Fatal("fakedocker exited")
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Setenv("DOCKER_HOST", "unix://"+sock)
	t.Setenv("DOCKER_API_VERSION", "")

	project := "fakedemo"
	composePath := filepath.Join(dir, "docker-compose.yml")
	yaml := []byte("name: " + project + "\nservices:\n  web:\n    image: alpine:latest\n    command: [\"sleep\", \"infinity\"]\n")
	if err := os.WriteFile(composePath, yaml, 0o600); err != nil {
		t.Fatal(err)
	}
	app, err := service.LoadComposeAppFromConfigFile(project, composePath)
	if err != nil {
		t.Fatal(err)
	}
	svc, dockerClient, err := service.ApiService()
	if err != nil {
		t.Fatal(err)
	}
	defer dockerClient.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := svc.Up(ctx, (*codegen.ComposeApp)(app), api.UpOptions{
		Create: api.CreateOptions{},
		Start:  api.StartOptions{Wait: true},
	}); err != nil {
		t.Fatal(err)
	}
	stacks, err := svc.List(ctx, api.ListOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range stacks {
		if s.ID == project || s.Name == project {
			found = true
			if s.Status != "running" {
				t.Fatalf("status %q", s.Status)
			}
		}
	}
	if !found {
		t.Fatalf("project %q not listed: %+v", project, stacks)
	}
	if _, err := os.Stat(filepath.Join(data, project, "docker-compose.yml")); err != nil {
		t.Fatalf("yaml: %v", err)
	}
}
```

`LoadComposeAppFromConfigFile` accepts a file with no `x-casaos`. Do not add that extension.

- [ ] **Step 2: Run test to verify it fails**

Run from `/home/ikanat/GIT/CasaOS-AppManagement`: `go test ./service/ -run TestComposeUpAgainstFakeDocker -count=1 -timeout 60s`

Expected: FAIL or skip-then-fail on 501 / decode. If it Skips, the sibling path is wrong — fix the path, do not skip past a present `dev/fakedocker`.

- [ ] **Step 3: Grow fake-docker until Up passes**

Log 501s (`log.Printf("%s %s", r.Method, r.URL.Path)`) on the default branch. Add the missing Engine path with the smallest JSON the client accepts. Typical extras compose v5 hits beyond Task 2:

- `POST /containers/{id}/resize` → 200 empty
- `GET /containers/{id}/logs` → 200 empty
- `POST /networks/{id}/connect` → 200
- `GET /images/json` → `[]`
- Engine not-found errors must be `{"message":"..."}` with status 404 so `errdefs.IsNotFound` works
- Container inspect `State.Health` omitted (no healthcheck in the fixture)
- `HostConfig.RestartPolicy` `{"Name":"no"}` if decode requires it

Do not pull from a registry. Do not publish host ports. Do not exec in a container.

If `Start` with `Wait: true` blocks on events: on `POST .../start`, write one JSON event to any open `/events` stream (`{"status":"start","id":"...","Type":"container","Action":"start"}`) and make inspect `State.Running == true` immediately.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./dev/fakedocker/ -count=1` (CasaOS)

Run: `go test ./service/ -run TestComposeUpAgainstFakeDocker -count=1 -timeout 60s` (AppManagement)

Expected: PASS. Existing `ApplyDataPath` tests still pass: `go test ./service/ -run 'TestApplyDataPath' -count=1`

- [ ] **Step 5: Commit**

Skip unless the user asks.

---

### Task 4: writeconf fake disks and AppData

**Files:**
- Modify: `/home/ikanat/GIT/CasaOS/dev/writeconf.sh`
- Modify: `/home/ikanat/GIT/CasaOS/dev/writeconf_test.go`

**Interfaces:**
- Consumes: existing `writeconf.sh DEVROOT CASAOS_REPO`
- Produces: `$DEVROOT/lib/fake-disks.json` (`blockdevices` length 2); `$DEVROOT/data/AppData` directory. Disks: `/dev/vda` (child `/dev/vda1` mounted `/`) and `/dev/vdb` (child `/dev/vdb1`, empty `mountpoint`), both `tran=sata`.

Work in `/home/ikanat/GIT/CasaOS`.

- [ ] **Step 1: Write the failing test**

Append inside `TestWriteconf` after the AppManagement `DataPath` assertions:

```go
	appData := filepath.Join(data, "AppData")
	if fi, err := os.Stat(appData); err != nil || !fi.IsDir() {
		t.Errorf("data/AppData: %v", err)
	}
	disksRaw, err := os.ReadFile(filepath.Join(lib, "fake-disks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disks struct {
		Blockdevices []map[string]any `json:"blockdevices"`
	}
	if err := json.Unmarshal(disksRaw, &disks); err != nil {
		t.Fatal(err)
	}
	if len(disks.Blockdevices) != 2 {
		t.Fatalf("blockdevices %d", len(disks.Blockdevices))
	}
```

Add `"encoding/json"` to the test imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./dev/ -count=1`

Expected: FAIL, `data/AppData` or `fake-disks.json` missing.

- [ ] **Step 3: Write minimal implementation**

In `writeconf.sh`, change the `mkdir -p` line to include `"$DATA/AppData"`.

After the last `cat >"$ETC/casaos.conf"` block, write `$LIB/fake-disks.json`:

```json
{
  "blockdevices": [
    {
      "name": "vda",
      "path": "/dev/vda",
      "type": "disk",
      "tran": "sata",
      "size": 10737418240,
      "ro": false,
      "rm": false,
      "hotplug": false,
      "serial": "FAKEVDA",
      "subsystems": "block:scsi:pci",
      "children": [
        {
          "name": "vda1",
          "path": "/dev/vda1",
          "type": "part",
          "fstype": "ext4",
          "mountpoint": "/",
          "size": 10737418240,
          "uuid": "11111111-1111-1111-1111-111111111111",
          "fssize": "10737418240",
          "fsused": "1073741824",
          "fsavail": "9663676416"
        }
      ]
    },
    {
      "name": "vdb",
      "path": "/dev/vdb",
      "type": "disk",
      "tran": "sata",
      "size": 21474836480,
      "ro": false,
      "rm": false,
      "hotplug": false,
      "serial": "FAKEVDB",
      "subsystems": "block:scsi:pci",
      "children": [
        {
          "name": "vdb1",
          "path": "/dev/vdb1",
          "type": "part",
          "fstype": "ext4",
          "mountpoint": "",
          "size": 21474836480,
          "uuid": "22222222-2222-2222-2222-222222222222",
          "fssize": "21474836480",
          "fsused": "0",
          "fsavail": "21474836480"
        }
      ]
    }
  ]
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./dev/ -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

Skip unless the user asks.

---

### Task 5: LocalStorage lsblk fixture and fake storage

**Files:**
- Modify: `/home/ikanat/GIT/CasaOS-LocalStorage/pkg/utils/command/command_helper.go`
- Test: `/home/ikanat/GIT/CasaOS-LocalStorage/pkg/utils/command/command_helper_test.go`

**Interfaces:**
- Consumes: env `CASAOS_LSBLK_FILE`, `CASAOS_FAKE_STORAGE`
- Produces: `ExecLSBLK` / `ExecLSBLKByPath` read the fixture when the file env is non-empty (no host `lsblk`); `ExecLSBLKByPath` returns `{"blockdevices":[matching]}` or empty list; unreadable file → `ExecLSBLK` returns `nil`; `CASAOS_FAKE_STORAGE=1` makes `ExecuteCommand` for `mount`,`umount`,`parted`,`partprobe`,`e2label`,`sfdisk` return `nil, nil`. Unset env keeps today’s exec. `ExecuteCommand("lsblk", …)` is **not** redirected to the fixture.

Work in `/home/ikanat/GIT/CasaOS-LocalStorage`. Create `feat/dev-stack` from current HEAD if that branch does not exist.

- [ ] **Step 1: Write the failing test**

Create `command_helper_test.go`:

```go
package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stubOnPATH(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const fixture = `{"blockdevices":[{"name":"vda","path":"/dev/vda","children":[{"name":"vda1","path":"/dev/vda1"}]},{"name":"vdb","path":"/dev/vdb"}]}`

func TestExecLSBLKFromFile(t *testing.T) {
	stubOnPATH(t, "lsblk", "echo CALLED >&2; exit 1")
	p := filepath.Join(t.TempDir(), "disks.json")
	if err := os.WriteFile(p, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CASAOS_LSBLK_FILE", p)
	got := ExecLSBLK()
	if string(got) != fixture {
		t.Fatalf("got %q", got)
	}
}

func TestExecLSBLKUnsetUsesHost(t *testing.T) {
	t.Setenv("CASAOS_LSBLK_FILE", "")
	marker := filepath.Join(t.TempDir(), "called")
	stubOnPATH(t, "lsblk", "echo -n '{\"blockdevices\":[]}' ; echo yes >"+marker)
	got := ExecLSBLK()
	if !strings.Contains(string(got), "blockdevices") {
		t.Fatalf("got %q", got)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("host lsblk not invoked")
	}
}

func TestExecLSBLKByPathFilters(t *testing.T) {
	stubOnPATH(t, "lsblk", "exit 1")
	p := filepath.Join(t.TempDir(), "disks.json")
	if err := os.WriteFile(p, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CASAOS_LSBLK_FILE", p)
	got := ExecLSBLKByPath("/dev/vdb")
	if !strings.Contains(string(got), `"path":"/dev/vdb"`) {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(string(got), "/dev/vda") {
		t.Fatalf("unfiltered %q", got)
	}
	none := ExecLSBLKByPath("/dev/vdz")
	if !strings.Contains(string(none), `"blockdevices":[]`) && !strings.Contains(string(none), `"blockdevices": []`) {
		t.Fatalf("empty match %q", none)
	}
}

func TestExecLSBLKUnreadable(t *testing.T) {
	t.Setenv("CASAOS_LSBLK_FILE", filepath.Join(t.TempDir(), "missing.json"))
	if got := ExecLSBLK(); got != nil {
		t.Fatalf("got %q", got)
	}
}

func TestFakeStoragePartedNoop(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "called")
	stubOnPATH(t, "parted", "echo yes >"+marker+"; exit 1")
	t.Setenv("CASAOS_FAKE_STORAGE", "1")
	out, err := ExecuteCommand("parted", "-s", "/dev/vdb", "print")
	if err != nil || out != nil {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("parted invoked")
	}
}

func TestPartedUnsetExecs(t *testing.T) {
	t.Setenv("CASAOS_FAKE_STORAGE", "")
	marker := filepath.Join(t.TempDir(), "called")
	stubOnPATH(t, "parted", "echo -n ok; echo yes >"+marker)
	out, err := ExecuteCommand("parted", "-s", "/dev/vdb", "print")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "ok" {
		t.Fatalf("out %q", out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("parted not invoked")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/utils/command/ -count=1`

Expected: FAIL (`CASAOS_LSBLK_FILE` ignored; stub `lsblk` exit 1).

- [ ] **Step 3: Write minimal implementation**

In `command_helper.go` add `"encoding/json"` and `"os"`.

```go
func lsblkFile() string { return os.Getenv("CASAOS_LSBLK_FILE") }

func fakeStorage() bool { return os.Getenv("CASAOS_FAKE_STORAGE") == "1" }

func readLSBLKFile() []byte {
	p := lsblkFile()
	if p == "" {
		return nil
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	return raw
}

func filterLSBLK(raw []byte, path string) []byte {
	var doc struct {
		Blockdevices []map[string]any `json:"blockdevices"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return []byte(`{"blockdevices":[]}`)
	}
	var match []map[string]any
	for _, d := range doc.Blockdevices {
		name, _ := d["name"].(string)
		p, _ := d["path"].(string)
		if p == path || p == "/dev/"+strings.TrimPrefix(path, "/dev/") || "/dev/"+name == path || name == path {
			match = append(match, d)
			continue
		}
		children, _ := d["children"].([]any)
		for _, c := range children {
			cm, _ := c.(map[string]any)
			cn, _ := cm["name"].(string)
			cp, _ := cm["path"].(string)
			if cp == path || "/dev/"+cn == path || cn == path {
				match = append(match, cm)
			}
		}
	}
	out, err := json.Marshal(map[string]any{"blockdevices": match})
	if err != nil {
		return []byte(`{"blockdevices":[]}`)
	}
	return out
}
```

`ExecLSBLK`: if `lsblkFile() != ""` { `return readLSBLKFile()` } then existing exec.

`ExecLSBLKByPath`: if `lsblkFile() != ""` { raw := readLSBLKFile(); if raw == nil { return nil }; return filterLSBLK(raw, path) } then existing exec.

`ExecuteCommand`: if `fakeStorage()` and name is one of `mount`,`umount`,`parted`,`partprobe`,`e2label`,`sfdisk` { return nil, nil }. Do not special-case `lsblk`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/utils/command/ -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

Skip unless the user asks.

---

### Task 6: CASAOS_GIT_APPS_DATA

**Files:**
- Modify: `/home/ikanat/GIT/CasaOS-AppManagement/service/datapath.go`
- Modify: `/home/ikanat/GIT/CasaOS-AppManagement/service/datapath_test.go`
- Modify: `/home/ikanat/GIT/CasaOS-AppManagement/main.go` (after `ApplyDataPath`, before `NewService`)

**Interfaces:**
- Consumes: `ApplyDataPath` (unchanged; still does not touch `gitAppsDataRoot`)
- Produces: `func ApplyGitAppsDataRoot(v string)` — empty → `/DATA/AppData`; non-empty → that path. `main` calls `service.ApplyGitAppsDataRoot(os.Getenv("CASAOS_GIT_APPS_DATA"))`.

Work in `/home/ikanat/GIT/CasaOS-AppManagement`.

- [ ] **Step 1: Write the failing test**

Append to `datapath_test.go`:

```go
func TestApplyGitAppsDataRoot(t *testing.T) {
	prev := gitAppsDataRoot
	t.Cleanup(func() { gitAppsDataRoot = prev })

	ApplyGitAppsDataRoot("/tmp/x")
	if gitAppsDataRoot != "/tmp/x" {
		t.Fatalf("got %q", gitAppsDataRoot)
	}
	ApplyGitAppsDataRoot("")
	if gitAppsDataRoot != "/DATA/AppData" {
		t.Fatalf("empty got %q", gitAppsDataRoot)
	}
}
```

Keep `TestApplyDataPath` asserting `gitAppsDataRoot != "/DATA/AppData"` is a failure — that test must still pass without calling `ApplyGitAppsDataRoot`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./service/ -run 'TestApplyGitAppsDataRoot|TestApplyDataPath$' -count=1`

Expected: FAIL, `ApplyGitAppsDataRoot` undefined.

- [ ] **Step 3: Write minimal implementation**

In `datapath.go`:

```go
func ApplyGitAppsDataRoot(v string) {
	if v == "" {
		gitAppsDataRoot = "/DATA/AppData"
		return
	}
	gitAppsDataRoot = v
}
```

In `main.go` immediately after `root := service.ApplyDataPath(config.AppInfo.DataPath)`:

```go
		service.ApplyGitAppsDataRoot(os.Getenv("CASAOS_GIT_APPS_DATA"))
```

`os` is already imported in `main.go`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./service/ -run 'TestApplyGitAppsDataRoot|TestApplyDataPath' -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

Skip unless the user asks.

---

### Task 7: stack.sh wires fake-docker and env

**Files:**
- Modify: `/home/ikanat/GIT/CasaOS/dev/stack.sh`

**Interfaces:**
- Consumes: `dev/fakedocker` (Task 1–3), `writeconf` fixture (Task 4)
- Produces: wipe `$DEVROOT/run/docker.sock` with the other stale files (not `rclone.sock`); export `DOCKER_HOST=unix://$DEVROOT/run/docker.sock`, `CASAOS_LSBLK_FILE=$DEVROOT/lib/fake-disks.json`, `CASAOS_FAKE_STORAGE=1`, `CASAOS_GIT_APPS_DATA=$DEVROOT/data/AppData` (overwrite parent); spawn fake-docker via existing `spawn` before Gateway; wait 120s for the socket; on miss/exit/timeout one warning, continue, env stays; remove the host `/var/run/docker.sock` warning; PID in `PIDS` (SIGINT) but not `REQUIRED`.

Work in `/home/ikanat/GIT/CasaOS`. No unit test for `stack.sh` (spec). Verify by reading the script and running `go test ./dev/ ./dev/fakedocker/`.

- [ ] **Step 1: Change the wipe and exports**

Replace:

```bash
rm -f "$DEVROOT/run/"*.url "$DEVROOT/run/"*.pid "$DEVROOT/run/internal.secret"
```

with:

```bash
rm -f "$DEVROOT/run/"*.url "$DEVROOT/run/"*.pid "$DEVROOT/run/internal.secret" "$DEVROOT/run/docker.sock"
```

After `bash "$WRITECONF" ...` and the rclone exports, add:

```bash
export DOCKER_HOST="unix://$DEVROOT/run/docker.sock"
export CASAOS_LSBLK_FILE="$DEVROOT/lib/fake-disks.json"
export CASAOS_FAKE_STORAGE=1
export CASAOS_GIT_APPS_DATA="$DEVROOT/data/AppData"
```

- [ ] **Step 2: Spawn fake-docker before Gateway**

Immediately before `spawn "$DEVROOT/etc" "$PARENT/CasaOS-Gateway" ...`:

```bash
spawn "$CASAOS_REPO" ./dev/fakedocker -socket "$DEVROOT/run/docker.sock" -data "$DEVROOT/lib/fake-docker"
fake_pid="${PIDS[-1]}"
fake_ok=0
deadline=$((SECONDS + 120))
while (( SECONDS < deadline )); do
	if ! alive "$fake_pid"; then
		echo "warning: fake-docker exited; continuing" >&2
		break
	fi
	if [[ -S "$DEVROOT/run/docker.sock" ]]; then
		fake_ok=1
		break
	fi
	sleep 0.5
done
if [[ "$fake_ok" -eq 0 ]] && alive "$fake_pid"; then
	echo "warning: fake-docker did not create its socket; continuing" >&2
fi
```

Do not add `fake_pid` to `REQUIRED`.

- [ ] **Step 3: Remove the host Docker warning**

Delete:

```bash
	if [[ ! -S /var/run/docker.sock && -z "${DOCKER_HOST:-}" ]]; then
		echo "warning: no Docker socket; compose calls will fail" >&2
	fi
```

Do not mention `/var/run/docker.sock` in `stack.sh`.

- [ ] **Step 4: Run unit tests still passing**

Run: `go test ./dev/ ./dev/fakedocker/ -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

Skip unless the user asks.

---

## Verification (after all tasks)

```sh
# CasaOS
go test ./dev/ ./dev/fakedocker/ -count=1

# CasaOS-LocalStorage
go test ./pkg/utils/command/ -count=1

# CasaOS-AppManagement
go test ./service/ -run 'TestApplyDataPath|TestApplyGitAppsDataRoot|TestComposeUpAgainstFakeDocker' -count=1 -timeout 60s
```

Then `./dev/stack.sh` (or `pnpm dev:stack`):

- Storage lists two disks from the fixture, not the host.
- App Management compose list uses `$DEVROOT/run/docker.sock`.
- Installing a one-service compose app records YAML under `.dev-runtime/lib/fake-docker/<name>/` and shows running.
- AppManagement has no connection to `/var/run/docker.sock` (`lsof -p <pid>` / `ss`).
- `DOCKER_HOST=unix:///var/run/docker.sock ./dev/stack.sh` still uses the stack socket.

`go generate ./...` remains a one-time prerequisite when `codegen/` is missing.
