package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dloomes/av-bridge/internal/device"
)

// metricsHandler exposes a minimal Prometheus-compatible /metrics endpoint.
// No external library needed — we write the text format directly.
func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	devs := s.hub.Devices()

	var b strings.Builder
	now := time.Now().UnixMilli()

	b.WriteString("# HELP av_bridge_device_online Whether the device is online (1) or not (0)\n")
	b.WriteString("# TYPE av_bridge_device_online gauge\n")
	for _, d := range devs {
		info := d.Info()
		val := 0
		if d.Status() == device.StatusOnline {
			val = 1
		}
		fmt.Fprintf(&b, `av_bridge_device_online{id=%q,name=%q,type=%q,location=%q} %d %d`+"\n",
			info.ID, info.Name, info.Type, info.Location, val, now)
	}

	b.WriteString("\n# HELP av_bridge_device_status Device status as numeric (0=unknown,1=online,2=degraded,3=offline)\n")
	b.WriteString("# TYPE av_bridge_device_status gauge\n")
	statusNum := map[device.Status]int{
		device.StatusUnknown:  0,
		device.StatusOnline:   1,
		device.StatusDegraded: 2,
		device.StatusOffline:  3,
	}
	for _, d := range devs {
		info := d.Info()
		fmt.Fprintf(&b, `av_bridge_device_status{id=%q,name=%q,type=%q} %d %d`+"\n",
			info.ID, info.Name, info.Type, statusNum[d.Status()], now)
	}

	online, offline, degraded := 0, 0, 0
	for _, d := range devs {
		switch d.Status() {
		case device.StatusOnline:
			online++
		case device.StatusOffline:
			offline++
		case device.StatusDegraded:
			degraded++
		}
	}

	b.WriteString("\n# HELP av_bridge_devices_total Total number of configured devices\n")
	b.WriteString("# TYPE av_bridge_devices_total gauge\n")
	fmt.Fprintf(&b, "av_bridge_devices_total %d %d\n", len(devs), now)

	b.WriteString("\n# HELP av_bridge_devices_online Number of devices currently online\n")
	b.WriteString("# TYPE av_bridge_devices_online gauge\n")
	fmt.Fprintf(&b, "av_bridge_devices_online %d %d\n", online, now)

	b.WriteString("\n# HELP av_bridge_devices_offline Number of devices currently offline\n")
	b.WriteString("# TYPE av_bridge_devices_offline gauge\n")
	fmt.Fprintf(&b, "av_bridge_devices_offline %d %d\n", offline, now)

	b.WriteString("\n# HELP av_bridge_devices_degraded Number of devices in degraded state\n")
	b.WriteString("# TYPE av_bridge_devices_degraded gauge\n")
	fmt.Fprintf(&b, "av_bridge_devices_degraded %d %d\n", degraded, now)

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}
