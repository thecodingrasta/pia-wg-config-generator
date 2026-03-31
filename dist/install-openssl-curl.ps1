<#
.SYNOPSIS
  Downloads the archive for the OpenSSL backend of cURL kindly maintained by LoRd_MuldeR, 
  verifies the SHA-256 and extracts it beside the pia-wg-config binary into .\openssl_cURL\

.USAGE (examples)
  # From the folder that contains pia-wg-config.exe
  powershell -ExecutionPolicy Bypass -File .\install-openssl-cURL.ps1

  # To extract to a custom path
  powershell -ExecutionPolicy Bypass -File .\install-openssl-cURL.ps1 -ExtractDir "C:\Users\Borat\Documents\"

NOTES
  - This script is intentionally strict. If the have a zip hash mismatch, it stops.
  - It extracts to: <script dir>\openssl_cURL\ or <custom dir>\openssl_cURL\
  - It will try to locate curl.exe inside the extracted zip and validate it via the `-V` flag.
#>

[CmdletBinding()]
param(
  [Parameter(Mandatory = $false)]
  [string]$ExtractDir,

  [Parameter(Mandatory = $false)]
  [switch]$Force
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
$ZipUrl = "https://github.com/lordmulder/cURL-build-win32/releases/download/2026-01-11/curl-8.18.0-win-x64-full.2026-01-11.zip"
$ExpectedSha256 = "809482aa3a16cfdf38dd5fa40c5c96a48d122a2c12246ccb6ac5511648c3417f"
$ExpectedCurlRelativePath = "curl.exe"

function Write-Info([string]$Msg) { Write-Host "[ INFO ] $Msg" -ForegroundColor Cyan }
function Write-Ok([string]$Msg)   { Write-Host "[  OK  ] $Msg" -ForegroundColor Green }
function Write-Warn([string]$Msg) { Write-Host "[ WARN ] $Msg" -ForegroundColor Yellow }
function Write-Fail([string]$Msg) { Write-Host "[ FAIL ] $Msg" -ForegroundColor Red }

function Normalize-Sha([string]$Sha) {
  return ($Sha.Trim().ToLowerInvariant() -replace '\s','')
}

function Get-Sha256([string]$Path) {
  $hashObj = Get-FileHash -Algorithm SHA256 -Path $Path
  return $hashObj.Hash.ToLowerInvariant()
}

function Ensure-Dir([string]$Dir) {
  if (-not (Test-Path -LiteralPath $Dir)) {
    New-Item -ItemType Directory -Path $Dir | Out-Null
  }
}

function Remove-DirIfExists([string]$Dir) {
  if (Test-Path -LiteralPath $Dir) {
    Remove-Item -LiteralPath $Dir -Recurse -Force
  }
}

# --- Paths ---
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$targetDir = Join-Path $scriptDir "openssl_cURL"
if ($ExtractDir -ne $null) {
    $targetDir = $targetDir.Replace($scriptDir, $ExtractDir)
}
$tmpDir    = Join-Path $targetDir ".tmp_openssl_cURL"
$tmpZip    = Join-Path $tmpDir "openssl_cURL.zip"
$expectedSha = Normalize-Sha $ExpectedSha256

Write-Info "Script dir: $scriptDir"
Write-Info "Target dir: $targetDir"
Write-Info "Download URL: $ZipUrl"

if ((Test-Path -LiteralPath $targetDir) -and -not $Force) {
  Write-Warn "Target folder already exists: $targetDir"
  Write-Warn "Re-run with -Force to overwrite."
  exit 0
}

# --- Prep temp ---
Remove-DirIfExists $tmpDir
Ensure-Dir $tmpDir

# --- Download ---
Write-Info "Downloading zip..."
try {
  # Basic parsing for compatibility with older PowerShell versions.
  Invoke-WebRequest -Uri $ZipUrl -OutFile $tmpZip -UseBasicParsing
} catch {
  Write-Fail "Download failed: $($_.Exception.Message)"
  throw
}

if (-not (Test-Path -LiteralPath $tmpZip)) {
  throw "Download did not produce a zip at expected path: $tmpZip"
}
Write-Ok "Downloaded: $tmpZip"

# --- Verify hash ---
Write-Info "Verifying SHA-256..."
$actualSha = Get-Sha256 $tmpZip
Write-Info "Expected: $expectedSha"
Write-Info "Actual  : $actualSha"

if ($actualSha -ne $expectedSha) {
  Write-Fail "SHA-256 mismatch. Refusing to extract."
  throw "Hash mismatch"
}
Write-Ok "SHA-256 verified."

# --- Extract ---
if ($Force) {
  Remove-DirIfExists $targetDir
}
Ensure-Dir $targetDir

Write-Info "Extracting zip..."
try {
  Expand-Archive -Path $tmpZip -DestinationPath $targetDir -Force
} catch {
  Write-Fail "Extraction failed: $($_.Exception.Message)"
  throw
}
Write-Ok "Extracted to: $targetDir"

# --- Locate cURL.exe ---
function Find-CurlExe([string]$Root, [string]$ExpectedRel) {
  if (-not [string]::IsNullOrWhiteSpace($ExpectedRel)) {
    $candidate = Join-Path $Root $ExpectedRel
    if (Test-Path -LiteralPath $candidate) { return $candidate }
    return $null
  }

  $hits = Get-ChildItem -Path $Root -Recurse -File -Filter "cURL.exe" -ErrorAction SilentlyContinue
  if ($null -eq $hits -or $hits.Count -eq 0) { return $null }

  # Prefer paths that look like bin\cURL.exe or just cURL.exe at root
  $preferred = $hits | Sort-Object {
    $p = $_.FullName.ToLowerInvariant()
    if ($p -match '\\bin\\cURL\.exe$') { 0 }
    elseif ($p -match '\\cURL\.exe$') { 1 }
    else { 2 }
  } | Select-Object -First 1

  return $preferred.FullName
}

$cURLExe = Find-CurlExe -Root $targetDir -ExpectedRel $ExpectedCurlRelativePath
if ([string]::IsNullOrWhiteSpace($cURLExe)) {
  Write-Fail "Could not find cURL.exe anywhere under: $targetDir"
  throw "cURL.exe not found after extraction"
}
Write-Ok "Found cURL.exe: $cURLExe"

# --- Sanity check cURL -V ---
Write-Info "Running: cURL.exe -V (sanity check)"
try {
  $ver = & $cURLExe -V 2>&1
  if ([string]::IsNullOrWhiteSpace($ver)) {
    Write-Fail "cURL.exe -V returned no output. Something is off."
    throw "cURL.exe -V produced no output"
  }

  Write-Host $ver

  # Soft validation: warn if it doesn't look like OpenSSL
  if ($ver -match 'Schannel') {
    Write-Warn "Detected the Schannel backend of cURL. PIA may block this on Windows."
  } elseif ($ver -match 'OpenSSL') {
    Write-Ok "cURL build reports OpenSSL ✅"
  } elseif ($ver -match 'LibreSSL') {
    Write-Warn "Detected the LibreSSL backend of cURL. This *might* work, but OpenSSL is preferred... PIAs WAF is strict."
  } else {
    Write-Warn "Unable to determine the TLS backend from cURL -V output."
  }
} catch {
  Write-Fail "Failed to execute cURL.exe: $($_.Exception.Message)"
  throw
}

# --- Cleanup temp ---
Remove-DirIfExists $tmpDir

Write-Ok "Done."
Write-Info "Next: Define the path to cURL by either:"
Write-Host "              - Setting within env vars: CURL_PATH=`"$cURLExe`"" -ForegroundColor Cyan
Write-Host "              - Or passing the following flag to the CLI: --curlPath `"$cURLExe`"" -ForegroundColor Cyan