package trash

import (
	"strings"
	"testing"
	"time"
)

func TestAnInfoReadsBackWhatItWrote(t *testing.T) {
	when := time.Date(2026, 10, 27, 9, 30, 5, 0, time.Local)
	path, deleted, err := parseInfo(infoText("Media/été 2026/a%b c.txt", when))
	if err != nil || path != "Media/été 2026/a%b c.txt" || !deleted.Equal(when) {
		t.Fatalf("got %q %v %v", path, deleted, err)
	}
	if !strings.Contains(string(infoText("a b", when)), "Path=a%20b\n") {
		t.Fatal("the path is not escaped")
	}
}

func TestAnInfoWithoutItsHeaderOrPathIsRefused(t *testing.T) {
	for _, text := range []string{"Path=a\n", "[Trash Info]\nDeletionDate=2026-01-01T00:00:00\n"} {
		if _, _, err := parseInfo([]byte(text)); err == nil {
			t.Errorf("%q was read", text)
		}
	}
	if _, deleted, err := parseInfo([]byte("[Trash Info]\nPath=a\nDeletionDate=yesterday\n")); err != nil || !deleted.IsZero() {
		t.Errorf("an unreadable date: %v %v", deleted, err)
	}
}

func TestNames(t *testing.T) {
	if got := trashName("a.txt", 3); got != "a.txt.3" {
		t.Errorf("trashName = %q", got)
	}
	long := strings.Repeat("é", 200) // 400 bytes
	if got := trashName(long, 2); len(got)+len(".trashinfo") > 255 || !strings.HasSuffix(got, ".2") || !strings.HasPrefix(long, strings.TrimSuffix(got, ".2")) {
		t.Errorf("trashName cut %d bytes wrong", len(got))
	}
	for _, c := range []struct {
		base  string
		isDir bool
		n     int
		want  string
	}{
		{"photo.jpg", false, 1, "photo (restored).jpg"},
		{"photo.jpg", false, 2, "photo (restored 2).jpg"},
		{"v1.2", true, 1, "v1.2 (restored)"},
		{".bashrc", false, 1, ".bashrc (restored)"},
	} {
		if got := restoredName(c.base, c.isDir, c.n); got != c.want {
			t.Errorf("restoredName(%q) = %q, want %q", c.base, got, c.want)
		}
	}
	if got := restoredName(strings.Repeat("a", 250)+".jpg", false, 2); len(got) > 255 || !strings.HasSuffix(got, " (restored 2).jpg") {
		t.Errorf("restoredName is %d bytes: %q", len(got), got)
	}
	if got := restoredName("a."+strings.Repeat("b", 250), false, 1); len(got) > 255 || !strings.HasSuffix(got, " (restored)") {
		t.Errorf("restoredName is %d bytes: %q", len(got), got)
	}
	for _, n := range []string{"", ".", "..", "a/b"} {
		if validName(n) {
			t.Errorf("%q is a valid name", n)
		}
	}
}
