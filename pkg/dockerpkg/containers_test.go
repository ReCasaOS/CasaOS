package dockerpkg

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"text/template"
	"unicode/utf8"
)

// dockerTemplate renders a format the way `docker inspect --format` / `docker info --format`
// does: Docker's `json` function does not escape HTML and is followed by nothing.
func dockerTemplate(t *testing.T, format string, data any) string {
	t.Helper()
	tmpl, err := template.New("docker").Funcs(template.FuncMap{
		"json": func(v any) (string, error) {
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(v); err != nil {
				return "", err
			}
			return strings.TrimSpace(buf.String()), nil
		},
	}).Option("missingkey=error").Parse(format)
	if err != nil {
		t.Fatalf("the format does not parse: %v", err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("the format does not run: %v", err)
	}
	return out.String()
}

// inspected is the part of `docker inspect` the format reads, as the CLI hands it to a template.
func inspected(name, image, policy, network string, ports map[string]any) map[string]any {
	return map[string]any{
		"Name":            name,
		"Config":          map[string]any{"Image": image},
		"HostConfig":      map[string]any{"RestartPolicy": map[string]any{"Name": policy}, "NetworkMode": network},
		"NetworkSettings": map[string]any{"Ports": ports},
	}
}

func binding(hostIP, hostPort string) map[string]any {
	return map[string]any{"HostIp": hostIP, "HostPort": hostPort}
}

func TestContainerInspectFormatRoundTrip(t *testing.T) {
	if strings.ContainsAny(ContainerInspectFormat, "\n\r") {
		t.Errorf("the format spreads over several lines: %q", ContainerInspectFormat)
	}
	var out strings.Builder
	for _, c := range []map[string]any{
		inspected("/web", "nginx:1.27", "always", "bridge", map[string]any{
			"80/tcp":  []any{binding("0.0.0.0", "8080"), binding("::", "8080")},
			"443/tcp": nil,
			"53/udp":  []any{binding("0.0.0.0", "53")},
		}),
		inspected("/db", "mariadb:11", "unless-stopped", "default", map[string]any{}),
		inspected("/pihole", "pihole/pihole", "no", "host", map[string]any{}),
		// third-party text: quotes, backslashes, HTML, a new line, a line separator
		inspected("/hostile", "a\"b\\c<img src=x onerror=alert(1)>\nd\u2028e", "always", "bridge", nil),
	} {
		out.WriteString(dockerTemplate(t, ContainerInspectFormat, c))
		out.WriteString("\n") // docker inspect puts the new line itself
	}
	if n := strings.Count(out.String(), "\n"); n != 4 {
		t.Fatalf("four containers made %d lines:\n%s", n, out.String())
	}
	got := ParseContainers(out.String())
	want := []Container{
		{Name: "db", Image: "mariadb:11", RestartPolicy: "unless-stopped", Ports: []Port{}},
		{Name: "hostile", Image: "a\"b\\c<img src=x onerror=alert(1)>de", RestartPolicy: "always", Ports: []Port{}},
		{Name: "pihole", Image: "pihole/pihole", RestartPolicy: "no", HostNetwork: true, Ports: []Port{}},
		{Name: "web", Image: "nginx:1.27", RestartPolicy: "always", Ports: []Port{{Port: 53, Protocol: "udp", HostPort: 53}, {Port: 80, Protocol: "tcp", HostPort: 8080}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseContainers() =\n%#v\nwant\n%#v", got, want)
	}
}

// Whatever a field holds, the template's output is one line of valid JSON: that is what
// {{json}} is for, and a field that went in with {{.Field}} would break it.
func TestContainerInspectFormatEscapesEveryFreeTextField(t *testing.T) {
	hostile := "a\"b\\c\nd\te</script>{{.}}\u2028"
	for _, c := range []map[string]any{
		inspected(hostile, "x", "no", "bridge", nil),
		inspected("/x", hostile, "no", "bridge", nil),
		inspected("/x", "x", hostile, "bridge", nil),
		inspected("/x", "x", "no", hostile, nil),
		inspected("/x", "x", "no", "bridge", map[string]any{hostile: []any{binding(hostile, hostile)}}),
	} {
		out := dockerTemplate(t, ContainerInspectFormat, c)
		if strings.ContainsAny(out, "\n\r") || !json.Valid([]byte(out)) {
			t.Errorf("the output is not one line of JSON: %q", out)
		}
	}
	for _, v := range []any{hostile, nil} {
		info := map[string]any{"ServerVersion": v, "Swarm": map[string]any{"LocalNodeState": hostile}}
		if out := dockerTemplate(t, DaemonInfoFormat, info); strings.ContainsAny(out, "\n\r") || !json.Valid([]byte(out)) {
			t.Errorf("the daemon info is not one line of JSON: %q", out)
		}
	}
}

const inspectLines = `{"name":"/web","image":"nginx:1.27","restart":"always","network":"bridge","ports":{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"8080"},{"HostIp":"::","HostPort":"8080"}],"443/tcp":null,"53/udp":[{"HostIp":"0.0.0.0","HostPort":"53"}]}}
{"name":"/db","image":"mariadb:11","restart":"unless-stopped","network":"bridge","ports":{}}
{"name":"/pihole","image":"pihole/pihole","restart":"no","network":"host","ports":{}}
`

func TestParseContainers(t *testing.T) {
	got := ParseContainers(inspectLines)
	want := []Container{
		{Name: "db", Image: "mariadb:11", RestartPolicy: "unless-stopped", Ports: []Port{}},
		{Name: "pihole", Image: "pihole/pihole", RestartPolicy: "no", HostNetwork: true, Ports: []Port{}},
		{Name: "web", Image: "nginx:1.27", RestartPolicy: "always", Ports: []Port{{Port: 53, Protocol: "udp", HostPort: 53}, {Port: 80, Protocol: "tcp", HostPort: 8080}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseContainers() =\n%#v\nwant\n%#v", got, want)
	}
	// the JSON the API sends
	encoded, err := json.Marshal(got[2])
	if err != nil || string(encoded) != `{"name":"web","image":"nginx:1.27","restart_policy":"always","host_network":false,"ports":[{"port":53,"protocol":"udp","host_port":53},{"port":80,"protocol":"tcp","host_port":8080}]}` {
		t.Errorf("Container as JSON = %s (%v)", encoded, err)
	}
}

func TestParseContainersNothing(t *testing.T) {
	for _, in := range []string{"", "\n\n", "   \n", "Error response from daemon: No such container: x\n"} {
		got := ParseContainers(in)
		if got == nil || len(got) != 0 {
			t.Errorf("ParseContainers(%q) = %#v, want an empty, non-nil list", in, got)
		}
	}
}

func TestParseContainersSkipsMalformedLinesAndKeepsTheRest(t *testing.T) {
	in := strings.Join([]string{
		`not json at all`,
		`{"name":"/truncated","image":"x","restart":"no","netw`,
		`[]`,
		`null`,
		`42`,
		`"/web"`,
		`{"name":"/typed","image":"x","restart":"no","network":"bridge","ports":"80"}`,
		`{"name":"/typed2","image":7,"restart":"no","network":"bridge","ports":{}}`,
		`{"name":"/ok","image":"nginx","restart":"no","network":"bridge","ports":{}}` + "\r",
		`   {"name":"/ok2","image":"nginx","restart":"","network":"bridge","ports":{}}   `,
		``,
	}, "\n")
	got := ParseContainers(in)
	if len(got) != 2 || got[0].Name != "ok" || got[1].Name != "ok2" || got[1].RestartPolicy != "" {
		t.Errorf("ParseContainers() = %#v", got)
	}
}

func containerLine(name, policy string) string {
	nameJSON, _ := json.Marshal(name)
	policyJSON, _ := json.Marshal(policy)
	return `{"name":` + string(nameJSON) + `,"image":"x","restart":` + string(policyJSON) + `,"network":"bridge","ports":{}}` + "\n"
}

func TestParseContainersValidatesNamesAndPolicies(t *testing.T) {
	var accepted = []string{"web", "/web", "a", "A1", "0day", "my_app-1.2", strings.Repeat("a", 128), "/" + strings.Repeat("a", 128)}
	for _, name := range accepted {
		if got := ParseContainers(containerLine(name, "always")); len(got) != 1 || got[0].Name != strings.TrimPrefix(name, "/") {
			t.Errorf("name %q gave %#v", name, got)
		}
	}
	for _, name := range []string{"", "/", "//web", "-web", "_web", ".web", "/-web", "a b", "/a b", "x;rm -rf /", "$(id)", "`id`", "a\nb", "a\tb", "a/b", "web\x00", "wéb", "a&b", "a|b", "a>b", "a'b", `a"b`, "a\\b", "a*", "a:b", "a@b", strings.Repeat("a", 129), "/" + strings.Repeat("a", 129), "CASAOS_DOCKER_UPDATE_SUCCESS x"} {
		if got := ParseContainers(containerLine(name, "always")); len(got) != 0 {
			t.Errorf("name %q was accepted: %#v", name, got)
		}
	}
	for _, policy := range []string{"", "no", "always", "unless-stopped", "on-failure"} {
		if got := ParseContainers(containerLine("web", policy)); len(got) != 1 || got[0].RestartPolicy != policy {
			t.Errorf("policy %q gave %#v", policy, got)
		}
	}
	for _, policy := range []string{"Always", "always;x", "always x", "on-failure:5", "$(id)", "a\nb", "alwayś", strings.Repeat("a", 33), "1", "no_"} {
		if got := ParseContainers(containerLine("web", policy)); len(got) != 0 {
			t.Errorf("policy %q was accepted: %#v", policy, got)
		}
	}
}

func TestParseContainersBoundsAndCleansTheImage(t *testing.T) {
	// one byte, then two-byte characters: the 256th byte is the first half of one
	long := "a" + strings.Repeat("é", 1000)
	line := `{"name":"/web","image":` + mustJSON(long) + `,"restart":"no","network":"bridge","ports":{}}`
	got := ParseContainers(line)
	if len(got) != 1 {
		t.Fatalf("an overlong image gave %#v", got)
	}
	if n := len(got[0].Image); n != 255 || !utf8.ValidString(got[0].Image) {
		t.Fatalf("an overlong image gave %d bytes, valid %v", n, utf8.ValidString(got[0].Image))
	}
	line = `{"name":"/web","image":` + mustJSON("ngi\x1b[31mnx\r\n\x00\x7f:1") + `,"restart":"no","network":"bridge","ports":{}}`
	if got := ParseContainers(line); len(got) != 1 || got[0].Image != "ngi[31mnx:1" {
		t.Errorf("control characters in the image gave %#v", got)
	}
	// text stays text: the page escapes it
	html := `<img src=x onerror=alert(1)>`
	line = `{"name":"/web","image":` + mustJSON(html) + `,"restart":"no","network":"bridge","ports":{}}`
	if got := ParseContainers(line); len(got) != 1 || got[0].Image != html {
		t.Errorf("an image with HTML gave %#v", got)
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestParseContainersPorts(t *testing.T) {
	ports := `{` +
		`"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"8080"}],` + // published
		`"81":[{"HostIp":"0.0.0.0","HostPort":"8081"}],` + // no protocol: tcp
		`"443/tcp":null,` + // exposed, not published
		`"8443/tcp":[],` +
		`"9000/tcp":[{"HostIp":"0.0.0.0","HostPort":""}],` +
		`"9001/tcp":[{"HostIp":"0.0.0.0","HostPort":"0"}],` +
		`"9002/tcp":[{"HostIp":"0.0.0.0","HostPort":"99999"}],` +
		`"9003/tcp":[{"HostIp":"0.0.0.0","HostPort":"-1"}],` +
		`"9004/tcp":[{"HostIp":"0.0.0.0","HostPort":"+80"}],` +
		`"9005/tcp":[{"HostIp":"0.0.0.0","HostPort":"80a"}],` +
		`"9006/tcp":[{"HostIp":"0.0.0.0","HostPort":"1-2"}],` +
		`"0/tcp":[{"HostIp":"0.0.0.0","HostPort":"1234"}],` +
		`"70000/tcp":[{"HostIp":"0.0.0.0","HostPort":"1235"}],` +
		`"abc/tcp":[{"HostIp":"0.0.0.0","HostPort":"1236"}],` +
		`"-5/tcp":[{"HostIp":"0.0.0.0","HostPort":"1237"}],` +
		`"80/x;rm":[{"HostIp":"0.0.0.0","HostPort":"1238"}],` +
		`"80/":[{"HostIp":"0.0.0.0","HostPort":"1239"}],` +
		`"53/udp":[{"HostIp":"0.0.0.0","HostPort":"53"},{"HostIp":"::","HostPort":"53"}],` +
		`"5000/sctp":[{"HostIp":"0.0.0.0","HostPort":"5000"}],` +
		`"1000/tcp":[{"HostIp":"0.0.0.0","HostPort":"2000"},{"HostIp":"0.0.0.0","HostPort":"1999"}]` +
		`}`
	got := ParseContainers(`{"name":"/p","image":"x","restart":"no","network":"bridge","ports":` + ports + `}`)
	want := []Port{
		{Port: 53, Protocol: "udp", HostPort: 53},
		{Port: 80, Protocol: "tcp", HostPort: 8080},
		{Port: 81, Protocol: "tcp", HostPort: 8081},
		{Port: 1000, Protocol: "tcp", HostPort: 1999},
		{Port: 1000, Protocol: "tcp", HostPort: 2000},
		{Port: 5000, Protocol: "sctp", HostPort: 5000},
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0].Ports, want) {
		t.Errorf("ports = %#v\nwant %#v", got, want)
	}
}

func TestParseContainersHostNetwork(t *testing.T) {
	for network, want := range map[string]bool{"host": true, "bridge": false, "default": false, "none": false, "container:abc": false, "Host": false, "host ": false, "": false, "my-host": false} {
		line := `{"name":"/n","image":"x","restart":"no","network":` + mustJSON(network) + `,"ports":{}}`
		if got := ParseContainers(line); len(got) != 1 || got[0].HostNetwork != want {
			t.Errorf("network %q gave %#v, want host network %v", network, got, want)
		}
	}
}

func TestParseContainersSortedByName(t *testing.T) {
	got := ParseContainers(containerLine("zeta", "no") + containerLine("alpha", "no") + containerLine("Beta", "no") + containerLine("mid", "no"))
	var names []string
	for _, c := range got {
		names = append(names, c.Name)
	}
	if !reflect.DeepEqual(names, []string{"Beta", "alpha", "mid", "zeta"}) {
		t.Errorf("names = %v", names)
	}
}

func TestDaemonInfoFormatRoundTrip(t *testing.T) {
	if strings.ContainsAny(DaemonInfoFormat, "\n\r") {
		t.Errorf("the format spreads over several lines: %q", DaemonInfoFormat)
	}
	info := func(version, swarm string) map[string]any {
		return map[string]any{"ServerVersion": version, "Swarm": map[string]any{"LocalNodeState": swarm}}
	}
	cases := []struct {
		version, swarm string
		want           DaemonInfo
	}{
		{"28.0.4", "inactive", DaemonInfo{ServerVersion: "28.0.4"}},
		{"29.8.0", "active", DaemonInfo{ServerVersion: "29.8.0", SwarmActive: true}},
		{"29.8.0", "", DaemonInfo{ServerVersion: "29.8.0"}},
		{"29.8.0", "pending", DaemonInfo{ServerVersion: "29.8.0"}},
		{"29.8.0", "error", DaemonInfo{ServerVersion: "29.8.0"}},
		{"29.8.0", "locked", DaemonInfo{ServerVersion: "29.8.0"}},
		{"25.0.0-rc.1", "inactive", DaemonInfo{ServerVersion: "25.0.0-rc.1"}},
		{"20.10.24+dfsg1", "inactive", DaemonInfo{ServerVersion: "20.10.24+dfsg1"}},
	}
	for _, c := range cases {
		out := dockerTemplate(t, DaemonInfoFormat, info(c.version, c.swarm)) + "\n"
		got, err := ParseDaemonInfo(out)
		if err != nil || got != c.want {
			t.Errorf("%s / %s: ParseDaemonInfo(%q) = %#v, %v, want %#v", c.version, c.swarm, out, got, err, c.want)
		}
	}
}

func TestParseDaemonInfo(t *testing.T) {
	good := map[string]DaemonInfo{
		`{"ServerVersion":"29.8.0","SwarmState":"inactive"}`:                                                          {ServerVersion: "29.8.0"},
		`{"ServerVersion":"29.8.0","SwarmState":"active"}` + "\n":                                                     {ServerVersion: "29.8.0", SwarmActive: true},
		"  {\"ServerVersion\":\"29.8.0\",\"SwarmState\":\"active\"}\r\n":                                              {ServerVersion: "29.8.0", SwarmActive: true},
		`{"ServerVersion":"29.8.0"}`:                                                                                  {ServerVersion: "29.8.0"},
		`{"ServerVersion":"29.8.0","SwarmState":"Active"}`:                                                            {ServerVersion: "29.8.0"},
		"WARNING: something\n" + `{"ServerVersion":"28.0.4","SwarmState":"inactive"}`:                                 {ServerVersion: "28.0.4"},
		`{"ServerVersion":"28.0.4","SwarmState":"inactive"}` + "\n" + `{"ServerVersion":"1.0","SwarmState":"active"}`: {ServerVersion: "28.0.4"},
	}
	for in, want := range good {
		if got, err := ParseDaemonInfo(in); err != nil || got != want {
			t.Errorf("ParseDaemonInfo(%q) = %#v, %v, want %#v", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "\n", "garbage", "[]", "null", "{}", `{"ServerVersion":""}`, `{"ServerVersion":null}`, `{"ServerVersion":7}`,
		// what Docker CLI 29.8.0 printed for DaemonInfoFormat with no daemon running (exit status 1)
		`{"ServerVersion":"","SwarmState":""}` + "\n",
		`{"ServerVersion":"29.8.0"`, // cut off
		`{"SwarmState":"active"}`,   // the daemon did not answer: the CLI prints what it knows of itself
		`{"ServerVersion":"29.8.0;reboot"}`, `{"ServerVersion":"29 8"}`, `{"ServerVersion":"$(id)"}`, `{"ServerVersion":"-1"}`,
		`{"ServerVersion":"` + strings.Repeat("1", 101) + `"}`, `{"ServerVersion":"29.8.0\n"}`,
		"Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?",
	} {
		if got, err := ParseDaemonInfo(in); err == nil {
			t.Errorf("ParseDaemonInfo(%q) = %#v, want an error", in, got)
		}
	}
}
