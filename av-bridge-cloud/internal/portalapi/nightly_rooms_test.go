package portalapi

import (
	"encoding/json"
	"testing"
)

// Exclusion reasons live behind a text CHECK constraint in migration 0042.
// If a caller can slip an unknown value past the API validator the DB will
// reject the write with a constraint violation — surfaced to the operator
// as a 500. These tests keep the API-layer allowlist in sync with the DB
// constraint so we return a clean 400 with an actionable message instead.

// TestValidExclusionReasons_ContainsMoJRequirementSet asserts that every
// reason called out by the MoJ-shaped requirements (FR39 + FR40) is
// present in the API allowlist. Removing any of these silently is a
// regression against a signed-off requirement.
func TestValidExclusionReasons_ContainsRequirementSet(t *testing.T) {
	required := []string{
		"in_use",               // FR39 — courtroom remains in use
		"active_incident",      // FR40 — first bullet
		"awaiting_replacement", // FR40 — second bullet
		"planned_maintenance",  // FR40 — third bullet
	}
	for _, r := range required {
		if _, ok := validExclusionReasons[r]; !ok {
			t.Errorf("required reason %q missing from validExclusionReasons — FR39/FR40 regression", r)
		}
	}
}

// TestValidExclusionReasons_RejectsUnknown ensures we do not silently
// accept an arbitrary string. If we ever add a new reason, add it to
// the required list above too.
func TestValidExclusionReasons_RejectsUnknown(t *testing.T) {
	unknown := []string{
		"", "unknown", "power_cycling", "IN_USE",
		"in-use", "  in_use  ",
	}
	for _, s := range unknown {
		if _, ok := validExclusionReasons[s]; ok {
			t.Errorf("unexpected reason %q accepted — enum drifted from CHECK constraint", s)
		}
	}
}

// TestUpdateRoomOverrideReq_RawMessageSemantics locks in the three-state
// contract for optional fields (absent / null / value). We rely on this
// distinction to tell "leave unchanged" apart from "clear to inherit".
func TestUpdateRoomOverrideReq_RawMessageSemantics(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantSet bool // ExcludedReason is present in the payload
	}{
		{"absent", `{}`, false},
		{"explicit null", `{"excluded_reason": null}`, true},
		{"value", `{"excluded_reason": "in_use"}`, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var req updateRoomOverrideReq
			if err := json.Unmarshal([]byte(tc.payload), &req); err != nil {
				t.Fatalf("unmarshal %q: %v", tc.payload, err)
			}
			gotSet := len(req.ExcludedReason) > 0
			if gotSet != tc.wantSet {
				t.Errorf("payload %q — set=%v, want=%v", tc.payload, gotSet, tc.wantSet)
			}
		})
	}
}

// TestDeferTonightReq_EmptyBodyOmitsNote sanity-checks that the
// DeferTonight body parser tolerates every reasonable operator input:
// no body, empty body, whitespace-only note, real note.
func TestDeferTonightReq_ParseShapes(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"empty object",         `{}`,                          ""},
		{"empty note field",     `{"note":""}`,                 ""},
		{"whitespace note",      `{"note":"   "}`,              "   "},
		{"real note",            `{"note":"late court sitting"}`, "late court sitting"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var req deferTonightReq
			if err := json.Unmarshal([]byte(tc.payload), &req); err != nil {
				t.Fatalf("unmarshal %q: %v", tc.payload, err)
			}
			if req.Note != tc.want {
				t.Errorf("payload %q — note=%q, want=%q", tc.payload, req.Note, tc.want)
			}
		})
	}
}
