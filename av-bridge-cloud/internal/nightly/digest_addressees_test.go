package nightly

import (
	"reflect"
	"testing"
)

// SES rejects a message whose To header is empty, so a digest going only to
// the helpdesk address must send it as To, not Cc.
func TestAddressees(t *testing.T) {
	cases := []struct {
		name           string
		to, cc         []string
		wantTo, wantCc []string
	}{
		{"helpdesk only becomes To", nil, []string{"helpdesk@example.com"}, []string{"helpdesk@example.com"}, nil},
		{"channels and helpdesk unchanged", []string{"a@example.com"}, []string{"helpdesk@example.com"}, []string{"a@example.com"}, []string{"helpdesk@example.com"}},
		{"channels only unchanged", []string{"a@example.com"}, nil, []string{"a@example.com"}, nil},
		{"nobody", nil, nil, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			to, cc := addressees(tc.to, tc.cc)
			if !reflect.DeepEqual(to, tc.wantTo) || !reflect.DeepEqual(cc, tc.wantCc) {
				t.Fatalf("addressees(%v, %v) = (%v, %v), want (%v, %v)", tc.to, tc.cc, to, cc, tc.wantTo, tc.wantCc)
			}
		})
	}
}
