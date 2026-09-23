// Package hostinfo captures a compact human-readable description of
// the host the bridge is running on. Emitted upstream to the cloud on
// every heartbeat so operators can answer "what OS is that collector
// on?" from the portal without SSH — helpful for support tickets and
// upgrade tracking.
//
// Format examples:
//
//	"Ubuntu 22.04.4 LTS (linux/amd64)"  — Linux with /etc/os-release
//	"linux/amd64"                       — Linux fallback (no os-release)
//	"windows/amd64"                     — Windows collectors
//	"darwin/arm64"                      — dev / demo laptops
//
// Detection is best-effort — if /etc/os-release can't be read, we fall
// back to the bare GOOS/GOARCH string. Kept to 128 chars max to match
// the DB column CHECK constraint (0046_collectors_bridge_os.sql).
package hostinfo

import (
	"bufio"
	"os"
	"runtime"
	"strings"
	"sync"
)

const maxOSLen = 128

var (
	once   sync.Once
	cached string
)

// OS returns a human-readable OS + arch descriptor for this host.
// Cached on first call — the answer doesn't change during the process's
// lifetime, and reading /etc/os-release once at startup is enough.
func OS() string {
	once.Do(func() {
		cached = detect()
	})
	return cached
}

func detect() string {
	arch := runtime.GOOS + "/" + runtime.GOARCH
	if runtime.GOOS != "linux" {
		return arch
	}
	pretty := readLinuxPrettyName()
	if pretty == "" {
		return arch
	}
	combined := pretty + " (" + arch + ")"
	if len(combined) > maxOSLen {
		// Prefer keeping the arch suffix — it's the machine-parseable bit.
		trim := maxOSLen - len(arch) - 4 // " (…)"
		if trim < 8 {
			return arch
		}
		pretty = pretty[:trim] + "…"
		combined = pretty + " (" + arch + ")"
	}
	return combined
}

// readLinuxPrettyName parses /etc/os-release and returns PRETTY_NAME's
// value with surrounding quotes stripped. Returns "" on any error —
// caller falls back to the bare GOOS/GOARCH.
func readLinuxPrettyName() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "PRETTY_NAME=") {
			continue
		}
		val := strings.TrimPrefix(line, "PRETTY_NAME=")
		val = strings.Trim(val, `"'`)
		return val
	}
	return ""
}
