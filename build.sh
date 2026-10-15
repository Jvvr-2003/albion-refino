#!/usr/bin/env bash
# Compila o AlbionRefino.exe para Windows (dá para rodar em Linux, macOS ou no
# Git Bash do Windows). O ícone e o manifesto já estão em rsrc_windows_amd64.syso.
set -euo pipefail
cd "$(dirname "$0")"
VERSION="${VERSION:-1.0.0}"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-H windowsgui -s -w -X main.version=${VERSION}" \
  -o AlbionRefino.exe .
echo "AlbionRefino.exe ${VERSION} pronto."

# Teste fora do Windows, com o Data Client simulado:
#   go build -o /tmp/fake/albiondata-client ./tools/fakeclient
#   go build -o /tmp/albionrefino .
#   ALBION_FAKE_CLIENT=/tmp/fake/albiondata-client /tmp/albionrefino -no-window
#   abra http://127.0.0.1:47115/
