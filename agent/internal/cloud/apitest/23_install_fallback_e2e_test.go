package apitest

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/routes"
)

type instFake struct {
	version           string
	json              bool
	schema, minSchema int // 0 = unset (1 in the binary)
	healthy, units    bool
}

// instFakeAgent is fakeAgent().
func instFakeAgent(f instFake) string {
	schema, minSchema := f.schema, f.minSchema
	if schema == 0 {
		schema = 1
	}
	if minSchema == 0 {
		minSchema = 1
	}
	meta := fmt.Sprintf(`{"version":"%s","schema":%d,"minSchema":%d,"commit":null,"builtAt":null}`, f.version, schema, minSchema)
	sub := func(name string, on bool, act string) string {
		help, inst := `echo "unknown command: `+name+`" >&2; exit 2`, `exit 2`
		if on {
			help, inst = `echo "usage: opm-agent `+name+` --install"; exit 0`, act
		}
		return `  ` + name + `) case "$2" in --help) ` + help + `;; --install) ` + inst + `;; esac;;`
	}
	version := `echo "opm-agent ` + f.version + ` linux/x"`
	if f.json {
		version = `if [ "${2:-}" = --json ]; then echo '` + meta + `'; else echo "opm-agent ` + f.version + ` linux/x"; fi`
	}
	health := `exit 22`
	if f.json && f.healthy {
		health = fmt.Sprintf(`echo '{"ok":true,"version":"%s","schema":%d,"pid":1,"startedAt":"x","cloud":{"connected":true,"route":"direct","lastContactAt":null},"docker":{"ok":true},"socketActivated":true,"checks":[]}'`, f.version, schema)
	}
	info := `exit 7`
	if f.healthy {
		info = `echo '{"deploymentId":"D","siteSlug":"app","agentVersion":"` + f.version + `","enrolled": true}'`
	}
	return `#!/bin/sh
# fake opm-agent ` + f.version + `
case "$1" in
  version) ` + version + `;;
  verify-release) exit 0;;
  status) echo '{"enrolled": true}';;
` + sub("systemd-units", f.units, `echo "systemd-units $*" >> /tmp/units.log; [ "${3:-}" = --socket ] && touch /etc/systemd/system/opm-agent.socket; echo "[Service]" > /etc/systemd/system/opm-agent.service; exit 0`) + `
` + sub("upgrade-guard", f.units, `echo "upgrade-guard $*" >> /tmp/units.log; exit 0`) + `
  fake-health) ` + health + `;;
  fake-info) ` + info + `;;
  *) echo "unknown command: $1" >&2; exit 2;;
esac
`
}

const instSystemctl = `#!/bin/sh
echo "systemctl $*" >> /tmp/systemctl.log
exit 0
`

const instDocker = `#!/bin/sh
echo "docker $*" >> /tmp/docker.log
case "$1" in version) echo "1.43 1.12 24.0.7";; info) exit 0;; --version) echo "Docker version 24.0.7, build fake";; *) exit 0;; esac
`

const instCurl = `#!/bin/sh
case "$*" in
  *127.0.0.1:7080/_opm/api/health*) exec /usr/local/bin/opm-agent fake-health;;
  *127.0.0.1:7080/_opm/api/info*) exec /usr/local/bin/opm-agent fake-info;;
esac
exec /usr/bin/curl "$@"
`

// Port of backend/test/23-install-fallback-e2e.test.ts.
func TestInstallFallbackE2E(t *testing.T) {
	const OLD = "0.7.2-10080232"
	const PIN = "0.10.0-10090000"
	const BAD = "0.10.1-10090100"
	const DOWN = "0.9.5-10080500"
	// not in the TS test: a target >= 0.19.1 reaches step 6b (the agent home offer), which must not touch a container
	// unless the operator asks for a move (see instCheckContainerRule)
	const HOME = "0.20.0-10100000"

	t.Run("install command = hard fallback: prompts, flags, upgrade / same / rollback / downgrade, backups, no container touched", func(t *testing.T) {
		a := instDockerArch(t)
		pub, seed := instGenKey(t)
		type build struct {
			bin               string
			schema, minSchema any
		}
		bins := map[string]build{
			PIN:  {instFakeAgent(instFake{version: PIN, json: true, schema: 2, minSchema: 1, healthy: true, units: true}), 2.0, 1.0},
			BAD:  {instFakeAgent(instFake{version: BAD, json: true, schema: 3, minSchema: 1, healthy: false, units: true}), 3.0, 1.0},
			DOWN: {instFakeAgent(instFake{version: DOWN, json: false, healthy: true, units: false}), nil, nil},
			HOME: {instFakeAgent(instFake{version: HOME, json: true, schema: 2, minSchema: 1, healthy: true, units: true}), 2.0, 1.0},
		}
		// the Docker "bundle": docker/docker = the logging shim, docker/dockerd
		bd := t.TempDir()
		NoErr(t, os.Mkdir(filepath.Join(bd, "docker"), 0o777))
		instWrite(t, filepath.Join(bd, "docker", "docker"), instDocker, 0o755)
		instWrite(t, filepath.Join(bd, "docker", "dockerd"), "#!/bin/sh\nexit 0\n", 0o755)
		if b, err := exec.Command("tar", "-czf", filepath.Join(bd, "docker.tgz"), "-C", bd, "docker").CombinedOutput(); err != nil {
			t.Fatalf("tar: %v\n%s", err, b)
		}
		tgz, err := os.ReadFile(filepath.Join(bd, "docker.tgz"))
		NoErr(t, err)
		token := "opi_" + strings.Repeat("F", 43)
		dep := "DepFallback000000001"
		st := instServe(t, token, func(st *instStub, w http.ResponseWriter, r *http.Request) {
			v := r.URL.Query().Get("version")
			x, known := bins[v]
			switch {
			case r.URL.Path == "/v1/install/script":
				pin := bins[PIN]
				sha := Sha256Hex(pin.bin)
				w.Header().Set("Content-Type", "text/x-shellscript")
				_, _ = io.WriteString(w, routes.InstallScript(routes.InstallScriptOpts{
					ApiBase: st.URL, DeploymentID: dep, SiteSlug: "app", Token: token, ReleasePublicKey: pub,
					Agent:     &routes.PinnedAgentInfo{Version: PIN, Files: map[string]routes.ArchFile{a: {Sha256: sha, Sig: cloud.SignHex(sha, seed)}}, Schema: pin.schema, MinSchema: pin.minSchema},
					Docker:    &routes.PinnedDocker{Version: "28.5.2", MinAPI: "1.41", Sha256: map[string]string{a: Sha256Hex(tgz)}},
					PortAgent: doc.Undefined, PortAlias: doc.Undefined, PortEdge: doc.Undefined,
					UpgradeOnly: true,
				}))
			case r.URL.Path == "/v1/install/release" && known:
				sha := Sha256Hex(x.bin)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, JOf(A{"version": v, "os": "linux", "arch": a, "sha256": sha, "signature": cloud.SignHex(sha, seed), "releasePublicKey": pub, "binaryPath": "/v1/install/binary?os=linux&arch=" + a + "&version=" + v, "notes": nil, "sizeBytes": len(x.bin), "schema": x.schema, "minSchema": x.minSchema}).String())
			case r.URL.Path == "/v1/install/binary" && known:
				_, _ = io.WriteString(w, x.bin)
			case r.URL.Path == "/v1/install/deps/docker":
				_, _ = w.Write(tgz)
			default:
				w.WriteHeader(404)
			}
		})
		url := st.URL + "/v1/install/script"
		put := func(path, body string) string {
			return "echo " + instB64(body) + " | base64 -d > " + path + "; chmod +x " + path
		}
		pipe := func(env string) string {
			return `curl -fsSL -H "Authorization: Bearer ` + token + `" ` + url + ` | ` + env + ` sh`
		}
		// answers typed on a fake terminal (script(1) gives the installer a /dev/tty)
		tty := func(answers, env string) string {
			return `(printf '` + answers + `'; sleep 2) | script -q -e -c '` + pipe(env) + `' /dev/null`
		}
		sc := func(name, cmd string) []string {
			return []string{`echo "== ` + name + `"`, cmd, `echo "rc=$?"`, `echo "bin=$(sha256sum /usr/local/bin/opm-agent | cut -d' ' -f1)"`, `echo "== END ` + name + `"`}
		}
		lines := []string{
			`apt-get update -qq >/dev/null && apt-get install -y -qq curl ca-certificates openssl >/dev/null`,
			`mkdir -p /etc/opm /var/lib/opm/backup /run/systemd/system /etc/systemd/system`,
			put("/usr/local/sbin/systemctl", instSystemctl),
			put("/usr/local/sbin/curl", instCurl),
			put("/usr/local/bin/opm-agent", instFakeAgent(instFake{version: OLD, json: false, healthy: true, units: false})),
			// agent.json (pretty, WITHOUT releasePublicKey → the script adds it), enrolled data dir, the DB
			`printf '{\n  "apiBase": "http://x",\n  "deploymentId": "` + dep + `",\n  "siteSlug": "app",\n  "dataDir": "/var/lib/opm"\n}\n' > /etc/opm/agent.json`,
			`printf "DB-v1" > /var/lib/opm/opm.db; printf "WAL-v1" > /var/lib/opm/opm.db-wal`,
			// 4 older backups written by earlier runs (the 5 newest are kept)
			`for i in 1 2 3 4; do d=/var/lib/opm/backup/0.6.$i-2026010${i}T000000Z; mkdir -p $d; printf "version=0.6.$i\nschema=1\n" > $d/meta.env; touch -d "2026-01-0$i" $d; done`,
		}
		add := func(l ...string) { lines = append(lines, l...) }
		// ── Docker missing
		add(sc("D-NOTTY", pipe(""))...)
		add(sc("D-TTY-NO", tty(`n\n`, ""))...)
		add(sc("D-YES-DRY", pipe("OPM_INSTALL_DOCKER=1 DRY_RUN=1"))...)
		add(`ls /usr/local/bin/dockerd >/dev/null && echo "dockerd-installed"`)
		// ── upgrade 0.7.2 → pin
		add(sc("U-TTY-NO", tty(`n\n`, ""))...)
		add(sc("U-NOTTY", pipe(""))...)
		add(sc("U-TTY-YES", tty(`y\n`, ""))...)
		add(`echo "== STATE-U"; cat /etc/opm/agent.json; ls /etc/opm; cat /tmp/units.log; sha256sum /usr/local/bin/opm-agent.prev; ls -1 /var/lib/opm/backup; cat /var/lib/opm/backup/0.7.2-*/meta.env; cat /var/lib/opm/backup/0.7.2-*/opm.db; echo; echo "== END STATE-U"`)
		// ── same version, reinstall (the old agent reported cloud connected → required again)
		add(sc("SAME", pipe("OPM_YES=1"))...)
		// ── unhealthy target → rollback (schema 2 → 3 differs: DB restored)
		add(`printf "DB-v2" > /var/lib/opm/opm.db`)
		add(sc("BAD", pipe("OPM_YES=1 OPM_AGENT_VERSION="+BAD+" OPM_HEALTH_TIMEOUT=6"))...)
		add(`echo "== STATE-BAD"; cat /var/lib/opm/opm.db; echo; echo "== END STATE-BAD"`)
		// ── downgrade pin (schema 2) → 0.9.5 (legacy schema 1): refused without a compatible backup
		add(`mkdir -p /tmp/bk && mv /var/lib/opm/backup/* /tmp/bk/`)
		add(sc("DOWN-REFUSED", pipe("OPM_YES=1 OPM_ALLOW_DOWNGRADE=1 OPM_AGENT_VERSION="+DOWN))...)
		add(`mv /tmp/bk/* /var/lib/opm/backup/`)
		add(sc("DOWN-NOTTY", pipe("OPM_YES=1 OPM_AGENT_VERSION="+DOWN))...)
		add(sc("DOWN-TTY-NO-RESTORE", tty(`y\ny\nn\n`, "OPM_AGENT_VERSION="+DOWN))...)
		add(sc("DOWN-YES", pipe("OPM_YES=1 OPM_ALLOW_DOWNGRADE=1 OPM_RESTORE_DB_BACKUP=1 OPM_AGENT_VERSION="+DOWN))...)
		add(`echo "== STATE-DOWN"; cat /var/lib/opm/opm.db; echo; ls /etc/systemd/system; grep ExecStart /etc/systemd/system/opm-agent.service; ls -1 /var/lib/opm/backup | wc -l; ls -1 /var/lib/opm/backup; echo "== END STATE-DOWN"`)
		// ── a target >= 0.19.1 without a terminal or OPM_MOVE_HOME: step 6b shows the home, moves nothing, stops nothing
		add(sc("HOME-NO-MOVE", pipe("OPM_YES=1 OPM_AGENT_VERSION="+HOME))...)
		add(`echo "== DOCKERLOG"; sort -u /tmp/docker.log; echo "== END DOCKERLOG"`)
		add(`echo "== SYSTEMCTL"; cat /tmp/systemctl.log; echo "== END SYSTEMCTL"`)
		// the Docker step of the first runs needs "no docker": the shim is only installed by the bundle (D-YES-DRY)
		status, out := instContainer(t, 1100*time.Second, strings.Join(lines, "\n"))
		instWrite(t, filepath.Join(os.TempDir(), "opm-install-fallback-e2e.log"), out, 0o644)
		Eq(t, status, 0, instTail(out, 6000))
		sec := func(n string) string {
			i, j := strings.Index(out, "== "+n+"\n"), strings.Index(out, "== END "+n+"\n")
			if !(i >= 0 && j > i) {
				t.Fatalf("section %s missing\n%s", n, instTail(out, 3000))
			}
			return out[i:j]
		}
		rc := func(s string) int {
			m := regexp.MustCompile(`rc=(\d+)`).FindStringSubmatch(s)
			if m == nil {
				t.Fatalf("no rc in\n%s", s)
			}
			n, _ := strconv.Atoi(m[1])
			return n
		}
		binOf := func(s string) string {
			m := regexp.MustCompile(`bin=([0-9a-f]{64})`).FindStringSubmatch(s)
			if m == nil {
				t.Fatalf("no bin in\n%s", s)
			}
			return m[1]
		}
		shaPin, shaDown := Sha256Hex(bins[PIN].bin), Sha256Hex(bins[DOWN].bin)

		s := sec("D-NOTTY")
		Eq(t, rc(s), 1, s)
		Match(t, s, `Docker Engine is not installed\. Install the pinned Docker 28\.5\.2 now\? \[y/N\] no \(no terminal to ask\)`)
		Contains(t, s, `curl -fsSL -H "Authorization: Bearer `+token+`" "`+url+`" | sudo OPM_INSTALL_DOCKER=1 sh`, s)
		Match(t, s, `MANUAL: install Docker Engine 20\.10 or newer yourself`)
		s = sec("D-TTY-NO")
		Eq(t, rc(s), 1, s)
		Match(t, s, `Install the pinned Docker 28\.5\.2 now\? \[y/N\]`)
		Match(t, s, `Docker Engine is required; nothing was changed`)
		NotContains(t, s, "no terminal to ask", "the fake TTY was used")
		s = sec("D-YES-DRY")
		Match(t, s, `\[y/N\] yes \(OPM_INSTALL_DOCKER\)`)
		Match(t, s, `downloaded docker-28\.5\.2\.tgz`)
		Match(t, s, `Installed `+regexp.QuoteMeta(OLD)+` → available `+regexp.QuoteMeta(PIN)+`: UPGRADE`)
		Match(t, s, `Nothing changed \(opm-agent 0\.7\.2-10080232 keeps running\)`, "OPM_INSTALL_DOCKER answers only the Docker question")
		Eq(t, rc(s), 0)
		Match(t, out, `dockerd-installed`)

		s = sec("U-TTY-NO")
		Eq(t, rc(s), 0, s)
		Match(t, s, `Installed `+regexp.QuoteMeta(OLD)+` → available `+regexp.QuoteMeta(PIN)+`: UPGRADE`)
		Match(t, s, `Nothing changed`)
		NotEq(t, binOf(s), shaPin)
		s = sec("U-NOTTY")
		Eq(t, rc(s), 0)
		Contains(t, s, "| sudo OPM_YES=1 sh", s)
		s = sec("U-TTY-YES")
		Eq(t, rc(s), 0, s)
		Eq(t, binOf(s), shaPin)
		for _, re := range []string{
			`release signature verified by openssl`,
			`agent\.json: \+ "releasePublicKey"`,
			`already enrolled .*the install token is NOT used`,
			`opm-agent stopped for the swap \(app containers keep running\)`,
			`database backup: /var/lib/opm/backup/0\.7\.2-10080232-\d{8}T\d{6}Z \(2 file\(s\), agent 0\.7\.2-10080232, schema 1\)`,
			`upgraded opm-agent 0\.7\.2-10080232 -> 0\.10\.0-10090000 \(previous binary kept as /usr/local/bin/opm-agent\.prev\)`,
			`systemd units written by opm-agent systemd-units --install --socket`,
			`opm-agent 0\.10\.0-10090000 is healthy`,
			`Upgrades from now on: cloud console Setup → Upgrade agent`,
		} {
			Match(t, s, re)
		}
		NoMatch(t, s, `FAILED`, s)
		st2 := sec("STATE-U")
		Match(t, st2, `"releasePublicKey": "`)
		Match(t, st2, `"dataDir": "/var/lib/opm"`, "existing keys kept")
		Match(t, st2, `agent\.json\.bak`)
		NoMatch(t, st2, `install-token`, "the token is never written for an enrolled site")
		Match(t, st2, `systemd-units --install --socket`)
		Match(t, st2, `version=0\.7\.2-10080232\nschema=1\n`)
		Match(t, st2, `DB-v1`)

		s = sec("SAME")
		Eq(t, rc(s), 0, s)
		Match(t, s, `available 0\.10\.0-10090000: SAME \(reinstall\)`)
		Match(t, s, `reinstalled opm-agent 0\.10\.0-10090000 -> 0\.10\.0-10090000`)
		Match(t, s, `opm-agent 0\.10\.0-10090000 is healthy and connected to the cloud`)

		s = sec("BAD")
		Eq(t, rc(s), 1, s)
		Match(t, s, `target agent 0\.10\.1-10090100 \(OPM_AGENT_VERSION`)
		Match(t, s, `is not healthy \(no healthy answer on 127\.0\.0\.1:7080 with version 0\.10\.1-10090100\) — rolling back to 0\.10\.0-10090000`)
		Match(t, s, `database restored from /var/lib/opm/backup/0\.10\.0-10090000-`)
		Match(t, s, `opm-agent 0\.10\.0-10090000 runs again \(rolled back\)`)
		Match(t, s, `FAILED at step 6/7 \(Verifying\): opm-agent 0\.10\.1-10090100 was not healthy within 6 s`)
		Eq(t, binOf(s), shaPin, "the previous binary is back")
		Match(t, sec("STATE-BAD"), `DB-v2`)

		s = sec("DOWN-REFUSED")
		Eq(t, rc(s), 1, s)
		Match(t, s, `available 0\.9\.5-10080500: DOWNGRADE`)
		Match(t, s, `the database \(schema 2, written by 0\.10\.0-10090000\) is too new for 0\.9\.5-10080500 \(schema 1, min 0\)`)
		Match(t, s, `downgrade refused: no database backup`)
		Eq(t, binOf(s), shaPin)
		s = sec("DOWN-NOTTY")
		Eq(t, rc(s), 0, s)
		Match(t, s, `Really downgrade 0\.10\.0-10090000 → 0\.9\.5-10080500\? \[y/N\] no \(no terminal to ask\)`)
		Contains(t, s, "| sudo OPM_ALLOW_DOWNGRADE=1 OPM_YES=1 sh", s)
		s = sec("DOWN-TTY-NO-RESTORE")
		Eq(t, rc(s), 0, s)
		Match(t, s, `backup /var/lib/opm/backup/0\.7\.2-10080232-[^:]+: agent 0\.7\.2-10080232, schema 1`)
		Match(t, s, `Restore this database backup for 0\.9\.5-10080500\?`)
		Match(t, s, `Nothing changed`)
		Eq(t, binOf(s), shaPin)
		s = sec("DOWN-YES")
		Eq(t, rc(s), 0, s)
		Eq(t, binOf(s), shaDown)
		Match(t, s, `restored the database backup /var/lib/opm/backup/0\.7\.2-10080232-`)
		Match(t, s, `downgraded opm-agent 0\.10\.0-10090000 -> 0\.9\.5-10080500`)
		Match(t, s, `removed opm-agent\.socket`)
		Match(t, s, `wrote /etc/systemd/system/opm-agent\.service`)
		Match(t, s, `boot guard \(rollback of a failed self-upgrade\): not available`)
		Match(t, s, `opm-agent 0\.9\.5-10080500 is healthy`)
		st2 = sec("STATE-DOWN")
		Match(t, st2, `DB-v1`, "the compatible DB backup was restored")
		NoMatch(t, st2, `opm-agent\.socket`)
		Match(t, st2, `ExecStart=/usr/local/bin/opm-agent run --config /etc/opm/agent\.json`)
		Match(t, st2, `\n\s*5\n`, "5 newest backups kept\n"+st2)
		s = sec("HOME-NO-MOVE")
		Eq(t, rc(s), 0, s)
		Eq(t, binOf(s), Sha256Hex(bins[HOME].bin))
		Match(t, s, `agent home /var/lib/opm: `)
		NotContains(t, s, "stop for the move", s)
		dl := []string{}
		for _, l := range strings.Split(sec("DOCKERLOG"), "\n") {
			if strings.HasPrefix(l, "docker ") {
				dl = append(dl, l)
			}
		}
		True(t, len(dl) > 0)
		for _, l := range dl {
			Match(t, l, `^docker (version|info|--version)\b`, "the installer must never touch containers: "+l)
		}
		sys := sec("SYSTEMCTL")
		NoMatch(t, sys, `systemctl (stop|restart|disable)[^\n]*docker`, "docker is never stopped / restarted")
		Match(t, sys, `systemctl restart opm-agent`)
		Match(t, sys, `systemctl start opm-agent\.socket`)
	})
}
