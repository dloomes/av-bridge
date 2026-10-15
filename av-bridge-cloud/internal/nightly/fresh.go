package nightly

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/commands"
)

// pollCommand is the reserved command that asks a Collector to poll a
// device now and return the reading as the command result. Mirrors the
// Collector's cloudpoll.CommandPollName.
const pollCommand = "_poll"

// Fresh readings are worth waiting for, but not indefinitely: a PDU poll
// is a dozen Telnet logins (well under a minute), and a device whose
// Collector is offline or too old to know _poll shouldn't hold a step for
// long. Past this the step falls back to the last report.
var (
	freshReadingTimeout = 60 * time.Second // var so tests can shorten it
	freshReadingTick    = time.Second
)

// noFreshPoll lists adapters whose devices aren't polled on demand: a
// full poll is slow and heavy for them (an ATEN PDU is 18-26 Telnet logins
// and up to 40 seconds), and their regular polls are recent enough for a
// check. Checks use their last report instead.
var noFreshPoll = map[string]bool{"aten_pdu": true}

// slowDeviceSampleWindow is how old the last reading of a noFreshPoll
// device may be for a Check metric step: two of a PDU's 5-minute polls.
const slowDeviceSampleWindow = 600

// freshReading is a device's state from an on-demand poll.
type freshReading struct {
	status  string
	metrics map[string]any
}

// freshReadings asks each device's Collector to poll it now and returns the
// readings by device id. Checks run straight after power-on, when the last
// scheduled poll (every 60 seconds, or 5 minutes for a PDU) can predate it.
//
// All the polls are queued in one transaction and then watched with one
// query a second, so a step in every room at once doesn't hold a database
// connection per device. A device missing from the result had no fresh
// reading (Collector offline or too old to know _poll, or the poll
// failed); callers fall back to the last report for it.
//
// _poll only reads, so it runs in dry-run too.
func (e *Executor) freshReadings(ctx context.Context, run RunContext, devices []resolvedDevice) map[string]freshReading {
	out := map[string]freshReading{}
	if len(devices) == 0 {
		return out
	}
	cmdDevice := map[string]string{} // command id -> device id
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		for _, d := range devices {
			if noFreshPoll[d.protocol] {
				continue
			}
			id, err := commands.Submit(ctx, tx, run.CustomerID, d.id, pollCommand, nil, "nightly:"+run.RunID)
			if err != nil {
				return err
			}
			cmdDevice[id] = d.id
		}
		return nil
	})
	if err != nil {
		e.log.Warn("nightly: fresh-reading request failed, using last reports", "run", run.RunID, "error", err)
		return out
	}
	if len(cmdDevice) == 0 {
		return out
	}
	ids := make([]string, 0, len(cmdDevice))
	for id := range cmdDevice {
		ids = append(ids, id)
	}

	deadline := time.Now().Add(freshReadingTimeout)
	tick := time.NewTicker(freshReadingTick)
	defer tick.Stop()
	for {
		pending, err := e.collectFresh(ctx, ids, cmdDevice, out)
		if err != nil {
			e.log.Warn("nightly: reading fresh results failed", "run", run.RunID, "error", err)
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

// collectFresh adds the finished _poll results to out and reports how many
// are still pending or in progress.
func (e *Executor) collectFresh(ctx context.Context, ids []string, cmdDevice map[string]string, out map[string]freshReading) (int, error) {
	rows, err := e.pool.Query(ctx,
		`SELECT id::text, status, result FROM commands WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	pending := 0
	for rows.Next() {
		var (
			id, status string
			result     []byte
		)
		if err := rows.Scan(&id, &status, &result); err != nil {
			return 0, err
		}
		switch commands.Status(status) {
		case commands.StatusPending, commands.StatusInProgress:
			pending++
		case commands.StatusSucceeded:
			var r struct {
				Parsed struct {
					Status  string         `json:"status"`
					Metrics map[string]any `json:"metrics"`
				} `json:"parsed"`
			}
			if json.Unmarshal(result, &r) == nil && r.Parsed.Status != "" {
				out[cmdDevice[id]] = freshReading{status: r.Parsed.Status, metrics: r.Parsed.Metrics}
			}
		}
	}
	return pending, rows.Err()
}
