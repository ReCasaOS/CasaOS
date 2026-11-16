package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/service"
	model2 "github.com/ReCasaOS/CasaOS/service/model"
	"github.com/labstack/echo/v4"
)

// newerRelease is version.json advertising a release far ahead of anything installed.
type newerRelease struct{ fetched *int }

func (newerRelease) GetCasaosVersion() model.Version { return model.Version{Version: "v999.0.0"} }
func (r newerRelease) FetchCasaosVersion() model.Version {
	if r.fetched != nil {
		*r.fetched++
	}
	return model.Version{Version: "v999.0.0"}
}

type countingNotify struct {
	service.NotifyServer
	added int
}

func (c *countingNotify) AddLog(model2.AppNotify) { c.added++ }

type versionCheckRepository struct {
	service.Repository
	notify  *countingNotify
	fetched *int
}

func (r versionCheckRepository) Casa() service.CasaService    { return newerRelease{fetched: r.fetched} }
func (r versionCheckRepository) Notify() service.NotifyServer { return r.notify }

// The check is open to anyone on the network, so it must not write: one row per call filled the disk.
func TestTheVersionCheckAnswersAndWritesNothing(t *testing.T) {
	notify := &countingNotify{}
	previous := service.MyService
	service.MyService = versionCheckRepository{notify: notify}
	t.Cleanup(func() { service.MyService = previous })

	for i := 0; i < 3; i++ {
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/v1/sys/version/check", nil), recorder)
		if err := GetSystemCheckVersion(ctx); err != nil {
			t.Fatal(err)
		}
		var answer struct {
			Data struct {
				NeedUpdate bool `json:"need_update"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil || recorder.Code != http.StatusOK || !answer.Data.NeedUpdate {
			t.Fatalf("status = %d, body = %s, err = %v", recorder.Code, recorder.Body, err)
		}
	}
	if notify.added != 0 {
		t.Errorf("the check added %d notifications", notify.added)
	}
}

// "Check for updates" looks past the cache, but the route is open: once per half minute, not per call.
func TestTheVersionCheckRefreshesOnRequestAndNotOnEveryCall(t *testing.T) {
	fetched := 0
	previous := service.MyService
	service.MyService = versionCheckRepository{notify: &countingNotify{}, fetched: &fetched}
	lastVersionRefresh.Store(0)
	t.Cleanup(func() { service.MyService = previous; lastVersionRefresh.Store(0) })

	check := func(target string) {
		t.Helper()
		ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, target, nil), httptest.NewRecorder())
		if err := GetSystemCheckVersion(ctx); err != nil {
			t.Fatal(err)
		}
	}
	check("/v1/sys/version")
	if fetched != 0 {
		t.Fatalf("a plain check went past the cache %d times", fetched)
	}
	check("/v1/sys/version?refresh=1")
	check("/v1/sys/version?refresh=1")
	check("/v1/sys/version?refresh=1")
	if fetched != 1 {
		t.Fatalf("three refreshes in a row looked %d times, want 1", fetched)
	}
}
