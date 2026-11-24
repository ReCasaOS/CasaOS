//go:build linux

package v1

import (
	"encoding/json"
	"net/http"
	"sort"
	"testing"
)

func searched(t *testing.T, target string) (int, SearchResp) {
	t.Helper()
	rec := call(t, SearchFiles, http.MethodGet, target, nil)
	var body struct {
		Data SearchResp `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	paths := []string{}
	for _, c := range body.Data.Content {
		paths = append(paths, c.Path)
	}
	sort.Strings(paths)
	return rec.Code, body.Data
}

// CasaOS#184: the search of Files reads what the listing may read, and nothing past it.
func TestTheSearchOfFilesStaysInItsRoots(t *testing.T) {
	data, secret := confinedBox(t)

	code, found := searched(t, "/v1/file/search?path="+q(data)+"&q=A.TX")
	if code != http.StatusOK || len(found.Content) != 1 || found.Content[0].Path != data+"/Media/a.txt" || found.Content[0].Size != 1 {
		t.Fatalf("in the root: %d %+v", code, found)
	}
	// from "/", above the root: the root is searched, the secret beside it is not
	if code, found = searched(t, "/v1/file/search?path=%2F&q=a.txt"); code != http.StatusOK || len(found.Content) != 1 {
		t.Fatalf("from /: %d %+v", code, found)
	}
	if _, found = searched(t, "/v1/file/search?path=%2F&q=key"); len(found.Content) != 0 {
		t.Fatalf("from /, found the secret: %+v", found)
	}
	// a link out of the root is a name, not a way in
	if _, found = searched(t, "/v1/file/search?path="+q(data)+"&q=key"); len(found.Content) != 0 {
		t.Fatalf("through the link: %+v", found)
	}
	for _, path := range []string{secret, data + "/escape", data + "/../secret"} {
		if code, _ := searched(t, "/v1/file/search?path="+q(path)+"&q=key"); code != http.StatusForbidden {
			t.Errorf("search of %s: %d, want 403", path, code)
		}
	}
	for _, query := range []string{"", "a", "%20%20b%20"} {
		if code, _ := searched(t, "/v1/file/search?path="+q(data)+"&q="+query); code != http.StatusBadRequest {
			t.Errorf("query %q: %d, want 400", query, code)
		}
	}
}

func TestRootsUnderDropsARootInsideAnother(t *testing.T) {
	got := rootsUnder("/", []string{"/DATA/sub", "/media", "/DATA"})
	sort.Strings(got)
	if len(got) != 2 || got[0] != "/DATA" || got[1] != "/media" {
		t.Fatalf("%v", got)
	}
	if got := rootsUnder("/mnt", []string{"/DATA", "/mnt/usb"}); len(got) != 1 || got[0] != "/mnt/usb" {
		t.Fatalf("%v", got)
	}
}
