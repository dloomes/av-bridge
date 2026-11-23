package polylens

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testSecret = "s3cr3t-value-xyz"

type fake struct {
	t        *testing.T
	tokens   atomic.Int32
	expires  int
	gqlCalls atomic.Int32
	handler  func(w http.ResponseWriter, r *http.Request, req gqlReq)
	authBad  bool
}

type gqlReq struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

func (f *fake) start() *Client {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		f.tokens.Add(1)
		if f.authBad {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"access_denied"}`)
			return
		}
		exp := f.expires
		if exp == 0 {
			exp = 86400
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": exp})
	})
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		f.gqlCalls.Add(1)
		var req gqlReq
		json.NewDecoder(r.Body).Decode(&req)
		f.handler(w, r, req)
	})
	srv := httptest.NewServer(mux)
	f.t.Cleanup(srv.Close)
	return New("cid", testSecret, WithBaseURLs(srv.URL+"/token", srv.URL+"/graphql"))
}

func file(t *testing.T, n string) string {
	b, err := os.ReadFile("testdata/" + n)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTokenCachedAndRefreshed(t *testing.T) {
	h := func(w http.ResponseWriter, r *http.Request, _ gqlReq) {
		io.WriteString(w, `{"data":{"tenants":[{"id":"a1","name":"Acme Test"}]}}`)
	}
	f := &fake{t: t, handler: h}
	c := f.start()
	for i := 0; i < 3; i++ {
		if _, err := c.Accounts(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if f.tokens.Load() != 1 {
		t.Fatalf("tokens=%d want 1", f.tokens.Load())
	}
	// A token expiring within 60 s is refreshed each call.
	f2 := &fake{t: t, expires: 30, handler: h}
	c2 := f2.start()
	c2.Accounts(context.Background())
	c2.Accounts(context.Background())
	if f2.tokens.Load() != 2 {
		t.Fatalf("short-lived tokens=%d want 2", f2.tokens.Load())
	}
}

func TestAccounts(t *testing.T) {
	f := &fake{t: t, handler: func(w http.ResponseWriter, r *http.Request, _ gqlReq) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		io.WriteString(w, `{"data":{"tenants":[{"id":"a1","name":"Acme Test"},{"id":"a2"}]}}`)
	}}
	got, err := f.start().Accounts(context.Background())
	if err != nil || len(got) != 2 || got[0] != (Account{"a1", "Acme Test"}) || got[1] != (Account{ID: "a2"}) {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestListTeamsRoomsPagingAndFilter(t *testing.T) {
	pages := []string{"page1.json", "page2.json", "page3.json"}
	var tokens []any
	f := &fake{t: t, handler: func(w http.ResponseWriter, r *http.Request, req gqlReq) {
		if req.Variables["tenantId"] != "acct1" {
			t.Errorf("tenantId = %v", req.Variables["tenantId"])
		}
		p := req.Variables["params"].(map[string]any)
		if p["pageSize"].(float64) != pageSize {
			t.Errorf("pageSize = %v", p["pageSize"])
		}
		tokens = append(tokens, p["nextToken"])
		io.WriteString(w, file(t, pages[len(tokens)-1]))
	}}
	got, err := f.start().ListTeamsRooms(context.Background(), "acct1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 3 || tokens[0] != nil || tokens[1] != "t2" || tokens[2] != "t3" {
		t.Fatalf("tokens %v", tokens)
	}
	var ids []string
	for _, d := range got {
		ids = append(ids, d.ID)
	}
	if strings.Join(ids, ",") != "d1,d3,d4" {
		t.Fatalf("ids %v (non-Teams d2 must be dropped)", ids)
	}
	d := got[0]
	if d.RoomName != "Room 1" || d.SiteName != "Site A" || d.Model != "X" || d.AppVersion != "5.0" ||
		d.TeamsHealth != "Healthy" || d.CallStatus != "Idle" || d.PresenceAvailability != "Available" ||
		d.PeripheralsLinked == nil || !*d.PeripheralsLinked || d.LastDetected.IsZero() || len(d.Tags) != 1 {
		t.Fatalf("d1 = %+v", d)
	}
	if got[2].PeripheralsLinked == nil || *got[2].PeripheralsLinked {
		t.Fatal("d4 peripherals should be false")
	}
}

func TestMissingOptionalFieldsAreZero(t *testing.T) {
	f := &fake{t: t, handler: func(w http.ResponseWriter, r *http.Request, _ gqlReq) {
		io.WriteString(w, file(t, "minimal.json"))
	}}
	got, err := f.start().ListTeamsRooms(context.Background(), "a")
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	d := got[0]
	if d.PeripheralsLinked != nil || d.CallStatus != "" || !d.LastDetected.IsZero() || d.RoomName != "" || d.Tags != nil {
		t.Fatalf("%+v", d)
	}
}

func TestOptionalFieldsDroppedWhenSchemaRejects(t *testing.T) {
	for _, status := range []int{200, 400} {
		f := &fake{t: t, handler: func(w http.ResponseWriter, r *http.Request, req gqlReq) {
			if strings.Contains(req.Query, "callStatus") {
				w.WriteHeader(status)
				io.WriteString(w, `{"errors":[{"message":"Cannot query field \"callStatus\" on type \"Device\".","extensions":{"code":"GRAPHQL_VALIDATION_FAILED"}}]}`)
				return
			}
			io.WriteString(w, `{"data":{"tenant":{"inventory":{"deviceSearch":{"pageInfo":{"hasNextPage":false},"edges":[{"node":{"id":"d1","name":"R"}},{"node":{"id":"d2","name":"S"}}]}}}}}`)
		}}
		c := f.start()
		if c.Degraded() {
			t.Fatal("degraded before any query")
		}
		got, err := c.ListTeamsRooms(context.Background(), "a")
		if err != nil {
			t.Fatal(err)
		}
		// No Teams fields available: all devices returned, not an empty list.
		if len(got) != 2 || !c.Degraded() {
			t.Fatalf("status %d: got %d devices, degraded=%v", status, len(got), c.Degraded())
		}
		if f.gqlCalls.Load() != 2 {
			t.Fatalf("calls=%d want 2", f.gqlCalls.Load())
		}
		// The choice is remembered: one call next time.
		c.ListTeamsRooms(context.Background(), "a")
		if f.gqlCalls.Load() != 3 {
			t.Fatalf("calls=%d want 3", f.gqlCalls.Load())
		}
	}
}

func TestAccountsFallbackDoesNotAffectInventory(t *testing.T) {
	f := &fake{t: t, handler: func(w http.ResponseWriter, r *http.Request, req gqlReq) {
		if strings.Contains(req.Query, "tenants { id name }") {
			io.WriteString(w, `{"errors":[{"message":"Cannot query field \"name\" on type \"Tenant\"."}]}`)
			return
		}
		io.WriteString(w, `{"data":{"tenants":[{"id":"a1"}]}}`)
	}}
	c := f.start()
	if _, err := c.Accounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !c.Degraded() {
		t.Fatal("accounts fallback should show as degraded")
	}
	var seenFull bool
	f.handler = func(w http.ResponseWriter, r *http.Request, req gqlReq) {
		seenFull = strings.Contains(req.Query, "callStatus")
		io.WriteString(w, file(t, "minimal.json"))
	}
	got, err := c.ListTeamsRooms(context.Background(), "a")
	if err != nil || len(got) != 1 || !seenFull {
		t.Fatalf("inventory must still request optional fields: %v %v full=%v", got, err, seenFull)
	}
}

func TestFlagNotSetWhenFallbackFails(t *testing.T) {
	f := &fake{t: t, handler: func(w http.ResponseWriter, r *http.Request, req gqlReq) {
		if strings.Contains(req.Query, "callStatus") {
			io.WriteString(w, `{"errors":[{"message":"Cannot query field \"callStatus\""}]}`)
			return
		}
		w.WriteHeader(500)
	}}
	c := f.start()
	if _, err := c.ListTeamsRooms(context.Background(), "a"); err == nil {
		t.Fatal("want error")
	}
	if c.Degraded() {
		t.Fatal("flag set although fallback failed")
	}
}

func TestPagingGuards(t *testing.T) {
	f := &fake{t: t, handler: func(w http.ResponseWriter, r *http.Request, _ gqlReq) {
		io.WriteString(w, `{"data":{"tenant":{"inventory":{"deviceSearch":{"pageInfo":{"nextToken":"same","hasNextPage":true},"edges":[]}}}}}`)
	}}
	if _, err := f.start().ListTeamsRooms(context.Background(), "a"); err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Fatalf("err %v", err)
	}
	var n atomic.Int32
	f2 := &fake{t: t, handler: func(w http.ResponseWriter, r *http.Request, _ gqlReq) {
		fmt.Fprintf(w, `{"data":{"tenant":{"inventory":{"deviceSearch":{"pageInfo":{"nextToken":"t%d","hasNextPage":true},"edges":[]}}}}}`, n.Add(1))
	}}
	if _, err := f2.start().ListTeamsRooms(context.Background(), "a"); err == nil || !strings.Contains(err.Error(), "pages") {
		t.Fatalf("err %v", err)
	}
	if n.Load() != 200 {
		t.Fatalf("pages=%d", n.Load())
	}
}

func TestGraphQL403IsUnauthorized(t *testing.T) {
	f := &fake{t: t, handler: func(w http.ResponseWriter, r *http.Request, _ gqlReq) { w.WriteHeader(403) }}
	if _, err := f.start().Accounts(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err %v", err)
	}
}

func TestBadCredentialsUnauthorizedNoSecret(t *testing.T) {
	f := &fake{t: t, authBad: true, handler: func(http.ResponseWriter, *http.Request, gqlReq) {}}
	_, err := f.start().Accounts(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), testSecret) {
		t.Fatal("secret leaked")
	}
}

func TestGraphQL401RetriesOnceWithFreshToken(t *testing.T) {
	var n atomic.Int32
	f := &fake{t: t, handler: func(w http.ResponseWriter, r *http.Request, _ gqlReq) {
		if n.Add(1) == 1 {
			w.WriteHeader(401)
			return
		}
		io.WriteString(w, `{"data":{"tenants":[]}}`)
	}}
	if _, err := f.start().Accounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.tokens.Load() != 2 {
		t.Fatalf("tokens=%d", f.tokens.Load())
	}
	f2 := &fake{t: t, handler: func(w http.ResponseWriter, r *http.Request, _ gqlReq) { w.WriteHeader(401) }}
	_, err := f2.start().Accounts(context.Background())
	if !errors.Is(err, ErrUnauthorized) || strings.Contains(err.Error(), testSecret) {
		t.Fatalf("err %v", err)
	}
}

func TestRateLimited(t *testing.T) {
	f := &fake{t: t, handler: func(w http.ResponseWriter, r *http.Request, _ gqlReq) {
		w.Header().Set("ratelimit-reset", "7")
		w.WriteHeader(429)
	}}
	_, err := f.start().Accounts(context.Background())
	var rl *RateLimitError
	if !errors.Is(err, ErrRateLimited) || !errors.As(err, &rl) || rl.RetryAfter.Seconds() != 7 {
		t.Fatalf("err %v", err)
	}
}

func TestAddAndRemoveTag(t *testing.T) {
	var seen []gqlReq
	have := false // whether the tag exists in the account
	f := &fake{t: t, handler: func(w http.ResponseWriter, r *http.Request, req gqlReq) {
		seen = append(seen, req)
		switch {
		case strings.Contains(req.Query, "lensTags(filter"):
			if have {
				io.WriteString(w, `{"data":{"tenant":{"lensTags":[{"id":"t0","name":"marcus:abcd"},{"id":"t1","name":"marcus:abc"}]}}}`)
			} else {
				io.WriteString(w, `{"data":{"tenant":{"lensTags":[{"id":"t0","name":"marcus:abcd"}]}}}`)
			}
		case strings.Contains(req.Query, "createTag"):
			have = true
			io.WriteString(w, `{"data":{"createTag":{"id":"t1"}}}`)
		case strings.Contains(req.Query, "addLensTagToDevice"):
			io.WriteString(w, `{"data":{"addLensTagToDevice":[{"deviceId":"dev1","success":true}]}}`)
		default:
			io.WriteString(w, `{"data":{"removeLensTagFromDevice":[{"deviceId":"dev1","success":true}]}}`)
		}
	}}
	c := f.start()
	id, err := c.FindTag(context.Background(), "acc1", "marcus:abc")
	if err != nil || id != "" {
		t.Fatalf("id %q err %v", id, err)
	}
	if id, err = c.CreateTag(context.Background(), "acc1", "marcus:abc"); err != nil || id != "t1" {
		t.Fatalf("id %q err %v", id, err)
	}
	if err := c.AssignTag(context.Background(), id, "dev1"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveTag(context.Background(), "acc1", "dev1", "marcus:abc"); err != nil {
		t.Fatal(err)
	}
	var add, rem *gqlReq
	creates := 0
	for i := range seen {
		switch {
		case strings.Contains(seen[i].Query, "createTag"):
			creates++
		case strings.Contains(seen[i].Query, "addLensTagToDevice"):
			add = &seen[i]
		case strings.Contains(seen[i].Query, "removeLensTagFromDevice"):
			rem = &seen[i]
		}
	}
	if creates != 1 || add == nil || rem == nil {
		t.Fatalf("creates=%d add=%v rem=%v", creates, add, rem)
	}
	if ids, _ := add.Variables["tagIds"].([]any); len(ids) != 1 || ids[0] != "t1" { // exact-name match, not t0
		t.Fatalf("%+v", add.Variables)
	}
	if rem.Variables["tagId"] != "t1" {
		t.Fatalf("%+v", rem.Variables)
	}
}

func TestRemoveTagMissingIsNoop(t *testing.T) {
	n := 0
	f := &fake{t: t, handler: func(w http.ResponseWriter, r *http.Request, req gqlReq) {
		n++
		io.WriteString(w, `{"data":{"tenant":{"lensTags":[]}}}`)
	}}
	if err := f.start().RemoveTag(context.Background(), "acc1", "dev1", "marcus:abc"); err != nil || n != 1 {
		t.Fatalf("err %v n %d", err, n)
	}
}

func TestDevicesBatches(t *testing.T) {
	var sizes []int
	f := &fake{t: t, handler: func(w http.ResponseWriter, r *http.Request, req gqlReq) {
		p := req.Variables["params"].(map[string]any)
		flt := p["filter"].(map[string]any)
		if or, ok := flt["OR"].([]any); ok {
			sizes = append(sizes, len(or))
		} else {
			sizes = append(sizes, 1) // single id: plain eq filter
		}
		io.WriteString(w, `{"data":{"tenant":{"inventory":{"deviceSearch":{"pageInfo":{"hasNextPage":false},"edges":[{"node":{"id":"x"}}]}}}}}`)
	}}
	ids := make([]string, 2*batchSize+1)
	for i := range ids {
		ids[i] = "id"
	}
	got, err := f.start().Devices(context.Background(), "a", ids)
	if err != nil || len(sizes) != 3 || sizes[0] != batchSize || sizes[1] != batchSize || sizes[2] != 1 || len(got) != 3 {
		t.Fatalf("sizes %v got %d err %v", sizes, len(got), err)
	}
}

func TestAuthorizationErrorIsForbidden(t *testing.T) {
	f := &fake{t: t, handler: func(w http.ResponseWriter, r *http.Request, req gqlReq) {
		w.WriteHeader(403)
		io.WriteString(w, `{"errors":[{"message":"Authorization failed","extensions":{"code":"AUTHORIZATION_ERROR"}}]}`)
	}}
	err := f.start().AssignTag(context.Background(), "t1", "dev1")
	if !errors.Is(err, ErrForbidden) || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err %v", err)
	}
}

func TestNoTagFoundIsErrTagNotFound(t *testing.T) {
	f := &fake{t: t, handler: func(w http.ResponseWriter, r *http.Request, req gqlReq) {
		io.WriteString(w, `{"errors":[{"message":"No Tag found","extensions":{"code":"DOWNSTREAM_SERVICE_ERROR"}}],"data":null}`)
	}}
	if err := f.start().AssignTag(context.Background(), "t1", "dev1"); !errors.Is(err, ErrTagNotFound) {
		t.Fatalf("err %v", err)
	}
}

// deleteFake answers deleteTag with 403 for the first fails calls, then with
// the given final body; seen counts deleteTag calls and removes.
func deleteFake(t *testing.T, fails int, final string, deletes *int, removes *int) *Client {
	f := &fake{t: t, handler: func(w http.ResponseWriter, r *http.Request, req gqlReq) {
		switch {
		case strings.Contains(req.Query, "lensTags(filter"):
			io.WriteString(w, `{"data":{"tenant":{"lensTags":[{"id":"t1","name":"marcus:abc"}]}}}`)
		case strings.Contains(req.Query, "deleteTag"):
			*deletes++
			if *deletes <= fails {
				w.WriteHeader(403)
				io.WriteString(w, `{"errors":[{"message":"Authorization failed","extensions":{"code":"AUTHORIZATION_ERROR"}}]}`)
				return
			}
			io.WriteString(w, final)
		default:
			*removes++
			io.WriteString(w, `{"data":{"removeLensTagFromDevice":[{"deviceId":"dev1","success":true}]}}`)
		}
	}}
	return f.start()
}

func TestDeleteTagRetriesForbidden(t *testing.T) {
	old := TagRetryDelay
	TagRetryDelay = time.Millisecond
	defer func() { TagRetryDelay = old }()
	var del, rem int
	if err := deleteFake(t, 2, `{"data":{"deleteTag":{"success":true}}}`, &del, &rem).DeleteTag(context.Background(), "t1"); err != nil || del != 3 {
		t.Fatalf("err %v deletes %d", err, del)
	}
	// Forever forbidden: gives up with ErrForbidden after the attempts.
	del = 0
	if err := deleteFake(t, 99, "", &del, &rem).DeleteTag(context.Background(), "t1"); !errors.Is(err, ErrForbidden) || del != deleteTagAttempts {
		t.Fatalf("err %v deletes %d", err, del)
	}
	// Already gone counts as deleted.
	del = 0
	if err := deleteFake(t, 0, `{"errors":[{"message":"No Tag found"}],"data":null}`, &del, &rem).DeleteTag(context.Background(), "t1"); err != nil || del != 1 {
		t.Fatalf("err %v deletes %d", err, del)
	}
}

func TestRemoveTagUnassignsThenIgnoresFinalDeleteFailure(t *testing.T) {
	old := TagRetryDelay
	TagRetryDelay = time.Millisecond
	defer func() { TagRetryDelay = old }()
	var del, rem int
	if err := deleteFake(t, 99, "", &del, &rem).RemoveTag(context.Background(), "acc1", "dev1", "marcus:abc"); err != nil || rem != 1 || del != deleteTagAttempts {
		t.Fatalf("err %v removes %d deletes %d", err, rem, del)
	}
}
