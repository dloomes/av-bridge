// Routine executor — Phase B slice 1.
//
// Runs during a nightly_run's `testing` phase. Iterates the assigned
// routine's steps, dispatches each one, records the outcome to
// nightly_step_result, and honours on_failure ("abort" | "continue" |
// "retry:N"). When the last step completes, the run moves to `ready`
// (or `failed`, if a step with on_failure=abort didn't pass).
//
// Design reference: docs/nightly-lifecycle-spec.md §6.3, §7.
//
// Step handling policy — read-only steps run for real, write steps
// respect the scheduler-wide DryRun flag:
//
//   wait          — real (no side effects; a plain time.Sleep)
//   section       — real (structural only; always passes)
//   expect_status — real (reads devices.latest_status)
//   check_metric  — real (reads telemetry table)
//   power_on/off  — dry-run log (matches existing scheduler behaviour)
//   device_command — dry-run log
//
// So a customer's routine produces useful step-result data from day one,
// even before command dispatch is wired.
//
// Runs as app_admin (BYPASSRLS) — the executor is system code operating
// across all customers, same as the scheduler.

package nightly

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dloomes/av-bridge-cloud/internal/commands"
)

// Default per-device command timeout for routine steps that don't
// carry an explicit timeout_seconds. Matches the portal's send-command
// wait window so a routine step feels no slower than a manual button
// press. Kept small so a broken adapter doesn't hold a step open for
// its full 5-minute step budget.
const defaultCommandTimeout = 30 * time.Second

// ExecutorConfig — the executor's tunables. DryRun mirrors the
// scheduler's dry-run flag; when true, write-side steps (power on/off,
// device commands) log rather than dispatch. Read-side steps
// (check_metric, expect_status) always execute for real.
type ExecutorConfig struct {
	// Enabled — master switch. When false, the scheduler bypasses the
	// executor entirely (warming → ready, no testing phase). Default
	// false so this slice ships without disturbing existing behaviour.
	Enabled bool

	// StepTimeout — hard cap on a single step's execution. Applies to
	// the whole step including waits. A step exceeding this is marked
	// failed with an error.
	StepTimeout time.Duration

	// DryRun — inherited from scheduler.Config.DryRun. When true, write
	// steps log the intended action instead of dispatching.
	DryRun bool

	// StuckAfter — on scheduler start, any run stuck in `testing`
	// phase for longer than this is marked failed with reason
	// "executor restart". Prevents runs abandoned by a crashed process
	// from sitting in-flight forever.
	StuckAfter time.Duration
}

// Executor runs routines. One instance per cloud process, wired into
// the scheduler which calls MaybeStart when a run reaches the point
// where testing would begin.
type Executor struct {
	pool *pgxpool.Pool
	cfg  ExecutorConfig
	log  *slog.Logger

	mu     sync.Mutex
	active map[string]context.CancelFunc
	parent context.Context
}

// NewExecutor. parentCtx is the shutdown-context — cancelling it stops
// every in-flight step across every run in progress. In production
// this is the same context that stops the scheduler + digest sender.
func NewExecutor(pool *pgxpool.Pool, cfg ExecutorConfig, log *slog.Logger, parentCtx context.Context) *Executor {
	return &Executor{
		pool:   pool,
		cfg:    cfg,
		log:    log,
		active: make(map[string]context.CancelFunc),
		parent: parentCtx,
	}
}

// RunContext — what the scheduler passes when handing off a run to
// the executor. Minimal by design; the executor re-queries anything
// else it needs.
type RunContext struct {
	RunID      string
	CustomerID string
	RoomID     string
	RoomName   string
	RoutineID  *string // nil means no routine assigned; MaybeStart declines

	// ForceRealDispatch overrides the executor's DryRun config for this
	// run only. Used by ad-hoc "Test on a room" triggers where the
	// operator has explicitly asked for real dispatch, even when
	// scheduled runs are still in dry-run mode.
	ForceRealDispatch bool
}

// MaybeStart is the executor's public entry from the scheduler. It
// returns (true, nil) when the executor has taken ownership of the run
// (a goroutine has been spawned) — in which case the scheduler must
// NOT finish the run itself; the executor will. It returns (false, nil)
// when execution is not applicable — no routine, no steps, or the
// executor is disabled — meaning the scheduler should proceed with its
// normal `ready` finalisation.
func (e *Executor) MaybeStart(ctx context.Context, run RunContext) (bool, error) {
	if !e.cfg.Enabled {
		return false, nil
	}
	if run.RoutineID == nil || *run.RoutineID == "" {
		return false, nil
	}

	steps, err := e.loadSteps(ctx, *run.RoutineID)
	if err != nil {
		return false, fmt.Errorf("load steps: %w", err)
	}
	if len(steps) == 0 {
		return false, nil
	}

	e.mu.Lock()
	if _, alreadyRunning := e.active[run.RunID]; alreadyRunning {
		// Idempotent: another goroutine is already executing this run.
		// Report as "started" so the scheduler doesn't double-finish.
		e.mu.Unlock()
		return true, nil
	}
	execCtx, cancel := context.WithCancel(e.parent)
	e.active[run.RunID] = cancel
	e.mu.Unlock()

	go func() {
		defer func() {
			e.mu.Lock()
			delete(e.active, run.RunID)
			e.mu.Unlock()
		}()
		e.executeSteps(execCtx, run, steps)
	}()

	return true, nil
}

// SweepStuck marks any run currently in the `testing` phase whose
// started_at is older than cfg.StuckAfter as failed. Called from the
// scheduler on startup so a crashed process's in-flight runs don't
// sit in-flight forever. Cheap — indexed by phase.
func (e *Executor) SweepStuck(ctx context.Context) error {
	if e.cfg.StuckAfter <= 0 {
		return nil
	}
	cutoffSecs := int(e.cfg.StuckAfter.Seconds())
	res, err := e.pool.Exec(ctx, `
		UPDATE nightly_run
		   SET phase = 'failed',
		       status = 'failed',
		       completed_at = now(),
		       failure_reason = 'executor restart, previous run abandoned'
		 WHERE phase = 'testing'
		   AND started_at < now() - make_interval(secs => $1)
	`, cutoffSecs)
	if err != nil {
		return err
	}
	if n := res.RowsAffected(); n > 0 {
		e.log.Warn("nightly executor: swept stuck testing runs on startup", "count", n)
	}
	return nil
}

// ── Step schema ──────────────────────────────────────────────────────
//
// Mirrors the JSON shape stored in nightly_test_routine.steps. Every
// step has type + on_failure + name; the rest is type-specific and
// carried on a raw map so the executor can dispatch by type without a
// per-type Unmarshal target.

type step struct {
	Name      string          `json:"name,omitempty"`
	Type      string          `json:"type"`
	OnFailure string          `json:"on_failure,omitempty"`
	Timeout   int             `json:"timeout_seconds,omitempty"`
	Raw       json.RawMessage `json:"-"` // whole step JSON, kept for type-specific access
}

// loadSteps pulls a routine's steps as an array. Reads via app_admin —
// no tenant context set — because the executor operates cross-customer.
// customer_id is enforced by the join with nightly_run's customer_id
// upstream (the run only exists for the tenant that authored the
// routine).
func (e *Executor) loadSteps(ctx context.Context, routineID string) ([]step, error) {
	var raw []byte
	err := e.pool.QueryRow(ctx, `
		SELECT steps FROM nightly_test_routine WHERE id = $1
	`, routineID).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, fmt.Errorf("routine steps not a JSON array: %w", err)
	}
	steps := make([]step, 0, len(arr))
	for _, r := range arr {
		var s step
		if err := json.Unmarshal(r, &s); err != nil {
			return nil, fmt.Errorf("step: %w", err)
		}
		s.Raw = r
		steps = append(steps, s)
	}
	return steps, nil
}

// ── Execution ─────────────────────────────────────────────────────────

// stepResult is what we write to nightly_step_result per step.
type stepResult struct {
	Passed    bool
	Error     string // "" when passed
	Expected  any    // nil-safe; encoded as JSON to the expected column
	Actual    any    // nil-safe; encoded as JSON to the actual column
	DeviceID  string // "" when step wasn't device-scoped
	StartedAt time.Time
	EndedAt   time.Time
}

// executeSteps is the per-run loop. Runs sequentially; each step's
// timeout is enforced with a child context. on_failure controls
// whether we abort remaining steps or continue — it does NOT grade the
// run. Any failed step means the run is failed overall; on_failure just
// decides whether we bail immediately or gather the full picture.
//
// Rationale: a room where the mic doesn't hear audio isn't ready, even
// if the routine continues to hang up the call cleanly for tidiness.
// The morning digest must surface that.
func (e *Executor) executeSteps(ctx context.Context, run RunContext, steps []step) {
	e.log.Info("nightly executor: starting",
		"run", run.RunID, "room", run.RoomName, "steps", len(steps))

	var (
		abortReason string   // set by on_failure=abort — stops the loop
		failures    []string // any-step-failed collector — grades the run
	)
	for i, s := range steps {
		if err := ctx.Err(); err != nil {
			// Shutdown / cancellation. Mark the run failed with the
			// context error and stop.
			abortReason = "executor cancelled: " + err.Error()
			break
		}

		res := e.runStep(ctx, run, i, s)
		if err := e.insertStepResult(ctx, run, i, s, res); err != nil {
			e.log.Warn("nightly executor: insert step result failed",
				"run", run.RunID, "step_index", i, "error", err)
		}
		if res.Passed {
			continue
		}

		// Step failed. Record for the run-outcome verdict.
		failures = append(failures, fmt.Sprintf("step %d (%s): %s",
			i+1, displayName(s), res.Error))

		policy := strings.ToLower(strings.TrimSpace(s.OnFailure))
		if policy == "" || policy == "abort" {
			abortReason = fmt.Sprintf("step %d (%s) failed: %s",
				i+1, displayName(s), res.Error)
			break
		}
		// "continue" — keep going through remaining steps, but the
		// failure is still counted against the run's overall status.
		e.log.Info("nightly executor: step failed, on_failure=continue (run will still be marked failed)",
			"run", run.RunID, "step_index", i, "step_name", displayName(s), "error", res.Error)
	}

	// Grade the run. Any failure at all → failed. Abort-caused failures
	// use their specific reason; otherwise we compose a summary of all
	// the failed steps.
	if abortReason != "" {
		if err := e.finishRun(ctx, run.RunID, "failed", "failed", abortReason); err != nil {
			e.log.Warn("nightly executor: finish (failed) update error",
				"run", run.RunID, "error", err)
		}
		e.log.Warn("nightly executor: run failed (aborted)",
			"run", run.RunID, "room", run.RoomName, "reason", abortReason)
		return
	}
	if len(failures) > 0 {
		reason := fmt.Sprintf("%d step(s) failed: %s",
			len(failures), strings.Join(failures, "; "))
		if err := e.finishRun(ctx, run.RunID, "failed", "failed", reason); err != nil {
			e.log.Warn("nightly executor: finish (failed) update error",
				"run", run.RunID, "error", err)
		}
		e.log.Warn("nightly executor: run failed (continued-through)",
			"run", run.RunID, "room", run.RoomName, "failed_steps", len(failures))
		return
	}
	if err := e.finishRun(ctx, run.RunID, "ready", "succeeded", ""); err != nil {
		e.log.Warn("nightly executor: finish (ready) update error",
			"run", run.RunID, "error", err)
	}
	e.log.Info("nightly executor: run ready",
		"run", run.RunID, "room", run.RoomName)
}

// runStep dispatches one step, honouring per-step timeout and retry
// policy. Returns the outcome; caller decides whether it aborts.
func (e *Executor) runStep(ctx context.Context, run RunContext, idx int, s step) stepResult {
	// Per-step timeout: prefer step.Timeout, fall back to executor default.
	stepCtx, cancel := e.stepContext(ctx, s.Timeout)
	defer cancel()

	retries := parseRetryCount(s.OnFailure)
	var last stepResult
	for attempt := 0; attempt <= retries; attempt++ {
		last = e.dispatchStep(stepCtx, run, s)
		if last.Passed {
			if attempt > 0 {
				e.log.Info("nightly executor: step passed on retry",
					"run", run.RunID, "step_index", idx, "attempt", attempt+1)
			}
			return last
		}
		if attempt < retries {
			e.log.Debug("nightly executor: step failed, retrying",
				"run", run.RunID, "step_index", idx, "attempt", attempt+1, "error", last.Error)
		}
	}
	return last
}

func (e *Executor) stepContext(parent context.Context, stepTimeoutSecs int) (context.Context, context.CancelFunc) {
	// Explicit step timeout wins over the executor default. A zero
	// value falls back to the default; a negative value (unlikely,
	// invalid) also falls back. This keeps behaviour predictable when
	// a routine is missing timeout_seconds.
	d := e.cfg.StepTimeout
	if stepTimeoutSecs > 0 {
		d = time.Duration(stepTimeoutSecs) * time.Second
	}
	if d <= 0 {
		// No cap. Return a cancel that's still a valid CancelFunc so
		// callers can defer it safely.
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, d)
}

// dispatchStep is the type-switch. Each handler returns a stepResult
// carrying its own started/ended timestamps so we can record accurate
// per-step durations without the caller passing them in.
func (e *Executor) dispatchStep(ctx context.Context, run RunContext, s step) stepResult {
	switch s.Type {
	case "wait":
		return e.stepWait(ctx, s)
	case "section":
		return e.stepSection(s)
	case "expect_status":
		return e.stepExpectStatus(ctx, run, s)
	case "check_metric":
		return e.stepCheckMetric(ctx, run, s)
	case "power_on":
		return e.stepPowerAction(ctx, run, s, "power_on")
	case "power_off":
		return e.stepPowerAction(ctx, run, s, "power_off")
	case "device_command":
		return e.stepDeviceCommand(ctx, run, s)
	default:
		return stepResult{
			Passed: false,
			Error:  "unknown step type: " + s.Type,
			StartedAt: time.Now(),
			EndedAt:   time.Now(),
		}
	}
}

// ── Step handlers ────────────────────────────────────────────────────

// stepWait — real. Sleeps for the configured duration or until the
// context cancels. Zero duration passes immediately.
func (e *Executor) stepWait(ctx context.Context, s step) stepResult {
	started := time.Now()
	var body struct {
		DurationSeconds int `json:"duration_seconds"`
	}
	_ = json.Unmarshal(s.Raw, &body)
	if body.DurationSeconds <= 0 {
		return stepResult{Passed: true, StartedAt: started, EndedAt: time.Now()}
	}
	select {
	case <-time.After(time.Duration(body.DurationSeconds) * time.Second):
		return stepResult{Passed: true, StartedAt: started, EndedAt: time.Now()}
	case <-ctx.Done():
		return stepResult{
			Passed:    false,
			Error:     "wait cancelled: " + ctx.Err().Error(),
			StartedAt: started,
			EndedAt:   time.Now(),
		}
	}
}

// stepSection — structural. Always passes; label goes into expected
// so the digest can display the section heading.
func (e *Executor) stepSection(s step) stepResult {
	now := time.Now()
	var body struct {
		Label string `json:"label"`
	}
	_ = json.Unmarshal(s.Raw, &body)
	return stepResult{
		Passed:    true,
		Expected:  map[string]any{"label": body.Label},
		StartedAt: now,
		EndedAt:   now,
	}
}

// stepExpectStatus — real. Resolves the target device set and asserts
// each device's latest_status matches. Fails if any device is out.
func (e *Executor) stepExpectStatus(ctx context.Context, run RunContext, s step) stepResult {
	started := time.Now()
	var body struct {
		Target map[string]any `json:"target"`
		Status string         `json:"status"`
	}
	if err := json.Unmarshal(s.Raw, &body); err != nil {
		return failedResult(started, "step json: "+err.Error())
	}
	if body.Status == "" {
		return failedResult(started, "expect_status requires `status`")
	}
	devices, err := resolveTargetDevices(ctx, e.pool, run, body.Target)
	if err != nil {
		return failedResult(started, "resolve target: "+err.Error())
	}
	if len(devices) == 0 {
		return failedResult(started, "target resolved to no devices")
	}
	var mismatches []string
	actual := map[string]string{}
	for _, d := range devices {
		actual[d.name] = d.latestStatus
		if !strings.EqualFold(d.latestStatus, body.Status) {
			mismatches = append(mismatches,
				fmt.Sprintf("%s is %q (want %q)", d.name, d.latestStatus, body.Status))
		}
	}
	if len(mismatches) > 0 {
		return stepResult{
			Passed:    false,
			Error:     strings.Join(mismatches, "; "),
			Expected:  map[string]any{"status": body.Status},
			Actual:    actual,
			StartedAt: started,
			EndedAt:   time.Now(),
		}
	}
	return stepResult{
		Passed:    true,
		Expected:  map[string]any{"status": body.Status},
		Actual:    actual,
		StartedAt: started,
		EndedAt:   time.Now(),
	}
}

// stepCheckMetric — real. For each target device, pulls the latest
// telemetry within sample_window_seconds and compares the requested
// metric against the threshold using the operator. Failing devices
// are aggregated into the error string.
func (e *Executor) stepCheckMetric(ctx context.Context, run RunContext, s step) stepResult {
	started := time.Now()
	var body struct {
		Target            map[string]any `json:"target"`
		Metric            string         `json:"metric"`
		Operator          string         `json:"operator"` // gt|gte|lt|lte|eq
		Threshold         float64        `json:"threshold"`
		SampleWindowSecs  int            `json:"sample_window_seconds"`
	}
	if err := json.Unmarshal(s.Raw, &body); err != nil {
		return failedResult(started, "step json: "+err.Error())
	}
	if body.Metric == "" {
		return failedResult(started, "check_metric requires `metric`")
	}
	if body.Operator == "" {
		body.Operator = "gt"
	}
	if body.SampleWindowSecs <= 0 {
		body.SampleWindowSecs = 30
	}
	devices, err := resolveTargetDevices(ctx, e.pool, run, body.Target)
	if err != nil {
		return failedResult(started, "resolve target: "+err.Error())
	}
	if len(devices) == 0 {
		return failedResult(started, "target resolved to no devices")
	}
	var mismatches []string
	actual := map[string]any{}
	for _, d := range devices {
		val, ok, terr := readLatestMetric(ctx, e.pool, d.id, body.Metric, body.SampleWindowSecs)
		if terr != nil {
			mismatches = append(mismatches, fmt.Sprintf("%s: telemetry read error: %s", d.name, terr.Error()))
			continue
		}
		if !ok {
			mismatches = append(mismatches, fmt.Sprintf("%s: no `%s` sample in the last %ds",
				d.name, body.Metric, body.SampleWindowSecs))
			continue
		}
		actual[d.name] = val
		if !compareMetric(val, body.Operator, body.Threshold) {
			mismatches = append(mismatches,
				fmt.Sprintf("%s: %s = %.4g (want %s %.4g)",
					d.name, body.Metric, val, body.Operator, body.Threshold))
		}
	}
	if len(mismatches) > 0 {
		return stepResult{
			Passed:    false,
			Error:     strings.Join(mismatches, "; "),
			Expected:  map[string]any{"metric": body.Metric, "operator": body.Operator, "threshold": body.Threshold},
			Actual:    actual,
			StartedAt: started,
			EndedAt:   time.Now(),
		}
	}
	return stepResult{
		Passed:    true,
		Expected:  map[string]any{"metric": body.Metric, "operator": body.Operator, "threshold": body.Threshold},
		Actual:    actual,
		StartedAt: started,
		EndedAt:   time.Now(),
	}
}

// stepPowerAction — power on/off across the resolved device set. When
// DryRun is on, we log and pass without dispatch; otherwise we enqueue
// a real command per device via the commands package and wait for
// each to reach a terminal status. Step passes only if every device's
// command succeeded (respecting on_failure at the caller).
func (e *Executor) stepPowerAction(ctx context.Context, run RunContext, s step, action string) stepResult {
	started := time.Now()
	var body struct {
		Target         map[string]any `json:"target"`
		TimeoutSeconds int            `json:"timeout_seconds"`
	}
	_ = json.Unmarshal(s.Raw, &body)
	devices, err := resolveTargetDevices(ctx, e.pool, run, body.Target)
	if err != nil {
		return failedResult(started, "resolve target: "+err.Error())
	}
	if e.cfg.DryRun && !run.ForceRealDispatch {
		for _, d := range devices {
			e.log.Info("[dry-run] step:"+action, "run", run.RunID, "device", d.id, "device_name", d.name)
		}
		return stepResult{
			Passed:    true,
			Expected:  map[string]any{"action": action, "target_devices": len(devices)},
			Actual:    map[string]any{"dispatched": false, "devices": len(devices)},
			StartedAt: started,
			EndedAt:   time.Now(),
		}
	}

	perDevice, allOK := e.dispatchAcross(ctx, run, devices, action, nil, body.TimeoutSeconds)
	return stepResult{
		Passed:    allOK,
		Expected:  map[string]any{"action": action, "target_devices": len(devices)},
		Actual:    map[string]any{"dispatched": true, "devices": len(devices), "per_device": perDevice},
		StartedAt: started,
		EndedAt:   time.Now(),
	}
}

// stepDeviceCommand — enqueues the routine's named command against the
// resolved target device set and waits for each to reach a terminal
// status. Dry-run branch mirrors stepPowerAction so a customer building
// routines against a live tenant sees consistent behaviour whether or
// not real dispatch is enabled.
func (e *Executor) stepDeviceCommand(ctx context.Context, run RunContext, s step) stepResult {
	started := time.Now()
	var body struct {
		Target         map[string]any `json:"target"`
		Command        string         `json:"command"`
		Parameters     map[string]any `json:"parameters"`
		TimeoutSeconds int            `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(s.Raw, &body); err != nil {
		return failedResult(started, "step json: "+err.Error())
	}
	if body.Command == "" {
		return failedResult(started, "device_command requires `command`")
	}
	devices, err := resolveTargetDevices(ctx, e.pool, run, body.Target)
	if err != nil {
		return failedResult(started, "resolve target: "+err.Error())
	}
	if e.cfg.DryRun && !run.ForceRealDispatch {
		for _, d := range devices {
			e.log.Info("[dry-run] step:device_command",
				"run", run.RunID, "device", d.id, "device_name", d.name,
				"command", body.Command, "params", body.Parameters)
		}
		return stepResult{
			Passed:    true,
			Expected:  map[string]any{"command": body.Command, "target_devices": len(devices)},
			Actual:    map[string]any{"dispatched": false, "devices": len(devices)},
			StartedAt: started,
			EndedAt:   time.Now(),
		}
	}

	perDevice, allOK := e.dispatchAcross(ctx, run, devices, body.Command, body.Parameters, body.TimeoutSeconds)
	return stepResult{
		Passed:    allOK,
		Expected:  map[string]any{"command": body.Command, "target_devices": len(devices)},
		Actual:    map[string]any{"dispatched": true, "devices": len(devices), "per_device": perDevice},
		StartedAt: started,
		EndedAt:   time.Now(),
	}
}

// ── Real dispatch (commands package) ─────────────────────────────────

// dispatchAcross submits a command per device, waits for each to reach
// a terminal status (bounded by step's timeout or defaultCommandTimeout),
// and returns a per-device result summary plus a bool for whether every
// device succeeded. Never returns an error — a failure is reflected in
// the perDevice map so the step's `actual` payload preserves which
// device broke and why. The bool is `false` when any device didn't
// return `succeeded`, including timeouts and enqueue failures.
func (e *Executor) dispatchAcross(
	ctx context.Context,
	run RunContext,
	devices []resolvedDevice,
	commandName string,
	params map[string]any,
	timeoutSecs int,
) (perDevice []map[string]any, allOK bool) {
	timeout := defaultCommandTimeout
	if timeoutSecs > 0 {
		timeout = time.Duration(timeoutSecs) * time.Second
	}

	// Marshal args once — every device gets the same payload for a
	// single routine step. Empty map -> nil args on the wire.
	var argsJSON []byte
	if len(params) > 0 {
		if b, err := json.Marshal(params); err == nil {
			argsJSON = b
		}
	}

	perDevice = make([]map[string]any, 0, len(devices))
	allOK = true
	for _, d := range devices {
		res := e.dispatchOne(ctx, run, d, commandName, argsJSON, timeout)
		perDevice = append(perDevice, res)
		status, _ := res["status"].(string)
		if status != string(commands.StatusSucceeded) {
			allOK = false
		}
	}
	return perDevice, allOK
}

// dispatchOne enqueues one command against one device and waits until
// it's terminal or the timeout elapses. Errors during Submit/Get are
// folded into the returned map (status=failed, error=...) so the
// caller never has to distinguish "the enqueue crashed" from "the
// bridge said the command failed".
func (e *Executor) dispatchOne(
	ctx context.Context,
	run RunContext,
	dev resolvedDevice,
	commandName string,
	argsJSON []byte,
	timeout time.Duration,
) map[string]any {
	base := map[string]any{
		"device_id":   dev.id,
		"device_name": dev.name,
		"command":     commandName,
	}

	// Submit — wrap the insert in a tx on the admin pool. Executor is
	// admin (BYPASSRLS) so setting app.current_customer isn't strictly
	// required, but wrapping in a tx keeps the commands package's tx
	// contract satisfied and gives a clean rollback point on error.
	var commandID string
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		id, err := commands.Submit(ctx, tx,
			run.CustomerID, dev.id, commandName, argsJSON, "nightly:"+run.RunID)
		if err != nil {
			return err
		}
		commandID = id
		return nil
	}); err != nil {
		e.log.Warn("nightly dispatch submit failed",
			"run", run.RunID, "device", dev.id, "command", commandName, "error", err)
		base["status"] = string(commands.StatusFailed)
		base["error"] = "submit: " + err.Error()
		return base
	}
	base["command_id"] = commandID

	// Wait — commands.WaitForTerminal polls a fresh short tx each tick
	// so we don't hold a DB connection across the wait window.
	cmd, err := commands.WaitForTerminal(ctx, func(ctx context.Context) (commands.Command, error) {
		var out commands.Command
		txErr := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			c, err := commands.Get(ctx, tx, commandID)
			if err != nil {
				return err
			}
			out = c
			return nil
		})
		return out, txErr
	}, timeout)

	if err != nil {
		base["status"] = string(commands.StatusFailed)
		base["error"] = "poll: " + err.Error()
		return base
	}
	base["status"] = string(cmd.Status)
	// Non-terminal at deadline means the bridge hasn't picked it up (or
	// its adapter is stuck). Report that distinctly from an adapter-
	// returned failure so operators reading the results know where to
	// look next.
	if !cmd.Status.Terminal() {
		base["status"] = string(commands.StatusFailed)
		base["error"] = fmt.Sprintf("timeout after %s waiting for bridge", timeout)
		return base
	}
	if cmd.Error != "" {
		base["error"] = cmd.Error
	}
	if len(cmd.Result) > 0 {
		var r any
		if err := json.Unmarshal(cmd.Result, &r); err == nil {
			base["result"] = r
		}
	}
	return base
}

// ── Persistence ──────────────────────────────────────────────────────

func (e *Executor) insertStepResult(ctx context.Context, run RunContext, idx int, s step, r stepResult) error {
	var expJSON, actJSON []byte
	if r.Expected != nil {
		expJSON, _ = json.Marshal(r.Expected)
	}
	if r.Actual != nil {
		actJSON, _ = json.Marshal(r.Actual)
	}
	var devArg any
	if r.DeviceID != "" {
		devArg = r.DeviceID
	}
	var errArg any
	if r.Error != "" {
		errArg = r.Error
	}
	_, err := e.pool.Exec(ctx, `
		INSERT INTO nightly_step_result (
		  customer_id, run_id, device_id, step_index, step_name, step_type,
		  expected, actual, passed, error, started_at, completed_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb, $9, $10, $11, $12)
	`, run.CustomerID, run.RunID, devArg, idx, displayName(s), s.Type,
		nullIfEmpty(expJSON), nullIfEmpty(actJSON), r.Passed, errArg,
		r.StartedAt, r.EndedAt)
	return err
}

func (e *Executor) finishRun(ctx context.Context, runID, phase, status, reason string) error {
	var reasonArg any
	if reason != "" {
		reasonArg = reason
	}
	_, err := e.pool.Exec(ctx, `
		UPDATE nightly_run
		   SET phase = $2, status = $3, completed_at = now(), failure_reason = $4
		 WHERE id = $1
	`, runID, phase, status, reasonArg)
	return err
}

// ── Helpers ──────────────────────────────────────────────────────────

type resolvedDevice struct {
	id           string
	name         string
	kind         string // devices.type — 'vc' / 'dsp' / 'display' etc.
	latestStatus string
}

// resolveTargetDevices turns a routine step's target selector into the
// concrete set of devices to act on. Every branch scopes to the run's
// room_id — a routine can only ever affect its own room, so mixing
// targets across rooms is not possible even if the JSON were malformed.
func resolveTargetDevices(ctx context.Context, pool *pgxpool.Pool, run RunContext, target map[string]any) ([]resolvedDevice, error) {
	if target == nil {
		return nil, errors.New("no target specified")
	}

	// {device_id: "<uuid>"} — one specific device, but still scoped to
	// the run's room for safety (an attacker or misauthored routine
	// can't reach outside the room).
	if did, ok := stringField(target, "device_id"); ok && did != "" {
		return queryDevices(ctx, pool, run.RoomID, `AND id = $2`, []any{did})
	}

	// {device_type: "vc"} — every device of that type in the room.
	if dt, ok := stringField(target, "device_type"); ok && dt != "" {
		return queryDevices(ctx, pool, run.RoomID, `AND type = $2`, []any{dt})
	}

	// {scope: "room"} — every device in the room.
	if sc, ok := stringField(target, "scope"); ok && sc == "room" {
		return queryDevices(ctx, pool, run.RoomID, ``, nil)
	}

	return nil, errors.New("target must specify scope, device_type, or device_id")
}

func queryDevices(ctx context.Context, pool *pgxpool.Pool, roomID string, extra string, extraArgs []any) ([]resolvedDevice, error) {
	args := append([]any{roomID}, extraArgs...)
	sql := `
		SELECT id::text,
		       COALESCE(name, reported_id, ''),
		       COALESCE(type, ''),
		       COALESCE(latest_status, 'unknown')
		  FROM devices
		 WHERE room_id = $1
		   AND deleted_at IS NULL ` + extra + `
		 ORDER BY name NULLS LAST, reported_id`
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []resolvedDevice
	for rows.Next() {
		var d resolvedDevice
		if err := rows.Scan(&d.id, &d.name, &d.kind, &d.latestStatus); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// readLatestMetric reads the most recent telemetry row for a device
// within the sample window, then extracts the named metric from its
// jsonb payload as a float. Returns (value, true, nil) on success,
// (0, false, nil) when no sample or metric is missing.
func readLatestMetric(ctx context.Context, pool *pgxpool.Pool, deviceID, metric string, windowSecs int) (float64, bool, error) {
	var raw []byte
	err := pool.QueryRow(ctx, `
		SELECT metrics FROM telemetry
		 WHERE device_id = $1
		   AND ts > now() - make_interval(secs => $2)
		 ORDER BY ts DESC
		 LIMIT 1
	`, deviceID, windowSecs).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return 0, false, fmt.Errorf("telemetry metrics json: %w", err)
	}
	v, ok := m[metric]
	if !ok {
		return 0, false, nil
	}
	f, ok := toFloat64(v)
	if !ok {
		return 0, false, fmt.Errorf("metric %q not numeric: %v", metric, v)
	}
	return f, true, nil
}

func compareMetric(v float64, op string, threshold float64) bool {
	switch strings.ToLower(op) {
	case "gt":
		return v > threshold
	case "gte":
		return v >= threshold
	case "lt":
		return v < threshold
	case "lte":
		return v <= threshold
	case "eq":
		return v == threshold
	}
	return false
}

func toFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		// Try parsing numeric strings — telemetry occasionally arrives
		// with numeric-looking strings when the source device is
		// stringly-typed.
		var f float64
		if _, err := fmt.Sscanf(x, "%f", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

func stringField(m map[string]any, key string) (string, bool) {
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func nullIfEmpty(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func displayName(s step) string {
	if s.Name != "" {
		return s.Name
	}
	return s.Type
}

func failedResult(started time.Time, msg string) stepResult {
	return stepResult{
		Passed:    false,
		Error:     msg,
		StartedAt: started,
		EndedAt:   time.Now(),
	}
}

// parseRetryCount extracts N from "retry:N". Returns 0 for any other
// on_failure value (abort, continue, empty).
func parseRetryCount(onFailure string) int {
	s := strings.ToLower(strings.TrimSpace(onFailure))
	const prefix = "retry:"
	if !strings.HasPrefix(s, prefix) {
		return 0
	}
	var n int
	_, err := fmt.Sscanf(s[len(prefix):], "%d", &n)
	if err != nil || n < 0 {
		return 0
	}
	if n > 10 {
		n = 10 // sanity cap
	}
	return n
}
