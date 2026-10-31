package core

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
)

func tsVectors(t *testing.T, stub, goHash, saKey string) *doc.Map {
	_, f, _, _ := runtime.Caller(0)
	back := filepath.Join(filepath.Dir(f), "../../../../backend")
	if _, err := os.Stat(filepath.Join(back, "node_modules/tsx")); err != nil {
		t.Skip("backend/node_modules missing")
	}
	if _, err := os.Stat(filepath.Join(back, "src/core/files.ts")); err != nil {
		t.Skip("the TypeScript cloud is gone: these vectors compared Go with it during the port")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node")
	}
	cmd := exec.Command("node", "--import", "tsx", "--no-warnings=ExperimentalWarning", "--disable-warning=ExperimentalWarning", filepath.Join(filepath.Dir(f), "testdata/vectors.mts"), stub, filepath.Join(t.TempDir(), "steps.json"), goHash, saKey)
	cmd.Dir = back
	var out, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &e
	if err := cmd.Run(); err != nil {
		t.Fatalf("vectors.mts: %v\n%s", err, e.String())
	}
	raw, err := os.ReadFile(cmd.Args[len(cmd.Args)-3])
	if err != nil {
		t.Fatal(err)
	}
	m, err := doc.ParseMap(raw)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

const (
	cfgDigest  = "sha256:6c9d0bc2bfa0e8e3f7ab9f7a3a3ed1f4c0d9f6a6b4b8bb1f3cf2f5d0f5a7a100"
	layerBytes = "layer-bytes"
)

func stubServer() *httptest.Server {
	sum := func(b string) string { return cloud.Sha256Of([]byte(b)) }
	config := `{"architecture":"amd64","os":"linux","config":{}}`
	layer := sum(layerBytes)
	plain := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"c","digest":"%s","size":%d},"layers":[{"mediaType":"l","digest":"%s","size":%d}]}`, sum(config), len(config), layer, len(layerBytes))
	index := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":"sha256:0","platform":{"os":"windows","architecture":"arm64"}},{"digest":"%s","platform":{"os":"linux","architecture":"arm64","variant":"v7"}},{"digest":"%s","platform":{"os":"linux","architecture":"arm64","variant":"v8"}}]}`, sum("x"), sum(plain))
	jwt := func(o string) string { return "h." + doc.B64URL([]byte(o)) + ".s" }
	h := func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		send := func(code int, ct, body string) {
			if ct != "" {
				w.Header().Set("Content-Type", ct)
			}
			w.WriteHeader(code)
			io.WriteString(w, body)
		}
		js := func(body string) { send(200, "application/json", body) }
		base := "http://" + r.Host
		switch {
		case p == "/open/v2/", p == "/hub/v2/":
			send(200, "", "{}")
		case p == "/open/v2/_catalog":
			js(`{"repositories":["lib/x","other/y"]}`)
		case p == "/open/v2/lib/x/tags/list":
			js(`{"tags":["v2","v1","latest"]}`)
		case strings.HasPrefix(p, "/open/v2/lib/x/manifests/"):
			switch ref := p[len("/open/v2/lib/x/manifests/"):]; ref {
			case "multi":
				send(200, "application/vnd.oci.image.index.v1+json", index)
			case "plain", sum(plain):
				send(200, "application/vnd.oci.image.manifest.v1+json; charset=x", plain)
			case "denied":
				send(401, "", "no")
			case "err":
				send(500, "", "bad   \n thing")
			case "v1":
				send(200, "", `{"schemaVersion":1}`)
			case "text":
				send(200, "", `not json`)
			case "nolayers":
				send(200, "application/json", `{"config":{}}`)
			case "redirect":
				w.Header().Set("Location", "/open/v2/lib/x/manifests/plain")
				w.WriteHeader(307)
			default:
				send(404, "", "nope")
			}
		case strings.HasPrefix(p, "/open/v2/lib/x/blobs/"):
			switch p[len("/open/v2/lib/x/blobs/"):] {
			case sum(config):
				send(200, "", config)
			case layer, "sha256:LAYER":
				send(200, "", layerBytes)
			default:
				send(404, "", "missing blob")
			}
		case p == "/bearer/v2/" && r.Header.Get("Authorization") == "":
			w.Header().Set("Www-Authenticate", `Bearer realm="`+base+`/bearer/token?a=b c",service="svc"`)
			send(401, "", "")
		case p == "/bearer/v2/":
			send(401, "", "")
		case p == "/bearer/token":
			repo := strings.Split(r.URL.Query().Get("scope"), ":")
			name := "other"
			if len(repo) > 1 && repo[1] == "lib/x" {
				name = "lib/x"
			}
			js(`{"token":"` + jwt(`{"access":[{"type":"repository","name":"`+name+`","actions":["pull"]},{"type":"x"}]}`) + `"}`)
		case p == "/basic/v2/":
			if u, pw, _ := r.BasicAuth(); u == "me" && pw == "pw" {
				send(200, "", "")
				return
			}
			w.Header().Set("Www-Authenticate", `Basic realm="x"`)
			send(401, "", "denied\n  here")
		case p == "/boom/v2/":
			send(500, "", "x")
		case p == "/badtoken/v2/", p == "/notoken/v2/", p == "/tokfail/v2/":
			w.Header().Set("Www-Authenticate", `Bearer realm="`+base+"/"+strings.Split(p, "/")[1]+`/token"`)
			send(401, "", "")
		case p == "/badtoken/token":
			send(200, "", "not   json")
		case p == "/notoken/token":
			js(`{}`)
		case p == "/tokfail/token":
			send(403, "", " no \n way ")
		case p == "/ar/token":
			b, _ := io.ReadAll(r.Body)
			parts := strings.Split(string(b), "assertion=")
			jwt := strings.Split(parts[len(parts)-1], ".")
			claims, _ := doc.ParseMap(doc.B64Decode(jwt[1]))
			if len(jwt) != 3 || claims.Val("iss") != "sa@x" {
				send(400, "", "bad assertion")
				return
			}
			js(`{"access_token":"AT"}`)
		case p == "/ar/v1/projects/proj/locations/us-central1/repositories":
			js(`{"repositories":[{"name":"projects/proj/locations/us-central1/repositories/repo","format":"DOCKER","description":"D"},{"name":"projects/proj/locations/us-central1/repositories/mvn","format":"MAVEN"},{"name":"projects/proj/locations/us-central1/repositories/empty"}]}`)
		case p == "/ar/v1/projects/proj/locations/us-central1/repositories/repo/packages":
			js(`{"packages":[{"name":"x/packages/img1","updateTime":"2026-01-01T00:00:00Z"},{"name":"x/packages/my%2Fimg2"}]}`)
		case p == "/ar/v1/projects/proj/locations/us-central1/repositories/repo/packages/my%2Fimg/tags", strings.HasSuffix(r.URL.EscapedPath(), "packages/my%2Fimg/tags"):
			js(`{"tags":[{"name":"x/tags/v1","version":"x/versions/sha256%3A` + strings.Repeat("a", 64) + `"},{"name":"x/tags/v2","version":"x/versions/nope"},{"name":"x/tags/v3"}]}`)
		case strings.HasSuffix(r.URL.EscapedPath(), "packages/my%2Fimg/versions"):
			js(`{"versions":[{"name":"x/versions/sha256%3A` + strings.Repeat("a", 64) + `","updateTime":"2026-02-01T00:00:00Z"},{"name":"x/versions/nope","createTime":"2026-03-01T00:00:00Z"}]}`)
		case p == "/hub/v2/users/login":
			b, _ := io.ReadAll(r.Body)
			if strings.Contains(string(b), `"pw"`) {
				js(`{"token":"JWT"}`)
			} else {
				send(401, "", "wrong  password")
			}
		case p == "/hub/v2/auth/token":
			send(401, "", "no")
		case p == "/hub/v2/namespaces/me/repositories":
			js(`{"results":[{"name":"app","namespace":"me","description":"","star_count":1,"pull_count":2.5,"last_updated":"2026-01-02T03:04:05.123456Z"},{"name":"redis-x","description":"d","last_updated":"bad"},{"x":1}]}`)
		case p == "/hub/v2/user/orgs/":
			js(`{"results":[{"orgname":"ME"},{"orgname":"org1"},{}]}`)
		case p == "/hub/v2/namespaces/org1/repositories":
			js(`{"results":[{"name":"svc","namespace":"org1"}]}`)
		case p == "/hub/v2/search/repositories/":
			js(`{"results":[{"repo_name":"foo/red"},{"repo_name":"redis","short_description":"R","star_count":9,"is_official":true},{"repo_name":"me/app"},{}]}`)
		case p == "/hub/v2/namespaces/lib/repositories/x/tags":
			js(`{"results":[{"name":"a"},{"name":"v2","tag_last_pushed":"2026-01-02T00:00:00Z","digest":"sha256:` + strings.Repeat("a", 64) + `","full_size":5},{"name":"v1","last_updated":"2026-03-02T00:00:00Z","digest":"x"},{"name":"B"},{}]}`)
		default:
			send(404, "", "no such route "+p)
		}
	}
	return httptest.NewServer(http.HandlerFunc(h))
}

var masks = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z`), "<TS>"},
	{regexp.MustCompile(`\b[0-9A-HJKMNP-TV-Z]{26}\b`), "<ULID>"},
	{regexp.MustCompile(`"(uploadId|transferId|_uploadId|auditId)":"[A-Za-z0-9]{20}"`), `"$1":"<ID>"`},
	{regexp.MustCompile(`(uploads|transfers/D1|auditLog|D1/transfers)/[A-Za-z0-9]{20}`), "$1/<ID>"},
	{regexp.MustCompile(`\\"transferId\\":\\"[A-Za-z0-9]{20}\\"`), `<TID>`},
	{regexp.MustCompile(`exp=\d+&sig=[A-Za-z0-9_-]+`), "<SIG>"},
	{regexp.MustCompile(`"_tsMs":\d+`), `"_tsMs":0`},
	{regexp.MustCompile(`"_seconds":\d+,"_nanoseconds":\d+`), `"_t":0`},
	{regexp.MustCompile(`"(prevHash|_cloudLastHash)":"sha256:[0-9a-f]{64}"`), `"$1":"<H>"`},
}

func mask(s string) string {
	for _, m := range masks {
		s = m.re.ReplaceAllString(s, m.with)
	}
	return s
}

// activity hashes cover timestamps.
var reActHash = regexp.MustCompile(`"hash":"sha256:[0-9a-f]{64}"`)

func errOf(err error) *doc.Map {
	var a *cloud.ApiError
	if errors.As(err, &a) {
		return doc.Obj("code", a.Code, "message", a.Message, "details", a.Details)
	}
	return doc.Obj("message", err.Error())
}

func void(err error) (any, error) { return "$undefined", err }

func toArr(v any) []any {
	l, _ := v.([]any)
	return l
}
func toMap(v any) *doc.Map {
	m, _ := v.(*doc.Map)
	return m
}
func toStr(v any) string {
	s, _ := v.(string)
	return s
}
func toStrs(v any) []string {
	if v == nil {
		return nil
	}
	out := []string{}
	for _, x := range toArr(v) {
		out = append(out, x.(string))
	}
	return out
}
func undef(v any) any {
	if v == "$u" {
		return doc.Undefined
	}
	return v
}
func strPtrAny(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}
func optStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// replay state
type env struct {
	acct   *cloud.Account
	stages map[string]string
	ups    map[string]*doc.Map
}

func (e *env) ctx(h *doc.Map, q string) *cloud.Ctx {
	headers := map[string]string{}
	h.Each(func(k string, v any) { headers[k] = v.(string) })
	return &cloud.Ctx{Account: e.acct, Headers: headers, Query: doc.ParseSearch(q)}
}

func (e *env) reload(n string) (*doc.Map, error) {
	s, err := cloud.DB().Doc("uploads/" + e.ups[n].Str("uploadId")).Get()
	if err != nil {
		return nil, err
	}
	e.ups[n] = s.Data()
	return e.ups[n], nil
}

func who(m *doc.Map) Creator {
	if m.Val("kind") == "agent" {
		return Creator{Agent: true, DeploymentID: m.Str("deploymentId"), SiteSlug: m.Str("siteSlug")}
	}
	return Creator{UID: m.Str("uid")}
}

func actorOf(m *doc.Map) Actor { return Actor{m.Val("id"), m.Val("siteSlug")} }

func catOf(m *doc.Map) *Catalog {
	return &Catalog{Services: m.Map("services"), Images: m.Map("images"), NotFoundPageIds: m.Strs("notFoundPageIds")}
}

func catJSON(c *Catalog) *doc.Map {
	o := doc.Obj("services", c.Services, "images", c.Images, "notFoundPageIds", strs(c.NotFoundPageIds))
	if c.Site != nil {
		o.Set("site", c.Site)
	}
	return o
}

func (e *env) serve(a []any) (any, error) {
	os.Setenv("OPM_BLOB_REDIRECT", toStr(a[2]))
	o := toMap(a[1])
	so := ServeOpts{Range: o.Str("range"), FileName: o.Str("fileName"), ContentType: o.Str("contentType")}
	if h := o.Map("headers"); h != nil {
		so.Headers = map[string]string{}
		h.Each(func(k string, v any) { so.Headers[k] = v.(string) })
	}
	if v, ok := o.NumOK("streamMax"); ok {
		n := int64(v)
		so.StreamMax = &n
	}
	r, err := ServeObject(toStr(a[0]), so)
	if err != nil {
		return nil, err
	}
	body := ""
	switch b := r.Body.(type) {
	case string:
		body = b
	case io.ReadCloser:
		x, _ := io.ReadAll(b)
		b.Close()
		body = string(x)
	}
	hs := doc.New()
	var keys []string
	for k := range r.Headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		hs.Set(k, r.Headers[k])
	}
	return doc.Obj("status", r.Status, "headers", hs, "body", body), nil
}

func errOr[T any](v T, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return v, nil
}

func keysOf(m *doc.Map) []any { return strs(m.Keys()) }

func mapsAny(l []*doc.Map) []any {
	out := []any{}
	for _, m := range l {
		out = append(out, m)
	}
	return out
}

func (e *env) table() map[string]func(a []any) (any, error) {
	const D = "D1"
	store := func() *driver.Store { return cloud.DB() }
	inTx := func(fn func(tx *driver.Tx) (any, error)) (any, error) { return driver.RunTx(store(), fn) }
	opts := func(a []any) EnsureBlobsOpts {
		p := map[string]string{}
		if len(a) > 2 {
			toMap(a[2]).Each(func(k string, v any) { p[k] = v.(string) })
		}
		return EnsureBlobsOpts{Needs: toArr(a[1]), StageID: e.stages[toStr(a[0])], UID: "u1", Paths: p}
	}
	stageExists := func(n string) (any, error) {
		s, err := StagesCol().Doc(e.stages[n]).Get()
		if err != nil {
			return nil, err
		}
		return s.Exists(), nil
	}
	putp := func(a []any) (any, error) {
		u, err := e.reload(toStr(a[0]))
		if err != nil {
			return nil, err
		}
		return errOr(PutPart(u, toStr(a[1]), doc.B64Decode(toStr(a[2]))))
	}
	complete := func(a []any) (any, error) {
		u, err := e.reload(toStr(a[0]))
		if err != nil {
			return nil, err
		}
		return errOr(CompleteUpload(u, actorOf(toMap(a[1]))))
	}
	scan := func(a []any) (any, error) {
		en, de, err := ScanUsage(a[0] == true)
		if err != nil {
			return nil, err
		}
		ids := []any{}
		for _, d := range de {
			ids = append(ids, d.Val("deploymentId"))
		}
		return doc.Obj("entries", mapsAny(en), "deps", ids), nil
	}
	buildCat := func(a []any) (any, error) {
		c, err := LoadCatalog()
		if err != nil {
			return nil, err
		}
		return errOr(BuildRevisionCatalog(toMap(a[0]), c))
	}
	wc := func(a []any) (any, error) {
		c := toMap(a[0])
		ext, err := StoreCommitBody(c)
		if err != nil {
			return nil, err
		}
		return inTx(func(tx *driver.Tx) (any, error) {
			WriteCommit(tx, c, ext)
			return ext, nil
		})
	}
	compat := func(a []any) (any, error) {
		return inTx(func(tx *driver.Tx) (any, error) {
			ref := store().Doc("deployments/D1")
			ds, err := tx.Get(ref)
			if err != nil {
				return nil, err
			}
			dep := cloud.DecodeDeployment(ds.Data())
			prep, err := PrepCompat(tx, D)
			if err != nil {
				return nil, err
			}
			log := NewCloudLog(D, dep)
			r := WriteCompat(tx, dep.Shallow().Set("head", a[2]), toMap(a[0]), prep, log, a[1] == true, "a@b")
			tx.Update(ref, log.DepUpdate())
			return doc.Obj("r", mapOrNil(r), "open", len(prep.Open), "needed", prep.Needed), nil
		})
	}
	resolved := func(a []any) (any, error) {
		x, err := ResolveBranch(e.ctx(nil, ""), toMap(a[0]), toStr(a[1]))
		if err != nil {
			return nil, err
		}
		var head any
		if x.Head != nil {
			head = x.Head.Val("commitId")
		}
		return doc.Obj("label", x.Label, "head", head, "unc", x.Uncommitted, "origin", x.Origin), nil
	}
	cs := func(a []any) (any, error) {
		s, err := CreateSource(e.ctx(nil, ""), a[0], a[1])
		if err != nil {
			return nil, err
		}
		if s == nil {
			return nil, nil
		}
		o, src, mb := OriginOf(s)
		var cid any
		if s.Commit != nil {
			cid = s.Commit.Val("commitId")
		}
		return doc.Obj("label", s.Label, "commit", cid, "keys", keysOf(s.Config), "point", s.Point, "o", doc.Obj("origin", o, "source", src, "mergeBase", mb.Shallow().Set("base", keysOf(mb.Map("base"))))), nil
	}
	settle := func(a []any) (any, error) {
		site := toStr(a[0])
		ar := store().Doc("deployments/D1/agents/" + site)
		dr := store().Doc("deployments/D1")
		if err := ar.Update(doc.Obj("upgradeRequest", a[1], "upgrade", a[2], "agentVersion", a[4])); err != nil {
			return nil, err
		}
		if err := dr.Update(doc.Obj("agentUpgradeRun", a[3])); err != nil {
			return nil, err
		}
		if err := SettleUpgrade(D, site); err != nil {
			return nil, err
		}
		as, _ := ar.Get()
		ds, _ := dr.Get()
		var req any
		if as.Data().Truthy("upgradeRequest") {
			req = 1
		}
		return doc.Obj("req", req, "run", ds.Data().Val("agentUpgradeRun"), "epoch", ds.Data().Val("agentEpoch"), "seq", ds.Data().Val("cloudActivitySeq")), nil
	}
	regClient := func(a any) *RegistryClient { return NewRegistryClient(credsOf(toMap(a)), 0) }
	regTest := func(a []any) (any, error) {
		s := toStr(a[1])
		if s == "null" {
			s = ""
		}
		return regClient(a[0]).Test(s, a[2] == true), nil
	}
	pm := func(a []any) (any, error) {
		m, err := regClient(a[0]).PlatformManifest(toStr(a[1]), toStr(a[2]), toStr(a[3]))
		if err != nil {
			return nil, err
		}
		return doc.Obj("mediaType", m.MediaType, "digest", m.Digest, "manifest", m.Manifest, "size", len(m.Bytes)), nil
	}
	getDoc := func(a []any) (any, error) {
		s, err := store().Doc(toStr(a[0])).Get()
		if err != nil {
			return nil, err
		}
		if !s.Exists() {
			return "$undefined", nil
		}
		return s.Data(), nil
	}
	runImp := func(a []any) (any, error) {
		ver := toStr(a[0])
		ref := cloud.Col.Images("web").Doc(ver)
		if err := ref.Set(a[1]); err != nil {
			return nil, err
		}
		RunImport("web", ver)
		s, err := ref.Get()
		if err != nil {
			return nil, err
		}
		return s.Data(), nil
	}
	put := func(a []any) (any, error) {
		return void(cloud.Blobs().Put(toStr(a[0]), []byte(toStr(a[1])), driver.PutOptions{ContentType: "text/plain"}))
	}
	var browseOpts BrowseOpts
	mkOpts := func() BrowseOpts { return browseOpts }
	return map[string]func(a []any) (any, error){
		"stubUrl":         func(a []any) (any, error) { return nil, nil },
		"normVariables":   func(a []any) (any, error) { return errOr(NormVariables(a[0])) },
		"normSecretDefs":  func(a []any) (any, error) { return errOr(NormSecretDefs(a[0])) },
		"normFiles":       func(a []any) (any, error) { return errOr(NormFiles(a[0])) },
		"normSites":       func(a []any) (any, error) { return errOr(NormSites(a[0], nil)) },
		"normalizeStored": func(a []any) (any, error) { return NormalizeStored(toMap(a[0]), toMap(a[1])), nil },
		"safeValidate": func(a []any) (any, error) {
			r := SafeValidate(toMap(a[0]), toMap(a[1]))
			return doc.Obj("issues", r.Issues, "errors", r.Errors, "warnings", r.Warnings), nil
		},
		"serviceView":    func(a []any) (any, error) { return ServiceView(toMap(a[0]), toStr(a[1])), nil },
		"imageView":      func(a []any) (any, error) { return errOr(ImageView(toMap(a[0]), toStr(a[1]), toStr(a[2]))) },
		"withSiteImages": func(a []any) (any, error) { return catJSON(WithSiteImages(catOf(toMap(a[0])), toMap(a[1]), nil)), nil },
		"imageVolumes": func(a []any) (any, error) {
			c := catOf(toMap(a[0]))
			c.Site = toMap(a[0]).Map("site")
			return ImageVolumes(c), nil
		},
		"validationCtx": func(a []any) (any, error) {
			c := catOf(toMap(a[2]))
			c.Site = toMap(a[2]).Map("site")
			return ValidationCtx(toStr(a[0]), toStr(a[1]), c, toStrs(a[3]), toMap(a[4])), nil
		},
		"imageSpec":    func(a []any) (any, error) { return errOr(ImageSpec(toMap(a[0]))) },
		"catalogImage": func(a []any) (any, error) { return CatalogImage(toMap(a[0])), nil },
		"dedupeUsage":  func(a []any) (any, error) { return DedupeUsage(mapsOf(toArr(a[0]))), nil },
		"isTerminal":   func(a []any) (any, error) { return IsTerminal(toStr(a[0])), nil },
		"noRedirectFor": func(a []any) (any, error) {
			h := map[string]string{}
			toMap(a[0]).Each(func(k string, v any) { h[k] = v.(string) })
			return NoRedirectFor(h, toStr(a[1])), nil
		},
		"serveMode": func(a []any) (any, error) {
			var m *int64
			if v, ok := a[3].(float64); ok {
				n := int64(v)
				m = &n
			}
			return ServeMode(int64(a[0].(float64)), a[1] == true, a[2] == true, m), nil
		},
		"parseBlobList": func(a []any) (any, error) { return errOr(ParseBlobList(a[0])) },
		"optStageId": func(a []any) (any, error) {
			s, err := OptStageId(toMap(a[0]))
			if err != nil {
				return nil, err
			}
			if s == "" {
				return "$undefined", nil
			}
			return s, nil
		},
		"encodeActivity":   func(a []any) (any, error) { return errOr(ActivityHash(toMap(a[0]))) },
		"isSecretSet":      func(a []any) (any, error) { return IsSecretSet(toMap(a[0])), nil },
		"maskConfig":       func(a []any) (any, error) { return MaskConfig(toMap(a[0])), nil },
		"registryView":     func(a []any) (any, error) { return RegistryView(toMap(a[0])), nil },
		"deploymentView":   func(a []any) (any, error) { return DeploymentView(toMap(a[0])), nil },
		"revisionView":     func(a []any) (any, error) { return RevisionView(toMap(a[0])), nil },
		"accountView":      func(a []any) (any, error) { return AccountView(toMap(a[0])), nil },
		"uploadView":       func(a []any) (any, error) { return UploadView(toMap(a[0])), nil },
		"expectedPartSize": func(a []any) (any, error) { return ExpectedPartSize(toMap(a[0]), a[1].(float64)), nil },
		"slugCheck": func(a []any) (any, error) {
			v := &cloud.V{}
			CheckRequestedSlug(v, undef(a[0]))
			return append([]cloud.Issue{}, v.List...), nil
		},
		"slugOf": func(a []any) (any, error) { return SlugOf(toMap(a[0]), toStr(a[1])), nil },
		"base32": func(a []any) (any, error) {
			b := make([]byte, len(toStr(a[0]))/2)
			fmt.Sscanf(toStr(a[0]), "%x", &b)
			return Base32(b), nil
		},
		"normalizeAccessKey": func(a []any) (any, error) { return NormalizeAccessKey(toStr(a[0])), nil },
		"canonicalSafe":      func(a []any) (any, error) { return CanonicalSafe(a[0]), nil },
		"sanitizeSvg":        func(a []any) (any, error) { return errOr(SanitizeSvg(toStr(a[0]))) },
		"sniffIcon":          func(a []any) (any, error) { return optStr(SniffIcon(doc.B64Decode(toStr(a[0])))), nil },
		"isIconContentType":  func(a []any) (any, error) { return IsIconContentType(toStr(a[0])), nil },
		"vIconFormat": func(a []any) (any, error) {
			v := &cloud.V{}
			VIconFormat(v, a[0], "icon", a[1] == true)
			return append([]cloud.Issue{}, v.List...), nil
		},
		"isDockerHub":         func(a []any) (any, error) { return IsDockerHub(toStr(a[0])), nil },
		"normalizeRepository": func(a []any) (any, error) { return NormalizeRepository(toStr(a[0]), toStr(a[1])), nil },
		"tokenGrantsPull": func(a []any) (any, error) {
			if g := TokenGrantsPull(toStr(a[0]), toStr(a[1])); g != nil {
				return *g, nil
			}
			return nil, nil
		},
		"noAccessMessage": func(a []any) (any, error) {
			return NoAccessMessage(RegistryCreds{toStr(a[0]), toStr(a[1]), toStr(a[2])}, "lib/x"), nil
		},
		"arLocation": func(a []any) (any, error) { return optStr(ArLocation(toStr(a[0]))), nil },
		"hostUrl":    func(a []any) (any, error) { return HostURL(toStr(a[0])), nil },
		"sourceRef":  func(a []any) (any, error) { return SourceRef(toMap(a[0])), nil },
		"sourceCreds": func(a []any) (any, error) {
			c, err := SourceCreds(toMap(a[0]), toMap(a[1]))
			if err != nil {
				return nil, err
			}
			return doc.Obj("url", c.URL, "username", c.Username, "password", c.Password), nil
		},
		"tarRoundtrip": func(a []any) (any, error) {
			var in []TarEntry
			for _, n := range toStrs(a[0]) {
				in = append(in, TarEntry{n, []byte(strings.Repeat("x", len(n)))})
			}
			b, err := WriteTar(in)
			if err != nil {
				return nil, err
			}
			l, err := ReadTar(b)
			if err != nil {
				return nil, err
			}
			out := []any{}
			for _, x := range l {
				out = append(out, []any{x.Name, string(x.Data)})
			}
			return out, nil
		},
		"readTar": func(a []any) (any, error) {
			l, err := ReadTar(doc.B64Decode(toStr(a[0])))
			if err != nil {
				return nil, err
			}
			out := []any{}
			for _, x := range l {
				out = append(out, []any{x.Name, base64.StdEncoding.EncodeToString(x.Data)})
			}
			return out, nil
		},
		"clampLimit": func(a []any) (any, error) {
			return errOr(ClampLimit(e.ctx(nil, toStr(a[0])), int(a[1].(float64)), int(a[2].(float64))))
		},
		"detectedAddressesError": func(a []any) (any, error) { return optStr(DetectedAddressesError(undef(a[0]))), nil },
		"detectedAddressesOf":    func(a []any) (any, error) { return DetectedAddressesOf(a[0]), nil },
		"peerInfo":               func(a []any) (any, error) { return PeerInfo(toMap(a[0])), nil },
		"upgradeHint":            func(a []any) (any, error) { return UpgradeHint(toStr(a[0])), nil },
		"upgradeViewOf":          func(a []any) (any, error) { return mapOrNil(UpgradeViewOf(toMap(a[0]), toMap(a[1]), nowMs())), nil },
		"liveRequests":           func(a []any) (any, error) { return LiveRequests(mapsOf(toArr(a[0])), nowMs()), nil },
		"upgradeProgressError":   func(a []any) (any, error) { return optStr(UpgradeProgressError(undef(a[0]))), nil },
		"cleanProgress":          func(a []any) (any, error) { return CleanProgress(toMap(a[0]), toStr(a[1])), nil },
		"applyRunExpiry": func(a []any) (any, error) {
			ag := map[string]*doc.Map{}
			for _, x := range mapsOf(toArr(a[1])) {
				ag[x.Str("siteSlug")] = x
			}
			r, ch := ApplyRunExpiry(toMap(a[0]), ag, nowMs())
			return doc.Obj("run", mapOrNil(r), "changed", ch), nil
		},
		"apiBaseOf":       func(a []any) (any, error) { return ApiBaseOf(e.ctx(toMap(a[0]), "")), nil },
		"proposalMeta":    func(a []any) (any, error) { return ProposalMeta(toMap(a[0])), nil },
		"revisionSummary": func(a []any) (any, error) { return RevisionSummary(toMap(a[0])), nil },
		"mergeSecrets":    func(a []any) (any, error) { return errOr(MergeSecrets(toArr(a[0]), toArr(a[1]))) },
		"validateTransferBody": func(a []any) (any, error) {
			if err := ValidateTransferBody(toMap(a[0])); err != nil {
				return nil, err
			}
			return "ok", nil
		},
		"asBranchRef":     func(a []any) (any, error) { return errOr(AsBranchRef(a[0], "p")) },
		"asPoint":         func(a []any) (any, error) { return errOr(AsPoint(a[0], "p")) },
		"volumeSyncError": func(a []any) (any, error) { return optStr(VolumeSyncError(undef(a[0]))), nil },
		"renderInstallScript": func(a []any) (any, error) {
			v := map[string]string{}
			toMap(a[0]).Each(func(k string, x any) { v[k] = x.(string) })
			return RenderInstallScript(v), nil
		},
		"seed": func(a []any) (any, error) { return void(store().Doc(toStr(a[0])).Set(a[1])) },
		"seedIcon": func(a []any) (any, error) {
			return void(store().Doc("icons/" + toStr(a[0])).Set(doc.Obj("contentType", "image/png", "_bytes", doc.Bytes(doc.B64Decode(toStr(a[1]))))))
		},
		"loadCatalog": func(a []any) (any, error) {
			c, err := LoadCatalog()
			if err != nil {
				return nil, err
			}
			return catJSON(c), nil
		},
		"loadDetectedHosts":    func(a []any) (any, error) { return errOr(LoadDetectedHosts(toStr(a[0]))) },
		"buildRevisionCatalog": buildCat,
		"scanUsage":            scan,
		"iconUsage": func(a []any) (any, error) {
			m, err := IconUsage()
			if err != nil {
				return nil, err
			}
			out := doc.New()
			var ks []string
			for k := range m {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			for _, k := range ks {
				out.Set(k, m[k])
			}
			return out, nil
		},
		"embedIcons": func(a []any) (any, error) {
			m, err := EmbedIcons(toArr(a[0]))
			if err != nil {
				return nil, err
			}
			out, _ := doc.ToMap(map[string]any(m))
			return out, nil
		},
		"vIconRefs": func(a []any) (any, error) {
			v := &cloud.V{}
			var f []IconField
			for _, x := range mapsOf(toArr(a[0])) {
				f = append(f, IconField{x.Str("path"), x.Val("value")})
			}
			if err := VIconRefs(v, f); err != nil {
				return nil, err
			}
			return append([]cloud.Issue{}, v.List...), nil
		},
		"takenSlugs": func(a []any) (any, error) {
			m, err := TakenSlugs(toStr(a[0]), nil)
			if err != nil {
				return nil, err
			}
			var ks []string
			for k := range m {
				ks = append(ks, k)
			}
			return ks, nil
		},
		"newSlug": func(a []any) (any, error) {
			return inTx(func(tx *driver.Tx) (any, error) { return errOr(NewSlug(tx, toStr(a[0]), toStr(a[1]), a[2])) })
		},
		"ensureSlug": func(a []any) (any, error) { return errOr(EnsureSlug(toStr(a[0]), toStr(a[1]))) },
		"putBlob":    put,
		"readText": func(a []any) (any, error) {
			s, ok, err := ReadText(toStr(a[0]))
			if err != nil || !ok {
				return nil, err
			}
			return s, nil
		},
		"sizeOf": func(a []any) (any, error) {
			n, err := SizeOf(toStr(a[0]))
			if err != nil || n < 0 {
				return nil, err
			}
			return n, nil
		},
		"hashObject": func(a []any) (any, error) {
			h, n, err := HashObject(toStr(a[0]))
			if err != nil {
				return nil, err
			}
			return doc.Obj("hex", h, "size", n), nil
		},
		"serveObject": e.serve,
		"writeBytes":  func(a []any) (any, error) { return void(WriteBytes(toStr(a[0]), []byte(toStr(a[1])), "")) },
		"openStage": func(a []any) (any, error) {
			s, err := OpenStage(toStr(a[1]), toArr(a[2]))
			if err != nil {
				return nil, err
			}
			e.stages[toStr(a[0])] = s.Str("stageId")
			return s, nil
		},
		"loadStage": func(a []any) (any, error) {
			if a[0] == "$bad" {
				return errOr(LoadStage("bad", toStr(a[1]), false))
			}
			return errOr(LoadStage(e.stages[toStr(a[0])], toStr(a[1]), false))
		},
		"ensureBlobs": func(a []any) (any, error) {
			n, err := EnsureBlobs(opts(a))
			if err != nil {
				return nil, err
			}
			return doc.Obj("promoted", n), nil
		},
		"ensureBlobsFault": func(a []any) (any, error) {
			o := opts(a[:2])
			o.Fault = toStr(a[2])
			return errOr(EnsureBlobs(o))
		},
		"writeStaged": func(a []any) (any, error) {
			return void(WriteStaged(e.stages[toStr(a[0])], toStr(a[1]), []byte(toStr(a[2]))))
		},
		"inStore": func(a []any) (any, error) { return errOr(InStore(toStr(a[0]))) },
		"commitBlobs": func(a []any) (any, error) {
			c := e.ctx(nil, "")
			return void(CommitBlobs(c, nil, e.stages[toStr(a[0])])(fullConfig(e)))
		},
		"testFault": func(a []any) (any, error) { return TestFault(e.ctx(toMap(a[0]), "")), nil },
		"dropStage": func(a []any) (any, error) {
			DropStage(e.stages[toStr(a[0])], toStr(a[1]))
			return "$undefined", nil
		},
		"stageExists":   func(a []any) (any, error) { return stageExists(toStr(a[0])) },
		"cleanupStages": func(a []any) (any, error) { return errOr(CleanupStages(time.Now().UnixMilli())) },
		"newUpload": func(a []any) (any, error) {
			o := toMap(a[1])
			u := NewUpload(NewUploadOpts{Purpose: o.Str("purpose"), SeedArchive: o.Truthy("seedArchive"), DeploymentID: o.Val("deploymentId"), TransferID: o.Val("transferId"), SizeBytes: o.Num("sizeBytes"), Sha256: o.Str("sha256"), CreatedBy: o.Str("createdBy"), CreatorSite: o.Val("creatorSite"), Target: o.Str("target"), StageID: o.Str("stageId")})
			e.ups[toStr(a[0])] = u
			if err := store().Doc("uploads/" + u.Str("uploadId")).Create(u); err != nil {
				return nil, err
			}
			return UploadView(u), nil
		},
		"loadUpload": func(a []any) (any, error) {
			return errOr(LoadUpload(e.ups[toStr(a[0])].Str("uploadId"), who(toMap(a[1])), a[2] == true))
		},
		"putPart":        putp,
		"completeUpload": complete,
		"clearCreator": func(a []any) (any, error) {
			if err := store().Doc("uploads/" + e.ups[toStr(a[0])].Str("uploadId")).Update(doc.Obj("_creatorSite", nil)); err != nil {
				return nil, err
			}
			return "ok", nil
		},
		"reload": func(a []any) (any, error) { return errOr(e.reload(toStr(a[0]))) },
		"createTransfer": func(a []any) (any, error) {
			n, body := toStr(a[0]), toMap(a[1])
			u := NewUpload(NewUploadOpts{Purpose: "transfer", DeploymentID: D, SizeBytes: 12, Sha256: body.Str("sha256"), CreatedBy: "u1", Target: "x"})
			t := NewTransfer(NewTransferOpts{D, "alpha", toStr(a[2]), body, "u1", u.Str("uploadId")})
			u.Set("transferId", t.Val("transferId")).Set("_target", t.Val("storagePath"))
			e.ups[n+"u"], e.ups[n] = u, t
			if err := store().Doc("uploads/" + u.Str("uploadId")).Create(u); err != nil {
				return nil, err
			}
			return t, store().Doc("deployments/D1/transfers/" + t.Str("transferId")).Create(t)
		},
		"createFile": func(a []any) (any, error) {
			u := NewUpload(NewUploadOpts{Purpose: "file", DeploymentID: D, TransferID: "file1", SizeBytes: 12, Sha256: toStr(a[1]), CreatedBy: "u1", CreatorSite: "alpha", Target: "shared-files/D1/file1"})
			e.ups[toStr(a[0])] = u
			if err := store().Doc("uploads/" + u.Str("uploadId")).Create(u); err != nil {
				return nil, err
			}
			return "ok", store().Doc("deployments/D1/files/file1").Create(doc.Obj("fileId", "file1", "fileName", "x.txt", "state", "ready", "publishedAt", past, "unpublishedAt", nil, "cloud", doc.Obj("stored", false, "fetch", NewFetch("u1", "beta")), "_storagePath", "p", "_uploadId", u.Str("uploadId")))
		},
		"createFile2": func(a []any) (any, error) {
			if err := store().Doc("deployments/D1/files/file2").Create(doc.Obj("fileId", "file2", "fileName", "y.txt", "state", "ready", "publishedAt", "2026-06-02T00:00:00.000Z", "unpublishedAt", nil, "cloud", doc.Obj("stored", false, "fetch", doc.Obj("from", "alpha")), "_storagePath", "p", "_uploadId", nil, "_s", 1)); err != nil {
				return nil, err
			}
			return "ok", store().Doc("deployments/D1/files/file3").Create(doc.Obj("fileId", "file3", "fileName", "z.txt", "state", "unpublished", "publishedAt", past, "unpublishedAt", doc.NowISO(), "cloud", doc.Obj("stored", false, "fetch", nil)))
		},
		"filesCloudItems": func(a []any) (any, error) { return errOr(FilesCloudItems(toStr(a[0]))) },
		"pickHolder": func(a []any) (any, error) {
			return inTx(func(tx *driver.Tx) (any, error) {
				s, err := PickHolder(tx, toStr(a[0]), toStrs(a[1]), toStr(a[2]))
				return optStr(s), err
			})
		},
		"appendCloud": func(a []any) (any, error) {
			var l []CloudEntry
			for _, x := range mapsOf(toArr(a[1])) {
				l = append(l, CloudEntry{x.Str("type"), x.Str("summary"), x.Map("data"), x.Val("actor"), x.Str("origin")})
			}
			return void(AppendCloud(toStr(a[0]), l...))
		},
		"ingestSiteLog": func(a []any) (any, error) {
			return errOr(IngestSiteLog(toStr(a[0]), toStr(a[1]), toArr(a[2]), toStr(a[3])))
		},
		"advanceAck":      func(a []any) (any, error) { return errOr(AdvanceAck(toStr(a[0]), toStr(a[1]))) },
		"encodeCommit":    func(a []any) (any, error) { return EncodeCommit(toMap(a[0]), a[1] == true), nil },
		"storeCommitBody": func(a []any) (any, error) { return errOr(StoreCommitBody(toMap(a[0]))) },
		"decodeCommit":    func(a []any) (any, error) { return errOr(DecodeCommit(toMap(a[0]), a[1] == true)) },
		"summaryOfDoc":    func(a []any) (any, error) { return SummaryOfDoc(toMap(a[0])), nil },
		"writeCommit":     wc,
		"loadCommit": func(a []any) (any, error) {
			c, err := LoadCommit(toMap(a[0]), toStr(a[1]), nil, a[2] == true)
			if err != nil || c == nil {
				return nil, err
			}
			if a[1] == "C2" {
				return c.Shallow().Set("config", keysOf(c.Map("config"))), nil
			}
			return c, nil
		},
		"siteRunningCommit": func(a []any) (any, error) {
			r, err := SiteRunningCommit(toStr(a[0]), toStr(a[1]), toStr(a[2]))
			return mapOrNil(r), err
		},
		"deploymentHeadConfig": func(a []any) (any, error) {
			c, err := DeploymentHeadConfig(toMap(a[0]))
			if err != nil {
				return nil, err
			}
			if c == nil {
				return nil, nil
			}
			return keysOf(c), nil
		},
		"encodeMergeBase": func(a []any) (any, error) { return EncodeMergeBase(toMap(a[0])), nil },
		"decodeMergeBase": func(a []any) (any, error) { return errOr(DecodeMergeBase(toMap(a[0]))) },
		"setMergeBase": func(a []any) (any, error) {
			_, err := inTx(func(tx *driver.Tx) (any, error) {
				SetMergeBase(tx, toMap(a[0]), toMap(a[1]))
				return nil, nil
			})
			return void(err)
		},
		"loadMergeBase": func(a []any) (any, error) {
			m, err := LoadMergeBase(toMap(a[0]), toMap(a[1]), nil)
			return mapOrNil(m), err
		},
		"personAuthor": func(a []any) (any, error) {
			ac := &cloud.Account{UID: "u", Doc: doc.New()}
			if s, ok := a[0].(string); ok {
				ac.Email = &s
				ac.Doc.Set("name", "N")
			}
			return PersonAuthor(&cloud.Ctx{Account: ac}), nil
		},
		"scopeFor":         func(a []any) (any, error) { return ScopeFor(toMap(a[0]), toMap(a[1])), nil },
		"secretValuesFor":  func(a []any) (any, error) { return SecretValuesFor(toMap(a[0]), toMap(a[1])), nil },
		"nextBranchState":  func(a []any) (any, error) { return NextBranchState(toMap(a[0]), toMap(a[1])), nil },
		"stripUncommitted": func(a []any) (any, error) { return StripUncommitted(toMap(a[0])), nil },
		"countUncommitted": func(a []any) (any, error) { return CountUncommitted(toStr(a[0]), toMap(a[1]), toMap(a[2])), nil },
		"needsCompat":      func(a []any) (any, error) { return errOr(NeedsCompat(toStr(a[0]), nil)) },
		"writeCompat":      compat,
		"openProposals": func(a []any) (any, error) {
			l, err := OpenProposals(toStr(a[0]), nil)
			return mapsAny(l), err
		},
		"loadRevision": func(a []any) (any, error) {
			r, err := LoadRevision(toStr(a[0]), toStr(a[1]), nil)
			return mapOrNil(r), err
		},
		"readAgentBranch": func(a []any) (any, error) {
			r, err := ReadAgentBranch(toStr(a[0]), toStr(a[1]), nil)
			return mapOrNil(r), err
		},
		"storeHeartbeatBranch": func(a []any) (any, error) {
			return void(StoreHeartbeatBranch(toStr(a[0]), toStr(a[1]), a[2], toStr(a[3])))
		},
		"cloudBranchView": func(a []any) (any, error) { return errOr(CloudBranchView(toStr(a[0]), toMap(a[1]))) },
		"allowedCommitsCount": func(a []any) (any, error) {
			l, err := AllowedCommits(toStr(a[0]))
			return len(l), err
		},
		"legacyHeadMirror": func(a []any) (any, error) {
			return inTx(func(tx *driver.Tx) (any, error) {
				f, err := LegacyHeadMirror(tx, D, "old", toMap(a[0]), a[1])
				if err != nil {
					return nil, err
				}
				if f != nil {
					f()
				}
				return f != nil, nil
			})
		},
		"resolveBranch": resolved,
		"resolvePoint":  func(a []any) (any, error) { return errOr(ResolvePoint(e.ctx(nil, ""), toMap(a[0]))) },
		"resolvePointAt": func(a []any) (any, error) {
			x, err := ResolvePoint(e.ctx(nil, ""), toMap(a[0]))
			if err != nil {
				return nil, err
			}
			return doc.Obj("label", x.Label, "commit", x.Commit.Val("commitId"), "keys", keysOf(x.Config)), nil
		},
		"createSource": cs,
		"agentCommit": func(a []any) (any, error) {
			x, err := AgentCommit(toStr(a[0]), toStr(a[1]), toStr(a[2]))
			if err != nil {
				return nil, err
			}
			return x.Val("commitId"), nil
		},
		"settle":                  settle,
		"volumeContentCloudItems": func(a []any) (any, error) { return errOr(VolumeContentCloudItems(toStr(a[0]))) },
		"storeTree":               func(a []any) (any, error) { return errOr(StoreTree(a[0])) },
		"readTree": func(a []any) (any, error) {
			t, err := ReadTree(toStr(a[0]))
			return mapOrNil(t), err
		},
		"pushBlobAllowed": func(a []any) (any, error) {
			return errOr(PushBlobAllowed(toStr(a[0]), toStr(a[1]), a[2].(float64), toStr(a[3])))
		},
		"storeVolumeSync":      func(a []any) (any, error) { return void(StoreVolumeSync(toStr(a[0]), toStr(a[1]), a[2], toStr(a[3]))) },
		"getDoc":               getDoc,
		"seedBlobContents":     func(a []any) (any, error) { return errOr(SeedBlobContents(toMap(a[0]))) },
		"loadDepsManifest":     func(a []any) (any, error) { return mapOrNil(LoadDepsManifest()), nil },
		"agentReleaseInfo":     func(a []any) (any, error) { return mapOrNil(AgentReleaseInfo(doc.Undefined)), nil },
		"agentReleaseInfoNull": func(a []any) (any, error) { return mapOrNil(AgentReleaseInfo(nil)), nil },
		"resolveAgentVersion":  func(a []any) (any, error) { return errOr(ResolveAgentVersion(toStr(a[0]))) },
		"regTest":              regTest,
		"platformManifest":     pm,
		"blobBytes":            func(a []any) (any, error) { return errOr(regClient(a[0]).BlobBytes(toStr(a[1]), toStr(a[2]))) },
		"listRepositories":     func(a []any) (any, error) { return ListRepositories(credsOf(toMap(a[0])), toStr(a[1]), mkOpts()), nil },
		"listTags": func(a []any) (any, error) {
			return ListTags(credsOf(toMap(a[0])), toStr(a[1]), toStr(a[2]), mkOpts()), nil
		},
		"accessibleRepositories": func(a []any) (any, error) { return strs(AccessibleRepositories(credsOf(toMap(a[0])), mkOpts())), nil },
		"accessHint":             func(a []any) (any, error) { return AccessHint(credsOf(toMap(a[0])), toStr(a[1]), mkOpts()), nil },
		"googleAccessToken": func(a []any) (any, error) {
			t, p, err := GoogleAccessToken(toStr(a[0]))
			if err != nil {
				return nil, err
			}
			return doc.Obj("token", t, "projectId", p), nil
		},
		"runImport":       runImp,
		"verifyAccessKey": func(a []any) (any, error) { return errOr(VerifyAccessKey(toStr(a[0]), toStr(a[1]))) },
		"verifyGoHash":    func(a []any) (any, error) { return true, nil },
		"accessKeyShape": func(a []any) (any, error) {
			k := NewAccessKey()
			ok, err := VerifyAccessKey(k.Grouped, k.Hash)
			return doc.Obj("plain", len(k.Plain), "grouped", len(k.Grouped), "ok", ok), err
		},
		"genSecrets": func(a []any) (any, error) {
			l, err := MergeSecrets(toArr(a[0]), toArr(a[1]))
			if err != nil {
				return nil, err
			}
			out := []any{}
			for _, x := range l {
				s := x.(*doc.Map)
				out = append(out, s.Shallow().Set("value", len(s.Str("value"))).Set("hex", regexp.MustCompile(`^[0-9a-f]*$`).MatchString(s.Str("value"))).Set("rotatedAt", s.Truthy("rotatedAt")))
			}
			return out, nil
		},
		"createFile3": func(a []any) (any, error) {
			u := NewUpload(NewUploadOpts{Purpose: "file", DeploymentID: D, TransferID: "file4", SizeBytes: 12, Sha256: toStr(a[1]), CreatedBy: "u1", CreatorSite: "alpha", Target: "shared-files/D1/file4"})
			e.ups[toStr(a[0])] = u
			if err := store().Doc("uploads/" + u.Str("uploadId")).Create(u); err != nil {
				return nil, err
			}
			return "ok", store().Doc("deployments/D1/files/file4").Create(doc.Obj("fileId", "file4", "fileName", "w.txt", "state", "ready", "publishedAt", past, "unpublishedAt", nil, "cloud", doc.Obj("stored", false, "fetch", NewFetch("u1", "beta")), "_storagePath", "p", "_uploadId", u.Str("uploadId")))
		},
		"readTreeHit": func(a []any) (any, error) {
			h, err := StoreTree(a[0])
			if err != nil {
				return nil, err
			}
			return errOr(ReadTree(h))
		},
		"primeNormalize": func(a []any) (any, error) {
			SetImageVolumes(nil)
			PrimeImageVolumes()
			return []any{ImageVolumesFresh(), NormalizeStored(toMap(a[0]), nil)}, nil
		},
		"fetchLayer": func(a []any) (any, error) { return void(FetchLayer(toMap(a[0]), toStr(a[1]))) },
		"dump": func(a []any) (any, error) {
			q, err := store().Collection(toStr(a[0])).Get()
			if err != nil {
				return nil, err
			}
			out := []any{}
			for _, d := range q.Docs {
				out = append(out, []any{d.Path(), d.Data()})
			}
			return out, nil
		},
		"setBrowse": func(a []any) (any, error) {
			browseOpts = BrowseOpts{toStr(a[0]) + "/hub", toStr(a[0]) + "/ar"}
			return nil, nil
		},
	}
}

const (
	past = "2026-05-01T00:00:00.000Z"
)

func credsOf(m *doc.Map) RegistryCreds {
	return RegistryCreds{m.Str("url"), m.Str("username"), m.Str("password")}
}

func nowMs() int64 {
	ms, _ := doc.ParseISO("2026-06-01T00:00:00.000Z")
	return ms
}

var fullCfg *doc.Map

func fullConfig(*env) *doc.Map { return fullCfg }

func normRows(s string) string {
	v, err := doc.ParseString(s)
	if err != nil {
		return s
	}
	l, ok := v.([]any)
	if !ok {
		return s
	}
	rows := make([]string, len(l))
	for i, x := range l {
		rows[i] = doc.MarshalString(x)
	}
	sort.Strings(rows)
	return strings.Join(rows, "\n")
}

func TestVectors(t *testing.T) {
	srv := stubServer()
	defer srv.Close()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	saKey := doc.MarshalString(doc.Obj("client_email", "sa@x", "project_id", "proj", "token_uri", srv.URL+"/ar/token", "private_key", string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))))
	v := tsVectors(t, srv.URL, HashAccessKey("ABCD-EFGH"), saKey)
	dir := t.TempDir()
	p, err := driver.Open(driver.Config{DataDir: dir, PublicURL: "http://x.test"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	cloud.SetPorts(p)
	t.Setenv("OPM_BLOB_REDIRECT", "0")
	t.Setenv("OPM_TEST_HOOKS", "1")
	t.Setenv("OPM_API_BASE", "")
	t.Setenv("OPM_AGENT_VERSION", "")
	e := &env{acct: cloud.AccountView(doc.Obj("uid", "u1", "email", "a@b.c", "name", "Ann", "access", cloud.FullAdminRules(), "disabled", false)), stages: map[string]string{}, ups: map[string]*doc.Map{}}
	table := e.table()
	table["setBrowse"]([]any{srv.URL})
	fails := 0
	for i, x := range v.Arr("steps") {
		s := x.(*doc.Map)
		fn := s.Str("fn")
		if fn == "stubUrl" {
			continue
		}
		if fn == "seed" && fullCfg == nil {
			if raw := toMap(s.Arr("args")[1]).Str("_draft"); raw != "" {
				fullCfg, _ = doc.ParseMap([]byte(raw))
			}
		}
		f := table[fn]
		if f == nil {
			t.Fatalf("no replay for %s", fn)
		}
		args := append(s.Arr("args"), nil, nil, nil, nil, nil)
		var got string
		func() {
			defer func() {
				if r := recover(); r != nil {
					got = fmt.Sprintf("panic: %v", r)
					buf := make([]byte, 4000)
					got += string(buf[:runtime.Stack(buf, false)])
				}
			}()
			out, err := f(args)
			if err != nil {
				got = `{"err":` + doc.MarshalString(errOf(err)) + `}`
			} else {
				from, ferr := doc.From(out)
				if ferr != nil {
					t.Fatal(ferr)
				}
				got = `{"out":` + doc.MarshalString(from) + `}`
			}
		}()
		var want string
		if m, ok := s.Get("err"); ok {
			want = `{"err":` + doc.MarshalString(m) + `}`
		} else {
			w, has := s.Get("out")
			if !has {
				w = nil
			}
			want = `{"out":` + doc.MarshalString(w) + `}`
		}
		g, w := mask(got), mask(want)
		if fn == "serveObject" {
			w = mask(sortHeaders(want))
		}
		if fn == "seedBlobContents" { // TS key order is the completion order of its pool
			g, w = sortKeys(g), sortKeys(w)
		}
		if fn == "dump" {
			g, w = mask(strings.TrimSuffix(strings.TrimPrefix(got, `{"out":`), "}")), mask(strings.TrimSuffix(strings.TrimPrefix(want, `{"out":`), "}"))
			if p := toStr(s.Arr("args")[0]); strings.HasSuffix(p, "activities") || p == "auditLog" {
				g, w = reActHash.ReplaceAllString(g, `"hash":"<H>"`), reActHash.ReplaceAllString(w, `"hash":"<H>"`)
			}
			g, w = normRows(g), normRows(w)
		}
		if g != w {
			fails++
			if fails <= 40 {
				a := doc.MarshalString(s.Val("args"))
				t.Errorf("step %d %s(%s)\n got %s\nwant %s", i, fn, trunc(a, 400), diffAt(g, w), diffAt(w, g))
			}
		}
	}
	t.Logf("%d steps, %d mismatches", len(v.Arr("steps")), fails)
}

// sortKeys sorts the keys of {"out":{…}}.
func sortKeys(s string) string {
	m, err := doc.ParseMap([]byte(s))
	if err != nil {
		return s
	}
	o := m.Map("out")
	ks := o.Keys()
	sort.Strings(ks)
	n := doc.New()
	for _, k := range ks {
		n.Set(k, o.Val(k))
	}
	return doc.MarshalString(n)
}

// sortHeaders puts the headers of a recorded reply in sorted order (HTTP header order does not matter here).
func sortHeaders(s string) string {
	m, err := doc.ParseMap([]byte(s))
	if err != nil || m.Map("out") == nil {
		return s
	}
	o := m.Map("out")
	h := o.Map("headers")
	ks := h.Keys()
	sort.Strings(ks)
	n := doc.New()
	for _, k := range ks {
		n.Set(k, h.Val(k))
	}
	o.Set("headers", n)
	return doc.MarshalString(m)
}

func TestImportTrigger(t *testing.T) {
	calls := 0
	orig := runImport
	runImport = func(s, v string) {
		calls++
		if s != "web" || v != "1" {
			t.Fatal(s, v)
		}
	}
	defer func() { runImport = orig }()
	st := func(s string) *doc.Map { return doc.Obj("import", doc.Obj("status", s)) }
	for _, c := range []struct {
		before, after *doc.Map
		want          int
	}{{nil, st("pending"), 1}, {st("failed"), st("pending"), 1}, {st("pending"), st("pending"), 0}, {nil, st("ready"), 0}, {st("pending"), nil, 0}} {
		calls = 0
		if err := onImageWritten(driver.DocEvent{Params: map[string]string{"serviceSlug": "web", "version": "1"}, Before: c.before, After: c.after}); err != nil || calls != c.want {
			t.Fatal(err, calls, c.want)
		}
	}
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// diffAt shows a around the first difference from b.
func diffAt(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	from := max(0, i-120)
	return fmt.Sprintf("[@%d/%d] %s", i, len(a), trunc(a[from:], 400))
}
