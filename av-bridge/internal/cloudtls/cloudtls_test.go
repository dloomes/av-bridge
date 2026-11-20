package cloudtls_test

import (
	"crypto/tls"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/dloomes/av-bridge/internal/cloudtls"
)

func serverCA(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	t.Cleanup(srv.Close)
	p := filepath.Join(t.TempDir(), "ca.pem")
	b := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return srv, p
}

func get(cfg *tls.Config, url string) error {
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
	resp, err := c.Get(url)
	if err == nil {
		_ = resp.Body.Close()
	}
	return err
}

func TestRejectsSelfSignedWithoutCAFile(t *testing.T) {
	srv, _ := serverCA(t)
	cfg, err := cloudtls.Config("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.InsecureSkipVerify || cfg.MinVersion < tls.VersionTLS12 {
		t.Fatal("config must verify and require TLS 1.2+")
	}
	if get(cfg, srv.URL) == nil {
		t.Fatal("self-signed cert accepted without ca_file")
	}
}

func TestAcceptsServerWithCAFile(t *testing.T) {
	srv, ca := serverCA(t)
	cfg, err := cloudtls.Config(ca)
	if err != nil {
		t.Fatal(err)
	}
	if err := get(cfg, srv.URL); err != nil {
		t.Fatalf("cert in ca_file rejected: %v", err)
	}
}

func TestBadCAFile(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.pem")
	junk := filepath.Join(dir, "junk.pem")
	_ = os.WriteFile(empty, nil, 0o600)
	_ = os.WriteFile(junk, []byte("not a cert"), 0o600)
	for _, p := range []string{filepath.Join(dir, "missing.pem"), empty, junk} {
		if _, err := cloudtls.Config(p); err == nil {
			t.Errorf("%s: want error", p)
		}
	}
}

func TestMustConfigFailsClosed(t *testing.T) {
	srv, _ := serverCA(t)
	cfg := cloudtls.MustConfig(filepath.Join(t.TempDir(), "missing.pem"))
	if cfg.InsecureSkipVerify || get(cfg, srv.URL) == nil {
		t.Fatal("must fail closed")
	}
}
