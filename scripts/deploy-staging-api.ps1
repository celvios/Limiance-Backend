param(
    [switch]$ValidateOnly,
    [string]$ImageTag = 'staging-phase1-accounts-security-r1',
    [switch]$BootstrapTestAdministrator,
    [switch]$EnableStagingConversions
)

$ErrorActionPreference = 'Stop'

$Region = 'eu-north-1'
$Cluster = 'limiance-staging'
$Family = 'limiance-staging-api'
$Service = 'limiance-staging-api'
$Image = "041659147758.dkr.ecr.eu-north-1.amazonaws.com/limiance-backend:$ImageTag"
$ExecutionRole = 'limiance-staging-ecs-execution'
$TaskRole = 'limiance-staging-ecs-task'
$ApiSecurityGroup = 'sg-0b6b43d8368e0db2a'
$DatabaseInstance = 'limiance-staging'
$ApplicationSecret = 'limiance/staging/app'
$LogGroup = '/ecs/limiance-staging'
$AllowedBrowserOrigins = 'https://staging.celvios.site,https://admin.celvios.site'
$Aws = 'C:\Program Files\Amazon\AWSCLIV2\aws.exe'

if (-not (Test-Path -LiteralPath $Aws)) {
    throw 'AWS CLI is not installed at the expected path.'
}

if ($ValidateOnly) {
    Write-Host 'Script syntax and local prerequisites are valid.'
    exit 0
}

$AccountId = & $Aws sts get-caller-identity --region $Region --query Account --output text
if ($LASTEXITCODE -ne 0) { throw 'AWS authentication failed. Run aws login, then retry.' }

$ExecutionRoleArn = "arn:aws:iam::$AccountId`:role/$ExecutionRole"
$TaskRoleArn = "arn:aws:iam::$AccountId`:role/$TaskRole"

$VpcId = & $Aws rds describe-db-instances --region $Region --db-instance-identifier $DatabaseInstance --query 'DBInstances[0].DBSubnetGroup.VpcId' --output text
$RdsHost = & $Aws rds describe-db-instances --region $Region --db-instance-identifier $DatabaseInstance --query 'DBInstances[0].Endpoint.Address' --output text
$RdsPort = & $Aws rds describe-db-instances --region $Region --db-instance-identifier $DatabaseInstance --query 'DBInstances[0].Endpoint.Port' --output text
$SubnetText = & $Aws ec2 describe-subnets --region $Region --filters "Name=vpc-id,Values=$VpcId" --query 'Subnets[].SubnetId' --output text
$Subnets = @($SubnetText -split '\s+' | Where-Object { $_ } | Select-Object -First 2)
if ($Subnets.Count -lt 2) { throw 'At least two subnets are required for the Fargate deployment.' }

$AppSecretArn = & $Aws secretsmanager describe-secret --region $Region --secret-id $ApplicationSecret --query ARN --output text
$RdsSecretArn = & $Aws rds describe-db-instances --region $Region --db-instance-identifier $DatabaseInstance --query 'DBInstances[0].MasterUserSecret.SecretArn' --output text

$AppSecretKeys = @(
    'APP_ENV', 'HTTP_ADDRESS', 'AWS_REGION', 'SESSION_TTL',
    'WITHDRAWAL_ADDRESS_COOLDOWN', 'LOG_LEVEL',
	'VERIFICATION_CODE_PEPPER', 'VERIFICATION_ENCRYPTION_KEY',
	'TOTP_ENCRYPTION_KEY', 'TRAVEL_RULE_ENCRYPTION_KEY',
    'SUMSUB_APP_TOKEN', 'SUMSUB_SECRET_KEY', 'SUMSUB_WEBHOOK_SECRET', 'SUMSUB_LEVEL_NAME',
    'TWILIO_ACCOUNT_SID', 'TWILIO_API_KEY', 'TWILIO_API_SECRET', 'TWILIO_VERIFY_SERVICE_SID',
    'GOOGLE_CLIENT_ID', 'GOOGLE_CLIENT_SECRET', 'GOOGLE_REDIRECT_URL',
    'TELEGRAM_CLIENT_ID', 'TELEGRAM_CLIENT_SECRET', 'TELEGRAM_REDIRECT_URL',
    'GEETEST_CAPTCHA_ID', 'GEETEST_PRIVATE_KEY',
    'BYBIT_MARKET_DATA_BASE_URL', 'FIREBLOCKS_API_KEY',
    'FIREBLOCKS_PRIVATE_KEY', 'FIREBLOCKS_BASE_URL', 'FIREBLOCKS_JWKS_URL',
    'CUSTODY_MODE',
    'SELF_CUSTODY_TESTNET_ENABLED', 'SQS_OUTBOX_QUEUE_URL',
    'SQS_NOTIFICATIONS_QUEUE_URL', 'SQS_DEPOSITS_QUEUE_URL',
    'SQS_UNROUTED_QUEUE_URL'
)

$Secrets = [System.Collections.Generic.List[object]]::new()
foreach ($Key in $AppSecretKeys) {
    $Secrets.Add([pscustomobject]@{
        name = $Key
        valueFrom = "${AppSecretArn}:$Key`::"
    })
}
$Secrets.Add([pscustomobject]@{
    name = 'RDS_SECRET_JSON'
    valueFrom = $RdsSecretArn
})

$Container = [pscustomobject]@{
    name = 'limiance-api'
    image = $Image
    essential = $true
    portMappings = @([pscustomobject]@{ containerPort = 8080; protocol = 'tcp' })
    environment = @(
        [pscustomobject]@{ name = 'RDS_DB_HOST'; value = $RdsHost },
        [pscustomobject]@{ name = 'RDS_DB_PORT'; value = "$RdsPort" },
		# Browser origins are public configuration. Keep the customer frontend
		# explicit; the administrator application must use its own API/session.
		[pscustomobject]@{ name = 'CORS_ALLOWED_ORIGINS'; value = $AllowedBrowserOrigins },
        # RDS created without an initial database supplies the default postgres
        # database. Staging uses it until a dedicated application database is
        # provisioned through a controlled migration/admin task.
        [pscustomobject]@{ name = 'RDS_DB_NAME'; value = 'postgres' }
    )
    secrets = @($Secrets)
    logConfiguration = [pscustomobject]@{
        logDriver = 'awslogs'
        options = [pscustomobject]@{
            'awslogs-group' = $LogGroup
            'awslogs-region' = $Region
            'awslogs-stream-prefix' = 'api'
        }
    }
}

$TaskDefinition = [pscustomobject]@{
    family = $Family
    networkMode = 'awsvpc'
    requiresCompatibilities = @('FARGATE')
    cpu = '512'
    memory = '1024'
    executionRoleArn = $ExecutionRoleArn
    taskRoleArn = $TaskRoleArn
    containerDefinitions = @($Container)
}

$TaskFile = New-TemporaryFile
try {
    $TaskDefinition | ConvertTo-Json -Depth 20 | Set-Content -LiteralPath $TaskFile -NoNewline
    $TaskDefinitionArn = & $Aws ecs register-task-definition --region $Region --cli-input-json "file://$TaskFile" --query 'taskDefinition.taskDefinitionArn' --output text
    if ($LASTEXITCODE -ne 0) { throw 'Task definition registration failed.' }
}
finally {
    Remove-Item -LiteralPath $TaskFile -ErrorAction SilentlyContinue
}

$NetworkConfig = "awsvpcConfiguration={subnets=[$($Subnets -join ',')],securityGroups=[$ApiSecurityGroup],assignPublicIp=ENABLED}"
$Overrides = 'containerOverrides=[{name=limiance-api,command=[/app/migrate]}]'

$RunResult = & $Aws ecs run-task --region $Region --cluster $Cluster --launch-type FARGATE --task-definition $TaskDefinitionArn --network-configuration $NetworkConfig --overrides $Overrides --output json | ConvertFrom-Json
if ($LASTEXITCODE -ne 0) { throw 'Migration task request failed.' }
if (-not $RunResult.tasks -or $RunResult.tasks.Count -eq 0) {
    $Failures = $RunResult.failures | ConvertTo-Json -Depth 10 -Compress
    throw "Migration task did not start. ECS reported: $Failures"
}
$MigrationTaskArn = $RunResult.tasks[0].taskArn

& $Aws ecs wait tasks-stopped --region $Region --cluster $Cluster --tasks $MigrationTaskArn
$MigrationStatus = & $Aws ecs describe-tasks --region $Region --cluster $Cluster --tasks $MigrationTaskArn --output json | ConvertFrom-Json
$ExitCode = $MigrationStatus.tasks[0].containers[0].exitCode
if ($ExitCode -ne 0) {
    $StoppedReason = $MigrationStatus.tasks[0].stoppedReason
    throw "Migration failed with exit code $ExitCode. ECS stopped reason: $StoppedReason. Check $LogGroup."
}

$ServiceStatus = & $Aws ecs describe-services --region $Region --cluster $Cluster --services $Service --query 'services[0].status' --output text 2>$null
if ($ServiceStatus -eq 'ACTIVE') {
    & $Aws ecs update-service --region $Region --cluster $Cluster --service $Service --task-definition $TaskDefinitionArn --desired-count 2 --force-new-deployment | Out-Null
}
else {
    & $Aws ecs create-service --region $Region --cluster $Cluster --service-name $Service --task-definition $TaskDefinitionArn --desired-count 2 --launch-type FARGATE --network-configuration $NetworkConfig | Out-Null
}

& $Aws ecs wait services-stable --region $Region --cluster $Cluster --services $Service
if ($LASTEXITCODE -ne 0) {
    throw 'ECS service did not reach a stable state. Re-authenticate with aws login, then inspect the service events and CloudWatch logs.'
}
if ($BootstrapTestAdministrator) {
    $BootstrapOverrides = 'containerOverrides=[{name=limiance-api,command=[/app/bootstrap-platform-admin,-email,toluking001@gmail.com,-confirm-staging]}]'
    $BootstrapRun = & $Aws ecs run-task --region $Region --cluster $Cluster --launch-type FARGATE --task-definition $TaskDefinitionArn --network-configuration $NetworkConfig --overrides $BootstrapOverrides --output json | ConvertFrom-Json
    if ($LASTEXITCODE -ne 0 -or -not $BootstrapRun.tasks -or $BootstrapRun.tasks.Count -eq 0) { throw 'Staging administrator bootstrap task did not start.' }
    $BootstrapTaskArn = $BootstrapRun.tasks[0].taskArn
    & $Aws ecs wait tasks-stopped --region $Region --cluster $Cluster --tasks $BootstrapTaskArn
    $BootstrapStatus = & $Aws ecs describe-tasks --region $Region --cluster $Cluster --tasks $BootstrapTaskArn --output json | ConvertFrom-Json
    if ($BootstrapStatus.tasks[0].containers[0].exitCode -ne 0) { throw 'Staging administrator bootstrap failed. Check the ECS task logs.' }
    Write-Host 'Staging administrator bootstrap succeeded.'
}
if ($EnableStagingConversions) {
    $ConversionOverrides = 'containerOverrides=[{name=limiance-api,command=[/app/enable-staging-conversions]}]'
    $ConversionRun = & $Aws ecs run-task --region $Region --cluster $Cluster --launch-type FARGATE --task-definition $TaskDefinitionArn --network-configuration $NetworkConfig --overrides $ConversionOverrides --output json | ConvertFrom-Json
    if ($LASTEXITCODE -ne 0 -or -not $ConversionRun.tasks -or $ConversionRun.tasks.Count -eq 0) { throw 'Staging conversion enablement task did not start.' }
    $ConversionTaskArn = $ConversionRun.tasks[0].taskArn
    & $Aws ecs wait tasks-stopped --region $Region --cluster $Cluster --tasks $ConversionTaskArn
    $ConversionStatus = & $Aws ecs describe-tasks --region $Region --cluster $Cluster --tasks $ConversionTaskArn --output json | ConvertFrom-Json
    if ($ConversionStatus.tasks[0].containers[0].exitCode -ne 0) { throw 'Staging conversion route enablement failed. Check the ECS task logs.' }
    Write-Host 'Staging conversion routes and control enabled.'
}
Write-Host 'Migration succeeded. Two API tasks are running.'
