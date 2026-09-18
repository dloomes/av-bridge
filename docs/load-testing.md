# Collector load testing with the `fake` adapter

The `fake` adapter is a synthetic device with no I/O. It exists so we
can measure real Collector and cloud throughput without needing hundreds
of physical devices — one row per fake device, one goroutine per row,
the same code paths as real hardware for polling, telemetry batching,
event ingest, and portal display.

Use it to put a defensible number on **"how many devices can one Collector
handle?"** and to shake out contention in the cloud ingest / DB / portal.

## What it exercises

- Hub goroutine-per-device lifecycle and reconciliation
- Per-device poll ticker + cloud telemetry batching
- Cloud ingest, DB write, tenant isolation
- Alert engine (raise `fake_flap_pct` to force status noise)
- Event pipeline (raise `fake_events_per_min` to load the event channel)
- Portal fleet-render performance at high device counts

## What it does NOT exercise

- Real vendor protocol handshakes (SSH, Telnet, TLS to the device)
- Adapter-specific parsing / session lifecycle
- Physical network I/O to the device

For those, follow up with a per-adapter benchmark against a real
device or a mock server speaking the target protocol.

## Adapter configuration

Every fake behaviour is controlled by device tags. Defaults give a
cheap "always online, 5 changing metrics per poll" device.

| Tag                     | Default | Meaning |
|-------------------------|---------|---------|
| `fake_status`           | `online` | Pinned status: `online` / `offline` / `degraded` / `unknown` |
| `fake_latency_ms`       | `0`     | Synthetic per-poll sleep in ms (simulates slow WAN) |
| `fake_jitter_ms`        | `0`     | Random extra `0..jitter` ms added to latency |
| `fake_flap_pct`         | `0`     | 0-100. Chance each poll of flipping status |
| `fake_error_pct`        | `0`     | 0-100. Chance each poll of returning an error |
| `fake_metrics_count`    | `5`     | Number of synthetic `metric_N` fields |
| `fake_events_per_min`   | `0`     | Synthetic event rate; 0 = no events |

Commands: `noop`, `set_status` (args: `status=online|offline|degraded`).

## Spinning up N fakes against a UAT tenant

Fakes come from the cloud like real devices — pick your load-test tenant
and collector, then bulk-insert:

```sql
-- 500 fake devices under one collector, mixed status flap
INSERT INTO devices (id, tenant_id, collector_id, name, type, protocol,
                     address, poll_rate, tags, created_at, updated_at)
SELECT
    gen_random_uuid(),
    :tenant_id,
    :collector_id,
    'Fake ' || lpad(n::text, 4, '0'),
    (ARRAY['display','conferencing','audio','control'])[1 + (n % 4)],
    'fake',
    'fake-' || n,
    interval '30 seconds',
    jsonb_build_object(
        'fake_status',      'online',
        'fake_latency_ms',  ((n % 6) * 20)::text,   -- 0, 20, 40, 60, 80, 100
        'fake_flap_pct',    '2',
        'fake_metrics_count','10'
    ),
    now(), now()
FROM generate_series(1, 500) AS n;
```

Adjust the ranges to model a realistic mix — a few fast local devices
plus many slow-WAN ones will match a multi-site fleet better than 500
identical rows.

Cleanup:

```sql
DELETE FROM devices WHERE protocol = 'fake' AND tenant_id = :tenant_id;
```

## Reading the results

On the Collector host:

```bash
# CPU + memory over a 5-minute window
top -b -n 30 -d 10 -p $(pgrep av-bridge) | grep av-bridge
# Goroutine count via debug endpoint if enabled
curl -s http://localhost:9090/debug/vars | jq '.goroutines'
# File descriptors
ls -la /proc/$(pgrep av-bridge)/fd | wc -l
```

In the cloud (Grafana / logs):

- Telemetry ingest rate (records/sec)
- p95 device→cloud latency
- DB write throughput on the `device_status` / `telemetry` tables
- Portal `/api/v1/devices` list-render time at high device counts

## Suggested test matrix for a sizing sign-off

Run each row for at least one full poll cycle plus a 5-minute stability
window, and record CPU %, RAM, and telemetry throughput.

| Devices | poll_rate | latency_ms | flap_pct | events/min | Purpose |
|---------|-----------|------------|----------|------------|---------|
| 250     | 30s       | 0          | 0        | 0          | Baseline — cheap fleet, LAN devices |
| 500     | 30s       | 50         | 0        | 0          | Comfortable mixed fleet |
| 500     | 30s       | 50         | 5        | 2          | Same, with alert + event pressure |
| 1000    | 60s       | 100        | 0        | 0          | Stretch — big remote fleet |
| 1000    | 60s       | 100        | 5        | 2          | Stretch, alert-noisy |
| 2000    | 5m        | 200        | 0        | 0          | Ceiling probe — long-poll big WAN fleet |

Publish the numbers under a signed capacity statement so customer
proposals can quote a concrete figure rather than the current
engineering estimate.
