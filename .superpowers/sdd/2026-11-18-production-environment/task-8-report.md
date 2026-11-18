# Task 8 report

## cloud-service
New variables (all defaults keep UAT unchanged): role "" (MARCUS_ROLE only when set), cluster_arn "" (module creates cluster as before), extra_environment [] , listener_rules [] (empty = one rule from listener_rule_priority/listener_host_names, path /*; empty path_patterns omits path condition), container_insights false.
Outputs added: cluster_arn, target_group_arn_suffix (service_name already existed; cluster_name now derived from the ARN).
Cluster + capacity providers use count; listener rule uses for_each keyed by priority. moved blocks: cluster, capacity providers -> [0]; listener rule -> ["100"] (UAT's priority).
UAT sets container_insights = true (was hardcoded enabled) so it stays identical.

## ses
New: manage_dns (default true), mail_from_subdomain (default ""); hosted_zone_id now defaults "". manage_dns=false creates no route53 record and no verification resource. Added aws_ses_domain_mail_from (+MX/SPF records when managed), output dns_records listing every record needed. moved blocks for domain_verification, verification, spf, dmarc -> [0] (dkim already counted).

## envs/uat
extra_environment BRANDED_DOMAIN = var.dns_zone_name (uat.involvecloud.com); portal NEXT_PUBLIC_AV_BRIDGE_BRANDED_DOMAIN = var.dns_zone_name.

## Plan (uat.plan left in envs/uat, untracked)
Plan: 1 to add, 3 to change, 1 to destroy.
- aws_ecs_task_definition.this: -/+ (new revision; forced only by container_definitions, the normal ECS revision behaviour; no way around it for an env change)
- aws_ecs_service.this: in-place (task_definition ARN only)
- portal aws_amplify_app.this, aws_amplify_branch.this: in-place (+1 env var each)
- moves only (no change): ecs cluster, capacity providers, listener rule, ses domain_verification record, verification, spf, dmarc.
No other changes. fmt -recursive clean, validate Success.
Concern: tfvars copied into worktree is git-ignored (verified); uat.plan is NOT ignored by .gitignore, so it is untracked and uncommitted.

## Fix round 1
- cloud-service: local.svc = role == "" ? name_prefix : "<name_prefix>-<role>", used for log group, execution/task roles, execution-secrets policy, task family, target group and service names (cluster keeps name_prefix). Precondition on the target group fails if "<svc>-cloud" exceeds 32 chars ("avrmm-prod-worker-cloud" = 23, fits).
- Restored valid UTF-8 in the host_header comment (ASCII "-").
- av-bridge-ops/terraform/.gitignore: added *.plan, *.tfplan; `git check-ignore` reports envs/uat/uat.plan ignored.
- Ran fmt -recursive, validate (Success), plan -out uat.plan: Plan: 1 to add, 3 to change, 1 to destroy; same resources/actions as before (task def -/+ revision, service + Amplify app/branch in-place, moves only). No UAT renames.
