//go:build lensverify

package polylens

import (
	"context"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newVerifyClient(t *testing.T) *Client {
	id, sec := os.Getenv("LENS_CLIENT_ID"), os.Getenv("LENS_CLIENT_SECRET")
	if id == "" || sec == "" {
		t.Skip("LENS_CLIENT_ID/LENS_CLIENT_SECRET not set")
	}
	return New(id, sec)
}

// TestLensVerify prints only counts and distinct enum-like values. No names,
// serials, MACs or emails.
func TestLensVerify(t *testing.T) {
	c := newVerifyClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	accts, err := c.Accounts(ctx)
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	t.Logf("accounts: %d degraded=%v", len(accts), c.Degraded())
	for i, a := range accts {
		t.Logf("account[%d] idlen=%d", i, len(a.ID))
	}
	if len(accts) == 0 {
		return
	}
	acc := accts[0].ID
	all, err := c.search(ctx, acc, nil)
	if err != nil {
		t.Fatalf("search all: %v", err)
	}
	t.Logf("all devices: %d degraded=%v", len(all), c.Degraded())
	rooms, err := c.ListTeamsRooms(ctx, acc)
	if err != nil {
		t.Fatalf("ListTeamsRooms: %v", err)
	}
	t.Logf("teams rooms: %d degraded=%v", len(rooms), c.Degraded())

	sets := map[string]map[string]int{}
	add := func(k, v string) {
		if sets[k] == nil {
			sets[k] = map[string]int{}
		}
		sets[k][v]++
	}
	pl := map[string]int{}
	models := map[string]int{}
	for _, d := range all {
		add("AppName(all)", d.AppName)
	}
	for _, d := range rooms {
		add("AppName", d.AppName)
		add("AppVersion", d.AppVersion)
		add("CallStatus", d.CallStatus)
		add("TeamsHealth", d.TeamsHealth)
		add("PresenceAvailability", d.PresenceAvailability)
		add("PresenceActivity", d.PresenceActivity)
		add("Model", d.Model)
		switch {
		case d.PeripheralsLinked == nil:
			pl["nil"]++
		case *d.PeripheralsLinked:
			pl["true"]++
		default:
			pl["false"]++
		}
	}
	_ = models
	keys := make([]string, 0, len(sets))
	for k := range sets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts := []string{}
		for v, n := range sets[k] {
			parts = append(parts, "'"+v+"' x"+strings.TrimSpace(fmtInt(n)))
		}
		sort.Strings(parts)
		t.Logf("%s: %s", k, strings.Join(parts, ", "))
	}
	t.Logf("PeripheralsLinked: %v", pl)

	n := 0
	var ids []string
	for _, d := range rooms {
		if n < 3 {
			ids = append(ids, d.ID)
			n++
		}
	}
	if len(ids) > 0 {
		got, err := c.Devices(ctx, acc, ids)
		if err != nil {
			t.Fatalf("Devices: %v", err)
		}
		t.Logf("Devices(%v): returned %d", ids, len(got))
	}
}

func fmtInt(n int) string { return strconv.Itoa(n) }
