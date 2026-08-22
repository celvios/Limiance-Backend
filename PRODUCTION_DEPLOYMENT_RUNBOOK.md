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
