package osreadiness

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestScrubKeepsOnlyWhatMayBePasted(t *testing.T) {
	for in, want := range map[string]string{
		"see https://user:secret@repo.example.org:8443/apt/dists?token=abc#frag now":           "see https://repo.example.org now",
		"deb http://deb.debian.org/debian bullseye main":                                       "deb http://deb.debian.org bullseye main",
		"ftp://a@b@host.example/x":                                                             "ftp://host.example",
		"https://[2001:db8::1]:443/path":                                                       "https://[<ip>]",
		"loopback ::1 and 2001:db8::8a2e:370:7334 and 2001:0db8:0000:0000:0000:ff00:0042:8329": "loopback <ip> and <ip> and <ip>",
		"the option Dir::Cache::pkgcache= and 20:15:02":                                        "the option Dir::Cache::pkgcache= and 20:15:02",
		"address 192.168.1.20 and 10.0.0.1":                                                    "address <ip> and <ip>",
		"card 00:1A:2b:3c:4d:5E here":                                                          "card <mac> here",
		"card 00-1a-2b-3c-4d-5e":                                                               "card <mac>",
		"v6 fe80::1ff:fe23:4567:890a end":                                                      "v6 <ip> end",
		"kernel 5.10.0-35-amd64 and 1:9.2p1-2+deb12u7":                                         "kernel 5.10.0-35-amd64 and 1:9.2p1-2+deb12u7",
		"plain text": "plain text",
	} {
		if got := scrub(in); got != want {
			t.Errorf("scrub(%q) = %q, want %q", in, got, want)
		}
	}
}

func sampleReport() Report {
	return Report{
		CheckedAt: time.Date(2026, 10, 11, 20, 15, 2, 0, time.UTC),
		State:     StateBlocked,
		Hop: &Hop{
			From: Release{ID: "debian", VersionID: "11", Codename: "bullseye", PrettyName: "Debian GNU/Linux 11 (bullseye)"},
			To:   Release{ID: "debian", VersionID: "12", Codename: "bookworm", PrettyName: "Debian 12 (bookworm)"},
		},
		Counts: Counts{Refuse: 1, Warn: 1, OK: 2, Info: 1},
		Checks: []Finding{
			{Code: "broken_dpkg", Severity: Refuse, Title: "dpkg has an unfinished operation", Detail: "dpkg --audit lists packages that are not fully configured.", Data: D{"audit": true, "updates": 2, "unknown": false}, Action: ActionFinishDpkg},
			{Code: "sources_third_party", Severity: Warn, Title: "Some software comes from other sources", Detail: "Docker's is put back after the hop.", Data: D{"files": []string{"docker.list", "tailscale.list"}, "unknown": false}},
			{Code: "held_docker", Severity: Info, Title: "Docker's packages are on hold", Data: D{"packages": []string{"docker-ce"}, "unknown": false}, Action: ActionNone},
			{Code: "container", Severity: OK, Title: "The system is not a container", Data: D{"unknown": false}},
			{Code: "not_root", Severity: OK, Title: "ReCasaOS runs as root", Data: D{"unknown": false}},
		},
	}
}

func TestTheTextIsPlainEnglishWithEverythingInIt(t *testing.T) {
	got := Text(sampleReport())
	for _, want := range []string{
		"Checked: 2026-10-11T20:15:02Z",
		"Result: Not ready: 1 blocker.",
		"From: Debian GNU/Linux 11 (bullseye)",
		"To: Debian 12 (bookworm)",
		"Blockers 1, warnings 1, notes 1, ok 2",
		"BLOCKERS\n[broken_dpkg] dpkg has an unfinished operation\n    dpkg --audit lists packages that are not fully configured.\n    audit: true\n    updates: 2\n    suggested: finish_dpkg\n",
		"WARNINGS\n[sources_third_party] Some software comes from other sources\n    Docker's is put back after the hop.\n    files: docker.list, tailscale.list\n",
		"NOTES\n[held_docker] Docker's packages are on hold\n    packages: docker-ce\n",
		"OK (2): container, not_root\n",
		"This check changes nothing on the machine.\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the text has no %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "unknown") || strings.Contains(got, "suggested: none") {
		t.Errorf("noise in the text:\n%s", got)
	}
	if strings.ContainsRune(got, '\r') || strings.Contains(got, "\t") {
		t.Errorf("control characters in the text")
	}
}

func TestACheckThatCouldNotBeMadeIsSaidSo(t *testing.T) {
	r := Report{State: StateBlocked, Counts: Counts{Refuse: 1}, Checks: []Finding{
		{Code: "apps", Severity: Refuse, Title: "Could not check: No app has an operation in progress", Detail: "This could not be checked: it did not answer in time.", Data: D{"unknown": true, "reason": ReasonTimeout}},
	}}
	got := Text(r)
	if !strings.Contains(got, "[apps] Could not check: No app has an operation in progress\n    This could not be checked: it did not answer in time.\n") || strings.Contains(got, "reason") {
		t.Fatalf("text:\n%s", got)
	}
}

func TestTheTextHasNoSecretsWhateverPutThemThere(t *testing.T) {
	r := sampleReport()
	r.Hop.From.PrettyName = "Debian at https://user:pw@internal.example:81/x?y=1"
	r.Checks[0].Detail = "from 192.168.1.20 (aa:bb:cc:dd:ee:ff) see http://admin:hunter2@10.0.0.5:8080/path?token=abc"
	r.Checks[1].Data = D{"hosts": []string{"https://u:p@repo.example.org/apt?k=v"}, "unknown": false}
	got := Text(r)
	for _, secret := range []string{"user:pw", "hunter2", "admin", "token=abc", "192.168", "10.0.0.5", "aa:bb:cc", "?y=1", "?k=v", "u:p@", ":8080", ":81"} {
		if strings.Contains(got, secret) {
			t.Errorf("the text has %q:\n%s", secret, got)
		}
	}
	if !strings.Contains(got, "https://internal.example") || !strings.Contains(got, "https://repo.example.org") {
		t.Errorf("the scheme and the host are gone:\n%s", got)
	}
}

func TestTheTextIsAtMostEightKiBAndSaysWhatItLeftOut(t *testing.T) {
	r := sampleReport()
	r.Checks = nil
	for i := 0; i < 400; i++ {
		r.Checks = append(r.Checks, Finding{Code: fmt.Sprintf("check_%03d", i), Severity: Warn, Title: "Something to look at, said in a sentence of some length", Detail: strings.Repeat("and a detail ", 10), Data: D{"files": []string{"a.list", "b.list"}}})
	}
	got := Text(r)
	if len(got) > MaxTextBytes {
		t.Fatalf("the text is %d bytes", len(got))
	}
	if !strings.HasSuffix(got, "This check changes nothing on the machine.\n") || !strings.Contains(got, "(the rest is on the page)") {
		t.Fatalf("the end of the text:\n%s", got[len(got)-300:])
	}
	if strings.Contains(got, "check_399") || !strings.Contains(got, "check_000") {
		t.Fatalf("the text is not cut where it should be")
	}
	// a short report is whole
	if short := Text(sampleReport()); strings.Contains(short, "the rest is on the page") {
		t.Fatal("a short report is cut")
	}
}

func TestEachStateHasItsSentence(t *testing.T) {
	for state, want := range map[string]string{
		StateReady:       "Result: Ready.",
		StateAttention:   "Result: Ready, with things to look at.",
		StateLatest:      "Result: Latest release: there is nothing to upgrade to.",
		StateUnsupported: "Result: Not supported for an upgrade from here.",
	} {
		if got := Text(Report{State: state}); !strings.Contains(got, want) {
			t.Errorf("%s: %s", state, got)
		}
	}
	if got := Text(Report{State: StateBlocked, Counts: Counts{Refuse: 3}}); !strings.Contains(got, "Result: Not ready: 3 blockers.") {
		t.Errorf("blocked: %s", got)
	}
}

func TestTheAnswerCarriesTheTextAndFlattensTheReport(t *testing.T) {
	answer := NewAnswer(sampleReport())
	if answer.Text != Text(sampleReport()) {
		t.Fatal("the text of the answer is not the text of the report")
	}
	data, err := json.Marshal(answer)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"checked_at"`, `"state"`, `"hop"`, `"counts"`, `"checks"`, `"text"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("no %s in %s", key, data)
		}
	}
	if strings.Contains(string(data), `"Report"`) {
		t.Errorf("the report is nested: %s", data)
	}
}

func manyFindings(severity Severity, n int, detail string) []Finding {
	var findings []Finding
	for i := 0; i < n; i++ {
		findings = append(findings, Finding{Code: fmt.Sprintf("check_%03d", i), Severity: severity, Title: "Something to look at, said in a sentence of some length", Detail: detail, Data: D{"files": []string{"a.list", "b.list"}}, Action: ActionReviewSources})
	}
	return findings
}

// A report that does not fit says less of each finding before it leaves one out: the owner who
// pastes it into a forum gets every code, and the page has the rest.
func TestAReportThatDoesNotFitLosesItsDetailsBeforeItLosesAFinding(t *testing.T) {
	blocker := Finding{Code: "broken_dpkg", Severity: Refuse, Title: "dpkg has an unfinished operation", Detail: "the detail of the blocker", Data: D{"audit": true}, Action: ActionFinishDpkg}
	for name, tc := range map[string]struct {
		findings []Finding
		blockers bool // the details of the blockers are kept
		details  bool // the details of the others are kept
	}{
		"one blocker and warnings of 195 bytes of detail": {append([]Finding{blocker}, manyFindings(Warn, 60, strings.Repeat("and a detail ", 15))...), true, false},
		"blockers whose details do not fit either":        {manyFindings(Refuse, 80, strings.Repeat("and a detail of a blocker ", 12)), false, false},
		"a report that fits":                              {append([]Finding{blocker}, manyFindings(Warn, 3, "and a detail and a detail")...), true, true},
	} {
		t.Run(name, func(t *testing.T) {
			r := sampleReport()
			r.Checks = tc.findings
			got := Text(r)
			if len(got) > MaxTextBytes {
				t.Fatalf("the text is %d bytes", len(got))
			}
			for _, f := range tc.findings {
				if !strings.Contains(got, "["+f.Code+"] ") {
					t.Fatalf("%s is not in the text:\n%s", f.Code, got)
				}
			}
			if strings.Contains(got, "the rest is on the page") {
				t.Errorf("a finding was left out")
			}
			if has := strings.Contains(got, "the detail of the blocker") || strings.Contains(got, "and a detail of a blocker"); has != tc.blockers {
				t.Errorf("the details of the blockers are in the text: %v, want %v", has, tc.blockers)
			}
			if has := strings.Contains(got, "and a detail and a detail"); has != tc.details {
				t.Errorf("the details of the others are in the text: %v, want %v", has, tc.details)
			}
			if said := strings.Contains(got, "Details are left out here"); said == (tc.details && tc.blockers) {
				t.Errorf("the text says that details are left out: %v", said)
			}
			if !strings.HasSuffix(got, "This check changes nothing on the machine.\n") {
				t.Errorf("no end")
			}
		})
	}
}
