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
    URLs are kept;
  - in audit_log entries about notification channels, any web address recorded as the
    channel's target (in before, after or metadata) is cut the same way, ServiceNow
    included, since an update entry does not record the channel type. Email targets are kept.
`

// redactRow applies the export redaction rules to one decoded row in place.
func redactRow(table string, cols []string, row []any) {
	for i, v := range row {
		row[i] = redactValue(v)
	}
	switch table {
	case "notification_channels":
		redactChannelTarget(cols, row)
	case "audit_log":
		redactAuditChannelTarget(cols, row)
	}
}

// redactAuditChannelTarget masks channel URLs in notification_channel audit
// entries: create and update record the target in before/after (the update
// entry does not say the channel type), so any http(s) "target" there is
// masked; email targets are kept.
func redactAuditChannelTarget(cols []string, row []any) {
	kind := -1
	for i, c := range cols {
		if c == "target_kind" {
			kind = i
		}
	}
	if kind < 0 || row[kind] != "notification_channel" {
		return
	}
	for i, c := range cols {
		if c != "before" && c != "after" && c != "metadata" {
			continue
		}
		m, ok := row[i].(map[string]any)
		if !ok {
			continue
		}
		if s, ok := m["target"].(string); ok {
			l := strings.ToLower(strings.TrimSpace(s))
			if strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") {
				m["target"] = maskURL(s)
			}
		}
	}
}

// redactChannelTarget masks the target of Teams and webhook channels.
func redactChannelTarget(cols []string, row []any) {
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
