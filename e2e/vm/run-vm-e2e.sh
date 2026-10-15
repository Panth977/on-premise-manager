#!/usr/bin/env bash
# opm-cloud VM end-to-end (SPEC §21 #108). Results: e2e/vm/RESULTS.md, logs e2e/run/vm/, screenshot e2e/vm/screenshots/.
#
#   e2e/vm/run-vm-e2e.sh            rebuild the selfhost image from this checkout, start it with the e2e override
#                                   (OPM_TEST_HOOKS=1 + host.docker.internal), run the test, then put the stack back
#                                   to the default compose (test hooks OFF; data volume kept)
#   OPM_VM_BUILD=0                  skip the image rebuild
#   OPM_VM_RESTORE=0                leave the e2e override (test hooks) running afterwards
#   OPM_VM_KEEP=1                   keep the agent + its containers running after the run
# Needs: Colima (DOCKER_HOST), the docker-compose binary, agent/dist built (make dist), e2e/node_modules.
# Admin: e2e/run/vm-admin.json (gitignored); created with `opm-cloud create-admin` when missing or no longer valid.
set -euo pipefail
cd "$(dirname "$0")/.."            # e2e/
E2E="$PWD"; ROOT="$(cd .. && pwd)"
export DOCKER_HOST="${DOCKER_HOST:-unix://$HOME/.colima/default/docker.sock}"
DC="${OPM_COMPOSE_BIN:-/opt/homebrew/bin/docker-compose}"
BASE="${OPM_VM_BASE:-http://127.0.0.1:8790}"
ADMIN_EMAIL="${OPM_VM_ADMIN_EMAIL:-admin@opm.local}"
mkdir -p run/vm vm/screenshots
docker info >/dev/null 2>&1 || { echo "docker (Colima) is not running"; exit 1; }
[ -d node_modules ] || npm install --no-audit --no-fund
[ -f ../shared/dist/cloud-api.js ] || (cd ../shared && npm run build)
[ -f ../agent/dist/opm-agent-darwin-arm64 ] && [ -f ../agent/dist/release.json ] || { echo "agent/dist is not built (cd agent && make dist)"; exit 1; }

cd "$ROOT/selfhost"
dce() { "$DC" -f docker-compose.yml -f ../e2e/vm/compose.e2e.yml "$@"; }
if [ "${OPM_VM_BUILD:-1}" = 1 ]; then echo "== build + start (e2e override)"; dce up -d --build >"$E2E/run/vm/build.log" 2>&1 || { tail -40 "$E2E/run/vm/build.log"; exit 1; }
else echo "== start (e2e override)"; dce up -d; fi
health() { for _ in $(seq 1 60); do curl -fsS "$BASE/healthz" >/dev/null 2>&1 && return 0; sleep 1; done; echo "opm-cloud is not healthy"; exit 1; }
health

# operator setup (idempotent): release key → restart (install scripts embed it), agent upload, deps, admin
KEY=$(dce exec -T opm-cloud opm-cloud release-key | tail -1)
if echo "$KEY" | grep -q '"created":true'; then echo "== release key created → restart"; dce restart opm-cloud >/dev/null; health; fi
VER=$(node -e "console.log(require('$ROOT/agent/dist/release.json').version)")
LATEST=$(dce exec -T opm-cloud cat /data/blobs/agent/latest 2>/dev/null | tr -d '\r\n' || true)
DIST_SHA=$(cut -d' ' -f1 "$ROOT/agent/dist/opm-agent-darwin-arm64.sha256")
UP_SHA=$(dce exec -T opm-cloud cat "/data/blobs/agent/$VER/darwin-arm64/opm-agent.sha256" 2>/dev/null | tr -d '\r\n' || true)
# upload-agent requires --version = agent/dist/release.json version (the binary's own version)
if [ "$LATEST" != "$VER" ] || [ "$UP_SHA" != "$DIST_SHA" ]; then echo "== upload-agent $VER"; dce exec -T opm-cloud opm-cloud upload-agent --dir /agent-dist --version "$VER" --latest | tail -2; fi
if ! dce exec -T opm-cloud cat /data/blobs/deps/manifest.json 2>/dev/null | grep -q "\"$VER\""; then
  echo "== upload-deps (docker 28.5.2, agent $VER)"
  dce exec -T opm-cloud opm-cloud upload-deps --docker-version 28.5.2 --agent-version "$VER" | tail -1
fi
ADMIN_JSON="$E2E/run/vm-admin.json"
admin_ok() { [ -f "$ADMIN_JSON" ] && node -e "const a=require('$ADMIN_JSON');fetch('$BASE/auth/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({email:a.email,password:a.password})}).then(r=>process.exit(r.ok?0:1),()=>process.exit(1))"; }
if ! admin_ok; then
  echo "== create-admin $ADMIN_EMAIL (password → e2e/run/vm-admin.json, gitignored)"
  dce exec -T opm-cloud opm-cloud create-admin --email "$ADMIN_EMAIL" | tail -1 | node -e "let s='';process.stdin.on('data',d=>s+=d).on('end',()=>{const j=JSON.parse(s);require('fs').writeFileSync('$ADMIN_JSON',JSON.stringify({email:j.email,password:j.password,uid:j.uid},null,1)+'\n',{mode:0o600})})"
fi

cd "$E2E"
set +e
OPM_VM_BASE="$BASE" npx tsx vm/vm-e2e.ts
CODE=$?
set -e
if [ "${OPM_VM_RESTORE:-1}" = 1 ]; then
  echo "== stack back to the default compose (test hooks off, data kept)"
  (cd "$ROOT/selfhost" && "$DC" up -d >/dev/null 2>&1 && health && "$DC" exec -T opm-cloud sh -c 'test -z "$OPM_TEST_HOOKS"' && echo "opm-cloud running on $BASE without test hooks")
fi
exit $CODE
