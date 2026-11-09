package share

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// callLog is shared by fakeACME and fakeTXT so a test sees their calls in the order they happened.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func newCallLog() *callLog { return &callLog{} }

func (l *callLog) add(s string) { l.mu.Lock(); l.calls = append(l.calls, s); l.mu.Unlock() }
func (l *callLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}
func (l *callLog) joined() string { return strings.Join(l.all(), " ") }

// fakeACME is an acmeAPI that records its calls. Fail maps a step name (Register, Order, DNS01, Accept,
// Finalize) to the error that step returns; Finalize answers with a self-signed leaf for the CSR's names,
// valid for Valid, and keeps it in Cert. Guard Fail and Cert with SetFail/Issued when a Loop is running.
type fakeACME struct {
	log   *callLog
	Valid time.Duration
	// AuthzValid makes DNS01 answer as for an authorization that is already valid: no challenge.
	AuthzValid bool

	mu   sync.Mutex
	fail map[string]error
	cert *x509.Certificate
	Keys []crypto.Signer // the account key of each newClient call
}

func newFakeACME(log *callLog) *fakeACME {
	return &fakeACME{log: log, fail: map[string]error{}, Valid: 90 * 24 * time.Hour}
}

// SetFail makes a step fail with err; nil makes it succeed again.
func (f *fakeACME) SetFail(step string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.fail, step)
	} else {
		f.fail[step] = err
	}
}

// Issued is the last certificate Finalize returned.
func (f *fakeACME) Issued() *x509.Certificate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cert
}

func (f *fakeACME) newClient(key crypto.Signer) acmeAPI {
	f.mu.Lock()
	f.Keys = append(f.Keys, key)
	f.mu.Unlock()
	return f
}

func (f *fakeACME) step(name, detail string) error {
	f.log.add(name + detail)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fail[name]
}

func (f *fakeACME) Register(ctx context.Context) error { return f.step("Register", "") }
func (f *fakeACME) Order(ctx context.Context, domain string) (string, []string, error) {
	return "order-1", []string{"authz-1"}, f.step("Order", ":"+domain)
}
func (f *fakeACME) DNS01(ctx context.Context, authzURL string) (string, string, error) {
	if err := f.step("DNS01", ":"+authzURL); err != nil || f.AuthzValid {
		return "", "", err
	}
	return "chal-1", "txt-value-1", nil
}
func (f *fakeACME) Accept(ctx context.Context, challengeURI, authzURL string) error {
	return f.step("Accept", "")
}
func (f *fakeACME) Finalize(ctx context.Context, orderURI string, csr []byte) ([][]byte, error) {
	if err := f.step("Finalize", ""); err != nil {
		return nil, err
	}
	req, err := x509.ParseCertificateRequest(csr)
	if err != nil {
		return nil, err
	}
	f.log.add("csr:" + strings.Join(req.DNSNames, ","))
	ca, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "fake"},
		DNSNames: req.DNSNames, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(f.Valid).Truncate(time.Second)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, req.PublicKey, ca)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.cert, _ = x509.ParseCertificate(der)
	f.mu.Unlock()
	return [][]byte{der}, nil
}

// fakeTXT is a TXT that records its calls; Err and ClearErr are what SetTXT and ClearTXT return.
type fakeTXT struct {
	log      *callLog
	Err      error
	ClearErr error
}

func newFakeTXT(log *callLog) *fakeTXT { return &fakeTXT{log: log} }

func (t *fakeTXT) SetTXT(ctx context.Context, value string) error {
	t.log.add("SetTXT:" + value)
	return t.Err
}
func (t *fakeTXT) ClearTXT(ctx context.Context) error { t.log.add("ClearTXT"); return t.ClearErr }

// newTestACME is an ACME writing in a temp dir, whose client is f.
func newTestACME(t *testing.T, f *fakeACME, txt TXT) *ACME {
	dir := t.TempDir()
	return &ACME{AccountFile: filepath.Join(dir, "fleet", "acme-account.json"),
		KeyFile: filepath.Join(dir, "tls", "share.key"), CertFile: filepath.Join(dir, "tls", "share.crt"),
		TXT: txt, newClient: f.newClient}
}

const testHost = "mybox.box.example.org"

func TestObtainRunsTheDNS01FlowInOrder(t *testing.T) {
	log := newCallLog()
	f := newFakeACME(log)
	a := newTestACME(t, f, newFakeTXT(log))
	if err := a.Obtain(context.Background(), testHost); err != nil {
		t.Fatal(err)
	}
	want := "Register Order:*.mybox.box.example.org DNS01:authz-1 SetTXT:txt-value-1 Accept ClearTXT Finalize csr:*.mybox.box.example.org"
	if got := log.joined(); got != want {
		t.Fatalf("calls:\n got %s\nwant %s", got, want)
	}
	ki, err := os.Stat(a.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	ci, err := os.Stat(a.CertFile)
	if err != nil {
		t.Fatal(err)
	}
	if ci.ModTime().Before(ki.ModTime()) {
		t.Error("the certificate was written before the key")
	}
	if blk, _ := pem.Decode(mustRead(t, a.KeyFile)); blk == nil || blk.Type != "EC PRIVATE KEY" {
		t.Error("share.key is not an EC private key PEM")
	}
	if st := a.Status(); !st.NotAfter.Equal(f.Issued().NotAfter) || st.Error != "" || st.Pending || len(st.Names) != 1 || st.Names[0] != "*."+testHost {
		t.Errorf("status = %+v", st)
	}
	ai, err := os.Stat(a.AccountFile)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && ai.Mode().Perm() != 0o600 {
		t.Errorf("account file mode = %v", ai.Mode().Perm())
	}
}

func TestObtainClearsTheTXTOnFailure(t *testing.T) {
	log := newCallLog()
	f := newFakeACME(log)
	f.SetFail("Accept", errors.New("boom"))
	a := newTestACME(t, f, newFakeTXT(log))
	if err := a.Obtain(context.Background(), testHost); err == nil {
		t.Fatal("expected an error")
	}
	if got := log.joined(); !strings.HasSuffix(got, "Accept ClearTXT") {
		t.Errorf("calls = %s", got)
	}
	for _, p := range []string{a.KeyFile, a.CertFile} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s written despite the failure", p)
		}
	}
}

type downErr struct{}

func (downErr) Error() string   { return "dial tcp: down" }
func (downErr) Timeout() bool   { return true }
func (downErr) Temporary() bool { return true }

func TestObtainErrorTexts(t *testing.T) {
	for _, c := range []struct {
		fail error
		want string
	}{
		{errors.New("acme: urn:ietf:params:acme:error:rateLimited: too many"), "Let's Encrypt refused: its limit of new certificates is reached, try again in a few days"},
		{downErr{}, "Let's Encrypt cannot be reached: dial tcp: down"},
		{errors.New("nope"), "ACME: nope"},
	} {
		log := newCallLog()
		f := newFakeACME(log)
		f.SetFail("Order", c.fail)
		a := newTestACME(t, f, newFakeTXT(log))
		err := a.Obtain(context.Background(), testHost)
		if err == nil || err.Error() != c.want || a.Status().Error != c.want {
			t.Errorf("%v -> %v / %q, want %q", c.fail, err, a.Status().Error, c.want)
		}
	}
}

func TestNeedsRenewalAt30Days(t *testing.T) {
	log := newCallLog()
	f := newFakeACME(log)
	a := newTestACME(t, f, newFakeTXT(log))
	now := time.Now()
	w := "*." + testHost
	if !a.Needs(w, now) {
		t.Error("no file: want true")
	}
	f.Valid = 31 * 24 * time.Hour
	if err := a.Obtain(context.Background(), testHost); err != nil {
		t.Fatal(err)
	}
	if a.Needs(w, now) {
		t.Error("31 days left: want false")
	}
	if !a.Needs("*.other.box.example.org", now) {
		t.Error("other wildcard: want true")
	}
	if !a.Needs(w, now.Add(2*24*time.Hour)) {
		t.Error("29 days left: want true")
	}
}

func TestACMEFailureIsReportedAndRetried(t *testing.T) {
	log := newCallLog()
	f := newFakeACME(log)
	f.SetFail("Order", errors.New("urn:ietf:params:acme:error:rateLimited"))
	a := newTestACME(t, f, newFakeTXT(log))
	var mu sync.Mutex
	clock := time.Now()
	a.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { a.Loop(ctx, func() string { return "*." + testHost }, 10*time.Millisecond); close(done) }()

	wait := func(cond func(CertStatus) bool) CertStatus {
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if st := a.Status(); cond(st) {
				return st
			}
		}
		t.Fatalf("timeout, status %+v", a.Status())
		return CertStatus{}
	}
	st := wait(func(s CertStatus) bool { return s.Error != "" })
	if !strings.Contains(st.Error, "limit") || st.Pending {
		t.Fatalf("status = %+v", st)
	}
	time.Sleep(60 * time.Millisecond) // several ticks pass, the clock does not: the backoff holds
	if n := strings.Count(log.joined(), "Order:"); n != 1 {
		t.Fatalf("%d attempts before the backoff", n)
	}
	f.SetFail("Order", nil)
	mu.Lock()
	clock = clock.Add(time.Hour + time.Minute)
	mu.Unlock()
	st = wait(func(s CertStatus) bool { return !s.NotAfter.IsZero() })
	if st.Error != "" {
		t.Errorf("error after success: %q", st.Error)
	}
	cancel()
	<-done
}

func TestWakeRunsTheCheckNow(t *testing.T) {
	z := &ACME{} // a zero value, no Loop: never blocks
	z.Wake()
	z.Wake()
	log := newCallLog()
	a := newTestACME(t, newFakeACME(log), newFakeTXT(log))
	var mu sync.Mutex
	wildcard, checks := "", 0
	first := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		a.Loop(ctx, func() string {
			mu.Lock()
			defer mu.Unlock()
			if checks++; checks == 1 {
				close(first)
			}
			return wildcard
		}, time.Hour)
		close(done)
	}()
	defer func() { cancel(); <-done }()
	<-first // the first check found nothing shared; the next tick is an hour away
	mu.Lock()
	wildcard = "*." + testHost
	mu.Unlock()
	a.Wake() // only this can run the check now
	for deadline := time.Now().Add(5 * time.Second); a.Status().NotAfter.IsZero(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("Wake did not run the check")
		}
	}
}

func TestAccountKeyIsReused(t *testing.T) {
	log := newCallLog()
	f := newFakeACME(log)
	a := newTestACME(t, f, newFakeTXT(log))
	for range 2 {
		if err := a.Obtain(context.Background(), testHost); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.Keys) != 2 || !f.Keys[0].(*ecdsa.PrivateKey).Equal(f.Keys[1]) {
		t.Fatal("the account key changed between two Obtain calls")
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestObtainSurvivesAClearTXTFailure(t *testing.T) {
	log := newCallLog()
	txt := newFakeTXT(log)
	txt.ClearErr = errors.New("relay down")
	a := newTestACME(t, newFakeACME(log), txt)
	if err := a.Obtain(context.Background(), testHost); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{a.KeyFile, a.CertFile} {
		if _, err := os.Stat(p); err != nil {
			t.Error(err)
		}
	}
}

func TestObtainKeepsTheMainErrorOverClearTXT(t *testing.T) {
	log := newCallLog()
	f := newFakeACME(log)
	f.SetFail("Accept", errors.New("boom"))
	txt := newFakeTXT(log)
	txt.ClearErr = errors.New("relay down")
	err := newTestACME(t, f, txt).Obtain(context.Background(), testHost)
	if err == nil || err.Error() != "ACME: boom" {
		t.Fatalf("err = %v", err)
	}
}

func TestObtainClearsTheTXTWhenSetTXTFails(t *testing.T) {
	log := newCallLog()
	txt := newFakeTXT(log)
	txt.Err = errors.New("nope")
	err := newTestACME(t, newFakeACME(log), txt).Obtain(context.Background(), testHost)
	if err == nil || err.Error() != "ACME: nope" {
		t.Fatalf("err = %v", err)
	}
	if got := log.joined(); strings.Count(got, "ClearTXT") != 1 || !strings.Contains(got, "SetTXT:txt-value-1 ClearTXT") {
		t.Errorf("calls = %s", got)
	}
}

func TestObtainSkipsTheChallengeOfAValidAuthorization(t *testing.T) {
	log := newCallLog()
	f := newFakeACME(log)
	f.AuthzValid = true
	if err := newTestACME(t, f, newFakeTXT(log)).Obtain(context.Background(), testHost); err != nil {
		t.Fatal(err)
	}
	got := log.joined()
	if strings.Contains(got, "SetTXT") || strings.Contains(got, "Accept") || strings.Contains(got, "ClearTXT") || !strings.Contains(got, "Finalize") {
		t.Errorf("calls = %s", got)
	}
}

func TestObtainDoesNotRecordACancellation(t *testing.T) {
	log := newCallLog()
	f := newFakeACME(log)
	f.SetFail("Order", context.Canceled)
	a := newTestACME(t, f, newFakeTXT(log))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Obtain(ctx, testHost); err == nil {
		t.Fatal("expected an error")
	}
	if st := a.Status(); st.Error != "" || st.Pending {
		t.Errorf("status = %+v", st)
	}
}

func TestObtainRecordsATimedOutAttempt(t *testing.T) {
	log := newCallLog()
	f := newFakeACME(log)
	f.SetFail("Order", context.DeadlineExceeded)
	a := newTestACME(t, f, newFakeTXT(log))
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := a.Obtain(ctx, testHost); err == nil {
		t.Fatal("expected an error")
	}
	if st := a.Status(); st.Error == "" || st.Pending {
		t.Errorf("status = %+v", st)
	}
}
