package osverify

import (
	"slices"
	"strings"
	"testing"
)

func TestTheAddressesThatListenAreTheFourthColumnOfSS(t *testing.T) {
	for name, tc := range map[string]struct {
		out  string
		want []string
	}{
		"nothing listens":             {"", nil},
		"two sockets":                 {"LISTEN 0 4096 0.0.0.0:22 0.0.0.0:*\nLISTEN 0 128 [::]:22 [::]:*\n", []string{"0.0.0.0:22", "[::]:22"}},
		"sorted and once":             {"LISTEN 0 1 127.0.0.1:80 0.0.0.0:*\nLISTEN 0 1 0.0.0.0:22 0.0.0.0:*\nLISTEN 0 1 127.0.0.1:80 0.0.0.0:*\n", []string{"0.0.0.0:22", "127.0.0.1:80"}},
		"lines that are short":        {"LISTEN 0 1\n\nx\nLISTEN 0 1 0.0.0.0:80 0.0.0.0:*\n", []string{"0.0.0.0:80"}},
		"an address that is not text": {"LISTEN 0 1 \x1b[31m:80 0.0.0.0:*\nLISTEN 0 1 0.0.0.0:81 0.0.0.0:*\n", []string{"0.0.0.0:81"}},
		"windows line ends":           {"LISTEN 0 1 0.0.0.0:22 0.0.0.0:*\r\n", []string{"0.0.0.0:22"}},
	} {
		f := healthyBox(t)
		f.on(tc.out, 0, "ss", "-Hltn")
		got := take(t, f)
		if !got.Knows(FactListening) || !slices.Equal(got.Listening, tc.want) {
			t.Errorf("%s: %q (known %v), want %q", name, got.Listening, got.Knows(FactListening), tc.want)
		}
	}
	for name, set := range map[string]func(*fakeProbe){
		"ss is not there":    func(f *fakeProbe) { f.forget("ss", "-Hltn") },
		"ss fails":           func(f *fakeProbe) { f.on("LISTEN 0 1 0.0.0.0:22 0.0.0.0:*\n", 1, "ss", "-Hltn") },
		"its output was cut": func(f *fakeProbe) { f.onTruncated("LISTEN 0 1 0.0.0.0:22 0.0.0.0:*\n", "ss", "-Hltn") },
	} {
		f := healthyBox(t)
		set(f)
		if got := take(t, f); got.Knows(FactListening) || got.Listening != nil {
			t.Errorf("%s: %+v", name, got.Listening)
		}
	}
}

func TestTheUnitsThatFailedAreTheFirstColumnOfSystemctl(t *testing.T) {
	for name, tc := range map[string]struct {
		out  string
		want []string
	}{
		"none":               {"", nil},
		"two":                {"wsdd.service loaded failed failed WSD\nsmartd.service loaded failed failed S.M.A.R.T.\n", []string{"smartd.service", "wsdd.service"}},
		"once":               {"a.service loaded failed failed A\na.service loaded failed failed A\n", []string{"a.service"}},
		"what is not a unit": {"not a unit!\n-x.service loaded failed failed X\n" + strings.Repeat("a", 200) + ".service x\ngood@1.service loaded failed failed G\n", []string{"good@1.service"}},
		"windows line ends":  {"a.service loaded failed failed A\r\n", []string{"a.service"}},
	} {
		f := healthyBox(t)
		f.on(tc.out, 0, "systemctl", "--failed", "--no-legend", "--plain")
		got := take(t, f)
		if !got.Knows(FactFailed) || !slices.Equal(got.Failed, tc.want) {
			t.Errorf("%s: %q (known %v), want %q", name, got.Failed, got.Knows(FactFailed), tc.want)
		}
	}
	for name, set := range map[string]func(*fakeProbe){
		"systemctl fails": func(f *fakeProbe) {
			f.on("a.service loaded failed failed A\n", 1, "systemctl", "--failed", "--no-legend", "--plain")
		},
		"its output was cut": func(f *fakeProbe) {
			f.onTruncated("a.service loaded failed failed A\n", "systemctl", "--failed", "--no-legend", "--plain")
		},
		"it is not there": func(f *fakeProbe) { f.forget("systemctl", "--failed", "--no-legend", "--plain") },
	} {
		f := healthyBox(t)
		set(f)
		if got := take(t, f); got.Knows(FactFailed) || got.Failed != nil {
			t.Errorf("%s: %+v", name, got.Failed)
		}
	}
}

func TestTheListsOfTheMachineAreBounded(t *testing.T) {
	var ss, failed strings.Builder
	for i := 0; i < maxListening+30; i++ {
		ss.WriteString("LISTEN 0 1 10.0." + string(rune('a'+i/26/26%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i%26)) + ":80 0.0.0.0:*\n")
	}
	for i := 0; i < maxFailed+30; i++ {
		failed.WriteString("u" + string(rune('a'+i/26/26%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i%26)) + ".service loaded failed failed U\n")
	}
	f := healthyBox(t)
	f.on(ss.String(), 0, "ss", "-Hltn")
	f.on(failed.String(), 0, "systemctl", "--failed", "--no-legend", "--plain")
	got := take(t, f)
	if len(got.Listening) != maxListening || len(got.Failed) != maxFailed {
		t.Errorf("%d addresses and %d units are kept", len(got.Listening), len(got.Failed))
	}
}
