package portalapi

import (
	"encoding/json"
	"testing"
)

// The BU CRUD surface is thin JSON-in / SQL-out — the load-bearing bits
// worth pinning are the request-shape parsers (three-state semantics for
// the region PATCH's business_unit_id, and the vendor-toggle body) and
// the response shape. Full end-to-end DB tests would need a real Postgres
// harness; those don't exist in this package (see FR39/40 test policy).

func TestUpdateRegionReq_BusinessUnitThreeState(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		set     bool
		clear   bool
	}{
		{"absent",         `{"name":"North"}`,                     false, false},
		{"explicit null",  `{"business_unit_id":null}`,            true,  true},
		{"empty string",   `{"business_unit_id":""}`,              true,  true},
		{"real value",     `{"business_unit_id":"abc-123"}`,       true,  false},
		{"whitespace",     `{"business_unit_id":" "}`,             true,  false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var req updateRegionReq
			if err := json.Unmarshal([]byte(tc.payload), &req); err != nil {
				t.Fatalf("unmarshal %q: %v", tc.payload, err)
			}
			set := len(req.BusinessUnitID) > 0
			if set != tc.set {
				t.Errorf("payload %q — set=%v, want=%v", tc.payload, set, tc.set)
			}
			if !set {
				return
			}
			// Emulate the handler's null / empty-string collapse.
			var clear bool
			if string(req.BusinessUnitID) == "null" {
				clear = true
			} else {
				var s string
				if err := json.Unmarshal(req.BusinessUnitID, &s); err == nil && s == "" {
					clear = true
				}
			}
			if clear != tc.clear {
				t.Errorf("payload %q — clear=%v, want=%v", tc.payload, clear, tc.clear)
			}
		})
	}
}

func TestCreateBusinessUnitReq_RequiresName(t *testing.T) {
	// An empty payload or missing name should fail the handler's early
	// guard. The struct parses fine but the guard sees Name=="" and
	// returns 400. We assert the struct parses to the expected value.
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"missing",       `{}`,                                          ""},
		{"empty string",  `{"name":""}`,                                 ""},
		{"real value",    `{"name":"HMCTS"}`,                            "HMCTS"},
		{"with desc",     `{"name":"HMCTS","description":"Courts"}`,     "HMCTS"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var req createBusinessUnitReq
			if err := json.Unmarshal([]byte(tc.payload), &req); err != nil {
				t.Fatalf("unmarshal %q: %v", tc.payload, err)
			}
			if req.Name != tc.want {
				t.Errorf("payload %q — name=%q, want=%q", tc.payload, req.Name, tc.want)
			}
		})
	}
}
