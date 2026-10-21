// Package logship sends the Collector's own log to the cloud. A slog
// handler copies every Info-and-above record into a bounded buffer, and
// the cloud client takes the buffer with each push, so log lines travel
// (and are spooled through outages) with the telemetry.
//
// Nothing secret should be logged in the first place; as a second line of
// defence, attributes that look like credentials are removed and usernames
// are masked before a record is buffered.
package logship

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Record is one log line as sent to the cloud.
type Record struct {
	TS    time.Time      `json:"ts"`
	Level string         `json:"level"`
	Msg   string         `json:"msg"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// Limits. A push every 30 seconds normally carries a handful of lines;
// these only bite in a log storm, where the oldest lines go first.
const (
	maxBuffered = 5000
	maxMsgLen   = 2000
	maxAttrLen  = 1000
)

// Buffer holds records until the cloud client takes them.
type Buffer struct {
	mu      sync.Mutex
	recs    []Record
	dropped int
}

// Default is the Collector's buffer: the handler in main fills it and the
// cloud client drains it.
var Default = &Buffer{}

func (b *Buffer) add(r Record) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.recs) >= maxBuffered {
		n := len(b.recs) - maxBuffered + 1
		b.recs = append(b.recs[:0], b.recs[n:]...)
		b.dropped += n
	}
	b.recs = append(b.recs, r)
}

// Take returns the buffered records and empties the buffer. If any were
// dropped since the last Take, a warning record saying how many leads.
func (b *Buffer) Take() []Record {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.recs) == 0 && b.dropped == 0 {
		return nil
	}
	out := b.recs
	if b.dropped > 0 {
		note := Record{TS: time.Now().UTC(), Level: "WARN",
			Msg: fmt.Sprintf("%d log lines were not sent: too many in one push interval", b.dropped)}
		out = append([]Record{note}, out...)
	}
	b.recs, b.dropped = nil, 0
	return out
}

// Handler passes records to next and copies those at Info or above into
// buf. It reports itself enabled at Info even when next is set quieter
// (say Warn), so the cloud still gets Info lines.
type Handler struct {
	next   slog.Handler
	buf    *Buffer
	attrs  []groupedAttr // from With, each under the groups open at the time
	groups []string
}

type groupedAttr struct {
	prefix string
	attr   slog.Attr
}

func NewHandler(next slog.Handler, buf *Buffer) *Handler {
	return &Handler{next: next, buf: buf}
}

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= slog.LevelInfo || h.next.Enabled(ctx, l)
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	var err error
	if h.next.Enabled(ctx, r.Level) {
		err = h.next.Handle(ctx, r)
	}
	if r.Level < slog.LevelInfo {
		return err
	}
	attrs := map[string]any{}
	prefix := strings.Join(h.groups, ".")
	for _, a := range h.attrs {
		addAttr(attrs, a.prefix, a.attr)
	}
	r.Attrs(func(a slog.Attr) bool {
		addAttr(attrs, prefix, a)
		return true
	})
	if len(attrs) == 0 {
		attrs = nil
	}
	h.buf.add(Record{
		TS:    r.Time.UTC(),
		Level: r.Level.String(),
		Msg:   truncate(r.Message, maxMsgLen),
		Attrs: attrs,
	})
	return err
}

func (h *Handler) WithAttrs(as []slog.Attr) slog.Handler {
	c := *h
	c.next = h.next.WithAttrs(as)
	prefix := strings.Join(h.groups, ".")
	c.attrs = append([]groupedAttr{}, h.attrs...)
	for _, a := range as {
		c.attrs = append(c.attrs, groupedAttr{prefix, a})
	}
	return &c
}

func (h *Handler) WithGroup(name string) slog.Handler {
	c := *h
	c.next = h.next.WithGroup(name)
	c.groups = append(append([]string{}, h.groups...), name)
	return &c
}

func addAttr(m map[string]any, prefix string, a slog.Attr) {
	v := a.Value.Resolve()
	key := a.Key
	if prefix != "" {
		key = prefix + "." + key
	}
	if v.Kind() == slog.KindGroup {
		for _, g := range v.Group() {
			addAttr(m, key, g)
		}
		return
	}
	if key == "" {
		return
	}
	switch kind := sensitivity(a.Key); kind {
	case secretAttr:
		m[key] = "[removed]"
		return
	case userAttr:
		m[key] = maskUser(fmt.Sprint(v.Any()))
		return
	}
	switch v.Kind() {
	case slog.KindString:
		m[key] = truncate(v.String(), maxAttrLen)
	case slog.KindInt64, slog.KindUint64, slog.KindFloat64, slog.KindBool:
		m[key] = v.Any()
	case slog.KindDuration:
		m[key] = v.Duration().String()
	case slog.KindTime:
		m[key] = v.Time().UTC().Format(time.RFC3339)
	default:
		m[key] = truncate(fmt.Sprint(v.Any()), maxAttrLen)
	}
}

type sens int

const (
	plainAttr sens = iota
	secretAttr
	userAttr
)

// sensitivity classifies an attribute by its key.
func sensitivity(key string) sens {
	k := strings.ToLower(key)
	for _, s := range []string{"password", "passwd", "secret", "token", "psk", "apikey", "api_key", "credential", "hmac", "authorization"} {
		if strings.Contains(k, s) {
			return secretAttr
		}
	}
	if k == "user" || k == "username" || k == "login" || strings.HasSuffix(k, "_user") || strings.HasSuffix(k, "_username") {
		return userAttr
	}
	return plainAttr
}

// maskUser keeps the first character so support can tell accounts apart
// ("a***" vs "s***") without the full name leaving site.
func maskUser(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	return string(r[0]) + "***"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
