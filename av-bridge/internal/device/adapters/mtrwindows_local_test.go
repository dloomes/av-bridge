//go:build windows

package adapters

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestMTRScriptRunsLocally runs the embedded script on this Windows machine
// through cmd.exe, exactly as WinRS would, and parses its output. Opt in with
// MTR_LOCAL_SCRIPT=1: it checks the bootstrap and the script's PowerShell
// syntax, not MTR behaviour (a dev PC has no Skype Room System log).
// The script is read-only, so it is safe to run anywhere.
func TestMTRScriptRunsLocally(t *testing.T) {
	if os.Getenv("MTR_LOCAL_SCRIPT") != "1" {
		t.Skip("set MTR_LOCAL_SCRIPT=1 to run the script on this PC")
	}
	line, err := mtrCommandLine(mtrScript)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Getenv("ComSpec"))
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /c ` + line}
	var stderr []byte
	out, err := cmd.Output()
	if ee, ok := err.(*exec.ExitError); ok {
		stderr = ee.Stderr
	}
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr)
	}
	t.Logf("output: %s", out)
	m, sections, err := mtrReadings(out, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("readings: %v", m)
	t.Logf("section errors: %v", sections)
	if _, ok := m["software_version"]; !ok {
		t.Error("no software_version: the PC section failed")
	}
}
