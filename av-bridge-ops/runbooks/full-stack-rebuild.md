# Full-stack rebuild

**Purpose.** Prove the whole production stack can be recreated from terraform
plus the latest database backup if the environment (or the account's resources)
were lost: network, database, secrets, ECR, services, load balancer, portal,
alarms. Find out what is missing from the code, how long it takes and what it
costs, then tear it down.

**When.** Semi-annual, and after large infrastructure changes. Allow a working
day: about 45 to 90 minutes of terraform apply, plus DNS and certificate waits,
plus checks, plus destroy.

**Who approves.** Daniel, **explicitly, for every `terraform apply` and every
destroy.** Nothing in this runbook may be run against `envs/prod`'s state, and no
apply or destroy command below may be run without that approval.

**Cost caveat.** While it exists the copy runs a Multi-AZ-sized database (use
single-AZ), NAT gateways (use one), an ALB, and Fargate tasks. Roughly 5 to 10
pounds per day. Destroy the same day. Customers are not affected **only if the
safety settings in "Known gaps" item 8 are in place**: the rebuild database is a
copy of real customer data (including their notification channels, webhooks and
ServiceNow settings), and a running worker could email,
call webhooks or raise tickets to real customers from the rebuild (so the worker is never started). This builds
real resources in the production account.

Related: [restore-rehearsal.md](restore-rehearsal.md) (restore and row-count
method reused here), [release-and-promote.md](release-and-promote.md) (image).

## Principle: a separate environment name

The rebuild uses its own **name prefix** (`avrmm-rb`), its own **state key**, its
own domains and its own secrets, so no resource name, state entry or DNS record of
production is touched. Production's state is `envs/prod/main.tfstate` in bucket
`avrmm-tf-state-prod` (lock table `avrmm-tf-lock-prod`); the rebuild must use a
different key such as `envs/rebuild-YYYYMMDD/main.tfstate`.

## Known gaps to close first (review and commit separately, one time)

`envs/prod` was written for one environment, so a faithful rebuild needs a few
parameters. The first run of this runbook will find more; record them in Notes.
Work on a **scratch copy** of the folder (do not edit `envs/prod` for the rebuild):

1. Copy the folder **without** terraform's local cache or any state:
   ```sh
   rsync -a --exclude .terraform --exclude '*.tfstate*' --exclude '*.tfplan' \
     av-bridge-ops/terraform/envs/prod/ av-bridge-ops/terraform/envs/rebuild/
   ```
   (No rsync? `cp -r av-bridge-ops/terraform/envs/prod av-bridge-ops/terraform/envs/rebuild`
   then `rm -rf av-bridge-ops/terraform/envs/rebuild/.terraform av-bridge-ops/terraform/envs/rebuild/*.tfstate*`.)
   Do not copy `.terraform/`: its cached backend points at production's state key,
   and after editing `backend.tf` `terraform init` would offer to copy that state
   into the rebuild key. Delete the folder after the run; never commit the copy's
   tfvars.
2. In the copy's `main.tf`: set the three `local.*_domain` values to rebuild
   names under a domain you control (for example `app.rb.involvecloud.com`,
   `api.rb.involvecloud.com`, `access.rb.involvecloud.com`). The `dns-zone`
   modules create hosted zones, so the parent DNS host (DNS Made Easy for
   `involvecloud.com`) needs NS delegation records for the `rb` names, and the ACM
   certificates validate through those zones. Add the delegation after the first
   partial apply (step 5).
3. Remove from the copy what is unique per account or already exists:
   `module.ses` and its three `import` blocks (the SES identity
   `notify.involvecloud.com` is shared and was created by hand), `module.github_oidc`
   (the account has one GitHub OIDC provider, already owned by production's
   state), and the `code_mirror` bucket resources (names are global, and the
   mirror is not part of the application).
4. In the copy's `backend.tf` change `key` to `envs/rebuild-YYYYMMDD/main.tfstate`.
   Leave bucket, lock table and profile unchanged.
5. `terraform.tfvars` in the copy: `name_prefix = "avrmm-rb"`, `single_nat_gateway = true`,
   and set the same non-secret values as production. Use a **test** email address
   for `alert_email` and `vendor_admin_email` so the rebuild never emails real
   people (do not write addresses into the repo).
6. Create the two secrets the code reads with data sources, with placeholder or
   copied values (the rebuild needs working ones only if you test sign-in):
   `avrmm-rb/github-token` (the GitHub token so Amplify can connect the repo; may
   be the same token) and `avrmm-rb/entra-vendor-client-secret`.
   ```sh
   aws secretsmanager create-secret --profile avrmm-prod --region eu-west-2 \
     --name avrmm-rb/github-token --secret-string '<paste-token-interactively>'
   aws secretsmanager create-secret --profile avrmm-prod --region eu-west-2 \
     --name avrmm-rb/entra-vendor-client-secret --secret-string '<placeholder>'
   ```
7. The database module cannot restore from a backup yet. Add (module change,
   its own reviewed commit) an optional `snapshot_identifier` variable passed to
   `aws_db_instance.this`. Also decide the Entra redirect URI: an app
   registration only accepts registered URIs, so Entra sign-in will not work on
   the rebuild domains unless you add a temporary redirect URI; skip sign-in in
   the test and say so in the Record.

8. **Safety settings (mandatory; prevents the rebuild contacting customers).**
   In the copy's `main.tf`, for **both** `cloud_web` and `cloud_worker`:
   `smtp_host = ""` (email stays in dry-run; also replace the other `module.ses.*`
   smtp arguments with `""`/defaults, since `module.ses` is removed),
   `nightly_dry_run = true`, `nightly_exec_enabled = false`. For `cloud_worker`
   also set `desired_count = 0`. The worker is where the Collector health watcher,
   nightly runs and notification delivery run: with no Collectors connected it
   would mark every Collector offline and raise alerts, which `internal/notify`
   would send to customers' real email, webhook, Teams and ServiceNow targets.
   The worker stays at `desired_count = 0` for the whole rebuild, with no
   exception: the database is a copy of real customer data with live customer
   notification targets, and there is no safe way here to switch those off.
   Only the web service is validated.

If any of these edits is not done, stop: a rebuild that silently reuses a
production name or state is worse than none.

## Pre-checks

1. `aws sso login --profile avrmm-prod`.
2. Take and name a fresh manual snapshot of production as the rebuild source (read
   only for production, and it also becomes the proof of the latest backup):
   ```sh
   SNAP=avrmm-prod-rebuild-src-$(date -u +%Y%m%d%H%M)
   aws rds create-db-snapshot --profile avrmm-prod --region eu-west-2 \
     --db-instance-identifier avrmm-prod-postgres --db-snapshot-identifier "$SNAP"
   aws rds wait db-snapshot-available --profile avrmm-prod --region eu-west-2 \
     --db-snapshot-identifier "$SNAP"
   ```
   Delete this snapshot at the end (step 13). The name does not start with
   `avrmm-prod-pre-`, so the promotion workflow's clean-up never touches it.
3. Confirm the image to use exists: `aws ecr describe-images --profile avrmm-prod --region eu-west-2 --repository-name avrmm-prod-cloud --image-ids imageTag=latest`.
   The rebuild ECR repository `avrmm-rb-cloud` starts empty, so step 8 copies the
   image in.
4. Confirm production's state is not locked and that you are in the copy:
   `cd av-bridge-ops/terraform/envs/rebuild && grep -n key backend.tf`. The key must
   contain `rebuild`.
5. Quota check for the account (VPCs, Elastic IPs, NAT gateways) is below the
   limits with the extra VPC: `aws service-quotas list-service-quotas --service-code vpc --query "Quotas[?QuotaName=='VPCs per Region'].Value" --output text --profile avrmm-prod --region eu-west-2`.

## Steps

1. Start the clock and record the date, the git commit of `main` and the snapshot id.
2. Initialise against the rebuild state key and check production is not in it. **Never
   answer yes if terraform offers to copy existing state** to the new backend: that
   would copy production's state into the rebuild key. Answer no and stop:
   ```sh
   cd av-bridge-ops/terraform/envs/rebuild
   terraform init -reconfigure
   terraform state list | wc -l      # expected: 0 (empty state)
   ```
3. Plan, read the whole plan, and check every resource name starts `avrmm-rb`
   (or is a data source / import excluded above), no resource has a production
   name, and nothing is replaced or destroyed:
   ```sh
   terraform plan -out rebuild.plan
   terraform show -no-color rebuild.plan | grep -E "^\s+(#|\+) .*(avrmm-prod|app\.involvecloud\.com|api\.involvecloud\.com|access\.involvecloud\.com)" | head -40
   ```
   Expected: no `avrmm-prod` names. Show the plan summary to Daniel.
4. **Approval gate: Daniel approves the apply.** Then apply in two phases so
   DNS can be delegated while the certificates wait. Phase one is the zones only:
   ```sh
   terraform apply -target=module.dns_app -target=module.dns_api -target=module.dns_access
   terraform output       # hosted zone name servers
   ```
5. Add the NS delegation records for the rebuild names at the parent DNS host.
   Check: `nslookup -type=NS api.rb.involvecloud.com 8.8.8.8` returns the zone's
   name servers. (Certificates validate over these records and stay pending until
   they resolve.)
6. **Approval gate: Daniel approves the full apply.** The step 3 plan is stale after
   the targeted apply, so always plan again, review it (same name checks as step 3,
   and confirm the safety settings from "Known gaps" item 8 appear), then apply
   that new plan (the long step, 30 to 60 minutes; RDS creation and ACM validation
   dominate):
   ```sh
   terraform plan -out rebuild2.plan
   terraform show -no-color rebuild2.plan | grep -E "avrmm-prod|app\.involvecloud\.com|api\.involvecloud\.com|access\.involvecloud\.com" | head
   terraform apply rebuild2.plan
   ```
   Expected: `Apply complete! Resources: N added, 0 changed, 0 destroyed.` If a
   resource already exists (a global name collision) or a variable is missing,
   record it as a gap, fix the copy, and re-plan.
7. Restore the data. The database is created empty by terraform (or, with the
   `snapshot_identifier` change from "Known gaps", from `$SNAP`). If it was
   created empty, replace it with the snapshot restore:
   - **DESTRUCTIVE (rebuild instance only):** confirm the identifier is
     `avrmm-rb-postgres`, never `avrmm-prod-postgres`, then restore `$SNAP` over
     it using `terraform apply -replace=module.db.aws_db_instance.this` with
     `snapshot_identifier` set, or restore by CLI and import it.
   - After the restore, reset the master password to the rebuild's secret (the
     restored database has production's master password). The services set the
     application role passwords from the secret at start-up, so nothing else is
     needed. The identifier must be `avrmm-rb-postgres`; the password never
     appears on screen:
     ```sh
     PW=$(aws secretsmanager get-secret-value --profile avrmm-prod --region eu-west-2 \
       --secret-id avrmm-rb/db/master --query SecretString --output text \
       | python -I -c "import sys,json;print(json.load(sys.stdin)['password'])")
     aws rds modify-db-instance --profile avrmm-prod --region eu-west-2 \
       --db-instance-identifier avrmm-rb-postgres --master-user-password "$PW" \
       --apply-immediately
     unset PW
     ```
   - Row counts (customers, devices, telemetry_daily) must match the snapshot
     time, using the check from [restore-rehearsal.md](restore-rehearsal.md)
     pointed at the rebuild instance. Record them.
7a. **Pre-start check (always).** Print the rebuild task definitions' settings and
   confirm them before any service starts:
   ```sh
   for f in web worker; do
     echo "== $f"
     aws ecs describe-task-definition --profile avrmm-prod --region eu-west-2 \
       --task-definition avrmm-rb-$f-cloud \
       --query 'taskDefinition.containerDefinitions[0].environment[?name==`NIGHTLY_DRY_RUN` || name==`NIGHTLY_EXEC_ENABLED` || name==`POC_SMTP_HOST` || name==`CLOUD_BASE_URL`].[name,value]' --output text
   done
   aws ecs describe-services --profile avrmm-prod --region eu-west-2 --cluster avrmm-rb-cluster \
     --services avrmm-rb-worker-cloud --query 'services[0].desiredCount' --output text
   ```
   Expected: `NIGHTLY_DRY_RUN true`, `NIGHTLY_EXEC_ENABLED false`, no `POC_SMTP_HOST`
   line (or an empty value), a `CLOUD_BASE_URL` on the rebuild domain, and worker
   desired count `0`. If any value differs, stop and fix the copy.
8. Get the application image into the rebuild repository and start it. ECR has
   no cross-repo copy, so pull and push with a temporary Docker config (the
   Windows credential-helper workaround from [laptop-release-fallback.md](laptop-release-fallback.md)):
   ```sh
   export DOCKER_CONFIG="$(mktemp -d)"; echo '{"auths":{"placeholder.invalid":{}}}' > "$DOCKER_CONFIG/config.json"
   ACC=568339496641.dkr.ecr.eu-west-2.amazonaws.com
   aws ecr get-login-password --profile avrmm-prod --region eu-west-2 | docker login --username AWS --password-stdin "$ACC"
   docker pull "$ACC/avrmm-prod-cloud:latest"
   docker tag  "$ACC/avrmm-prod-cloud:latest" "$ACC/avrmm-rb-cloud:latest"
   docker push "$ACC/avrmm-rb-cloud:latest"
   rm -rf "$DOCKER_CONFIG"; unset DOCKER_CONFIG
   # Web only. The worker stays at desired count 0 (Known gaps item 8).
   aws ecs update-service --profile avrmm-prod --region eu-west-2 --cluster avrmm-rb-cluster \
     --service avrmm-rb-web-cloud --force-new-deployment
   aws ecs wait services-stable --profile avrmm-prod --region eu-west-2 --cluster avrmm-rb-cluster \
     --services avrmm-rb-web-cloud
   ```
   Expected: the web service stable (names follow the module pattern; confirm with
   `aws ecs list-services --cluster avrmm-rb-cluster`). Production's tasks are on a
   different cluster and are not touched.
9. Validate end to end against the rebuild domain (not production):
   - `curl -fsS https://api.rb.involvecloud.com/healthz` returns OK (use the real
     rebuild domain you chose);
   - the portal (Amplify app `avrmm-rb-portal`) deployed and loads;
   - the API answers an authenticated read (use a read-only token created in the
     rebuild database; do not reuse a production customer's token);
   - the web tasks log no start-up errors (`aws logs tail /ecs/avrmm-rb-web-cloud --since 30m`); the worker is never started, so worker behaviour (nightly runs, Collector tunnel) is not validated by this runbook;
   - the migrations ran and the schema version matches production;
   - alarms exist (`aws cloudwatch describe-alarms --profile avrmm-prod --region eu-west-2 --alarm-name-prefix avrmm-rb`). The rebuild's worker running-tasks
     alarm (and worker healthy-hosts) will go to `ALARM` because the worker is at 0:
     expected. It emails the rebuild's own alert topic (the test address), not
     customers.
   Do **not** enrol real Collectors or let the rebuild reach customer devices or
   contact customers. The settings were checked in step 7a (they are copied from
   production, where nightly power commands and email are live). The rebuild
   database holds real customer data, so keep access to yourself and delete it the
   same day.
10. Stop the clock; record total time to a healthy stack (the rebuild RTO), the
    list of gaps found, and the cost estimate from Cost Explorer for the day.

### Tear down (**DESTRUCTIVE**: needs Daniel's explicit approval)

11. Confirm what will be destroyed. The state key must contain `rebuild`, and the
    plan must contain only `avrmm-rb` names:
    ```sh
    cd av-bridge-ops/terraform/envs/rebuild
    grep -n key backend.tf
    terraform plan -destroy -out destroy.plan
    terraform show -no-color destroy.plan | grep -E "avrmm-prod" | head   # expected: no output
    ```
12. **Approval gate: Daniel approves the destroy.** The rebuild database has
    deletion protection (copied from production), so first turn it off in the
    rebuild only (`aws rds modify-db-instance --profile avrmm-prod --region eu-west-2 --db-instance-identifier avrmm-rb-postgres --no-deletion-protection --apply-immediately`,
    and set `deletion_protection = false` in the copy), then:
    ```sh
    terraform apply destroy.plan
    ```
    Secrets have a recovery window; delete the rebuild's two hand-made secrets
    force-immediately so the prefix is clean:
    ```sh
    for s in avrmm-rb/github-token avrmm-rb/entra-vendor-client-secret; do
      aws secretsmanager delete-secret --profile avrmm-prod --region eu-west-2 --secret-id "$s" --force-delete-without-recovery
    done
    ```
13. Remove the delegation records you added at the parent DNS host, and delete
    the rebuild snapshot and state:
    ```sh
    aws rds delete-db-snapshot --profile avrmm-prod --region eu-west-2 --db-snapshot-identifier "$SNAP"
    KEY=envs/rebuild-YYYYMMDD/main.tfstate
    case "$KEY" in
      envs/rebuild-*) aws s3 rm "s3://avrmm-tf-state-prod/$KEY" --profile avrmm-prod ;;
      *) echo "refusing: $KEY" ;;
    esac
    ```
    Only delete the state after `terraform state list` in the rebuild folder is
    empty. Delete the scratch copy of the folder.
14. Verify nothing is left and production is intact:
    ```sh
    aws resourcegroupstaggingapi get-resources --profile avrmm-prod --region eu-west-2 \
      --query 'ResourceTagMappingList[?contains(ResourceARN,`avrmm-rb`)].ResourceARN' --output text
    aws rds describe-db-instances --profile avrmm-prod --region eu-west-2 --query 'DBInstances[].DBInstanceIdentifier' --output text
    curl -fsS https://api.involvecloud.com/healthz
    ```
    Expected: the first is empty, the second lists only `avrmm-prod-postgres`, and
    production is healthy. In `envs/prod`, `terraform plan` shows no changes
    (run it only if Daniel asks; it does not apply anything).

## Rollback / abort

- Abort at any approval gate by not approving. Before step 6 nothing costs money.
- If something goes wrong mid-apply, do not touch `envs/prod`. Fix the copy and
  re-run, or go straight to the tear-down (steps 11 to 14).
- If a command ever references `avrmm-prod-*` as a target of a change (other than
  the read-only snapshot and ECR pull above), stop and ask Daniel.

## Record

| Date | Operator | Result | Time to healthy stack | Time to destroy | Approx. cost | Gaps found / terraform fixes | Notes |
|---|---|---|---|---|---|---|---|
| | | | | | | | |
