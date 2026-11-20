package portalapi

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The CSV imports accept files up to 4 MB. A larger upload is refused
// with 413 before it is parsed or reaches the database (so these run
// without one), and the body is cut off rather than spooled to disk.
func TestCSVImportsRefuseOversizeUploads(t *testing.T) {
	h := &Handler{}
	handlers := map[string]http.HandlerFunc{
		"room tags": h.ImportRoomTags,
		"assets":    h.ImportAssets,
	}
	for name, handle := range handlers {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "big.csv")
		_, _ = fw.Write(bytes.Repeat([]byte("a,b,c\n"), (5<<20)/6))
		_ = mw.Close()
		req := httptest.NewRequest("POST", "/import", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		handle(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: 5 MB upload got %d %s, want 413", name, rec.Code, rec.Body.String())
		}
	}
}
