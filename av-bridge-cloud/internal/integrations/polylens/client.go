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

	pageSize  = 200 // Lens caps query cost at 10000; ~25 points per device with all fields
	batchSize = 200
	maxPages  = 200
)

var (
	// ErrUnauthorized means Lens rejected the credentials.
	ErrUnauthorized = errors.New("polylens: unauthorized")
	// ErrForbidden means the credential is valid but its Lens role may not do
	// this (Lens answers 403 with an AUTHORIZATION_ERROR GraphQL error), e.g.
	// assigning tags to devices.
	ErrForbidden = errors.New("polylens: forbidden: the Lens role does not allow this")
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

	// Set once Lens rejected the optional (undocumented) fields of that query
	// kind AND the base query then succeeded; later queries of that kind go
	// straight to the base selection.
	noOptionalInv, noOptionalAcc atomic.Bool
}

// Degraded reports whether optional fields had to be dropped from any query,
// so Teams-specific data (call status, health, presence, peripherals, tags)
// is unavailable and ListTeamsRooms cannot filter to Teams devices.
func (c *Client) Degraded() bool { return c.noOptionalInv.Load() || c.noOptionalAcc.Load() }

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
	ActiveApplicationName    string    `json:"activeApplicationName"`
	ActiveApplicationVersion string    `json:"activeApplicationVersion"`
	CallStatus               string    `json:"callStatus"`
	TeamsDeviceHealthStatus  string    `json:"teamsDeviceHealthStatus"`
	TeamsPresenceAvail       string    `json:"teamsPresenceAvailability"`
	TeamsPresenceActivity    string    `json:"teamsPresenceActivity"`
	AllPeripheralsLinked     *bool     `json:"allPeripheralsLinked"`
	LensTags                 []nameRef `json:"lensTags"`
}

func (r rawDevice) device() Device {
	d := Device{
		ID: r.ID, Name: r.Name, SerialNumber: r.SerialNumber, MACAddress: r.MACAddress,
		Model: r.HardwareModel, SoftwareVersion: r.SoftwareVersion, Connected: r.Connected,
		AppName: r.ActiveApplicationName, AppVersion: r.ActiveApplicationVersion,
		CallStatus: r.CallStatus, TeamsHealth: r.TeamsDeviceHealthStatus,
		PresenceAvailability: r.TeamsPresenceAvail, PresenceActivity: r.TeamsPresenceActivity,
		PeripheralsLinked: r.AllPeripheralsLinked,
	}
	for _, t := range r.LensTags {
		if t.Name != "" {
			d.Tags = append(d.Tags, t.Name)
		}
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
	if err := c.graphqlOptional(ctx, `query { tenants { id name } }`, `query { tenants { id } }`, nil, &out, &c.noOptionalAcc); err != nil {
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
// Verified against a real tenant (Task 11); the deprecated `tags` field is replaced by `lensTags { name }`.
const optionalFields = `activeApplicationName activeApplicationVersion callStatus teamsDeviceHealthStatus
teamsPresenceAvailability teamsPresenceActivity allPeripheralsLinked lensTags { name }`

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
	seen := map[string]bool{}
	for page := 0; ; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf("polylens: device search exceeded %d pages", maxPages)
		}
		params := map[string]any{"pageSize": pageSize, "nextToken": next}
		if filter != nil {
			params["filter"] = filter
		}
		var out searchResp
		err := c.graphqlOptional(ctx, inventoryQuery(baseFields+" "+optionalFields), inventoryQuery(baseFields),
			map[string]any{"tenantId": accountID, "params": params}, &out, &c.noOptionalInv)
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
		if seen[*ds.PageInfo.NextToken] {
			return nil, errors.New("polylens: device search repeated a page token")
		}
		seen[*ds.PageInfo.NextToken] = true
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
	if c.noOptionalInv.Load() {
		// Teams fields are unavailable: filtering would hide every device.
		// Callers should check Degraded().
		return all, nil
	}
	var res []Device
	for _, d := range all {
		if d.isTeams() {
			res = append(res, d)
		}
	}
	return res, nil
}

// Devices re-reads the given devices (at most 200 ids per query) within one account.
func (c *Client) Devices(ctx context.Context, accountID string, ids []string) ([]Device, error) {
	var res []Device
	for start := 0; start < len(ids); start += batchSize {
		end := start + batchSize
		if end > len(ids) {
			end = len(ids)
		}
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

// ErrTagNotFound means Lens says the tag does not exist (a deleted tag can
// linger in listings as a ghost).
var ErrTagNotFound = errors.New("polylens: no tag found")

// TagRetryDelay is the pause between deleteTag attempts. Lens answers 403 for
// a tag that was created seconds ago, so a delete is retried a few times.
var TagRetryDelay = 4 * time.Second

const deleteTagAttempts = 5

// FindTag returns the id of the tag with this exact name, "" if none is
// listed. Lens listings can keep deleted tags for minutes, so a found id is
// not proof the tag exists.
func (c *Client) FindTag(ctx context.Context, accountID, tag string) (string, error) {
	var out struct {
		Tenant struct {
			LensTags []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"lensTags"`
		} `json:"tenant"`
	}
	err := c.graphql(ctx, `query($tenantId: ID!, $name: String) { tenant(id: $tenantId) { lensTags(filter: {name: $name}) { id name } } }`,
		map[string]any{"tenantId": accountID, "name": tag}, &out)
	if err != nil {
		return "", err
	}
	for _, t := range out.Tenant.LensTags {
		if t.Name == tag { // Lens's name filter may be a substring match
			return t.ID, nil
		}
	}
	return "", nil
}

// CreateTag creates a tag in the account and returns its id. Lens rejects
// assigning or deleting the tag (403) for several seconds afterwards.
func (c *Client) CreateTag(ctx context.Context, accountID, tag string) (string, error) {
	var cr struct {
		CreateTag struct {
			ID string `json:"id"`
		} `json:"createTag"`
	}
	if err := c.graphql(ctx, `mutation($tenantId: String!, $name: String!) { createTag(tenantId: $tenantId, input: {name: $name}) { id } }`,
		map[string]any{"tenantId": accountID, "name": tag}, &cr); err != nil {
		return "", err
	}
	if cr.CreateTag.ID == "" {
		return "", errors.New("polylens: createTag returned no id")
	}
	return cr.CreateTag.ID, nil
}

type tagResult []struct {
	DeviceID string `json:"deviceId"`
	Success  *bool  `json:"success"`
}

func (r tagResult) check() error {
	for _, x := range r {
		if x.Success != nil && !*x.Success {
			return errors.New("polylens: tag change was not applied")
		}
	}
	return nil
}

// AssignTag adds an existing tag (by id) to a device. A single attempt:
// callers retry on ErrForbidden or ErrTagNotFound.
func (c *Client) AssignTag(ctx context.Context, tagID, deviceID string) error {
	var out struct {
		R tagResult `json:"addLensTagToDevice"`
	}
	if err := c.graphql(ctx, `mutation($tagIds: [String!]!, $deviceIds: [String!]!) { addLensTagToDevice(tagIds: $tagIds, deviceIds: $deviceIds) { deviceId success } }`,
		map[string]any{"tagIds": []string{tagID}, "deviceIds": []string{deviceID}}, &out); err != nil {
		return err
	}
	return out.R.check()
}

// DeleteTagOnce is a single deleteTag attempt. A tag that is already gone
// counts as deleted; a young tag answers ErrForbidden.
func (c *Client) DeleteTagOnce(ctx context.Context, id string) error {
	err := c.graphql(ctx, `mutation($id: String!) { deleteTag(id: $id) { success } }`, map[string]any{"id": id}, nil)
	if errors.Is(err, ErrTagNotFound) {
		return nil
	}
	return err
}

// DeleteTag deletes a tag by id, retrying a 403 a few times (the tag may be
// brand new). A tag that is already gone counts as deleted. A 403 that
// outlasts the retries is returned as ErrForbidden.
func (c *Client) DeleteTag(ctx context.Context, id string) error {
	var err error
	for i := 0; i < deleteTagAttempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(TagRetryDelay):
			}
		}
		if err = c.DeleteTagOnce(ctx, id); !errors.Is(err, ErrForbidden) {
			return err
		}
	}
	return err
}

// UnassignTag removes a tag (by id) from a device. A tag Lens no longer knows
// is a no-op.
func (c *Client) UnassignTag(ctx context.Context, tagID, deviceID string) error {
	var out struct {
		R tagResult `json:"removeLensTagFromDevice"`
	}
	if err := c.graphql(ctx, `mutation($tagId: String!, $deviceIds: [String!]!) { removeLensTagFromDevice(tagId: $tagId, deviceIds: $deviceIds) { deviceId success } }`,
		map[string]any{"tagId": tagID, "deviceIds": []string{deviceID}}, &out); err != nil {
		if errors.Is(err, ErrTagNotFound) {
			return nil
		}
		return err
	}
	return out.R.check()
}

// RemoveTagByID unassigns a per-device tag and then deletes it (the delete is
// best effort: once the unassign has succeeded nothing is left on the device).
func (c *Client) RemoveTagByID(ctx context.Context, tagID, deviceID string) error {
	if err := c.UnassignTag(ctx, tagID, deviceID); err != nil {
		return err
	}
	_ = c.DeleteTag(ctx, tagID)
	return nil
}

// RemoveTag removes a tag from a device. A tag that does not exist is a no-op.
// A per-device tag ("marcus:<device id>") is unique to that device, so it is
// deleted after the unassign (best effort: a 403 or a missing tag is ignored,
// since the unassign already succeeded). Any other tag is left in place
// because other devices may use it.
func (c *Client) RemoveTag(ctx context.Context, accountID, deviceID, tag string) error {
	id, err := c.FindTag(ctx, accountID, tag)
	if err != nil || id == "" {
		return err
	}
	if err := c.UnassignTag(ctx, id, deviceID); err != nil {
		return err
	}
	if strings.HasPrefix(tag, "marcus:") {
		_ = c.DeleteTag(ctx, id)
	}
	return nil
}

// DeleteTagByName deletes the tag with this name if one is listed. Ghosts and
// a lingering 403 are not errors.
func (c *Client) DeleteTagByName(ctx context.Context, accountID, tag string) error {
	id, err := c.FindTag(ctx, accountID, tag)
	if err != nil || id == "" {
		return err
	}
	if err := c.DeleteTag(ctx, id); err != nil && !errors.Is(err, ErrForbidden) {
		return err
	}
	return nil
}

// schemaError marks a GraphQL validation error (unknown field/argument).
type schemaError struct{ msg string }

func (e *schemaError) Error() string { return e.msg }

// graphqlOptional runs full; if Lens rejects it as a schema/validation error it
// runs base instead and, if that succeeds, remembers the choice in flag.
func (c *Client) graphqlOptional(ctx context.Context, full, base string, vars map[string]any, out any, flag *atomic.Bool) error {
	if !flag.Load() {
		err := c.graphql(ctx, full, vars, out)
		var se *schemaError
		if err == nil || !errors.As(err, &se) {
			return err
		}
		if err := c.graphql(ctx, base, vars, out); err != nil {
			return err
		}
		flag.Store(true) // only once the fallback is proven to work
		return nil
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
	case resp.StatusCode == http.StatusForbidden && bytes.Contains(respBody, []byte("AUTHORIZATION_ERROR")):
		return ErrForbidden
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return ErrUnauthorized
	case resp.StatusCode == http.StatusTooManyRequests:
		return &RateLimitError{RetryAfter: parseReset(resp.Header.Get("ratelimit-reset"))}
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message    string `json:"message"`
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	parseErr := json.Unmarshal(respBody, &env)
	if resp.StatusCode >= 400 && (parseErr != nil || len(env.Errors) == 0) {
		return fmt.Errorf("polylens: graphql HTTP %d: %s", resp.StatusCode, snippet(respBody))
	}
	if parseErr != nil {
		return fmt.Errorf("polylens: parse graphql response: %w", parseErr)
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, 0, len(env.Errors))
		schema := true
		for _, e := range env.Errors {
			msgs = append(msgs, e.Message)
			m := strings.ToLower(e.Message)
			if e.Extensions.Code != "GRAPHQL_VALIDATION_FAILED" && !strings.Contains(m, "cannot query field") && !strings.Contains(m, "unknown argument") &&
				!strings.Contains(m, "unknown type") {
				schema = false
			}
		}
		msg := "polylens: graphql errors: " + strings.Join(msgs, "; ")
		for _, m := range msgs {
			if strings.Contains(m, "No Tag found") {
				return fmt.Errorf("%w: %s", ErrTagNotFound, msg)
			}
		}
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
