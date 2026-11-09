package share

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/external"
)

// ErrGatewayNoShare is what the gateway of a box without public sharing gives.
var ErrGatewayNoShare = errors.New("the gateway on this box does not support public sharing yet")

// Gateway is the box's gateway, reached through the runtime directory.
type Gateway struct{ RuntimePath func() string }

// ShareAddr is the address the gateway hands the relay's connections to.
func (g Gateway) ShareAddr() (string, error) {
	data, err := os.ReadFile(filepath.Join(g.RuntimePath(), "share.addr"))
	if err != nil {
		return "", ErrGatewayNoShare
	}
	return strings.TrimSpace(string(data)), nil
}

// SetHost makes the gateway send requests for host to target.
func (g Gateway) SetHost(ctx context.Context, host, target string) error {
	body, _ := json.Marshal(map[string]string{"host": host, "target": target})
	return g.call(ctx, http.MethodPut, "/v1/gateway/hosts", body, http.StatusCreated)
}

// DeleteHost stops the gateway sending requests for host anywhere.
func (g Gateway) DeleteHost(ctx context.Context, host string) error {
	return g.call(ctx, http.MethodDelete, "/v1/gateway/hosts/"+url.PathEscape(host), nil, http.StatusNoContent)
}

func (g Gateway) call(ctx context.Context, method, path string, body []byte, ok int) error {
	runtimePath := g.RuntimePath()
	status, message, err := internalCall(ctx, runtimePath, external.ManagementURLFilename, method, path, body)
	if err != nil {
		return fmt.Errorf("gateway: %w", err)
	}
	switch {
	case status == ok:
		return nil
	case status == http.StatusNotFound || status == http.StatusMethodNotAllowed:
		return ErrGatewayNoShare // an older gateway has neither route
	}
	return fmt.Errorf("gateway answered %d: %s", status, message)
}

// internalCall asks the service whose address is in urlFile of the runtime directory, as the core.
// It returns the status and, for anything but a success, the message of the JSON answer.
func internalCall(ctx context.Context, runtimePath, urlFile, method, path string, body []byte) (int, string, error) {
	address, err := os.ReadFile(filepath.Join(runtimePath, urlFile))
	if err != nil {
		return 0, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(strings.TrimSpace(string(address)), "/")+path, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	_ = external.InternalRequestEditor(runtimePath)(ctx, request) // never fails
	response, err := (&http.Client{}).Do(request)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return response.StatusCode, string(data), nil
	}
	var failure struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &failure) != nil {
		failure.Message = ""
	}
	return response.StatusCode, failure.Message, nil
}
