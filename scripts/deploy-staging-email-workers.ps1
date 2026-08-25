param(
    [switch]$ValidateOnly
)

$ErrorActionPreference = 'Stop'

$Region = 'eu-north-1'
$Cluster = 'limiance-staging'
$Image = '041659147758.dkr.ecr.eu-north-1.amazonaws.com/limiance-backend:staging-phase1-accounts-security-r1'
$ExecutionRole = 'limiance-staging-ecs-execution'
$TaskRole = 'limiance-staging-ecs-task'
$ApiSecurityGroup = 'sg-0b6b43d8368e0db2a'
$DatabaseInstance = 'limiance-staging'
$ApplicationSecret = 'limiance/staging/app'
$LogGroup = '/ecs/limiance-staging'
$Aws = (Get-Command aws -ErrorAction SilentlyContinue).Source
if ([string]::IsNullOrWhiteSpace($Aws)) { $Aws = 'C:\Program Files\Amazon\AWSCLIV2\aws.exe' }

if (-not (Test-Path -LiteralPath $Aws)) { throw 'AWS CLI is not installed at the expected path.' }
if ($ValidateOnly) { Write-Host 'Worker deployment script syntax and local prerequisites are valid.'; exit 0 }

$AccountId = & $Aws sts get-caller-identity --no-cli-pager --region $Region --query Account --output text
if ($LASTEXITCODE -ne 0) { throw 'AWS authentication failed. Run aws login, then retry.' }
$ExecutionRoleArn = "arn:aws:iam::$AccountId`:role/$ExecutionRole"
$TaskRoleArn = "arn:aws:iam::$AccountId`:role/$TaskRole"

$VpcId = & $Aws rds describe-db-instances --no-cli-pager --region $Region --db-instance-identifier $DatabaseInstance --query 'DBInstances[0].DBSubnetGroup.VpcId' --output text
$RdsHost = & $Aws rds describe-db-instances --no-cli-pager --region $Region --db-instance-identifier $DatabaseInstance --query 'DBInstances[0].Endpoint.Address' --output text
$RdsPort = & $Aws rds describe-db-instances --no-cli-pager --region $Region --db-instance-identifier $DatabaseInstance --query 'DBInstances[0].Endpoint.Port' --output text
$SubnetText = & $Aws ec2 describe-subnets --no-cli-pager --region $Region --filters "Name=vpc-id,Values=$VpcId" --query 'Subnets[].SubnetId' --output text
$Subnets = @($SubnetText -split '\s+' | Where-Object { $_ } | Select-Object -First 2)
if ($Subnets.Count -lt 2) { throw 'At least two subnets are required for the Fargate workers.' }
$NetworkConfig = "awsvpcConfiguration={subnets=[$($Subnets -join ',')],securityGroups=[$ApiSecurityGroup],assignPublicIp=ENABLED}"

$AppSecretArn = & $Aws secretsmanager describe-secret --no-cli-pager --region $Region --secret-id $ApplicationSecret --query ARN --output text
$RdsSecretArn = & $Aws rds describe-db-instances --no-cli-pager --region $Region --db-instance-identifier $DatabaseInstance --query 'DBInstances[0].MasterUserSecret.SecretArn' --output text

$required = @('SENDGRID_API_KEY', 'SENDGRID_FROM_EMAIL', 'VERIFICATION_ENCRYPTION_KEY', 'SQS_OUTBOX_QUEUE_URL', 'SQS_NOTIFICATIONS_QUEUE_URL', 'SQS_DEPOSITS_QUEUE_URL', 'SQS_UNROUTED_QUEUE_URL')
$secretValue = & $Aws secretsmanager get-secret-value --no-cli-pager --region $Region --secret-id $ApplicationSecret --query SecretString --output text
if ($LASTEXITCODE -ne 0) { throw 'Could not read the staging application secret.' }
$secretObject = $secretValue | ConvertFrom-Json
$secretData = @{}
foreach ($property in $secretObject.PSObject.Properties) { $secretData[$property.Name] = $property.Value }
$missing = @($required | Where-Object { -not $secretData.ContainsKey($_) -or [string]::IsNullOrWhiteSpace([string]$secretData[$_]) })
if ($missing.Count -gt 0) { throw "Cannot start email workers; missing staging secret keys: $($missing -join ', '). Run configure-staging-email.ps1 first." }

$secretKeys = @('AWS_REGION', 'LOG_LEVEL', 'SENDGRID_API_KEY', 'SENDGRID_FROM_EMAIL', 'SENDGRID_VERIFICATION_TEMPLATE_ID', 'VERIFICATION_ENCRYPTION_KEY', 'SQS_OUTBOX_QUEUE_URL', 'SQS_NOTIFICATIONS_QUEUE_URL', 'SQS_DEPOSITS_QUEUE_URL', 'SQS_UNROUTED_QUEUE_URL')
function New-SecretList {
    $list = [System.Collections.Generic.List[object]]::new()
    foreach ($key in $secretKeys) {
        $list.Add([pscustomobject]@{ name = $key; valueFrom = "${AppSecretArn}:$key`::" })
    }
    return ,$list
}

function Deploy-Worker([string]$Name, [string]$Command, [bool]$NeedsDatabase) {
    $secrets = New-SecretList
    if ($NeedsDatabase) { $secrets.Add([pscustomobject]@{ name = 'RDS_SECRET_JSON'; valueFrom = $RdsSecretArn }) }
    $environment = @()
    if ($NeedsDatabase) {
        $environment = @(
            [pscustomobject]@{ name = 'RDS_DB_HOST'; value = $RdsHost },
            [pscustomobject]@{ name = 'RDS_DB_PORT'; value = "$RdsPort" },
            [pscustomobject]@{ name = 'RDS_DB_NAME'; value = 'postgres' }
        )
    }
    $container = [pscustomobject]@{
        name = $Name; image = $Image; essential = $true; command = @("/app/$Command")
        environment = $environment; secrets = @($secrets)
        logConfiguration = [pscustomobject]@{ logDriver = 'awslogs'; options = [pscustomobject]@{
            'awslogs-group' = $LogGroup; 'awslogs-region' = $Region; 'awslogs-stream-prefix' = $Name
        }}
    }
    $taskDefinition = [pscustomobject]@{
        family = "limiance-staging-$Name"; networkMode = 'awsvpc'; requiresCompatibilities = @('FARGATE')
        cpu = '256'; memory = '512'; executionRoleArn = $ExecutionRoleArn; taskRoleArn = $TaskRoleArn
        containerDefinitions = @($container)
    }
    $file = New-TemporaryFile
    try {
        $taskDefinition | ConvertTo-Json -Depth 20 | Set-Content -LiteralPath $file -NoNewline
        $arn = & $Aws ecs register-task-definition --no-cli-pager --region $Region --cli-input-json "file://$file" --query 'taskDefinition.taskDefinitionArn' --output text
        if ($LASTEXITCODE -ne 0) { throw "Task definition registration failed for $Name." }
    } finally { Remove-Item -LiteralPath $file -ErrorAction SilentlyContinue }
    $status = & $Aws ecs describe-services --no-cli-pager --region $Region --cluster $Cluster --services "limiance-staging-$Name" --query 'services[0].status' --output text 2>$null
    if ($status -eq 'ACTIVE') {
        & $Aws ecs update-service --no-cli-pager --region $Region --cluster $Cluster --service "limiance-staging-$Name" --task-definition $arn --desired-count 1 --force-new-deployment | Out-Null
    } else {
        & $Aws ecs create-service --no-cli-pager --region $Region --cluster $Cluster --service-name "limiance-staging-$Name" --task-definition $arn --desired-count 1 --launch-type FARGATE --network-configuration $NetworkConfig | Out-Null
    }
    if ($LASTEXITCODE -ne 0) { throw "Could not deploy $Name worker." }
}

Deploy-Worker -Name 'outbox' -Command 'outbox' -NeedsDatabase $true
Deploy-Worker -Name 'event-router' -Command 'event-router' -NeedsDatabase $false
Deploy-Worker -Name 'deposits' -Command 'deposits' -NeedsDatabase $true
Deploy-Worker -Name 'notifications' -Command 'notifications' -NeedsDatabase $true
Write-Host 'Workers requested: outbox, event-router, deposits, notifications. Each runs one small Fargate task.'
