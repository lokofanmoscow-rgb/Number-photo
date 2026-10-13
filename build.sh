#!/bin/sh
# Builds NumberPhoto.exe (Windows, 64-bit). Works on Linux, macOS and Windows
# (Git Bash) with Go 1.22 or newer; no C compiler is needed.
set -e
cd "$(dirname "$0")"
go test ./core
# Embed the icon and the manifest (common controls v6, DPI awareness).
go run github.com/akavel/rsrc@v0.10.2 -manifest res/app.manifest -ico res/app.ico -o rsrc_windows_amd64.syso
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-H windowsgui -s -w" -o NumberPhoto.exe .
echo "OK: NumberPhoto.exe"
