// Package collectorupdate decides when a collector is offered the
// collector release bundled with this cloud build, hands the offer to the
// bridge on /bridge/poll, and tracks each attempt to a result.
//
// The cloud never signs anything: the release is signed at image build
// time (av-bridge/cmd/av-bridge-sign, key passed as a BuildKit secret) and
// collectors verify the signature against keys compiled into them. This
// package only relays the manifest's hashes and signatures.
package collectorupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	// Window times are in each building's zone; embed the zone database
	// so this never depends on the base image shipping one.
	_ "time/tzdata"
)

// File is one signed artefact in the bundled release.
type File struct {
	SHA256    string `json:"sha256"`
	Signature string `json:"signature,omitempty"`
	Size      int64  `json:"size"`
}

// Release is the collector release bundled with this cloud image
// (/downloads/manifest.json).
type Release struct {
	Version string          `json:"version"`
	Files   map[string]File `json:"files"`
}

// LoadRelease reads the manifest. A missing file returns (nil, nil): the
// cloud runs fine without a bundled release, it just offers no updates.
func LoadRelease(path string) (*Release, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Release
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &r, nil
}

// Signed reports whether every artefact carries a signature.
func (r *Release) Signed() bool {
	if r == nil || len(r.Files) == 0 {
		return false
	}
	for _, f := range r.Files {
		if f.Signature == "" || f.SHA256 == "" {
			return false
		}
	}
	return true
}

// ArtefactFor maps a bridge platform ("os/arch") to its download name.
func ArtefactFor(platform string) (string, bool) {
	switch platform {
	case "linux/amd64":
		return "av-bridge-linux-amd64", true
	case "linux/arm64":
		return "av-bridge-linux-arm64", true
	case "windows/amd64":
		return "av-bridge-windows-amd64.exe", true
	}
	return "", false
}

// parseVersion reads "v1.2.3" or "1.2.3", ignoring any "-pre" or "+meta"
// suffix and anything git describe appends ("v1.2.3-4-gabc").
func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Newer reports whether candidate is a newer version than current. A
// current version that doesn't parse ("dev", "") counts as older, so
// hand-built collectors can be brought onto a release; a candidate that
// doesn't parse is never offered.
func Newer(candidate, current string) bool {
	c, ok := parseVersion(candidate)
	if !ok {
		return false
	}
	cur, ok := parseVersion(current)
	if !ok {
		return true
	}
	for i := 0; i < 3; i++ {
		if c[i] != cur[i] {
			return c[i] > cur[i]
		}
	}
	return false
}

// Collector is the update-relevant view of a collector row.
type Collector struct {
	Version  string
	Platform string
	// Capable is nil until a bridge new enough to report it has polled.
	Capable *bool
	Blocker string
}

// Eligibility says whether c can be offered r, and if not, why — worded
// for the portal.
func (r *Release) Eligibility(c Collector) (bool, string) {
	switch {
	case r == nil:
		return false, "no collector release is bundled with this cloud"
	case !r.Signed():
		return false, "this cloud's collector release isn't signed"
	case c.Capable == nil || c.Platform == "":
		return false, "this collector's version can't update itself; update it manually once"
	case !*c.Capable:
		if c.Blocker != "" {
			return false, c.Blocker
		}
		return false, "this collector can't update itself"
	}
	art, ok := ArtefactFor(c.Platform)
	if !ok {
		return false, "no collector build for " + c.Platform
	}
	if _, ok := r.Files[art]; !ok {
		return false, "no collector build for " + c.Platform
	}
	if !Newer(r.Version, c.Version) {
		return false, "up to date"
	}
	return true, ""
}

// WindowLength is how long the automatic update window stays open.
const WindowLength = 2 * time.Hour

// InWindow reports whether now falls in the window opening at startMin
// minutes after local midnight in tz. Handles windows that cross
// midnight; an unknown time zone falls back to Europe/London.
func InWindow(now time.Time, tz string, startMin int) bool {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc, err = time.LoadLocation("Europe/London")
		if err != nil {
			loc = time.UTC
		}
	}
	local := now.In(loc)
	for _, dayOffset := range []int{0, -1} {
		d := local.AddDate(0, 0, dayOffset)
		open := time.Date(d.Year(), d.Month(), d.Day(), startMin/60, startMin%60, 0, 0, loc)
		if !local.Before(open) && local.Before(open.Add(WindowLength)) {
			return true
		}
	}
	return false
}

// FormatWindow renders minutes-after-midnight as "HH:MM".
func FormatWindow(min int) string {
	return fmt.Sprintf("%02d:%02d", min/60, min%60)
}

// ParseWindow parses "HH:MM" into minutes after midnight.
func ParseWindow(s string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, errors.New("update window must be a time like 02:00")
	}
	return t.Hour()*60 + t.Minute(), nil
}
