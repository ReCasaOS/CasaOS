package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/config"
)

func TestMessageBusSendsInternalSecret(t *testing.T) {
	runtimePath := t.TempDir()
	if err := external.WriteInternalSecret(runtimePath); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(runtimePath, external.InternalSecretFilename))
	if err != nil {
		t.Fatal(err)
	}
	want := "Internal " + strings.TrimSpace(string(raw))

	auth := make(chan string, 1)
	bus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth <- r.Header.Get("Authorization")
	}))
	defer bus.Close()
	if err := os.WriteFile(filepath.Join(runtimePath, external.MessageBusAddressFilename), []byte(bus.URL), 0o600); err != nil {
		t.Fatal(err)
	}

	previous := config.CommonInfo
	config.CommonInfo = &model.CommonModel{RuntimePath: runtimePath}
	t.Cleanup(func() { config.CommonInfo = previous })

	if _, err := (&store{}).MessageBus().GetActionTypesWithResponse(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := <-auth; got != want {
		t.Fatalf("Authorization = %q, want the internal secret", got)
	}
}
