package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Collector logs: what each Collector sends of its own log (Info and
// above) with every push; see migration 0063. Kept 30 days.

type collectorLogLine struct {
	ID    int64           `json:"id"`
	TS    time.Time       `json:"ts"`
	Level string          `json:"level"`
	Msg   string          `json:"msg"`
	Attrs json.RawMessage `json:"attrs,omitempty"`
}

// logLevelsFrom is every level at or above the one asked for.
var logLevelsFrom = map[string][]string{
	"debug": {"DEBUG", "INFO", "WARN", "ERROR"},
	"info":  {"INFO", "WARN", "ERROR"},
	"warn":  {"WARN", "ERROR"},
	"error": {"ERROR"},
}

const maxLogDownload = 50_000

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// ListCollectorLogs — GET /api/v1/collectors/{id}/logs
//
//	level=info|warn|error  lowest level shown (default info)
//	q=<text>               lines whose message or details contain it
//	since=<RFC3339>        lines from then on (by the Collector's clock)
//	before=<id>            the page older than this line (from next_before)
//	limit=<n>              lines per page, newest first (default 200, max 1000)
//	format=text            the matching lines as a text file instead,
//	                       oldest first, up to 50,000
func (h *Handler) ListCollectorLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !collectorIDRE.MatchString(id) {
		writeErr(w, http.StatusBadRequest, "invalid collector id")
		return
	}
	qs := r.URL.Query()
	level := strings.ToLower(qs.Get("level"))
	if level == "" {
		level = "info"
	}
	levels, ok := logLevelsFrom[level]
	if !ok {
		writeErr(w, http.StatusBadRequest, "level must be debug, info, warn or error")
		return
	}
	download := qs.Get("format") == "text"
	limit := queryInt(r, "limit", 200, 1000)
	if download {
		limit = maxLogDownload
	}

	conds := []string{"collector_id = $1", "level = ANY($2)"}
	args := []any{id, levels}
	if q := strings.TrimSpace(qs.Get("q")); q != "" {
		args = append(args, "%"+likeEscaper.Replace(q)+"%")
		n := strconv.Itoa(len(args))
		conds = append(conds, "(msg ILIKE $"+n+" OR attrs::text ILIKE $"+n+")")
	}
	if s := qs.Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "since must be an RFC 3339 time")
			return
		}
		args = append(args, t)
		conds = append(conds, "ts >= $"+strconv.Itoa(len(args)))
	}
	if s := qs.Get("before"); s != "" {
		b, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "before must be a line id")
			return
		}
		args = append(args, b)
		conds = append(conds, "id < $"+strconv.Itoa(len(args)))
	}
	args = append(args, limit+1)
	sql := `SELECT id, ts, level, msg, attrs FROM collector_logs WHERE ` +
		strings.Join(conds, " AND ") + ` ORDER BY id DESC LIMIT $` + strconv.Itoa(len(args))

	var name string
	notFound := false
	lines := []collectorLogLine{}
	ok = h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT name FROM collectors WHERE id = $1`, id).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			notFound = true
			return nil
		} else if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l collectorLogLine
			var attrs []byte
			if err := rows.Scan(&l.ID, &l.TS, &l.Level, &l.Msg, &attrs); err != nil {
				return err
			}
			if len(attrs) > 0 {
				l.Attrs = attrs
			}
			lines = append(lines, l)
		}
		return rows.Err()
	})
	if !ok {
		return
	}
	if notFound {
		writeErr(w, http.StatusNotFound, "collector not found")
		return
	}

	var next *int64
	if len(lines) > limit {
		lines = lines[:limit]
		last := lines[len(lines)-1].ID
		next = &last
	}

	if download {
		writeCollectorLogText(w, name, lines)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"collector":   map[string]string{"id": id, "name": name},
		"logs":        lines,
		"next_before": next,
	})
}

var unsafeFilename = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// writeCollectorLogText writes lines oldest first, one per line:
// "2026-10-21T14:03:12Z WARN  device unreachable device=dsp-1 error=timeout".
func writeCollectorLogText(w http.ResponseWriter, collector string, lines []collectorLogLine) {
	file := strings.Trim(unsafeFilename.ReplaceAllString(collector, "-"), "-")
	if file == "" {
		file = "collector"
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s-logs-%s.txt"`, file, time.Now().UTC().Format("2006-01-02")))
	var sb strings.Builder
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		fmt.Fprintf(&sb, "%s %-5s %s", l.TS.UTC().Format(time.RFC3339), l.Level, l.Msg)
		if len(l.Attrs) > 0 {
			var m map[string]any
			if json.Unmarshal(l.Attrs, &m) == nil {
				keys := make([]string, 0, len(m))
				for k := range m {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					fmt.Fprintf(&sb, " %s=%v", k, m[k])
				}
			}
		}
		sb.WriteByte('\n')
	}
	_, _ = w.Write([]byte(sb.String()))
}

var collectorIDRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
