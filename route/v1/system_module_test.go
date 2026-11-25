package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/modulecmd"
	"github.com/ReCasaOS/CasaOS/service"
	"github.com/labstack/echo/v4"
)

// fakeModuleList is a box whose list says how many fetches of the catalogue were made before it.
type fakeModuleList struct {
	service.SystemService
	refreshes int
}

func (f *fakeModuleList) RefreshSystemModuleCatalogue(context.Context) { f.refreshes++ }

func (f *fakeModuleList) ListSystemModules() (modulecmd.ListResult, error) {
	return modulecmd.ListResult{Catalogue: modulecmd.CatalogueInfo{OK: true, Online: &modulecmd.OnlineInfo{OK: true, Serial: int64(f.refreshes)}}, Modules: []modulecmd.ListModule{}}, nil
}

func TestModulesRefreshIsLimitedToOneInThirtySeconds(t *testing.T) {
	fake := &fakeModuleList{}
	original := service.MyService
	t.Cleanup(func() { service.MyService = original })
	service.MyService = fakeSystemPackageRepository{system: fake}
	lastModulesRefresh.Store(0)
	t.Cleanup(func() { lastModulesRefresh.Store(0) })

	list := func(target string) int64 {
		t.Helper()
		recorder := httptest.NewRecorder()
		if err := GetSystemModules(echo.New().NewContext(httptest.NewRequest(http.MethodGet, target, nil), recorder)); err != nil {
			t.Fatal(err)
		}
		var answer struct {
			Data modulecmd.ListResult `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil || recorder.Code != http.StatusOK || answer.Data.Catalogue.Online == nil {
			t.Fatalf("%d %s %v", recorder.Code, recorder.Body, err)
		}
		return answer.Data.Catalogue.Online.Serial
	}

	if got := list("/v1/sys/modules"); got != 0 || fake.refreshes != 0 {
		t.Errorf("a plain list fetched: %d", fake.refreshes)
	}
	for _, other := range []string{"0", "true", "yes"} {
		if list("/v1/sys/modules?refresh=" + other); fake.refreshes != 0 {
			t.Errorf("refresh=%s fetched: %d", other, fake.refreshes)
		}
	}
	// the list answered is the one after the fetch
	if got := list("/v1/sys/modules?refresh=1"); got != 1 || fake.refreshes != 1 {
		t.Errorf("the first refresh: list after %d fetches, %d fetches", got, fake.refreshes)
	}
	if got := list("/v1/sys/modules?refresh=1"); got != 1 || fake.refreshes != 1 {
		t.Errorf("a second refresh within thirty seconds fetched: %d", fake.refreshes)
	}
	// thirty seconds later, one more
	lastModulesRefresh.Add(-30)
	if list("/v1/sys/modules?refresh=1"); fake.refreshes != 2 {
		t.Errorf("a refresh after thirty seconds: %d fetches", fake.refreshes)
	}
}
