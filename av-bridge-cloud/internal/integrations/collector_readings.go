package integrations

import "strings"

// SourceCollector marks alert notifications for Teams Rooms a Collector reads
// directly (protocol mtr_windows) rather than through Poly Lens.
const SourceCollector = "collector"

// ReadingsFromMetrics maps a Collector Teams Room's status and stored readings
// (devices.latest_metrics, the keys Lens rooms use too) to the Readings the
// Alerter evaluates. Keys that are absent or of the wrong shape stay unknown.
func ReadingsFromMetrics(status string, m map[string]any) Readings {
	r := Readings{Status: status, Source: SourceCollector}
	switch status {
	case "online", "offline", "unknown":
	default:
		// degraded or anything else: the device answered, so it is not offline,
		// but say nothing about online either.
		r.Status = ""
	}
	if v, ok := m["teams_signed_in"].(bool); ok {
		r.SignedIn = &v
	}
	if v, ok := m["peripherals_linked"].(bool); ok {
		r.PeripheralsLinked = &v
	}
	if s, ok := m["teams_health"].(string); ok {
		r.TeamsHealth = s
	}
	if list, ok := m["peripherals"].([]any); ok {
		r.LinkedDevices = len(list)
		r.MissingPeripherals = []string{}
		if names, ok := m["peripherals_missing"].([]any); ok {
			for _, n := range names {
				if s, ok := n.(string); ok && strings.TrimSpace(s) != "" {
					r.MissingPeripherals = append(r.MissingPeripherals, strings.TrimSpace(s))
				}
			}
		} else {
			// No list of missing names: name the not-connected ones as
			// missingPeripherals does for Lens (model, else name).
			for _, p := range list {
				e, ok := p.(map[string]any)
				if !ok {
					continue
				}
				if c, ok := e["connected"].(bool); !ok || c {
					continue
				}
				n, _ := e["model"].(string)
				if strings.TrimSpace(n) == "" {
					n, _ = e["name"].(string)
				}
				if n = strings.TrimSpace(n); n != "" {
					r.MissingPeripherals = append(r.MissingPeripherals, n)
				}
			}
		}
	}
	return r
}
