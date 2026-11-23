package devicestatus

import (
	"strings"
	"testing"
)

// The Collector-staleness branch must only apply to devices that have a
// Collector; devices with none (Poly Lens managed) fall through to the
// 15-minute and latest_status branches.
func TestEffectiveStatusSQLGuardsCollectorBranch(t *testing.T) {
	first := EffectiveStatusSQL[:strings.Index(EffectiveStatusSQL, "THEN 'unknown'")]
	if !strings.Contains(first, "d.collector_id IS NOT NULL") {
		t.Errorf("collector-stale branch is not guarded by d.collector_id IS NOT NULL:\n%s", first)
	}
	for _, want := range []string{"'switched_off'", "interval '15 minutes'", "COALESCE(d.latest_status, 'unknown')"} {
		if !strings.Contains(EffectiveStatusSQL, want) {
			t.Errorf("EffectiveStatusSQL lost %s", want)
		}
	}
}
