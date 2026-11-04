package privacy

import (
	"net/url"
	"regexp"
	"strings"
)

// Redacted replaces a secret value in an export.
const Redacted = "[redacted]"

// secretKey matches the keys, inside any JSON value, whose values are
// credentials: the ServiceNow password_enc, webhook headers (which carry
// Authorization: Bearer ...), and anything named like a password, secret,
// token or API key. Matched anywhere in the key, case-insensitively.
var secretKey = regexp.MustCompile(`(?i)password|secret|token|api_key|apikey|authorization|headers|_enc$`)

// urlSecretChannels are notification channel types whose target URL is
// itself the credential (anyone holding it can post).
var urlSecretChannels = map[string]bool{"teams": true, "webhook": true}

// RedactionNote says what an export redacts; READMEs include it.
const RedactionNote = `Secrets are redacted (replaced with "[redacted]"):
  - columns ending in _hash or _enc, and binary columns, are left out entirely;
  - inside any JSON value (for example notification_channels.config), the value of every key
    whose name contains password, secret, token, api_key, apikey, authorization or headers,
    or ends in _enc, including the whole of webhook headers;
  - notification_channels.target for Teams and webhook channels, whose URL is itself a
    credential, is cut to scheme://host/[redacted]. Email addresses and ServiceNow instance
    URLs are kept.
`

// redactRow applies the export redaction rules to one decoded row in place.
func redactRow(table string, cols []string, row []any) {
	for i, v := range row {
		row[i] = redactValue(v)
	}
	if table != "notification_channels" {
		return
	}
	typ, target := -1, -1
	for i, c := range cols {
		switch c {
		case "type":
			typ = i
		case "target":
			target = i
		}
	}
	if typ < 0 || target < 0 {
		return
	}
	if t, _ := row[typ].(string); urlSecretChannels[strings.ToLower(t)] {
		if s, ok := row[target].(string); ok {
			row[target] = maskURL(s)
		}
	}
}

// redactValue walks a decoded JSON value, replacing the value of every
// secretKey key. Plain scalars are returned as they are.
func redactValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, inner := range x {
			if secretKey.MatchString(k) {
				x[k] = Redacted
			} else {
				x[k] = redactValue(inner)
			}
		}
		return x
	case []any:
		for i, inner := range x {
			x[i] = redactValue(inner)
		}
		return x
	default:
		return v
	}
}

// maskURL keeps a URL's scheme and host only.
func maskURL(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return Redacted
	}
	return u.Scheme + "://" + u.Host + "/" + Redacted
}
