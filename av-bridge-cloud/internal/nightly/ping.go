package nightly

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/commands"
)

// pingCommand is the reserved command that asks a Collector to ping a
// device's address, whatever adapter the device uses. Mirrors the
// Collector's cloudpoll.CommandPingName.
const pingCommand = "_ping"

// A ping is three echoes a second apart; this leaves room for a Collector
// that's busy, without holding a step for long when it's offline.
var (
	pingTimeout = 30 * time.Second // var so tests can shorten it
	pingTick    = time.Second
)

// pingResult is one device's answer to a _ping.
type pingResult struct {
	Host          string   `json:"host"`
	PacketsSent   int      `json:"packets_sent"`
	PacketsRecv   int      `json:"packets_recv"`
	PacketLossPct float64  `json:"packet_loss_pct"`
	AvgMs         *float64 `json:"avg_ms"`
}

// pingOutcome is a _ping's end state: a result, or why there isn't one.
type pingOutcome struct {
	result *pingResult
	err    string
	at     time.Time
}

// stepPing — real, and read-only, so it runs in dry-run too. Pings each
// target device's address from its Collector. A device passes when it
// replies, within max_loss_pct and max_response_ms if the step sets them.
// Devices with no network address (serial) are left out of a room or type
// target, and fail a step that names them.
func (e *Executor) stepPing(ctx context.Context, run RunContext, s step) stepResult {
	started := time.Now()
	var body struct {
		Target        map[string]any `json:"target"`
		Count         int            `json:"count"`
		MaxLossPct    *float64       `json:"max_loss_pct"`
		MaxResponseMs *float64       `json:"max_response_ms"`
	}
	if err := json.Unmarshal(s.Raw, &body); err != nil {
		return failedResult(started, "step json: "+err.Error())
	}
	if body.Count <= 0 {
		body.Count = 3
	}
	if body.Count > 10 {
		body.Count = 10
	}
	devices, err := resolveTargetDevices(ctx, e.pool, run, body.Target)
	if err != nil {
		return failedResult(started, "resolve target: "+err.Error())
	}
	named := false
	if did, ok := stringField(body.Target, "device_id"); ok && did != "" {
		named = true
	}
	var pingable []resolvedDevice
	for _, d := range devices {
		if d.protocol == "serial" && !named {
			continue
		}
		pingable = append(pingable, d)
	}
	if len(pingable) == 0 {
		return failedResult(started, "target resolved to no devices with a network address")
	}

	expected := map[string]any{"replies": true}
	if body.MaxLossPct != nil {
		expected["max_loss_pct"] = *body.MaxLossPct
	}
	if body.MaxResponseMs != nil {
		expected["max_response_ms"] = *body.MaxResponseMs
	}

	outcomes := e.pingDevices(ctx, run, pingable, body.Count)
	judged := time.Now()
	var failures []string
	var readings []stepReading
	actual := map[string]any{}
	for _, d := range pingable {
		o, ok := outcomes[d.id]
		switch {
		case !ok:
			actual[d.name] = map[string]any{"error": "no answer from the Collector"}
			readings = append(readings, readingFrom(d.name, "none", nil, judged))
			failures = append(failures, d.name+": no answer from the Collector (offline, or too old for Ping steps)")
			continue
		case o.err != "":
			actual[d.name] = map[string]any{"error": pingError(o.err)}
			readings = append(readings, readingFrom(d.name, "none", nil, judged))
			failures = append(failures, d.name+": "+pingError(o.err))
			continue
		}
		r := o.result
		readings = append(readings, readingFrom(d.name, "fresh", &o.at, judged))
		a := map[string]any{"host": r.Host, "replies": r.PacketsRecv, "sent": r.PacketsSent, "packet_loss_pct": r.PacketLossPct}
		if r.AvgMs != nil {
			a["response_ms"] = *r.AvgMs
		}
		actual[d.name] = a
		switch {
		case r.PacketsRecv == 0:
			failures = append(failures, fmt.Sprintf("%s (%s): no reply to %d pings", d.name, r.Host, r.PacketsSent))
		case body.MaxLossPct != nil && r.PacketLossPct > *body.MaxLossPct:
			failures = append(failures, fmt.Sprintf("%s (%s): %.0f%% packet loss (want at most %.0f%%)", d.name, r.Host, r.PacketLossPct, *body.MaxLossPct))
		case body.MaxResponseMs != nil && r.AvgMs != nil && *r.AvgMs > *body.MaxResponseMs:
			failures = append(failures, fmt.Sprintf("%s (%s): %.0f ms response (want at most %.0f ms)", d.name, r.Host, *r.AvgMs, *body.MaxResponseMs))
		}
	}
	res := stepResult{
		Passed:    len(failures) == 0,
		Error:     strings.Join(failures, "; "),
		Expected:  expected,
		Actual:    actual,
		Readings:  readings,
		StartedAt: started,
		EndedAt:   time.Now(),
	}
	if len(pingable) == 1 {
		res.DeviceID = pingable[0].id
	}
	return res
}

// pingError words a Collector's _ping failure for the step result.
func pingError(err string) string {
	if strings.Contains(err, pingCommand) {
		// An adapter refusing "_ping" as an unknown command: the
		// Collector predates Ping steps.
		return "the Collector needs updating to run Ping steps"
	}
	return err
}

// pingDevices queues a _ping for each device in one transaction, then
// watches them with one query a second, like freshReadings. A device
// missing from the result got no answer in time.
func (e *Executor) pingDevices(ctx context.Context, run RunContext, devices []resolvedDevice, count int) map[string]pingOutcome {
	out := map[string]pingOutcome{}
	args := []byte(fmt.Sprintf(`{"count":%d}`, count))
	cmdDevice := map[string]string{} // command id -> device id
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		for _, d := range devices {
			id, err := commands.Submit(ctx, tx, run.CustomerID, d.id, pingCommand, args, "nightly:"+run.RunID)
			if err != nil {
				return err
			}
			cmdDevice[id] = d.id
		}
		return nil
	})
	if err != nil {
		e.log.Warn("nightly: ping request failed", "run", run.RunID, "error", err)
		return out
	}
	ids := make([]string, 0, len(cmdDevice))
	for id := range cmdDevice {
		ids = append(ids, id)
	}

	deadline := time.Now().Add(pingTimeout)
	tick := time.NewTicker(pingTick)
	defer tick.Stop()
	for {
		pending, err := e.collectPings(ctx, ids, cmdDevice, out)
		if err != nil {
			e.log.Warn("nightly: reading ping results failed", "run", run.RunID, "error", err)
			return out
		}
		if pending == 0 || time.Now().After(deadline) {
			return out
		}
		select {
		case <-ctx.Done():
			return out
		case <-tick.C:
		}
	}
}

// collectPings adds the finished _ping results to out and reports how many
// are still pending or in progress.
func (e *Executor) collectPings(ctx context.Context, ids []string, cmdDevice map[string]string, out map[string]pingOutcome) (int, error) {
	rows, err := e.pool.Query(ctx,
		`SELECT id::text, status, result, COALESCE(error, ''), COALESCE(completed_at, now()) FROM commands WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	pending := 0
	for rows.Next() {
		var (
			id, status, cmdErr string
			result             []byte
			done               time.Time
		)
		if err := rows.Scan(&id, &status, &result, &cmdErr, &done); err != nil {
			return 0, err
		}
		switch commands.Status(status) {
		case commands.StatusPending, commands.StatusInProgress:
			pending++
		case commands.StatusSucceeded:
			var r struct {
				Parsed pingResult `json:"parsed"`
			}
			if err := json.Unmarshal(result, &r); err != nil || r.Parsed.PacketsSent == 0 {
				out[cmdDevice[id]] = pingOutcome{err: "unreadable ping result", at: done}
				continue
			}
			out[cmdDevice[id]] = pingOutcome{result: &r.Parsed, at: done}
		default:
			if cmdErr == "" {
				cmdErr = "ping " + status
			}
			out[cmdDevice[id]] = pingOutcome{err: cmdErr, at: done}
		}
	}
	return pending, rows.Err()
}
