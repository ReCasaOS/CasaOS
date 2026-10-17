package modulecmd

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-ini/ini"
)

// The gateway is asked on its own port, which it keeps in /etc/casaos/gateway.ini, at [gateway] port: empty until the gateway has found a free one at its first start. A route that
// answers is a route the gateway has: any status but the ones the gateway gives for a service behind it that is not there (502, 503, 504) is an answer; so is any status of a service
// that has not been written yet to the gateway... the gateway's own 404 is the gateway's. A connection that is refused is no answer.
type httpGateway struct {
	roots  Roots
	client *http.Client
}

func newGateway(roots Roots) Gateway {
	return httpGateway{roots: roots, client: &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// gatewayPort is the port in the gateway's configuration.
func gatewayPort(raw []byte) (int, error) {
	f, err := ini.LoadSources(ini.LoadOptions{Insensitive: true}, raw)
	if err != nil {
		return 0, err
	}
	port, err := strconv.Atoi(f.Section("gateway").Key("port").String())
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("the gateway has no port yet")
	}
	return port, nil
}

func (g httpGateway) Answers(ctx context.Context, route string) (bool, error) {
	raw, err := readRegular(g.roots.Path(pathGateway), 64<<10)
	if err != nil {
		return false, err
	}
	port, err := gatewayPort(raw)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+route, nil)
	if err != nil {
		return false, err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return false, nil // (refused, or no answer in time)
	}
	_ = resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return false, nil
	}
	return true, nil
}
