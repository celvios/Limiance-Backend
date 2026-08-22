param(
    [Parameter(Mandatory = $true)]
    [string]$CertificateArn
)

$ErrorActionPreference = 'Stop'

$Region = 'eu-north-1'
$Cluster = 'limiance-staging'
$Service = 'limiance-staging-api'
$DatabaseInstance = 'limiance-staging'
$AlbName = 'limiance-staging-alb'
$TargetGroupName = 'limiance-staging-api'
$AlbSecurityGroup = 'sg-0cabd6951f1bc2b76'
$ApiSecurityGroup = 'sg-0b6b43d8368e0db2a'
$Aws = 'C:\Program Files\Amazon\AWSCLIV2\aws.exe'

if (-not (Test-Path -LiteralPath $Aws)) {
    throw 'AWS CLI is not installed at the expected path.'
}

function Invoke-Aws {
    param(
        [switch]$AllowDuplicatePermission,
        [Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments
    )

    # Redirect stderr to a file instead of the PowerShell error stream. This
    # prevents Windows PowerShell and PowerShell 7 from terminating early and
    # lets us show the original AWS CLI diagnostic when a command fails.
    $stderrFile = New-TemporaryFile
    $previousErrorActionPreference = $ErrorActionPreference
    try {
        # Some PowerShell installations promote non-zero native exit codes to
        # NativeCommandError when ErrorActionPreference is Stop. We evaluate
        # the exit code immediately below instead.
        $ErrorActionPreference = 'Continue'
        $result = & $Aws @Arguments 2>$stderrFile
        $exitCode = $LASTEXITCODE
        $stderr = Get-Content -LiteralPath $stderrFile -Raw
    }
    finally {
        $ErrorActionPreference = $previousErrorActionPreference
        Remove-Item -LiteralPath $stderrFile -ErrorAction SilentlyContinue
    }
    if ($AllowDuplicatePermission -and $exitCode -ne 0 -and $stderr -match 'InvalidPermission.Duplicate') {
        return
    }
    if ($exitCode -ne 0) {
        throw "AWS command failed: $($Arguments -join ' ')`n$stderr"
    }
    return $result
}

function Ensure-CidrIngress {
    param([string]$GroupId, [int]$Port, [string]$Description)

    Invoke-Aws -AllowDuplicatePermission ec2 authorize-security-group-ingress --region $Region --group-id $GroupId --protocol tcp --port $Port --cidr '0.0.0.0/0' | Out-Null
}

function Ensure-SecurityGroupIngress {
    param([string]$GroupId, [string]$SourceGroupId, [int]$Port)

    $permission = "IpProtocol=tcp,FromPort=$Port,ToPort=$Port,UserIdGroupPairs=[{GroupId=$SourceGroupId,Description='ALB to ECS API only'}]"
    Invoke-Aws -AllowDuplicatePermission ec2 authorize-security-group-ingress --region $Region --group-id $GroupId --ip-permissions $permission | Out-Null
}

$CertificateStatus = Invoke-Aws acm describe-certificate --region $Region --certificate-arn $CertificateArn --query 'Certificate.Status' --output text
if ($CertificateStatus.Trim() -ne 'ISSUED') {
    throw "The ACM certificate must be ISSUED; current status is $CertificateStatus."
}

$VpcId = Invoke-Aws rds describe-db-instances --region $Region --db-instance-identifier $DatabaseInstance --query 'DBInstances[0].DBSubnetGroup.VpcId' --output text
$SubnetText = Invoke-Aws ec2 describe-subnets --region $Region --filters "Name=vpc-id,Values=$VpcId" --query 'Subnets[].SubnetId' --output text
$Subnets = @($SubnetText -split '\s+' | Where-Object { $_ } | Select-Object -First 2)
if ($Subnets.Count -lt 2) {
    throw 'The VPC needs at least two subnets in different Availability Zones for an ALB.'
}

Ensure-CidrIngress -GroupId $AlbSecurityGroup -Port 80 -Description 'HTTP redirect to HTTPS'
Ensure-CidrIngress -GroupId $AlbSecurityGroup -Port 443 -Description 'Public HTTPS API'
Ensure-SecurityGroupIngress -GroupId $ApiSecurityGroup -SourceGroupId $AlbSecurityGroup -Port 8080

$LoadBalancerArn = Invoke-Aws elbv2 describe-load-balancers --region $Region --query "LoadBalancers[?LoadBalancerName=='$AlbName'].LoadBalancerArn | [0]" --output text
if ($LoadBalancerArn.Trim() -eq 'None' -or [string]::IsNullOrWhiteSpace($LoadBalancerArn)) {
    # Pass each subnet as a distinct argument. Passing the array directly
    # through the helper causes Windows PowerShell to combine it into one
    # invalid subnet ID.
    $LoadBalancerArn = Invoke-Aws elbv2 create-load-balancer --region $Region --name $AlbName --subnets $Subnets[0] $Subnets[1] --security-groups $AlbSecurityGroup --scheme internet-facing --type application --ip-address-type ipv4 --query 'LoadBalancers[0].LoadBalancerArn' --output text
}

$TargetGroupArn = Invoke-Aws elbv2 describe-target-groups --region $Region --query "TargetGroups[?TargetGroupName=='$TargetGroupName'].TargetGroupArn | [0]" --output text
if ($TargetGroupArn.Trim() -eq 'None' -or [string]::IsNullOrWhiteSpace($TargetGroupArn)) {
    $TargetGroupArn = Invoke-Aws elbv2 create-target-group --region $Region --name $TargetGroupName --protocol HTTP --port 8080 --target-type ip --vpc-id $VpcId --health-check-protocol HTTP --health-check-path /healthz --matcher 'HttpCode=200' --query 'TargetGroups[0].TargetGroupArn' --output text
}

$HttpsListenerArn = Invoke-Aws elbv2 describe-listeners --region $Region --load-balancer-arn $LoadBalancerArn --query 'Listeners[?Port==`443`].ListenerArn | [0]' --output text
if ($HttpsListenerArn.Trim() -eq 'None' -or [string]::IsNullOrWhiteSpace($HttpsListenerArn)) {
    Invoke-Aws elbv2 create-listener --region $Region --load-balancer-arn $LoadBalancerArn --protocol HTTPS --port 443 --certificates "CertificateArn=$CertificateArn" --ssl-policy 'ELBSecurityPolicy-TLS13-1-2-2021-06' --default-actions "Type=forward,TargetGroupArn=$TargetGroupArn" | Out-Null
}

$HttpListenerArn = Invoke-Aws elbv2 describe-listeners --region $Region --load-balancer-arn $LoadBalancerArn --query 'Listeners[?Port==`80`].ListenerArn | [0]' --output text
if ($HttpListenerArn.Trim() -eq 'None' -or [string]::IsNullOrWhiteSpace($HttpListenerArn)) {
    # Use JSON rather than AWS CLI shorthand for the nested redirect action;
    # PowerShell and the CLI shorthand parser otherwise disagree about braces.
    $redirectActionFile = New-TemporaryFile
    try {
        $redirectActionJson = @(
            [pscustomobject]@{
                Type = 'redirect'
                RedirectConfig = [pscustomobject]@{
                    Protocol = 'HTTPS'
                    Port = '443'
                    Host = '#{host}'
                    Path = '/#{path}'
                    Query = '#{query}'
                    StatusCode = 'HTTP_301'
                }
            }
        ) | ConvertTo-Json -Depth 5
        # Windows PowerShell defaults Set-Content to UTF-16; AWS CLI JSON
        # parameter files must be UTF-8.
        [System.IO.File]::WriteAllText($redirectActionFile, $redirectActionJson, [System.Text.UTF8Encoding]::new($false))
        Invoke-Aws elbv2 create-listener --region $Region --load-balancer-arn $LoadBalancerArn --protocol HTTP --port 80 --default-actions "file://$redirectActionFile" | Out-Null
    }
    finally {
        Remove-Item -LiteralPath $redirectActionFile -ErrorAction SilentlyContinue
    }
}

$loadBalancer = "targetGroupArn=$TargetGroupArn,containerName=limiance-api,containerPort=8080"
Invoke-Aws ecs update-service --region $Region --cluster $Cluster --service $Service --load-balancers $loadBalancer --desired-count 2 --force-new-deployment | Out-Null
Invoke-Aws ecs wait services-stable --region $Region --cluster $Cluster --services $Service

$DnsName = Invoke-Aws elbv2 describe-load-balancers --region $Region --load-balancer-arns $LoadBalancerArn --query 'LoadBalancers[0].DNSName' --output text
$TargetHealth = Invoke-Aws elbv2 describe-target-health --region $Region --target-group-arn $TargetGroupArn --query 'TargetHealthDescriptions[].TargetHealth.State' --output text

Write-Host ''
Write-Host 'ALB provisioned and attached to ECS.'
Write-Host "Namecheap CNAME host:  api.staging"
Write-Host "Namecheap CNAME value: $DnsName"
Write-Host "Target health: $TargetHealth"
Write-Host 'After adding the Namecheap CNAME, test: https://api.staging.celvios.site/healthz'
