#!/bin/sh
# Upgrade smoke test of a REAL opm-agent binary upgrading itself on Linux (SPEC-final §21 #90 + #91), against the
# standalone fake cloud, in Docker (Colima). Usage: test/upgrade-smoke/run.sh [exec|systemd|both]   (default both)
#   exec    debian:12, no supervisor: A → B via the local API (session + actor kept), then B → B' (exits at start) →
#           the setsid watcher restores B.
#   systemd privileged debian:12 + systemd as PID 1: `opm-agent systemd-units --install` (service + socket + guard),
#           A → B, edge continuity during `systemctl restart` with / without socket activation, B → B' (watcher
#           rollback), B → B' with the watcher killed (boot-guard rollback).
# Builds linux/<docker arch> binaries A (0.10.0-1), B (0.10.0-2), B' (0.10.0-3, -X …TestBrokenRun=smoke) into
# $SMOKE_DIR (default $HOME/.cache/opm-upgrade-smoke; must be under $HOME for Colima bind mounts).
set -eu
HERE=$(cd "$(dirname "$0")" && pwd)
AGENT=$(cd "$HERE/../.." && pwd)
MODE="${1:-both}"
WORK="${SMOKE_DIR:-$HOME/.cache/opm-upgrade-smoke}"
rm -rf "$WORK"
mkdir -p "$WORK/bin"
case "$(docker info --format '{{.Architecture}}')" in
aarch64 | arm64) ARCH=arm64 ;;
*) ARCH=amd64 ;;
esac
echo "$ARCH" >"$WORK/arch"
STAMP=$(date -u +%Y-%m-%dT%H:%M:%SZ)
build() { # version out extra-ldflags
	(cd "$AGENT" && CGO_ENABLED=0 GOOS=linux GOARCH=$ARCH go build -trimpath \
		-ldflags "-s -w -X opm/agent/internal/agent.Version=$1 -X opm/agent/internal/agent.Commit=smoke -X opm/agent/internal/agent.BuiltAt=$STAMP $3" \
		-o "$WORK/bin/$2" ./cmd/opm-agent)
}
echo "building linux/$ARCH binaries into $WORK/bin"
build 0.10.0-1 opm-agent-A ""
build 0.10.0-2 opm-agent-B ""
build 0.10.0-3 opm-agent-Bbroken "-X opm/agent/internal/agent.TestBrokenRun=smoke"
(cd "$AGENT" && CGO_ENABLED=0 GOOS=linux GOARCH=$ARCH go build -o "$WORK/bin/opm-fakecloud" ./cmd/opm-fakecloud)
(cd "$AGENT" && CGO_ENABLED=0 GOOS=linux GOARCH=$ARCH go build -o "$WORK/bin/driver" ./test/upgrade-smoke/driver)
cp "$HERE/inner.sh" "$WORK/inner.sh"

rc=0
if [ "$MODE" = exec ] || [ "$MODE" = both ]; then
	docker rm -f opm-smoke-exec >/dev/null 2>&1 || true
	docker run --rm --name opm-smoke-exec -v "$WORK:/smoke" -v /var/run/docker.sock:/var/run/docker.sock debian:12 sh /smoke/inner.sh exec || rc=1
fi
if [ "$MODE" = systemd ] || [ "$MODE" = both ]; then
	docker build -q -t opm-smoke-systemd -f "$HERE/Dockerfile.systemd" "$HERE" >/dev/null
	docker rm -f opm-smoke-systemd >/dev/null 2>&1 || true
	docker run -d --name opm-smoke-systemd --privileged --cgroupns=host -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
		--tmpfs /run --tmpfs /run/lock -v "$WORK:/smoke" -v /var/run/docker.sock:/var/run/docker.sock opm-smoke-systemd >/dev/null
	for _ in $(seq 1 30); do
		st=$(docker exec opm-smoke-systemd systemctl is-system-running 2>/dev/null || true)
		case "$st" in running | degraded) break ;; esac
		sleep 1
	done
	echo "systemd in the container: $st"
	docker exec opm-smoke-systemd sh /smoke/inner.sh systemd || rc=1
	[ "${KEEP:-0}" = 1 ] || docker rm -f opm-smoke-systemd >/dev/null
fi
exit $rc
