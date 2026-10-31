package apitest

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/routes"
)

// Port of backend/test/21-install-upgrade-e2e.test.ts.
func TestInstallUpgradeE2E(t *testing.T) {
	const NEW = "0.10.0-10090000"
	const OLD = "0.7.2-10080232"

	t.Run("installer upgrade-only e2e: enrolled 0.7.2 site → pinned agent, no token, upgrade-guard --install", func(t *testing.T) {
		a := instDockerArch(t)
		// the "new" agent: knows verify-release, status, upgrade-guard (records its arguments)
		newBin := `#!/bin/sh
case "$1" in
  version) echo "opm-agent ` + NEW + ` linux/` + a + `";;
  verify-release) exit 0;;
  status) if [ -f /var/lib/opm/identity.json ]; then echo '{"enrolled": true, "siteSlug": "app"}'; else echo '{"enrolled": false}'; fi;;
  upgrade-guard)
    case "$2" in
      --help) echo "usage: opm-agent upgrade-guard --install [--data-dir DIR] [--binary PATH] [--no-reload]"; exit 0;;
      --install) echo "$*" > /tmp/guard-called; exit 0;;
    esac; exit 2;;
  *) echo "unknown command: $1" >&2; exit 2;;
esac
`
		// answers the local agent GUI check of step 6 (no real agent runs in the container); everything else = real curl
		curlWrap := `#!/bin/sh
case "$*" in
  *127.0.0.1:7080/_opm/api/info*) echo '{"enrolled": true}'; exit 0;;
  *127.0.0.1:7080/_opm/api/health*) /usr/local/bin/opm-agent version | grep -q ` + NEW + ` || exit 22; echo '{"ok":true,"version":"` + NEW + `","schema":2,"cloud":{"connected":true}}'; exit 0;;
esac
exec /usr/bin/curl "$@"
`
		// the old agent on the server (0.7.2: no upgrade, no upgrade-guard)
		oldBin := `#!/bin/sh
case "$1" in
  version) echo "opm-agent ` + OLD + ` linux/` + a + `";;
  status) echo '{"enrolled": true}';;
  *) echo "unknown command: $1" >&2; exit 2;;
esac
`
		pub, seed := instGenKey(t)
		token := "opi_" + strings.Repeat("U", 43)
		dep := "DepUpg00000000000001"
		newSha := Sha256Hex(newBin)
		st := instServe(t, token, func(st *instStub, w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			switch {
			case r.URL.Path == "/v1/install/script":
				w.Header().Set("Content-Type", "text/x-shellscript")
				_, _ = io.WriteString(w, routes.InstallScript(routes.InstallScriptOpts{
					ApiBase: st.URL, DeploymentID: dep, SiteSlug: "app", Token: token, ReleasePublicKey: pub,
					Agent:     &routes.PinnedAgentInfo{Version: NEW, Files: map[string]routes.ArchFile{a: {Sha256: newSha, Sig: cloud.SignHex(newSha, seed)}}},
					Docker:    &routes.PinnedDocker{Version: "28.5.2", MinAPI: "1.41", Sha256: map[string]string{a: strings.Repeat("c", 64)}},
					PortAgent: doc.Undefined, PortAlias: doc.Undefined, PortEdge: doc.Undefined,
					UpgradeOnly: true, SeenIP: "139.59.2.56",
				}))
			case r.URL.Path == "/v1/install/binary" && q.Get("arch") == a && q.Get("version") == NEW:
				w.Header().Set("X-OPM-Sha256", newSha)
				_, _ = io.WriteString(w, newBin)
			default:
				w.WriteHeader(404)
			}
		})
		get := fmt.Sprintf(`curl -fsSL -H "Authorization: Bearer %s" %s/v1/install/script`, token, st.URL)
		inner := strings.Join([]string{
			`set -e`,
			`apt-get update -qq >/dev/null && apt-get install -y -qq curl ca-certificates >/dev/null`,
			// existing Docker (fake CLI, API 1.43) so step 2 passes without the bundle
			`printf '#!/bin/sh\ncase "$1" in version) echo "1.43 1.12 24.0.7";; info) exit 0;; --version) echo "Docker version 24.0.7, build fake";; esac\n' > /usr/local/bin/docker; chmod +x /usr/local/bin/docker`,
			// the enrolled site: old agent 0.7.2, agent.json of this deployment/site, an enrolled data dir
			`old() { echo ` + instB64(oldBin) + ` | base64 -d > /usr/local/bin/opm-agent; chmod +x /usr/local/bin/opm-agent; }`,
			`old`,
			`mkdir -p /etc/opm /var/lib/opm`,
			`printf '{"apiBase":"http://x","deploymentId":"` + dep + `","siteSlug":"app"}\n' > /etc/opm/agent.json`,
			`echo '{"siteSlug":"app"}' > /var/lib/opm/identity.json`,
			`echo "== BEFORE"; /usr/local/bin/opm-agent version; /usr/local/bin/opm-agent upgrade 2>&1 || true`,
			`echo "== DRY"; ` + get + ` | OPM_YES=1 DRY_RUN=1 sh; echo "== DRYEND"`,
			`echo "== SHA"; sha256sum /usr/local/bin/opm-agent; /usr/local/bin/opm-agent version; test -e /etc/opm/install-token && echo TOKEN-WRITTEN || echo no-token`,
			// real path: fake systemd (systemctl + /run/systemd/system) and a curl wrapper answering the local agent GUI
			`old`,
			`mkdir -p /run/systemd/system /etc/systemd/system`,
			`printf '#!/bin/sh\necho "systemctl $*" >> /tmp/systemctl.log\nexit 0\n' > /usr/local/sbin/systemctl; chmod +x /usr/local/sbin/systemctl`,
			`echo ` + instB64(curlWrap) + ` | base64 -d > /usr/local/sbin/curl; chmod +x /usr/local/sbin/curl`,
			`echo "== REAL"; ` + get + ` | OPM_YES=1 sh; echo "== REALEND"`,
			`echo "== GUARD"; cat /tmp/guard-called`,
			`echo "== SYSTEMCTL"; cat /tmp/systemctl.log`,
			`echo "== UNIT"; grep ExecStart /etc/systemd/system/opm-agent.service`,
			`echo "== AFTER"; sha256sum /usr/local/bin/opm-agent; test -e /etc/opm/install-token && echo TOKEN-WRITTEN || echo no-token`,
		}, "\n")
		status, out := instContainer(t, 800*time.Second, inner)
		Eq(t, status, 0, instTail(out, 5000))
		sec := func(a1, b1 string) string { return instSlice(t, out, a1, b1) }
		before := sec("== BEFORE", "== DRY")
		qOld, qNew := regexp.QuoteMeta(OLD), regexp.QuoteMeta(NEW)
		Match(t, before, `opm-agent `+qOld)
		Match(t, before, `unknown command: upgrade`, "the old agent cannot upgrade itself")
		dry := sec("== DRY", "== DRYEND")
		Match(t, dry, `This install command already enrolled site app: it upgrades / downgrades that server only`)
		Match(t, dry, `OK +upgrade of the enrolled site app`)
		Match(t, dry, `installed now: opm-agent `+qOld)
		Match(t, dry, `OK +upgraded opm-agent `+qOld+` -> `+qNew)
		Match(t, dry, `already enrolled \(identity in /var/lib/opm\): the same setup is kept, the install token is NOT used`)
		Match(t, dry, `Installed 0\.7\.2-10080232 → available 0\.10\.0-10090000: UPGRADE`)
		Match(t, dry, `release signature verified by openssl`)
		Match(t, dry, `DRY_RUN: would run: /usr/local/bin/opm-agent upgrade-guard --install --data-dir /var/lib/opm --binary /usr/local/bin/opm-agent`)
		Match(t, dry, `Upgrades from now on: cloud console Setup → Upgrade agent, or the agent GUI Agent page`)
		NoMatch(t, dry, `FAILED`, dry)
		shaSec := sec("== SHA", "== REAL")
		Contains(t, shaSec, newSha, shaSec)
		Match(t, shaSec, `opm-agent `+qNew)
		Match(t, shaSec, `no-token`)
		real := sec("== REAL", "== REALEND")
		NoMatch(t, real, `FAILED`, real)
		Match(t, real, `OK +upgraded opm-agent `+qOld+` -> `+qNew)
		Match(t, real, `OK +boot guard \(rollback of a failed self-upgrade\): /usr/local/lib/opm/upgrade-guard\.sh`)
		Match(t, real, `OK +opm-agent 0\.10\.0-10090000 is healthy`)
		Match(t, sec("== GUARD", "== SYSTEMCTL"), `(?m)^upgrade-guard --install --data-dir /var/lib/opm --binary /usr/local/bin/opm-agent$`)
		sc := sec("== SYSTEMCTL", "== UNIT")
		Match(t, sc, `systemctl stop opm-agent`)
		Match(t, sc, `systemctl restart opm-agent`)
		// the guard runs before the restart (it installs the ExecStartPre drop-in)
		after := out[strings.Index(out, "== AFTER"):]
		Contains(t, after, newSha)
		Match(t, after, `no-token`)
		Eq(t, st.BinaryHits(), 2, "one download per run (the old binary was put back)")
	})
}
