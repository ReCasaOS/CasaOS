package httper

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap/zapcore"
)

// rclone answers with what it was sent when it refuses it, and config/create is sent the cloud token: no reply is logged.
func TestTheRepliesOfRcloneAreNotLogged(t *testing.T) {
	const secret = "ya29.SECRET-ACCESS-TOKEN"
	socket := filepath.Join(t.TempDir(), "rclone.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var replies [][]byte
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		reply, _ := json.Marshal(map[string]any{"error": "refused", "input": r.PostForm})
		mu.Lock()
		replies = append(replies, reply)
		mu.Unlock()
		status := http.StatusOK
		if r.URL.Path == "/config/create" || r.PostForm.Get("mountPoint") == "/mnt/refused" {
			status = http.StatusInternalServerError
		}
		w.WriteHeader(status)
		_, _ = w.Write(reply)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	previous := rcloneSocket
	rcloneSocket = socket
	t.Cleanup(func() { rcloneSocket = previous })

	var logs bytes.Buffer
	logger.LogInitWithWriterSyncers(zapcore.AddSync(&logs))

	if err := CreateConfig(map[string]string{"token": `{"access_token":"` + secret + `"}`}, "gd", "drive"); err == nil {
		t.Error("CreateConfig: no error for a refused config")
	}
	if err := Mount("/mnt/gd", "gd:"); err != nil {
		t.Errorf("Mount: %v", err)
	}
	if err := Unmount("/mnt/gd"); err != nil {
		t.Errorf("Unmount: %v", err)
	}
	if err := Unmount("/mnt/refused"); err == nil {
		t.Error("Unmount: no error for a refused unmount")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(replies) != 4 {
		t.Fatalf("%d calls reached rclone, want 4", len(replies))
	}
	logged := logs.String()
	if strings.Contains(logged, secret) {
		t.Errorf("the token is in the log:\n%s", logged)
	}
	for _, reply := range replies {
		if strings.Contains(logged, string(reply)) || strings.Contains(logged, base64.StdEncoding.EncodeToString(reply)) {
			t.Errorf("a reply of rclone is in the log: %s\n%s", reply, logged)
		}
	}
	if !strings.Contains(logged, `"status": 500`) && !strings.Contains(logged, `"status":500`) {
		t.Errorf("the refused config's status is not logged:\n%s", logged)
	}
}
