#!/usr/bin/env bash
set -euo pipefail

version="1.14.0-rc.1-breaker"
tags="$(cat release/DEFAULT_BUILD_TAGS_OTHERS)"
ldflags="$(cat release/LDFLAGS)"

podman run --rm \
  -v "$(pwd)":/root/sing-box \
  -w /root/sing-box \
  golang:bookworm \
  go build -v -trimpath -mod=mod \
    -o dist/sing-box \
    -tags "$tags" \
    -ldflags "-s -w -buildid= -X github.com/sagernet/sing-box/constant.Version=v${version} ${ldflags}" \
    ./cmd/sing-box

command -v fpm >/dev/null
cp .fpm_systemd .fpm
rm -f "dist/sing-box_${version}_debian_amd64.deb"
fpm -t deb \
  -v "$version" \
  -p "dist/sing-box_${version}_debian_amd64.deb" \
  --architecture amd64 \
  dist/sing-box=/usr/bin/sing-box

echo "done: dist/sing-box_${version}_debian_amd64.deb"
