package dockerpkg

import (
	"strings"
	"testing"
	"time"
)

func TestFailedMarkerIsTheLineParseRunReads(t *testing.T) {
	const nonce = "0123456789abcdef0123456789abcdef"
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	queued := QueuedMarker(nonce, at.Add(-time.Minute))
	for _, reason := range []string{FailGuard, FailDownload, FailInstall, FailDaemon} {
		line := FailedMarker(nonce, at, reason)
		if !strings.HasSuffix(line, "\n") || strings.Count(line, "\n") != 1 {
			t.Fatalf("FailedMarker() = %q, want one line", line)
		}
		run := ParseRun(queued + "systemd-run said no\n" + line)
		if run.Terminal != TerminalFailed || run.FailReason != reason || !run.CompletedAt.Equal(at) {
			t.Errorf("%s: run = %#v", reason, run)
		}
	}
	// another nonce is not the run's word
	if run := ParseRun(queued + FailedMarker("ffffffffffffffffffffffffffffffff", at, FailGuard)); run.Terminal != "" {
		t.Errorf("run = %#v", run)
	}
}
