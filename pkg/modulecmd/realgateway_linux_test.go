//go:build linux

package modulecmd

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS-Common/model"
)

// The assistant against the real gateway, the binary that ReCasaOS/CasaOS-Gateway builds, with the real files, in a copy of a Debian chroot that is to be spared (the test writes
// /etc/casaos/gateway.ini and the runtime path of the gateway, and runs the gateway there; the file /.rv-chroot, which only a copy that was made for this has, says that it is one):
//
//	CASAOS_MODULECMD_REAL_GATEWAY=/usr/local/bin/casaos-gateway <this test binary> -test.run TestTheRealGateway
//
// It is run as root. A user that is not root is the child of the test, which asks the same questions.

func realGatewayBinary(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("CASAOS_MODULECMD_REAL_GATEWAY")
	if bin == "" {
		t.Skip("needs the binary of the gateway, in a chroot to spare (CASAOS_MODULECMD_REAL_GATEWAY=/usr/local/bin/casaos-gateway)")
	}
	if _, err := os.Stat("/.rv-chroot"); err != nil || os.Geteuid() != 0 {
		t.Skip("not a copy of a chroot made for this, or not root")
	}
	return bin
}

// selfSigned writes a certificate and its key for the gateway to serve with
func selfSigned(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "gateway"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, "gw.crt"), filepath.Join(dir, "gw.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

type runningGateway struct {
	cmd  *exec.Cmd
	out  bytes.Buffer
	port string
}

func (g *runningGateway) stop() {
	if g.cmd.Process != nil {
		_ = g.cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = g.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = g.cmd.Process.Kill()
			<-done
		}
	}
}

// startGateway writes the configuration and starts the binary; it returns when the public port answers /ping.
func startGateway(t *testing.T, bin, ini, host, port string, tlsOn bool) *runningGateway {
	t.Helper()
	const runtime = "/var/run/casaos"
	_ = os.RemoveAll(runtime)
	if err := os.MkdirAll("/etc/casaos", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/etc/casaos/gateway.ini", []byte(ini), 0o644); err != nil {
		t.Fatal(err)
	}
	www := t.TempDir()
	g := &runningGateway{cmd: exec.Command(bin, "-w", www), port: port}
	g.cmd.Dir = "/tmp" // (a file called gateway in the directory it runs in would be read as its configuration)
	g.cmd.Stdout, g.cmd.Stderr = &g.out, &g.out
	if err := g.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.stop)
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	scheme := "http"
	if tlsOn {
		scheme = "https"
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		resp, err := client.Get(scheme + "://" + net.JoinHostPort(host, port) + "/ping")
		if err == nil {
			ok := resp.StatusCode == 200
			_ = resp.Body.Close()
			if _, statErr := os.Stat(runtime + "/management.url"); ok && statErr == nil {
				return g
			}
		}
	}
	t.Fatalf("the gateway did not come up:\n%s", g.out.String())
	return nil
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return port
}

func gatewayINI(port, address, cert, key string) string {
	return fmt.Sprintf("[common]\nruntimepath=/var/run/casaos\n\n[gateway]\nport=%s\naddress=%s\ntlscert=%s\ntlskey=%s\nhttpsport=0\nLogPath=/tmp/rv-gateway-log\n", port, address, cert, key)
}

// registerRoute registers a route the way the services do, with the secret of the boot
func registerRoute(t *testing.T, route, target string) {
	t.Helper()
	svc, err := external.NewManagementService("/var/run/casaos")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CreateRoute(&model.Route{Path: route, Target: target}); err != nil {
		t.Fatal(err)
	}
}

func answersNow(t *testing.T, route string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ok, err := newGateway(Roots{}).Answers(ctx, route)
	if err != nil {
		t.Fatalf("answers: %v", err)
	}
	return ok
}

func TestTheRealGatewayIsAskedWhereItIsAndWhatItHas(t *testing.T) {
	bin := realGatewayBinary(t)
	port := freePort(t)
	certFile, keyFile := selfSigned(t, t.TempDir())

	for _, c := range []struct {
		name, host, address string
		tls                 bool
	}{
		{"every interface, plain", "127.0.0.1", "", false},
		{"one address, which is not 127.0.0.1", "127.0.0.2", "127.0.0.2", false},
		{"a certificate and a key", "127.0.0.1", "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			cert, key := "", ""
			if c.tls {
				cert, key = certFile, keyFile
			}
			gw := startGateway(t, bin, gatewayINI(port, c.address, cert, key), c.host, port, c.tls)

			// what the gateway says of a path nobody registered: the static server's 404
			client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
			scheme := "http"
			if c.tls {
				scheme = "https"
			}
			resp, err := client.Get(scheme + "://" + net.JoinHostPort(c.host, port) + testRoute)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != 404 {
				t.Fatalf("an unregistered route: %d %s", resp.StatusCode, body)
			}
			if answersNow(t, testRoute) {
				t.Error("an unregistered route answers")
			}

			// a module behind the route
			var status atomic.Int32
			status.Store(200)
			module := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(int(status.Load())) }))
			defer module.Close()
			registerRoute(t, testRoute, module.URL)
			if !answersNow(t, testRoute) {
				t.Errorf("a registered route with a module behind it does not answer\n%s", gw.out.String())
			}
			status.Store(404)
			if !answersNow(t, testRoute) {
				t.Error("a module that says 404 for the bare route does not answer")
			}
			status.Store(503)
			if answersNow(t, testRoute) {
				t.Error("a module that is not ready answers")
			}
			status.Store(200)
			module.Close()
			if answersNow(t, testRoute) {
				t.Error("a module that is gone answers")
			}

			// the same questions from a user that is not root: it cannot read the table, and a 404 is nothing
			module2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(int(status.Load())) }))
			defer module2.Close()
			registerRoute(t, testRoute, module2.URL)
			for _, step := range []struct {
				status int
				route  string
				want   bool
			}{{200, testRoute, true}, {404, testRoute, false}, {200, "/v2/not_registered", false}, {503, testRoute, false}} {
				status.Store(int32(step.status))
				if got := asNobody(t, step.route); got != step.want {
					t.Errorf("as a user that is not root, route %s, module says %d: %v", step.route, step.status, got)
				}
			}
			gw.stop()
		})
	}
}

// asNobody asks the same question in a process of the user nobody, and gives the answer
func asNobody(t *testing.T, route string) bool {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestTheRealGatewayAsNobody$", "-test.count=1")
	cmd.Env = append(os.Environ(), "CASAOS_MODULECMD_GW_HELPER="+route)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the child: %v\n%s", err, out)
	}
	switch {
	case strings.Contains(string(out), "ANSWERS=true"):
		return true
	case strings.Contains(string(out), "ANSWERS=false"):
		return false
	}
	t.Fatalf("the child said: %s", out)
	return false
}

func TestTheRealGatewayAsNobody(t *testing.T) {
	route := os.Getenv("CASAOS_MODULECMD_GW_HELPER")
	if route == "" {
		t.Skip("the child of another test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ok, err := newGateway(Roots{}).Answers(ctx, route)
	if err != nil {
		t.Fatalf("answers: %v", err)
	}
	fmt.Printf("ANSWERS=%v\n", ok)
}
