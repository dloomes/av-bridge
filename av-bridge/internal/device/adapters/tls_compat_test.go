package adapters

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A device whose TLS only offers RSA key exchange (older codec firmware)
// refuses Go's default client with "handshake failure", but accepts the
// device config.
func TestDeviceTLSConfigReachesRSAKeyExchangeDevice(t *testing.T) {
	dev := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	dev.TLS = &tls.Config{
		MaxVersion:   tls.VersionTLS12,
		CipherSuites: []uint16{tls.TLS_RSA_WITH_AES_128_CBC_SHA},
	}
	dev.StartTLS()
	defer dev.Close()

	get := func(cfg *tls.Config) error {
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
		resp, err := c.Get(dev.URL)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if err := get(&tls.Config{InsecureSkipVerify: true}); err == nil {
		t.Skip("this Go version still offers RSA key exchange by default; nothing to prove")
	}
	if err := get(deviceTLSConfig(true)); err != nil {
		t.Fatalf("device config: %v", err)
	}
}

// A device limited to classic key exchange groups still connects.
func TestDeviceTLSConfigClassicGroups(t *testing.T) {
	dev := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	dev.TLS = &tls.Config{CurvePreferences: []tls.CurveID{tls.CurveP256}}
	dev.StartTLS()
	defer dev.Close()
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: deviceTLSConfig(true)}}
	resp, err := c.Get(dev.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}
