#!/usr/bin/env bash
# SPEC-final §20.2 names the script e2e/run.sh — same as run-e2e.sh.
exec "$(dirname "$0")/run-e2e.sh" "$@"
