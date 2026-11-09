package share

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

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
		apps = append(apps, Installed{name, info.Title["en_us"], info.PortMap, info.Scheme})
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	return apps, nil
}
