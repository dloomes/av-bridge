package cloud

import (
	"net/http"
	"testing"

	"github.com/dloomes/av-bridge/internal/config"
)

// The telemetry client must honour HTTPS_PROXY so a Collector behind an
// explicit outbound proxy still reports.
func TestClientUsesProxyFromEnvironment(t *testing.T) {
	c := NewClient(config.CloudConfig{}, "c1", "s1", "v", "t")
	tr, ok := c.http.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatalf("telemetry transport has no Proxy func; HTTPS_PROXY would be ignored")
	}
}
