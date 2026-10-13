@echo off
rem Builds NumberPhoto.exe on Windows. Requires Go 1.22+ (https://go.dev/dl/).
cd /d "%~dp0"
go test ./core || exit /b 1
go run github.com/akavel/rsrc@v0.10.2 -manifest res\app.manifest -ico res\app.ico -o rsrc_windows_amd64.syso || exit /b 1
set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=0
go build -trimpath -ldflags "-H windowsgui -s -w" -o NumberPhoto.exe . || exit /b 1
echo OK: NumberPhoto.exe
