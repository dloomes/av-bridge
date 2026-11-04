package privacy

import "testing"

func TestRedactRow(t *testing.T) {
	cols := []string{"type", "target", "config"}
	cases := []struct {
		typ, target, want string
	}{
		{"webhook", "https://hooks.example.com/services/T0/B0/xyz?sig=1", "https://hooks.example.com/[redacted]"},
		{"teams", "https://user:pw@acme.webhook.office.com/webhookb2/abc", "https://acme.webhook.office.com/[redacted]"},
		{"Webhook", "not a url", Redacted},
		{"email", "ops@example.com", "ops@example.com"},
		{"servicenow", "https://acme.service-now.com", "https://acme.service-now.com"},
	}
	for _, c := range cases {
		row := []any{c.typ, c.target, map[string]any{
			"headers":       map[string]any{"Authorization": "Bearer x"},
			"PASSWORD_ENC":  "x",
			"client_secret": "x",
			"nested":        []any{map[string]any{"apikey": "x", "ok": "y"}},
			"username":      "kept",
		}}
		redactRow("notification_channels", cols, row)
		if row[1] != c.want {
			t.Errorf("%s target %q -> %v, want %q", c.typ, c.target, row[1], c.want)
		}
		cfg := row[2].(map[string]any)
		for _, k := range []string{"headers", "PASSWORD_ENC", "client_secret"} {
			if cfg[k] != Redacted {
				t.Errorf("config[%s] = %v, want redacted", k, cfg[k])
			}
		}
		inner := cfg["nested"].([]any)[0].(map[string]any)
		if inner["apikey"] != Redacted || inner["ok"] != "y" || cfg["username"] != "kept" {
			t.Errorf("nested redaction wrong: %v", cfg)
		}
	}
	// Other tables keep their target-like text columns.
	row := []any{"webhook", "https://keep.example.com/x", nil}
	redactRow("something_else", cols, row)
	if row[1] != "https://keep.example.com/x" {
		t.Errorf("non-channel table changed: %v", row[1])
	}
}
