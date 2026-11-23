// Package polylens is the cloud-side client for the Poly Lens GraphQL API
// (https://api.lens.poly.com/). It connects a customer's Lens account, lists
// Microsoft Teams Rooms devices, re-reads linked devices and tags them.
//
// Authentication is OAuth 2.0 client credentials; tokens are cached until 60 s
// before expiry. The client secret is never included in an error string.
package polylens

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultTokenURL   = "https://login.lens.poly.com/oauth/token"
	defaultGraphQLURL = "https://api.silica-prod01.io.lens.poly.com/graphql"

	pageSize  = 500
	batchSize = 500
)

var (
	// ErrUnauthorized means Lens rejected the credentials.
	ErrUnauthorized = errors.New("polylens: unauthorized")
	// ErrRateLimited means Lens answered 429. Use errors.As with *RateLimitError
	// for the wait Lens asked for.
	ErrRateLimited = errors.New("polylens: rate limited")
)

// RateLimitError wraps ErrRateLimited with the delay from the ratelimit-reset
// header (zero if absent or unparseable).
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("polylens: rate limited (retry after %s)", e.RetryAfter)
}
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// Option configures a Client.
type Option func(*Client)

// WithBaseURLs overrides the token and GraphQL endpoints (tests).
func WithBaseURLs(tokenURL, graphqlURL string) Option {
	return func(c *Client) { c.tokenURL, c.graphqlURL = tokenURL, graphqlURL }
}

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// Client is safe for concurrent use.
type Client struct {
	clientID, clientSecret string
	tokenURL, graphqlURL   string
	http                   *http.Client

	tokenMu  sync.Mutex
	token    string
	tokenExp time.Time

	// noOptional is set once Lens rejected the optional (undocumented) fields;
	// later queries then go straight to the base selection.
	noOptional atomic.Bool
}

// New returns a client for the given OAuth credentials.
func New(clientID, clientSecret string, opts ...Option) *Client {
	c := &Client{
		clientID: clientID, clientSecret: clientSecret,
		tokenURL: defaultTokenURL, graphqlURL: defaultGraphQLURL,
		http: &http.Client{Timeout: 30 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Account is a Lens tenant the credential can reach.
type Account struct{ ID, Name string }

// Device is a flat projection of a Lens device.
type Device struct {
	ID, Name, SerialNumber, MACAddress, Model, SoftwareVersion string
	RoomName, SiteName, AppName, AppVersion                    string
	CallStatus, TeamsHealth                                    string
	PresenceAvailability, PresenceActivity                     string
	Connected                                                  bool
	LastDetected                                               time.Time
	PeripheralsLinked                                          *bool // nil = unknown
	Tags                                                       []string
}

type nameRef struct {
	Name string `json:"name"`
}

type rawDevice struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	SerialNumber    string     `json:"serialNumber"`
	MACAddress      string     `json:"macAddress"`
	HardwareModel   string     `json:"hardwareModel"`
	SoftwareVersion string     `json:"softwareVersion"`
	Connected       bool       `json:"connected"`
	LastDetected    *time.Time `json:"lastDetected"`
	Room            *nameRef   `json:"room"`
	Site            *nameRef   `json:"site"`

	// Optional / undocumented.
	ActiveApplicationName    string   `json:"activeApplicationName"`
	ActiveApplicationVersion string   `json:"activeApplicationVersion"`
	CallStatus               string   `json:"callStatus"`
	TeamsDeviceHealthStatus  string   `json:"teamsDeviceHealthStatus"`
	TeamsPresenceAvail       string   `json:"teamsPresenceAvailability"`
	TeamsPresenceActivity    string   `json:"teamsPresenceActivity"`
	AllPeripheralsLinked     *bool    `json:"allPeripheralsLinked"`
	Tags                     []string `json:"tags"`
}

func (r rawDevice) device() Device {
	d := Device{
		ID: r.ID, Name: r.Name, SerialNumber: r.SerialNumber, MACAddress: r.MACAddress,
		Model: r.HardwareModel, SoftwareVersion: r.SoftwareVersion, Connected: r.Connected,
		AppName: r.ActiveApplicationName, AppVersion: r.ActiveApplicationVersion,
		CallStatus: r.CallStatus, TeamsHealth: r.TeamsDeviceHealthStatus,
		PresenceAvailability: r.TeamsPresenceAvail, PresenceActivity: r.TeamsPresenceActivity,
		PeripheralsLinked: r.AllPeripheralsLinked, Tags: r.Tags,
	}
	if r.LastDetected != nil {
		d.LastDetected = *r.LastDetected
	}
	if r.Room != nil {
		d.RoomName = r.Room.Name
	}
	if r.Site != nil {
		d.SiteName = r.Site.Name
	}
	return d
}

func (d Device) isTeams() bool {
	return strings.Contains(strings.ToLower(d.AppName), "teams") ||
		d.TeamsHealth != "" || d.PresenceAvailability != "" || d.PresenceActivity != ""
}

// Accounts lists the tenants the credential can reach.
func (c *Client) Accounts(ctx context.Context) ([]Account, error) {
	var out struct {
		Tenants []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"tenants"`
	}
	// unverified: confirm in Task 11 - docs show only `tenants { id }`; `name` is optional.
	if err := c.graphqlOptional(ctx, `query { tenants { id name } }`, `query { tenants { id } }`, nil, &out); err != nil {
		return nil, err
	}
	res := make([]Account, 0, len(out.Tenants))
	for _, t := range out.Tenants {
		res = append(res, Account{ID: t.ID, Name: t.Name})
	}
	return res, nil
}

const baseFields = `id name serialNumber macAddress hardwareModel softwareVersion connected lastDetected
room { name } site { name }`

// Optional, undocumented fields. Lens rejects unknown fields, so these live in
// a separate selection that is dropped if the first query fails with a schema error.
// unverified: confirm in Task 11 - names of all fields below, including `tags`.
const optionalFields = `activeApplicationName activeApplicationVersion callStatus teamsDeviceHealthStatus
teamsPresenceAvailability teamsPresenceActivity allPeripheralsLinked tags`

func inventoryQuery(fields string) string {
	return `query Inventory($tenantId: ID!, $params: DeviceFindArgs) {
  tenant(id: $tenantId) { inventory { deviceSearch(params: $params) {
    pageInfo { nextToken hasNextPage }
    edges { node { ` + fields + ` } }
  } } }
}`
}

type searchResp struct {
	Tenant struct {
		Inventory struct {
			DeviceSearch struct {
				PageInfo struct {
					NextToken   *string `json:"nextToken"`
					HasNextPage bool    `json:"hasNextPage"`
				} `json:"pageInfo"`
				Edges []struct {
					Node rawDevice `json:"node"`
				} `json:"edges"`
			} `json:"deviceSearch"`
		} `json:"inventory"`
	} `json:"tenant"`
}

// search pages deviceSearch for one account with the given filter (may be nil).
func (c *Client) search(ctx context.Context, accountID string, filter map[string]any) ([]Device, error) {
	var devs []Device
	var next *string
	for {
		params := map[string]any{"pageSize": pageSize, "nextToken": next}
		if filter != nil {
			params["filter"] = filter
		}
		var out searchResp
		err := c.graphqlOptional(ctx, inventoryQuery(baseFields+" "+optionalFields), inventoryQuery(baseFields),
			map[string]any{"tenantId": accountID, "params": params}, &out)
		if err != nil {
			return nil, err
		}
		ds := out.Tenant.Inventory.DeviceSearch
		for _, e := range ds.Edges {
			devs = append(devs, e.Node.device())
		}
		if !ds.PageInfo.HasNextPage || ds.PageInfo.NextToken == nil || *ds.PageInfo.NextToken == "" {
			return devs, nil
		}
		next = ds.PageInfo.NextToken
	}
}

// ListTeamsRooms returns the account's Teams Rooms devices. The discriminator
// (app name contains "Teams", or a Teams field is non-empty) is initial.
func (c *Client) ListTeamsRooms(ctx context.Context, accountID string) ([]Device, error) {
	all, err := c.search(ctx, accountID, nil)
	if err != nil {
		return nil, err
	}
	var res []Device
	for _, d := range all {
		if d.isTeams() {
			res = append(res, d)
		}
	}
	return res, nil
}

// Devices re-reads the given devices (at most 500 ids per query) within one account.
func (c *Client) Devices(ctx context.Context, accountID string, ids []string) ([]Device, error) {
	var res []Device
	for start := 0; start < len(ids); start += batchSize {
		end := start + batchSize
		if end > len(ids) {
			end = len(ids)
		}
		// unverified: confirm in Task 11 - OR filter of id equality.
		or := make([]map[string]any, 0, end-start)
		for _, id := range ids[start:end] {
			or = append(or, map[string]any{"field": "id", "eq": id})
		}
		filter := map[string]any{"OR": or}
		if len(or) == 1 {
			filter = or[0]
		}
		ds, err := c.search(ctx, accountID, filter)
		if err != nil {
			return nil, err
		}
		res = append(res, ds...)
	}
	return res, nil
}

// AddTag adds a tag to a device.
func (c *Client) AddTag(ctx context.Context, deviceID, tag string) error {
	// unverified: confirm in Task 11 - mutation argument names and return type.
	return c.graphql(ctx, `mutation($deviceId: ID!, $tag: String!) { addDeviceTag(deviceId: $deviceId, tag: $tag) }`,
		map[string]any{"deviceId": deviceID, "tag": tag}, nil)
}

// RemoveTag removes a tag from a device.
func (c *Client) RemoveTag(ctx context.Context, deviceID, tag string) error {
	// unverified: confirm in Task 11 - mutation argument names and return type.
	return c.graphql(ctx, `mutation($deviceId: ID!, $tag: String!) { removeDeviceTag(deviceId: $deviceId, tag: $tag) }`,
		map[string]any{"deviceId": deviceID, "tag": tag}, nil)
}

// schemaError marks a GraphQL validation error (unknown field/argument).
type schemaError struct{ msg string }

func (e *schemaError) Error() string { return e.msg }

// graphqlOptional runs full; if Lens rejects it as a schema/validation error it
// remembers that and runs base instead.
func (c *Client) graphqlOptional(ctx context.Context, full, base string, vars map[string]any, out any) error {
	if !c.noOptional.Load() {
		err := c.graphql(ctx, full, vars, out)
		var se *schemaError
		if err == nil || !errors.As(err, &se) {
			return err
		}
		c.noOptional.Store(true)
	}
	return c.graphql(ctx, base, vars, out)
}

func (c *Client) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return fmt.Errorf("polylens: marshal query: %w", err)
	}
	send := func() (*http.Response, error) {
		token, err := c.accessToken(ctx)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.graphqlURL, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("polylens: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("polylens: graphql request: %w", err)
		}
		return resp, nil
	}
	resp, err := send()
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		c.invalidateToken()
		if resp, err = send(); err != nil {
			return err
		}
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrUnauthorized
	case resp.StatusCode == http.StatusTooManyRequests:
		return &RateLimitError{RetryAfter: parseReset(resp.Header.Get("ratelimit-reset"))}
	case resp.StatusCode >= 400:
		return fmt.Errorf("polylens: graphql HTTP %d: %s", resp.StatusCode, snippet(respBody))
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(respBody, &env); err != nil {
		return fmt.Errorf("polylens: parse graphql response: %w", err)
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, 0, len(env.Errors))
		schema := true
		for _, e := range env.Errors {
			msgs = append(msgs, e.Message)
			m := strings.ToLower(e.Message)
			if !strings.Contains(m, "cannot query field") && !strings.Contains(m, "unknown argument") &&
				!strings.Contains(m, "unknown type") {
				schema = false
			}
		}
		msg := "polylens: graphql errors: " + strings.Join(msgs, "; ")
		if schema {
			return &schemaError{msg}
		}
		return errors.New(msg)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("polylens: parse graphql data: %w", err)
		}
	}
	return nil
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.token != "" && time.Until(c.tokenExp) > 60*time.Second {
		return c.token, nil
	}
	body, _ := json.Marshal(map[string]string{
		"client_id": c.clientID, "client_secret": c.clientSecret, "grant_type": "client_credentials",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("polylens: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("polylens: token request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return "", ErrUnauthorized
	case resp.StatusCode == http.StatusTooManyRequests:
		return "", &RateLimitError{RetryAfter: parseReset(resp.Header.Get("ratelimit-reset"))}
	case resp.StatusCode >= 400:
		// Body deliberately omitted: auth servers can echo request details.
		return "", fmt.Errorf("polylens: token HTTP %d", resp.StatusCode)
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(respBody, &t); err != nil {
		return "", errors.New("polylens: parse token response")
	}
	if t.AccessToken == "" {
		return "", errors.New("polylens: token response missing access_token")
	}
	c.token = t.AccessToken
	if t.ExpiresIn > 0 {
		c.tokenExp = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	} else {
		c.tokenExp = time.Now().Add(23 * time.Hour)
	}
	return c.token, nil
}

func (c *Client) invalidateToken() {
	c.tokenMu.Lock()
	c.token, c.tokenExp = "", time.Time{}
	c.tokenMu.Unlock()
}

func parseReset(v string) time.Duration {
	if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && n > 0 {
		return time.Duration(n * float64(time.Second))
	}
	return 0
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
