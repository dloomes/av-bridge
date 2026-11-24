// Package neatpulse is the cloud-side client for the Neat Pulse REST API
// (https://api.pulse.neat.no/docs/). It is read-only: it lists an
// organisation's endpoints (Neat devices), reads one endpoint's details and
// reads the organisation's latest sensor data.
//
// Authentication is a per-organisation API key sent as a bearer token. The
// key is never included in an error string.
package neatpulse

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.pulse.neat.no"

var (
	// ErrUnauthorized means Pulse refused the API key (401), or the key may
	// not read this organisation (403).
	ErrUnauthorized = errors.New("neatpulse: the API key was rejected or cannot read this organisation")
	// ErrRateLimited means Pulse answered 429. Use errors.As with
	// *RateLimitError for the wait Pulse asked for.
	ErrRateLimited = errors.New("neatpulse: rate limited")
	// ErrNotFound means Pulse answered 404 (unknown organisation or endpoint).
	ErrNotFound = errors.New("neatpulse: not found")
)

// RateLimitError wraps ErrRateLimited with the delay from Retry-After (zero
// if absent or unparseable).
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("neatpulse: rate limited (retry after %s)", e.RetryAfter)
}
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API base URL (tests).
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") }
}

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// Client is safe for concurrent use.
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// New returns a client for an API key.
func New(apiKey string, opts ...Option) *Client {
	c := &Client{apiKey: apiKey, baseURL: defaultBaseURL, http: &http.Client{Timeout: 30 * time.Second}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// BasicEndpoint is one device in the organisation's endpoint list.
type BasicEndpoint struct {
	ID           string `json:"id"`
	Connected    bool   `json:"connected"`
	InByod       bool   `json:"inByod"`
	InCallStatus string `json:"inCallStatus"`
	Model        string `json:"model"`
	Serial       string `json:"serial"`
	RoomID       int    `json:"roomId"`
	RoomName     string `json:"roomName"`
	LocationID   int    `json:"locationId"`
	LocationName string `json:"locationName"`
	RegionID     int    `json:"regionId"`
	RegionName   string `json:"regionName"`
}

// Network is one network interface of an endpoint.
type Network struct {
	MACAddress  string `json:"macAddress"`
	Type        string `json:"type"`
	LinkUp      bool   `json:"linkUp"`
	NetworkName string `json:"networkName"`
	Display     string `json:"display"`
}

// Endpoint is one device's details.
type Endpoint struct {
	ID                           string    `json:"id"`
	Connected                    bool      `json:"connected"`
	ConnectionTime               Time      `json:"connectionTime"`
	RestartTime                  Time      `json:"restartTime"`
	Model                        string    `json:"model"`
	Serial                       string    `json:"serial"`
	FirmwareVersion              string    `json:"firmwareVersion"`
	FirmwareVersionReleaseName   string    `json:"firmwareVersionReleaseName"`
	LatestVersion                string    `json:"latestVersion"`
	HasScheduledFirmwareUpdate   bool      `json:"hasScheduledFirmwareUpdate"`
	HasUnscheduledFirmwareUpdate bool      `json:"hasUnscheduledFirmwareUpdate"`
	UpgradeStatus                string    `json:"upgradeStatus"`
	InCallStatus                 string    `json:"inCallStatus"`
	InByod                       bool      `json:"inByod"`
	PrimaryMode                  string    `json:"primaryMode"`
	ControllerMode               string    `json:"controllerMode"`
	PairingSerial                string    `json:"pairingSerial"`
	LocalIPAddress               string    `json:"localIpAddress"`
	Networks                     []Network `json:"networks"`
	RoomID                       int       `json:"roomId"`
	RoomName                     string    `json:"roomName"`
	Timezone                     string    `json:"timezone"`
}

// MAC returns the first network interface's MAC address as aa:bb:cc:dd:ee:ff.
// The schema types macAddress as bytes, which protobuf JSON sends as base64;
// a value that is already text is returned as given.
func (e Endpoint) MAC() string {
	for _, n := range e.Networks {
		if m := FormatMAC(n.MACAddress); m != "" {
			return m
		}
	}
	return ""
}

// FormatMAC turns a base64 6-byte MAC into colon-separated hex; any other
// non-empty value is returned trimmed.
func FormatMAC(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if strings.ContainsAny(v, ":-") {
		return strings.ToLower(v)
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(v); err == nil && len(b) == 6 {
			parts := make([]string, len(b))
			for i, x := range b {
				parts[i] = fmt.Sprintf("%02x", x)
			}
			return strings.Join(parts, ":")
		}
	}
	return v
}

// SensorSample is one sensor reading. A field Pulse did not report is nil.
type SensorSample struct {
	CO2          *float64  `json:"co2"`
	Humidity     *float64  `json:"humidity"`
	Illumination *float64  `json:"illumination"`
	People       *int      `json:"people"`
	Temp         *float64  `json:"temp"`
	VOC          *float64  `json:"voc"`
	VOCIndex     *float64  `json:"vocIndex"`
	Timestamp    time.Time `json:"-"`
}

// UnmarshalJSON reads the sample. timestamp is an int64, which protobuf
// JSON may send as a string; seconds and milliseconds since the epoch and
// RFC 3339 are all accepted. people may also arrive as a string.
func (s *SensorSample) UnmarshalJSON(b []byte) error {
	type plain struct {
		CO2          *float64        `json:"co2"`
		Humidity     *float64        `json:"humidity"`
		Illumination *float64        `json:"illumination"`
		People       json.RawMessage `json:"people"`
		Temp         *float64        `json:"temp"`
		VOC          *float64        `json:"voc"`
		VOCIndex     *float64        `json:"vocIndex"`
		Timestamp    json.RawMessage `json:"timestamp"`
	}
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*s = SensorSample{CO2: p.CO2, Humidity: p.Humidity, Illumination: p.Illumination, Temp: p.Temp, VOC: p.VOC, VOCIndex: p.VOCIndex}
	if n, ok := rawNumber(p.People); ok {
		v := int(n)
		s.People = &v
	}
	s.Timestamp = rawTime(p.Timestamp)
	return nil
}

// rawNumber reads a JSON number or a numeric string.
func rawNumber(r json.RawMessage) (float64, bool) {
	t := strings.Trim(strings.TrimSpace(string(r)), `"`)
	if t == "" || t == "null" {
		return 0, false
	}
	n, err := strconv.ParseFloat(t, 64)
	return n, err == nil
}

func rawTime(r json.RawMessage) time.Time {
	t := strings.Trim(strings.TrimSpace(string(r)), `"`)
	if t == "" || t == "null" {
		return time.Time{}
	}
	if n, err := strconv.ParseInt(t, 10, 64); err == nil && n > 0 {
		if n > 1e12 { // milliseconds
			return time.UnixMilli(n).UTC()
		}
		return time.Unix(n, 0).UTC()
	}
	if ts, err := time.Parse(time.RFC3339Nano, t); err == nil {
		return ts.UTC()
	}
	return time.Time{}
}

// Time is a timestamp that reads RFC 3339, epoch seconds or milliseconds
// (as a number or a string); anything else, or null, is the zero time.
type Time struct{ time.Time }

// UnmarshalJSON never fails, so one odd timestamp cannot lose a device.
func (t *Time) UnmarshalJSON(b []byte) error {
	t.Time = rawTime(b)
	return nil
}

// EndpointSensors is the sensor data of one endpoint.
type EndpointSensors struct {
	EndpointID    string
	ShutterClosed *bool
	Samples       []SensorSample
}

// Latest returns the newest sample (by timestamp, else the last listed), and
// false when there are none.
func (e EndpointSensors) Latest() (SensorSample, bool) {
	if len(e.Samples) == 0 {
		return SensorSample{}, false
	}
	best := e.Samples[len(e.Samples)-1]
	for _, s := range e.Samples {
		if s.Timestamp.After(best.Timestamp) {
			best = s
		}
	}
	return best, true
}

// ListEndpoints lists the organisation's endpoints.
func (c *Client) ListEndpoints(ctx context.Context, org string) ([]BasicEndpoint, error) {
	var out struct {
		Endpoints []BasicEndpoint `json:"endpoints"`
	}
	if err := c.get(ctx, "/v1/orgs/"+url.PathEscape(org)+"/endpoints", &out); err != nil {
		return nil, err
	}
	return out.Endpoints, nil
}

// Endpoint reads one endpoint's details.
func (c *Client) Endpoint(ctx context.Context, org, id string) (Endpoint, error) {
	var out Endpoint
	err := c.get(ctx, "/v1/orgs/"+url.PathEscape(org)+"/endpoints/"+url.PathEscape(id), &out)
	return out, err
}

// SensorData reads the latest sensor data of every endpoint in the organisation.
func (c *Client) SensorData(ctx context.Context, org string) ([]EndpointSensors, error) {
	var out struct {
		Data []struct {
			ID           string `json:"id"`
			EndpointData *struct {
				ShutterClosed *bool          `json:"shutterClosed"`
				Data          []SensorSample `json:"data"`
			} `json:"endpointData"`
		} `json:"data"`
	}
	if err := c.get(ctx, "/v1/orgs/"+url.PathEscape(org)+"/endpoints/sensor", &out); err != nil {
		return nil, err
	}
	res := make([]EndpointSensors, 0, len(out.Data))
	for _, d := range out.Data {
		e := EndpointSensors{EndpointID: d.ID}
		if d.EndpointData != nil {
			e.ShutterClosed = d.EndpointData.ShutterClosed
			e.Samples = d.EndpointData.Data
		}
		res = append(res, e)
	}
	return res, nil
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("neatpulse: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		// url.Error carries the URL (no key: it is a header) and the cause.
		return fmt.Errorf("neatpulse: request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		// 403 is folded into "rejected": Pulse answers it both for a key
		// without Read access and for a key from another organisation, and
		// either way the fix is a new key.
		return ErrUnauthorized
	case resp.StatusCode == http.StatusTooManyRequests:
		return &RateLimitError{RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode >= 400:
		return fmt.Errorf("neatpulse: HTTP %d: %s", resp.StatusCode, errorMessage(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("neatpulse: parse response: %w", err)
	}
	return nil
}

// errorMessage returns Pulse's error message (webpageErrorResponse), cut short.
func errorMessage(b []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	s := strings.TrimSpace(string(b))
	if json.Unmarshal(b, &e) == nil && e.Message != "" {
		s = e.Message
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// MaxRetryAfter caps the wait a 429 can ask for, so a bad or hostile header
// cannot stop polling for long.
const MaxRetryAfter = 5 * time.Minute

// parseRetryAfter reads delta-seconds or an HTTP date, capped at MaxRetryAfter.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	var d time.Duration
	if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
		if n > MaxRetryAfter.Seconds() {
			return MaxRetryAfter
		}
		d = time.Duration(n * float64(time.Second))
	} else if t, err := http.ParseTime(v); err == nil {
		d = time.Until(t)
	}
	switch {
	case d <= 0:
		return 0
	case d > MaxRetryAfter:
		return MaxRetryAfter
	}
	return d
}

// CallPlatform maps inCallStatus to the platform of the call in progress:
// "" when not in a call (NONE or empty), else zoom, teams, meet, byod or other.
func CallPlatform(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "", "NONE":
		return ""
	case "ZOOM":
		return "zoom"
	case "TEAMS":
		return "teams"
	case "MEET", "MEET_CONTENT":
		return "meet"
	case "OPEN", "OPEN_BYOD":
		return "byod"
	}
	return "other"
}

// RoomType maps a primary mode to a device type: zoom_room, teams_room, or
// "" when unknown. Pulse does not document the values; any value containing
// "zoom" is Zoom, any containing "teams" or "microsoft" is Teams.
func RoomType(primaryMode string) string {
	m := strings.ToLower(primaryMode)
	switch {
	case strings.Contains(m, "zoom"):
		return "zoom_room"
	case strings.Contains(m, "teams"), strings.Contains(m, "microsoft"):
		return "teams_room"
	}
	return ""
}

// IsCompanionModel reports whether a model is a companion device (a Neat Pad)
// that pairs with a room's main unit rather than being one.
func IsCompanionModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "pad")
}
