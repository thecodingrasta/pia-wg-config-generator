$env:GOOS="windows"
$env:GOARCH="amd64"
Write-Host "Building x64"
go build -trimpath -ldflags "-s -w" -o ".\dist\pia-wg-config.exe" .\
$env:GOARCH="arm64"
Write-Host "Building ARM"
go build -trimpath -ldflags "-s -w" -o ".\dist\pia-wg-config-arm64.exe" .\
Remove-Item Env:GOOS, Env:GOARCH