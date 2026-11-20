package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dloomes/av-bridge/internal/config"
)

func cloudCfg(extra string) string {
	return "cloud:\n  webhook_url: \"https://example.com/ingest\"\n" + extra + "devices: []\n"
}

func TestLoad_TLSSkipVerify(t *testing.T) {
	_, err := config.Load(writeConfig(t, cloudCfg("  tls_skip_verify: true\n")))
	want := "cloud.tls_skip_verify is no longer supported: use cloud.ca_file to trust a TLS-inspecting proxy's certificate"
	if err == nil || err.Error() != want {
		t.Fatalf("true: got %v", err)
	}
	if _, err := config.Load(writeConfig(t, cloudCfg("  tls_skip_verify: false\n"))); err != nil {
		t.Fatalf("false: %v", err)
	}
}

func TestLoad_CAFile(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk.pem")
	_ = os.WriteFile(junk, []byte("nope"), 0o600)
	empty := filepath.Join(dir, "empty.pem")
	_ = os.WriteFile(empty, nil, 0o600)
	missing := filepath.Join(dir, "missing.pem")
	for _, p := range []string{missing, empty, junk} {
		_, err := config.Load(writeConfig(t, cloudCfg("  ca_file: '"+p+"'\n")))
		if err == nil || !strings.Contains(err.Error(), p) {
			t.Errorf("%s: want error naming the path, got %v", p, err)
		}
	}
}
