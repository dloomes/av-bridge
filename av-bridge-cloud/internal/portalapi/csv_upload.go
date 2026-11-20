package portalapi

import (
	"errors"
	"mime/multipart"
	"net/http"
)

// maxCSVImportBytes is the largest CSV the imports accept (the onboarding
// docs promise 4 MB). The request body may be a little larger for the
// multipart framing around the file.
const (
	maxCSVImportBytes = 4 << 20
	maxCSVImportBody  = maxCSVImportBytes + 64<<10
)

// csvUpload reads the multipart "file" field of a CSV import, refusing a
// body over the limit with 413 before it is buffered or spooled to disk.
// On false the error response has been written.
func csvUpload(w http.ResponseWriter, r *http.Request) (multipart.File, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxCSVImportBody)
	if err := r.ParseMultipartForm(maxCSVImportBytes); err != nil { // #nosec G120 -- body bounded by http.MaxBytesReader on the line above (gosec does not track r.Body)
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, "file exceeds 4MB")
			return nil, false
		}
		writeErr(w, http.StatusBadRequest, "could not parse multipart body")
		return nil, false
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "file field is required")
		return nil, false
	}
	if header.Size > maxCSVImportBytes {
		file.Close()
		writeErr(w, http.StatusRequestEntityTooLarge, "file exceeds 4MB")
		return nil, false
	}
	return file, true
}
