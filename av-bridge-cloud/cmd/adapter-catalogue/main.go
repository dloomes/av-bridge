// Command adapter-catalogue prints the adapter catalogue as JSON.
//
// It exists so documentation (docs/product/mintlify/build.py) is generated
// from the same source of truth as the portal's Adapters page, instead of
// a hand-maintained list that drifts as adapters are added. Test-only
// adapters are omitted.
//
//	go run ./cmd/adapter-catalogue > adapters.json
package main

import (
	"encoding/json"
	"os"

	"github.com/dloomes/av-bridge-cloud/internal/adapters"
)

// testOnly lists catalogue entries that exist for development and must not
// appear in customer documentation.
var testOnly = map[string]bool{"fake": true}

func main() {
	var out []adapters.Info
	for _, a := range adapters.Catalogue() {
		if !testOnly[a.ID] {
			out = append(out, a)
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		os.Stderr.WriteString("encode catalogue: " + err.Error() + "\n")
		os.Exit(1)
	}
}
