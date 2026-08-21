param(
    [switch]$ValidateOnly
)

$ErrorActionPreference = 'Stop'

$Region = 'eu-north-1'
$Cluster = 'limiance-staging'
$Family = 'limiance-staging-api'
$Service = 'limiance-staging-api'
$Image = '041659147758.dkr.ecr.eu-north-1.amazonaws.com/limiance-backend:staging-065eb41'
$ExecutionRole = 'limiance-staging-ecs-execution'
$TaskRole = 'limiance-staging-ecs-task'
$ApiSecurityGroup = 'sg-0b6b43d8368e0db2a'
$DatabaseInstance = 'limiance-staging'
$ApplicationSecret = 'limiance/staging/app'
$LogGroup = '/ecs/limiance-staging'
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
$SubnetText = & $Aws ec2 describe-subnets --region $Region --filters "Name=vpc-id,Values=$VpcId" --query 'Subnets[].SubnetId' --output text
$Subnets = @($SubnetText -split '\s+' | Where-Object { $_ } | Select-Object -First 2)
if ($Subnets.Count -lt 2) { throw 'At least two subnets are required for the Fargate deployment.' }

$AppSecretArn = & $Aws secretsmanager describe-secret --region $Region --secret-id $ApplicationSecret --query ARN --output text
$RdsSecretArn = & $Aws rds describe-db-instances --region $Region --db-instance-identifier $DatabaseInstance --query 'DBInstances[0].MasterUserSecret.SecretArn' --output text

$AppSecretKeys = @(
    'APP_ENV', 'HTTP_ADDRESS', 'AWS_REGION', 'SESSION_TTL',
    'WITHDRAWAL_ADDRESS_COOLDOWN', 'LOG_LEVEL',
    'BYBIT_MARKET_DATA_BASE_URL', 'FIREBLOCKS_BASE_URL',
    'FIREBLOCKS_JWKS_URL', 'CUSTODY_MODE',
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
foreach ($Field in @('host', 'port', 'username', 'password')) {
    $Name = "RDS_DB_$($Field.ToUpper())"
    $Secrets.Add([pscustomobject]@{
        name = $Name
        valueFrom = "${RdsSecretArn}:$Field`::"
    })
}

$Container = [pscustomobject]@{
    name = 'limiance-api'
    image = $Image
    essential = $true
    portMappings = @([pscustomobject]@{ containerPort = 8080; protocol = 'tcp' })
    environment = @([pscustomobject]@{ name = 'RDS_DB_NAME'; value = 'limiance' })
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

$NetworkConfig = [pscustomobject]@{
    awsvpcConfiguration = [pscustomobject]@{
        subnets = $Subnets
        securityGroups = @($ApiSecurityGroup)
        assignPublicIp = 'ENABLED'
    }
} | ConvertTo-Json -Depth 10 -Compress

$Overrides = [pscustomobject]@{
    containerOverrides = @([pscustomobject]@{
        name = 'limiance-api'
        command = @('/app/migrate')
    })
} | ConvertTo-Json -Depth 10 -Compress

$MigrationTaskArn = & $Aws ecs run-task --region $Region --cluster $Cluster --launch-type FARGATE --task-definition $TaskDefinitionArn --network-configuration $NetworkConfig --overrides $Overrides --query 'tasks[0].taskArn' --output text
if ($LASTEXITCODE -ne 0) { throw 'Migration task did not start.' }

& $Aws ecs wait tasks-stopped --region $Region --cluster $Cluster --tasks $MigrationTaskArn
$ExitCode = & $Aws ecs describe-tasks --region $Region --cluster $Cluster --tasks $MigrationTaskArn --query 'tasks[0].containers[0].exitCode' --output text
if ($ExitCode -ne '0') { throw "Migration failed with exit code $ExitCode. Check $LogGroup." }

$ServiceStatus = & $Aws ecs describe-services --region $Region --cluster $Cluster --services $Service --query 'services[0].status' --output text 2>$null
if ($ServiceStatus -eq 'ACTIVE') {
    & $Aws ecs update-service --region $Region --cluster $Cluster --service $Service --task-definition $TaskDefinitionArn --desired-count 2 --force-new-deployment | Out-Null
}
else {
    & $Aws ecs create-service --region $Region --cluster $Cluster --service-name $Service --task-definition $TaskDefinitionArn --desired-count 2 --launch-type FARGATE --network-configuration $NetworkConfig | Out-Null
}

& $Aws ecs wait services-stable --region $Region --cluster $Cluster --services $Service
Write-Host 'Migration succeeded. Two API tasks are running.'
