package apitest

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/routes"
)

// instSample is SAMPLE of 17-install-script.test.ts.
func instSample() routes.InstallScriptOpts {
	return routes.InstallScriptOpts{
		ApiBase:          "https://api.opm.example",
		DeploymentID:     "Dep0000000000000000a",
		SiteSlug:         "app",
		Token:            "opi_" + strings.Repeat("A", 43),
		ReleasePublicKey: "Af0P9kVPsXG0cMwfCGgLSqZjaHwLViQDd6cKv1npyi0=",
		Agent: &routes.PinnedAgentInfo{Version: "0.7.0-test", Files: map[string]routes.ArchFile{
			"amd64": {Sha256: strings.Repeat("a", 64), Sig: "sigA"},
			"arm64": {Sha256: strings.Repeat("b", 64), Sig: "sigB"},
		}},
		Docker:    &routes.PinnedDocker{Version: "28.5.2", MinAPI: "1.41", Sha256: map[string]string{"amd64": strings.Repeat("c", 64), "arm64": strings.Repeat("d", 64)}},
		PortAgent: float64(7080), PortAlias: float64(8080), PortEdge: float64(80),
	}
}

// instRun is spawnSync: the exit status (-1 = could not start or killed), stdout, stderr.
type instRun struct {
	Status      int
	Out, ErrOut string
	Err         error
}

func instSpawn(input *string, env []string, name string, args ...string) instRun {
	c := exec.Command(name, args...)
	if input != nil {
		c.Stdin = strings.NewReader(*input)
	}
	if env != nil {
		c.Env = append(os.Environ(), env...)
	}
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	err := c.Run()
	r := instRun{Status: 0, Out: out.String(), ErrOut: errb.String()}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		r.Status = ee.ExitCode()
	} else if err != nil {
		r.Status, r.Err = -1, err
	}
	return r
}

func instShN(t testing.TB, file string) {
	t.Helper()
	if r := instSpawn(nil, nil, "sh", "-n", file); r.Status != 0 {
		t.Fatalf("sh -n: %s", r.ErrOut)
	}
}

// instDashN is `dash -n` when dash exists.
func instDashN(t testing.TB, file string) {
	t.Helper()
	if r := instSpawn(nil, nil, "dash", "-n", file); r.Err == nil {
		Eq(t, r.Status, 0, r.ErrOut)
	}
}

// instShellcheck is shellcheck(file): must be silent when shellcheck exists.
func instShellcheck(t testing.TB, file, what string) {
	t.Helper()
	if r := instSpawn(nil, nil, "shellcheck", "-s", "sh", file); r.Err == nil {
		Eq(t, strings.TrimSpace(r.Out+r.ErrOut), "", "shellcheck"+what+":\n"+r.Out)
	}
}

// instWrite writes a test file.
func instWrite(t testing.TB, file, body string, mode os.FileMode) {
	t.Helper()
	NoErr(t, os.WriteFile(file, []byte(body), mode))
}

// instSlice is s.slice(s.indexOf(a), s.indexOf(b)); a missing marker fails the test.
func instSlice(t testing.TB, s, a, b string) string {
	t.Helper()
	i, j := strings.Index(s, a), strings.Index(s, b)
	if i < 0 || j < i {
		t.Fatalf("section %q..%q missing\n%s", a, b, instTail(s, 4000))
	}
	return s[i:j]
}

func instTail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// instDockerArch is dockerArch() of the e2e files, skipping the test under -short or without Docker.
func instDockerArch(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("-short")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.Architecture}}").Output()
	if err != nil {
		t.Skip("no Docker available")
	}
	switch strings.TrimSpace(string(b)) {
	case "aarch64", "arm64":
		return "arm64"
	case "x86_64", "amd64":
		return "amd64"
	}
	t.Skip("no Docker available")
	return ""
}

// instGenKey generates a release key: public key + seed, base64.
func instGenKey(t testing.TB) (pub, seed string) {
	t.Helper()
	p, k, err := ed25519.GenerateKey(rand.Reader)
	NoErr(t, err)
	return base64.StdEncoding.EncodeToString(p), base64.StdEncoding.EncodeToString(k.Seed())
}

// instStub is the stub install server of the e2e files, on 0.0.0.0 of a free port (the container reaches it through
// host.docker.internal). A request without the bearer token is 401.
type instStub struct {
	Port int
	URL  string // the base URL as the container sees it
	mu   sync.Mutex
	hits []string
}

func instServe(t testing.TB, token string, h func(st *instStub, w http.ResponseWriter, r *http.Request)) *instStub {
	t.Helper()
	l, err := net.Listen("tcp", "0.0.0.0:0")
	NoErr(t, err)
	st := &instStub{Port: l.Addr().(*net.TCPAddr).Port}
	st.URL = fmt.Sprintf("http://host.docker.internal:%d", st.Port)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		st.hits = append(st.hits, r.URL.RequestURI())
		st.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(401)
			return
		}
		h(st, w, r)
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return st
}

// BinaryHits counts the requests of /v1/install/binary.
func (s *instStub) BinaryHits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, h := range s.hits {
		if strings.HasPrefix(h, "/v1/install/binary") {
			n++
		}
	}
	return n
}

// instContainer runs an sh script in a --rm ubuntu:24.04 container, with a name of its own so that a timeout leaves
// nothing behind.
func instContainer(t testing.TB, timeout time.Duration, inner string) (int, string) {
	t.Helper()
	name := Uniq("opm-install-e2e-")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := exec.CommandContext(ctx, "docker", "run", "--rm", "--name", name, "--add-host=host.docker.internal:host-gateway", "ubuntu:24.04", "sh", "-c", inner)
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, out.String()
	case errors.As(err, &ee):
		return ee.ExitCode(), out.String()
	}
	t.Fatalf("docker run: %v\n%s", err, instTail(out.String(), 3000))
	return 0, ""
}

// instB64 is Buffer.from(s).toString('base64').
func instB64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// Port of backend/test/17-install-script.test.ts.
func TestInstallScript(t *testing.T) {
	t.Run("install script: sh -n, shellcheck, pinned versions, docker auto-install branch + OPM_SKIP_DOCKER_INSTALL guard", func(t *testing.T) {
		sh := routes.InstallScript(instSample())
		file := filepath.Join(t.TempDir(), "install.sh")
		instWrite(t, file, sh, 0o644)
		instShN(t, file) // fails on a syntax error
		instShellcheck(t, file, "")
		// the token never appears on curl's command line (ps shows it to every local user): a 0600 header file in the temp dir
		Contains(t, sh, `-H @"$TMP/auth.header"`)
		Contains(t, sh, `(umask 077; printf 'Authorization: Bearer %s\n' "$TOKEN" > "$TMP/auth.header")`)
		NoMatch(t, sh, `curl [^\n]*-H "Authorization: Bearer \$TOKEN" "\$API\$1"`, "fetch() must not pass the token as an argument")
		// pinned (never "latest" at run time)
		NoMatch(t, sh, `(?i)latest`, `the script must not resolve "latest"`)
		Contains(t, sh, `AGENT_VERSION="0.7.0-test"`)
		Contains(t, sh, `ASHA="`+strings.Repeat("a", 64)+`"; ASIG="sigA"`)
		Contains(t, sh, `ASHA="`+strings.Repeat("b", 64)+`"; ASIG="sigB"`)
		Contains(t, sh, `/v1/install/binary?os=linux&arch=$ARCH&version=$TARGET`)
		Contains(t, sh, `DOCKER_VERSION="28.5.2"; MIN_DOCKER_API="1.41"; MAX_DOCKER_API="1.47"`)
		Contains(t, sh, `! ver_ge "$MAX_DOCKER_API" "$MIN_V"`, "existing Docker accepted when its min API <= the agent cap")
		Contains(t, sh, `DSHA="`+strings.Repeat("c", 64)+`"`)
		Contains(t, sh, `DSHA="`+strings.Repeat("d", 64)+`"`)
		// Docker: detection, auto-install of the pinned static bundle, the skip guard, too-old / too-new existing Docker untouched
		Contains(t, sh, `Docker Engine not found — installing the pinned static bundle Docker $DOCKER_VERSION`)
		Contains(t, sh, `/v1/install/deps/docker?arch=$ARCH`)
		Contains(t, sh, `SKIP_DOCKER="${OPM_SKIP_DOCKER_INSTALL:-0}"`)
		Contains(t, sh, `[ -z "$ANS_DOCKER" ] && [ "$SKIP_DOCKER" = 1 ] && ANS_DOCKER=0`, "OPM_SKIP_DOCKER_INSTALL=1 = OPM_INSTALL_DOCKER=0")
		Contains(t, sh, `https://docs.docker.com/engine/install/`)
		Contains(t, sh, `this script does not touch an existing Docker`)
		Contains(t, sh, `ExecStart=/usr/local/bin/dockerd -H fd:// --containerd=/run/containerd/containerd.sock`)
		Contains(t, sh, `"max-size": "10m", "max-file": "3"`)
		Contains(t, sh, `"live-restore": true`)
		Contains(t, sh, `/etc/opm/installed-deps.json`)
		Contains(t, sh, `systemctl enable --now docker`)
		// logging, failure handling, idempotency, dry run, verification
		Contains(t, sh, `LOG=/var/log/opm-install.log`)
		Contains(t, sh, `tail -n 20 "$LOG"`)
		Contains(t, sh, `already enrolled`)
		Contains(t, sh, `DRY="${DRY_RUN:-0}"`)
		Contains(t, sh, `/_opm/api/info`)
		Contains(t, sh, `hostname -I`)
		Contains(t, sh, `ALIAS_PORT="8080"`)
		Contains(t, sh, `EDGE_PORT="80"`)
		Contains(t, sh, `ExecStart=/usr/local/bin/opm-agent run --config /etc/opm/agent.json`)
		// §21 #86 final step: listening ports (port 80 first), firewall report (never changes rules), self-check, upgrade line
		Contains(t, sh, `[ "$UPGRADE_ONLY" = 1 ]`)
		Contains(t, sh, `UPGRADE_ONLY="0"`)
		Contains(t, sh, `"releasePublicKey":"%s"`, "a new agent.json carries the release public key")
		Contains(t, sh, `ss -ltnH`)
		Contains(t, sh, `netstat -ltn`)
		Contains(t, sh, `/proc/net/tcp`)
		Contains(t, sh, `for p in "$HTTP_PORT" "$EDGE_PORT" 443 "$ALIAS_PORT" "$AGENT_PORT"; do`)
		Contains(t, sh, `sudo ufw allow $p/tcp`)
		Contains(t, sh, `sudo firewall-cmd --permanent --add-port=$p/tcp`)
		Contains(t, sh, `sudo firewall-cmd --reload`)
		Contains(t, sh, `no host firewall active (ufw / firewalld)`)
		NoMatch(t, sh, `ufw (allow|enable)[^\n]*\n\s*run `, "firewall rules are never changed")
		NoMatch(t, sh, `run (sudo )?(ufw|firewall-cmd|iptables)`, "firewall rules are never changed")
		Contains(t, sh, `curl -s -o /dev/null -m 3 -w '%{http_code}'`)
		Contains(t, sh, `reachable from this host`)
		Contains(t, sh, `NOT reachable from this host`)
		Contains(t, sh, `A cloud firewall (e.g. DigitalOcean Cloud Firewall, AWS security group) or NAT in front of the server is invisible from here`)
		NoMatch(t, sh, `(?i)takeover`, "no takeover wording: port 80 is the edge")
		Contains(t, sh, `port $HTTP_PORT is not listening — another program may hold it (ss -ltnp 'sport = :$HTTP_PORT'); the agent retries every 30 s; GUI also on$ALT_PORTS`)
		Contains(t, sh, `Upgrade later: rerun this same command (it stays valid for upgrades of this enrolled site), or run: sudo opm-agent upgrade (agent >= 0.8)`)
		// upgrade-only render: also sh -n / dash -n / shellcheck clean; refuses a server that is not the enrolled site
		o := instSample()
		o.UpgradeOnly, o.SeenIP = true, "::ffff:139.59.2.56"
		up := routes.InstallScript(o)
		instWrite(t, file, up, 0o644)
		instShN(t, file)
		instDashN(t, file)
		instShellcheck(t, file, " (upgrade-only)")
		Contains(t, up, `UPGRADE_ONLY="1"; SEEN_IP="139.59.2.56"`)
		Contains(t, up, `This install command already enrolled site $SITE; it can only upgrade that server`)
		o2 := instSample()
		o2.SeenIP = "1.2.3.4; rm -rf /"
		Eq(t, strings.Contains(routes.InstallScript(o2), `SEEN_IP=""`), true, "SEEN_IP is shell-safe")
		// §21 #90: boot guard step (agents >= 0.10.0 have `upgrade-guard`; older ones are skipped) + the GUI upgrade line
		Contains(t, up, `GUI_UPGRADE="0"`)
		Contains(t, up, `GH=$("$1" "$2" --help`)
		Contains(t, up, `"$BIN" upgrade-guard --install --data-dir "$DATA_DIR" --binary "$BIN"`)
		Contains(t, up, `DRY_RUN: would run: $BIN upgrade-guard --install --data-dir $DATA_DIR --binary $BIN`)
		Contains(t, up, `$PAST opm-agent $OLD_VER -> $(agent_ver)`)
		o3 := instSample()
		o3.Agent = &routes.PinnedAgentInfo{Version: "0.10.0-10090000", Files: o3.Agent.Files}
		o3.UpgradeOnly = true
		gui := routes.InstallScript(o3)
		instWrite(t, file, gui, 0o644)
		instShN(t, file)
		instDashN(t, file)
		instShellcheck(t, file, " (0.10 upgrade-only)")
		Contains(t, gui, `GUI_UPGRADE="1"`)
		Contains(t, gui, `Upgrades from now on: cloud console Setup → Upgrade agent, or the agent GUI Agent page`)
		// no docker manifest → the script still parses and points to the manual install
		o4 := instSample()
		o4.Docker, o4.Agent, o4.PortAgent, o4.PortAlias, o4.PortEdge = nil, nil, doc.Undefined, doc.Undefined, doc.Undefined
		noDeps := routes.InstallScript(o4)
		instWrite(t, file, noDeps, 0o644)
		instShN(t, file)
		Contains(t, noDeps, `DOCKER_VERSION=""`)
		Contains(t, noDeps, `the cloud has no pinned Docker bundle`)
	})

	t.Run("install script docker API check: ver_ge accepts Docker 20.10 (1.41) and 29 (min 1.44 <= cap 1.47), rejects 1.40 / too new", func(t *testing.T) {
		sh := routes.InstallScript(instSample())
		fn := sh[strings.Index(sh, "ver_ge() {"):strings.Index(sh, "sha_of() {")]
		check := func(a, b string) bool {
			return instSpawn(nil, nil, "sh", "-c", fn+"\n"+`ver_ge "$1" "$2"`, "sh", a, b).Status == 0
		}
		True(t, check("1.51", "1.41"), "Docker 28 max API 1.51 >= floor")
		True(t, check("1.41", "1.41"), "Docker 20.10 (API 1.41) is accepted")
		False(t, check("1.40", "1.41"), "Docker 19.03 (API 1.40) is too old")
		True(t, check("1.47", "1.44"), "Docker 29 min API 1.44 <= cap 1.47")
		False(t, check("1.47", "1.50"), "a daemon with min API 1.50 is above the cap")
	})

	t.Run("install script runs: non-root fails at preflight with the manual hint (exit 1, log written)", func(t *testing.T) {
		// runs the real script as the current (non-root) user with a private LOG path; it must stop at step 1
		sh := strings.Replace(routes.InstallScript(instSample()), "LOG=/var/log/opm-install.log", "LOG="+filepath.Join(t.TempDir(), "install.log"), 1)
		r := instSpawn(&sh, nil, "sh", "-s")
		if os.Getuid() == 0 {
			return // CI as root: covered by the container e2e
		}
		Eq(t, r.Status, 1, r.Out+r.ErrOut)
		Match(t, r.Out, `\[1/7\] Checking this server`)
		Match(t, r.Out, `FAILED at step 1/7`)
		Match(t, r.Out, `MANUAL: rerun with sudo`)
	})

	t.Run("GET /v1/install/deps/docker: pinned bundle (sha + version headers), token rules; getSetup.installDeps; script embeds the pins", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		bundle := []byte("fake docker bundle " + id)
		hex := Sha256Hex(bundle)
		path := "deps/docker/0.0.1-test/arm64/docker-0.0.1-test.tgz"
		Bucket.Save(t, path, bundle)
		manifest := A{
			"docker":       A{"version": "0.0.1-test", "files": A{"amd64": A{"path": strings.Replace(path, "arm64", "amd64", 1), "sha256": strings.Repeat("e", 64), "size": 1}, "arm64": A{"path": path, "sha256": hex, "size": len(bundle)}}},
			"minDockerApi": "1.41",
			"agent":        A{"version": "0.0.2-pinned"},
			"updatedAt":    Now(),
		}
		Bucket.Save(t, "deps/manifest.json", JOf(manifest).String())
		tok := admin.API.Call(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "app"}})
		bearer := func(tk string) Opt { return Opt{Headers: map[string]string{"Authorization": "Bearer " + tk}} }
		H := bearer(tok.At("token").Str())
		r := Raw(t, "GET", "/v1/install/deps/docker?arch=arm64", H)
		Eq(t, r.Status, 200)
		True(t, bytes.Equal(r.Body, bundle))
		Eq(t, r.Header("x-opm-sha256"), hex)
		Eq(t, r.Header("x-opm-version"), "0.0.1-test")
		Eq(t, Raw(t, "GET", "/v1/install/deps/docker?arch=s390x", H).Status, 422)
		Eq(t, Raw(t, "GET", "/v1/install/deps/podman?arch=arm64", H).Status, 404)
		Eq(t, Raw(t, "GET", "/v1/install/deps/docker?arch=arm64").Status, 401)
		Eq(t, Raw(t, "GET", "/v1/install/deps/docker?arch=arm64", bearer("opi_unknown")).Status, 401)
		// setup shows what the script installs
		setup := admin.API.Call(t, "getSetup", A{"deploymentId": id})
		Eq(t, setup.At("installDeps"), A{"dockerVersion": "0.0.1-test", "minDockerApi": "1.41", "agentVersion": "0.0.2-pinned"})
		// the script pins the manifest's versions + sha256 (agent version from the manifest, not agent/latest)
		sh := Raw(t, "GET", "/v1/install/script", H).Text()
		Contains(t, sh, `DOCKER_VERSION="0.0.1-test"; MIN_DOCKER_API="1.41"; MAX_DOCKER_API="1.47"`)
		Contains(t, sh, `DSHA="`+hex+`"`)
		Contains(t, sh, `AGENT_VERSION="0.0.2-pinned"`)
		// expired / revoked / used tokens → gone
		tRef := "deployments/" + id + "/installTokens/"
		UpdateDoc(t, tRef+tok.At("tokenHash").Str(), A{"expiresAt": ISO(time.Now().Add(-time.Second))})
		Eq(t, Raw(t, "GET", "/v1/install/deps/docker?arch=arm64", H).Status, 410)
		t2 := admin.API.Call(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "app"}})
		admin.API.Call(t, "revokeInstallToken", A{"deploymentId": id, "tokenHash": t2.At("tokenHash").Str()})
		Eq(t, Raw(t, "GET", "/v1/install/deps/docker?arch=arm64", bearer(t2.At("token").Str())).Status, 410)
		t3 := admin.API.Call(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "app"}})
		UpdateDoc(t, tRef+t3.At("tokenHash").Str(), A{"usedAt": Now()})
		Eq(t, Raw(t, "GET", "/v1/install/deps/docker?arch=arm64", bearer(t3.At("token").Str())).Status, 410)
		// no manifest → setup null, deps 404
		Bucket.Delete(t, "deps/manifest.json")
		Eq(t, admin.API.Call(t, "getSetup", A{"deploymentId": id}).At("installDeps"), nil)
		t4 := admin.API.Call(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "db"}})
		Eq(t, Raw(t, "GET", "/v1/install/deps/docker?arch=arm64", bearer(t4.At("token").Str())).Status, 404)
	})

	t.Run("install script final step: listen_ports (ss / netstat / /proc fallback) + gui_url (port 80 first, no :80 / :443)", func(t *testing.T) {
		sh := routes.InstallScript(instSample())
		fns := sh[strings.Index(sh, "have() {"):strings.Index(sh, "ver_ge() {")] + sh[strings.Index(sh, "listen_ports() {"):strings.Index(sh, `CAND=""`)]
		dir := t.TempDir()
		// fake ss: LISTEN on 80 (v4 + v6), 7080, 8080
		instWrite(t, filepath.Join(dir, "ss"), "#!/bin/sh\nprintf \"%s\\n\" \"LISTEN 0 4096 0.0.0.0:80 0.0.0.0:*\" \"LISTEN 0 4096 [::]:80 [::]:*\" \"LISTEN 0 4096 *:7080 *:*\" \"LISTEN 0 4096 *:8080 *:*\"\n", 0o755)
		body := `echo "$LISTEN"; is_listening 80 && echo L80; is_listening 443 || echo N443; gui_url 1.2.3.4 80; gui_url 1.2.3.4 443; gui_url 1.2.3.4 8080; gui_url fe80::1 7080`
		r := instSpawn(nil, []string{"PATH=" + dir + ":/usr/bin:/bin"}, "sh", "-c", "LOG=/dev/null\nts() { :; }\nlogf() { :; }\n"+fns+"\n"+body)
		Eq(t, r.Status, 0, r.ErrOut)
		lines := strings.Split(strings.TrimSpace(r.Out), "\n")
		Eq(t, strings.TrimSpace(lines[0]), "80 7080 8080")
		Eq(t, lines[1:], L{"L80", "N443", "http://1.2.3.4/_opm/", "https://1.2.3.4/_opm/", "http://1.2.3.4:8080/_opm/", "http://[fe80::1]:7080/_opm/"})
	})
}
