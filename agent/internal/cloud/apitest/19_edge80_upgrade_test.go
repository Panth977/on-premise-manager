package apitest

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// instUploadAgent is uploadAgent() / uploadPin(): an agent build + its .sha256 + .sig in the bucket.
func instUploadAgent(t testing.TB, version string, bin []byte) {
	t.Helper()
	base := "agent/" + version + "/linux-amd64/opm-agent"
	Bucket.Save(t, base, bin)
	Bucket.Save(t, base+".sha256", Sha256Hex(bin)+"\n")
	Bucket.Save(t, base+".sig", "sig-"+version+"\n")
}

// instManifest writes deps/manifest.json.
func instManifest(t testing.TB, dockerVersion, sub string, agent A) {
	t.Helper()
	Bucket.Save(t, "deps/manifest.json", JOf(A{
		"docker": A{"version": dockerVersion, "files": A{
			"amd64": A{"path": "deps/docker/" + sub + "/amd64/docker.tgz", "sha256": strings.Repeat("e", 64), "size": 1},
			"arm64": A{"path": "deps/docker/" + sub + "/arm64/docker.tgz", "sha256": strings.Repeat("f", 64), "size": 1},
		}},
		"minDockerApi": "1.41",
		"agent":        agent,
		"updatedAt":    Now(),
	}).String())
}

// instWith is {...a, ...b}.
func instWith(a, b A) A {
	o := A{}
	for k, v := range a {
		o[k] = v
	}
	for k, v := range b {
		o[k] = v
	}
	return o
}

func instPast() string { return ISO(time.Now().Add(-time.Second)) }

// Port of backend/test/19-edge80-upgrade.test.ts.
func TestEdge80Upgrade(t *testing.T) {
	const VER = "0.0.9-roundh"
	BIN := []byte("fake opm-agent binary " + VER)

	t.Run("round H: used-token upgrade path, agent release/binary, heartbeat guiPorts → setup guiUrls / defaultEdge / portAlerts", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		instUploadAgent(t, VER, BIN)
		instManifest(t, "0.0.1-h", "h", A{"version": VER})
		app := EnrollSite(t, admin, id, "app")
		bearer := func(tk string, extra ...string) Opt {
			h := map[string]string{"Authorization": "Bearer " + tk}
			for i := 0; i+1 < len(extra); i += 2 {
				h[extra[i]] = extra[i+1]
			}
			return Opt{Headers: h}
		}
		H := bearer(app.Token)
		toks := "deployments/" + id + "/installTokens"

		t.Run("used token of the current enrollment → upgrade-only script, binary allowed; enroll still gone", func(t *testing.T) {
			var tokDoc J
			for _, d := range ListDocs(t, toks) {
				if d.Data.At("siteSlug").Str() == "app" {
					tokDoc = d.Data
					break
				}
			}
			agentDoc := Doc(t, "deployments/"+id+"/agents/app")
			Eq(t, agentDoc.At("enrolledAt"), tokDoc.At("usedAt"), "enroll writes the same timestamp to token.usedAt and agent.enrolledAt")
			// even after the 24 h expiry: the upgrade rule has no expiry
			UpdateDoc(t, toks+"/"+tokDoc.At("tokenHash").Str(), A{"expiresAt": instPast()})
			s := Raw(t, "GET", "/v1/install/script", bearer(app.Token, "X-Forwarded-For", "139.59.2.56"))
			Eq(t, s.Status, 200, s.Text())
			sh := s.Text()
			Contains(t, sh, `UPGRADE_ONLY="1"; SEEN_IP="139.59.2.56"`)
			Contains(t, sh, `AGENT_VERSION="`+VER+`"`)
			Contains(t, sh, "it can only upgrade that server")
			b := Raw(t, "GET", "/v1/install/binary?os=linux&arch=amd64&version="+VER, H)
			Eq(t, b.Status, 200)
			True(t, bytes.Equal(b.Body, BIN))
			Bearer(app.Token).Rejects(t, "enroll", A{"body": A{"publicKey": NewSiteKey().PublicKey, "agentVersion": "1", "hostname": "h", "ip": "1.2.3.4", "advertiseHost": "1.2.3.4", "agentPort": 7080, "os": "linux", "arch": "amd64"}}, "gone", 410)
			// a fresh (unused) token → the normal script
			fresh := admin.API.Call(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "db"}})
			s2 := Raw(t, "GET", "/v1/install/script", bearer(fresh.At("token").Str()))
			Eq(t, s2.Status, 200)
			Contains(t, s2.Text(), `UPGRADE_ONLY="0"`)
			// expired unused token → gone
			UpdateDoc(t, toks+"/"+fresh.At("tokenHash").Str(), A{"expiresAt": instPast()})
			TinyScript(t, Raw(t, "GET", "/v1/install/script", bearer(fresh.At("token").Str())), `token expired`)
		})

		t.Run("GET /v1/agent/release + /v1/agent/binary (signed)", func(t *testing.T) {
			r := app.API.Call(t, "agentRelease", A{"query": A{"os": "linux", "arch": "amd64"}})
			Eq(t, r.At("version"), VER)
			Eq(t, r.At("sha256"), Sha256Hex(BIN))
			Eq(t, r.At("signature"), "sig-"+VER)
			Eq(t, r.At("os"), "linux")
			Eq(t, r.At("arch"), "amd64")
			_, isStr := r.At("releasePublicKey").V.(string)
			True(t, isStr)
			Eq(t, r.At("binaryPath"), "/v1/agent/binary?os=linux&arch=amd64&version="+VER)
			app.API.Rejects(t, "agentRelease", A{"query": A{"os": "linux", "arch": "arm64"}}, "not-found", 404)
			app.API.Rejects(t, "agentRelease", A{"query": A{"os": "windows", "arch": "amd64"}}, "invalid", 422)
			sg := func(p string) Opt { return Opt{Headers: SignHeaders(app.Key, id, "app", "GET", p, nil)} }
			bp := r.At("binaryPath").Str()
			b := Raw(t, "GET", bp, sg(bp))
			Eq(t, b.Status, 200)
			True(t, bytes.Equal(b.Body, BIN))
			Eq(t, b.Header("x-opm-sha256"), Sha256Hex(BIN))
			Eq(t, b.Header("x-opm-agent-version"), VER)
			bad := "/v1/agent/binary?os=linux&arch=amd64&version=..%2Fx"
			Eq(t, Raw(t, "GET", bad, sg(bad)).Status, 422)
			missing := "/v1/agent/binary?os=linux&arch=amd64&version=9.9.9"
			Eq(t, Raw(t, "GET", missing, sg(missing)).Status, 404)
			Eq(t, Raw(t, "GET", "/v1/agent/release?os=linux&arch=amd64").Status, 401)
		})

		t.Run("heartbeat guiPorts + edge.defaultEdge / bindErrors owner → stored, setup guiUrls (port 80 first), defaultEdge, portAlerts; bad shapes 422", func(t *testing.T) {
			hb := A{"siteSlug": "app", "agentVersion": VER, "headRevId": nil, "staging": nil, "peersSeen": L{}, "metrics": A{"cpu": 0, "memPct": 0, "diskPct": 0}, "edge": nil, "keyVersion": 1, "advertiseHost": "127.0.0.1", "agentPort": 7080, "ip": "139.59.2.56", "hostname": "droplet", "detectedAddresses": L{"139.59.2.56", "10.47.0.5", "fe80::1", "droplet"}}
			setupSite := func(slug string) J {
				return admin.API.Call(t, "getSetup", A{"deploymentId": id}).At("sites").Find("siteSlug", slug)
			}
			send := func(b A) { app.API.Call(t, "heartbeat", A{"body": b}) }
			refused := func(b A) { app.API.Rejects(t, "heartbeat", A{"body": b}, "invalid", 422) }
			// old agent: no fields → guiUrls [], defaultEdge null, no alerts
			send(hb)
			site := setupSite("app")
			Eq(t, site.At("guiUrls"), L{})
			Eq(t, site.At("defaultEdge"), nil)
			Eq(t, site.At("portAlerts"), L{})
			guiPorts := L{
				A{"port": 7080, "scheme": "http", "kind": "agent-port"},
				A{"port": 8080, "scheme": "http", "kind": "gui-alias"},
				A{"port": 80, "scheme": "http", "kind": "edge"},
			}
			edge := A{"revSeq": nil, "reloadedAt": nil, "lastReloadError": nil, "boundPorts": L{80, 7080, 8080}, "bindErrors": L{}, "certs": L{}, "defaultEdge": true}
			send(instWith(hb, A{"edge": edge, "guiPorts": guiPorts}))
			site = setupSite("app")
			Eq(t, site.At("defaultEdge"), true)
			Eq(t, site.At("guiUrls"), L{
				A{"url": "http://139.59.2.56/_opm/", "kind": "edge"},
				A{"url": "http://10.47.0.5/_opm/", "kind": "edge"},
				A{"url": "http://139.59.2.56:8080/_opm/", "kind": "gui-alias"},
				A{"url": "http://10.47.0.5:8080/_opm/", "kind": "gui-alias"},
				A{"url": "http://139.59.2.56:7080/_opm/", "kind": "agent-port"},
				A{"url": "http://10.47.0.5:7080/_opm/", "kind": "agent-port"},
			})
			Eq(t, Doc(t, "deployments/"+id+"/agents/app").At("guiPorts"), guiPorts)
			// :80 held by another program → portAlerts
			bindErrors := L{
				A{"port": 80, "purpose": "edge-http", "error": "listen tcp :80: bind: address already in use", "owner": "nginx"},
				A{"port": 443, "purpose": "edge-https", "error": "in use", "owner": nil},
				A{"port": 8080, "purpose": "gui-alias", "error": "in use"},
			}
			send(instWith(hb, A{"edge": instWith(edge, A{"bindErrors": bindErrors, "boundPorts": L{7080}}), "guiPorts": guiPorts[:1]}))
			setup := admin.API.Call(t, "getSetup", A{"deploymentId": id})
			site = setup.At("sites").Find("siteSlug", "app")
			Eq(t, site.At("portAlerts"), L{
				A{"port": 80, "purpose": "edge-http", "owner": "nginx", "message": "port 80 is used by nginx — stop it or change edge.httpPort"},
				A{"port": 443, "purpose": "edge-https", "owner": nil, "message": "port 443 is used by another program — stop it or change edge.httpPort"},
			})
			Eq(t, site.At("guiUrls").Pluck("url"), L{"http://139.59.2.56:7080/_opm/", "http://10.47.0.5:7080/_opm/"})
			// not enrolled site → []
			db := setup.At("sites").Find("siteSlug", "db")
			Eq(t, db.At("guiUrls"), L{})
			Eq(t, db.At("defaultEdge"), nil)
			// bad shapes
			refused(instWith(hb, A{"edge": instWith(edge, A{"defaultEdge": "yes"})}))
			refused(instWith(hb, A{"edge": instWith(edge, A{"bindErrors": L{A{"port": 80, "purpose": "edge-http", "error": "x", "owner": strings.Repeat("n", 256)}}})}))
			refused(instWith(hb, A{"guiPorts": L{A{"port": 0, "scheme": "http", "kind": "edge"}}}))
			refused(instWith(hb, A{"guiPorts": L{A{"port": 80, "scheme": "ftp", "kind": "edge"}}}))
			refused(instWith(hb, A{"guiPorts": L{A{"port": 80, "scheme": "http", "kind": "takeover"}}}))
			many := L{}
			for i := 0; i < 17; i++ {
				many = append(many, guiPorts[0])
			}
			refused(instWith(hb, A{"guiPorts": many}))
		})

		t.Run("revokeAgent ends the upgrade rule; a re-enroll with a newer token does not revive the old one", func(t *testing.T) {
			admin.API.Call(t, "revokeAgent", A{"deploymentId": id, "siteSlug": "app", "body": A{"confirm": "app"}})
			TinyScript(t, Raw(t, "GET", "/v1/install/script", H), `used for an earlier enrollment`)
			Eq(t, Raw(t, "GET", "/v1/install/binary?os=linux&arch=amd64&version="+VER, H).Status, 410)
			again := EnrollSite(t, admin, id, "app")
			TinyScript(t, Raw(t, "GET", "/v1/install/script", H), `used for an earlier enrollment`) // the old used token stays dead
			Eq(t, Raw(t, "GET", "/v1/install/script", bearer(again.Token)).Status, 200, "the new enrollment token upgrades")
			Bucket.Delete(t, "deps/manifest.json")
		})
	})
}
