// Package cloudtls builds the TLS configuration every Collector-to-cloud
// connection uses. Certificate verification is never disabled: a site behind
// a TLS-inspecting proxy trusts the proxy's CA by naming it in cloud.ca_file.
package cloudtls

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// Config returns system roots plus the PEM certificates in caFile (when set).
func Config(caFile string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile == "" {
		return cfg, nil
	}
	pem, err := os.ReadFile(caFile) // #nosec G304 -- operator-supplied cloud.ca_file
	if err != nil {
		return nil, fmt.Errorf("cloud.ca_file %s: %w", caFile, err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("cloud.ca_file %s: no PEM certificate found", caFile)
	}
	cfg.RootCAs = pool
	return cfg, nil
}

// MustConfig is Config for constructors that cannot return an error. The
// file is validated at startup (config.Load), so a failure here means it
// changed since; the result then trusts nothing and every connection fails
// closed rather than falling back to system roots or skipping verification.
func MustConfig(caFile string) *tls.Config {
	cfg, err := Config(caFile)
	if err != nil {
		return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: x509.NewCertPool()}
	}
	return cfg
}
