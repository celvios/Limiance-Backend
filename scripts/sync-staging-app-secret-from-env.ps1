param(
    [string]$EnvFile = '.env'
)

$ErrorActionPreference = 'Stop'

$Region = 'eu-north-1'
$ApplicationSecret = 'limiance/staging/app'
$Aws = 'C:\Program Files\Amazon\AWSCLIV2\aws.exe'

if (-not (Test-Path -LiteralPath $Aws)) { throw 'AWS CLI is not installed at the expected path.' }
if (-not (Test-Path -LiteralPath $EnvFile)) { throw "Environment file not found: $EnvFile" }

# Only configuration which is meaningful and safe for the AWS staging workload.
# Deliberately excluded: APP_ENV, DATABASE_URL, REDIS_URL, SQS_ENDPOINT,
# local SQS URLs, HTTP tuning, and all test-recipient/test-code values.
$allowedKeys = @(
    'VERIFICATION_CODE_PEPPER', 'VERIFICATION_ENCRYPTION_KEY',
    'TRAVEL_RULE_ENCRYPTION_KEY', 'TOTP_ENCRYPTION_KEY',
    'SENDGRID_API_KEY', 'SENDGRID_FROM_EMAIL', 'SENDGRID_VERIFICATION_TEMPLATE_ID',
    'TWILIO_ACCOUNT_SID', 'TWILIO_API_KEY', 'TWILIO_API_SECRET', 'TWILIO_VERIFY_SERVICE_SID',
    'SUMSUB_APP_TOKEN', 'SUMSUB_SECRET_KEY', 'SUMSUB_WEBHOOK_SECRET', 'SUMSUB_LEVEL_NAME',
    'GOOGLE_CLIENT_ID', 'GOOGLE_CLIENT_SECRET', 'GOOGLE_REDIRECT_URL',
    'TELEGRAM_CLIENT_ID', 'TELEGRAM_CLIENT_SECRET', 'TELEGRAM_REDIRECT_URL',
    'GEETEST_CAPTCHA_ID', 'GEETEST_PRIVATE_KEY',
    'FIREBLOCKS_API_KEY', 'FIREBLOCKS_API_KEY_ID', 'FIREBLOCKS_PRIVATE_KEY',
    'FIREBLOCKS_BASE_URL', 'FIREBLOCKS_JWKS_URL', 'FIREBLOCKS_WEBHOOK_SECRET',
    'YELLOW_CARD_API_KEY', 'CNGN_API_KEY'
)

$local = @{}
foreach ($line in Get-Content -LiteralPath $EnvFile) {
    $trimmed = $line.Trim()
    if ($trimmed -eq '' -or $trimmed.StartsWith('#')) { continue }
    $parts = $trimmed -split '=', 2
    if ($parts.Count -ne 2) { continue }
    $key = $parts[0].Trim()
    $value = $parts[1].Trim()
    if ($value.Length -ge 2 -and (($value.StartsWith('"') -and $value.EndsWith('"')) -or ($value.StartsWith("'") -and $value.EndsWith("'")))) {
        $value = $value.Substring(1, $value.Length - 2)
    }
    if ($allowedKeys -contains $key -and -not [string]::IsNullOrWhiteSpace($value)) {
        $local[$key] = $value
    }
}

if ($local.Count -eq 0) { throw 'No populated staging-safe settings were found in the environment file.' }

$existingRaw = & $Aws secretsmanager get-secret-value --no-cli-pager --region $Region --secret-id $ApplicationSecret --query SecretString --output text
if ($LASTEXITCODE -ne 0) { throw 'Could not read the AWS staging application secret. Authenticate with AWS and retry.' }
$staging = [ordered]@{}
$existing = $existingRaw | ConvertFrom-Json
foreach ($property in $existing.PSObject.Properties) { $staging[$property.Name] = $property.Value }
foreach ($key in $local.Keys) { $staging[$key] = $local[$key] }

$payload = $staging | ConvertTo-Json -Depth 20 -Compress
$payloadFile = New-TemporaryFile
try {
    [System.IO.File]::WriteAllText($payloadFile, $payload, [System.Text.UTF8Encoding]::new($false))
    & $Aws secretsmanager put-secret-value --no-cli-pager --region $Region --secret-id $ApplicationSecret --secret-string "file://$payloadFile" | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not update the AWS staging application secret.' }
}
finally {
    Remove-Item -LiteralPath $payloadFile -ErrorAction SilentlyContinue
}

Write-Host "Synced $($local.Count) non-empty staging-safe settings to AWS Secrets Manager."
Write-Host "Keys synced: $(($local.Keys | Sort-Object) -join ', ')"
Write-Host 'No secret values were printed.'
