package cloudpull

import (
	"net/http"
	"testing"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
)

// The config pull must honour HTTPS_PROXY so a Collector behind an explicit
// outbound proxy still receives its device list.
func TestPollerUsesProxyFromEnvironment(t *testing.T) {
	p := NewPoller(config.CloudConfig{}, time.Minute, "c1", "v", "t", "", nil)
	tr, ok := p.http.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatalf("config pull transport has no Proxy func; HTTPS_PROXY would be ignored")
	}
}
