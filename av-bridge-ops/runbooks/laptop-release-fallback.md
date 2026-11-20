# Laptop release fallback

**Purpose.** Build, sign, push and deploy the cloud image by hand when GitHub
Actions (or its AWS roles) cannot be used, for example during a GitHub outage or
a broken workflow, and a release or fix cannot wait.

**When.** Emergency only. Normal releases use
[release-and-promote.md](release-and-promote.md).

**Who approves.** Daniel, before step 8 (anything pushed to production).

**Customer impact.** Same rolling deploy as a normal promotion.

Prerequisites on the laptop: Docker with BuildKit, AWS CLI v2 with SSO profiles
`avrmm-uat` and `avrmm-prod`, Git Bash, `jq`, and the Collector update signing key
as a local file (called `<signing-key-file>` below; its location is not recorded
here, and it must never be committed, printed or put in a build arg).

## Pre-checks

1. The release commit is on `main` and tagged (`git tag -a vX.Y.Z -m ...` and
   `git push origin vX.Y.Z`); the production promotion normally requires this and
   the portal build uses it. If GitHub itself is down the tag cannot be pushed:
   create it locally, and **push it as soon as GitHub is back, before any later
   "Promote to production" run** (promote refuses a version whose tag is not on
   `main`, and the `release` branch step needs it).
2. `aws sso login --profile avrmm-prod` and, for the UAT leg, `--profile avrmm-uat`.
3. Working tree state does not matter: the build uses `git archive` of the tag.

## Steps

1. Export a clean copy of the tag (Git Bash):
   ```sh
   VERSION=vX.Y.Z
   SRC="$(mktemp -d)"
   git archive "$VERSION" | tar -x -C "$SRC"
   ```
2. Build and sign (the key is a BuildKit secret mount only):
   ```sh
   BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
   docker build -f "$SRC/av-bridge-cloud/Dockerfile" \
     --build-arg "BRIDGE_VERSION=$VERSION" \
     --build-arg "BRIDGE_BUILD_TIME=$BUILD_TIME" \
     --secret id=update_signing_key,src=<signing-key-file> \
     --provenance=false --sbom=false \
     -t "avrmm-cloud:$VERSION" "$SRC"
   ```
   Expected: build succeeds. If the secret is missing the Collector update
   manifest is unsigned and Collector self-update is switched off, so do not skip
   the check in step 3.
3. Check the signed manifest (the image is distroless, so copy the file out):
   ```sh
   cid="$(docker create "avrmm-cloud:$VERSION")"
   docker cp "$cid:/downloads/manifest.json" "$SRC/manifest.json"; docker rm "$cid"
   jq -r '.version' "$SRC/manifest.json"
   jq '[.files[] | .signature // empty | select(length>0)] | length' "$SRC/manifest.json"
   ```
   Expected: the version equals `vX.Y.Z` and the count is `3`. Otherwise stop.
4. Push to UAT first and test there. Windows Docker's credential helper can fail
   with "The stub received bad data", so use a temporary Docker config seeded
   with a placeholder entry:
   ```sh
   export DOCKER_CONFIG="$(mktemp -d)"
   echo '{"auths":{"placeholder.invalid":{}}}' > "$DOCKER_CONFIG/config.json"
   UAT=628088982408.dkr.ecr.eu-west-2.amazonaws.com
   aws ecr get-login-password --profile avrmm-uat --region eu-west-2 \
     | docker login --username AWS --password-stdin "$UAT"
   docker tag "avrmm-cloud:$VERSION" "$UAT/avrmm-uat-cloud:$VERSION"
   docker tag "avrmm-cloud:$VERSION" "$UAT/avrmm-uat-cloud:latest"
   docker push "$UAT/avrmm-uat-cloud:$VERSION"
   docker push "$UAT/avrmm-uat-cloud:latest"
   rm -rf "$DOCKER_CONFIG"; unset DOCKER_CONFIG
   ```
5. Deploy UAT and test:
   ```sh
   aws ecs update-service --profile avrmm-uat --region eu-west-2 \
     --cluster avrmm-uat-cluster --service avrmm-uat-cloud --force-new-deployment \
     --query 'service.deployments[0].rolloutState' --output text
   aws ecs wait services-stable --profile avrmm-uat --region eu-west-2 \
     --cluster avrmm-uat-cluster --services avrmm-uat-cloud
   curl -fsS https://api.uat.involvecloud.com/healthz
   ```
   If Actions is merely broken but GitHub is up, you can now run "Promote to
   production" (see [release-and-promote.md](release-and-promote.md)) and stop
   here. Continue only if the promote workflow cannot be used.
6. (Only if promote cannot be used) Record what production runs now, for rollback:
   ```sh
   aws ecr describe-images --profile avrmm-prod --region eu-west-2 \
     --repository-name avrmm-prod-cloud --image-ids imageTag=latest \
     --query 'imageDetails[0].[imageDigest,imageTags]' --output text
   ```
   Write the digest down.
7. (Only if promote cannot be used) **DESTRUCTIVE-ish: snapshot the production
   database first** so there is a restore point:
   ```sh
   SNAP="avrmm-prod-pre-${VERSION//./-}-$(date -u +%Y%m%d%H%M)"
   aws rds create-db-snapshot --profile avrmm-prod --region eu-west-2 \
     --db-instance-identifier avrmm-prod-postgres --db-snapshot-identifier "$SNAP"
   aws rds wait db-snapshot-available --profile avrmm-prod --region eu-west-2 \
     --db-snapshot-identifier "$SNAP"
   ```
8. **CUSTOMER-VISIBLE, needs Daniel's approval.** Push to production and roll
   worker, then web:
   ```sh
   export DOCKER_CONFIG="$(mktemp -d)"
   echo '{"auths":{"placeholder.invalid":{}}}' > "$DOCKER_CONFIG/config.json"
   PROD=568339496641.dkr.ecr.eu-west-2.amazonaws.com
   aws ecr get-login-password --profile avrmm-prod --region eu-west-2 \
     | docker login --username AWS --password-stdin "$PROD"
   docker tag "avrmm-cloud:$VERSION" "$PROD/avrmm-prod-cloud:$VERSION"
   docker tag "avrmm-cloud:$VERSION" "$PROD/avrmm-prod-cloud:latest"
   docker push "$PROD/avrmm-prod-cloud:$VERSION"
   docker push "$PROD/avrmm-prod-cloud:latest"
   rm -rf "$DOCKER_CONFIG"; unset DOCKER_CONFIG
   for s in avrmm-prod-worker-cloud avrmm-prod-web-cloud; do
     aws ecs update-service --profile avrmm-prod --region eu-west-2 \
       --cluster avrmm-prod-cluster --service $s --force-new-deployment \
       --query 'service.deployments[0].rolloutState' --output text
     aws ecs wait services-stable --profile avrmm-prod --region eu-west-2 \
       --cluster avrmm-prod-cluster --services $s
   done
   curl -fsS --retry 6 --retry-delay 10 --retry-all-errors https://api.involvecloud.com/healthz
   ```
   Expected: both services stable, health OK.
9. Move the portal: `git push origin "$(git rev-list -n1 $VERSION):refs/heads/release" --force`
   then check the Amplify job as in [release-and-promote.md](release-and-promote.md) step 6.
10. Clean up: `rm -rf "$SRC"` and remove the local image
    (`docker rmi avrmm-cloud:$VERSION`). Confirm the signing key file was not copied
    into `$SRC` or the repo.

## Rollback / abort

- Stop at any check that fails; nothing reaches production before step 8.
- After step 8, roll back with the previous digest from step 6: the commands are
  in the Rollback section of [release-and-promote.md](release-and-promote.md)
  (`batch-get-image`, `put-image --image-tag latest`, then roll worker and web).
- Data damage from a migration: see the same section; the snapshot from step 7
  is the restore point.

## Record

| Date | Operator | Version | Why the fallback was needed | Result | Notes |
|---|---|---|---|---|---|
| | | | | | |
