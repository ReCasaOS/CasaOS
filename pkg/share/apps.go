package share

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/external"
)

// Apps is AppManagement, reached through the runtime directory.
type Apps struct{ RuntimePath func() string }

// Installed is an installed app, by its compose name.
type Installed struct{ Name, Title, Port, Scheme string }

// List returns the installed apps, sorted by name.
func (a Apps) List(ctx context.Context) ([]Installed, error) {
	status, data, err := internalCall(ctx, a.RuntimePath(), external.AppManageURLFilename, http.MethodGet, "/v2/app_management/compose", nil)
	if err != nil {
		return nil, fmt.Errorf("AppManagement: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("AppManagement answered %d", status)
	}
	var body struct {
		Data map[string]struct {
			StoreInfo struct {
				PortMap string            `json:"port_map"`
				Scheme  string            `json:"scheme"`
				Title   map[string]string `json:"title"`
			} `json:"store_info"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(data), &body); err != nil {
		return nil, fmt.Errorf("AppManagement's list is not readable: %w", err)
	}
	apps := make([]Installed, 0, len(body.Data))
	for name, app := range body.Data {
		info := app.StoreInfo
		if info.Scheme == "" {
			info.Scheme = "http"
		}
		if !validPort(info.PortMap) || (info.Scheme != "http" && info.Scheme != "https") {
			info.PortMap = "" // no host is registered with a target the Gateway cannot reach
		} else if info.Scheme == "https" && !speaksTLS(ctx, info.PortMap) {
			info.Scheme = "http"
		}
		apps = append(apps, Installed{name, info.Title["en_us"], info.PortMap, info.Scheme})
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	return apps, nil
}

// speaksTLS reports whether the app on 127.0.0.1:port answers a TLS handshake. The store's scheme is the
// link the dashboard opens, not always what the port serves: Vaultwarden says https (its web vault needs a
// secure context) and serves plain HTTP. Only an answer that is not TLS says false; an app that is down or
// slow keeps its declared https, and the next resync probes again.
func speaksTLS(ctx context.Context, port string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	d := tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // probe only, no data is sent
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", port))
	if err == nil {
		conn.Close()
		return true
	}
	var notTLS tls.RecordHeaderError
	return !errors.As(err, &notTLS)
}

// validPort is digits only, 1 to 65535.
func validPort(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535 && strings.Trim(p, "0123456789") == ""
}
