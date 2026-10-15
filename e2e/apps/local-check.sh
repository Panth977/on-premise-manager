#!/usr/bin/env bash
# Local smoke test of the hello images without OPM: redis + hello-api + hello-web(THEME) + a tiny edge on :$EDGE.
# Usage: local-check.sh [theme] (default green). Leaves containers running; `local-check.sh down` removes them.
set -euo pipefail
cd "$(dirname "$0")"
REPO="${OPM_E2E_REPO:-registry.example.com/opm-test}"
NET=hello-local; EDGE=${EDGE:-18081}
if [[ "${1:-}" == "down" ]]; then docker rm -f hl-redis hl-api hl-web >/dev/null 2>&1 || true; docker network rm $NET >/dev/null 2>&1 || true; pkill -f "local-edge.mjs $EDGE" || true; exit 0; fi
THEME=${1:-green}
docker network inspect $NET >/dev/null 2>&1 || docker network create $NET >/dev/null
docker volume inspect hl-redis-data >/dev/null 2>&1 || docker volume create hl-redis-data >/dev/null
docker inspect hl-redis >/dev/null 2>&1 || docker run -d --name hl-redis --network $NET -v hl-redis-data:/data "$REPO/redis:7.4-alpine" redis-server --appendonly yes >/dev/null
docker inspect hl-api >/dev/null 2>&1 || docker run -d --name hl-api --network $NET -p 127.0.0.1::8080 -e REDIS_ADDR=hl-redis:6379 -e API_TOKEN=abcdefghijklmnopqrstuvwx -e GREETING="Hello from OPM" "$REPO/hello-api:1.0.0" >/dev/null
docker rm -f hl-web >/dev/null 2>&1 || true
printf 'Welcome to the hello-world deployment\n' > $PWD/.banner.txt
docker run -d --name hl-web --network $NET -p 127.0.0.1::8080 -e THEME=$THEME -e BANNER_FILE=/etc/hello/banner.txt -v $PWD/.banner.txt:/etc/hello/banner.txt:ro "$REPO/hello-web:1.0.0" >/dev/null
API=$(docker port hl-api 8080/tcp | head -1); WEB=$(docker port hl-web 8080/tcp | head -1)
pkill -f "local-edge.mjs $EDGE" || true
nohup node local-edge.mjs $EDGE "http://$API" "http://$WEB" >/dev/null 2>&1 &
sleep 1.5
echo "edge http://127.0.0.1:$EDGE  api=$API web=$WEB"
