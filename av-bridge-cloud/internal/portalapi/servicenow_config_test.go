package portalapi

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/dloomes/av-bridge-cloud/internal/secrets"
)

func TestPrepareServiceNowConfig(t *testing.T) {
	c, err := secrets.NewAESGCMFromHexKey(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cipher: c}

	target, cfg, err := h.prepareServiceNowConfig("acme", map[string]any{
		"username": " marcus ", "password": "s3cret", "assignment_group": "AV Support",
		"unexpected": "dropped",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if target != "https://acme.service-now.com" || cfg["username"] != "marcus" || cfg["assignment_group"] != "AV Support" {
		t.Errorf("target %q cfg %v", target, cfg)
	}
	if _, ok := cfg["password"]; ok {
		t.Error("plaintext password must not be stored")
	}
	if _, ok := cfg["unexpected"]; ok {
		t.Error("unknown keys must be dropped")
	}
	raw, _ := hex.DecodeString(cfg["password_enc"].(string))
	if plain, err := c.Decrypt(raw); err != nil || string(plain) != "s3cret" {
		t.Errorf("password_enc decrypts to %q, %v", plain, err)
	}

	// Editing without a password keeps the stored one.
	_, kept, err := h.prepareServiceNowConfig("acme", map[string]any{"username": "marcus"}, cfg)
	if err != nil || kept["password_enc"] != cfg["password_enc"] {
		t.Errorf("edit without password: %v, %v", kept, err)
	}

	for name, in := range map[string]map[string]any{
		"no username": {"password": "x"},
		"no password": {"username": "u"},
	} {
		if _, _, err := h.prepareServiceNowConfig("acme", in, nil); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, _, err := h.prepareServiceNowConfig("http://acme.service-now.com", map[string]any{"username": "u", "password": "p"}, nil); err == nil {
		t.Error("http instance URL should be rejected")
	}

	safe := safeChannelConfig("servicenow", cfg)
	if _, ok := safe["password_enc"]; ok || safe["has_password"] != true || safe["username"] != "marcus" {
		t.Errorf("safe config %v", safe)
	}
	if safeChannelConfig("webhook", map[string]any{"headers": map[string]any{"Authorization": "Bearer x"}}) != nil {
		t.Error("webhook config (headers) must not be returned")
	}
}
