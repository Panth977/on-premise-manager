package apitest

import (
	"bytes"
	"encoding/json"
	"errors"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
)

// vsCheck runs core.VerifySchema and keeps the lines that start with one of the paths.
func vsCheck(t *testing.T, o core.VerifyOptions, mine ...string) []string {
	t.Helper()
	all, err := core.VerifySchema(DB, o)
	NoErr(t, err)
	out := []string{}
	for _, l := range all {
		if slices.ContainsFunc(mine, func(p string) bool { return strings.HasPrefix(l, p) }) {
			out = append(out, l)
		}
	}
	return out
}

func vsSome(t *testing.T, lines []string, re string) {
	t.Helper()
	r := regexp.MustCompile(re)
	if !slices.ContainsFunc(lines, r.MatchString) {
		t.Fatalf("no line matches %s:\n%s", re, strings.Join(lines, "\n"))
	}
}

func vsNone(t *testing.T, lines []string, re string) {
	t.Helper()
	r := regexp.MustCompile(re)
	if i := slices.IndexFunc(lines, r.MatchString); i >= 0 {
		t.Fatalf("%s matched %s", lines[i], re)
	}
}

// vsLine: this exact violation line is reported.
func vsLine(t *testing.T, lines []string, want string) {
	t.Helper()
	if !slices.Contains(lines, want) {
		t.Fatalf("missing line %q:\n%s", want, strings.Join(lines, "\n"))
	}
}

func vsClean(t *testing.T, lines []string) {
	t.Helper()
	if len(lines) != 0 {
		t.Fatalf("expected no violation:\n%s", strings.Join(lines, "\n"))
	}
}

// vsSnapshot keeps documents as they are; the returned func (also run at cleanup) writes them back. The legacy shapes
// stay inside one subtest: later tests and the daily job read the same data dir.
func vsSnapshot(t *testing.T, paths ...string) func() {
	t.Helper()
	saved := map[string]*doc.Map{}
	for _, p := range paths {
		s, err := DB.Doc(p).Get()
		NoErr(t, err)
		True(t, s.Exists(), p)
		saved[p] = s.Data()
	}
	restore := func() {
		for p, d := range saved {
			NoErr(t, DB.Doc(p).Set(d))
		}
	}
	t.Cleanup(restore)
	return restore
}

// vsDrop deletes a collection, the returned func (also run at cleanup) writes its documents back.
func vsDrop(t *testing.T, path string) func() {
	t.Helper()
	q, err := DB.Collection(path).Get()
	NoErr(t, err)
	saved := map[string]*doc.Map{}
	for _, d := range q.Docs {
		saved[d.Path()] = d.Data()
		NoErr(t, d.Ref().Delete())
	}
	restore := func() {
		for p, d := range saved {
			NoErr(t, DB.Doc(p).Set(d))
		}
	}
	t.Cleanup(restore)
	return restore
}

func vsDelete(t *testing.T, paths ...string) {
	t.Cleanup(func() {
		for _, p := range paths {
			NoErr(t, DB.Doc(p).Delete())
		}
	})
}

func vsJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	NoErr(t, err)
	return string(b)
}

func vsConfigOf(d J, field string) J { return ParseJ([]byte(d.At(field).Str())) }

// vsPublished is a hello deployment with one revision, and the documents the legacy tests rewrite.
type vsPublished struct {
	HelloDep
	tpl, dep, rev string
}

func vsHello(t *testing.T, admin *Person) vsPublished {
	t.Helper()
	h := HelloDeployment(t, admin)
	p := Publish(t, admin.API, h.ID(), 0, "r1")
	revID := p.Revision.At("revId").Str()
	True(t, revID != "", "published")
	return vsPublished{h, "templates/" + h.TemplateID, "deployments/" + h.ID(), "deployments/" + h.ID() + "/revisions/" + revID}
}

func (p vsPublished) mine() []string { return []string{p.tpl, p.dep} }

// rewrite replaces the template body, draft, srcTemplate snapshot and revision config by legacy(config).
func (p vsPublished) rewrite(t *testing.T, legacy func(J) J) {
	t.Helper()
	td := Doc(t, p.tpl)
	UpdateDoc(t, p.tpl, A{"_body": vsJSON(t, legacy(vsConfigOf(td, "_body")).V)})
	dd := Doc(t, p.dep)
	UpdateDoc(t, p.dep, A{"_draft": vsJSON(t, legacy(vsConfigOf(dd, "_draft")).V), "_srcSnapshot": vsJSON(t, legacy(vsConfigOf(dd, "_srcSnapshot")).V)})
	rev := legacy(vsConfigOf(Doc(t, p.rev), "_config"))
	h, err := cloud.JcsHash(rev.V)
	NoErr(t, err)
	UpdateDoc(t, p.rev, A{"_config": vsJSON(t, rev.V), "hash": h})
}

func vsApp(cfg J) A { return cfg.At("sites").Find("siteSlug", "app").Map() }

func vsAppConfig(cfg J) A { return vsApp(cfg)["config"].(A) }

// vsLegacyContainers: one container per service, port / volume instead of ports / volumes, no containerSlug; files
// without render; host ""; proxy actions naming serviceSlug; no site volumes.
func vsLegacyContainers(c J) J {
	cfg := c.Clone()
	for _, f := range cfg.At("files").Arr() {
		delete(f.Map(), "render")
	}
	sc := vsAppConfig(cfg)
	delete(sc, "volumes")
	sc["host"] = ""
	for _, ct := range (J{sc}).At("containers").Arr() {
		m := ct.Map()
		port, volume := L{}, L{}
		for _, p := range ct.At("ports").Arr() {
			port = append(port, A{"portName": p.At("portName").V, "hostPort": p.At("hostPort").V, "protocol": "tcp"})
		}
		for _, v := range ct.At("volumes").Arr() {
			if v.At("imageVolume").Truthy() {
				volume = append(volume, A{"kind": "data", "name": v.At("imageVolume").V, "mountPath": "/data"})
			} else if v.At("source.kind").Str() == "file" {
				volume = append(volume, A{"kind": "file", "fileSlug": v.At("source.fileSlug").V, "mountPath": v.At("mountPath").V})
			}
		}
		out := A{"port": port, "volume": volume, "depends": L{}}
		for _, d := range ct.At("depends").Arr() {
			out["depends"] = append(out["depends"].(L), A{"serviceSlug": d.At("containerSlug").V})
		}
		for _, k := range []string{"containerSlug", "ports", "volumes", "depends"} {
			delete(m, k)
		}
		for k, v := range out {
			m[k] = v
		}
	}
	for _, r := range (J{sc}).At("edge.routes").Arr() {
		if a := r.At("action"); a.At("type").Str() == "proxy" {
			a.Map()["serviceSlug"] = a.At("containerSlug").V
			delete(a.Map(), "containerSlug")
		}
	}
	return cfg
}

// vsNoDeclaredVolumes: no `volumes` list; hello-api mounts its image volume through the old `data` source, read-only;
// implicit = hello-api has no row for it.
func vsNoDeclaredVolumes(implicit bool) func(J) J {
	return func(c J) J {
		cfg := c.Clone()
		sc := vsAppConfig(cfg)
		delete(sc, "volumes")
		api := (J{sc}).At("containers").Find("containerSlug", "hello-api").Map()
		api["volumes"] = L{A{"imageVolume": "data", "mountPath": nil, "source": A{"kind": "data", "name": "hello-api-data"}, "readOnly": true}}
		if implicit {
			api["volumes"] = L{}
		}
		return cfg
	}
}

func vsNoLocations(c J) J {
	cfg := c.Clone()
	for _, f := range cfg.At("files").Arr() {
		delete(f.Map(), "location")
	}
	for _, s := range cfg.At("sites").Arr() {
		for _, v := range s.At("config.volumes").Arr() {
			delete(v.Map(), "location")
		}
	}
	return cfg
}

// vsLegacyEdge: {pathPrefix} matches, proxy timeoutSec, no responseHeaders / maxBodyBytes; site volumes without kind / seed.
func vsLegacyEdge(c J) J {
	cfg := c.Clone()
	sc := vsAppConfig(cfg)
	for _, v := range (J{sc}).At("volumes").Arr() {
		delete(v.Map(), "kind")
		delete(v.Map(), "seed")
	}
	e := (J{sc}).At("edge")
	delete(e.Map(), "maxBodyBytes")
	for _, h := range e.At("hosts").Arr() {
		delete(h.Map(), "responseHeaders")
	}
	for _, r := range e.At("routes").Arr() {
		m := r.Map()
		delete(m, "responseHeaders")
		m["match"] = A{"pathPrefix": r.At("match.path").V, "methods": r.At("match.methods").V}
		if a := r.At("action"); a.At("type").Str() == "proxy" {
			pa := a.Map()
			pa["timeoutSec"] = a.At("readTimeoutSec").V
			for _, k := range []string{"connectTimeoutSec", "sendTimeoutSec", "readTimeoutSec", "rewrite", "requestBuffering", "responseBuffering", "maxBodyBytes"} {
				delete(pa, k)
			}
		}
	}
	return cfg
}

func vsOldImage(now, serviceID, tag, registryID string, extra A) A {
	m := A{
		"tag": tag, "serviceId": serviceID, "registryId": registryID, "digest": "sha256:" + strings.Repeat("b", 64), "platform": "linux/amd64",
		"import": A{"mode": "copy", "status": "ready", "error": nil, "sizeBytes": 1, "manifestMediaType": "application/vnd.oci.image.manifest.v1+json", "configDigest": "sha256:" + strings.Repeat("c", 64), "layers": L{}, "startedAt": now, "finishedAt": now},
		"kind":   "service", "entrypoint": nil, "command": nil, "workdir": nil, "user": nil,
		"ports":       L{A{"name": "http", "port": 80, "protocol": "tcp", "description": ""}},
		"env":         L{A{"name": "NODE_ENV", "value": L{"a", "b"}, "required": false, "secret": false, "description": ""}},
		"volumes":     L{},
		"healthcheck": nil, "stopTimeoutSec": 30, "description": "", "createdAt": now, "createdBy": "test", "updatedAt": now, "updatedBy": "test",
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// TestVerifySchema: each legacy document shape is reported, with the same lines as before; the current shape reports
// nothing. The legacy documents and their assertions come from the tests of the data migrations: 10-phase-u, 11-round-c,
// 12-round-d, 13-round-e, 15-volume-kind, 16-reveal, 29-migrate-i, 31-migrate-j.
func TestVerifySchema(t *testing.T) {
	none := core.VerifyOptions{}
	all := core.VerifyOptions{RequireBranches: true, RequireNoWorkingCopy: true}

	t.Run("old services, images, serviceSlugs, serviceId containers and catalogs are reported; the current shape is not", func(t *testing.T) {
		admin := NewPerson(t, "admin") // a person is rate limited
		h := vsHello(t, admin)
		sfx := Uniq("")
		pad := func(s string) string { return (s + strings.Repeat("0", 20))[:20] }
		regID, oldA, oldB, slugA, slugB := pad("RegMig"+sfx), pad("OldA"+sfx), pad("OldB"+sfx), Uniq("mga"), Uniq("mgb")
		now := Now()
		audit := func(m A) A {
			m["createdAt"], m["createdBy"], m["updatedAt"], m["updatedBy"] = now, "test", now, "test"
			return m
		}
		regP, aP, bP := "registries/"+regID, "services/"+oldA, "services/"+oldB
		vsDelete(t, regP, aP, aP+"/images/1.0", aP+"/images/Latest_Build", bP, bP+"/images/7", "serviceSlugs/"+slugA)
		SetDoc(t, regP, audit(A{"registryId": regID, "label": "Hub (mig)", "url": "https://registry-1.docker.io", "username": "u", "password": "p", "lastCheck": nil}))
		SetDoc(t, aP, audit(A{"serviceId": oldA, "slug": slugA, "label": "Mig A", "icon": "box", "repository": "acme/api", "description": ""}))
		SetDoc(t, aP+"/images/1.0", vsOldImage(now, oldA, "1.0", regID, nil))
		SetDoc(t, aP+"/images/Latest_Build", vsOldImage(now, oldA, "Latest_Build", regID, A{"createdAt": "2020-01-01T00:00:00.000Z"}))
		SetDoc(t, bP, audit(A{"serviceId": oldB, "slug": slugB, "label": "Mig B", "icon": "https://x.example/i.png", "repository": "redis", "description": ""}))
		SetDoc(t, bP+"/images/7", vsOldImage(now, oldB, "7", regID, nil))
		SetDoc(t, "serviceSlugs/"+slugA, A{"serviceId": oldA})

		legacy := func(id, tag string) A {
			return A{"serviceId": id, "tag": tag, "enabled": true, "env": L{}, "volume": L{}, "port": L{A{"portName": "http", "hostPort": nil, "protocol": "tcp"}}, "command": nil, "restart": "always", "resources": A{"cpus": nil, "memoryMb": nil}, "depends": L{}}
		}
		restore := vsSnapshot(t, h.tpl, h.dep, h.rev)
		cfgOf := func(routes L) J {
			b := legacy(oldB, "7")
			b["depends"] = L{A{"serviceId": oldA}}
			c := vsConfigOf(Doc(t, h.tpl), "_body").Clone()
			sc := vsAppConfig(c)
			sc["containers"] = L{legacy(oldA, "1.0"), b}
			(J{sc}).At("edge").Map()["routes"] = routes
			return c
		}
		cfg := cfgOf(L{})
		UpdateDoc(t, h.tpl, A{"_body": vsJSON(t, cfg.V), "icon": "https://x.example/i.png"})
		UpdateDoc(t, h.dep, A{"_draft": vsJSON(t, cfg.V), "image": "rocket"})
		revCfg := cfgOf(L{A{"match": A{"pathPrefix": "/"}, "action": A{"type": "proxy", "serviceId": oldA, "portName": "http"}}})
		revCat := A{
			"services":      A{oldA: A{"serviceId": oldA, "slug": slugA, "label": "Mig A", "icon": "box"}, oldB: A{"serviceId": oldB, "slug": slugB, "label": "Mig B", "icon": nil}},
			"images":        A{oldA + ":1.0": A{"serviceId": oldA, "serviceSlug": slugA, "tag": "1.0", "digest": "sha256:" + strings.Repeat("b", 64)}},
			"notFoundPages": A{},
		}
		rh, err := cloud.JcsHash(revCfg.V)
		NoErr(t, err)
		ch, err := cloud.JcsHash(revCat)
		NoErr(t, err)
		UpdateDoc(t, h.rev, A{"_config": vsJSON(t, revCfg.V), "_catalog": vsJSON(t, revCat), "hash": rh, "catalogHash": ch})

		v0 := vsCheck(t, none, aP, bP, regP, "serviceSlugs/"+slugA, h.tpl, h.dep)
		vsLine(t, v0, aP+": doc id is not a service slug")
		vsLine(t, v0, aP+": serviceSlug ≠ doc id")
		for _, k := range []string{"serviceId", "slug", "repository"} {
			vsLine(t, v0, aP+": obsolete field "+k)
		}
		vsLine(t, v0, bP+": icon must be null, lucide:<name> or upload:<iconId>")
		ia := aP + "/images/1.0"
		vsLine(t, v0, ia+": version ≠ doc id")
		vsLine(t, v0, ia+": serviceSlug ≠ parent")
		for _, k := range []string{"serviceId", "tag"} {
			vsLine(t, v0, ia+": obsolete field "+k)
		}
		vsSome(t, v0, "^"+regexp.QuoteMeta(ia)+`: reference: enter an image reference`)
		vsLine(t, v0, ia+": env NODE_ENV: options missing (normalize)")
		vsLine(t, v0, ia+": env NODE_ENV: legacy list value")
		vsLine(t, v0, ia+": env NODE_ENV: not normalized")
		vsLine(t, v0, aP+"/images/Latest_Build: doc id is not a version")
		vsLine(t, v0, "serviceSlugs/"+slugA+": obsolete collection")
		vsLine(t, v0, h.tpl+": contains serviceId (old shape)")
		vsLine(t, v0, h.tpl+": icon must be null, lucide:<name> or upload:<iconId>")
		vsLine(t, v0, h.dep+": must be null, lucide:<name>, upload:<iconId> or an https URL")
		vsLine(t, v0, h.dep+" draft: contains serviceId (old shape)")
		vsLine(t, v0, h.dep+" draft: site app container undefined: containerSlug missing / invalid (legacy one-per-service shape)")
		vsLine(t, v0, h.dep+" draft: site app container undefined: depends entry must be {containerSlug}")
		vsLine(t, v0, h.rev+": contains serviceId (old shape)")
		vsLine(t, v0, h.rev+" catalog: contains serviceId (old shape)")
		vsLine(t, v0, h.rev+": catalog service "+oldA+": serviceSlug ≠ key")
		vsLine(t, v0, h.rev+": catalog image "+oldA+":1.0: serviceSlug:version ≠ key")
		vsLine(t, v0, h.rev+": site app: container without serviceSlug / version")

		// the current shape of the same data
		restore()
		for _, p := range []string{aP + "/images/1.0", aP + "/images/Latest_Build", bP + "/images/7", aP, bP, "serviceSlugs/" + slugA} {
			NoErr(t, DB.Doc(p).Delete())
		}
		saP, sbP := "services/"+slugA, "services/"+slugB
		vsDelete(t, saP, saP+"/images/1.0", sbP)
		SetDoc(t, saP, audit(A{"serviceSlug": slugA, "label": "Mig A", "icon": "lucide:box", "description": ""}))
		SetDoc(t, sbP, audit(A{"serviceSlug": slugB, "label": "Mig B", "icon": nil, "description": ""}))
		img := vsOldImage(now, oldA, "1.0", regID, A{
			"version": "1.0", "serviceSlug": slugA, "reference": "docker.io/acme/api:1.0",
			"source": A{"host": "docker.io", "repository": "acme/api", "tag": "1.0", "digest": nil},
			"env":    L{A{"name": "NODE_ENV", "value": "a", "options": L{"a", "b"}, "required": false, "secret": false, "description": ""}},
		})
		delete(img, "tag")
		delete(img, "serviceId")
		SetDoc(t, saP+"/images/1.0", img)
		vsClean(t, vsCheck(t, none, saP, sbP, regP, h.tpl, h.dep))
	})

	t.Run("one-per-service containers, files without render, host \"\" and serviceSlug proxies are reported in every config", func(t *testing.T) {
		admin := NewPerson(t, "admin") // a person is rate limited
		h := vsHello(t, admin)
		restore := vsSnapshot(t, h.tpl, h.dep, h.rev)
		h.rewrite(t, vsLegacyContainers)
		v0 := vsCheck(t, none, h.mine()...)
		vsSome(t, v0, `containerSlug missing`)
		vsSome(t, v0, `render missing`)
		vsSome(t, v0, `host ""`)
		vsSome(t, v0, `proxy action must name containerSlug`)
		for _, p := range []string{h.tpl + ":", h.dep + " draft:", h.dep + " srcTemplate:", h.rev + ":"} {
			vsLine(t, v0, p+` file banner: render missing / invalid (null)`)
			vsLine(t, v0, p+` site app: host "" (must be null = auto)`)
			vsLine(t, v0, p+" site app container hello-api: containerSlug missing / invalid (legacy one-per-service shape)")
			vsLine(t, v0, p+" site app container hello-api: legacy field port")
			vsLine(t, v0, p+" site app container hello-api: legacy field volume")
			vsLine(t, v0, p+" site app container hello-api: ports must be a list")
			vsLine(t, v0, p+" site app container hello-api: volumes must be a list")
			vsLine(t, v0, p+" site app container hello-web: depends entry must be {containerSlug}")
			vsLine(t, v0, p+" site app route api: proxy action must name containerSlug (not serviceSlug)")
		}
		restore()
		vsClean(t, vsCheck(t, none, h.mine()...))
	})

	t.Run("site configs without a volumes list and data mounts are reported, revisions included", func(t *testing.T) {
		admin := NewPerson(t, "admin") // a person is rate limited
		h := vsHello(t, admin)
		restore := vsSnapshot(t, h.tpl, h.dep, h.rev)
		h.rewrite(t, vsNoDeclaredVolumes(false))
		v0 := vsCheck(t, none, h.mine()...)
		vsSome(t, v0, `volumes list missing`)
		vsSome(t, v0, `legacy source kind data`)
		vsSome(t, v0, "^"+regexp.QuoteMeta(h.dep+"/revisions/")+`.*volumes list missing`)
		vsLine(t, v0, h.tpl+": site app: volumes list missing (pre-round-D site config)")
		vsLine(t, v0, h.tpl+": site app container hello-api image volume data: legacy source kind data (hello-api-data) — must be a declared volume")
		vsNone(t, v0, `V_REQUIRED|V_VOLUME_UNDECLARED|V_READONLY|"data"`)
		restore()
		h.rewrite(t, vsNoDeclaredVolumes(true))
		vsLine(t, vsCheck(t, none, h.tpl), h.tpl+": site app: volumes list missing (pre-round-D site config)")
		restore()
		vsClean(t, vsCheck(t, none, h.mine()...))
	})

	t.Run("files and site volumes without a location are reported, revisions included", func(t *testing.T) {
		admin := NewPerson(t, "admin") // a person is rate limited
		h := vsHello(t, admin)
		restore := vsSnapshot(t, h.tpl, h.dep, h.rev)
		h.rewrite(t, vsNoLocations)
		v0 := vsCheck(t, none, h.mine()...)
		vsSome(t, v0, `file banner: location missing`)
		vsSome(t, v0, `volume hello-api-data: location missing`)
		vsSome(t, v0, "^"+regexp.QuoteMeta(h.dep+"/revisions/")+`.*location missing`)
		vsLine(t, v0, h.tpl+": file banner: location missing")
		vsLine(t, v0, h.tpl+": site app volume hello-api-data: location missing")
		restore()
		vsClean(t, vsCheck(t, none, h.mine()...))
	})

	t.Run("legacy edges and site volumes without kind / seed are reported once, in every config", func(t *testing.T) {
		admin := NewPerson(t, "admin") // a person is rate limited
		h := vsHello(t, admin)
		restore := vsSnapshot(t, h.tpl, h.dep, h.rev)
		h.rewrite(t, vsLegacyEdge)
		v0 := vsCheck(t, none, h.mine()...)
		for _, p := range []string{h.tpl + ":", h.dep + " draft:", h.dep + " srcTemplate:", h.dep + "/revisions/"} {
			vsSome(t, v0, "^"+regexp.QuoteMeta(p)+`.*route api: legacy match \{pathPrefix\}`)
		}
		vsSome(t, v0, `legacy proxy timeoutSec`)
		vsSome(t, v0, `edge maxBodyBytes missing`)
		vsSome(t, v0, `host \*: responseHeaders`)
		vsSome(t, v0, `route old: responseHeaders missing`)
		vsSome(t, v0, `volume hello-api-data: kind missing`)
		vsSome(t, v0, `volume hello-api-data: seed missing`)
		vsNone(t, v0, `V_ENUM|V_RANGE|V_TYPE|V_VOLUME_KIND`)
		vsLine(t, v0, h.tpl+": site app: edge maxBodyBytes missing")
		vsLine(t, v0, h.tpl+": site app route api: legacy match {pathPrefix} (must be {kind, path, stopRegex})")
		vsLine(t, v0, h.tpl+": site app route api: legacy proxy timeoutSec (must be readTimeoutSec)")
		vsLine(t, v0, h.tpl+": site app volume hello-api-data: kind missing / invalid (null)")
		restore()
		vsClean(t, vsCheck(t, none, h.mine()...))
	})

	t.Run("image volumes without kind are reported", func(t *testing.T) {
		slug := Uniq("nr")
		ip := "services/" + slug + "/images/v1"
		vsDelete(t, "services/"+slug, ip)
		now := "2026-10-01T00:00:00.000Z"
		audit := func(m A) A {
			m["createdAt"], m["createdBy"], m["updatedAt"], m["updatedBy"] = now, "test", now, "test"
			return m
		}
		SetDoc(t, "services/"+slug, audit(A{"serviceSlug": slug, "label": slug, "icon": nil, "description": ""}))
		hx := func(k string) string { return Sha256Hex(slug + ":" + k) }
		SetDoc(t, ip, audit(A{
			"version": "v1", "serviceSlug": slug, "reference": "docker.io/nodered/node-red:4.0",
			"source":     A{"host": "docker.io", "repository": "nodered/node-red", "tag": "4.0", "digest": nil},
			"registryId": nil, "digest": "sha256:" + hx("m"), "platform": "linux/amd64",
			"import": A{"mode": "copy", "status": "ready", "error": nil, "sizeBytes": 100, "manifestMediaType": "application/vnd.oci.image.manifest.v1+json", "configDigest": "sha256:" + hx("c"), "layers": L{A{"digest": "sha256:" + hx("l"), "sizeBytes": 100, "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip"}}, "startedAt": now, "finishedAt": now},
			"kind":   "service", "entrypoint": nil, "command": nil, "workdir": nil, "user": nil,
			"ports": L{A{"name": "http", "port": 1880, "protocol": "tcp", "description": ""}},
			"env":   L{},
			"volumes": L{
				A{"name": "flow", "mountPath": "/data/flows.json", "description": ""},
				A{"name": "settings", "mountPath": "/data/settings.js", "description": ""},
				A{"name": "lib", "mountPath": "/data/lib", "description": ""},
			},
			"healthcheck": nil, "stopTimeoutSec": 30, "description": "",
		}))
		vs0 := vsCheck(t, none, ip)
		vsSome(t, vs0, `volume flow: kind missing`)
		vsLine(t, vs0, ip+": volume flow: kind missing / not dir | file")
		vsLine(t, vs0, ip+": volume lib: kind missing / not dir | file")
		UpdateDoc(t, ip, A{"volumes": L{
			A{"name": "flow", "mountPath": "/data/flows.json", "kind": "file", "description": ""},
			A{"name": "settings", "mountPath": "/data/settings.js", "kind": "file", "description": ""},
			A{"name": "lib", "mountPath": "/data/lib", "kind": "dir", "description": ""},
		}})
		vsClean(t, vsCheck(t, none, ip))
	})

	t.Run("missing stored-credential fields are reported; null is valid", func(t *testing.T) {
		admin := NewPerson(t, "admin") // a person is rate limited
		d := HelloDeployment(t, admin)
		id := d.ID()
		tok := admin.API.Call(t, "createInstallToken", A{"deploymentId": id, "body": A{"siteSlug": "app"}})
		other := NewPerson(t, L{})
		dp, tp, ap := "deployments/"+id, "deployments/"+id+"/installTokens/"+tok.At("tokenHash").Str(), "accounts/"+other.UID
		UpdateDoc(t, dp, A{"accessKey": doc.DeleteField()})
		UpdateDoc(t, tp, A{"token": doc.DeleteField()})
		UpdateDoc(t, ap, A{"lastSetPassword": doc.DeleteField(), "lastResetLink": doc.DeleteField()})
		v0 := vsCheck(t, none, dp, ap)
		vsSome(t, v0, `accessKey missing`)
		vsSome(t, v0, `token missing`)
		vsSome(t, v0, `lastSetPassword missing`)
		vsSome(t, v0, `lastResetLink missing`)
		vsLine(t, v0, dp+": accessKey missing")
		vsLine(t, v0, tp+": token missing")
		vsLine(t, v0, ap+": lastSetPassword missing")
		vsLine(t, v0, ap+": lastResetLink missing")
		UpdateDoc(t, dp, A{"accessKey": nil})
		UpdateDoc(t, tp, A{"token": nil})
		UpdateDoc(t, ap, A{"lastSetPassword": nil, "lastResetLink": nil})
		vsClean(t, vsCheck(t, none, dp, ap))
	})

	t.Run("a template or deployment without branch is reported with RequireBranches; commit chains are checked", func(t *testing.T) {
		admin := NewPerson(t, "admin") // a person is rate limited
		d := vsHello(t, admin)
		restore := vsSnapshot(t, d.tpl, d.dep)
		var back []func()
		for _, sub := range []string{d.dep + "/commits", d.dep + "/mergeBases", d.dep + "/agentBranches", d.tpl + "/commits"} {
			back = append(back, vsDrop(t, sub))
		}
		UpdateDoc(t, d.dep, A{"branch": doc.DeleteField()})
		UpdateDoc(t, d.tpl, A{"branch": doc.DeleteField()})
		v0 := vsCheck(t, core.VerifyOptions{RequireBranches: true}, d.mine()...)
		vsLine(t, v0, d.dep+": branch missing")
		vsLine(t, v0, d.tpl+": branch missing")
		vsNone(t, vsCheck(t, none, d.mine()...), `branch missing`)
		restore()
		for _, f := range back {
			f()
		}
		v1 := vsCheck(t, core.VerifyOptions{RequireBranches: true}, d.mine()...)
		vsClean(t, v1)

		// a broken chain
		head := Doc(t, d.dep).At("branch.head.commitId").Str()
		n := Doc(t, d.dep).At("branch.commitCount").Int()
		UpdateDoc(t, d.dep, A{"branch.commitCount": n + 7, "branch.head.commitId": "nope"})
		UpdateDoc(t, d.dep+"/commits/"+head, A{"hash": "sha256:bad"})
		v2 := vsCheck(t, none, d.dep)
		vsLine(t, v2, d.dep+": branch.head nope is not the newest commit "+head)
		vsSome(t, v2, "^"+regexp.QuoteMeta(d.dep)+`: branch\.commitCount \d+ ≠ \d+ commits$`)
		vsLine(t, v2, d.dep+"/commits/"+head+": hash ≠ sha256(JCS(config))")
	})

	t.Run("a stored working copy is reported with RequireNoWorkingCopy; a config equal to the head is not", func(t *testing.T) {
		admin := NewPerson(t, "admin") // a person is rate limited
		a := HelloDeployment(t, admin, "dirty dep (verify-schema)")
		b := HelloDeployment(t, admin, "clean dep (verify-schema)")
		c := HelloDeployment(t, admin, "never committed (verify-schema)")
		tP, aP, bP, cP := "templates/"+a.TemplateID, "deployments/"+a.ID(), "deployments/"+b.ID(), "deployments/"+c.ID()
		restore := vsSnapshot(t, tP, aP, bP, cP)
		// template: theme red
		body := vsConfigOf(Doc(t, tP), "_body")
		body.At("variables").Find("variableSlug", "theme").Map()["value"] = "red"
		UpdateDoc(t, tP, A{"_body": vsJSON(t, body.V), "branch.uncommitted": 1})
		// deployment: theme blue + a new secret value
		draft := vsConfigOf(Doc(t, aP), "_draft")
		draft.At("variables").Find("variableSlug", "theme").Map()["value"] = "blue"
		draft.At("secrets.0").Map()["value"] = "migrated-secret-value"
		UpdateDoc(t, aP, A{"_draft": vsJSON(t, draft.V), "branch.uncommitted": 2})
		// clean, but the field is there
		UpdateDoc(t, bP, A{"branch.uncommitted": 0})
		// never committed: no head, no commits, a config
		back := vsDrop(t, cP+"/commits")
		UpdateDoc(t, cP, A{"branch": A{"head": nil, "origin": Doc(t, cP).At("branch.origin").V, "commitCount": 0, "uncommitted": 5}})

		v0 := vsCheck(t, all, tP, aP, bP, cP)
		vsSome(t, v0, `branch\.uncommitted is still stored`)
		vsSome(t, v0, `config copy differs`)
		vsLine(t, v0, bP+": branch.uncommitted is still stored (there is no working copy since §21 #105)")
		vsSome(t, v0, "^"+regexp.QuoteMeta(tP)+`: the config copy differs from the head commit seq \d+ \(1 item\(s\) — uncommitted changes, run migrate-j\)$`)
		vsSome(t, v0, "^"+regexp.QuoteMeta(aP)+`: the config copy differs from the head commit seq \d+ \(2 item\(s\) — uncommitted changes, run migrate-j\)$`)
		vsLine(t, v0, cP+": no head commit but a non-empty config copy (uncommitted changes)")
		// without the option a numeric field is fine, the copy is not compared
		vsNone(t, vsCheck(t, core.VerifyOptions{RequireBranches: true}, tP, aP, bP, cP), `uncommitted`)
		UpdateDoc(t, bP, A{"branch.uncommitted": "two"})
		vsLine(t, vsCheck(t, none, bP), bP+": branch.uncommitted must be a number")

		restore()
		back()
		vsClean(t, vsCheck(t, all, tP, aP, bP, cP))
	})

	t.Run("opm-cloud verify-schema prints one line per violation and exits 1", func(t *testing.T) {
		admin := NewPerson(t, "admin") // a person is rate limited
		slug := Uniq("vs")
		sp := "services/" + slug
		vsDelete(t, sp)
		SetDoc(t, sp, A{"serviceSlug": slug, "label": slug, "icon": nil, "repository": "acme/api"})
		d := HelloDeployment(t, admin)
		dp := "deployments/" + d.ID()
		restore := vsSnapshot(t, dp)
		UpdateDoc(t, dp, A{"branch": doc.DeleteField()})

		run := func(args ...string) (string, int) {
			c := exec.Command(CloudBin, append([]string{"verify-schema"}, args...)...)
			c.Env = ServerEnv
			var out, errb bytes.Buffer
			c.Stdout, c.Stderr = &out, &errb
			err := c.Run()
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return out.String(), ee.ExitCode()
			}
			NoErr(t, err)
			return out.String(), 0
		}
		// both requirements are the default
		for _, args := range [][]string{nil, {"--require-branches", "--require-no-working-copy"}} {
			out, code := run(args...)
			Eq(t, code, 1, out)
			lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
			vsLine(t, lines, sp+": obsolete field repository")
			vsLine(t, lines, dp+": branch missing")
		}
		out, code := run("--require-branches=false")
		Eq(t, code, 1, out)
		NotContains(t, out, dp+": branch missing")
		Contains(t, out, sp+": obsolete field repository\n")
		restore()
	})
}
