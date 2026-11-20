# Restore rehearsal

**Purpose.** Prove that the production database backups actually restore, check
the restored data against production, and measure how long a restore takes (RTO)
and how much data a point-in-time restore can lose (RPO). A backup that has never
been restored is not a backup.

**When.** Quarterly, and after any change to the database module, KMS key or
backup settings. Takes about 45 minutes, mostly waiting.

**Who approves.** Daniel. The restore creates a temporary instance in `avrmm-prod`
(costs a few pounds per day while it exists) and deletes it at the end. Nothing
here touches production data or services, and nothing is started against the
restored instance except read-only checks.

Related: [failover-simulation.md](failover-simulation.md),
[full-stack-rebuild.md](full-stack-rebuild.md),
[release-and-promote.md](release-and-promote.md) (the `avrmm-prod-pre-*` snapshots).

## Approach to querying the restored instance

The restored instance must not be publicly accessible, there is no bastion, RDS
Query Editor does not support plain RDS PostgreSQL, and the production cloud image
is distroless (no `psql`). So the checks run in a **one-off Fargate task inside
the production VPC**, on the same subnets and security group as the services (the
database security group already allows that group on 5432). The task uses the
official `postgres:16-alpine` image from the public ECR mirror
(`public.ecr.aws/docker/library/postgres`, reached through the NAT gateways), the
worker's existing execution role, and the existing `DATABASE_ADMIN_URL` secret
(role `app_admin`, which bypasses row-level security so counts are real). The
script swaps the host for the restored instance, runs `count(*)` queries, does the
same against production for comparison, and the output lands in CloudWatch Logs
(`/ecs/avrmm-prod-worker-cloud`, stream prefix `restore-check`). The restored
instance is a copy, so it has the same credentials as production.

Note for all runbooks: code blocks inside numbered steps are indented. When a block
contains a heredoc (`<<'SCRIPT'`, `<<JSON`), remove the list indent before pasting so
the closing marker starts in column 0.

## Pre-checks

1. `aws sso login --profile avrmm-prod`; use `export AWS_PROFILE=avrmm-prod AWS_REGION=eu-west-2`
   for the commands below.
2. No other rehearsal instance exists:
   ```sh
   aws rds describe-db-instances --profile avrmm-prod --region eu-west-2 --query 'DBInstances[?starts_with(DBInstanceIdentifier,`avrmm-prod-restore`)].DBInstanceIdentifier' --output text
   ```
   Expected: empty.
3. Backups exist and the window is covered:
   ```sh
   aws rds describe-db-instances --profile avrmm-prod --region eu-west-2 --db-instance-identifier avrmm-prod-postgres \
     --query 'DBInstances[0].[BackupRetentionPeriod,LatestRestorableTime]' --output text
   aws rds describe-db-snapshots --profile avrmm-prod --region eu-west-2 --db-instance-identifier avrmm-prod-postgres --snapshot-type automated \
     --query 'DBSnapshots[].[DBSnapshotIdentifier,SnapshotCreateTime,Status]' --output text
   ```
   Expected: retention `35`; `LatestRestorableTime` within about 5 minutes of now;
   automated snapshots `available`.
4. Capture the production facts used later:
   ```sh
   aws rds describe-db-instances --profile avrmm-prod --region eu-west-2 --db-instance-identifier avrmm-prod-postgres \
     --query 'DBInstances[0].[DBInstanceClass,DBSubnetGroup.DBSubnetGroupName,VpcSecurityGroups[0].VpcSecurityGroupId,DBParameterGroups[0].DBParameterGroupName]' --output text
   ```
   Expected today: `db.t4g.medium avrmm-prod-db sg-0dc5eb729b72a94cd avrmm-prod-db-pg16`.
   If these differ, use the live values.

## Steps

### A. Restore to a temporary instance

1. Pick the identifier, capture the restore data point, and start the clock. Same
   instance class as production so the RTO is honest; no Multi-AZ, not public, no
   deletion protection:
   ```sh
   RID=avrmm-prod-restore-$(date -u +%Y%m%d)
   # Data point for RPO, read just before the restore request:
   aws rds describe-db-instances --profile avrmm-prod --region eu-west-2 --db-instance-identifier avrmm-prod-postgres \
     --query 'DBInstances[0].LatestRestorableTime' --output text
   T0=$(date -u +%s); echo "start $(date -u +%FT%TZ)"
   aws rds restore-db-instance-to-point-in-time --profile avrmm-prod --region eu-west-2 \
     --source-db-instance-identifier avrmm-prod-postgres \
     --target-db-instance-identifier "$RID" \
     --use-latest-restorable-time \
     --db-instance-class db.t4g.medium \
     --db-subnet-group-name avrmm-prod-db \
     --vpc-security-group-ids sg-0dc5eb729b72a94cd \
     --db-parameter-group-name avrmm-prod-db-pg16 \
     --no-publicly-accessible --no-multi-az --no-deletion-protection \
     --tags Key=purpose,Value=restore-rehearsal Key=delete-after,Value=same-day \
     --query 'DBInstance.[DBInstanceIdentifier,DBInstanceStatus]' --output text
   ```
   Expected: `<RID> creating`. To rehearse restoring a specific snapshot instead,
   use `restore-db-instance-from-db-snapshot --db-snapshot-identifier <id>` with
   the same networking flags.
2. Work out the RPO: RPO = time of the restore request in step 1 minus the
   `LatestRestorableTime` printed just before it (normally under 5 minutes). A
   restore with `--use-latest-restorable-time` recovers to about that point.
3. Wait until available, then stop the clock:
   ```sh
   aws rds wait --profile avrmm-prod --region eu-west-2 db-instance-available --db-instance-identifier "$RID"
   T1=$(date -u +%s); echo "restore took $(( (T1-T0)/60 )) min"
   aws rds describe-db-instances --profile avrmm-prod --region eu-west-2 --db-instance-identifier "$RID" \
     --query 'DBInstances[0].[DBInstanceStatus,PubliclyAccessible,MultiAZ,Endpoint.Address]' --output text
   ```
   Expected: `available False False <endpoint>`. The elapsed minutes are the
   measured RTO for the database restore (add the time to repoint services in a
   real incident). If `wait` times out (it polls for about 60 minutes), check
   `aws rds describe-events --profile avrmm-prod --region eu-west-2 --source-type db-instance --source-identifier "$RID" --duration 120`.

### B. Check the data from inside the VPC

4. Register a throwaway task definition (this adds an ECS task-definition
   revision; it writes nothing to any database). First the check script, which
   only runs `select count(*)` and `max(day)`; it is passed to the container
   base64-encoded so no quoting survives into the JSON:
   ```sh
   cat > restore-check.sh <<'SCRIPT'
   for h in "$RESTORE_HOST" prod; do
     if [ "$h" = prod ]; then u="$DATABASE_ADMIN_URL"
     else
       u=$(printf %s "$DATABASE_ADMIN_URL" | sed -E "s#@[^/@]+/#@$h/#")
       [ "$u" != "$DATABASE_ADMIN_URL" ] || { echo "host swap failed"; exit 1; }
     fi
     echo "== $h"
     psql "$u" -At -c "select 'server_addr', inet_server_addr()" 2>&1 | sed -E 's#postgres(ql)?://[^ ]*#<url>#'
     psql "$u" -At -c "select 'customers', count(*) from customers union all select 'devices', count(*) from devices union all select 'telemetry_daily', count(*) from telemetry_daily" 2>&1 | sed -E 's#postgres(ql)?://[^ ]*#<url>#'
     psql "$u" -At -c "select 'telemetry_daily_latest_day', max(day) from telemetry_daily" 2>&1 | sed -E 's#postgres(ql)?://[^ ]*#<url>#'
   done
   echo CHECK_DONE
   SCRIPT
   B64=$(base64 -w0 restore-check.sh)
   ```
   Then the task definition. The secret reference is copied from the live worker
   task definition so it is never typed here:
   ```sh
   EXEC_ROLE=$(aws ecs describe-task-definition --profile avrmm-prod --region eu-west-2 --task-definition avrmm-prod-worker-cloud \
     --query 'taskDefinition.executionRoleArn' --output text)
   SECRET_REF=$(aws ecs describe-task-definition --profile avrmm-prod --region eu-west-2 --task-definition avrmm-prod-worker-cloud \
     --query 'taskDefinition.containerDefinitions[0].secrets[?name==`DATABASE_ADMIN_URL`].valueFrom' --output text)
   RESTORE_HOST=$(aws rds describe-db-instances --profile avrmm-prod --region eu-west-2 --db-instance-identifier "$RID" \
     --query 'DBInstances[0].Endpoint.Address' --output text)
   cat > restore-check-taskdef.json <<JSON
   {
     "family": "avrmm-prod-restore-check",
     "requiresCompatibilities": ["FARGATE"],
     "networkMode": "awsvpc",
     "cpu": "256", "memory": "512",
     "executionRoleArn": "$EXEC_ROLE",
     "containerDefinitions": [{
       "name": "psql",
       "image": "public.ecr.aws/docker/library/postgres:16-alpine",
       "essential": true,
       "entryPoint": ["sh", "-c"],
       "command": ["echo $B64 | base64 -d | sh"],
       "environment": [{"name": "RESTORE_HOST", "value": "$RESTORE_HOST"}],
       "secrets": [{"name": "DATABASE_ADMIN_URL", "valueFrom": "$SECRET_REF"}],
       "logConfiguration": {"logDriver": "awslogs", "options": {
         "awslogs-group": "/ecs/avrmm-prod-worker-cloud",
         "awslogs-region": "eu-west-2", "awslogs-stream-prefix": "restore-check"}}
     }]
   }
   JSON
   aws ecs register-task-definition --profile avrmm-prod --region eu-west-2 --cli-input-json file://restore-check-taskdef.json \
     --query 'taskDefinition.taskDefinitionArn' --output text
   ```
   Expected: an ARN ending `avrmm-prod-restore-check:1`. The script and JSON
   contain no secret values (ECS injects the URL at run time, and the script
   masks any URL that appears in error output).
5. Run it once on the production network (no public IP, same subnets and group as
   the services) and wait:
   ```sh
   TASK=$(aws ecs run-task --profile avrmm-prod --region eu-west-2 --cluster avrmm-prod-cluster --launch-type FARGATE \
     --task-definition avrmm-prod-restore-check \
     --network-configuration 'awsvpcConfiguration={subnets=[subnet-00077fa04c31e6e0c,subnet-07f7319705cb348f3,subnet-0ff63068b8f582fa6],securityGroups=[sg-0e25328a456001dfe],assignPublicIp=DISABLED}' \
     --query 'tasks[0].taskArn' --output text)
   aws ecs wait --profile avrmm-prod --region eu-west-2 tasks-stopped --cluster avrmm-prod-cluster --tasks "$TASK"
   aws ecs describe-tasks --profile avrmm-prod --region eu-west-2 --cluster avrmm-prod-cluster --tasks "$TASK" \
     --query 'tasks[0].[stopCode,containers[0].exitCode,stoppedReason]' --output text
   ```
   Expected: exit code `0`. If the task stops with an image-pull error, the NAT
   path to `public.ecr.aws` is blocked: mirror `postgres:16-alpine` into an ECR
   repo you create temporarily and use that image instead.
6. Read the output (the task id is the last part of the task ARN):
   ```sh
   aws logs get-log-events --profile avrmm-prod --region eu-west-2 --log-group-name /ecs/avrmm-prod-worker-cloud \
     --log-stream-name "restore-check/psql/${TASK##*/}" --query 'events[].message' --output text
   ```
   Expected: two blocks, the restored host and `prod`, each with a `server_addr`
   line, `customers`, `devices`, `telemetry_daily` counts, then `CHECK_DONE`.
   Pass criteria: the two `server_addr` values **differ** (if they are equal, or the
   output says `host swap failed`, the restored block queried production or did not
   run: the check is void, fix and rerun); then
   `customers` and `devices` equal production (or differ by at most the changes
   since the restore point); `telemetry_daily` within a few percent of production
   (it grows continuously); no query errors. Record the counts in the Record table
   (counts are not personal data; do not paste any customer names).

### C. Clean up (DESTRUCTIVE: deletes the temporary instance)

7. Check the identifier is the temporary one, then delete it without a final
   snapshot. Never run this against `avrmm-prod-postgres` (it also has deletion
   protection on):
   ```sh
   # If the shell restarted, recover RID from the instance list (pre-check 2 query):
   #   aws rds describe-db-instances --profile avrmm-prod --region eu-west-2 --query 'DBInstances[?starts_with(DBInstanceIdentifier,`avrmm-prod-restore-`)].DBInstanceIdentifier' --output text
   case "$RID" in
     avrmm-prod-restore-2*)
       aws rds delete-db-instance --profile avrmm-prod --region eu-west-2 --db-instance-identifier "$RID" \
         --skip-final-snapshot --delete-automated-backups \
         --query 'DBInstance.DBInstanceStatus' --output text
       aws rds wait --profile avrmm-prod --region eu-west-2 db-instance-deleted --db-instance-identifier "$RID" ;;
     *) echo "refusing: $RID" ;;
   esac
   ```
8. Confirm deletion and remove the helper files:
   ```sh
   aws rds describe-db-instances --profile avrmm-prod --region eu-west-2 --db-instance-identifier "$RID" 2>&1 | head -2
   aws rds describe-db-instances --profile avrmm-prod --region eu-west-2 --query 'DBInstances[].DBInstanceIdentifier' --output text
   aws ecs deregister-task-definition --profile avrmm-prod --region eu-west-2 --task-definition avrmm-prod-restore-check:1 >/dev/null
   rm -f restore-check-taskdef.json restore-check.sh
   ```
   Expected: `DBInstanceNotFound` for `$RID`; the instance list shows only
   `avrmm-prod-postgres`. (Deregister every revision you created.)

## Rollback / abort

- Nothing here changes production. To abort at any point, jump to step 7.
- If the restore fails to start (for example a KMS or subnet-group error), the
  error message is the finding: record it, fix it in terraform, rerun.
- Do not leave the instance running: it holds a full copy of customer data.

## Record

| Date | Operator | Result | Restore time (RTO) | Data point (RPO) | customers (restored / prod) | devices (restored / prod) | telemetry_daily (restored / prod) | Notes |
|---|---|---|---|---|---|---|---|---|
| | | | | | | | | |
