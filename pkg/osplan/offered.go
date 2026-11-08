package osplan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// MetaReleaseLTS is where Canonical says which upgrades between its LTS releases it offers: the URI_LTS of /etc/update-manager/meta-release, which do-release-upgrade reads. A
// hop of Ubuntu is planned only when the stanza of its target is there and says Supported: 1. Canonical opens an upgrade with the first point release of the target, and holds
// one back by taking its stanza out (from 22.04 to 24.04, from 2 to 9 September 2024: LP #2078895).
const MetaReleaseLTS = "https://changelogs.ubuntu.com/meta-release-lts"

const (
	// the file is some 7 KB (2026-11-08)
	maxMetaReleaseBytes = 1 << 20
	metaReleaseTimeout  = 30 * time.Second
)

// fetchMetaRelease is a GET of MetaReleaseLTS, bounded in size.
// shortcut: Go's own client, with the proxy of the core's environment and not apt's; a box whose only way out is apt's proxy is refused (not_offered, unknown). Read apt's proxy
// here when an owner has one.
func fetchMetaRelease(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, MetaReleaseLTS, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", MetaReleaseLTS, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetaReleaseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxMetaReleaseBytes {
		return nil, errors.New("the file is larger than it can be")
	}
	return body, nil
}

// checkOffered asks Canonical whether it offers the hop, for a hop of Ubuntu (a hop of Debian is not asked): nil, or not_offered. A file that could not be had is not_offered with
// unknown, as busy is when the lock cannot be seen: the plan does not go on without the answer.
func checkOffered(ctx context.Context, files Files, hop HopSpec, opts Options) *Failure {
	if hop.Distro != "Ubuntu" {
		return nil
	}
	fetch := opts.MetaRelease
	if fetch == nil {
		fetch = fetchMetaRelease
	}
	cmdCtx, cancel := context.WithTimeout(ctx, metaReleaseTimeout)
	defer cancel()
	body, err := fetch(cmdCtx)
	if f := ctxFailure(ctx, cmdCtx, PhaseSources); f != nil {
		return f
	}
	if err != nil {
		return fail(CodeNotOffered, "Canonical's list of the upgrades it offers could not be read: "+sanitizeLine(err.Error()),
			map[string]any{"target": hop.To, "unknown": true})
	}
	return offered(hop, body, files.Now())
}

// offered reads the file: the last stanza whose Dist is the target decides, as it does for do-release-upgrade.
func offered(hop HopSpec, body []byte, now time.Time) *Failure {
	data := map[string]any{"target": hop.To, "read_at": now.UTC().Format(time.RFC3339)}
	stanzas, ok := osreadiness.ReadDeb822(string(body))
	if !ok {
		data["unknown"] = true
		return fail(CodeNotOffered, "Canonical's list of the upgrades it offers is not in the form it has", data)
	}
	found, supported := false, ""
	for _, st := range stanzas {
		var dist, sup string
		for _, f := range st.Fields {
			switch strings.ToLower(f.Key) {
			case "dist":
				dist = strings.Join(fieldWordsOf(f), " ")
			case "supported":
				sup = strings.Join(fieldWordsOf(f), " ")
			}
		}
		if dist == hop.To {
			found, supported = true, sup
		}
	}
	switch {
	case !found:
		data["reason"] = "absent"
		return fail(CodeNotOffered, "Canonical does not offer the upgrade to "+hop.ToName()+" yet", data)
	case supported != "1":
		data["reason"] = "unsupported"
		return fail(CodeNotOffered, "Canonical does not offer the upgrade to "+hop.ToName()+": its list says it is not supported", data)
	}
	return nil
}
