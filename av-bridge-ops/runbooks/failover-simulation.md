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
   aws rds describe-db-instances --db-instance-identifier avrmm-prod-postgres \
     --query 'DBInstances[0].[DBInstanceStatus,MultiAZ,AvailabilityZone,SecondaryAvailabilityZone]' --output text
   ```
   Expected: `available True <az-a> <az-b>`. Note the primary AZ.
3. Services are healthy and no alarm is firing:
   ```sh
   curl -fsS https://api.involvecloud.com/healthz
   aws ecs describe-services --cluster avrmm-prod-cluster \
     --services avrmm-prod-web-cloud avrmm-prod-worker-cloud \
     --query 'services[].[serviceName,runningCount,desiredCount]' --output text
   aws cloudwatch describe-alarms --alarm-name-prefix avrmm-prod --state-value ALARM \
     --query 'MetricAlarms[].AlarmName' --output text
   ```
   Expected: healthz OK, web `2 2`, worker `1 1`, no alarms.
4. No release or restore rehearsal is in progress, and the announcement (if
   needed) has gone out.
5. You know who receives the alert emails (the address subscribed to SNS topic
   `avrmm-prod-alerts` and the `avrmm-prod-rds-events` subscription: categories
   failure, failover, low storage) and that the subscription is confirmed:
   ```sh
   aws sns list-subscriptions-by-topic \
     --topic-arn arn:aws:sns:eu-west-2:568339496641:avrmm-prod-alerts \
     --query 'Subscriptions[].[Protocol,SubscriptionArn]' --output text
   ```
   Expected: `email arn:aws:sns:...` (not `PendingConfirmation`).

## Steps

1. Start a health probe in a second terminal. It logs one line every 2 seconds
   with a UTC timestamp and HTTP status, so the outage window can be read off:
   ```sh
   while true; do
     printf '%s %s\n' "$(date -u +%T)" \
       "$(curl -s -o /dev/null -m 3 -w '%{http_code}' https://api.involvecloud.com/healthz)"
     sleep 2
   done | tee failover-probe.log
   ```
2. **CUSTOMER-VISIBLE.** Start the failover and note the time:
   ```sh
   date -u +%FT%TZ
   aws rds reboot-db-instance --db-instance-identifier avrmm-prod-postgres \
     --force-failover \
     --query 'DBInstance.DBInstanceStatus' --output text
   ```
   Expected: `rebooting`.
3. Watch RDS events until the failover completes (about 1 to 2 minutes):
   ```sh
   aws rds describe-events --source-type db-instance \
     --source-identifier avrmm-prod-postgres --duration 30 \
     --query 'Events[].[Date,Message]' --output text
   ```
   Expected messages include "Multi-AZ instance failover started" and
   "Multi-AZ instance failover completed".
4. In the probe log, note: **T_down** (first non-200 or timeout), **T_up** (first
   200 after the gap). Outage = T_up - T_down. Stop the probe with Ctrl+C once it
   has returned 200 for a minute. Note that `/healthz` may stay 200 if it does
   not touch the database; if the log shows no gap, also test a database-backed
   call (for example the portal sign-in page loading data, or
   `GET /pub/v1/...` with a test token) and record which probe you used.
5. Check the instance is available in the other zone:
   ```sh
   aws rds describe-db-instances --db-instance-identifier avrmm-prod-postgres \
     --query 'DBInstances[0].[DBInstanceStatus,AvailabilityZone,SecondaryAvailabilityZone]' --output text
   ```
   Expected: `available`, and the two zones swapped compared with pre-check 2.
6. Check the application reconnected without a restart:
   ```sh
   aws ecs describe-services --cluster avrmm-prod-cluster \
     --services avrmm-prod-web-cloud avrmm-prod-worker-cloud \
     --query 'services[].[serviceName,runningCount,desiredCount,deployments[0].rolloutState]' --output text
   aws logs tail /ecs/avrmm-prod-web-cloud --since 15m | grep -i -E "error|connection|reset|refused" | tail -20
   aws logs tail /ecs/avrmm-prod-worker-cloud --since 15m | grep -i -E "error|connection|reset|refused" | tail -20
   ```
   Expected: counts unchanged (`2 2`, `1 1`), no task replacement, errors only
   within the failover window and none afterwards. Also check the Collectors list
   in the portal shows them online again, and a command sent to a device (or a
   fresh telemetry reading) flows. Record the time until the first successful
   database-backed request as "app reconnect time".
7. Check the alerting arrived. Within about 10 minutes:
   - RDS event emails for failover (via `avrmm-prod-rds-events`);
   - any CloudWatch alarm that moved to `ALARM` and back
     (`aws cloudwatch describe-alarm-history --alarm-name-prefix avrmm-prod --history-item-type StateUpdate --start-date <start-time> --query 'AlarmHistoryItems[].[Timestamp,AlarmName,HistorySummary]' --output text`;
     `avrmm-prod-rds-connections` and the healthy-host alarms are the likely ones).
   Record which emails arrived and how long after T_down. A missing email is a
   finding: fix the subscription or alarm and rerun.

## Rollback / abort

- Once the reboot is requested it cannot be cancelled; the standby takes over.
  The old primary comes back as the standby, so there is nothing to undo.
- Abort before step 2 if a pre-check fails.
- If the services do not recover within 10 minutes: force a new deployment of
  worker then web (`aws ecs update-service --cluster avrmm-prod-cluster --service <name> --force-new-deployment`),
  look at the logs, and treat it as an incident; record it below. If the database
  itself is unhealthy, see [restore-rehearsal.md](restore-rehearsal.md) for the
  restore method.

## Record

| Date | Operator | Result | Outage (T_down to T_up) | App reconnect time | RDS events / alarm emails received | Notes |
|---|---|---|---|---|---|---|
| | | | | | | |
