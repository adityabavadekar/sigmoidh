#!/usr/bin/env bash
set -euo pipefail

IMAGE="sigmoidh-test"
CONTAINER="go-test"
MOD_CACHE="go-test-mod-cache"
BUILD_CACHE="go-test-build-cache"

cleanup() {
  echo
  echo "[*] Cleaning up..."
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  echo "[+] Done"
}

trap cleanup EXIT INT TERM

docker volume create "$MOD_CACHE" >/dev/null
docker volume create "$BUILD_CACHE" >/dev/null

docker create \
  --name "$CONTAINER" \
  --security-opt seccomp=unconfined \
  --workdir /app \
  --mount "type=volume,src=$MOD_CACHE,dst=/go/pkg/mod" \
  --mount "type=volume,src=$BUILD_CACHE,dst=/root/.cache/go-build" \
  "$IMAGE" \
  sleep infinity >/dev/null

docker start "$CONTAINER" >/dev/null

echo "[+] bwrap: $(docker exec "$CONTAINER" which bwrap)"

echo "[*] Copying project..."
docker cp "$PWD/." "$CONTAINER:/app/"

echo
echo "[+] Running go test -v"
echo

docker exec -it "$CONTAINER" go test -v
