package notify

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ServiceNow opens and resolves incidents through the Table API
// (/api/now/table/incident) using Basic auth for an integration user with
// the itil role (or any role that can create and update incidents).
//
// Channel shape:
//
//	target  https://<instance>.service-now.com
//	config  username, password_enc (hex AES-GCM; set by the portal API from a
//	        plaintext "password" that is never stored), and optional incident
//	        defaults: assignment_group, caller, category, subcategory,
//	        close_code (default "Solution provided"), resolved_state
//	        (default "6", ServiceNow's Resolved).
//
// Field values are sent with sysparm_input_display_value=true so admins can
// enter an assignment group or caller by name rather than sys_id. For the
// same reason state is sent as the display value on resolve unless
// resolved_state is numeric.
type ServiceNow struct {
	httpc   *http.Client
	decrypt func([]byte) ([]byte, error)
}

// NewServiceNow builds the client. decrypt unwraps config.password_enc; nil
// makes every call fail with a clear error rather than sending no password.
func NewServiceNow(decrypt func([]byte) ([]byte, error)) *ServiceNow {
	return &ServiceNow{httpc: &http.Client{Timeout: 15 * time.Second}, decrypt: decrypt}
}

// SNIncident identifies an incident ServiceNow created.
type SNIncident struct {
	SysID  string
	Number string
}

// Severity → ServiceNow urgency / impact (1 high … 3 low). With the default
// priority matrix: critical → P2, warning → P3, info → P4.
var snSeverity = map[string][2]string{
	"critical": {"1", "2"},
	"warning":  {"2", "2"},
	"info":     {"3", "3"},
}

// Open creates an incident for the alert. correlation_id carries the alert
// ID so the incident can be traced back, and so a retried open can find an
// incident that was created but whose reply was lost.
func (s *ServiceNow) Open(ctx context.Context, ch Channel, evt AlertEvent) (SNIncident, error) {
	base, user, pass, err := s.creds(ch)
	if err != nil {
		return SNIncident{}, err
	}
	if evt.AlertID != "" {
		if inc, err := s.findByCorrelation(ctx, base, user, pass, evt.AlertID); err != nil || inc.SysID != "" {
			return inc, err
		}
	}
	uw := snSeverity[evt.Severity]
	if uw[0] == "" {
		uw = snSeverity["warning"]
	}
	body := map[string]any{
		"short_description":   truncate(fmt.Sprintf("[M.A.R.C.U.S.] %s: %s", evt.SubjectName(), alertTitle(evt)), 160),
		"description":         snDescription(evt),
		"urgency":             uw[0],
		"impact":              uw[1],
		"correlation_display": "M.A.R.C.U.S.",
	}
	if evt.AlertID != "" {
		body["correlation_id"] = evt.AlertID
	}
	for cfgKey, field := range map[string]string{
		"assignment_group": "assignment_group", "caller": "caller_id",
		"category": "category", "subcategory": "subcategory",
	} {
		if v := cfgString(ch.Config, cfgKey); v != "" {
			body[field] = v
		}
	}
	var out struct {
		Result struct {
			SysID  string `json:"sys_id"`
			Number string `json:"number"`
		} `json:"result"`
	}
	q := url.Values{"sysparm_input_display_value": {"true"}, "sysparm_fields": {"sys_id,number"}}
	if err := s.do(ctx, http.MethodPost, base+"/api/now/table/incident?"+q.Encode(), user, pass, body, &out); err != nil {
		return SNIncident{}, err
	}
	if out.Result.SysID == "" {
		return SNIncident{}, errors.New("servicenow: no sys_id in response")
	}
	return SNIncident{SysID: out.Result.SysID, Number: out.Result.Number}, nil
}

// Resolve moves the incident to Resolved with a close code and notes.
func (s *ServiceNow) Resolve(ctx context.Context, ch Channel, sysID, notes string) error {
	base, user, pass, err := s.creds(ch)
	if err != nil {
		return err
	}
	state := cfgString(ch.Config, "resolved_state")
	if state == "" {
		state = "6"
	}
	closeCode := cfgString(ch.Config, "close_code")
	if closeCode == "" {
		closeCode = "Solution provided"
	}
	body := map[string]any{
		"state":       state,
		"close_code":  closeCode,
		"close_notes": notes,
	}
	q := url.Values{"sysparm_fields": {"sys_id,number,state"}}
	if !isDigits(state) {
		q.Set("sysparm_input_display_value", "true")
	}
	return s.do(ctx, http.MethodPatch, base+"/api/now/table/incident/"+url.PathEscape(sysID)+"?"+q.Encode(),
		user, pass, body, nil)
}

func (s *ServiceNow) findByCorrelation(ctx context.Context, base, user, pass, alertID string) (SNIncident, error) {
	var out struct {
		Result []struct {
			SysID  string `json:"sys_id"`
			Number string `json:"number"`
		} `json:"result"`
	}
	q := url.Values{
		"sysparm_query":  {"correlation_id=" + alertID},
		"sysparm_fields": {"sys_id,number"},
		"sysparm_limit":  {"1"},
	}
	if err := s.do(ctx, http.MethodGet, base+"/api/now/table/incident?"+q.Encode(), user, pass, nil, &out); err != nil {
		return SNIncident{}, err
	}
	if len(out.Result) == 0 {
		return SNIncident{}, nil
	}
	return SNIncident{SysID: out.Result[0].SysID, Number: out.Result[0].Number}, nil
}

func (s *ServiceNow) creds(ch Channel) (base, user, pass string, err error) {
	base, err = NormaliseServiceNowURL(ch.Target)
	if err != nil {
		return "", "", "", err
	}
	user = cfgString(ch.Config, "username")
	enc := cfgString(ch.Config, "password_enc")
	if user == "" || enc == "" {
		return "", "", "", errors.New("servicenow: username and password are required")
	}
	if s.decrypt == nil {
		return "", "", "", errors.New("servicenow: no cipher configured to read the password")
	}
	raw, err := hex.DecodeString(enc)
	if err != nil {
		return "", "", "", errors.New("servicenow: stored password is unreadable; re-enter it")
	}
	plain, err := s.decrypt(raw)
	if err != nil {
		return "", "", "", errors.New("servicenow: stored password is unreadable; re-enter it")
	}
	return base, user, string(plain), nil
}

func (s *ServiceNow) do(ctx context.Context, method, u, user, pass string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return err
	}
	req.SetBasicAuth(user, pass)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("servicenow: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("servicenow returned %d%s", resp.StatusCode, snErrorDetail(data))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("servicenow: unexpected response: %w", err)
		}
	}
	return nil
}

// snErrorDetail pulls ServiceNow's {"error":{"message","detail"}} into the
// error text, so the portal's "last error" says why (bad credentials,
// missing ACL, unknown assignment group…).
func snErrorDetail(data []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Detail  string `json:"detail"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &e) != nil || e.Error.Message == "" {
		return ""
	}
	if e.Error.Detail != "" && e.Error.Detail != e.Error.Message {
		return ": " + e.Error.Message + " — " + e.Error.Detail
	}
	return ": " + e.Error.Message
}

// NormaliseServiceNowURL accepts "acme", "acme.service-now.com" or a full
// https URL, and returns "https://host" with no trailing path.
func NormaliseServiceNowURL(target string) (string, error) {
	t := strings.TrimSpace(target)
	if t == "" {
		return "", errors.New("servicenow: instance URL is required")
	}
	if !strings.Contains(t, "://") {
		if !strings.Contains(t, ".") {
			t += ".service-now.com"
		}
		t = "https://" + t
	}
	u, err := url.Parse(t)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("servicenow: %q is not a valid instance URL", target)
	}
	if u.Scheme != "https" {
		return "", errors.New("servicenow: instance URL must use https")
	}
	return "https://" + u.Host, nil
}

func alertTitle(evt AlertEvent) string {
	if evt.Message != "" {
		return evt.Message
	}
	return strings.ReplaceAll(evt.AlertKey, "_", " ")
}

func snDescription(evt AlertEvent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n", evt.SubjectLabel(), evt.SubjectName())
	fmt.Fprintf(&b, "Alert: %s\n", evt.AlertKey)
	fmt.Fprintf(&b, "Severity: %s\n", evt.Severity)
	fmt.Fprintf(&b, "Opened: %s\n", evt.OpenedAt.UTC().Format(time.RFC3339))
	if evt.Message != "" {
		fmt.Fprintf(&b, "\n%s\n", evt.Message)
	}
	if evt.AlertID != "" {
		fmt.Fprintf(&b, "\nM.A.R.C.U.S. alert ID: %s\nThis incident is resolved automatically when the alert clears.\n", evt.AlertID)
	}
	return b.String()
}

func cfgString(cfg map[string]any, key string) string {
	if cfg == nil {
		return ""
	}
	s, _ := cfg[key].(string)
	return strings.TrimSpace(s)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
