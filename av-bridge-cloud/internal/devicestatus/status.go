// Package devicestatus centralises the read-side derivation of a device's
// "effective" status. The bridge writes devices.latest_status on every
// ingest push; that column is a snapshot in time and stays put when the
// bridge stops phoning home. A device on an offline collector therefore
// looks green in the DB long after we've actually lost visibility.
//
// Every user-facing read site should project through EffectiveStatusSQL
// so the portal (and public API) present "unknown" when the collector
// is unreachable rather than propagating a stale value.
package devicestatus

import "time"

// OfflineAfter is the collector last_seen_at freshness threshold — a
// collector we haven't heard from in this long is treated as offline,
// which downgrades every device on that collector to "unknown". Kept
// aligned with computeCollectorStatus's threshold in portalapi so the
// /collectors page and the derived device status agree.
const OfflineAfter = 5 * time.Minute

// EffectiveStatusSQL projects a device row's latest_status, downgraded
// to 'unknown' when the device's collector hasn't checked in within
// OfflineAfter. Uses "d." for the device row and "c." for the
// collector row — every calling query must LEFT JOIN collectors c ON
// c.id = d.collector_id (or an equivalent alias mapping).
//
// The 5-minute interval is inlined rather than parameterised because
// this fragment is baked into many static queries and passing an
// argument everywhere buys nothing at current scale. If OfflineAfter
// changes, update both places.
const EffectiveStatusSQL = `CASE
  WHEN c.last_seen_at IS NULL
    OR c.last_seen_at < now() - interval '5 minutes'
    THEN 'unknown'
  ELSE COALESCE(d.latest_status, 'unknown')
END`
