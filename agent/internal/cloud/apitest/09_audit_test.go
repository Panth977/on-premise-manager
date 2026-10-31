package apitest

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/canonical"
	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/shared"
)

var waDepScope = regexp.MustCompile(`^/v1/deployments/\{deploymentId\}/.+`)

func waAuditByRequest(t testing.TB, requestID string) []J {
	t.Helper()
	q, err := DB.Collection("auditLog").Where("requestId", "==", requestID).Get()
	NoErr(t, err)
	out := []J{}
	for _, d := range q.Docs {
		x := JOf(d.Data())
		x.Map()["changes"] = ParseJ([]byte(x.At("_changes").Str())).V
		x.Map()["data"] = ParseJ([]byte(x.At("_data").Str())).V
		out = append(out, x)
	}
	return out
}

func waCloudSeq(t testing.TB, deploymentID string) int {
	t.Helper()
	return Doc(t, "deployments/"+deploymentID).At("cloudActivitySeq").Int()
}

func waLastCloud(t testing.TB, deploymentID string) J {
	t.Helper()
	d := Doc(t, fmt.Sprintf("deployments/%s/activities/_cloud:%d", deploymentID, waCloudSeq(t, deploymentID)))
	d.Map()["data"] = ParseJ([]byte(d.At("_data").Str())).V
	return d
}

func waFill(path string, params map[string]string) string {
	return pathParam.ReplaceAllStringFunc(path, func(m string) string {
		k := m[1 : len(m)-1]
		v, ok := params[k]
		if !ok {
			v = "zz-" + k
		}
		return url.PathEscape(v)
	})
}

type waRes struct {
	*Resp
	RequestID string
}

func waCall(t testing.TB, p *Person, def cloud.Endpoint, params map[string]string, body any, rawBody []byte) waRes {
	t.Helper()
	o := Opt{Headers: map[string]string{"Authorization": p.Bearer, "User-Agent": "opm-audit-test/1"}}
	switch def.Body {
	case "json":
		o.Headers["Content-Type"] = "application/json"
		if body == nil {
			body = A{}
		}
		o.Body = []byte(show(body))
	case "raw":
		o.Headers["Content-Type"] = "application/octet-stream"
		o.Body = rawBody
		if o.Body == nil {
			o.Body = []byte{}
		}
	}
	r := Raw(t, def.Method, waFill(def.Path, params), o)
	return waRes{r, r.Header("x-request-id")}
}

func waMakeActivity(deploymentID, siteSlug string, seq int, prevHash *string) A {
	id, ts, actor := fmt.Sprintf("%s:%d", siteSlug, seq), ISO(time.Now().Add(time.Duration(seq)*time.Millisecond)), "ops@example.com"
	summary, data := fmt.Sprintf("entry %d", seq), A{"n": seq}
	in, err := canonical.ActivityHashInput(canonical.ActivityFields{ActivityID: id, DeploymentID: deploymentID, SiteSlug: siteSlug, Seq: float64(seq), Ts: ts, Actor: &actor, Origin: "agent", Type: "actor.declared", Summary: summary, Data: data, PrevHash: prevHash})
	if err != nil {
		panic(err)
	}
	var prev any
	if prevHash != nil {
		prev = *prevHash
	}
	return A{"activityId": id, "deploymentId": deploymentID, "siteSlug": siteSlug, "seq": seq, "ts": ts, "actor": actor, "origin": "agent", "type": "actor.declared", "summary": summary, "data": data, "prevHash": prev, "hash": "sha256:" + Sha256Hex(in), "uploadedVia": nil, "uploadedAt": nil}
}

func waOpmlog(t testing.TB, deploymentID, siteSlug string, key SiteKey, entries []A) []byte {
	t.Helper()
	var nd bytes.Buffer
	for _, e := range entries {
		nd.WriteString(canonical.MustCanonicalize(Plain(e)) + "\n")
	}
	manifest := A{"format": "opmlog/1", "deploymentId": deploymentID, "createdAt": Now(), "createdBy": "ops@example.com", "signedBy": A{"siteSlug": siteSlug, "publicKey": key.PublicKey}, "sites": L{A{"siteSlug": siteSlug, "fromSeq": 1, "toSeq": len(entries), "count": len(entries)}}, "activities": A{"path": "activities.ndjson", "sizeBytes": nd.Len(), "sha256": "sha256:" + Sha256Hex(nd.Bytes())}, "debug": L{}}
	mb := []byte(canonical.MustCanonicalize(Plain(manifest)))
	sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(key.Priv, mb))
	tar, err := core.WriteTar([]core.TarEntry{{Name: "manifest.json", Data: mb}, {Name: "activities.ndjson", Data: nd.Bytes()}, {Name: "signature.sig", Data: []byte(sig)}})
	NoErr(t, err)
	return tar
}

// waText is JSON.stringify: no HTML escaping, so a secret holding & < > is still found by its plain text.
func waText(v any) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		panic(err)
	}
	return b.String()
}

// waTick separates two writes whose audit entries a test expects in order: the log sorts by ts (ms) and entries of one
// millisecond come back in random-id order; this client, unlike the TS one, can fit two requests in a millisecond.
func waTick() { Sleep(2) }

// §21 #104 + #105: removed routes answer 410 gone (kept in the contract for old clients)
// §21 #117: the cloud-copy writes of persist volumes are gone too (content is committed)
// §21 #118 openRemoteGui and §21 #125 port tunnels need the single-process cloud (503 elsewhere)
var (
	waNoActivity = []string{"fetchFileToCloud"}
	waGone       = []string{"publish", "resetDraftToHead", "applyTemplateSync", "unlinkTemplate", "putDraftSection", "putDraftSite", "regenerateSecret", "discardDeploymentWorking", "createVolumeContentVersion", "restoreVolumeContentVersion", "requestVolumeSnapshot", "sendVolumeContent", "cancelVolumeContentPush"}
)

type waRecipe struct {
	params map[string]string
	body   any
	raw    []byte
}

// Port of backend/test/09-audit.test.ts: root audit log (SPEC-final §6.2) + deployment activities guarantee: every human
// non-GET CLOUD_API op writes an audit entry (root scope) or a "_cloud" activity (deployment scope); the only exemption is
// putUploadPart. A new route that logs nothing fails here.
func TestAudit(t *testing.T) {
	t.Run("guarantee: every human non-GET op writes an audit entry (root) or a _cloud activity (deployment scope)", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		depPath := "deployments/" + id
		dep := A{"deploymentId": id}
		// §21 #101: the app volume is persisted to the cloud (published by the publish recipe, head via resetDraftToHead)
		v0 := admin.API.Call(t, "getDeployment", dep)
		appSite0 := v0.At("draft.sites").Find("siteSlug", "app").Clone()
		appSite0.At("config.volumes.0").Map()["persist"] = A{"exclude": L{}, "maxBytes": 50 * 1024 * 1024, "auto": true}
		admin.API.Call(t, "putDraftSite", A{"deploymentId": id, "siteSlug": "app", "body": A{"ifRevision": v0.At("draftVersion"), "site": appSite0}})
		// §21 #104: commit it (the release) and point the legacy head at its compat revision (volume content needs a head)
		c0 := admin.API.Call(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "persist the app volume", "ifRevision": admin.API.Call(t, "getDeployment", dep).At("draftVersion")}})
		UpdateDoc(t, depPath, A{"head": A{"revId": c0.At("data.compat.revId"), "seq": c0.At("data.compat.seq"), "hash": c0.At("hash"), "reportedAt": Now()}})
		view := admin.API.Call(t, "getDeployment", dep)
		db := EnrollSite(t, admin, id, "db")
		EnrollSite(t, admin, id, "app")
		secretSlug := view.At("draft.secrets").FindF(func(s J) bool { return s.At("isSet").Truthy() }).At("secretSlug").Str()
		var transferID, fileID, tunnelID, tokenHash string
		// a port of the server's tunnel range (no fixed port)
		tunnelFrom, _, _ := strings.Cut(TunnelPorts, "-")
		publicPort := 0
		_, err := fmt.Sscan(tunnelFrom, &publicPort)
		NoErr(t, err)
		// deployment-scope recipes (must succeed and must write a _cloud activity)
		recipes := map[string]func() waRecipe{
			"revealSecret":    func() waRecipe { return waRecipe{params: map[string]string{"secretSlug": secretSlug}} },
			"revealAccessKey": func() waRecipe { return waRecipe{} },
			"revealInstallCommand": func() waRecipe {
				NoErr(t, DB.Doc(depPath+"/agents/app").Delete())
				tk := admin.API.Call(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "app", "replaceActive": true}})
				return waRecipe{params: map[string]string{"tokenHash": tk.At("tokenHash").Str()}}
			},
			// §21 #92 the enrolled site db's upgrade command (its install token keeps its plaintext)
			"revealUpgradeCommand":     func() waRecipe { return waRecipe{params: map[string]string{"siteSlug": "db"}} },
			"regenerateUpgradeCommand": func() waRecipe { return waRecipe{params: map[string]string{"siteSlug": "db"}} },
			// §21 #104 commit = the release (+ the compat proposal; the legacy head points at it for the volume-content recipes)
			"commitDeployment": func() waRecipe {
				config := DepInput(t, admin.Real, id)
				config.At("variables.0").Map()["description"] = "audited"
				return waRecipe{body: A{"message": "audit guarantee", "baseCommitId": admin.Real.Call(t, "getDeployment", dep).At("branch.head.commitId"), "config": config}}
			},
			"rotateAccessKey": func() waRecipe { return waRecipe{} },
			"createInstallToken": func() waRecipe {
				NoErr(t, DB.Doc(depPath+"/agents/app").Delete()) // free the slug for a token
				return waRecipe{body: A{"siteSlug": "app"}}
			},
			"revokeInstallToken": func() waRecipe {
				tk := admin.API.Call(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "app"}})
				tokenHash = tk.At("tokenHash").Str()
				return waRecipe{params: map[string]string{"tokenHash": tokenHash}}
			},
			// §21 #90 (pin 0.10.5-audit via deps/manifest.json; site db on 0.10.0)
			"requestAgentUpgrade": func() waRecipe {
				Bucket.Save(t, "deps/manifest.json", show(A{"docker": A{"version": "0.0.1-a", "files": A{"amd64": A{"path": "x", "sha256": strings.Repeat("e", 64), "size": 1}, "arm64": A{"path": "y", "sha256": strings.Repeat("f", 64), "size": 1}}}, "minDockerApi": "1.41", "agent": A{"version": "0.10.5-audit", "notes": nil}, "updatedAt": Now()}))
				UpdateDoc(t, depPath+"/agents/db", A{"agentVersion": "0.10.0"})
				return waRecipe{params: map[string]string{"siteSlug": "db"}, body: A{"version": "0.10.5-audit", "confirm": "db"}}
			},
			"cancelAgentUpgrade":    func() waRecipe { return waRecipe{params: map[string]string{"siteSlug": "db"}} },
			"upgradeAllAgents":      func() waRecipe { return waRecipe{body: A{"version": "0.10.5-audit", "confirm": "UPGRADE"}} },
			"cancelAgentUpgradeRun": func() waRecipe { return waRecipe{} },
			"revokeAgent": func() waRecipe {
				Bucket.Delete(t, "deps/manifest.json")
				return waRecipe{params: map[string]string{"siteSlug": "db"}, body: A{"confirm": "db"}}
			},
			"uploadActivities": func() waRecipe {
				a1 := waMakeActivity(id, "db", 1, nil)
				h1 := a1["hash"].(string)
				a2 := waMakeActivity(id, "db", 2, &h1)
				return waRecipe{raw: waOpmlog(t, id, "db", db.Key, []A{a1, a2})}
			},
			"createTransfer": func() waRecipe {
				return waRecipe{params: map[string]string{"siteSlug": "app"}, body: A{"fileName": "notes.txt", "sizeBytes": 5, "sha256": "sha256:" + Sha256Hex("hello")}}
			},
			"deleteTransfer": func() waRecipe { return waRecipe{params: map[string]string{"transferId": transferID}} },
			// §21 #123 shared files
			"openRemoteGui": func() waRecipe { return waRecipe{params: map[string]string{"siteSlug": "app"}} }, // §21 #118 remote-gui.opened
			"publishFile": func() waRecipe {
				return waRecipe{body: A{"fileName": "shared.txt", "sizeBytes": 5, "sha256": "sha256:" + Sha256Hex("hello")}}
			},
			"unpublishFile": func() waRecipe { return waRecipe{params: map[string]string{"fileId": fileID}} },
			"createPortTunnel": func() waRecipe {
				return waRecipe{body: A{"siteSlug": "app", "publicPort": publicPort, "target": A{"kind": "host", "port": 5432}}} // §21 #125 tunnel.created
			},
			"deletePortTunnel": func() waRecipe { return waRecipe{params: map[string]string{"tunnelId": tunnelID}} }, // tunnel.deleted
		}
		checked := []string{}
		for _, def := range cloud.Endpoints() {
			op := def.Op
			if def.Auth != "human" || def.Method == "GET" {
				continue
			}
			if slices.Contains(shared.AUDIT_EXEMPT_OPS, op) {
				continue
			}
			// §21 #104: removed routes answer 410 gone (kept in the contract for old clients)
			if slices.Contains(waGone, op) {
				continue
			}
			// §21 #123 asking a holder for a copy writes no activity (the arrival does: file.fetched)
			if slices.Contains(waNoActivity, op) {
				continue
			}
			if waDepScope.MatchString(def.Path) {
				recipe, ok := recipes[op]
				True(t, ok, "deployment-scope op "+op+" has no recipe in 09_audit_test.go: add one (and make the op write a _cloud activity)")
				r0 := recipe()
				before := waCloudSeq(t, id)
				params := map[string]string{"deploymentId": id}
				for k, v := range r0.params {
					params[k] = v
				}
				res := waCall(t, admin, def, params, r0.body, r0.raw)
				if res.Status >= 300 {
					t.Fatalf("%s must succeed for the guarantee test: %d %.300s", op, res.Status, res.Text())
				}
				if op == "createTransfer" {
					transferID = res.J.At("transfer.transferId").Str()
				}
				if op == "publishFile" {
					fileID = res.J.At("file.fileId").Str()
				}
				if op == "createPortTunnel" {
					tunnelID = res.J.At("tunnelId").Str()
				}
				after := waCloudSeq(t, id)
				True(t, after > before, op+" wrote no _cloud activity")
				audited := slices.Contains(shared.AUDIT_DEPLOYMENT_OPS, op)
				entries := waAuditByRequest(t, res.RequestID)
				if audited {
					Eq(t, len(entries), 1, op+" is a deployment credential op: exactly one root audit entry")
					Eq(t, entries[0].At("target"), "deployments/"+id)
				} else {
					Eq(t, len(entries), 0, op+" is deployment-internal: no root audit entry")
				}
			} else {
				// root scope: any outcome (dummy ids / empty bodies) must produce exactly one entry with this request id
				res := waCall(t, admin, def, nil, A{}, nil)
				entries := waAuditByRequest(t, res.RequestID)
				Eq(t, len(entries), 1, fmt.Sprintf("%s (%d) wrote %d audit entries", op, res.Status, len(entries)))
				e := entries[0]
				Eq(t, e.At("op"), op)
				Eq(t, e.At("actor"), admin.UID)
				Eq(t, e.At("actorEmail"), admin.Email)
				Eq(t, e.At("status"), res.Status)
				if res.Status < 400 {
					Eq(t, e.At("outcome"), "ok")
				} else {
					Eq(t, e.At("outcome"), "error")
					Eq(t, e.At("errorCode"), res.J.At("error.code"))
				}
				Eq(t, e.At("userAgent"), "opm-audit-test/1")
				True(t, e.At("ip").Exists())
			}
			checked = append(checked, op)
		}
		// exempt: per-part upload PUTs write nothing
		part := waCall(t, admin, endpoints["putUploadPart"], nil, nil, []byte("x"))
		Eq(t, len(waAuditByRequest(t, part.RequestID)), 0)
		True(t, len(checked) >= 40, fmt.Sprintf("checked %d ops", len(checked)))
		// the root deployment ops are audited in addition to their activities
		for _, op := range []string{"patchDeployment", "deleteDeployment"} {
			True(t, slices.Contains(checked, op))
		}
		q, err := DB.Collection(depPath+"/activities").Where("siteSlug", "==", "_cloud").Get()
		NoErr(t, err)
		types := map[string]bool{}
		for _, d := range q.Docs {
			types[JOf(d.Data()).At("type").Str()] = true
		}
		for _, k := range []string{"transfer.sent", "transfer.deleted", "activities.uploaded", "secret.revealed", "proposal.published", "branch.committed", "access-key.rotated", "install-token.created", "install-token.revoked", "agent.revoked", "access-key.revealed", "install-token.revealed", "agent.upgrade-requested", "agent.upgrade-cancelled", "file.published", "file.unpublished"} {
			True(t, types[k], "missing _cloud activity "+k)
		}
	})

	t.Run("§21 #105 a commit records secret value changes by slug (never values); deployment delete writes deployment.deleted + audit", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		v := admin.Real.Call(t, "getDeployment", A{"deploymentId": id})
		config := DepInput(t, admin.Real, id)
		slugs := L{}
		for _, s := range config.At("secrets").Arr() {
			s.Map()["plainValue"] = "PLAIN-VALUE-Q9"
			slugs = append(slugs, s.At("secretSlug").V)
		}
		waTick()
		admin.Real.Call(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "set the token", "baseCommitId": v.At("branch.head.commitId"), "config": config}})
		a := waLastCloud(t, id)
		Eq(t, a.At("type"), "branch.committed")
		Eq(t, a.At("data.secretValues"), A{"set": slugs, "generated": L{}, "cleared": L{}})
		NotContains(t, a.String(), "PLAIN-VALUE-Q9")
		waTick()
		admin.API.Call(t, "deleteDeployment", A{"deploymentId": id, "body": A{"confirm": v.At("label")}})
		Eq(t, waLastCloud(t, id).At("type"), "deployment.deleted")
		log := admin.API.Call(t, "listAuditLog", A{"query": A{"target": "deployments/" + id}})
		Eq(t, log.At("items").Pluck("action"), L{"deployment.deleted", "deployment.committed", "deployment.created"})
		True(t, waSome(log.At("items.0.changes"), func(c J) bool {
			return c.At("path").Str() == "deleted" && c.At("before").V == false && c.At("after").V == true
		}))
		NotContains(t, log.At("items").String(), "PLAIN-VALUE-Q9")
	})

	t.Run("redaction: no secret value is ever stored in the audit log", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		pw1 := "REG-PW-" + Uniq("x")
		pw2 := "REG-PW2-" + Uniq("x")
		reg := admin.API.Call(t, "createRegistry", A{"body": A{"label": "Redact", "url": "https://127.0.0.1:1", "username": "u", "password": pw1}})
		rid := reg.At("registryId").Str()
		admin.API.Call(t, "patchRegistry", A{"registryId": rid, "body": A{"password": pw2}})
		admin.API.Call(t, "revealRegistryPassword", A{"registryId": rid})
		hd := HelloDeployment(t, admin)
		accessKey := hd.AccessKey.Str()
		secretSlug := admin.API.Call(t, "getDeployment", A{"deploymentId": hd.ID()}).At("draft.secrets").FindF(func(s J) bool { return s.At("isSet").Truthy() }).At("secretSlug")
		generated := admin.API.Call(t, "revealSecret", A{"deploymentId": hd.ID(), "secretSlug": secretSlug}).At("value").Str()
		acc := admin.API.Call(t, "createAccount", A{"body": A{"name": "Redact person", "email": Uniq("rd") + "@opm.test"}})
		reset := admin.API.Call(t, "resetAccountPassword", A{"uid": acc.At("account.uid")})
		u, err := url.Parse(reset.At("value").Str())
		NoErr(t, err)
		oob := reset.At("passwordSetLink").Str()
		if q := u.Query(); q.Has("oobCode") {
			oob = q.Get("oobCode")
		}
		var lines []string
		for _, d := range ListDocs(t, "auditLog") {
			lines = append(lines, waText(d.Data.V))
		}
		all := strings.Join(lines, "\n")
		for _, secret := range []string{pw1, pw2, generated, accessKey, strings.ReplaceAll(accessKey, "-", ""), oob, acc.At("value").Str()} {
			False(t, strings.Contains(all, secret), "secret leaked into auditLog: "+secret[:min(6, len(secret))]+"…")
		}
		False(t, strings.Contains(all, "$argon2id"), "access key hash leaked")
		patch := admin.API.Call(t, "listAuditLog", A{"query": A{"target": "registries/" + rid, "action": "registry.updated"}}).At("items.0")
		Eq(t, patch.At("changes").Find("path", "password"), A{"path": "password", "before": "***", "after": "***", "secret": true})
		created := admin.API.Call(t, "listAuditLog", A{"query": A{"target": "deployments/" + hd.ID(), "action": "deployment.created"}}).At("items.0")
		Eq(t, created.At("changes").Find("path", "accessKeyHash").At("after"), "***")
		reveal := admin.API.Call(t, "listAuditLog", A{"query": A{"target": "registries/" + rid, "action": "registry.password-revealed"}}).At("items")
		Eq(t, reveal.Len(), 1)
		Eq(t, reveal.At("0.actor"), admin.UID)
	})

	t.Run("failure entries: 403 forbidden, 422 invalid, no account; 401 is not logged", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		viewer := NewPerson(t, L{})
		slug := Uniq("t")
		viewer.API.Rejects(t, "createTag", A{"body": A{"tagSlug": slug, "label": "X", "icon": "tag", "color": "#112233"}}, "forbidden", 403)
		f := admin.API.Call(t, "listAuditLog", A{"query": A{"actor": viewer.UID}}).At("items")
		Eq(t, f.Len(), 1)
		Eq(t, f.At("0.action"), "tag.created")
		Eq(t, f.At("0.outcome"), "error")
		Eq(t, f.At("0.status"), 403)
		Eq(t, f.At("0.errorCode"), "forbidden")
		Eq(t, f.At("0.target"), "tags/"+slug)
		Eq(t, f.At("0.actorEmail"), viewer.Email)
		True(t, f.At("0.requestId").Truthy())
		byEmail := admin.API.Call(t, "listAuditLog", A{"query": A{"actor": strings.ToUpper(viewer.Email)}}).At("items")
		Eq(t, byEmail.Len(), 1)
		admin.API.Rejects(t, "createTag", A{"body": A{"tagSlug": "Bad Slug", "label": "", "icon": "tag", "color": "red"}}, "invalid", 422)
		inv := admin.API.Call(t, "listAuditLog", A{"query": A{"actor": admin.UID, "outcome": "error"}}).At("items.0")
		Eq(t, inv.At("status"), 422)
		Eq(t, inv.At("errorCode"), "invalid")
		ghost := NewPerson(t, L{}, PersonOpts{NoAccount: true})
		ghost.API.Rejects(t, "createTemplate", A{"body": A{"label": "x", "icon": "box"}}, "forbidden", 403)
		g := admin.API.Call(t, "listAuditLog", A{"query": A{"actor": ghost.UID}}).At("items")
		Eq(t, g.Len(), 1)
		Eq(t, g.At("0.errorMessage"), "no account")
		r := Raw(t, "POST", "/v1/tags", Opt{Headers: map[string]string{"Authorization": "Bearer nope", "Content-Type": "application/json"}, Body: []byte("{}")})
		Eq(t, r.Status, 401)
		Eq(t, len(waAuditByRequest(t, r.Header("x-request-id"))), 0)
	})

	t.Run("template patch: before → after diff; account actions refined; filters", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		tpl := admin.API.Call(t, "createTemplate", A{"body": A{"label": "Diff me", "icon": "box"}})
		id := tpl.At("templateId").Str()
		// §21 #105: details by PATCH (template.updated), config by commits (template.committed)
		waTick()
		admin.Real.Call(t, "patchTemplate", A{"templateId": id, "body": A{"ifVersion": 1, "label": "Diff me 2", "description": "new"}})
		port := func(value int) L {
			return L{A{"variableSlug": "port", "label": "Port", "description": "", "type": "number", "value": value, "required": false, "options": nil, "validation": nil}}
		}
		waTick()
		c1 := admin.Real.Call(t, "commitTemplate", A{"templateId": id, "body": A{"message": "port", "baseCommitId": tpl.At("branch.head.commitId"), "config": A{"variables": port(8080), "secrets": L{}, "files": L{}, "sites": L{}}}})
		waTick()
		admin.Real.Call(t, "commitTemplate", A{"templateId": id, "body": A{"message": "port 9090", "baseCommitId": c1.At("commitId"), "config": A{"variables": port(9090), "secrets": L{}, "files": L{}, "sites": L{}}}})
		target := "templates/" + id
		hist := admin.API.Call(t, "listAuditLog", A{"query": A{"target": target}})
		Eq(t, hist.At("items").Pluck("action"), L{"template.committed", "template.committed", "template.updated", "template.created"})
		p2, p1, c := hist.At("items.1"), hist.At("items.2"), hist.At("items.3")
		by := func(e J, path string) J { return e.At("changes").Find("path", path) }
		Eq(t, by(p1, "label"), A{"path": "label", "before": "Diff me", "after": "Diff me 2"})
		Eq(t, by(p1, "version"), A{"path": "version", "before": 1, "after": 2})
		False(t, by(p1, "updatedAt").Truthy())
		Eq(t, by(c, "label").At("after"), "Diff me")
		Eq(t, c.At("targetType"), "template")
		// accounts: refined actions
		acc := admin.API.Call(t, "createAccount", A{"body": A{"name": "Refine", "email": Uniq("rf") + "@opm.test"}})
		uid := acc.At("account.uid").Str()
		waTick()
		admin.API.Call(t, "patchAccount", A{"uid": uid, "body": A{"name": "Refined"}})
		waTick()
		admin.API.Call(t, "patchAccount", A{"uid": uid, "body": A{"access": L{A{"resource": "templates", "actions": L{"read"}, "scope": A{"kind": "all"}}}}})
		waTick()
		admin.API.Call(t, "disableAccount", A{"uid": uid, "body": A{"disabled": true}})
		waTick()
		admin.API.Call(t, "disableAccount", A{"uid": uid, "body": A{"disabled": false}})
		waTick()
		admin.API.Call(t, "resetAccountPassword", A{"uid": uid})
		acts := admin.API.Call(t, "listAuditLog", A{"query": A{"target": "accounts/" + uid}}).At("items").Pluck("action")
		Eq(t, acts, L{"account.reset-link-created", "account.enabled", "account.disabled", "account.access-changed", "account.updated", "account.created"})
		// filters
		prefix := admin.API.Call(t, "listAuditLog", A{"query": A{"action": "account.", "target": "accounts/" + uid}}).At("items")
		Eq(t, prefix.Len(), 6)
		exact := admin.API.Call(t, "listAuditLog", A{"query": A{"action": "account.disabled", "actor": admin.UID}}).At("items")
		True(t, exact.Len() >= 1 && waEvery(exact, func(e J) bool { return e.At("action").Str() == "account.disabled" }))
		tt := admin.API.Call(t, "listAuditLog", A{"query": A{"targetType": "template", "actor": admin.UID}}).At("items")
		Eq(t, tt.Len(), 4)
		ok := admin.API.Call(t, "listAuditLog", A{"query": A{"outcome": "ok", "target": target}}).At("items")
		Eq(t, ok.Len(), 4)
		ranged := admin.API.Call(t, "listAuditLog", A{"query": A{"target": target, "from": p1.At("ts"), "to": p2.At("ts")}}).At("items")
		Eq(t, ranged.Pluck("auditId"), L{p2.At("auditId"), p1.At("auditId")})
		paged1 := admin.API.Call(t, "listAuditLog", A{"query": A{"actor": admin.UID, "limit": 4}})
		paged2 := admin.API.Call(t, "listAuditLog", A{"query": A{"actor": admin.UID, "limit": 4, "cursor": paged1.At("cursor")}})
		Eq(t, paged1.At("items").Len(), 4)
		True(t, paged2.At("items").Len() >= 4)
		True(t, paged2.At("items.0.ts").Str() <= paged1.At("items.3.ts").Str())
		for _, q := range []A{{"outcome": "maybe"}, {"targetType": "planet"}, {"action": "Bad Action"}, {"from": "yesterday"}} {
			admin.API.Rejects(t, "listAuditLog", A{"query": q}, "invalid", 422)
		}
		viewer := NewPerson(t, L{})
		viewer.API.Rejects(t, "listAuditLog", A{}, "forbidden", 403)
	})

	t.Run("uploads: one entry at complete (part count), none per part; blobs audited", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		data := []byte("audit-blob-" + Uniq("x"))
		blob := admin.API.Call(t, "uploadBlob", A{"body": data})
		b := admin.API.Call(t, "listAuditLog", A{"query": A{"target": "blobs/" + blob.At("sha256").Str()}}).At("items")
		Eq(t, b.At("0.action"), "blob.uploaded")
		Eq(t, b.At("0.data.sizeBytes"), len(data))
		up := admin.API.Call(t, "createUpload", A{"body": A{"purpose": "blob", "sizeBytes": len(data), "sha256": blob.At("sha256")}})
		admin.API.Call(t, "putUploadPart", A{"uploadId": up.At("uploadId"), "index": "0", "body": data})
		waTick()
		admin.API.Call(t, "completeUpload", A{"uploadId": up.At("uploadId")})
		u := admin.API.Call(t, "listAuditLog", A{"query": A{"target": "uploads/" + up.At("uploadId").Str()}}).At("items")
		Eq(t, u.Pluck("action"), L{"upload.completed", "upload.created"})
		Eq(t, u.At("0.data.partCount"), 1)
		True(t, waSome(u.At("0.changes"), func(c J) bool {
			return c.At("path").Str() == "status" && c.At("before").Str() == "open" && c.At("after").Str() == "complete"
		}))
	})

	t.Run("system entries: image import failure (trigger) and the daily job", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		reg := admin.API.Call(t, "createRegistry", A{"body": A{"label": "Dead", "url": "https://127.0.0.1:1", "username": "u", "password": "p"}})
		svc := admin.API.Call(t, "createService", A{"body": A{"label": "Imp", "serviceSlug": Uniq("imp"), "icon": "lucide:box"}})
		slug := svc.At("serviceSlug").Str()
		admin.API.Call(t, "registerImage", A{"serviceSlug": slug, "body": A{"version": "v1", "reference": "127.0.0.1:1/test/imp:v1", "registryId": reg.At("registryId"), "mode": "copy", "kind": "service"}})
		target := "services/" + slug + "/images/v1"
		isSystem := func(e J) bool { return e.At("actor").Str() == "system" }
		entries := J{L{}}
		for i := 0; i < 60 && !waSome(entries, isSystem); i++ {
			Sleep(1000)
			entries = admin.API.Call(t, "listAuditLog", A{"query": A{"target": target}}).At("items")
		}
		sys := entries.FindF(isSystem)
		True(t, sys.Truthy(), fmt.Sprintf("no system entry for %s: %s", target, JOf(entries.Pluck("action"))))
		Eq(t, sys.At("action"), "image.import-failed")
		Eq(t, sys.At("op"), "system:importImage")
		Eq(t, sys.At("outcome"), "error")
		True(t, waSome(sys.At("changes"), func(c J) bool {
			return c.At("path").Str() == "import.status" && c.At("after").Str() == "failed"
		}))
		True(t, waSome(entries, func(e J) bool { return e.At("action").Str() == "image.registered" && e.At("actor").Str() == admin.UID }))
		r := RunDaily(t)
		d := admin.API.Call(t, "listAuditLog", A{"query": A{"actor": "system", "action": "system.daily-cleanup"}}).At("items.0")
		Eq(t, d.At("op"), "system:daily")
		Eq(t, d.At("data.uptimeDeleted"), r.At("uptimeDeleted"))
	})
}
