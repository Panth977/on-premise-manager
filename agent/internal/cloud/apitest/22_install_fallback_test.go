package apitest

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/routes"
	"opm/agent/internal/cloud/shared"
)

// instSample22 is SAMPLE of 22-install-fallback.test.ts.
func instSample22() routes.InstallScriptOpts {
	return routes.InstallScriptOpts{
		ApiBase:          "https://api.opm.example",
		DeploymentID:     "Dep0000000000000000a",
		SiteSlug:         "app",
		Token:            "opi_" + strings.Repeat("A", 43),
		ReleasePublicKey: "Af0P9kVPsXG0cMwfCGgLSqZjaHwLViQDd6cKv1npyi0=",
		Agent:            &routes.PinnedAgentInfo{Version: "0.10.0-1", Files: map[string]routes.ArchFile{"amd64": {Sha256: strings.Repeat("a", 64), Sig: "sigA"}}, Schema: float64(3), MinSchema: float64(1)},
		Docker:           &routes.PinnedDocker{Version: "28.5.2", MinAPI: "1.41", Sha256: map[string]string{"amd64": strings.Repeat("c", 64)}},
		PortAgent:        doc.Undefined, PortAlias: doc.Undefined, PortEdge: doc.Undefined,
	}
}

// instTouchesContainers: docker subcommands that change a container, an image or a compose project. Read-only ones (ps,
// inspect, version, info) are not in it.
var instTouchesContainers = regexp.MustCompile(`docker (stop|start|rm|kill|restart|rmi|compose|pause|unpause|update|exec|run|create|container|image|system|volume|network)\b`)

// instCheckContainerRule states what the script may do to containers (SPEC-final §21 #91 d + §21 #120 d): nothing, except
// when the operator asks to move the agent home on a reinstall / upgrade to >= 0.19.1. Then, and only then, it stops the
// running containers of THIS deployment + site for the move and, when the move fails, starts them again.
func instCheckContainerRule(t testing.TB, sh string) {
	t.Helper()
	const open = `if [ -n "$NEW_HOME" ]; then`
	const end = "# ───────────────────────────── 7. reachability"
	i, j := strings.Index(sh, open), strings.Index(sh, end)
	True(t, i >= 0 && j > i, "the agent home move block")
	outside := sh[:i] + sh[j:]
	if m := instTouchesContainers.FindString(outside); m != "" {
		t.Fatalf("the script touches containers outside the agent home move: %q", m)
	}
	// the block is reachable only by an explicit answer, and never in a dry run
	Contains(t, sh, `if [ "$ACTION" != fresh ] && [ "$DRY" != 1 ] && [ "$(ver_cmp "$TARGET" 0.19.1)" -ge 0 ]; then`)
	Contains(t, sh, `NEW_HOME="${OPM_MOVE_HOME:-}"`)
	Contains(t, sh, `if [ -z "$NEW_HOME" ] && [ "$TTY" = 1 ]; then`)
	Contains(t, sh, `if ask "Move the agent home to another folder / disk (its containers stop for the move)?" "" "OPM_MOVE_HOME=/path"; then`)
	// inside: exactly one stop and one start, each on the containers selected by both labels
	block := sh[i:j]
	var got []string
	for _, l := range strings.Split(block, "\n") {
		if instTouchesContainers.MatchString(l) {
			got = append(got, strings.TrimSpace(l))
		}
	}
	Eq(t, got, L{
		`docker stop -t 30 "$cid" >/dev/null 2>&1 && STOPPED="$STOPPED${STOPPED:+,}\"$slug\""`,
		`for cid in $(docker ps -aq --filter "label=opm.deployment=$DEP" --filter "label=opm.site=$SITE" --filter status=exited 2>/dev/null); do docker start "$cid" >/dev/null 2>&1 || true; done`,
	})
	Contains(t, block, `for cid in $(docker ps -q --filter "label=opm.deployment=$DEP" --filter "label=opm.site=$SITE" 2>/dev/null); do`)
	True(t, strings.Index(block, `info "the move failed`) < strings.Index(block, `docker start "$cid"`), "containers are started again only after a failed move")
}

// Port of backend/test/22-install-fallback.test.ts.
func TestInstallFallback(t *testing.T) {
	vectors := []string{
		"0.7.2-10080232", "0.7.2", "0.10.0", "0.10.0-10090000", "0.10.0-9", "0.10.0-10", "0.9.0", "0.10.1", "1.0.0", "v0.10.0", " 0.10.0 ",
		"0.10.0-beta", "0.10.0-alpha", "0.10.0-beta2", "0.10.0-10", "0.10.0+10", "0.10.0-007", "0.10.0-7", "10.0.0", "2.0.0",
		"0.10.0-9007199254740991", "0.10.0-900719925474099", "garbage", "", "0.10", "0.10.0-", "00.010.000", "0.0.0",
	}
	shFns := func() string {
		sh := routes.InstallScript(instSample22())
		return sh[strings.Index(sh, "have() {"):strings.Index(sh, "# ── questions")]
	}
	sign := func(n int) int {
		switch {
		case n < 0:
			return -1
		case n > 0:
			return 1
		}
		return 0
	}

	t.Run("sh ver_cmp == compareAgentVersions; schema_ok == agentSchemaCompatible; install actions", func(t *testing.T) {
		fns := shFns()
		type pair struct{ a, b string }
		var pairs []pair
		var input strings.Builder
		for _, a := range vectors {
			for _, b := range vectors {
				pairs = append(pairs, pair{a, b})
				input.WriteString(a + "|" + b + "\n")
			}
		}
		// one sh run for every pair (tab-separated lines)
		// (builds beyond 2^53 differ: Go compares doubles, sh compares the digits exactly — not used by MMDDHHMM stamps)
		in := input.String()
		prog := "LOG=/dev/null\nts() { :; }\nlogf() { :; }\n" + fns + "\nwhile IFS='|' read -r a b; do ver_cmp \"$a\" \"$b\"; done"
		r := instSpawn(&in, nil, "sh", "-c", prog)
		Eq(t, r.Status, 0, r.ErrOut)
		got := strings.Split(strings.TrimSpace(r.Out), "\n")
		Eq(t, len(got), len(pairs))
		bad := L{}
		for i, p := range pairs {
			shv, err := strconv.Atoi(got[i])
			NoErr(t, err)
			if g := sign(shared.CompareAgentVersions(p.a, p.b)); shv != g {
				bad = append(bad, A{"a": p.a, "b": p.b, "sh": shv, "go": g})
			}
		}
		Eq(t, bad, L{}, "sh and Go disagree")
		// dash too
		if d := instSpawn(&in, nil, "dash", "-c", prog); d.Err == nil {
			Eq(t, strings.Split(strings.TrimSpace(d.Out), "\n"), got)
		}
		// schema rule
		var cases [][3]any
		for _, db := range []any{nil, 0.0, 1.0, 2.0, 3.0, 5.0} {
			for _, mx := range []any{nil, 1.0, 2.0, 3.0} {
				for _, mn := range []any{nil, 0.0, 1.0, 2.0} {
					cases = append(cases, [3]any{db, mx, mn})
				}
			}
		}
		s := func(n any) string {
			if n == nil {
				return ""
			}
			return fmt.Sprint(n)
		}
		var ci strings.Builder
		for _, c := range cases {
			ci.WriteString(s(c[0]) + "," + s(c[1]) + "," + s(c[2]) + "\n")
		}
		csv := ci.String()
		sr := instSpawn(&csv, nil, "sh", "-c", fns+"\nwhile IFS=, read -r d x n; do if schema_ok \"$d\" \"$x\" \"$n\"; then echo 1; else echo 0; fi; done")
		sg := strings.Split(strings.TrimSpace(sr.Out), "\n")
		Eq(t, len(sg), len(cases))
		for i, c := range cases {
			Eq(t, sg[i] == "1", shared.AgentSchemaCompatible(c[0], doc.Obj("schema", c[1], "minSchema", c[2])), fmt.Sprintf("schema %v vs %v/%v", c[0], c[1], c[2]))
		}
		// the script's ACTION mapping (installed vs target) = agentInstallAction
		for _, p := range []pair{{"0.7.2-10080232", "0.10.0-1"}, {"0.10.0-2", "0.10.0-1"}, {"0.10.0-1", "0.10.0-1"}} {
			c, err := strconv.Atoi(strings.TrimSpace(instSpawn(nil, nil, "sh", "-c", fns+"\n"+`ver_cmp "$1" "$2"`, "sh", p.a, p.b).Out))
			NoErr(t, err)
			w := "same"
			if c < 0 {
				w = "upgrade"
			} else if c > 0 {
				w = "downgrade"
			}
			Eq(t, w, shared.AgentInstallAction(p.a, p.b))
		}
		Eq(t, shared.AgentInstallAction("", "0.10.0"), "fresh")
	})

	t.Run("script text: prompts via /dev/tty, env flags, rerun hint, backups, units, health + rollback; lint clean", func(t *testing.T) {
		o := instSample22()
		o.UpgradeOnly, o.RequestedVersion = true, "0.10.0-1"
		sh := routes.InstallScript(o)
		file := filepath.Join(t.TempDir(), "install.sh")
		instWrite(t, file, sh, 0o644)
		instShN(t, file)
		instDashN(t, file)
		instShellcheck(t, file, "")
		for _, s := range []string{
			`(: </dev/tty) 2>/dev/null`,
			`read -r ANS </dev/tty`,
			`ANS_YES="${OPM_YES:-}"; ANS_DOCKER="${OPM_INSTALL_DOCKER:-}"; ANS_DOWN="${OPM_ALLOW_DOWNGRADE:-}"; ANS_RESTORE="${OPM_RESTORE_DB_BACKUP:-}"`,
			`[ -z "$ANS_DOCKER" ] && [ "$SKIP_DOCKER" = 1 ] && ANS_DOCKER=0`,
			`WANT_VERSION="${OPM_AGENT_VERSION:-}"; SOCKET="${OPM_SOCKET_ACTIVATION:-1}"`,
			`SCRIPT_QS="?version=0.10.0-1"`,
			`ASCHEMA="3"; AMINSCHEMA="1"`,
			`Docker Engine is not installed. Install the pinned Docker $DOCKER_VERSION now?`,
			`"OPM_INSTALL_DOCKER=1"`,
			`Installed $OLD_VER → available $TARGET: $WORD`,
			`"OPM_ALLOW_DOWNGRADE=1 OPM_YES=1"`,
			`/v1/install/release?os=linux&arch=$ARCH&version=$WANT_VERSION`,
			`openssl pkeyutl -verify -pubin`,
			`printf 'version=%s\nschema=%s\ncreatedAt=%s\nreason=install-script %s to %s\n'`,
			`tail -n +6`,
			`cp -p "$BIN" "$BIN.prev"`,
			`"$BIN" systemd-units --install "$SOCK_FLAG"`,
			`/_opm/api/health`,
			`rollback "no healthy answer on 127.0.0.1:$AGENT_PORT with version $TARGET"`,
			`Hard fallback for any up- / downgrade: rerun this same command`,
		} {
			Contains(t, sh, s, "missing: "+s)
		}
		// the token is never written to the log by the rerun hint
		Contains(t, sh, `logf "rerun with: curl ... | sudo $* sh"`)
		// changed from the TS test: `!/docker (stop|rm|kill|restart|rmi|compose)/` predates §21 #120 (d), the explicit agent
		// home move, which stops the site's containers on purpose. The rule is now stated exactly.
		instCheckContainerRule(t, sh)
	})

	uploadVersion := func(t *testing.T, v string, meta A) {
		bin := []byte("bin " + v)
		instUploadAgent(t, v, bin)
		if meta != nil {
			Bucket.Save(t, "agent/"+v+"/release.json", JOf(instWith(A{"version": v, "commit": "abc", "builtAt": nil}, meta)).String())
		}
	}

	t.Run("tokens: unusable → 200 tiny script (exit 1); installRelease / ?version= / schema headers", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		uploadVersion(t, "0.10.0-fb", A{"schema": 4, "minSchema": 2})
		uploadVersion(t, "0.9.0-fb", nil)
		Bucket.Save(t, "deps/manifest.json", JOf(A{"docker": A{"version": "0.0.1-f", "files": A{"amd64": A{"path": "x", "sha256": strings.Repeat("e", 64), "size": 1}, "arm64": A{"path": "y", "sha256": strings.Repeat("f", 64), "size": 1}}}, "minDockerApi": "1.41", "agent": A{"version": "0.10.0-fb", "notes": "n", "schema": 4, "minSchema": 2}, "updatedAt": Now()}).String())
		app := EnrollSite(t, admin, id, "app")
		H := func(tk string) Opt { return Opt{Headers: map[string]string{"Authorization": "Bearer " + tk}} }
		runBody := func(body string) instRun { return instSpawn(&body, nil, "sh", "-s") }

		// the enrolled site's used token: the real (upgrade-only) script, pin schema embedded
		s := Raw(t, "GET", "/v1/install/script", H(app.Token))
		Eq(t, s.Status, 200)
		Contains(t, s.Text(), `UPGRADE_ONLY="1"`)
		Contains(t, s.Text(), `ASCHEMA="4"; AMINSCHEMA="2"`)
		// ?version= embeds that version (schema unknown → empty = legacy 1)
		s = Raw(t, "GET", "/v1/install/script?version=0.9.0-fb", H(app.Token))
		Contains(t, s.Text(), `AGENT_VERSION="0.9.0-fb"; ASCHEMA=""; AMINSCHEMA=""; SCRIPT_QS="?version=0.9.0-fb"`)
		s = Raw(t, "GET", "/v1/install/script?version=7.7.7", H(app.Token))
		Eq(t, s.Status, 200)
		r := runBody(s.Text())
		Eq(t, r.Status, 1)
		Match(t, r.ErrOut, `agent 7\.7\.7 is not uploaded`)
		// installRelease
		rel := Raw(t, "GET", "/v1/install/release?os=linux&arch=amd64", H(app.Token))
		Eq(t, rel.Status, 200)
		Eq(t, rel.J.At("version"), "0.10.0-fb")
		Eq(t, rel.J.At("schema"), 4)
		Eq(t, rel.J.At("minSchema"), 2)
		Eq(t, rel.J.At("notes"), "n")
		Eq(t, rel.J.At("binaryPath"), "/v1/install/binary?os=linux&arch=amd64&version=0.10.0-fb")
		rel9 := Raw(t, "GET", "/v1/install/release?os=linux&arch=amd64&version=0.9.0-fb", H(app.Token))
		Eq(t, rel9.J.At("schema"), nil)
		Eq(t, rel9.J.At("notes"), nil)
		Eq(t, rel9.J.At("sha256"), Sha256Hex("bin 0.9.0-fb"))
		Eq(t, Raw(t, "GET", "/v1/install/release?os=linux&arch=amd64&version=7.7.7", H(app.Token)).Status, 404)
		Eq(t, Raw(t, "GET", "/v1/install/release?os=linux&arch=s390x", H(app.Token)).Status, 422)
		// installBinary schema headers + agentRelease schema
		b := Raw(t, "GET", "/v1/install/binary?os=linux&arch=amd64&version=0.10.0-fb", H(app.Token))
		Eq(t, b.Header("x-opm-agent-schema"), "4")
		Eq(t, b.Header("x-opm-agent-min-schema"), "2")
		ar := app.API.Call(t, "agentRelease", A{"query": A{"os": "linux", "arch": "amd64"}})
		Eq(t, ar.At("schema"), 4)
		Eq(t, ar.At("minSchema"), 2)

		// expired / revoked / used-elsewhere / unknown → 200 tiny script, exit 1, nothing changed; binary etc. keep 410
		toks := "deployments/" + id + "/installTokens/"
		exp := admin.API.Call(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "db"}})
		expTok := exp.At("token").Str()
		UpdateDoc(t, toks+exp.At("tokenHash").Str(), A{"expiresAt": instPast()})
		s = Raw(t, "GET", "/v1/install/script", H(expTok))
		Eq(t, s.Status, 200)
		r = runBody(s.Text())
		Eq(t, r.Status, 1)
		Match(t, r.ErrOut, `token expired \(site db\) — open the cloud Setup page → Install command → Regenerate \(or View\) and run the new command`)
		Eq(t, Raw(t, "GET", "/v1/install/binary?os=linux&arch=amd64", H(expTok)).Status, 410)
		Eq(t, Raw(t, "GET", "/v1/install/release?os=linux&arch=amd64", H(expTok)).Status, 410)
		Eq(t, Raw(t, "GET", "/v1/install/deps/docker?arch=amd64", H(expTok)).Status, 410)
		rev := admin.API.Call(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "db"}})
		admin.API.Call(t, "revokeInstallToken", A{"deploymentId": id, "tokenHash": rev.At("tokenHash").Str()})
		r = runBody(Raw(t, "GET", "/v1/install/script", H(rev.At("token").Str())).Text())
		Eq(t, r.Status, 1)
		Match(t, r.ErrOut, `token revoked \(site db\)`)
		// the app token after a revoke + re-enroll = used for an earlier enrollment
		admin.API.Call(t, "revokeAgent", A{"deploymentId": id, "siteSlug": "app", "body": A{"confirm": "app"}})
		EnrollSite(t, admin, id, "app")
		r = runBody(Raw(t, "GET", "/v1/install/script", H(app.Token)).Text())
		Eq(t, r.Status, 1)
		Match(t, r.ErrOut, `was used for an earlier enrollment \(site app\)`)
		r = runBody(Raw(t, "GET", "/v1/install/script", H("opi_"+strings.Repeat("Z", 43))).Text())
		Match(t, r.ErrOut, `unknown install token`)
		Eq(t, Raw(t, "GET", "/v1/install/script").Status, 401, "no token at all stays 401")
		Bucket.Delete(t, "deps/manifest.json")
	})

	t.Run("upload-agent.ts stores <dir>/release.json as agent/<v>/release.json; upload-deps copies schema into the manifest", func(t *testing.T) {
		dir := t.TempDir()
		instWrite(t, filepath.Join(dir, "opm-agent-linux-amd64"), "linux-amd64-rel", 0o644)
		instWrite(t, filepath.Join(dir, "release.json"), `{"version":"0.10.0-rel","schema":5,"minSchema":3,"commit":"c0ffee","builtAt":"2026-10-08T00:00:00Z"}`, 0o644)
		out := OpmCloud(t, "upload-agent", "--dir", dir, "--version", "0.10.0-rel")
		Match(t, out, `release\.json \(schema 5, minSchema 3\)`)
		m := ParseJ(Bucket.Download(t, "agent/0.10.0-rel/release.json"))
		Eq(t, m, A{"version": "0.10.0-rel", "schema": 5, "minSchema": 3, "commit": "c0ffee", "builtAt": "2026-10-08T00:00:00Z"})
		Match(t, instOpsFails(t, "upload-agent", "--dir", dir, "--version", "0.10.0-other"), `version 0\.10\.0-rel != --version 0\.10\.0-other`)
		for _, a := range []string{"amd64", "arm64"} {
			instWrite(t, filepath.Join(dir, "docker-0.0.8-"+a+".tgz"), "d", 0o644)
		}
		OpmCloud(t, "upload-deps", "--from-dir", dir, "--docker-version", "0.0.8", "--agent-version", "0.10.0-rel")
		man := ParseJ(Bucket.Download(t, "deps/manifest.json"))
		Eq(t, man.At("agent"), A{"version": "0.10.0-rel", "notes": nil, "schema": 5, "minSchema": 3})
		Bucket.Delete(t, "deps/manifest.json")
	})
}
