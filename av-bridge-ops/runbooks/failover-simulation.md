# Failover simulation

**Purpose.** Prove that production survives the loss of the primary database
node: RDS Multi-AZ promotes the standby, the web and worker services reconnect on
their own, alarms and RDS event emails arrive. Measure how long the outage lasts.

**When.** Quarterly, off-peak. Also after changing the database module, the
connection pool settings or the alarms.

**Who approves.** Daniel. **CUSTOMER-VISIBLE:** the API is unavailable or degraded
for typically 1 to 2 minutes (Collectors buffer telemetry and retry; portal
requests fail meanwhile). If customers could notice, announce it in the
Wednesday 21:00-23:00 maintenance window and run it then.

Related: [restore-rehearsal.md](restore-rehearsal.md),
[release-and-promote.md](release-and-promote.md) (do not run this during a release).

## Pre-checks

1. `aws sso login --profile avrmm-prod`; `export AWS_PROFILE=avrmm-prod AWS_REGION=eu-west-2`.
2. The instance is Multi-AZ and available, with a standby:
   ```sh
   aws rds describe-db-instances --profile avrmm-prod --region eu-west-2 --db-instance-identifier avrmm-prod-postgres \
     --query 'DBInstances[0].[DBInstanceStatus,MultiAZ,AvailabilityZone,SecondaryAvailabilityZone]' --output text
   ```
   Expected: `available True <az-a> <az-b>`. Note the primary AZ.
3. Services are healthy and no alarm is firing:
   ```sh
   curl -fsS https://api.involvecloud.com/healthz
   aws ecs describe-services --profile avrmm-prod --region eu-west-2 --cluster avrmm-prod-cluster \
     --services avrmm-prod-web-cloud avrmm-prod-worker-cloud \
     --query 'services[].[serviceName,runningCount,desiredCount]' --output text
   aws cloudwatch describe-alarms --profile avrmm-prod --region eu-west-2 --alarm-name-prefix avrmm-prod --state-value ALARM \
     --query 'MetricAlarms[].AlarmName' --output text
   ```
   Expected: healthz OK, web `2 2`, worker `1 1`, no alarms.
4. No release or restore rehearsal is in progress, and the announcement (if
   needed) has gone out.
5. You know who receives the alert emails (the address subscribed to SNS topic
   `avrmm-prod-alerts` and the `avrmm-prod-rds-events` subscription: categories
   failure, failover, low storage) and that the subscription is confirmed:
   ```sh
   aws sns list-subscriptions-by-topic --profile avrmm-prod --region eu-west-2 \
     --topic-arn arn:aws:sns:eu-west-2:568339496641:avrmm-prod-alerts \
     --query 'Subscriptions[].[Protocol,SubscriptionArn]' --output text
   ```
   Expected: `email arn:aws:sns:...` (not `PendingConfirmation`).

## Steps

1. Start the probes in a second terminal. **`/healthz` is liveness only**: it
   returns a fixed `{"status":"ok"}` without touching the database (and
   `/public/branding` swallows database errors), so it can stay green through a
   database outage. The primary measure is a database-backed call: an
   authenticated read on the Public API, which looks up the bearer token and reads
   devices. Use a read-only token for a customer or test tenant, held in an
   environment variable and never written into this runbook, a log or the Record
   (`read -rs MARCUS_PUB_TOKEN; export MARCUS_PUB_TOKEN`). One line per second with a
   UTC timestamp and status codes:
   ```sh
   while true; do
     printf '%s db=%s health=%s\n' "$(date -u +%T)" \
       "$(curl -s -o /dev/null -m 3 -w '%{http_code}' -H "Authorization: Bearer $MARCUS_PUB_TOKEN" 'https://api.involvecloud.com/pub/v1/devices?limit=1')" \
       "$(curl -s -o /dev/null -m 3 -w '%{http_code}' https://api.involvecloud.com/healthz)"
     sleep 1
   done | tee failover-probe.log
   ```
   The log holds status codes only. Expected before the failover: `db=200 health=200`.
2. **CUSTOMER-VISIBLE.** Start the failover and note the time:
   ```sh
   date -u +%FT%TZ
   aws rds reboot-db-instance --profile avrmm-prod --region eu-west-2 --db-instance-identifier avrmm-prod-postgres \
     --force-failover \
     --query 'DBInstance.DBInstanceStatus' --output text
   ```
   Expected: `rebooting`.
3. Watch RDS events until the failover completes (about 1 to 2 minutes):
   ```sh
   aws rds describe-events --profile avrmm-prod --region eu-west-2 --source-type db-instance \
     --source-identifier avrmm-prod-postgres --duration 30 \
     --query 'Events[].[Date,Message]' --output text
   ```
   Expected messages include "Multi-AZ instance failover started" and
   "Multi-AZ instance failover completed".
4. In the probe log, note **T_down** (first `db=` value that is not 200, or a
   timeout, `000`) and **T_up** (first `db=200` after the gap). Outage = T_up -
   T_down. Stop the probe with Ctrl+C once it has returned `db=200` for a minute,
   then `unset MARCUS_PUB_TOKEN`. `health=` is expected to stay 200 throughout;
   that is not a pass signal. Cross-check the outage with two other sources: the
   RDS events from step 3 (failover started and completed times) and the ALB 5xx
   count from the targets over the window:
   ```sh
   LB=$(aws elbv2 describe-load-balancers --profile avrmm-prod --region eu-west-2 --query 'LoadBalancers[?contains(LoadBalancerName,`avrmm-prod`)].LoadBalancerArn' --output text | sed 's#.*:loadbalancer/##')
   aws cloudwatch get-metric-statistics --profile avrmm-prod --region eu-west-2 --namespace AWS/ApplicationELB \
     --metric-name HTTPCode_Target_5XX_Count --dimensions Name=LoadBalancer,Value="$LB" \
     --start-time <start-time-minus-5m> --end-time <end-time-plus-5m> --period 60 \
     --statistics Sum --query 'sort_by(Datapoints,&Timestamp)[].[Timestamp,Sum]' --output text
   ```
   Record the minutes with 5xx errors next to the probe's outage.
5. Check the instance is available in the other zone:
   ```sh
   aws rds describe-db-instances --profile avrmm-prod --region eu-west-2 --db-instance-identifier avrmm-prod-postgres \
     --query 'DBInstances[0].[DBInstanceStatus,AvailabilityZone,SecondaryAvailabilityZone]' --output text
   ```
   Expected: `available`, and the two zones swapped compared with pre-check 2.
6. Check the application reconnected without a restart:
   ```sh
   aws ecs describe-services --profile avrmm-prod --region eu-west-2 --cluster avrmm-prod-cluster \
     --services avrmm-prod-web-cloud avrmm-prod-worker-cloud \
     --query 'services[].[serviceName,runningCount,desiredCount,deployments[0].rolloutState]' --output text
   aws logs tail --profile avrmm-prod --region eu-west-2 /ecs/avrmm-prod-web-cloud --since 15m | grep -i -E "error|connection|reset|refused" | tail -20
   aws logs tail --profile avrmm-prod --region eu-west-2 /ecs/avrmm-prod-worker-cloud --since 15m | grep -i -E "error|connection|reset|refused" | tail -20
   ```
   Expected: counts unchanged (`2 2`, `1 1`), no task replacement, errors only
   within the failover window and none afterwards. Also check the Collectors list
   in the portal shows them online again, and a command sent to a device (or a
   fresh telemetry reading) flows. Record the time until the first successful
   database-backed request as "app reconnect time".
7. Check the alerting arrived. Within about 10 minutes:
   - RDS event emails for failover (via `avrmm-prod-rds-events`);
   - any CloudWatch alarm that moved to `ALARM` and back
     (`aws cloudwatch describe-alarm-history --profile avrmm-prod --region eu-west-2 --history-item-type StateUpdate --start-date <start-time> --query "AlarmHistoryItems[?starts_with(AlarmName,'avrmm-prod')].[Timestamp,AlarmName,HistorySummary]" --output text`;
     `avrmm-prod-rds-connections` and the healthy-host alarms are the likely ones).
   Record which emails arrived and how long after T_down. A missing email is a
   finding: fix the subscription or alarm and rerun.

## Rollback / abort

- Once the reboot is requested it cannot be cancelled; the standby takes over.
  The old primary comes back as the standby, so there is nothing to undo.
- Abort before step 2 if a pre-check fails.
- If the services do not recover within 10 minutes: force a new deployment of
  worker then web (`aws ecs update-service --profile avrmm-prod --region eu-west-2 --cluster avrmm-prod-cluster --service <name> --force-new-deployment`),
  look at the logs, and treat it as an incident; record it below. If the database
  itself is unhealthy, see [restore-rehearsal.md](restore-rehearsal.md) for the
  restore method.

## Record

| Date | Operator | Result | Outage (T_down to T_up) | App reconnect time | RDS events / alarm emails received | Notes |
|---|---|---|---|---|---|---|
| 2026-11-20 | Claude (approved by Daniel) | Pass | DB failover 44 s (started 15:10:54Z, completed 15:11:38Z); no user traffic, 0 ALB 5xx | No reconnect errors logged by web or worker; no task replaced | RDS events present; RDS event emails received by Daniel (failover started + completed); no CloudWatch alarm state changes (none expected) | First rehearsal, before go-live. Primary moved eu-west-2a → eu-west-2c (zone fields took ~2 min to update after "completed"). DB-backed probe not run (no Public API token in prod yet) — measured from RDS events, app logs and ALB 5xx; create a read-only Public API token before the next run. |
