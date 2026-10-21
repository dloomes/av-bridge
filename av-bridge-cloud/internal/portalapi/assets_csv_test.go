package portalapi

import (
	"strings"
	"testing"
)

const bom = string(rune(0xFEFF))

func TestParseAssetCSV(t *testing.T) {
	// Excel's "CSV UTF-8" output: byte-order mark, CRLF, a trailing blank row.
	in := bom + "asset_tag,name,category,building,room,purchase_date\r\n" +
		"AV-001, Boardroom display ,display,HQ,Boardroom,2025-03-01\r\n" +
		",Wall mount,mount,,,\r\n" +
		",,,,,\r\n"
	rows, err := parseAssetCSV(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want 2", rows)
	}
	if r := rows[0]; r.Row != 2 || r.AssetTag != "AV-001" || r.Name != "Boardroom display" ||
		r.Building != "HQ" || r.Room != "Boardroom" || r.PurchaseDate != "2025-03-01" {
		t.Errorf("row 0 = %+v", r)
	}
	if r := rows[1]; r.Row != 3 || r.AssetTag != "" || r.Category != "mount" {
		t.Errorf("row 1 = %+v", r)
	}

	if _, err := parseAssetCSV(strings.NewReader(bom + "name,manufacturer\nX,Y\n")); err == nil ||
		!strings.Contains(err.Error(), "category") {
		t.Errorf("missing category: err = %v", err)
	}
	if _, err := parseAssetCSV(strings.NewReader(bom + "name,category\nX,display\n")); err != nil {
		t.Errorf("BOM before a required column: %v", err)
	}
}
