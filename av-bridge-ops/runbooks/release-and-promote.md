# Release and promote to production

**Purpose.** Ship a new cloud and portal version: tag on `main`, build and sign,
deploy to UAT automatically, then promote the same image to production on demand.

**When.** Every release.

**Who approves.** Daniel runs the "Promote to production" workflow. Nobody else
promotes.

**Customer impact.** Rolling ECS deploy, worker then web. No planned downtime,
but in-flight Collector long-polls reconnect. Prefer the Wednesday 21:00-23:00
window for anything that changes behaviour customers see.

Workflows: `.github/workflows/release-cloud.yml` (on a `v*` tag) and
`.github/workflows/promote.yml` (manual). Related:
[laptop-release-fallback.md](laptop-release-fallback.md) if Actions is down,
[restore-rehearsal.md](restore-rehearsal.md) for the snapshot the promotion takes.

## Versioning

Semver from v1.1.0: new features bump the minor (v1.2.0), fixes bump the patch
(v1.1.1), breaking changes bump the major (v2.0.0). Tags are annotated, on `main`.

## Pre-checks

1. The change is merged to `main` and CI is green.
2. `git fetch --tags` then `git tag --list 'v*' --sort=-v:refname | head -3` to
   confirm the next version is unused.
3. If the release contains a database migration, read it. Migrations run on
   start-up and are not reversed by rolling the image back (see Rollback).
4. GitHub repo secret `UPDATE_SIGNING_KEY` is set (the release refuses to build
   unsigned).
5. Production is healthy before you start: `curl -fsS https://api.involvecloud.com/healthz`.

## Steps

### A. Release to UAT

1. On `main`, create and push an annotated tag:
   ```sh
   git switch main && git pull --ff-only
   git tag -a v1.1.0 -m "v1.1.0: <one-line summary>"
   git push origin v1.1.0
   ```
2. Watch GitHub Actions, workflow "Release cloud". It builds the image once
   (which also builds and signs the Collector binaries), checks the manifest
   (`version == tag`, exactly 3 signatures), pushes `:<tag>` and `:latest` to
   `avrmm-uat-cloud`, rolls service `avrmm-uat-cloud` in `avrmm-uat-cluster`, waits
   for it to be stable, then checks `https://api.uat.involvecloud.com/healthz`.
   Expected: green run, log line `manifest OK: version v1.1.0, 3 signatures`.
3. Test in UAT (the changed features, Collector enrolment if touched). Do not
   promote until UAT is accepted. UAT must hold the exact tag:
   ```sh
   aws ecr describe-images --profile avrmm-uat --region eu-west-2 \
     --repository-name avrmm-uat-cloud --image-ids imageTag=v1.1.0 \
     --query 'imageDetails[0].imageDigest' --output text
   ```
   Expected: a `sha256:...` digest.

### B. Promote to production (CUSTOMER-VISIBLE: rolling deploy)

4. Get Daniel's go-ahead for the time of day.
5. GitHub, Actions, "Promote to production", **Use workflow from: main**, enter
   `version` = `v1.1.0`, Run. The workflow, in order:
   1. checks it runs on `main` and the version matches `vN.N.N`;
   2. checks the tag exists and is an ancestor of `main`;
   3. with the UAT role: confirms the image is in `avrmm-uat-cloud`, pulls it and
      checks `/downloads/manifest.json` carries this version;
   4. with the prod role: takes RDS snapshot
      `avrmm-prod-pre-v1-1-0-YYYYMMDDHHMM` of `avrmm-prod-postgres` (dots in the
      version become hyphens; an earlier attempt's snapshot under 6 hours old is
      reused) and waits until it is available;
   5. deletes `avrmm-prod-pre-*` snapshots older than 365 days (nothing else);
   6. records the current `:latest` digest and tags of `avrmm-prod-cloud`;
   7. pushes `:v1.1.0` and `:latest` to `avrmm-prod-cloud`;
   8. force-rolls `avrmm-prod-worker-cloud`, waits stable, then
      `avrmm-prod-web-cloud`, waits stable;
   9. health check `https://api.involvecloud.com/healthz` (6 retries);
   10. pushes the tag's commit to the `release` branch, which makes Amplify
       build the matching portal.
6. When it finishes, confirm:
   ```sh
   curl -fsS https://api.involvecloud.com/healthz
   aws ecs describe-services --profile avrmm-prod --region eu-west-2 \
     --cluster avrmm-prod-cluster \
     --services avrmm-prod-web-cloud avrmm-prod-worker-cloud \
     --query 'services[].[serviceName,runningCount,desiredCount,deployments[0].rolloutState]' --output text
   aws amplify list-jobs --profile avrmm-prod --region eu-west-2 \
     --app-id d3mm9kij0w4gk --branch-name release --max-results 1 \
     --query 'jobSummaries[0].[status,commitId]' --output text
   ```
   Expected: healthz OK; web `2 2 COMPLETED`, worker `1 1 COMPLETED`; the Amplify
   job `SUCCEED` with the tag's commit id. If the job is missing or `FAILED`,
   retry it in the Amplify console (a webhook has missed a push before).
7. Check no alarm is firing:
   ```sh
   aws cloudwatch describe-alarms --profile avrmm-prod --region eu-west-2 \
     --alarm-name-prefix avrmm-prod --state-value ALARM \
     --query 'MetricAlarms[].AlarmName' --output text
   ```
   Expected: empty output.

## Rollback / abort

- **Before step 5:** nothing changed in production; just stop.
- **Promotion fails after the image push began:** the run's Summary prints the
  exact commands and the snapshot id. In outline (use the digest from the
  Summary or from the step "Record the current production image"):
  ```sh
  aws ecr batch-get-image --profile avrmm-prod --region eu-west-2 \
    --repository-name avrmm-prod-cloud --image-ids imageDigest=<previous-digest> \
    --query 'images[0].imageManifest' --output text > prev-manifest.json
  aws ecr put-image --profile avrmm-prod --region eu-west-2 \
    --repository-name avrmm-prod-cloud --image-tag latest \
    --image-manifest file://prev-manifest.json
  for s in avrmm-prod-worker-cloud avrmm-prod-web-cloud; do
    aws ecs update-service --profile avrmm-prod --region eu-west-2 \
      --cluster avrmm-prod-cluster --service $s --force-new-deployment
    aws ecs wait services-stable --profile avrmm-prod --region eu-west-2 \
      --cluster avrmm-prod-cluster --services $s
  done
  curl -fsS https://api.involvecloud.com/healthz
  rm prev-manifest.json
  ```
- **Portal:** re-run Promote with the previous version, or push the previous
  tag's commit to `release`: `git push origin <commit>:refs/heads/release --force`.
- **A migration changed data or schema:** rolling the image back does not undo
  it. Do not restore casually: a restore replaces current data. Decide with
  Daniel, using snapshot `avrmm-prod-pre-...` and the method in
  [restore-rehearsal.md](restore-rehearsal.md); repointing production at a
  restored instance is a terraform and secrets change, so plan it deliberately.
- Promoted the wrong version: promote the right one. Promotion is repeatable and
  takes a fresh snapshot each time.

## Record

One row per production promotion (the tag and the GitHub run hold the detail).

| Date | Operator | Version | Result | Promote run duration | Notes |
|---|---|---|---|---|---|
| | | | | | |
