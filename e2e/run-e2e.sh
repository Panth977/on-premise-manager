#!/usr/bin/env bash
# OPM hello-world end-to-end (SPEC-final §20.2). Results: e2e/results/<ts>.md (+ copy e2e/RESULTS.md), screenshots in e2e/screenshots/.
#
# Targets (env):
#   OPM_API_BASE        cloud API base (default http://127.0.0.1:8790 = a local opm-cloud)
#   OPM_FRONTEND_URL    cloud frontend (default = OPM_API_BASE)
#   OPM_ADMIN_EMAIL / OPM_ADMIN_PASSWORD   (default .secrets/admin.json)
#   OPM_AGENT_BIN       agent binary (default: downloaded via /v1/install/binary, else agent/dist/opm-agent-<os>-<arch>)
#   OPM_APPROVE_WAIT_SEC approve wait (default 3; the final run uses 60)
#   OPM_E2E_UI=0        API only (skip Playwright UI flows)
#   OPM_E2E_UNTIL=<n>   stop after step n ; OPM_E2E_KEEP=1 keep agents running at the end
# Shortcut: --deployed (https://opm.example.com, wait 60)
set -euo pipefail
cd "$(dirname "$0")"
case "${1:-}" in
  --deployed)
    export OPM_API_BASE="${OPM_API_BASE:-https://opm.example.com/api}"; export OPM_FRONTEND_URL="${OPM_FRONTEND_URL:-https://opm.example.com}"
    export OPM_APPROVE_WAIT_SEC="${OPM_APPROVE_WAIT_SEC:-60}";;
  "") ;;
  *) echo "usage: $0 [--deployed]"; exit 2;;
esac
export DOCKER_HOST="${DOCKER_HOST:-unix://$HOME/.colima/default/docker.sock}"
docker info >/dev/null 2>&1 || { echo "docker (Colima) is not running"; exit 1; }
[ -d node_modules ] || npm install --no-audit --no-fund
[ -f ../shared/dist/cloud-api.js ] || (cd ../shared && npm run build)
mkdir -p run results screenshots
exec npx tsx src/run.ts
