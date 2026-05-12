// Package lens is a small client for the Poly Lens GraphQL API
// (https://api.lens.poly.com/docs/graphql/getting-started).
//
// It exists to enrich device telemetry with fields the device's own REST API
// doesn't expose (MAC address, asset tag, site/room assignment, last-seen).
// Authentication is OAuth 2.0 client credentials; tokens are cached for their
// stated lifetime and refreshed on demand.
package lens

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	tokenURL   = "https://login.lens.poly.com/oauth/token"
	graphqlURL = "https://api.silica-prod01.io.lens.poly.com/graphql"
)

// Client is safe for concurrent use.
type Client struct {
	clientID     string
	clientSecret string
	http         *http.Client

	tokenMu  sync.Mutex
	token    string
	tokenExp time.Time
}

func NewClient(clientID, clientSecret string) *Client {
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		http: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

// Device is a flat projection of the Lens Device GraphQL type, holding only
// the fields av-bridge currently surfaces. Extend as needed; missing fields
// stay zero-valued and are skipped by the adapter.
type Device struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	DisplayName     string     `json:"displayName"`
	SerialNumber    string     `json:"serialNumber"`
	MacAddress      string     `json:"macAddress"`
	InternalIP      string     `json:"internalIp"`
	ExternalIP      string     `json:"externalIp"`
	Model           string     `json:"model"`
	Manufacturer    string     `json:"manufacturer"`
	SoftwareVersion string     `json:"softwareVersion"`
	Connected       bool       `json:"connected"`
	LastDetected    time.Time  `json:"lastDetected"`
	Room            *NameRef   `json:"room"`
	Site            *NameRef   `json:"site"`
}

// NameRef is the shape we project for any Lens nested type that has a name —
// keep this minimal so the query stays well below the rate-limit cost.
type NameRef struct {
	Name string `json:"name"`
}

// LookupBySerial fetches a single device from Lens matched by serial number.
// Returns nil, nil when the device isn't found (so callers can distinguish
// "not in Lens" from "lookup failed").
//
// The Lens DeviceFindArgs schema accepts a nested `filter` object; field set
// here is the subset confirmed valid via Introspect.
func (c *Client) LookupBySerial(ctx context.Context, serial string) (*Device, error) {
	query := `
query DeviceBySerial($serial: String!) {
  deviceSearch(params: { filter: { serialNumber: $serial } }) {
    edges {
      node {
        id
        name
        displayName
        serialNumber
        macAddress
        internalIp
        externalIp
        model
        manufacturer
        softwareVersion
        connected
        lastDetected
        room { name }
        site { name }
      }
    }
  }
}`
	var out struct {
		DeviceSearch struct {
			Edges []struct {
				Node Device `json:"node"`
			} `json:"edges"`
		} `json:"deviceSearch"`
	}
	if err := c.graphql(ctx, query, map[string]any{"serial": serial}, &out); err != nil {
		return nil, err
	}
	for _, e := range out.DeviceSearch.Edges {
		if e.Node.SerialNumber == serial {
			n := e.Node
			return &n, nil
		}
	}
	return nil, nil
}

// Introspect dumps the schema for DeviceFindArgs, the type referenced by its
// `filter` arg, and the Device type. Used as a startup-time diagnostic so
// schema mismatches surface as a single log line rather than a stream of
// per-poll lookup failures.
func (c *Client) Introspect(ctx context.Context) (string, error) {
	type typeRef struct {
		Kind   string   `json:"kind"`
		Name   string   `json:"name"`
		OfType *typeRef `json:"ofType"`
	}
	type fieldDesc struct {
		Name string  `json:"name"`
		Type typeRef `json:"type"`
	}

	query1 := `{
  args: __type(name: "DeviceFindArgs") {
    inputFields { name type { kind name ofType { kind name ofType { kind name } } } }
  }
  dev: __type(name: "Device") {
    fields { name type { kind name ofType { kind name ofType { kind name } } } }
  }
}`
	var resp1 struct {
		Args struct {
			InputFields []fieldDesc `json:"inputFields"`
		} `json:"args"`
		Dev struct {
			Fields []fieldDesc `json:"fields"`
		} `json:"dev"`
	}
	if err := c.graphql(ctx, query1, nil, &resp1); err != nil {
		return "", err
	}

	filterTypeName := ""
	for _, f := range resp1.Args.InputFields {
		if f.Name == "filter" {
			filterTypeName = unwrapTypeName(&f.Type)
			break
		}
	}

	var filterFields []fieldDesc
	if filterTypeName != "" {
		query2 := `query($n: String!) {
  ft: __type(name: $n) {
    inputFields { name type { kind name ofType { kind name ofType { kind name } } } }
  }
}`
		var resp2 struct {
			FT struct {
				InputFields []fieldDesc `json:"inputFields"`
			} `json:"ft"`
		}
		if err := c.graphql(ctx, query2, map[string]any{"n": filterTypeName}, &resp2); err == nil {
			filterFields = resp2.FT.InputFields
		}
	}

	formatFields := func(fields []fieldDesc) string {
		parts := make([]string, 0, len(fields))
		for _, f := range fields {
			parts = append(parts, f.Name+":"+formatType(&f.Type))
		}
		sort.Strings(parts)
		return strings.Join(parts, ", ")
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "DeviceFindArgs.inputFields = [%s]", formatFields(resp1.Args.InputFields))
	if filterTypeName != "" {
		fmt.Fprintf(&sb, "\n%s.inputFields = [%s]", filterTypeName, formatFields(filterFields))
	}
	fmt.Fprintf(&sb, "\nDevice.fields = [%s]", formatFields(resp1.Dev.Fields))
	return sb.String(), nil
}

// unwrapTypeName follows NON_NULL / LIST wrappers down to the concrete type
// name. Returns empty string if the chain has no named type.
func unwrapTypeName(t any) string {
	type ref struct {
		Kind   string
		Name   string
		OfType *ref
	}
	// We accept any here so the helper can be used against the inline structs
	// in Introspect without having to thread a shared type through.
	b, _ := json.Marshal(t)
	var r ref
	if err := json.Unmarshal(b, &r); err != nil {
		return ""
	}
	for cur := &r; cur != nil; cur = cur.OfType {
		if cur.Name != "" {
			return cur.Name
		}
	}
	return ""
}

// formatType renders a type reference in the standard GraphQL shorthand
// (e.g. "String!", "[Device!]!") for compact log output.
func formatType(t any) string {
	type ref struct {
		Kind   string
		Name   string
		OfType *ref
	}
	b, _ := json.Marshal(t)
	var r ref
	if err := json.Unmarshal(b, &r); err != nil {
		return "?"
	}
	var render func(*ref) string
	render = func(x *ref) string {
		if x == nil {
			return "?"
		}
		switch x.Kind {
		case "NON_NULL":
			return render(x.OfType) + "!"
		case "LIST":
			return "[" + render(x.OfType) + "]"
		}
		if x.Name != "" {
			return x.Name
		}
		return "?"
	}
	return render(&r)
}

// graphql executes a single GraphQL operation. Re-authenticates and retries
// once if the response is 401 Unauthorized (covers the JWT expiring mid-call).
func (c *Client) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": vars,
	})
	if err != nil {
		return fmt.Errorf("marshal query: %w", err)
	}

	send := func() (*http.Response, error) {
		token, err := c.accessToken(ctx)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphqlURL, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("build graphql request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		return c.http.Do(req)
	}

	resp, err := send()
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		c.invalidateToken()
		resp, err = send()
		if err != nil {
			return err
		}
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("lens graphql HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(respBody, &env); err != nil {
		return fmt.Errorf("parse graphql envelope: %w (body: %s)", err, string(respBody))
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("lens graphql errors: %v", msgs)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("parse graphql data: %w", err)
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

	body, err := json.Marshal(map[string]string{
		"client_id":     c.clientID,
		"client_secret": c.clientSecret,
		"grant_type":    "client_credentials",
	})
	if err != nil {
		return "", fmt.Errorf("marshal token request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("lens token request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("lens token HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var t struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(respBody, &t); err != nil {
		return "", fmt.Errorf("parse token response: %w", err)
	}
	if t.AccessToken == "" {
		return "", errors.New("lens token response missing access_token")
	}
	c.token = t.AccessToken
	if t.ExpiresIn > 0 {
		c.tokenExp = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	} else {
		c.tokenExp = time.Now().Add(23 * time.Hour)
	}
	slog.Debug("lens token refreshed", "expires_in_s", t.ExpiresIn)
	return c.token, nil
}

func (c *Client) invalidateToken() {
	c.tokenMu.Lock()
	c.token = ""
	c.tokenExp = time.Time{}
	c.tokenMu.Unlock()
}
