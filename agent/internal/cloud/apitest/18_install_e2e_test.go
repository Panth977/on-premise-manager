package apitest

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/routes"
)

// instDockerBundle is dockerBundle(): the real static bundle, cached under ~/.cache/opm-install-e2e.
func instDockerBundle(t testing.TB, arch string) []byte {
	t.Helper()
	home, err := os.UserHomeDir()
	NoErr(t, err)
	dir := filepath.Join(home, ".cache", "opm-install-e2e")
	NoErr(t, os.MkdirAll(dir, 0o777))
	f := filepath.Join(dir, "docker-"+cloud.DockerVersion+"-"+arch+".tgz")
	if _, err := os.Stat(f); err != nil {
		up := map[string]string{"arm64": "aarch64", "amd64": "x86_64"}[arch]
		r, err := http.Get("https://download.docker.com/linux/static/stable/" + up + "/docker-" + cloud.DockerVersion + ".tgz")
		NoErr(t, err)
		defer r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatalf("download docker bundle: %d", r.StatusCode)
		}
		b, err := io.ReadAll(r.Body)
		NoErr(t, err)
		NoErr(t, os.WriteFile(f, b, 0o666))
	}
	b, err := os.ReadFile(f)
	NoErr(t, err)
	return b
}

// instBuildAgent cross-builds the real linux agent for arch, its version set like agent/Makefile does.
func instBuildAgent(t testing.TB, arch, version string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "opm-agent")
	c := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w -X opm/agent/internal/agent.Version="+version, "-o", out, "./cmd/opm-agent")
	c.Dir = AgentDir
	c.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
	if b, err := c.CombinedOutput(); err != nil {
		t.Fatalf("build the linux agent: %v\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	NoErr(t, err)
	return b
}

// Port of backend/test/18-install-e2e.test.ts.
//
// The agent is built here with the version the script pins (the TS test used agent/dist, which reports its own version
// while the script calls it 0.0.0-e2e: the rerun became a DOWNGRADE to a release of unknown schema, which the installer
// rightly refuses for an agent of schema 2, §21 #91 (c), so the "already installed" line never came).
func TestInstallE2E(t *testing.T) {
	t.Run("installer e2e: ubuntu:24.04 container, DRY_RUN=1 → pinned Docker bundle + agent land in /usr/local/bin", func(t *testing.T) {
		const version = "0.0.0-e2e"
		a := instDockerArch(t)
		pub, seed := instGenKey(t)
		docker := instDockerBundle(t, a)
		dockerSha := Sha256Hex(docker)
		agent := instBuildAgent(t, a, version)
		agentSha := Sha256Hex(agent)
		token := "opi_" + strings.Repeat("E", 43)
		st := instServe(t, token, func(st *instStub, w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			switch {
			case r.URL.Path == "/v1/install/script":
				w.Header().Set("Content-Type", "text/x-shellscript")
				_, _ = io.WriteString(w, routes.InstallScript(routes.InstallScriptOpts{
					ApiBase: st.URL, DeploymentID: "DepE2E00000000000001", SiteSlug: "app", Token: token, ReleasePublicKey: pub,
					Agent:     &routes.PinnedAgentInfo{Version: version, Files: map[string]routes.ArchFile{a: {Sha256: agentSha, Sig: cloud.SignHex(agentSha, seed)}}},
					Docker:    &routes.PinnedDocker{Version: cloud.DockerVersion, MinAPI: cloud.MinDockerAPI, Sha256: map[string]string{a: dockerSha}},
					PortAgent: doc.Undefined, PortAlias: doc.Undefined, PortEdge: doc.Undefined,
				}))
			case r.URL.Path == "/v1/install/deps/docker" && q.Get("arch") == a:
				w.Header().Set("X-OPM-Sha256", dockerSha)
				w.Header().Set("X-OPM-Version", cloud.DockerVersion)
				_, _ = w.Write(docker)
			case r.URL.Path == "/v1/install/binary" && q.Get("arch") == a && q.Get("version") == version:
				w.Header().Set("X-OPM-Sha256", agentSha)
				_, _ = w.Write(agent)
			default:
				w.WriteHeader(404)
			}
		})
		get := fmt.Sprintf(`curl -fsSL -H "Authorization: Bearer %s" %s/v1/install/script`, token, st.URL)
		inner := strings.Join([]string{
			`set -e`,
			`apt-get update -qq >/dev/null && apt-get install -y -qq curl ca-certificates >/dev/null`,
			// an existing Docker is judged by its API range: fake docker CLIs reporting API 1.40 (refused) and 1.41 (accepted)
			`mk() { printf '#!/bin/sh\ncase "$1" in version) echo "%s 1.12 %s";; info) exit 0;; --version) echo "Docker version %s, build fake";; esac\n' "$1" "$2" "$2" > /usr/local/bin/docker; chmod +x /usr/local/bin/docker; }`,
			`mk 1.40 19.03.15`,
			`echo "== FAKE140"; ` + get + ` | DRY_RUN=1 sh || echo "exit=$?"`,
			`mk 1.41 20.10.24`,
			`echo "== FAKE141"; ` + get + ` | DRY_RUN=1 sh; echo "== END141"`,
			`rm -rf /usr/local/bin/docker /usr/local/bin/opm-agent /etc/opm /var/log/opm-install.log`,
			`echo "== REAL"`,
			// §21 #91: Docker is installed only when the operator agrees (no terminal here → the flag)
			get + ` | OPM_INSTALL_DOCKER=1 DRY_RUN=1 sh`,
			`echo "== BIN"; ls /usr/local/bin`,
			`echo "== CFG"; cat /etc/opm/agent.json; test -s /etc/opm/install-token && echo token-ok`,
			`echo "== VER"; /usr/local/bin/dockerd --version; /usr/local/bin/opm-agent version`,
			`echo "== LOG"; grep -c . /var/log/opm-install.log`,
			// idempotent rerun: everything detected, nothing downloaded again for the agent (§21 #91: the rerun asks first).
			// The binary reports the pinned version, so this is a SAME reinstall. The output goes to a file so that a failing
			// rerun fails here (behind `| grep | head` its exit status and its FAILED line were both lost).
			get + ` | OPM_YES=1 DRY_RUN=1 sh > /tmp/rerun.log 2>&1 || { echo "rerun failed: rc=$?"; cat /tmp/rerun.log; exit 1; }`,
			`grep -E "available|already installed|OK|Docker found|Installed" /tmp/rerun.log | head -50`,
		}, "\n")
		status, out := instContainer(t, 800*time.Second, inner)
		Eq(t, status, 0, instTail(out, 4000))
		fake140 := instSlice(t, out, "== FAKE140", "== FAKE141")
		Match(t, fake140, `FAILED at step 2/7 \(Docker Engine\): Docker Engine 19\.03\.15 \(API 1\.40\) is too old`, fake140)
		Match(t, fake140, `exit=1`)
		fake141 := instSlice(t, out, "== FAKE141", "== END141")
		Match(t, fake141, `OK +Docker Engine 20\.10\.24 \(API 1\.41, min 1\.12; agent speaks 1\.41–1\.47\)`, fake141)
		Match(t, fake141, `\[7/7\] GUI addresses and firewall`)
		Match(t, fake141, `DRY_RUN: the agent was not started, no GUI port listens yet`)
		Match(t, fake141, `Upgrade later: rerun this same command`)
		NoMatch(t, fake141, `FAILED`, fake141)
		out = out[strings.Index(out, "== REAL"):]
		for _, f := range []string{"containerd", "containerd-shim-runc-v2", "ctr", "docker", "docker-init", "docker-proxy", "dockerd", "runc", "opm-agent"} {
			Match(t, out, `(?m)^`+regexp.QuoteMeta(f)+`$`, "missing /usr/local/bin/"+f+"\n"+instTail(out, 3000))
		}
		Match(t, out, `\[1/7\] Checking this server`)
		Match(t, out, `OK +running as root`)
		qv := regexp.QuoteMeta(cloud.DockerVersion)
		Match(t, out, `downloaded docker-`+qv+`\.tgz \(sha256 `+dockerSha+`\)`)
		Match(t, out, `release signature verified`)
		Match(t, out, `"deploymentId":"DepE2E00000000000001"`)
		Match(t, out, `token-ok`)
		Match(t, out, `Docker version `+qv)
		Match(t, out, `opm-agent 0\.0\.0-e2e already installed \(sha256 matches\)`)
		Match(t, out, `Installed 0\.0\.0-e2e → available 0\.0\.0-e2e: SAME \(reinstall\)`)
		Match(t, out, `Docker found: Docker version`)
		// fake-1.41 run + the real run download the agent once each; the rerun does not
		Eq(t, st.BinaryHits(), 2, "the rerun must not download the agent again")
	})
}
