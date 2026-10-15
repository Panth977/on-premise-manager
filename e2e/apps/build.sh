#!/usr/bin/env bash
# Build + push the hello-world e2e images (multi-arch linux/arm64 + linux/amd64).
# Usage: e2e/apps/build.sh [--load]   (--load: build only the native arch into the local docker, no push)
set -euo pipefail
cd "$(dirname "$0")"
REPO="${OPM_E2E_REPO:-registry.example.com/opm-test}"
VERSION="${HELLO_VERSION:-1.0.0}"
PLATFORMS="${PLATFORMS:-linux/arm64,linux/amd64}"
BUILDER=opm-multiarch

for app in hello-api hello-web; do
  for arch in arm64 amd64; do
    mkdir -p "$app/dist/$arch"
    (cd "$app" && CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags="-s -w" -o "dist/$arch/$app" .)
  done
done

if [[ "${1:-}" == "--load" ]]; then
  native=$(docker info --format '{{.Architecture}}'); case $native in aarch64|arm64) native=arm64;; *) native=amd64;; esac
  for app in hello-api hello-web; do
    docker buildx build --builder colima --platform linux/$native -t "$REPO/$app:$VERSION" -t "$app:dev" --load "$app"
  done
  exit 0
fi

docker buildx inspect "$BUILDER" >/dev/null 2>&1 || docker buildx create --name "$BUILDER" --driver docker-container colima >/dev/null
for app in hello-api hello-web; do
  docker buildx build --builder "$BUILDER" --platform "$PLATFORMS" -t "$REPO/$app:$VERSION" --push "$app"
done
# redis: copy the upstream multi-arch index unchanged (both tags: 7.4-alpine as in hello-catalog.json, 7.4 alias)
docker buildx imagetools create -t "$REPO/redis:7.4-alpine" -t "$REPO/redis:7.4" docker.io/library/redis:7.4-alpine
for i in hello-api:$VERSION hello-web:$VERSION redis:7.4-alpine; do
  docker buildx imagetools inspect "$REPO/$i" | sed -n '1,12p'
done
