package integrations

// Poller tagging of Lens devices against a fake Lens that mimics the real
// behaviour: assigning a fresh tag answers 403 until it has aged.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/integrations/polylens"
)

type tagLens struct {
	mu       sync.Mutex
	calls    []string
	listed   map[string]string // tag id -> name (what the listing shows)
	ghost    map[string]bool   // listed but "No Tag found" on assign
	fail500  bool             // assigns answer a plain GraphQL error
	forbid   int               // assigns to answer 403 before succeeding; -1 forever
	nextID   int
	srv      *httptest.Server
	assigned []string
}

func newTagLens(t *testing.T) *tagLens {
	old := polylens.TagRetryDelay
	polylens.TagRetryDelay = time.Millisecond
	t.Cleanup(func() { polylens.TagRetryDelay = old })
	f := &tagLens{listed: map[string]string{}, ghost: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	})
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case strings.Contains(req.Query, "lensTags(filter"):
			name, _ := req.Variables["name"].(string)
			out := ""
			for id, n := range f.listed {
				if n == name {
					out = `{"id":"` + id + `","name":"` + n + `"}`
				}
			}
			io.WriteString(w, `{"data":{"tenant":{"lensTags":[`+out+`]}}}`)
		case strings.Contains(req.Query, "createTag"):
			name, _ := req.Variables["name"].(string)
			f.nextID++
			id := "tag-" + string(rune('a'+f.nextID))
			f.listed[id] = name
			f.calls = append(f.calls, "create|"+name)
			io.WriteString(w, `{"data":{"createTag":{"id":"`+id+`"}}}`)
		case strings.Contains(req.Query, "addLensTagToDevice"):
			ids, _ := req.Variables["tagIds"].([]any)
			devs, _ := req.Variables["deviceIds"].([]any)
			id := ids[0].(string)
			f.calls = append(f.calls, "add|"+f.listed[id])
			switch {
			case f.ghost[id]:
				io.WriteString(w, `{"errors":[{"message":"No Tag found","extensions":{"code":"DOWNSTREAM_SERVICE_ERROR"}}],"data":null}`)
			case f.fail500:
				io.WriteString(w, `{"errors":[{"message":"boom","extensions":{"code":"INTERNAL"}}],"data":null}`)
			case f.forbid != 0:
				if f.forbid > 0 {
					f.forbid--
				}
				w.WriteHeader(403)
				io.WriteString(w, `{"errors":[{"message":"Authorization failed","extensions":{"code":"AUTHORIZATION_ERROR"}}]}`)
			default:
				f.assigned = append(f.assigned, devs[0].(string))
				io.WriteString(w, `{"data":{"addLensTagToDevice":[{"deviceId":"`+devs[0].(string)+`","success":true}]}}`)
			}
		case strings.Contains(req.Query, "deleteTag"):
			id, _ := req.Variables["id"].(string)
			f.calls = append(f.calls, "delete|"+f.listed[id])
			delete(f.listed, id)
			io.WriteString(w, `{"data":{"deleteTag":{"success":true}}}`)
		default:
			io.WriteString(w, deviceJSON)
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *tagLens) tagCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// pendingFixture is a tenant with one linked device whose tag is pending.
func (e *env) pendingFixture(account string, tagging bool, requestedAgo string) (tenantFixture, string) {
	f := e.tenant(account, "ext-1")
	e.exec(`UPDATE integrations SET allow_tagging = $2 WHERE id = $1`, f.integ, tagging)
	dev := f.devs["ext-1"]
	if tagging {
		e.exec(`UPDATE integration_devices SET tag_state = 'pending', tag_requested_at = now() - $2::interval WHERE device_id = $1`, dev, requestedAgo)
	}
	return f, dev
}

func (e *env) tagPoller(lens *tagLens, cust string) *Poller {
	p := NewPoller(e.st.AdminPool(), e.cipher, nil, nil, 0, e.quiet).SetLensBaseURLs(lens.srv.URL+"/token", lens.srv.URL+"/graphql")
	p.onlyCustomers = []string{cust}
	return p
}

func (e *env) tagRow(dev string) string {
	return e.q(`SELECT tag_state || '|' || COALESCE(tag_error,'') || '|' || (tag_attempted_at IS NOT NULL)::text FROM integration_devices WHERE device_id = $1`, dev)
}

func TestTagPendingRetriesAfterForbidden(t *testing.T) {
	e := newEnv(t)
	lens := newTagLens(t)
	lens.forbid = 1
	f, dev := e.pendingFixture("acct-tag-retry", true, "1 second")
	p := e.tagPoller(lens, f.cust)
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.tagRow(dev); got != "pending||true" {
		t.Fatalf("after 403: %s", got)
	}
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.tagRow(dev); got != "tagged||true" {
		t.Fatalf("after retry: %s", got)
	}
	if want := []string{"create|marcus:" + dev, "add|marcus:" + dev, "add|marcus:" + dev}; strings.Join(lens.tagCalls(), ",") != strings.Join(want, ",") {
		t.Fatalf("calls %v want %v", lens.tagCalls(), want)
	}
	// Tagged links are not touched again.
	n := len(lens.tagCalls())
	_ = p.PollOnce(context.Background())
	if len(lens.tagCalls()) != n {
		t.Fatalf("tagged link retagged: %v", lens.tagCalls())
	}
}

func TestTagPendingGivesUpAfterThirtyMinutes(t *testing.T) {
	e := newEnv(t)
	lens := newTagLens(t)
	lens.forbid = -1
	f, dev := e.pendingFixture("acct-tag-giveup", true, "31 minutes")
	// It has been tried before: the window runs from the first attempt.
	e.exec(`UPDATE integration_devices SET tag_attempted_at = now() - interval '1 minute' WHERE device_id = $1`, dev)
	if err := e.tagPoller(lens, f.cust).PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.tagRow(dev); got != "failed|Lens role can't tag devices|true" {
		t.Fatalf("got %s", got)
	}
	// The per-device tag created for it is deleted at give-up.
	if c := lens.tagCalls(); c[len(c)-1] != "delete|marcus:"+dev {
		t.Fatalf("tag not cleaned up: %v", c)
	}
	// A device under the give-up age stays pending on the same error.
	f2, dev2 := e.pendingFixture("acct-tag-young", true, "2 minutes")
	if err := e.tagPoller(lens, f2.cust).PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.tagRow(dev2); got != "pending||true" {
		t.Fatalf("young: %s", got)
	}
}

// A link that waited out a poller outage (requested long ago, never tried)
// gets a full give-up window from its first attempt instead of failing at once.
func TestTagPendingWindowStartsAtFirstAttempt(t *testing.T) {
	e := newEnv(t)
	lens := newTagLens(t)
	lens.forbid = -1
	f, dev := e.pendingFixture("acct-tag-outage", true, "30 minutes")
	if err := e.tagPoller(lens, f.cust).PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.tagRow(dev); got != "pending||true" {
		t.Fatalf("first attempt after an outage gave up: %s", got)
	}
	if fresh := e.q(`SELECT (tag_requested_at > now() - interval '1 minute')::text FROM integration_devices WHERE device_id = $1`, dev); fresh != "true" {
		t.Fatal("window did not restart at the first attempt")
	}
}

func TestTagPendingGhostTagIsRecreated(t *testing.T) {
	e := newEnv(t)
	lens := newTagLens(t)
	f, dev := e.pendingFixture("acct-tag-ghost", true, "2 minutes")
	// Our stored tag id no longer exists in Lens.
	e.exec(`UPDATE integration_devices SET lens_tag_id = 'ghost-1' WHERE device_id = $1`, dev)
	lens.listed["ghost-1"] = "marcus:" + dev
	lens.ghost["ghost-1"] = true
	if err := e.tagPoller(lens, f.cust).PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.tagRow(dev); got != "tagged||true" {
		t.Fatalf("got %s", got)
	}
	if got := e.q(`SELECT lens_tag_id FROM integration_devices WHERE device_id = $1`, dev); got == "ghost-1" || got == "" {
		t.Fatalf("stored tag id %q", got)
	}
	want := "add|marcus:" + dev + ",create|marcus:" + dev + ",add|marcus:" + dev
	if strings.Join(lens.tagCalls(), ",") != want {
		t.Fatalf("calls %v", lens.tagCalls())
	}
}

func TestTagPendingNeverTouchedWhenTaggingOff(t *testing.T) {
	e := newEnv(t)
	lens := newTagLens(t)
	f, dev := e.pendingFixture("acct-tag-off", false, "")
	if err := e.tagPoller(lens, f.cust).PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(lens.tagCalls()) != 0 || !strings.HasPrefix(e.tagRow(dev), "none|") {
		t.Fatalf("calls %v row %s", lens.tagCalls(), e.tagRow(dev))
	}
	// A request left pending when tagging is switched off is cleared, not served.
	e.exec(`UPDATE integration_devices SET tag_state = 'pending', tag_requested_at = now() WHERE device_id = $1`, dev)
	if err := e.tagPoller(lens, f.cust).PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(lens.tagCalls()) != 0 || !strings.HasPrefix(e.tagRow(dev), "none|") {
		t.Fatalf("calls %v row %s", lens.tagCalls(), e.tagRow(dev))
	}
}

func TestTagPendingOtherErrorKeepsMessageAndTag(t *testing.T) {
	e := newEnv(t)
	lens := newTagLens(t)
	lens.fail500 = true
	f, dev := e.pendingFixture("acct-tag-other", true, "1 minute")
	p := e.tagPoller(lens, f.cust)
	for i := 0; i < 2; i++ {
		if err := p.PollOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.tagRow(dev); got != "pending|Poly Lens did not accept the tag|true" {
		t.Fatalf("got %s", got)
	}
	// The tag is created once and kept between attempts.
	creates := 0
	for _, c := range lens.tagCalls() {
		if strings.HasPrefix(c, "create|") {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("creates %d: %v", creates, lens.tagCalls())
	}
	e.exec(`UPDATE integration_devices SET tag_requested_at = now() - interval '31 minutes' WHERE device_id = $1`, dev)
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.tagRow(dev); got != "failed|Poly Lens did not accept the tag|true" {
		t.Fatalf("after give-up: %s", got)
	}
}

func TestTagPendingForbiddenCreatesTagOnce(t *testing.T) {
	e := newEnv(t)
	lens := newTagLens(t)
	lens.forbid = -1
	f, dev := e.pendingFixture("acct-tag-once", true, "2 minutes")
	p := e.tagPoller(lens, f.cust)
	for i := 0; i < 4; i++ {
		if err := p.PollOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	creates := 0
	for _, c := range lens.tagCalls() {
		if strings.HasPrefix(c, "create|") {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("creates %d: %v", creates, lens.tagCalls())
	}
	if got := e.tagRow(dev); got != "pending||true" {
		t.Fatalf("got %s", got)
	}
}

func TestTagPendingSwitchedOffDeletesStoredTag(t *testing.T) {
	e := newEnv(t)
	lens := newTagLens(t)
	f, dev := e.pendingFixture("acct-tag-off2", false, "")
	e.exec(`UPDATE integration_devices SET tag_state = 'pending', tag_requested_at = now(), lens_tag_id = 'tag-x' WHERE device_id = $1`, dev)
	lens.listed["tag-x"] = "marcus:" + dev
	if err := e.tagPoller(lens, f.cust).PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c := lens.tagCalls(); len(c) != 1 || c[0] != "delete|marcus:"+dev {
		t.Fatalf("calls %v", c)
	}
	if got := e.tagRow(dev); !strings.HasPrefix(got, "none|") {
		t.Fatalf("got %s", got)
	}
}
