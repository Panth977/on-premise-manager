// Package exporttest is the §21 #106 fixture shared by the export unit tests and the agent ↔ compose proof test
// (internal/agent/compose_equiv_docker_test.go): one site with Mosquitto (a password file rendered from a secret with
// mosquitto_passwd), two instances of the testsrv echo image (env from a variable, a direct secret, a concatenation and
// a derived secret value), a seeded folder volume (one entry rendered with a secret), a seeded single-file volume,
// http + tcp health checks, explicit + implicit depends, published ports, restart / resources, and edge routes (prefix,
// exact, regex, redirect, rewrite, response headers, body limit, websocket off, not-found page).
package exporttest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"opm/agent/internal/export"
	"opm/agent/internal/model"
)

// Options of the fixture.
type Options struct {
	DeploymentID string
	Site         string
	// Images: the catalog specs (keys "mosquitto:2", "testsrv:1"); nil = synthetic specs (unit tests).
	Images map[string]model.ImageSpec
	// Pull: compose image references by key (default docker.io references)
	Pull      map[string]string
	EdgePort  int // default 8088
	MQTTPort  int // published host port of mqtt (default 21883)
	WebPort   int // published host port of web (default 28080)
	AgentPort int
	AliasPort int
	Prefix    string // container slug prefix (default "cq-")
}

// Fixture is the built fixture: the export request (no secret values), the config WITH the secret values (for the
// agent), the content blobs by hex and the .env values.
type Fixture struct {
	Req     export.Request
	Config  model.Config
	Catalog model.RevisionCatalog
	Blobs   map[string][]byte
	Env     map[string]string
	Slugs   struct{ MQTT, Web, Web2 string }
}

// Secret values of the fixture.
const (
	MQTTPassword = "Mqtt-Secret-Pässwort-2026"
	APIToken     = "tok-$123-abc"
	DBPassword   = "pg pw!x"
)

func ptr[T any](v T) *T { return &v }

func num(f float64) model.NumOrExpr { return model.NumOrExpr{Num: &f} }

func hexOf(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// Build makes the fixture.
func Build(o Options) Fixture {
	if o.DeploymentID == "" {
		o.DeploymentID = "DepComposeEquiv00001"
	}
	if o.Site == "" {
		o.Site = "site-01"
	}
	if o.EdgePort == 0 {
		o.EdgePort = 8088
	}
	if o.MQTTPort == 0 {
		o.MQTTPort = 21883
	}
	if o.WebPort == 0 {
		o.WebPort = 28080
	}
	if o.AgentPort == 0 {
		o.AgentPort = 7080
	}
	if o.Prefix == "" {
		o.Prefix = "cq-"
	}
	fx := Fixture{Blobs: map[string][]byte{}, Env: map[string]string{}}
	fx.Slugs.MQTT, fx.Slugs.Web, fx.Slugs.Web2 = o.Prefix+"mqtt", o.Prefix+"web", o.Prefix+"web-2"
	put := func(b []byte) model.BlobRef {
		h := hexOf(b)
		fx.Blobs[h] = b
		return model.BlobRef{Sha256: "sha256:" + h, SizeBytes: int64(len(b))}
	}
	mosqConf := []byte("listener 1883\nallow_anonymous false\npassword_file /mosquitto/config/pwfile\npersistence false\n")
	pwfile := []byte("northwind:{{ secret.mqtt-password | mosquitto_passwd }}\n")
	appConf := []byte(`{"greeting":"{{ var.greeting }}","mqtt":"{{ containers.` + fx.Slugs.MQTT + `.host }}:{{ containers.` + fx.Slugs.MQTT + `.port.mqtt }}","site":"{{ self.slug }}"}` + "\n")
	hello := []byte("hello from the starter content\n")
	seedConf := []byte(`{"site":"{{ self.slug }}","tokenHash":"{{ secret.api-token | sha256 }}"}` + "\n")
	run := []byte("#!/bin/sh\necho run\n")
	state := []byte("initial state\n")
	page := []byte("<!doctype html><title>gone</title><h1>Custom 404 — {{site}}</h1>\n")
	rMosq, rPw, rApp, rHello, rSeed, rRun, rState := put(mosqConf), put(pwfile), put(appConf), put(hello), put(seedConf), put(run), put(state)

	cat := model.RevisionCatalog{Services: map[string]model.CatalogService{
		"mosquitto": {ServiceSlug: "mosquitto", Label: "Mosquitto"}, "testsrv": {ServiceSlug: "testsrv", Label: "testsrv"}},
		Images: map[string]model.ImageSpec{}, NotFoundPages: map[string]model.CatalogNotFoundPage{"page404xxxxxxxxxxxxx": {NotFoundPageID: "page404xxxxxxxxxxxxx", Label: "gone", HTMLCode: string(page)}}}
	if o.Images != nil {
		for k, v := range o.Images {
			cat.Images[k] = v
		}
	} else {
		cat.Images["mosquitto:2"] = model.ImageSpec{ServiceSlug: "mosquitto", Version: "2", Digest: "sha256:" + hexOf([]byte("m-manifest")), ConfigDigest: "sha256:" + hexOf([]byte("m-config")),
			Platform: "linux/amd64", ManifestMediaType: "application/vnd.oci.image.manifest.v1+json", Layers: []model.ImageLayer{}, Kind: "service",
			Entrypoint: []string{"/docker-entrypoint.sh"}, Command: []string{"/usr/sbin/mosquitto", "-c", "/mosquitto/config/mosquitto.conf"},
			Ports: []model.ImagePort{{Name: "mqtt", Port: num(1883), Protocol: "tcp"}}, Env: []model.ImageEnv{}, Volumes: []model.ImageVolume{}, StopTimeoutSec: 5}
		cat.Images["testsrv:1"] = model.ImageSpec{ServiceSlug: "testsrv", Version: "1", Digest: "sha256:" + hexOf([]byte("t-manifest")), ConfigDigest: "sha256:" + hexOf([]byte("t-config")),
			Platform: "linux/amd64", ManifestMediaType: "application/vnd.oci.image.manifest.v1+json", Layers: []model.ImageLayer{}, Kind: "service",
			Entrypoint: []string{"/testsrv"}, Ports: []model.ImagePort{{Name: "http", Port: num(8080), Protocol: "tcp"}},
			Env: []model.ImageEnv{{Name: "PORT", Value: &model.ImageEnvValue{Str: ptr("8080")}, Description: "listen port"}}, Volumes: []model.ImageVolume{}, StopTimeoutSec: 2}
	}
	// the health checks come from the container (independent of the image)
	httpHC := &model.HealthCheck{Type: "http", PortName: "http", Path: "/healthz", ExpectStatus: 200, IntervalSec: 1, TimeoutSec: 2, Retries: 10, StartPeriodSec: 1}
	tcpHC := &model.HealthCheck{Type: "tcp", PortName: "mqtt", IntervalSec: 1, TimeoutSec: 2, Retries: 10, StartPeriodSec: 1}
	dir, file := "dir", "file"
	cpus, mem := 0.5, 128.0
	files := []model.FileDef{
		{FileSlug: "mosquitto-conf", Label: "mosquitto.conf", Mime: "text/plain", Editor: "text", MaxBytes: 65536, Render: model.Some("none"), Content: &rMosq},
		{FileSlug: "mqtt-pwfile", Label: "Mosquitto password file", Mime: "text/plain", Editor: "text", MaxBytes: 65536, Render: model.Some("expressions"), Content: &rPw},
		{FileSlug: "app-config", Label: "app config", Mime: "application/json", Editor: "text", MaxBytes: 65536, Render: model.Some("expressions"), Content: &rApp},
	}
	m := fx.Slugs.MQTT
	containers := []model.Container{
		{ContainerSlug: m, ServiceSlug: "mosquitto", Version: "2", Enabled: true, Env: []model.ContainerEnv{},
			Ports: []model.ContainerPort{{PortName: "mqtt", HostPort: num(float64(o.MQTTPort)), HostIP: "0.0.0.0", Edge: false}},
			Volumes: []model.ContainerMount{
				{MountPath: ptr("/mosquitto/config/mosquitto.conf"), ReadOnly: true, Source: model.VolumeSource{Kind: "file", FileSlug: "mosquitto-conf"}},
				{MountPath: ptr("/mosquitto/config/pwfile"), ReadOnly: true, Source: model.VolumeSource{Kind: "file", FileSlug: "mqtt-pwfile"}},
			}, Restart: "always", Healthcheck: tcpHC, Depends: []model.ContainerRef{}},
		{ContainerSlug: fx.Slugs.Web, ServiceSlug: "testsrv", Version: "1", Enabled: true,
			Env: []model.ContainerEnv{
				{Name: "NAME", Value: ptr("web")},
				{Name: "GREETING", Value: ptr("{{ var.greeting }} from {{ self.slug }}")},
				{Name: "API_TOKEN", SecretRef: ptr("api-token")},
				{Name: "DB_URL", Value: ptr("postgres://app:{{ secret.db-password }}@db:5432/app?x=$HOME")},
				{Name: "TOKEN_HASH", Value: ptr("{{ secret.api-token | sha256 }}")},
				{Name: "MQTT_URL", Value: ptr("mqtt://{{ containers." + m + ".host }}:{{ containers." + m + ".port.mqtt }}")},
			},
			Ports: []model.ContainerPort{{PortName: "http", HostPort: num(float64(o.WebPort)), HostIP: "127.0.0.1", Edge: true}},
			Volumes: []model.ContainerMount{
				{MountPath: ptr("/data"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "data"}},
				{MountPath: ptr("/state/value.txt"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "state"}},
				{MountPath: ptr("/etc/app/config.json"), ReadOnly: true, Source: model.VolumeSource{Kind: "file", FileSlug: "app-config"}},
				{MountPath: ptr("/run/secrets/db-password"), ReadOnly: true, Source: model.VolumeSource{Kind: "secret", SecretSlug: "db-password"}},
			}, Restart: "always", Resources: model.ContainerResources{Cpus: &cpus, MemoryMb: &mem}, Healthcheck: httpHC, Depends: []model.ContainerRef{}},
		{ContainerSlug: fx.Slugs.Web2, ServiceSlug: "testsrv", Version: "1", Enabled: true,
			Env:   []model.ContainerEnv{{Name: "NAME", Value: ptr("web-2")}},
			Ports: []model.ContainerPort{{PortName: "http", HostIP: "0.0.0.0", Edge: true}},
			Volumes: []model.ContainerMount{
				{MountPath: ptr("/data"), ReadOnly: true, Source: model.VolumeSource{Kind: "volume", VolumeSlug: "data"}},
			}, Restart: "on-failure", Healthcheck: httpHC, Depends: []model.ContainerRef{{ContainerSlug: fx.Slugs.Web}}},
	}
	rh := func(name, value string) *model.EdgeResponseHeaders {
		h := model.EmptyResponseHeaders()
		h.Set = append(h.Set, model.HeaderKV{Name: name, Value: value})
		return &h
	}
	limit := int64(1024)
	route := func(id, kind, path string, order int, a model.EdgeRouteAction) model.EdgeRoute {
		return model.EdgeRoute{RouteID: id, Hosts: []string{}, Match: model.EdgeMatch{Kind: kind, Path: path}, Action: a, Order: order}
	}
	proxy := func(c string, strip bool) model.EdgeRouteAction {
		return model.EdgeRouteAction{Type: "proxy", ContainerSlug: c, PortName: "http", StripPrefix: strip, Websocket: true, SetHeaders: model.HeaderMap{}, RemoveHeaders: []string{},
			ConnectTimeoutSec: 60, SendTimeoutSec: 60, ReadTimeoutSec: 60}
	}
	api := proxy(fx.Slugs.Web, true)
	api.SetHeaders = model.HeaderMap{{Name: "X-Site", Value: "{{ self.slug }}"}}
	small := proxy(fx.Slugs.Web, false)
	small.MaxBodyBytes = &limit
	nows := proxy(fx.Slugs.Web2, false)
	nows.Websocket = false
	regex := proxy(fx.Slugs.Web2, false)
	routes := []model.EdgeRoute{
		route("app", "prefix", "/app/", 1, proxy(fx.Slugs.Web, false)),
		route("api", "prefix", "/api/", 2, api),
		route("exact", "exact", "/exact", 3, proxy(fx.Slugs.Web2, false)),
		route("items", "regex", `^/items/(\d+)$`, 4, regex),
		route("old", "prefix", "/old/", 5, model.EdgeRouteAction{Type: "redirect", To: "/new/", Status: 301, KeepPath: true}),
		route("rw", "regex", `^/rw/(.*)$`, 6, model.EdgeRouteAction{Type: "rewrite", Regex: `^/rw/(.*)$`, Replacement: "/api/$1"}),
		route("small", "prefix", "/small/", 7, small),
		route("nows", "prefix", "/nows/", 8, nows),
	}
	routes[2].ResponseHeaders = rh("X-Exact", "yes")
	hostRH := model.EmptyResponseHeaders()
	hostRH.Set = model.HeaderMap{{Name: "X-Frame-Options", Value: "SAMEORIGIN"}, {Name: "X-OPM-Site", Value: "{{ self.slug }}"}}
	pageID := "page404xxxxxxxxxxxxx"
	sc := model.SiteConfig{Host: nil, AgentPort: o.AgentPort, AgentGuiAliasPort: nil,
		Volumes: &[]model.SiteVolume{
			{VolumeSlug: "data", Label: "data", Backup: true, Kind: &dir, Seed: model.Some(model.VolumeSeed{Kind: "tree",
				Tree: []model.VolumeTreeEntry{
					{Path: "config/site.json", Sha256: rSeed.Sha256, Size: rSeed.SizeBytes, Mode: "0644", Render: "expressions"},
					{Path: "hello.txt", Sha256: rHello.Sha256, Size: rHello.SizeBytes, Mode: "0644", Render: "none"},
					{Path: "bin/run.sh", Sha256: rRun.Sha256, Size: rRun.SizeBytes, Mode: "0755", Render: "none"},
				}, Dirs: []string{"empty"}})},
			{VolumeSlug: "state", Label: "state", Backup: true, Kind: &file, Seed: model.Some(model.VolumeSeed{Kind: "single", Sha256: rState.Sha256, Size: rState.SizeBytes, Mode: "0644", Render: "none"})},
		},
		Containers: containers, Depends: []model.SiteRef{}, NotFoundPageID: &pageID,
		Edge: model.Edge{HTTPPort: o.EdgePort, HTTPSPort: nil, Hosts: []model.EdgeHost{{Hostname: "*", TLS: model.EdgeTls{Mode: "none"}, ResponseHeaders: hostRH}}, Routes: routes}}
	if o.AliasPort != 0 {
		sc.AgentGuiAliasPort = &o.AliasPort
	}
	cfg := model.Config{
		Variables: []model.Variable{{VariableSlug: "greeting", Label: "Greeting", Type: "string", Value: model.VariableValue{V: "Hello"}}},
		Secrets: []model.SecretDef{
			{SecretSlug: "mqtt-password", Label: "MQTT password", Description: "password of the northwind MQTT user", Required: true, Value: ptr(MQTTPassword)},
			{SecretSlug: "api-token", Label: "API token", Required: true, Value: ptr(APIToken)},
			{SecretSlug: "db-password", Label: "DB password", Required: true, Generate: &model.SecretGenerate{Charset: "alnum", Length: 24}, Value: ptr(DBPassword)},
		},
		Files: files,
		Sites: []model.Site{{SiteSlug: o.Site, Label: "Site 01", Config: sc}},
	}
	fx.Config = cfg
	fx.Catalog = cat
	fx.Env = map[string]string{"OPM_SECRET_MQTT_PASSWORD": MQTTPassword, "OPM_SECRET_API_TOKEN": APIToken, "OPM_SECRET_DB_PASSWORD": DBPassword}
	pull := map[string]string{"mosquitto:2": "docker.io/library/eclipse-mosquitto:2", "testsrv:1": "opm-edge-testsrv:1"}
	for k, v := range o.Pull {
		pull[k] = v
	}
	imgs := map[string]export.ImageRef{}
	for k, v := range pull {
		imgs[k] = export.ImageRef{Pull: v, Platform: cat.Images[k].Platform}
	}
	texts := map[string]string{}
	for _, r := range []model.BlobRef{rPw, rApp, rSeed} {
		texts[r.Sha256[7:]] = string(fx.Blobs[r.Sha256[7:]])
	}
	seq := 1
	fx.Req = export.Request{Format: export.Format,
		Branch:       export.BranchInfo{Kind: "deployment", GitBranch: "deployment/compose-equiv/cloud", EntitySlug: "compose-equiv", Label: "Compose equivalence", DeploymentID: ptr(o.DeploymentID)},
		Commit:       export.CommitInfo{ID: "01JCOMPOSEEQUIV0000000000", Seq: &seq, Kind: "change", Message: "proof fixture\n\nmosquitto + 2 testsrv + seeded volumes + edge", Author: export.Person{Name: "Ops", Email: "ops@example.com"}, CreatedAt: "2026-10-09T10:00:00.000Z"},
		DeploymentID: o.DeploymentID, Config: cfg, SecretsSet: []string{"api-token", "db-password", "mqtt-password"}, Catalog: cat, Images: imgs, Texts: texts,
		Sites: []string{o.Site}, Persist: map[string]map[string]export.PersistSnapshot{}, Tool: export.Tool{Version: "test", Amd64: &export.ToolBin{Sha256: "sha256:" + hexOf([]byte("amd64")), Size: 1}, Arm64: &export.ToolBin{Sha256: "sha256:" + hexOf([]byte("arm64")), Size: 1}}}
	_ = strconv.Itoa
	return fx
}

// WriteTree materialises an export result into dir (like a git checkout of the branch): blobs from blobs[hex], tool
// binaries from tools["linux-amd64"|"linux-arm64"] (missing → a stub that fails).
func WriteTree(res export.Result, dir string, blobs map[string][]byte, tools map[string][]byte) error {
	for _, f := range res.Files {
		p := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		var b []byte
		switch {
		case f.Text != nil:
			b = []byte(*f.Text)
		case f.Base64 != nil:
			x, err := base64.StdEncoding.DecodeString(*f.Base64)
			if err != nil {
				return err
			}
			b = x
		case f.Blob != nil:
			x, ok := blobs[*f.Blob]
			if !ok {
				return fmt.Errorf("%s: blob %s missing", f.Path, *f.Blob)
			}
			b = x
		case f.Tool != nil:
			x, ok := tools[*f.Tool]
			if !ok {
				x = []byte("#!/bin/sh\necho opm-render " + *f.Tool + " not built >&2; exit 1\n")
			}
			b = x
		}
		mode := os.FileMode(0o644)
		if f.Mode == "100755" {
			mode = 0o755
		}
		if err := os.WriteFile(p, b, mode); err != nil {
			return err
		}
		_ = os.Chmod(p, mode)
	}
	return nil
}

// Tree is a printable tree of the result paths.
func Tree(res export.Result) string {
	var b strings.Builder
	for _, f := range res.Files {
		depth := strings.Count(f.Path, "/")
		name := f.Path[strings.LastIndex(f.Path, "/")+1:]
		kind := "text"
		switch {
		case f.Blob != nil:
			kind = "blob"
		case f.Tool != nil:
			kind = "tool " + *f.Tool
		case f.Base64 != nil:
			kind = "binary"
		}
		fmt.Fprintf(&b, "%s%s  (%d B, %s)\n", strings.Repeat("  ", depth), f.Path[:len(f.Path)-len(name)]+name, f.Size, kind)
	}
	return b.String()
}
