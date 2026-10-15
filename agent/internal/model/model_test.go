package model

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const siteConfigJSON = `{
  "host": "{{ var.public-host }}", "agentPort": 7080, "agentGuiAliasPort": null,
  "depends": [{ "siteSlug": "db" }], "notFoundPageId": "N9bC1dE3fG5hI7jK9lM1",
  "containers": [
    { "containerSlug": "api", "serviceSlug": "api", "version": "v2", "enabled": true,
      "env": [ { "name": "DATABASE_URL", "value": "postgres://{{ site.db.host }}", "secretRef": null },
               { "name": "SESSION_KEY", "value": null, "secretRef": "session-key" } ],
      "volumes": [ { "imageVolume": "uploads", "mountPath": null, "source": { "kind": "data", "name": "api-uploads" }, "readOnly": false },
                  { "imageVolume": "cache", "mountPath": null, "source": { "kind": "none" }, "readOnly": false },
                  { "imageVolume": null, "mountPath": "/srv/host", "source": { "kind": "host", "path": "/opt/x" }, "readOnly": true },
                  { "imageVolume": null, "mountPath": "/etc/api/config.json", "source": { "kind": "file", "fileSlug": "api-config" }, "readOnly": true },
                  { "imageVolume": null, "mountPath": "/run/key.pem", "source": { "kind": "secret", "secretSlug": "tls-key" }, "readOnly": true } ],
      "ports": [ { "portName": "http", "hostPort": null, "hostIp": "0.0.0.0", "edge": true },
                { "portName": "metrics", "hostPort": 9100, "hostIp": "127.0.0.1", "edge": false },
                { "portName": "admin", "hostPort": "{{ var.admin-port }}", "hostIp": "0.0.0.0", "edge": true } ],
      "command": null, "entrypoint": ["/bin/sh", "-c"], "restart": "always", "resources": { "cpus": 0.5, "memoryMb": null }, "healthcheck": null, "depends": [] },
    { "containerSlug": "web-2", "serviceSlug": "web", "version": "v3", "enabled": false, "env": [], "volumes": [], "ports": [],
      "command": ["nginx", "-g", "daemon off;"], "entrypoint": null, "restart": "on-failure", "resources": { "cpus": null, "memoryMb": 256 },
      "healthcheck": { "type": "tcp", "portName": "http", "intervalSec": 10, "timeoutSec": 5, "retries": 3, "startPeriodSec": 0 }, "depends": [{ "containerSlug": "api" }] } ],
  "edge": { "httpPort": 80, "httpsPort": 443,
    "hosts": [
      { "hostname": "plant.acme.com", "tls": { "mode": "acme", "email": "it@acme.com", "challenge": "http-01", "dnsProvider": null, "dnsTokenSecretSlug": null }, "redirectHttpToHttps": true },
      { "hostname": "*.acme.com", "tls": { "mode": "acme", "email": "it@acme.com", "challenge": "dns-01", "dnsProvider": "cloudflare", "dnsTokenSecretSlug": "cf-token" }, "redirectHttpToHttps": true },
      { "hostname": "10.0.1.10", "tls": { "mode": "internal" }, "redirectHttpToHttps": false },
      { "hostname": "intranet.corp.local", "tls": { "mode": "files", "certFileSlug": "tls-cert", "keySecretSlug": "tls-key" }, "redirectHttpToHttps": true },
      { "hostname": "*", "tls": { "mode": "none" }, "redirectHttpToHttps": false } ],
    "routes": [
      { "routeId": "api", "hosts": [], "order": 1, "match": { "pathPrefix": "/api", "methods": null },
        "action": { "type": "proxy", "containerSlug": "api", "portName": "http", "stripPrefix": true, "addPrefix": null, "setHeaders": { "X-Forwarded-Prefix": "/api" }, "removeHeaders": [], "websocket": false, "timeoutSec": 60 } },
      { "routeId": "old", "hosts": ["plant.acme.com"], "order": 2, "match": { "pathPrefix": "/old", "methods": ["GET", "HEAD"] }, "action": { "type": "redirect", "to": "/new", "status": 308, "keepPath": true } } ] } }`

const imageSpecJSON = `{ "serviceSlug": "api", "version": "v1", "digest": "sha256:aa", "configDigest": "sha256:bb", "platform": "linux/amd64",
  "manifestMediaType": "application/vnd.oci.image.manifest.v1+json", "layers": [ { "digest": "sha256:cc", "sizeBytes": 12, "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip" } ],
  "sizeBytes": 12, "kind": "service", "entrypoint": null, "command": ["node", "server.js"], "workdir": null, "user": "1000:1000",
  "ports": [ { "name": "http", "port": 8080, "protocol": "tcp", "description": "" }, { "name": "alt", "port": "{{ var.p }}", "protocol": "tcp", "description": "x" } ],
  "env": [ { "name": "A", "required": true, "secret": true, "description": "" }, { "name": "B", "value": "x", "required": false, "secret": false, "description": "" },
           { "name": "C", "value": ["info", "debug"], "required": false, "secret": false, "description": "" } ],
  "volumes": [ { "name": "data", "mountPath": "/data", "description": "" } ],
  "healthcheck": { "type": "http", "portName": "http", "path": "/healthz", "expectStatus": 200, "intervalSec": 10, "timeoutSec": 5, "retries": 3, "startPeriodSec": 30 },
  "stopTimeoutSec": 30 }`

const execHCJSON = `{ "type": "exec", "command": ["redis-cli", "ping"], "expectExitCode": 0, "intervalSec": 5, "timeoutSec": 3, "retries": 3, "startPeriodSec": 5 }`

const proposalJSON = `{ "revId": "01JA2Y0000000000000000000A", "parentRevId": null, "seq": 1, "summary": "first", "origin": "cloud", "kind": "change",
  "createdAt": "2026-10-07T12:30:00Z", "createdBy": "uidOps07", "status": "ready",
  "sites": { "app": { "staging": "ready", "progress": 1, "error": null, "updatedAt": "2026-10-07T12:31:00Z" }, "db": { "staging": "failed", "progress": 0.5, "error": "x", "updatedAt": null } },
  "approvedBy": null, "approvedAt": null, "updateId": null, "result": { "ok": false, "message": "m", "snapshotId": null }, "supersededBy": null, "updatedAt": "2026-10-07T12:31:00Z" }`

const cloudViewJSON = `{ "fetchedAt": "2026-10-07T12:04:58Z", "source": "peer:app", "cursor": "193", "deploymentId": "D1", "deleted": false,
  "proposals": [ { "revId": "R2", "parentRevId": "R1", "seq": 2, "summary": "s", "status": "staging", "origin": "cloud", "kind": "change", "createdAt": "2026-10-07T12:00:00Z", "createdBy": "u" } ],
  "transfersToSite": [], "draftHead": { "revId": "R1", "seq": 1 }, "accessKey": { "hash": "$argon2id$v=19$m=65536,t=3,p=1$x$y", "version": 3 },
  "peers": [ { "siteSlug": "db", "publicKey": "AAAA", "advertiseHost": "127.0.0.1", "agentPort": 7081, "enrolledAt": "2026-10-07T11:00:00Z" } ] }`

func roundTrip[T any](t *testing.T, name, in string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(in), &v); err != nil {
		t.Fatalf("%s: unmarshal: %v", name, err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("%s: marshal: %v", name, err)
	}
	var a, b any
	_ = json.Unmarshal([]byte(in), &a)
	_ = json.Unmarshal(out, &b)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("%s: round trip differs\n in: %v\nout: %s", name, a, out)
	}
	return v
}

func TestRoundTrips(t *testing.T) {
	sc := roundTrip[SiteConfig](t, "SiteConfig", siteConfigJSON)
	if sc.Containers[0].Ports[0].HostPort.IsNull() != true || *sc.Containers[0].Ports[1].HostPort.Num != 9100 || *sc.Containers[0].Ports[2].HostPort.Expr != "{{ var.admin-port }}" {
		t.Fatal("hostPort variants")
	}
	if sc.Containers[0].Volumes[2].Source.Path != "/opt/x" || sc.Containers[0].Volumes[1].Source.Kind != "none" || sc.Containers[1].Healthcheck == nil {
		t.Fatal("volume sources / healthcheck")
	}
	nullHost := roundTrip[SiteConfig](t, "SiteConfig host null", `{"host":null,"agentPort":7080,"agentGuiAliasPort":8080,"containers":[],"depends":[],"notFoundPageId":null,"edge":{"httpPort":80,"httpsPort":443,"hosts":[],"routes":[]}}`)
	if nullHost.Host != nil {
		t.Fatal("host null = auto")
	}
	roundTrip[FileDef](t, "FileDef render", `{"fileSlug":"a","label":"A","description":"","mime":"text/plain","editor":"text","maxBytes":10,"content":null,"render":"expressions"}`)
	legacy := roundTrip[FileDef](t, "FileDef legacy", `{"fileSlug":"a","label":"A","description":"","mime":"text/plain","editor":"text","maxBytes":10,"content":null}`)
	if legacy.RenderMode() != "expressions" {
		t.Fatal("legacy text render default")
	}
	if sc.Edge.Hosts[1].TLS.DNSProvider == nil || sc.Edge.Routes[1].Action.Status != 308 {
		t.Fatal("unions")
	}
	spec := roundTrip[ImageSpec](t, "ImageSpec", imageSpecJSON)
	if spec.Env[0].Value != nil || *spec.Env[1].Value.Str != "x" || spec.Env[2].Value.List[0] != "info" {
		t.Fatal("env value variants")
	}
	roundTrip[HealthCheck](t, "exec healthcheck", execHCJSON)
	roundTrip[Proposal](t, "Proposal", proposalJSON)
	roundTrip[CloudView](t, "CloudView", cloudViewJSON)
	roundTrip[Variable](t, "Variable", `{ "variableSlug": "f", "label": "F", "description": "", "type": "json", "value": { "a": [1, true, null] }, "required": false, "options": null, "validation": { "min": null, "max": 3, "pattern": null } }`)
}

func TestUnknownDiscriminators(t *testing.T) {
	for _, c := range []struct {
		v  any
		in string
	}{
		{&HealthCheck{}, `{"type":"grpc"}`},
		{&EdgeTls{}, `{"mode":"magic"}`},
		{&EdgeRouteAction{}, `{"type":"magic"}`},
		{&VolumeSource{}, `{"kind":"tmpfs"}`},
	} {
		if err := json.Unmarshal([]byte(c.in), c.v); err == nil {
			t.Errorf("%s: want error", c.in)
		}
	}
}

func TestOptionalFields(t *testing.T) {
	in := SecretDefInput{SecretSlug: "a", Label: "A"}
	b, _ := json.Marshal(in)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, ok := m["plainValue"]; ok {
		t.Fatal("unset plainValue must be absent")
	}
	var back SecretDefInput
	_ = json.Unmarshal([]byte(`{"secretSlug":"a","label":"A","description":"","required":true,"generate":null,"plainValue":null}`), &back)
	if !back.PlainValue.Set || back.PlainValue.Value != nil {
		t.Fatal("null plainValue must be Set with nil value")
	}
	b, _ = json.Marshal(back)
	_ = json.Unmarshal(b, &m)
	if v, ok := m["plainValue"]; !ok || v != nil {
		t.Fatal("explicit null must be kept")
	}
	cfg := roundTrip[AgentConfigFile](t, "AgentConfigFile", `{"apiBase":"https://api","deploymentId":"D","siteSlug":"app","guiAliasPort":null,"agentPort":7081}`)
	if !cfg.GuiAliasPort.Set || cfg.GuiAliasPort.Value != nil || cfg.Proxy.Set {
		t.Fatal("agent config optional")
	}
	roundTrip[ProposalStatusReport](t, "ProposalStatusReport", `{"siteSlug":"db","staging":"downloading","progress":0.62}`)
	roundTrip[ProposalStatusReport](t, "ProposalStatusReport null error", `{"siteSlug":"db","staging":"ready","progress":1,"error":null}`)
}

// Phase U: ImageEnv.options (absent / null / list) and catalog service icons (null, lucide, data URL) must round-trip
// byte-exactly, because the agent re-hashes Revision.catalog from the decoded structs.
func TestCatalogOptionsAndIconsRoundTrip(t *testing.T) {
	const catalog = `{
  "services": {
    "api": { "serviceSlug": "api", "label": "API", "icon": null },
    "web": { "serviceSlug": "web", "label": "Web", "icon": "lucide:globe" },
    "kv": { "serviceSlug": "kv", "label": "KV", "icon": "data:image/png;base64,iVBORw0KGgo=" },
    "old": { "serviceSlug": "old", "label": "Old", "icon": "server" } },
  "images": {
    "api:v1": { "serviceSlug": "api", "version": "v1", "digest": "sha256:aa", "configDigest": "sha256:bb", "platform": "linux/amd64",
      "manifestMediaType": "m", "layers": [], "sizeBytes": 1, "kind": "service", "entrypoint": null, "command": null, "workdir": null, "user": null,
      "ports": [], "volumes": [ { "name": "cfg", "mountPath": "/app/helper/config/env/{{ container.env.NODE_ENV }}.ts", "description": "" } ],
      "env": [
        { "name": "OLD", "value": "x", "required": false, "secret": false, "description": "" },
        { "name": "LEGACY", "value": ["a", "b"], "required": false, "secret": false, "description": "" },
        { "name": "FREE", "value": "y", "options": null, "required": false, "secret": false, "description": "" },
        { "name": "NODE_ENV", "value": "worker", "options": ["worker", "api"], "required": true, "secret": false, "description": "" },
        { "name": "NODEF", "options": ["p", "q"], "required": false, "secret": false, "description": "" } ],
      "healthcheck": null, "stopTimeoutSec": 10 } },
  "notFoundPages": {} }`
	c := roundTrip[RevisionCatalog](t, "catalog", catalog)
	if c.Services["api"].Icon != nil || *c.Services["kv"].Icon != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatal("icons")
	}
	env := c.Images["api:v1"].Env
	if env[0].Options.Set || !env[2].Options.Set || env[2].Options.Value != nil || env[3].OptionList()[1] != "api" || env[4].Value != nil {
		t.Fatalf("options presence: %+v", env)
	}
	// byte-exact per item: keys absent in the input must stay absent
	for i, want := range []bool{false, false, true, true, true} {
		b, _ := json.Marshal(env[i])
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		if _, has := m["options"]; has != want {
			t.Errorf("env %d: options present=%v want %v (%s)", i, has, want, b)
		}
	}
}

// Round D: site volumes. `volumes` absent stays absent (pre-D revision hashes) and [] stays []; the volume source
// round-trips; the legacy data source is still decoded (and re-encoded unchanged); snapshots written before round D
// carry `name` (read as volumeSlug).
func TestRoundDVolumesRoundTrip(t *testing.T) {
	for _, in := range []string{
		`{"host":null,"agentPort":7080,"agentGuiAliasPort":null,"containers":[],"depends":[],"notFoundPageId":null,"edge":{"httpPort":80,"httpsPort":null,"hosts":[],"routes":[]}}`,
		`{"host":null,"agentPort":7080,"agentGuiAliasPort":null,"volumes":[],"containers":[],"depends":[],"notFoundPageId":null,"edge":{"httpPort":80,"httpsPort":null,"hosts":[],"routes":[]}}`,
		`{"host":null,"agentPort":7080,"agentGuiAliasPort":null,"volumes":[{"volumeSlug":"db-data","label":"DB","description":"","backup":false}],"containers":[],"depends":[],"notFoundPageId":null,"edge":{"httpPort":80,"httpsPort":null,"hosts":[],"routes":[]}}`,
	} {
		var sc SiteConfig
		if err := json.Unmarshal([]byte(in), &sc); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(sc)
		if string(b) != in {
			t.Errorf("round trip\n in %s\nout %s", in, b)
		}
	}
	var sc SiteConfig
	_ = json.Unmarshal([]byte(`{"volumes":[{"volumeSlug":"a","label":"A","description":"","backup":true}]}`), &sc)
	if sc.FindVolume("a") == nil || sc.FindVolume("b") != nil || len(sc.VolumeList()) != 1 {
		t.Error("FindVolume / VolumeList")
	}
	var none SiteConfig
	if none.VolumeList() != nil || none.FindVolume("a") != nil {
		t.Error("nil-safe")
	}
	for _, in := range []string{`{"kind":"volume","volumeSlug":"x-data"}`, `{"kind":"data","name":"x-data"}`} {
		var v VolumeSource
		if err := json.Unmarshal([]byte(in), &v); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(v)
		if string(b) != in || !v.IsVolume() || v.VolumeName() != "x-data" {
			t.Errorf("%s → %s", in, b)
		}
	}
	var h VolumeSource
	_ = json.Unmarshal([]byte(`{"kind":"host","path":"/srv"}`), &h)
	if h.IsVolume() || h.VolumeName() != "" {
		t.Error("host is not a volume")
	}
	var sv SnapshotVolume
	if err := json.Unmarshal([]byte(`{"name":"kv-data","containers":["kv"],"sizeBytes":3,"sha256":"sha256:x","path":"sites/db/volumes/kv-data.tar.zst"}`), &sv); err != nil || sv.VolumeSlug != "kv-data" || sv.SizeBytes != 3 || len(sv.Containers) != 1 {
		t.Fatalf("legacy snapshot volume %+v %v", sv, err)
	}
	b, _ := json.Marshal(sv)
	if !strings.Contains(string(b), `"volumeSlug":"kv-data"`) || strings.Contains(string(b), `"name"`) {
		t.Errorf("snapshot volume %s", b)
	}
	var sv2 SnapshotVolume
	_ = json.Unmarshal([]byte(`{"volumeSlug":"new","containers":[],"sizeBytes":1,"sha256":"sha256:y","path":"p"}`), &sv2)
	if sv2.VolumeSlug != "new" {
		t.Error("volumeSlug")
	}
}

// Round E: a legacy edge decodes normalized, re-encodes byte-identical (revision hash), and an edited one re-encodes in
// the round E shape.
func TestEdgeLegacyNormalizeAndRoundTrip(t *testing.T) {
	var sc SiteConfig
	if err := json.Unmarshal([]byte(siteConfigJSON), &sc); err != nil {
		t.Fatal(err)
	}
	r := sc.Edge.Routes[0]
	if r.Match.Kind != "prefix" || r.Match.Path != "/api" || r.Match.StopRegex || r.Match.Methods != nil {
		t.Fatalf("match %+v", r.Match)
	}
	a := r.Action
	if a.ReadTimeoutSec != 60 || a.ConnectTimeoutSec != 60 || a.SendTimeoutSec != 60 || a.Websocket || a.MaxBodyBytes != nil || a.Rewrite != nil || a.RequestBuffering {
		t.Fatalf("action %+v", a)
	}
	if r.ResponseHeaders != nil || sc.Edge.Hosts[0].ResponseHeaders.Set == nil || sc.Edge.MaxBodyBytes != nil {
		t.Fatal("response headers / maxBody defaults")
	}
	if sc.Edge.Routes[1].Match.Methods[1] != "HEAD" {
		t.Fatal("methods")
	}
	out, _ := json.Marshal(sc.Edge)
	if !strings.Contains(string(out), `"pathPrefix"`) || !strings.Contains(string(out), `"timeoutSec"`) {
		t.Fatalf("legacy bytes not kept: %s", out)
	}
	// legacy timeoutSec 30 → readTimeoutSec 30
	var e Edge
	_ = json.Unmarshal([]byte(`{"httpPort":80,"httpsPort":null,"hosts":[],"routes":[{"routeId":"a","hosts":[],"order":1,"match":{"pathPrefix":"/a","methods":[]},"action":{"type":"proxy","containerSlug":"x","portName":"http","stripPrefix":false,"addPrefix":null,"setHeaders":{"B":"2","A":"1"},"removeHeaders":[],"websocket":true,"timeoutSec":30}}]}`), &e)
	if e.Routes[0].Action.ReadTimeoutSec != 30 || e.HTTPSPort != nil || e.Routes[0].Action.SetHeaders[0].Name != "B" {
		t.Fatalf("%+v", e.Routes[0].Action)
	}
	e.Routes[0].Order = 2
	out, _ = json.Marshal(e)
	if strings.Contains(string(out), `"pathPrefix"`) || !strings.Contains(string(out), `"readTimeoutSec":30`) || !strings.Contains(string(out), `"setHeaders":{"B":"2","A":"1"}`) {
		t.Fatalf("edited edge not in round E shape: %s", out)
	}
	// rewrite action round trip
	roundTrip[EdgeRouteAction](t, "rewrite action", `{"type":"rewrite","regex":"^","replacement":"/x$request_uri"}`)
}

// Round E: SiteVolume kind / seed — absent keys stay absent (older revisions keep their hash), null and values round-trip.
func TestSiteVolumeKindSeedRoundTrip(t *testing.T) {
	for _, in := range []string{
		`{"volumeSlug":"a","label":"A","description":"","backup":true}`,
		`{"volumeSlug":"a","label":"A","description":"","backup":true,"location":{"kind":"auto"},"kind":"dir","seed":null}`,
		`{"volumeSlug":"a","label":"A","description":"","backup":true,"location":{"kind":"auto"},"kind":"dir","seed":{"kind":"files","entries":[{"path":"flows.json","fileSlug":"f"}]}}`,
		`{"volumeSlug":"a","label":"A","description":"","backup":true,"kind":"file","seed":{"kind":"file","fileSlug":"f"}}`,
		`{"volumeSlug":"a","label":"A","description":"","backup":true,"kind":"dir","seed":{"kind":"archive","blobSha256":"sha256:aa","name":"s.tar.gz","size":12}}`,
	} {
		var v SiteVolume
		if err := json.Unmarshal([]byte(in), &v); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(v)
		if string(b) != in {
			t.Errorf("round trip\n in %s\nout %s", in, b)
		}
	}
	var v SiteVolume
	_ = json.Unmarshal([]byte(`{"volumeSlug":"a"}`), &v)
	if v.VolumeKind() != "dir" || v.SeedOf() != nil {
		t.Fatal("defaults")
	}
}
