package portalauth

import "testing"

// TestPermNightlyDefer_InKnownCatalogue guards against removing the
// permission constant from KnownPermissions — the role-CRUD handler
// uses KnownPermissions as its closed-set allowlist and would reject a
// custom role that referenced nightly.defer if this drifted.
func TestPermNightlyDefer_InKnownCatalogue(t *testing.T) {
	if PermNightlyDefer != "nightly.defer" {
		t.Fatalf("PermNightlyDefer = %q, want %q — stable value; changing it breaks migrations", PermNightlyDefer, "nightly.defer")
	}
	if _, ok := KnownPermissions[PermNightlyDefer]; !ok {
		t.Fatalf("PermNightlyDefer missing from KnownPermissions — role.crud will reject any role referencing it")
	}
}

// TestVendorRolePermissions_OperatorHasDefer asserts the vendor
// "operator" role bundle grants nightly.defer. Operators need this to
// action a "room in use tonight" override (FR39) without holding
// nightly.manage — losing this grant regresses the requirement.
func TestVendorRolePermissions_OperatorHasDefer(t *testing.T) {
	perms, ok := VendorRolePermissions["operator"]
	if !ok {
		t.Fatal("VendorRolePermissions has no operator entry")
	}
	if _, has := perms[PermNightlyDefer]; !has {
		t.Errorf("operator vendor role missing PermNightlyDefer — FR39 regression")
	}
}

// TestVendorRolePermissions_ViewerHasNoDefer asserts the "viewer"
// role remains read-only. Defer is a write action even though it is
// scoped to today only — granting it to viewers would break the
// read-only guarantee that lets read-only auditors safely browse.
func TestVendorRolePermissions_ViewerHasNoDefer(t *testing.T) {
	perms, ok := VendorRolePermissions["viewer"]
	if !ok {
		t.Fatal("VendorRolePermissions has no viewer entry")
	}
	if _, has := perms[PermNightlyDefer]; has {
		t.Errorf("viewer vendor role granted PermNightlyDefer — viewer must stay read-only")
	}
}

// TestVendorRoleAdmin_BypassesEverything is a defensive assertion of
// the admin sentinel — a nil map value signals "bypass all checks"
// per the HasPermission contract. This is not defer-specific but is a
// cheap guard against a well-meaning refactor accidentally replacing
// the nil with an empty map (which would deny everything).
func TestVendorRoleAdmin_BypassesEverything(t *testing.T) {
	perms, ok := VendorRolePermissions["admin"]
	if !ok {
		t.Fatal("VendorRolePermissions has no admin entry")
	}
	if perms != nil {
		t.Errorf("admin vendor role permission map = %v; expected nil sentinel meaning 'bypass'", perms)
	}
}
