package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS-Common/model"
)

// disk is a disk of LocalStorage's GET /v1/disks, the dashboard's list.
type disk struct {
	Name        string `json:"name"`
	Model       string `json:"model"`
	Serial      string `json:"serial"`
	SmartStatus string `json:"smart_status"` // passed, failed or unavailable
}

// storage is a volume of LocalStorage's GET /v1/storage?system=show, the
// dashboard's list: its sizes are in bytes, as strings.
type storage struct {
	MountPoint string `json:"mount_point"`
	Label      string `json:"label"`
	Size       string `json:"size"`
	Used       string `json:"used"`
}

// id is what a disk's keys go by: its serial, else its name.
func (d disk) id() string {
	if d.Serial != "" {
		return d.Serial
	}
	return d.Name
}

func (d disk) String() string {
	if d.Model == "" {
		return d.Name
	}
	return d.Name + " (" + d.Model + ")"
}

// pollDisks asks LocalStorage for its disks and storages, as the dashboard
// does, and raises or resolves what they show. LocalStorage unreachable, or an
// answer without a disk: nothing is concluded, not even that a disk is gone.
func (h *Hub) pollDisks(ctx context.Context) {
	runtimePath := h.RuntimePath()
	address, err := localStorage(ctx, runtimePath)
	if err != nil {
		return
	}
	var disks struct {
		Disks []disk `json:"disks"`
	}
	var storages []struct {
		Children []storage `json:"children"`
	}
	if get(ctx, address+"/v1/disks", runtimePath, &model.Result{Data: &disks}) != nil ||
		get(ctx, address+"/v1/storage?system=show", runtimePath, &model.Result{Data: &storages}) != nil || len(disks.Disks) == 0 {
		return
	}
	var volumes []storage
	for _, s := range storages {
		volumes = append(volumes, s.Children...)
	}
	raise, resolve := diskAlerts(h.seen, disks.Disks, volumes, h.load().DiskThreshold)
	h.seen = disks.Disks
	for _, a := range resolve {
		h.resolve(a)
	}
	for _, a := range raise {
		h.raise(a)
	}
}

// diskAlerts is what one poll shows, given the disks of the previous one: a
// disk failing its SMART check, a disk gone, a storage above the threshold,
// and each of them cleared. A cleared condition is only sent as resolved when
// its alert was sent.
func diskAlerts(previous, disks []disk, volumes []storage, threshold int) (raise, resolve []alert) {
	present := map[string]bool{}
	for _, d := range disks {
		present[d.id()] = true
		smart := "disk:" + d.id() + ":smart"
		switch d.SmartStatus {
		case "failed":
			raise = append(raise, alert{key: smart, category: Disks, sentence: "Disk " + d.String() + " fails its SMART check: copy its data elsewhere and replace it."})
		case "passed":
			resolve = append(resolve, alert{key: smart, category: Disks, sentence: "Resolved: disk " + d.String() + " passes its SMART check again."})
		}
		resolve = append(resolve, alert{key: "disk:" + d.id() + ":missing", category: Disks, sentence: "Resolved: disk " + d.String() + " is back."})
	}
	for _, d := range previous {
		if !present[d.id()] {
			raise = append(raise, alert{key: "disk:" + d.id() + ":missing", category: Disks, sentence: "Disk " + d.String() + " is gone: it was there an hour ago."})
		}
	}
	for _, v := range volumes {
		size, sizeErr := strconv.ParseFloat(v.Size, 64)
		used, usedErr := strconv.ParseFloat(v.Used, 64)
		if v.MountPoint == "" || sizeErr != nil || usedErr != nil || size <= 0 {
			continue
		}
		full := "storage:" + v.MountPoint + ":full"
		if percent := used / size * 100; percent > float64(threshold) {
			raise = append(raise, alert{key: full, category: Disks, sentence: fmt.Sprintf("Storage %s is %d %% full, above %d %%.", v.Label, int(percent), threshold)})
		} else {
			resolve = append(resolve, alert{key: full, category: Disks, sentence: fmt.Sprintf("Resolved: storage %s is back under %d %%.", v.Label, threshold)})
		}
	}
	return raise, resolve
}

// localStorage is LocalStorage's address. It leaves none in the runtime path,
// so it is read from the gateway's routes, whose management API does: the
// target of /v1/disks.
func localStorage(ctx context.Context, runtimePath string) (string, error) {
	management, err := os.ReadFile(filepath.Join(runtimePath, external.ManagementURLFilename))
	if err != nil {
		return "", err
	}
	var routes []model.Route
	if err := get(ctx, strings.TrimRight(strings.TrimSpace(string(management)), "/")+external.APIGatewayRoutes, runtimePath, &routes); err != nil {
		return "", err
	}
	for _, route := range routes {
		if route.Path == "/v1/disks" {
			return strings.TrimRight(route.Target, "/"), nil
		}
	}
	return "", errors.New("no route to LocalStorage's disks")
}

// get reads the JSON at address into out, as an internal request.
func get(ctx context.Context, address, runtimePath string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	_ = external.InternalRequestEditor(runtimePath)(ctx, request) // never fails
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", address, response.Status)
	}
	return json.NewDecoder(response.Body).Decode(out)
}
