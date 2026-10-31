#!/bin/sh
# Run the backend suite against the LOCAL adapter (§21 #108): opm-cloud (dist/opm-cloud.cjs, npm run build)
# on a fresh temp data dir + port, the tests open the same data dir (OPM_TEST_ADAPTER=local).
#   scripts/test-local.sh [test files…]     default: test/*.test.ts
set -eu
cd "$(dirname "$0")/.."
PORT="${OPM_TEST_PORT:-8791}"
DATA="$(mktemp -d "${TMPDIR:-/tmp}/opm-local-test.XXXXXX")"
LOG="$DATA/server.log"
# the release PUBLIC key the install scripts embed (the tests sign with the matching .secrets key)
PUB="$(node -e 'try{process.stdout.write(JSON.parse(require("fs").readFileSync("../.secrets/release-key.json","utf8")).publicKeyB64)}catch{}')"
export OPM_TEST_ADAPTER=local OPM_ADAPTER=local OPM_DATA_DIR="$DATA" OPM_TEST_HOOKS=1 OPM_LOCAL_STRICT=1
export OPM_TEST_SERVER="http://127.0.0.1:$PORT" OPM_PUBLIC_URL="http://127.0.0.1:$PORT"
export OPM_FRONTEND_ORIGINS="http://localhost:5173,http://localhost:4173,http://localhost:5002"
# §21 #125 port tunnels listen on these (test/44)
export TUNNEL_FREE_PORTS="${TUNNEL_FREE_PORTS:-28700-28703}"
export OPM_OUTBOX_MIN_AGE_MS="${OPM_OUTBOX_MIN_AGE_MS:-1000}" OPM_BLOB_REDIRECT="${OPM_BLOB_REDIRECT:-0}" OPM_RELEASE_PUBLIC_KEY="$PUB" GCLOUD_PROJECT=local
OPM_PORT="$PORT" OPM_HOST=127.0.0.1 node --no-warnings=ExperimentalWarning dist/opm-cloud.cjs serve >"$LOG" 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null || true; wait $SRV 2>/dev/null || true; [ "${OPM_KEEP_DATA:-0}" = 1 ] && echo "data kept: $DATA" || rm -rf "$DATA"' EXIT INT TERM
for i in $(seq 1 100); do
  if curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then break; fi
  if ! kill -0 $SRV 2>/dev/null; then cat "$LOG"; echo "opm-cloud did not start"; exit 1; fi
  sleep 0.2
done
echo "opm-cloud (local adapter) on :$PORT, data $DATA, log $LOG"
if [ $# -gt 0 ]; then FILES="$*"; else FILES="test/*.test.ts"; fi
set +e
# shellcheck disable=SC2086
node --import tsx --no-warnings=ExperimentalWarning --test --test-concurrency=1 --test-timeout=600000 $FILES
RC=$?
set -e
if [ $RC -ne 0 ]; then echo "--- server log (tail)"; tail -n 80 "$LOG"; fi
exit $RC
