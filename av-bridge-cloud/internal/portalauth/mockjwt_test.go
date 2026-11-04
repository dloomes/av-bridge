package portalauth

import (
	"context"
	"encoding/base64"
	"testing"
)

type fakeLookup struct{ calls int }

func (f *fakeLookup) LookupCustomerByEntraTenant(context.Context, string) (string, error) {
	f.calls++
	return "cust-1", nil
}
func (f *fakeLookup) LookupVendorByEntraTenant(context.Context, string) (string, error) {
	f.calls++
	return "vendor-1", nil
}
func (f *fakeLookup) ResolveRole(context.Context, string, string, []string) (string, error) {
	return "admin", nil
}
func (f *fakeLookup) SystemRolePermissions(context.Context, string, string) ([]string, error) {
	return nil, nil
}

func mintMock() string {
	return mockPrefix + base64.RawURLEncoding.EncodeToString(
		[]byte(`{"sub":"u1","tid":"t1","groups":["g1"]}`))
}

func TestMockJWTResolver_RejectsWhenDisabled(t *testing.T) {
	lk := &fakeLookup{}
	r := NewMockJWTResolver(lk, false)
	if p, ok := r.Resolve(mintMock()); ok || p.UserID != "" {
		t.Fatalf("mock token accepted with allowMock=false: %+v", p)
	}
	if lk.calls != 0 {
		t.Fatalf("lookup consulted %d times with allowMock=false", lk.calls)
	}
	// Through the chain too.
	if _, ok := NewChainResolver(r).Resolve(mintMock()); ok {
		t.Fatal("chain accepted mock token with allowMock=false")
	}
}

func TestMockJWTResolver_AcceptsWhenEnabled(t *testing.T) {
	r := NewMockJWTResolver(&fakeLookup{}, true)
	if _, ok := r.Resolve(mintMock()); !ok {
		t.Fatal("mock token rejected with allowMock=true")
	}
}
