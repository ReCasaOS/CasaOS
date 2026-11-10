package share

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAppsList(t *testing.T) {
	var path, auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		w.Write([]byte(`{"data":{
			"nextcloud":{"store_info":{"port_map":"7580","scheme":"https"}},
			"jellyfin":{"store_info":{"port_map":"8096","scheme":"http","title":{"en_us":"Jellyfin"}}},
			"plain":{"store_info":{"port_map":"80"}}}}`))
	}))
	defer server.Close()
	got, err := Apps{RuntimePath: fakeRuntime(t, "app-management.url", server)}.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Installed{
		{Name: "jellyfin", Title: "Jellyfin", Port: "8096", Scheme: "http"},
		{Name: "nextcloud", Port: "7580", Scheme: "https"},
		{Name: "plain", Port: "80", Scheme: "http"},
	}
	if path != "/v2/app_management/compose" || auth != "Internal s3cret" || len(got) != 3 {
		t.Fatalf("%s %q %+v", path, auth, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %+v want %+v", i, got[i], want[i])
		}
	}
}
