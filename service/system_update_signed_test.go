package service

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/autoupdate"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/ReCasaOS/CasaOS/pkg/installersig"
	"github.com/patrickmn/go-cache"
)

const testTag = "v0.5.200"

func installerScript(tag string) []byte {
	return []byte("#!/bin/bash\nreadonly CASAOS_INSTALL_RELEASE_TAG=\"" + tag + "\"\necho installing\n")
}

func signInstaller(key ed25519.PrivateKey, script []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, script)) + "\n")
}

func newInstallerKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// releaseServer serves files by path, 404 for any other, and records the paths asked.
type releaseServer struct {
	*httptest.Server
	mu    sync.Mutex
	files map[string][]byte
	asked []string
}

func (r *releaseServer) handle(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.asked = append(r.asked, req.URL.Path)
	body, ok := r.files[req.URL.Path]
	r.mu.Unlock()
	if !ok {
		http.NotFound(w, req)
		return
	}
	_, _ = w.Write(body)
}

func (r *releaseServer) paths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.asked...)
}

// newInstallerFixture points every seam of the update path at a test server and temporary
// folders, accepts key only, and serves a valid signed installer of testTag at the release's path.
func newInstallerFixture(t *testing.T, tls bool) (*releaseServer, ed25519.PrivateKey) {
	t.Helper()
	logger.LogInitConsoleOnly()
	pub, priv := newInstallerKey(t)
	script := installerScript(testTag)
	srv := &releaseServer{files: map[string][]byte{
		"/" + testTag + "/install.sh":     script,
		"/" + testTag + "/install.sh.sig": signInstaller(priv, script),
	}}
	if tls {
		srv.Server = httptest.NewTLSServer(http.HandlerFunc(srv.handle))
	} else {
		srv.Server = httptest.NewServer(http.HandlerFunc(srv.handle))
	}
	t.Cleanup(srv.Close)

	base, client, dir, release, keys := installerReleaseBase, installerClient, installerDir, installedReleaseFile, installersig.Keys
	updateURL := config.ServerInfo.UpdateUrl
	t.Cleanup(func() {
		installerReleaseBase, installerClient, installerDir, installedReleaseFile, installersig.Keys = base, client, dir, release, keys
		config.ServerInfo.UpdateUrl = updateURL
	})
	installerReleaseBase = srv.URL + "/"
	installerClient = srv.Client()
	installerDir = filepath.Join(t.TempDir(), "update")
	installedReleaseFile = filepath.Join(t.TempDir(), "fork-release") // absent: no lower bound
	installersig.Keys = []ed25519.PublicKey{pub}
	config.ServerInfo.UpdateUrl = ""
	t.Setenv(installerURLEnv, "")
	t.Setenv(installerTestKeyEnv, "")
	return srv, priv
}

const notSigned = "The update to " + testTag + " was not installed: its installer is not signed by ReCasaOS."

func wantRefused(t *testing.T, err error) {
	t.Helper()
	if err == nil || errors.Is(err, autoupdate.ErrBusy) || !errors.Is(err, autoupdate.ErrRefused) || err.Error() != notSigned {
		t.Fatalf("err = %v, want exactly %q, refused and not busy", err, notSigned)
	}
	if entries, _ := os.ReadDir(installerDir); len(entries) != 0 {
		t.Fatalf("%d files written for a refused installer", len(entries))
	}
}

func wantUnreachable(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, autoupdate.ErrBusy) || errors.Is(err, autoupdate.ErrRefused) {
		t.Fatalf("err = %v, want a download error (busy, not refused)", err)
	}
	if entries, _ := os.ReadDir(installerDir); len(entries) != 0 {
		t.Fatalf("%d files written for an installer not downloaded", len(entries))
	}
}

func TestASignedInstallerIsWrittenPrivateAndRunFromItsFile(t *testing.T) {
	srv, _ := newInstallerFixture(t, false)

	path, err := fetchVerifiedInstaller(t.Context(), testTag)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(installerDir, "install-"+testTag+".sh"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if got, _ := os.ReadFile(path); string(got) != string(installerScript(testTag)) {
		t.Fatalf("written %q", got)
	}
	for name, p := range map[string]string{"file": path, "folder": installerDir} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %v (%v), want 0700", name, info.Mode().Perm(), err)
		}
	}
	if got, want := srv.paths(), []string{"/" + testTag + "/install.sh", "/" + testTag + "/install.sh.sig"}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("asked %q, want %q", got, want)
	}

	args := detachedUpdateArgs(path, "/var/log/casaos/upgrade.log", testTag)
	if last := args[len(args)-1]; last != "exec >> '/var/log/casaos/upgrade.log' 2>&1; exec /bin/bash '"+path+"'" {
		t.Fatalf("the unit runs %q", last)
	}
	for _, arg := range args {
		if strings.Contains(arg, "curl") {
			t.Fatalf("systemd-run is given %q", arg)
		}
	}
}

func TestAnExistingFolderIsMadePrivateAndAnOldFileReplaced(t *testing.T) {
	newInstallerFixture(t, false)
	if err := os.MkdirAll(installerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(installerDir, "install-"+testTag+".sh")
	if err := os.WriteFile(old, []byte("old"), 0o666); err != nil {
		t.Fatal(err)
	}
	path, err := fetchVerifiedInstaller(t.Context(), testTag)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(installerDir); info.Mode().Perm() != 0o700 {
		t.Errorf("folder mode = %v, want 0700", info.Mode().Perm())
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o700 {
		t.Errorf("file mode = %v, want 0700", info.Mode().Perm())
	}
	if got, _ := os.ReadFile(path); string(got) != string(installerScript(testTag)) {
		t.Fatalf("written %q", got)
	}
}

func TestAnInstallerWithABadSignatureIsRefused(t *testing.T) {
	srv, _ := newInstallerFixture(t, false)
	_, other := newInstallerKey(t)
	srv.files["/"+testTag+"/install.sh.sig"] = signInstaller(other, installerScript(testTag))
	_, err := fetchVerifiedInstaller(t.Context(), testTag)
	wantRefused(t, err)

	srv.files["/"+testTag+"/install.sh.sig"] = []byte("not base64 at all\n")
	_, err = fetchVerifiedInstaller(t.Context(), testTag)
	wantRefused(t, err)
}

func TestAnInstallerThatCannotBeDownloadedIsBusyNotRefused(t *testing.T) {
	for name, change := range map[string]func(files map[string][]byte){
		"no signature": func(f map[string][]byte) { delete(f, "/"+testTag+"/install.sh.sig") },
		"no script":    func(f map[string][]byte) { delete(f, "/"+testTag+"/install.sh") },
		"a script over 5 MiB": func(f map[string][]byte) {
			f["/"+testTag+"/install.sh"] = append(installerScript(testTag), make([]byte, 5<<20)...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv, _ := newInstallerFixture(t, false)
			change(srv.files)
			_, err := fetchVerifiedInstaller(t.Context(), testTag)
			wantUnreachable(t, err)
		})
	}
	t.Run("no server", func(t *testing.T) {
		srv, _ := newInstallerFixture(t, false)
		srv.Close()
		_, err := fetchVerifiedInstaller(t.Context(), testTag)
		wantUnreachable(t, err)
	})
}

func TestAnInstallerOfAnotherReleaseIsRefused(t *testing.T) {
	for name, scriptTag := range map[string]string{
		"another release": "v0.5.199",
		"a placeholder":   "__RELEASE_TAG__",
	} {
		t.Run(name, func(t *testing.T) {
			srv, priv := newInstallerFixture(t, false)
			script := installerScript(scriptTag)
			srv.files["/"+testTag+"/install.sh"] = script
			srv.files["/"+testTag+"/install.sh.sig"] = signInstaller(priv, script)
			_, err := fetchVerifiedInstaller(t.Context(), testTag)
			wantRefused(t, err)
		})
	}
}

func TestAnInstallerOlderThanTheInstalledReleaseIsRefused(t *testing.T) {
	newInstallerFixture(t, false)
	if err := os.WriteFile(installedReleaseFile, []byte("v0.5.201\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := fetchVerifiedInstaller(t.Context(), testTag)
	wantRefused(t, err)

	// the same release again is not older
	if err := os.WriteFile(installedReleaseFile, []byte(testTag+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fetchVerifiedInstaller(t.Context(), testTag); err != nil {
		t.Fatalf("reinstalling the installed release: %v", err)
	}
}

// A core built before the release keys exist accepts no installer at all.
func TestNoKeyAcceptsNothing(t *testing.T) {
	newInstallerFixture(t, false)
	installersig.Keys = nil
	_, err := fetchVerifiedInstaller(t.Context(), testTag)
	wantRefused(t, err)
}

func TestTheTestKeyCountsOnlyWithTheInstallCheckInstaller(t *testing.T) {
	srv, priv := newInstallerFixture(t, false)
	installersig.Keys = nil
	pub := priv.Public().(ed25519.PublicKey)
	t.Setenv(installerTestKeyEnv, base64.StdEncoding.EncodeToString(pub))

	// the key alone, the release's own address: refused
	_, err := fetchVerifiedInstaller(t.Context(), testTag)
	wantRefused(t, err)

	// with the install check's installer, from its address and that address + .sig
	srv.files["/check/install.sh"] = srv.files["/"+testTag+"/install.sh"]
	srv.files["/check/install.sh.sig"] = srv.files["/"+testTag+"/install.sh.sig"]
	t.Setenv(installerURLEnv, srv.URL+"/check/install.sh")
	before := len(srv.paths())
	if _, err := fetchVerifiedInstaller(t.Context(), testTag); err != nil {
		t.Fatalf("the install check's signed installer: %v", err)
	}
	if got := srv.paths()[before:]; strings.Join(got, " ") != "/check/install.sh /check/install.sh.sig" {
		t.Fatalf("asked %q", got)
	}

	// a key that is not 32 bytes is no key
	if err := os.RemoveAll(installerDir); err != nil {
		t.Fatal(err)
	}
	t.Setenv(installerTestKeyEnv, base64.StdEncoding.EncodeToString(pub[:16]))
	_, err = fetchVerifiedInstaller(t.Context(), testTag)
	wantRefused(t, err)
}

func TestAConfiguredInstallerHasItsSignatureBesideIt(t *testing.T) {
	srv, _ := newInstallerFixture(t, true)
	srv.files["/custom/install.sh"] = srv.files["/"+testTag+"/install.sh"]
	srv.files["/custom/install.sh.sig"] = srv.files["/"+testTag+"/install.sh.sig"]
	config.ServerInfo.UpdateUrl = srv.URL + "/custom/install.sh"
	if _, err := fetchVerifiedInstaller(t.Context(), testTag); err != nil {
		t.Fatal(err)
	}
	if got := srv.paths(); strings.Join(got, " ") != "/custom/install.sh /custom/install.sh.sig" {
		t.Fatalf("asked %q", got)
	}
}

func TestATagThatIsNotOneGoesNowhere(t *testing.T) {
	srv, _ := newInstallerFixture(t, false)
	for _, tag := range []string{"v0.5.70/../x", "", "latest", "0.5.70"} {
		if _, err := fetchVerifiedInstaller(t.Context(), tag); err == nil {
			t.Errorf("tag %q: no error", tag)
		}
	}
	if got := srv.paths(); len(got) != 0 {
		t.Fatalf("asked %q", got)
	}
}

// newIdleSystem is a system service whose box is free, with systemd-run recorded, not run.
func newIdleSystem(t *testing.T) (*systemService, *[][]string) {
	t.Helper()
	updater := newTestSystemPackageUpdater(t)
	updater.command = moduleSystemd(nil)
	updater.dpkgLockState = func() (bool, error) { return false, nil }
	var runs [][]string
	original, cachePrevious := runSystemdRun, Cache
	t.Cleanup(func() { runSystemdRun, Cache = original, cachePrevious })
	Cache = cache.New(time.Minute, time.Minute)
	runSystemdRun = func(args ...string) ([]byte, error) { runs = append(runs, args); return nil, nil }
	return &systemService{packageUpdates: updater}, &runs
}

func TestTheButtonInstallsTheAnnouncedReleaseFromItsOwnFiles(t *testing.T) {
	srv, _ := newInstallerFixture(t, false)
	system, runs := newIdleSystem(t)
	original := latestReleaseTag
	t.Cleanup(func() { latestReleaseTag = original })
	latestReleaseTag = func() string { return strings.TrimPrefix(testTag, "v") } // a feed without the "v"

	if err := system.UpdateSystemVersion("whatever the dashboard sent"); err != nil {
		t.Fatal(err)
	}
	if got := srv.paths(); strings.Join(got, " ") != "/"+testTag+"/install.sh /"+testTag+"/install.sh.sig" {
		t.Fatalf("asked %q", got)
	}
	if len(*runs) != 1 {
		t.Fatalf("systemd-run ran %d times", len(*runs))
	}
	args := (*runs)[0]
	if last := args[len(args)-1]; !strings.HasSuffix(last, "exec /bin/bash '"+filepath.Join(installerDir, "install-"+testTag+".sh")+"'") {
		t.Fatalf("the unit runs %q", last)
	}

	// no release announced: nothing fetched, nothing run, and the night tries again
	latestReleaseTag = func() string { return "" }
	err := system.UpdateSystemVersion("")
	if !errors.Is(err, autoupdate.ErrBusy) || len(*runs) != 1 {
		t.Fatalf("err = %v, %d runs", err, len(*runs))
	}
}

func TestARefusedInstallerIsNotRun(t *testing.T) {
	srv, _ := newInstallerFixture(t, false)
	system, runs := newIdleSystem(t)
	delete(srv.files, "/"+testTag+"/install.sh.sig")
	if err := system.UpdateSystemRelease(testTag); !errors.Is(err, autoupdate.ErrBusy) {
		t.Fatalf("err = %v", err)
	}
	srv.files["/"+testTag+"/install.sh.sig"] = []byte("AAAA\n")
	if err := system.UpdateSystemRelease(testTag); err == nil || err.Error() != notSigned {
		t.Fatalf("err = %v", err)
	}
	if len(*runs) != 0 {
		t.Fatalf("systemd-run ran %v", *runs)
	}
}
