//go:build lensverify

package polylens

import (
	"context"
	"strings"
	"testing"
	"time"
)

const verifyTag = "marcus:verify-test"

// TestLensTagRoundTrip tags two Teams Rooms devices, confirms, removes the tag
// and deletes the throwaway account-level tag. Prints opaque ids only.
func TestLensTagRoundTrip(t *testing.T) {
	c := newVerifyClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	accts, err := c.Accounts(ctx)
	if err != nil || len(accts) == 0 {
		t.Fatalf("accounts: %v", err)
	}
	acc := accts[0].ID
	rooms, err := c.ListTeamsRooms(ctx, acc)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, d := range rooms {
		clean := true
		for _, tg := range d.Tags {
			if strings.HasPrefix(tg, "marcus:") {
				clean = false
			}
		}
		if clean && len(ids) < 2 {
			ids = append(ids, d.ID)
		}
	}
	if len(ids) < 2 {
		t.Fatalf("need 2 untagged teams devices, have %d", len(ids))
	}
	t.Logf("devices used: %v", ids)
	hasTag := func(id string) bool {
		got, err := c.Devices(ctx, acc, []string{id})
		if err != nil || len(got) != 1 {
			t.Fatalf("Devices: %v n=%d", err, len(got))
		}
		for _, tg := range got[0].Tags {
			if tg == verifyTag {
				return true
			}
		}
		return false
	}
	defer func() {
		for _, id := range ids {
			if err := c.RemoveTag(ctx, acc, id, verifyTag); err != nil {
				t.Errorf("cleanup RemoveTag %s: %v", id, err)
			}
		}
		tid, err := c.tagID(ctx, acc, verifyTag)
		if err == nil && tid != "" {
			if err := c.graphql(ctx, `mutation($id: String!) { deleteTag(id: $id) { success } }`, map[string]any{"id": tid}, nil); err != nil {
				t.Errorf("cleanup deleteTag: %v", err)
			}
		}
		for _, id := range ids {
			t.Logf("final: device %s has tag=%v", id, hasTag(id))
		}
		tid, _ = c.tagID(ctx, acc, verifyTag)
		t.Logf("final: tag entity exists=%v", tid != "")
	}()
	for _, id := range ids {
		if err := c.AddTag(ctx, acc, id, verifyTag); err != nil {
			t.Fatalf("AddTag %s: %v", id, err)
		}
		t.Logf("after add: device %s has tag=%v", id, hasTag(id))
	}
	for _, id := range ids {
		if err := c.RemoveTag(ctx, acc, id, verifyTag); err != nil {
			t.Fatalf("RemoveTag %s: %v", id, err)
		}
		t.Logf("after remove: device %s has tag=%v", id, hasTag(id))
	}
}
