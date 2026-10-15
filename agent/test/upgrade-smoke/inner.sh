#!/bin/sh
# Runs INSIDE the smoke container (see run.sh). $1 = exec | systemd.
#   exec:    no supervisor — the agent re-execs itself, the watcher is a setsid process.
#   systemd: systemd is PID 1 — systemctl restart --no-block, systemd-run watcher, the boot guard drop-in, socket activation.
set -eu
MODE="${1:-exec}"
S=/smoke
B=$S/bin
D="$B/driver"
ADMIN=http://127.0.0.1:9998
API=http://127.0.0.1:7080
step() { printf '\n=== %s\n' "$*"; }

step "fake cloud"
"$B/opm-fakecloud" --addr 127.0.0.1:9999 --admin 127.0.0.1:9998 --deployment DepSmoke --os linux --arch "$(cat $S/arch)" >/var/log/opm-fakecloud.log 2>&1 &
for _ in $(seq 1 50); do "$D" http GET $ADMIN/pubkey >/dev/null 2>&1 && break; sleep 0.2; done
PUB=$("$D" http GET $ADMIN/pubkey)
mkdir -p /etc/opm
"$D" http POST "$ADMIN/token?site=app" >/etc/opm/install-token
cat >/etc/opm/agent.json <<EOF
{
  "apiBase": "http://127.0.0.1:9999",
  "deploymentId": "DepSmoke",
  "siteSlug": "app",
  "dataDir": "/var/lib/opm",
  "agentPort": 7080,
  "guiAliasPort": 8080,
  "edgeHttpPort": 80,
  "advertiseHost": "127.0.0.1",
  "containerNames": "prefixed",
  "releasePublicKey": "$PUB"
}
EOF
install -m 0755 "$B/opm-agent-A" /usr/local/bin/opm-agent
/usr/local/bin/opm-agent version

if [ "$MODE" = systemd ]; then
	# the host's Docker is mounted as a socket: a stand-in docker.service satisfies Requires=docker.service
	printf '[Unit]\nDescription=stand-in for the mounted Docker socket\n[Service]\nType=oneshot\nRemainAfterExit=yes\nExecStart=/bin/true\n' >/etc/systemd/system/docker.service
	step "systemd units (opm-agent systemd-units --install: service + socket + guard)"
	/usr/local/bin/opm-agent systemd-units --install --config /etc/opm/agent.json
	mkdir -p /etc/systemd/system/opm-agent.service.d
	printf '[Service]\nEnvironment=OPM_UPGRADE_HEALTH_SEC=30\n' >/etc/systemd/system/opm-agent.service.d/smoke.conf
	systemctl daemon-reload
	systemctl enable --now opm-agent.socket opm-agent.service
	systemctl --no-pager status opm-agent.socket | head -n 12 || true
else
	step "agent A without a supervisor (exec mode)"
	OPM_UPGRADE_HEALTH_SEC=30 nohup /usr/local/bin/opm-agent run --config /etc/opm/agent.json >>/var/log/opm-agent.log 2>&1 &
fi
"$D" wait --base $API --version 0.10.0-1 --timeout 60
for _ in $(seq 1 60); do "$D" login --base $API >/dev/null 2>&1 && break; sleep 1; done
"$D" login --base $API
"$D" info --base $API

step "upgrade A → B (0.10.0-2) from the local API with the session + actor"
"$D" http POST "$ADMIN/release?version=0.10.0-2&file=$B/opm-agent-B&notes=smoke+release+B"
sleep 3
T0=$(date +%s)
"$D" upgrade --base $API --version 0.10.0-2
"$D" wait-phase --base $API --version "done" --timeout 120
echo "upgrade A → B took $(($(date +%s) - T0)) s"
"$D" wait --base $API --version 0.10.0-2 --timeout 30
"$D" info --base $API
echo "opm-agent.prev: $(/usr/local/bin/opm-agent.prev version)"

if [ "$MODE" = systemd ]; then
	step "edge continuity: systemctl restart opm-agent WITH socket activation"
	"$D" hammer --timeout 12 http://127.0.0.1:80/_opm/healthz http://127.0.0.1:7080/_opm/healthz &
	H=$!
	sleep 3
	systemctl restart opm-agent
	wait $H
	"$D" wait --base $API --version 0.10.0-2 --timeout 60 >/dev/null
	step "edge continuity: systemctl restart opm-agent WITHOUT socket activation"
	/usr/local/bin/opm-agent systemd-units --install --config /etc/opm/agent.json --no-socket
	systemctl restart opm-agent
	"$D" wait --base $API --version 0.10.0-2 --timeout 60 >/dev/null
	"$D" hammer --timeout 12 http://127.0.0.1:80/_opm/healthz http://127.0.0.1:7080/_opm/healthz &
	H=$!
	sleep 3
	systemctl restart opm-agent
	wait $H
	"$D" wait --base $API --version 0.10.0-2 --timeout 60 >/dev/null
	/usr/local/bin/opm-agent systemd-units --install --config /etc/opm/agent.json --socket
	systemctl restart opm-agent
	"$D" wait --base $API --version 0.10.0-2 --timeout 60 >/dev/null
	"$D" info --base $API
fi

step "upgrade B → B' (0.10.0-3, exits at start): rolled back to B (the watcher; under systemd the boot guard may win the race)"
"$D" http POST "$ADMIN/release?version=0.10.0-3&file=$B/opm-agent-Bbroken"
sleep 3
T0=$(date +%s)
"$D" upgrade --base $API --version 0.10.0-3
"$D" wait-phase --base $API --version rolled-back --timeout 180
echo "B' rolled back by the $(sed -n 's/.*"by": "\([a-z]*\)".*/\1/p' /var/lib/opm/upgrade/rolled-back.json) after $(($(date +%s) - T0)) s"
"$D" wait --base $API --version 0.10.0-2 --timeout 30
"$D" info --base $API
echo "--- watch.log"
cat /var/lib/opm/upgrade/watch.log || true

if [ "$MODE" = systemd ]; then
	step "upgrade B → B' again, watcher stopped: the boot guard (ExecStartPre) restores B after the deadline"
	T0=$(date +%s)
	"$D" upgrade --base $API --version 0.10.0-3
	for _ in $(seq 1 60); do
		W=$(systemctl list-units --plain --no-legend 'opm-agent-upgrade-watch-*' | awk '{print $1}' | head -n 1)
		[ -n "$W" ] && break
		sleep 0.5
	done
	echo "stopping the watcher $W (simulates a reboot during the watch); opm-agent.prev made non-executable so the guard"
	echo "cannot start a new watcher either — only the boot guard's restore is left"
	systemctl kill --signal=KILL "$W" || true
	chmod 0644 /usr/local/bin/opm-agent.prev
	"$D" wait-phase --base $API --version rolled-back --timeout 180
	echo "B' rolled back by the $(sed -n 's/.*"by": "\([a-z]*\)".*/\1/p' /var/lib/opm/upgrade/rolled-back.json) after $(($(date +%s) - T0)) s"
	"$D" wait --base $API --version 0.10.0-2 --timeout 30
	journalctl -u opm-agent --no-pager | grep -i "upgrade-guard" | tail -n 5 || true
	cat /var/lib/opm/upgrade/rolled-back.json
	chmod 0755 /usr/local/bin/opm-agent.prev
fi

step "activities"
"$D" acts --base $API
step "heartbeat upgrade reports (cloud)"
"$D" http GET "$ADMIN/reports?site=app" | tr ',' '\n' | grep '"phase"' | uniq -c || true
echo
echo "SMOKE $MODE: PASS"
