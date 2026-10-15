#!/bin/sh
# Run a firebase emulator command with a Java >= 21 (firebase-tools 15 needs it).
# usage: scripts/emu.sh exec "<cmd>"  |  scripts/emu.sh start
set -eu
cd "$(dirname "$0")/.."
for j in "${JAVA_HOME:-}" /opt/homebrew/opt/openjdk@21 /opt/homebrew/opt/openjdk /usr/lib/jvm/java-21-openjdk-amd64; do
  if [ -n "$j" ] && [ -x "$j/bin/java" ] && "$j/bin/java" -version 2>&1 | grep -Eq 'version "(2[1-9]|[3-9][0-9])'; then export JAVA_HOME="$j"; export PATH="$j/bin:$PATH"; break; fi
done
PROJECT="${OPM_EMU_PROJECT:-demo-opm}"
case "${1:-start}" in
  exec) shift; exec firebase emulators:exec --project "$PROJECT" --only auth,firestore,functions,storage "$@" ;;
  start) shift; exec firebase emulators:start --project "$PROJECT" --only auth,firestore,functions,storage "$@" ;;
  *) echo "usage: $0 exec <cmd> | start"; exit 2 ;;
esac
