package adapters

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"testing"
)

// TestFactoryAndCatalogueAgree pins the two Go modules together: every
// protocol string the bridge's factory switches on must appear in the
// cloud catalogue, and vice versa. Without this, adding an adapter to
// one and forgetting the other silently produces a portal that either
// can't offer the protocol (missing catalogue entry) or offers a
// protocol the bridge will reject at reconciliation (missing factory
// case). Runs at cloud test time so `go test ./...` catches it before
// a PR merges.
//
// The bridge is a separate Go module — the cloud test can't import it,
// so we read its factory.go as text and grep for `case "..."` inside
// the switch. Fragile if the factory ever stops being a plain switch,
// but that's a hop worth making noise about anyway.
func TestFactoryAndCatalogueAgree(t *testing.T) {
	factoryPath := findBridgeFactory(t)
	if factoryPath == "" {
		t.Skip("bridge factory.go not found — running against a checkout without the sibling bridge repo")
	}

	data, err := os.ReadFile(factoryPath)
	if err != nil {
		t.Fatalf("read bridge factory %s: %v", factoryPath, err)
	}

	// `case "protocol":` — the switch inside adapters.New. Kept intentionally
	// strict: only lowercase identifiers with underscores, exactly the shape
	// every real adapter has. `default:` and any other case are ignored by
	// the pattern.
	pattern := regexp.MustCompile(`(?m)^\s*case\s+"([a-z][a-z0-9_]*)":`)
	matches := pattern.FindAllStringSubmatch(string(data), -1)
	if len(matches) == 0 {
		t.Fatalf("no `case \"...\":` statements found in %s — pattern may need updating", factoryPath)
	}

	factoryProtocols := map[string]bool{}
	for _, m := range matches {
		factoryProtocols[m[1]] = true
	}

	catalogueProtocols := map[string]bool{}
	for _, a := range Catalogue() {
		catalogueProtocols[a.ID] = true
	}

	// Direction 1: everything the bridge understands must be documented
	// in the cloud so the portal can list it and the API accepts it.
	var missingFromCatalogue []string
	for p := range factoryProtocols {
		if !catalogueProtocols[p] {
			missingFromCatalogue = append(missingFromCatalogue, p)
		}
	}
	sort.Strings(missingFromCatalogue)
	if len(missingFromCatalogue) > 0 {
		t.Errorf("bridge factory registers protocols not in cloud catalogue: %v\n"+
			"→ add entries to av-bridge-cloud/internal/adapters/catalogue.go",
			missingFromCatalogue)
	}

	// Direction 2: everything the cloud advertises must actually be wired
	// up on the bridge — otherwise the portal picker offers a dead option
	// and the bridge fails with "unsupported protocol" at reconcile time.
	var missingFromFactory []string
	for p := range catalogueProtocols {
		if !factoryProtocols[p] {
			missingFromFactory = append(missingFromFactory, p)
		}
	}
	sort.Strings(missingFromFactory)
	if len(missingFromFactory) > 0 {
		t.Errorf("cloud catalogue advertises protocols the bridge factory doesn't handle: %v\n"+
			"→ add a case to av-bridge/internal/device/adapters/factory.go New()",
			missingFromFactory)
	}
}

// findBridgeFactory locates the bridge's factory.go relative to this
// test file. The two modules are checked out side-by-side under the
// same parent — walking up from here reaches the repo root, then down
// into av-bridge. Returns "" when the file can't be found so CI runs
// that only check out the cloud module skip the test rather than fail.
func findBridgeFactory(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	// this file: <repo>/av-bridge-cloud/internal/adapters/contract_test.go
	// target:    <repo>/av-bridge/internal/device/adapters/factory.go
	candidate := filepath.Join(
		filepath.Dir(thisFile),
		"..", "..", "..",
		"av-bridge", "internal", "device", "adapters", "factory.go",
	)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}
