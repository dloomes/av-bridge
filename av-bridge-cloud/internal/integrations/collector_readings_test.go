package integrations

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestReadingsFromMetrics(t *testing.T) {
	decode := func(s string) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	r := ReadingsFromMetrics("online", decode(`{"teams_signed_in": false, "teams_health": "unhealthy",
		"peripherals_linked": false,
		"peripherals": [{"name":"Fake Cam","model":"Camera","connected":true},{"name":"Fake Mic","model":"Microphone","connected":false}],
		"peripherals_missing": ["Microphone"]}`))
	if r.Status != "online" || r.Source != SourceCollector || r.SignedIn == nil || *r.SignedIn ||
		r.TeamsHealth != "unhealthy" || r.PeripheralsLinked == nil || *r.PeripheralsLinked ||
		r.LinkedDevices != 2 || !reflect.DeepEqual(r.MissingPeripherals, []string{"Microphone"}) {
		t.Errorf("full readings: %+v", r)
	}

	// No missing list: named from the not-connected peripherals.
	r = ReadingsFromMetrics("online", decode(`{"peripherals": [{"name":"Fake Speaker","connected":false},{"name":"x","model":"Camera","connected":true}]}`))
	if r.LinkedDevices != 2 || !reflect.DeepEqual(r.MissingPeripherals, []string{"Fake Speaker"}) || r.SignedIn != nil {
		t.Errorf("derived missing: %+v", r)
	}

	// Unknowns stay unknown; a degraded status says nothing about offline.
	r = ReadingsFromMetrics("degraded", decode(`{"teams_signed_in": "yes"}`))
	if r.Status != "" || r.SignedIn != nil || r.PeripheralsLinked != nil || r.LinkedDevices != 0 {
		t.Errorf("unknowns: %+v", r)
	}
	if _, known := conditions[0].state(r); known {
		t.Error("degraded status must not decide device_offline")
	}
}
