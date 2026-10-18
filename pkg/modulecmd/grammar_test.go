package modulecmd

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// checkGrammar pins the grammar of the output of a run (job.go): whatever way a run ends, the lines the core reads are in the one shape, once, in the one order.
func checkGrammar(t *testing.T, f *fixture, exit int, remove, early bool, logBefore string) {
	t.Helper()
	out := strings.TrimRight(f.out.String(), "\n")
	lines := strings.Split(out, "\n")
	var steps []string
	results := 0
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, stepMarker):
			m := stepRe.FindStringSubmatch(l)
			if m == nil {
				t.Errorf("a marker that is not one: %q", l)
				continue
			}
			steps = append(steps, m[2]+" "+m[3])
		case strings.HasPrefix(l, resultMarker):
			results++
			if !resultRe.MatchString(l) {
				t.Errorf("a result that is not one: %q", l)
			}
			if i != len(lines)-1 && exit != ExitOK {
				t.Errorf("the result of a run that did not succeed is not the last line:\n%s", out)
			}
		}
	}
	if results != 1 {
		t.Fatalf("%d result lines, want exactly 1:\n%s", results, out)
	}
	last := lines[len(lines)-1]
	var result string
	for _, l := range lines {
		if strings.HasPrefix(l, resultMarker) {
			result = l
		}
	}
	fields := strings.Fields(result)
	kind, step, code, undo := fields[3], fields[4], fields[5], fields[6]
	// the kind is the exit status
	if want := map[int]string{ExitOK: "ok", ExitRefused: "refused", ExitFailed: "failed"}[exit]; kind != want {
		t.Errorf("exit %d and result %q:\n%s", exit, kind, out)
	}
	// every begin has its end, once, and a step that failed is not done
	open := ""
	ended := map[string]string{}
	for _, s := range steps {
		name, phase, _ := strings.Cut(s, " ")
		switch {
		case s == "done done":
			// (the end of a run is one marker, with no begin)
			if open != "" {
				t.Errorf("done while %s is open:\n%s", open, out)
			}
			ended[name] = phase
		case phase == "begin":
			if open != "" {
				t.Errorf("%s begins while %s is open:\n%s", name, open, out)
			}
			open = name
		default:
			if open != name {
				t.Errorf("%s %s with %q open:\n%s", name, phase, open, out)
			}
			open = ""
			if prior, ok := ended[name]; ok && prior != phase {
				t.Errorf("%s is %s and %s:\n%s", name, prior, phase, out)
			}
			ended[name] = phase
		}
	}
	if open != "" {
		t.Errorf("%s was begun and not ended:\n%s", open, out)
	}
	switch exit {
	case ExitRefused:
		if len(steps) != 0 || last != result || !strings.HasSuffix(lines[len(lines)-2], "refused: "+code) || undo != "-" || step != "-" {
			t.Errorf("a refusal:\n%s", out)
		}
	case ExitFailed:
		switch {
		case remove && undo != "-":
			t.Errorf("a remove has nothing to roll back: %q", undo)
		case !remove && undo != "rolled_back" && undo != "rollback_incomplete":
			t.Errorf("an install that failed says %q about its rollback", undo)
		}
		if step != "-" {
			if ended[step] != "failed" {
				t.Errorf("the result says %s failed:\n%s", step, out)
			}
			if !strings.Contains(out, "failed at step "+step+": "+code+"\n") {
				t.Errorf("the sentence and the result disagree:\n%s", out)
			}
		}
		for name, phase := range ended {
			if phase == "failed" && name != step && !strings.HasPrefix(name, "rollback_") {
				t.Errorf("%s failed and the result says %s:\n%s", name, step, out)
			}
		}
		if undo == "rollback_incomplete" && !strings.Contains(out, "rollback incomplete") {
			t.Errorf("an incomplete rollback that is not said:\n%s", out)
		}
	case ExitOK:
		if step != "done" || code != "-" || undo != "-" || ended["done"] != "done" {
			t.Errorf("a success:\n%s", out)
		}
	}
	// what is on the screen is in the log too, when the run had the log; a run refused before it had the lock leaves the log of whoever has it alone
	now, _ := os.ReadFile(f.path("/var/log/casaos/module-virt.log"))
	switch {
	case early && string(now) != logBefore:
		t.Errorf("a run that was refused before it had the lock wrote the log:\n%s", now)
	case !early:
		raw, err := os.ReadFile(f.path("/var/log/casaos/module-virt.log"))
		if err != nil || !strings.Contains(string(raw), result+"\n") {
			t.Errorf("the result is not in the log (%v):\n%s", err, raw)
		}
	}
}

// every way a run can end, with the output in the one grammar
func TestTheOutputOfEveryExitPathIsInTheOneGrammar(t *testing.T) {
	type scenario struct {
		name   string
		remove bool
		setup  func(f *fixture) (ctx context.Context)
		exit   int
	}
	// the refusals before the run has the lock, and so before it may write the log
	early := []string{"not root", "the lock is held", "the lock cannot be taken", "the log cannot be written", "a remove, not root", "a remove, the lock is held"}
	bg := context.Background()
	holdLock := func(f *fixture) {
		if err := os.MkdirAll(f.env.Roots.state(), 0o755); err != nil {
			f.t.Fatal(err)
		}
		release, err := f.host.Host.Lock(f.env.Roots.lock())
		if err != nil {
			f.t.Fatal(err)
		}
		f.t.Cleanup(release)
	}
	cancelAt := func(f *fixture, step string) context.Context {
		ctx, cancel := context.WithCancel(bg)
		f.t.Cleanup(cancel)
		f.env.StepHook = func(_ context.Context, s, phase string) {
			if s == step && phase == "begin" {
				cancel()
			}
		}
		return ctx
	}
	scenarios := []scenario{
		// installs that succeed or are refused
		{"an install", false, func(f *fixture) context.Context { return bg }, ExitOK},
		{"not root", false, func(f *fixture) context.Context { f.env.EUID = func() int { return 1000 }; return bg }, ExitRefused},
		{"no confirmation", false, func(f *fixture) context.Context { f.input = ""; return bg }, ExitRefused},
		{"declined", false, func(f *fixture) context.Context { f.isTTY, f.input = true, "n\n"; return bg }, ExitRefused},
		{"another system", false, func(f *fixture) context.Context {
			f.writeFile("/etc/os-release", "ID=debian\nVERSION_ID=\"11\"\n")
			return bg
		}, ExitRefused},
		{"the lock is held", false, func(f *fixture) context.Context { holdLock(f); return bg }, ExitRefused},
		{"the lock cannot be taken", false, func(f *fixture) context.Context {
			f.writeFile("/var/lib/casaos/modules", "a file where the directory goes")
			return bg
		}, ExitRefused},
		{"the log cannot be written", false, func(f *fixture) context.Context {
			if err := os.MkdirAll(f.path("/var/log/casaos/module-virt.log"), 0o755); err != nil {
				f.t.Fatal(err)
			}
			return bg
		}, ExitRefused},
		{"the area of the download is a file", false, func(f *fixture) context.Context { f.writeFile("/var/lib/casaos/modules/tmp", "x"); return bg }, ExitRefused},
		// installs that fail, at each step, for a reason and by an interruption
		{"apt fails", false, func(f *fixture) context.Context { f.runner.fail["apt-get install"] = 100; return bg }, ExitFailed},
		{"the download fails", false, func(f *fixture) context.Context { f.dl.err = errors.New("reset"); return bg }, ExitFailed},
		{"the checksum is not the catalogue's", false, func(f *fixture) context.Context {
			f.dl.body = append([]byte{}, f.dl.body...)
			f.dl.body[10] ^= 1
			return bg
		}, ExitFailed},
		{"the archive is refused", false, func(f *fixture) context.Context {
			f.setEntries(append(moduleEntries(manifestJSON(f.t, testManifest())), tent{name: "build/sysroot/usr/bin/../../../etc/cron.d/x", body: "x"}))
			return bg
		}, ExitFailed},
		{"systemd cannot read its units", false, func(f *fixture) context.Context { f.runner.fail["systemctl daemon-reload"] = 1; return bg }, ExitFailed},
		{"the manifest is not the catalogue's", false, func(f *fixture) context.Context {
			m := testManifest()
			m.Unit = "casaos-other.service"
			f.setEntries(moduleEntries(manifestJSON(f.t, m)))
			return bg
		}, ExitFailed},
		{"the unit does not start", false, func(f *fixture) context.Context { f.runner.fail["systemctl enable --now"] = 1; return bg }, ExitFailed},
		{"the gateway does not answer", false, func(f *fixture) context.Context { f.gw.never = true; return bg }, ExitFailed},
		{"a rollback that cannot finish", false, func(f *fixture) context.Context {
			f.runner.fail["systemctl enable --now"] = 1
			f.env.StepHook = func(_ context.Context, s, phase string) {
				if s == StepStart && phase == "begin" {
					p := f.path("/etc/casaos/virt-management.conf.sample")
					_ = os.Remove(p)
					_ = os.Mkdir(p, 0o755)
				}
			}
			return bg
		}, ExitFailed},
		{"a panic in the unpack", false, func(f *fixture) context.Context {
			f.host.wrapDest = func(d osmodule.Destination) osmodule.Destination {
				return &hookedDest{Destination: d, onFile: func() { panic("a panic of the test") }}
			}
			return bg
		}, ExitFailed},
		// removes
		{"a remove", true, func(f *fixture) context.Context { return bg }, ExitOK},
		{"a remove of what is not there", true, func(f *fixture) context.Context { return bg }, ExitRefused},
		{"a remove, not root", true, func(f *fixture) context.Context { f.env.EUID = func() int { return 1 }; return bg }, ExitRefused},
		{"a remove while dpkg works", true, func(f *fixture) context.Context {
			f.host.dpkg = func([]string) (bool, error) { return true, nil }
			return bg
		}, ExitRefused},
		{"a remove, the lock is held", true, func(f *fixture) context.Context { holdLock(f); return bg }, ExitRefused},
		{"a remove with no confirmation", true, func(f *fixture) context.Context { f.input = ""; return bg }, ExitRefused},
		{"a remove that is declined", true, func(f *fixture) context.Context { f.isTTY, f.input = true, "n\n"; return bg }, ExitRefused},
		{"a remove, the unit does not stop", true, func(f *fixture) context.Context { f.runner.fail["systemctl disable --now"] = 1; return bg }, ExitFailed},
		{"a remove, what is to be deleted cannot be", true, func(f *fixture) context.Context {
			_ = os.MkdirAll(tempName(f.path("/etc/casaos/virt-management.conf.sample"))+"/in", 0o755)
			return bg
		}, ExitFailed},
		{"a remove, the list cannot be read", true, func(f *fixture) context.Context { f.writeFile(filesList, ""); return bg }, ExitFailed},
		{"an install, the list cannot be read", false, func(f *fixture) context.Context { f.writeFile(dirsList, ""); return bg }, ExitRefused},
		{"a remove, systemd cannot read its units", true, func(f *fixture) context.Context { f.runner.fail["systemctl daemon-reload"] = 1; return bg }, ExitFailed},
		{"a remove that is interrupted", true, func(f *fixture) context.Context { return cancelAt(f, StepStop) }, ExitFailed},
		{"a panic in a remove", true, func(f *fixture) context.Context {
			f.runner.onRun = func(_ context.Context, argv []string) {
				if argv[0] == "systemctl" && argv[1] == "disable" {
					panic("a panic of the test")
				}
			}
			return bg
		}, ExitFailed},
	}
	for _, step := range allSteps {
		step := step
		scenarios = append(scenarios, scenario{"an install interrupted at " + step, false, func(f *fixture) context.Context { return cancelAt(f, step) }, ExitFailed})
	}
	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			var f *fixture
			if s.remove && s.name != "a remove of what is not there" {
				f = installed(t)
			} else {
				f = newFixture(t)
			}
			ctx := s.setup(f)
			before, _ := os.ReadFile(f.path("/var/log/casaos/module-virt.log"))
			args := []string{"--yes"}
			switch {
			case strings.Contains(s.name, "no confirmation") || strings.Contains(s.name, "declined"):
				args = nil
			}
			var exit int
			if s.remove {
				exit = f.main(ctx, append([]string{"remove", testName}, args...)...)
			} else {
				exit = f.main(ctx, append([]string{"install", testName}, args...)...)
			}
			if exit != s.exit {
				t.Fatalf("exit %d, want %d:\n%s", exit, s.exit, f.out)
			}
			checkGrammar(t, f, exit, s.remove, slices.Contains(early, s.name), string(before))
		})
	}
}
