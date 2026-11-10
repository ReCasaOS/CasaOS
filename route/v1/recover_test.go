package v1

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/service"
	"github.com/labstack/echo/v4"
)

type sentNotify struct {
	service.NotifyServer
	sent []map[string]interface{}
}

func (n *sentNotify) SendNotify(_ string, message map[string]interface{}) {
	n.sent = append(n.sent, message)
}

// a storage that saves every remote and mounts none
type unmountableStorage struct {
	service.StorageService
	saved []string
}

func (s *unmountableStorage) CreateConfig(_ map[string]string, name, _ string) error {
	s.saved = append(s.saved, name)
	return nil
}
func (*unmountableStorage) MountStorage(string, string) error { return errors.New("rclone refused") }

type recoverRepository struct {
	service.Repository
	notify  *sentNotify
	storage *unmountableStorage
}

func (r recoverRepository) Notify() service.NotifyServer    { return r.notify }
func (r recoverRepository) Storage() service.StorageService { return r.storage }

func recoverBox(t *testing.T) (recoverRepository, func(target string) (int, string)) {
	t.Helper()
	repository := recoverRepository{notify: &sentNotify{}, storage: &unmountableStorage{}}
	previous := service.MyService
	service.MyService = repository
	t.Cleanup(func() { service.MyService = previous })

	// the route of route/v1.go: a last parameter takes the rest of the path, slashes and all
	e := echo.New()
	e.GET("/v1/recover/:type", GetRecoverStorage)

	return repository, func(target string) (int, string) {
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		return recorder.Code, recorder.Body.String()
	}
}

// The way back from a cloud sign-in is open to anybody: a browser comes back to it
// with no token. It goes on only with the state this box put in the sign-in
// address, and the box gives that address to a signed-in dashboard alone.
func TestACloudSignInComesBackWithTheStateOfThisBox(t *testing.T) {
	_, get := recoverBox(t)

	var list struct{ Data []model.Drive }
	if err := json.Unmarshal(call(t, ListDriverInfo, http.MethodGet, "/v1/driver", nil).Body.Bytes(), &list); err != nil || len(list.Data) != 3 {
		t.Fatalf("the three drivers: %v %+v", err, list.Data)
	}

	for _, driver := range list.Data {
		address, err := url.Parse(driver.AuthUrl)
		if err != nil {
			t.Fatal(err)
		}
		// the relay sends the browser to <state>/?code=...; an empty code stops before any network
		way := strings.TrimPrefix(address.Query().Get("state"), "${HOST}")
		kind := strings.Split(way, "/")[3]

		if status, body := get(way + "/?code="); status != http.StatusOK || !strings.Contains(body, "Code cannot be empty") {
			t.Errorf("%s, with the state of this box: %d %s", driver.Name, status, body)
		}
		for _, forged := range []string{"/v1/recover/" + kind + "/?code=", "/v1/recover/" + kind + "/" + strings.Repeat("0", 32) + "/?code="} {
			if status, body := get(forged); status != http.StatusForbidden {
				t.Errorf("%s: %d %s, want it refused", forged, status, body)
			}
		}
	}
}

// A remote that could not be mounted is not announced as added.
func TestACloudAccountThatDoesNotMountIsNotSaidToBeAdded(t *testing.T) {
	repository, _ := recoverBox(t)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), recorder)

	if err := finishRecover(ctx, "GoogleDrive", map[string]string{}, "someone_google_drive_1", "drive"); err != nil {
		t.Fatal(err)
	}

	// the dashboard reads the driver of every message about a sign-in
	if sent := repository.notify.sent; len(sent) != 1 || sent[0]["status"] != "fail" || sent[0]["driver"] != "GoogleDrive" {
		t.Fatalf("the dashboard was told %+v, want one failure of GoogleDrive", sent)
	}
	if strings.Contains(recorder.Body.String(), "Just close the page") {
		t.Fatalf("the page says it worked: %s", recorder.Body.String())
	}
}
