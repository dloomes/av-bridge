package cloudpoll

import (
	"net/http"
	"testing"

	"github.com/dloomes/av-bridge/internal/config"
)

// The command long-poll must honour HTTPS_PROXY so a Collector behind an
// explicit outbound proxy still receives commands.
func TestPollerUsesProxyFromEnvironment(t *testing.T) {
	p := NewPoller(config.CloudConfig{}, "c1", nil)
	tr, ok := p.http.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatalf("command poll transport has no Proxy func; HTTPS_PROXY would be ignored")
	}
}
