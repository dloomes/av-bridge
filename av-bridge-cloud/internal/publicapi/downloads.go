package publicapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Downloadable binaries live under /downloads inside the container
// (populated at image build time — see av-bridge-cloud/Dockerfile).
// The map is a strict allowlist keyed by the URL path suffix so a
// caller can't smuggle in a "..\\etc\\passwd" or similar; anything
// not present here returns 404.
//
// Each entry names both the on-disk file to serve and the
// Content-Disposition filename the browser should save it as. Right
// now that's the same string, but keeping them separate makes it
// easy to add "latest" aliases without duplicating the file.
type downloadEntry struct {
	// diskPath is the location inside the container the file is
	// copied to by the Dockerfile.
	diskPath string
	// filename is the name the browser downloads it as. Kept next
	// to diskPath so it survives any future path reshuffle.
	filename string
	// contentType — Windows exes go as application/octet-stream so
	// browsers don't try to execute them inline on click.
	contentType string
}

var downloadCatalogue = map[string]downloadEntry{
	"av-bridge-windows-amd64.exe": {
		diskPath:    "/downloads/av-bridge-windows-amd64.exe",
		filename:    "av-bridge-windows-amd64.exe",
		contentType: "application/octet-stream",
	},
	"av-bridge-linux-amd64": {
		diskPath:    "/downloads/av-bridge-linux-amd64",
		filename:    "av-bridge-linux-amd64",
		contentType: "application/octet-stream",
	},
	"av-bridge-linux-arm64": {
		diskPath:    "/downloads/av-bridge-linux-arm64",
		filename:    "av-bridge-linux-arm64",
		contentType: "application/octet-stream",
	},
}

// ListDownloads — GET /public/downloads
//
// Returns metadata for every allowlisted binary the cloud can serve.
// The portal /downloads page consumes this so the list of available
// binaries lives in the cloud rather than being hard-coded on the
// portal side — adding a new artefact only needs a Dockerfile COPY
// and a downloadCatalogue entry.
func (h *Handler) ListDownloads(w http.ResponseWriter, r *http.Request) {
	type item struct {
		Key         string `json:"key"`
		Filename    string `json:"filename"`
		SizeBytes   int64  `json:"size_bytes"`
		ContentType string `json:"content_type"`
		Available   bool   `json:"available"`
	}
	out := make([]item, 0, len(downloadCatalogue))
	for key, entry := range downloadCatalogue {
		it := item{
			Key:         key,
			Filename:    entry.filename,
			ContentType: entry.contentType,
		}
		if info, err := os.Stat(entry.diskPath); err == nil && !info.IsDir() {
			it.SizeBytes = info.Size()
			it.Available = true
		}
		out = append(out, it)
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	writeJSONStatus(w, http.StatusOK, out)
}

// ServeDownload — GET /public/downloads/{key}
//
// Streams a specific binary. Auth-free by design: the enrolment
// install scripts fetch these before the collector has a session,
// and the exe itself is public (customers can inspect it, and
// signing — once wired — protects integrity). The strict allowlist
// is what keeps this from being a filesystem-traversal foothold.
func (h *Handler) ServeDownload(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	// Belt-and-braces path scrub even before the allowlist lookup —
	// makes the intent obvious to a reader and gives a cleaner 404
	// response for accidentally-encoded ".." attempts.
	if key == "" || strings.ContainsAny(key, "/\\") || strings.Contains(key, "..") {
		http.NotFound(w, r)
		return
	}
	entry, ok := downloadCatalogue[key]
	if !ok {
		http.NotFound(w, r)
		return
	}

	info, err := os.Stat(entry.diskPath)
	if err != nil || info.IsDir() {
		http.Error(w, "artefact not available", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", entry.contentType)
	w.Header().Set(
		"Content-Disposition",
		`attachment; filename="`+filepath.Base(entry.filename)+`"`,
	)
	// Short cache — release rotation should invalidate quickly, and
	// browsers still get an efficient conditional GET via ETag which
	// http.ServeFile handles for us.
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeFile(w, r, entry.diskPath)
}
