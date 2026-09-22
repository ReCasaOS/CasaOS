package route

import (
	"strings"
	"testing"
)

func TestTheAccessLogLeavesTheQueryStringOut(t *testing.T) {
	if strings.Contains(accessLogFormat, "${uri}") || !strings.Contains(accessLogFormat, "${path}") {
		t.Fatalf("the access log must log the path, not the URI with its query: %s", accessLogFormat)
	}
}
