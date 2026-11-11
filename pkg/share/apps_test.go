package share

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// An app the store calls https but that serves plain HTTP (Vaultwarden) is reached over http; one that
// really serves TLS keeps https.
func TestAppsListProbesHTTPS(t *testing.T) {
	ok := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	plain := httptest.NewServer(ok)
	defer plain.Close()
	secure := httptest.NewTLSServer(ok)
	defer secure.Close()
	port := func(s *httptest.Server) string { return s.URL[strings.LastIndex(s.URL, ":")+1:] }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":{
			"vaultwarden":{"store_info":{"port_map":%q,"scheme":"https"}},
			"portainer":{"store_info":{"port_map":%q,"scheme":"https"}}}}`, port(plain), port(secure))
	}))
	defer server.Close()
	got, err := Apps{RuntimePath: fakeRuntime(t, "app-management.url", server)}.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "portainer" || got[0].Scheme != "https" || got[1].Scheme != "http" {
		t.Fatalf("%+v", got)
	}
}
