$ErrorActionPreference = "Stop"

Write-Host "[windows] Checking public IP reachability"
Invoke-WebRequest -UseBasicParsing -Uri "https://api.ipify.org" | Out-Null

Write-Host "[windows] PASS (connectivity ok)."
Write-Host "Note: This script assumes you already activated the tunnel in WireGuard for Windows."