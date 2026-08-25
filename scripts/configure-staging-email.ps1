param(
    [string]$FromEmail,
    [string]$SendGridAPIKey
)

$ErrorActionPreference = 'Stop'

$Region = 'eu-north-1'
$ApplicationSecret = 'limiance/staging/app'
$Aws = (Get-Command aws -ErrorAction SilentlyContinue).Source
if ([string]::IsNullOrWhiteSpace($Aws)) { $Aws = 'C:\Program Files\Amazon\AWSCLIV2\aws.exe' }

if (-not (Test-Path -LiteralPath $Aws)) {
    throw 'AWS CLI is not installed at the expected path.'
}

if ([string]::IsNullOrWhiteSpace($FromEmail)) {
    $FromEmail = Read-Host 'Verified SendGrid sender email (for example security@celvios.site)'
}
if ([string]::IsNullOrWhiteSpace($SendGridAPIKey)) {
    $secureKey = Read-Host 'SendGrid API key (Mail Send permission only)' -AsSecureString
    $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secureKey)
    try {
        $SendGridAPIKey = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr)
    }
    finally {
        [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)
    }
}

try {
    $parsedEmail = [System.Net.Mail.MailAddress]::new($FromEmail)
}
catch {
    throw 'The sender must be a valid email address and must already be verified in SendGrid.'
}
if ([string]::IsNullOrWhiteSpace($SendGridAPIKey)) {
    throw 'A SendGrid API key is required.'
}

$raw = & $Aws secretsmanager get-secret-value --no-cli-pager --region $Region --secret-id $ApplicationSecret --query SecretString --output text
if ($LASTEXITCODE -ne 0) { throw 'Could not read the staging application secret. Re-authenticate with AWS and retry.' }

$secret = [ordered]@{}
$existing = $raw | ConvertFrom-Json
foreach ($property in $existing.PSObject.Properties) { $secret[$property.Name] = $property.Value }
$secret['SENDGRID_API_KEY'] = $SendGridAPIKey
$secret['SENDGRID_FROM_EMAIL'] = $parsedEmail.Address
# The current sender builds the verification message in code. Retain an empty
# template id for a future template-based implementation without requiring it.
if (-not $secret.Contains('SENDGRID_VERIFICATION_TEMPLATE_ID')) {
    $secret['SENDGRID_VERIFICATION_TEMPLATE_ID'] = ''
}

$payload = $secret | ConvertTo-Json -Depth 20 -Compress
$payloadFile = New-TemporaryFile
try {
    [System.IO.File]::WriteAllText($payloadFile, $payload, [System.Text.UTF8Encoding]::new($false))
    & $Aws secretsmanager put-secret-value --no-cli-pager --region $Region --secret-id $ApplicationSecret --secret-string "file://$payloadFile" | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not update the staging application secret.' }
}
finally {
    Remove-Item -LiteralPath $payloadFile -ErrorAction SilentlyContinue
}

Write-Host 'Staging SendGrid settings saved. The API and notification worker must now be redeployed.'
