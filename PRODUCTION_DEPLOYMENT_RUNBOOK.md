# Limiance Production Deployment Runbook

This runbook is for the transition from the 50-user staging environment to a production environment that can handle real customers and real funds. Production must use a separate AWS account or, at minimum, separate VPC, secrets, databases, queues, Fireblocks workspace, domains, and IAM roles from staging.

## Preconditions

- Staging has passed functional testing, including registration, email verification, login, KYC, deposit detection and crediting, conversion, withdrawal hold/cancel, and webhook replay handling.
- Ledger reconciliation has been reviewed and signed off for staging test activity.
- Fireblocks production workspace, Sumsub production, messaging/email providers, and chain-risk/compliance providers are contracted and configured.
- Legal, AML/KYC, Travel Rule, sanctions, privacy, and jurisdictional requirements have been approved for the launch markets.
- No real customer funds are accepted until withdrawal approval, custody broadcast lifecycle, reconciliation, monitoring, and incident processes are exercised.

## Separate production foundation

- Region: choose deliberately and keep dependent services in the same region. Do not reuse staging credentials.
- Network: VPC spanning at least two Availability Zones. Public subnets contain only the ALB and NAT gateways. ECS, RDS, Redis, and workers run in private subnets.
- Network egress: use NAT gateways or VPC endpoints for ECR, CloudWatch Logs, Secrets Manager, SQS, STS, and KMS. Do not assign public IPs to production ECS tasks.
- HTTPS: ACM certificate, Application Load Balancer, TLS-only listener, HTTP-to-HTTPS redirect, and a WAF baseline.
- DNS: separate production API and admin domains. The admin application must use a separate origin and session policy.

## Database and stateful services

- RDS PostgreSQL: Multi-AZ, encrypted storage, automated backups with point-in-time recovery, deletion protection, maintenance window, Performance Insights, and at least 30-day backup retention.
- Create a dedicated `limiance` database before first migration. Do not use the default `postgres` database in production.
- RDS security group: PostgreSQL `5432` allowed only from ECS API/worker security groups.
- Redis: Multi-AZ replication group, encryption in transit/at rest, automated backups, and access only from ECS security groups.
- SQS: separate queues and DLQs per environment. Alarm on queue age, depth, DLQ activity, and repeated consumer failures.

## Secrets and IAM

- Store provider credentials and encryption keys only in Secrets Manager/KMS. Do not place secrets in images, task-definition environment literals, Git, CI logs, or frontend code.
- Use the RDS managed secret for database credentials. ECS injects it as `RDS_SECRET_JSON`; the application builds the connection safely at runtime.
- Rotate all credentials previously used in local development or shared through chat.
- Use separate ECS execution and task roles with only required permissions. No AdministratorAccess on workloads.
- Require MFA, least-privilege human IAM roles, CloudTrail, and identity-access review.

## ECS deployment model

- Use immutable image tags and record image digest with every release. Never deploy production by mutable `latest` or `staging` tag.
- Run at least two API tasks across Availability Zones behind the ALB. Enable deployment circuit breaker and automatic rollback.
- Run migrations as a one-off task before the API release. Migrations must be idempotent and logged.
- Run separate ECS services for outbox, event router, deposits, notifications, and reconciliation workers. No worker exposes an HTTP listener publicly.
- API target group health check: `/healthz`; operational readiness check: `/readyz`.
- Configure autoscaling based on CPU, memory, request count, and queue depth. Set explicit minimum and maximum task counts.

## Custody, money movement, and compliance

- Use Fireblocks production credentials and production webhook/JWKS endpoints only after sandbox exit criteria are met.
- Enforce maker-checker approvals for irreversible and high-value operations. A requester cannot approve their own action.
- Reconcile custody balances, blockchain activity, and the double-entry ledger on a scheduled basis. Alert on any mismatch.
- Enforce KYC tier limits, address cooldowns, velocity controls, chain-risk screening, Travel Rule policy decisions, and sanctions checks server-side.
- Keep hot, warm, and cold wallet exposure limits documented and reviewed. Cold-wallet movement is human-approved and scheduled.
- Enable withdrawal kill switch and conversion kill switch; test both before launch.

## Observability and incident readiness

- CloudWatch: structured logs, metrics, dashboards, and alarms for ALB 4xx/5xx, ECS task restarts, RDS CPU/storage/connections, Redis, SQS, webhook failures, and reconciliation exceptions.
- Error monitoring: capture application errors without secrets, payment data, KYC documents, OTPs, session tokens, or raw Travel Rule data.
- Security monitoring: CloudTrail, GuardDuty, AWS Config, Security Hub, and alerts for IAM/secret/security-group changes.
- Incident runbooks: withdrawals disabled, custody provider unavailable, webhook backlog, RDS failure, credential rotation, chain reorg, suspected account takeover, and ledger discrepancy.
- Perform restore testing from RDS backups and a tabletop incident exercise before launch.

## Release gate

Production launch requires written approval from engineering, security, operations/treasury, and compliance. Confirm all of the following:

- Deployment is healthy across at least two API tasks.
- Migrations are complete and schema version recorded.
- External provider webhooks reach the production HTTPS endpoints and pass signature verification.
- All secrets are production-only and rotation owners are assigned.
- Security-group review confirms no database, Redis, or worker public exposure.
- Reconciliation reports match and alerting is tested.
- Rollback image and rollback procedure are ready.
- Support and incident contacts are staffed for launch.

## Production deployment sequence

Use the deployment sequence below as the controlled cutover procedure for the first production release and any later hotfixes that require a live release.

### 1. Final readiness freeze

- Confirm no unreviewed database schema drift, config drift, or provider credential changes remain in progress.
- Lock the production release branch or tag and record the release commit SHA, image digest, migration version, and operator name.
- Confirm the launch window, support coverage, and incident command structure are in place.
- Ensure all production-facing providers are in their live mode, not sandbox mode, and that their production webhook endpoints are reachable from the internet.

### 2. Pre-deploy validation

- Verify the production image was built from the approved commit and signed if your process requires signing.
- Confirm ECS task definitions reference the intended image digest, environment variables, secrets, and security groups.
- Confirm the ALB target group and health checks are pointing at the correct service and port.
- Confirm RDS, Redis, SQS, and CloudWatch are all accessible from the production ECS tasks only through private networking.
- Validate provider configuration for Fireblocks, KYC, email, SMS, webhook callbacks, and chain-risk integrations against production credentials.

### 3. Migrations and schema gate

- Run the migration task in a one-off ECS task or equivalent deployment job before switching traffic to the new API version.
- Check application logs for migration start, completion, and exit status. Capture the exact migration version and timestamp.
- Validate that the application can connect to the production database using the runtime-managed secret and can read the new schema version.
- Do not proceed if any migration fails, hangs, or leaves the schema in a partially upgraded state.

### 4. Deploy the new version

- Deploy the API service first with a single controlled rollout, keeping at least two healthy tasks in service before raising traffic.
- Deploy workers and background consumers after the API reaches healthy status and the migration gate is green.
- Confirm ECS deployment circuit breaker is enabled and that rollback is configured to a previously known-good image.
- Watch ALB health check results, task restarts, CPU, and memory consumption throughout the rollout.

### 5. Production smoke checks

Run a short, explicit smoke suite immediately after deployment and before broad customer access:

- POST to the health check endpoint and verify `200 OK` from `/healthz` and `/readyz`.
- Confirm the API can authenticate a test user and issue a valid session/token pair.
- Test KYC submission and status reporting using a safe test profile or a controlled staging-like account that does not involve real funds.
- Verify deposit webhook ingestion, deposit detection, and ledger crediting with a known test wallet or mock provider callback in production-safe conditions.
- Verify withdrawal initiation, approval flow, and kill-switch behavior in a non-live or low-risk scenario.
- Confirm alerts are firing for a synthetic error or test queue event to prove the monitoring path is working.
- Ensure the outbox, event router, and notification workers are actively processing without backlog growth.

### 6. Traffic cutover

- Begin with a minimal traffic percentage, if your architecture supports gradual routing or weighted target groups.
- Increase traffic only after the new version remains healthy for a defined observation period with no elevated errors or invalid queue backlog.
- Keep the old version warm and available for quick rollback until the release has resided in production for a sufficient observation window.
- Notify the support team, compliance contacts, treasury, and operations lead that the release is live and the incident bridge is standing by.

### 7. Post-launch verification window

- Monitor for at least 30 minutes or a longer defined period depending on the release scope and risk profile.
- Check ALB latency, 4xx/5xx rate, queue depth, DLQ volume, webhook retry counts, and ledger reconciliation drift.
- Review application logs for errors involving custody, deposits, withdrawals, conversions, notifications, or auth flows.
- Verify that customer-facing systems remain operational without data corruption, duplicate crediting, or missing notifications.
- Confirm the rollback trigger conditions remain clear and the rollback owner is available.

## Rollback plan

Rollback readiness is mandatory for every production deployment. Rollback must be practiced before launch and documented in a release ticket or runbook.

### Trigger conditions

Rollback should happen immediately when one or more of the following occurs:

- Health checks fail across the production API fleet and the issue cannot be remediated within the defined incident window.
- Migrations leave the database in a broken or partial state, or the application cannot safely read/write the schema.
- Significant ledger discrepancies, duplicate fund credits, or missing settlement events appear after the rollout.
- External provider integrations fail in a way that prevents core deposit, withdrawal, or custody operations from being performed safely.
- Authentication, authorization, or compliance controls stop working as designed.
- Unrecoverable queue backlog or consumer deadlocks create operational risk.

### Rollback procedure

- Stop or pause new traffic to the failing version using the ALB, weighted target groups, or a manual service drain.
- Revert the ECS service to the last known-good image digest and task definition.
- Run the rollback database check: confirm the schema is consistent with the previous version and that no partial migration remains.
- If the release included a new incompatible migration, restore from the latest approved backup or use the pre-approved recovery path only after engineering and DB owner sign-off.
- Re-enable traffic only after the old version is healthy, migrations are validated, and the rollback is confirmed in logs.
- Communicate the rollback clearly to support, compliance, and operations, and document the root cause before the next release attempt.

### Rollback validation

- Verify `/healthz` and `/readyz` are green again on the recovered tasks.
- Confirm backlog drains or worker queues recover without data loss or duplicate processing.
- Validate that deposit, withdrawal, KYC, and ledger reconciliation flows operate as expected on the reverted version.
- Review infrastructure alerts to ensure no unhandled outage remains.
- Record the incident and postmortem actions before closing the release.

## Daily operational guardrails

- Review CloudWatch dashboards and alerts daily during launch and at least once per shift during the first week of production operation.
- Check queue depth, DLQ count, provider webhook failures, and reconciliation mismatches before the business day begins.
- Verify backups, PITR readiness, and encryption key rotation schedules remain on track.
- Review IAM access changes, security group modifications, and secret rotation events for any unauthorized activity.
- Keep a written log of production issues, mitigation actions, and owner assignments.

## Launch closeout

Production is considered stable only after the launch observation window has passed without critical incident, reconciliation drift, or provider failure. At closeout:

- Save the final release record with commit SHA, image digest, migration version, and approval names.
- Archive the deployment logs, support notes, and rollback evidence in the engineering change record.
- Confirm the incident bridge, escalation chain, and on-call ownership remain assigned for the next release cycle.
- Schedule the first retrospective to review launch performance, operational issues, and changes needed before the next production release.
