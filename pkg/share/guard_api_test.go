package share

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

var codeShape = regexp.MustCompile(`^[A-HJKMNP-Z2-9]{4}-[A-HJKMNP-Z2-9]{4}-[A-HJKMNP-Z2-9]{4}$`)

func wantGuardErr(t *testing.T, err error, code string) {
	t.Helper()
	var ge ErrGuard
	if !errors.As(err, &ge) || ge.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
}

func TestAddPersonReturnsCodeOnce(t *testing.T) {
	ctx := context.Background()
	f := twoShared(t, jellyCode())
	p, code, err := f.s.AddPerson(ctx, "Maman", []string{"jellyfin"})
	if err != nil || !codeShape.MatchString(code) || p.Name != "Maman" {
		t.Fatalf("%+v %q %v", p, code, err)
	}
	view, err := f.s.GuardView(ctx)
	if err != nil || len(view.People) != 1 || view.People[0].ID != p.ID {
		t.Fatalf("%+v %v", view, err)
	}
	raw, _ := os.ReadFile(f.s.guardPath())
	if strings.Contains(string(raw), code) || strings.Contains(string(raw), NormalizeCode(code)) {
		t.Fatal("the code is on disk")
	}
	saved, _ := loadGuard(f.s.guardPath())
	if len(saved.People) != 1 || saved.People[0].CodeHash != CodeHash(saved.Secret, code) || saved.People[0].Gen != 1 {
		t.Fatalf("saved %+v", saved.People)
	}
	if pushed := f.mgmt.pushes(t); len(pushed) != 1 || len(pushed[0].People) != 1 {
		t.Fatalf("pushed %+v", pushed)
	}
}

func TestNewPersonCodeBumpsGen(t *testing.T) {
	ctx := context.Background()
	f := twoShared(t, jellyCode())
	p, first, err := f.s.AddPerson(ctx, "Maman", []string{"jellyfin"})
	if err != nil {
		t.Fatal(err)
	}
	old, _ := loadGuard(f.s.guardPath())
	code, err := f.s.NewPersonCode(ctx, p.ID)
	if err != nil || !codeShape.MatchString(code) || code == first {
		t.Fatalf("%q %v", code, err)
	}
	saved, _ := loadGuard(f.s.guardPath())
	if got := saved.People[0]; got.Gen != 2 || got.CodeHash == old.People[0].CodeHash || got.CodeHash != CodeHash(saved.Secret, code) {
		t.Fatalf("saved %+v", got)
	}
	if pushed := f.mgmt.pushes(t); pushed[len(pushed)-1].People[0].Gen != 2 {
		t.Fatalf("pushed %+v", pushed)
	}
	_, err = f.s.NewPersonCode(ctx, "p_nope")
	wantGuardErr(t, err, "not_found")
}

func TestAddPersonUnknownApp(t *testing.T) {
	f := twoShared(t, nil)
	_, _, err := f.s.AddPerson(context.Background(), "Maman", []string{"jellyfin", "ghost"})
	wantGuardErr(t, err, "unknown_app:ghost")
	if saved, _ := loadGuard(f.s.guardPath()); len(saved.People) != 0 {
		t.Fatalf("saved %+v", saved.People)
	}
}

func TestBadName(t *testing.T) {
	f := twoShared(t, nil)
	for _, name := range []string{"", "  ", strings.Repeat("a", 41), "a\nb"} {
		_, _, err := f.s.AddPerson(context.Background(), name, nil)
		wantGuardErr(t, err, "bad_name")
	}
	if _, _, err := f.s.AddPerson(context.Background(), strings.Repeat("é", 40), nil); err != nil {
		t.Fatalf("40 characters: %v", err)
	}
}

func TestSetAppGuardNotShared(t *testing.T) {
	f := twoShared(t, nil)
	_, err := f.s.SetAppGuard(context.Background(), "ghost", "code", nil)
	wantGuardErr(t, err, "not_shared")
	_, err = f.s.SetAppGuard(context.Background(), "jellyfin", "code", &PolicyOverride{RatePerMin: new(int)})
	wantGuardErr(t, err, "bad_policy:rate_per_min")
	if _, err := f.s.SetAppGuard(context.Background(), "jellyfin", "code", nil); err != nil {
		t.Fatal(err)
	}
}

func TestCodeRefusedOnOldGateway(t *testing.T) {
	f := twoShared(t, nil)
	f.mgmt.setGuard(404)
	_, err := f.s.SetAppGuard(context.Background(), "jellyfin", "code", nil)
	wantGuardErr(t, err, "gateway_unsupported")
	_, err = f.s.SetProtection(context.Background(), true)
	wantGuardErr(t, err, "gateway_unsupported")
	if saved, _ := loadGuard(f.s.guardPath()); len(saved.Apps) != 0 || saved.Protection {
		t.Fatalf("saved %+v", saved)
	}
	if v, err := f.s.GuardView(context.Background()); err != nil || v.Supported || v.Reason != "gateway_unsupported" {
		t.Fatalf("%+v %v", v, err)
	}
}

func TestConcurrentAddPerson(t *testing.T) {
	f := twoShared(t, nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := f.s.AddPerson(context.Background(), "P", []string{"jellyfin"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if saved, _ := loadGuard(f.s.guardPath()); len(saved.People) != 20 {
		t.Fatalf("%d saved", len(saved.People))
	}
}

func TestGuardWriteSaveFailureKeepsMemory(t *testing.T) {
	f := twoShared(t, nil)
	f.s.mu.Lock()
	f.s.guardLocked()
	f.s.mu.Unlock()
	defer func(orig func(string, GuardState) error) { saveGuardFile = orig }(saveGuardFile)
	saveGuardFile = func(string, GuardState) error { return errors.New("disk full") }
	if _, err := f.s.SetProtection(context.Background(), true); err == nil {
		t.Fatal("no error")
	}
	f.s.mu.Lock()
	on := f.s.guard.Protection
	f.s.mu.Unlock()
	if on || len(f.mgmt.pushes(t)) != 0 {
		t.Fatal("memory or Gateway ran ahead of the disk")
	}
}

func TestGuardWritePushFailureKeepsChange(t *testing.T) {
	f := twoShared(t, nil)
	f.mgmt.mu.Lock()
	f.mgmt.fail = 2 // the status check (502, not a 404: the write goes on), then the push
	f.mgmt.mu.Unlock()
	_, err := f.s.SetProtection(context.Background(), true)
	if !errors.Is(err, ErrGuardPush) {
		t.Fatalf("error = %v", err)
	}
	if saved, _ := loadGuard(f.s.guardPath()); !saved.Protection {
		t.Fatal("the change was not kept")
	}
}

func TestGuardViewMergesGateway(t *testing.T) {
	f := twoShared(t, jellyCode())
	f.mgmt.mu.Lock()
	f.mgmt.status = `{"version":1,"configured":true,"geo":{"country":true,"asn":false},"people_last_used":{"p_1":"2026-11-12T10:20:00Z"},"waves":[{"app":"jellyfin","bans":23}]}`
	f.mgmt.mu.Unlock()
	f.s.mu.Lock()
	g, _ := f.s.guardLocked()
	g.People = []Person{{ID: "p_1", Name: "Maman", Gen: 1, CodeHash: strings.Repeat("a", 64), Apps: []string{"jellyfin", "gone"}, CreatedAt: time.Now()}}
	f.s.mu.Unlock()
	v, err := f.s.GuardView(context.Background())
	if err != nil || !v.Supported || len(v.Apps) != 2 || v.Apps["jellyfin"].Access != "code" || !v.Geo.Country || len(v.Waves) != 1 {
		t.Fatalf("%+v %v", v, err)
	}
	if p := v.People[0]; len(p.Apps) != 1 || p.LastUsedAt == nil || !p.LastUsedAt.Equal(time.Date(2026, 11, 12, 10, 20, 0, 0, time.UTC)) {
		t.Fatalf("%+v", p)
	}
}

func TestGuardViewUnreadableFile(t *testing.T) {
	f := twoShared(t, nil)
	defer func(orig func(string) ([]byte, error)) { readGuardFile = orig }(readGuardFile)
	readGuardFile = func(string) ([]byte, error) { return nil, errors.New("io error") }
	if _, err := f.s.GuardView(context.Background()); err == nil {
		t.Fatal("a view of the placeholder")
	}
}
