#!/bin/sh
# OPM agent boot guard (SPEC-final §21 #90 c). Single source: embedded in opm-agent and installed by
# `opm-agent upgrade-guard --install`; systemd runs it before every start of opm-agent.service:
#   ExecStartPre=-/bin/sh /usr/local/lib/opm/upgrade-guard.sh <dataDir> <binary>
# When an agent upgrade is pending (<dataDir>/upgrade/pending.json), its health deadline has passed, the new agent
# never wrote <dataDir>/upgrade/healthy, and <binary> is still the new release (sha256), it restores <binary>.prev
# (a crash-looping new binary, a reboot during the watch). While the deadline is still ahead and no watcher runs
# (e.g. after a reboot), it starts the known-good watcher again. It never fails the unit: it always exits 0.
set -u

DATA="${1:-/var/lib/opm}"
BIN="${2:-/usr/local/bin/opm-agent}"
UP="$DATA/upgrade"
PENDING="$UP/pending.json"
LOCK="$UP/rollback.lock"

log() { echo "opm upgrade-guard: $*" >&2; }

[ -f "$PENDING" ] || exit 0

# field KEY: the value of "KEY": "…" or "KEY": 123 in pending.json (written by the agent, one key per line)
field() {
	sed -n "s/^[[:space:]]*\"$1\":[[:space:]]*\"\{0,1\}\([^\",]*\)\"\{0,1\},\{0,1\}[[:space:]]*\$/\1/p" "$PENDING" | head -n 1
}

sha() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	else
		openssl dgst -sha256 -r "$1" | cut -d' ' -f1
	fi
}

healthy() {
	[ -f "$UP/healthy" ] && [ "$(tr -d '[:space:]' <"$UP/healthy" 2>/dev/null)" = "$ID" ]
}

ID=$(field upgradeId)
FROM=$(field from)
TO=$(field to)
WANT=$(field toSha256)
DEADLINE=$(field deadline)

case "$DEADLINE" in
'' | *[!0-9]*)
	log "pending.json has no deadline; ignored"
	exit 0
	;;
esac
[ "$DEADLINE" -gt 0 ] || exit 0 # no deadline: an upgrade without an automatic restart
healthy && exit 0

NOW=$(date +%s)
if [ "$NOW" -lt "$DEADLINE" ]; then
	# still inside the health window: make sure the known-good watcher runs (it does not survive a reboot)
	if [ -f "$UP/watch.pid" ] && kill -0 "$(cat "$UP/watch.pid" 2>/dev/null)" 2>/dev/null; then
		exit 0
	fi
	if [ -x "$BIN.prev" ] && command -v systemd-run >/dev/null 2>&1; then
		systemd-run --quiet --collect --unit "opm-agent-upgrade-watch-$ID-g$NOW" \
			"$BIN.prev" upgrade-watch --data-dir "$DATA" --binary "$BIN" >/dev/null 2>&1 ||
			log "could not start the upgrade watcher"
	fi
	exit 0
fi

# past the deadline without a healthy marker: roll back (once; the watcher uses the same lock)
if ! mkdir "$LOCK" 2>/dev/null; then
	if [ -n "$(find "$LOCK" -maxdepth 0 -mmin +5 2>/dev/null)" ]; then
		rmdir "$LOCK" 2>/dev/null
		mkdir "$LOCK" 2>/dev/null || exit 0
	else
		log "a rollback is already running"
		exit 0
	fi
fi
trap 'rmdir "$LOCK" 2>/dev/null' EXIT

[ -f "$PENDING" ] || exit 0
healthy && exit 0

AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
write_rolled_back() {
	printf '{\n  "upgradeId": "%s",\n  "from": "%s",\n  "to": "%s",\n  "restored": "%s",\n  "reason": "%s",\n  "by": "guard",\n  "at": "%s"\n}\n' \
		"$ID" "$FROM" "$TO" "$FROM" "$1" "$AT" >"$UP/rolled-back.json.tmp" &&
		mv -f "$UP/rolled-back.json.tmp" "$UP/rolled-back.json"
}

if [ ! -f "$BIN.prev" ]; then
	log "agent $TO was not healthy before the deadline, but $BIN.prev is missing: cannot roll back (reinstall with the install command)"
	exit 0
fi

CUR=$(sha "$BIN")
if [ -n "$WANT" ] && [ "$CUR" != "$WANT" ]; then
	# not the new release anymore (restored already, or replaced by hand)
	if [ "$CUR" = "$(sha "$BIN.prev")" ] && [ ! -f "$UP/rolled-back.json" ]; then
		write_rolled_back "the new agent was not healthy within the deadline (restored before the guard ran)"
	fi
	rm -f "$PENDING"
	exit 0
fi

if cp "$BIN.prev" "$BIN.guard-tmp" && chmod 755 "$BIN.guard-tmp" && mv -f "$BIN.guard-tmp" "$BIN"; then
	write_rolled_back "agent $TO was not healthy within the deadline (crash loop or reboot); the boot guard restored $FROM"
	rm -f "$PENDING"
	sync 2>/dev/null
	log "restored $BIN.prev (agent $FROM): agent $TO was not healthy before the deadline"
else
	rm -f "$BIN.guard-tmp"
	log "could not restore $BIN.prev"
fi
exit 0
