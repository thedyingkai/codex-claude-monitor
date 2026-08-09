#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUT="${OUT:-$ROOT/dist}"
VERSION="${VERSION:-dev}"
mkdir -p "$OUT"

build() {
  GOOS="$1" GOARCH="$2" CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION" \
    -o "$OUT/$3" ./cmd/quota-monitor
}

cd "$ROOT"
build windows amd64 quota-monitor-windows-amd64.exe
build linux amd64 quota-monitor-linux-amd64
build linux arm64 quota-monitor-linux-arm64

(
  cd "$OUT"
  sha256sum quota-monitor-windows-amd64.exe \
    quota-monitor-linux-amd64 quota-monitor-linux-arm64 > SHA256SUMS
)
