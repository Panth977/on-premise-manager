package apitest

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/canonical"
	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/routes"
)

// Sha256Hex is sha256Hex.
func Sha256Hex(b any) string {
	var s [32]byte
	switch x := b.(type) {
	case string:
		s = sha256.Sum256([]byte(x))
	case []byte:
		s = sha256.Sum256(x)
	default:
		panic("Sha256Hex")
	}
	return hex.EncodeToString(s[:])
}

// Uniq is uniq(prefix).
func Uniq(p string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return p + hex.EncodeToString(b)
}

// Sleep is sleep(ms).
func Sleep(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

// Load is load(name): a shared/testdata JSON file.
func Load(t testing.TB, name string) J {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(RepoRoot, "shared/testdata", name))
	NoErr(t, err)
	return ParseJ(b)
}

// Now is new Date().toISOString().
func Now() string { return ISO(time.Now()) }

// ISO is Date.toISOString.
func ISO(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// RunDaily runs the Go daily job (routes.RunDaily) in this process on the server's data dir.
func RunDaily(t testing.TB) J {
	t.Helper()
	return RunDailyAt(t, time.Now())
}

// RunDailyAt is RunDaily at a given "now".
func RunDailyAt(t testing.TB, now time.Time) J {
	t.Helper()
	r, err := routes.RunDaily(now)
	NoErr(t, err)
	return JOf(r)
}

// BlobShim is abucket: the calls the tests make on the blob store.
type BlobShim struct{ B *driver.BlobStore }

// Save is file(p).save(data).
func (s BlobShim) Save(t testing.TB, p string, data any, o ...driver.PutOptions) {
	t.Helper()
	var b []byte
	switch x := data.(type) {
	case string:
		b = []byte(x)
	case []byte:
		b = x
	default:
		panic("Save")
	}
	var po driver.PutOptions
	if len(o) > 0 {
		po = o[0]
	}
	NoErr(t, s.B.Put(p, b, po))
}

// Download is file(p).download(); it fails the test when the object is missing.
func (s BlobShim) Download(t testing.TB, p string) []byte {
	t.Helper()
	b, err := s.B.Get(p)
	NoErr(t, err)
	if b == nil {
		t.Fatalf("No such object: %s", p)
	}
	return b
}

// Exists is file(p).exists().
func (s BlobShim) Exists(t testing.TB, p string) bool {
	t.Helper()
	m, err := s.B.Head(p)
	NoErr(t, err)
	return m != nil
}

// Delete is file(p).delete({ignoreNotFound: true}).
func (s BlobShim) Delete(t testing.TB, p string) {
	t.Helper()
	NoErr(t, s.B.Delete(p))
}

// IDToken signs in through /auth/login.
func IDToken(t testing.TB, email, password string) string {
	t.Helper()
	r := Raw(t, "POST", "/auth/login", Opt{Headers: map[string]string{"Content-Type": "application/json"}, Body: []byte(show(A{"email": email, "password": password}))})
	if !r.J.At("idToken").Truthy() {
		t.Fatalf("sign-in failed: %s", r.Text())
	}
	return r.J.At("idToken").Str()
}

// Person is a signed-in user.
type Person struct {
	UID, Email, Token, Bearer string
	// API is the browser-emulating client (§21 #105), Real the plain API client.
	API  *BrowserClient
	Real *Client
}

// PersonOpts are the options of NewPerson.
type PersonOpts struct {
	Disabled, NoAccount bool
}

// NewPerson creates an auth user + account document directly (the bootstrap path) and signs in; access is "admin" or a
// list of access rules.
func NewPerson(t testing.TB, access any, opts ...PersonOpts) *Person {
	t.Helper()
	var o PersonOpts
	if len(opts) > 0 {
		o = opts[0]
	}
	email := Uniq("u") + "@opm.test"
	pw := "pw-" + Uniq("") + Uniq("")
	u, err := AuthAdmin.CreateUser(driver.CreateUserInput{Email: email, Password: &pw, DisplayName: &email})
	NoErr(t, err)
	if !o.NoAccount {
		var rules any = access
		if access == "admin" {
			rules = cloud.FullAdminRules()
		}
		NoErr(t, DB.Collection("accounts").Doc(u.UID).Set(A{"uid": u.UID, "img": nil, "name": email, "email": email, "phone": nil, "access": Plain(rules), "disabled": o.Disabled, "createdAt": Now(), "createdBy": "bootstrap", "lastLoginAt": nil, "lastSetPassword": nil, "lastResetLink": nil}))
	}
	tok := IDToken(t, email, pw)
	real := Bearer(tok)
	return &Person{UID: u.UID, Email: email, Token: tok, Bearer: "Bearer " + tok, Real: real, API: Browser(real)}
}

// ── agents

// SiteKey is a site's Ed25519 key.
type SiteKey struct {
	Priv      ed25519.PrivateKey
	PublicKey string // raw 32 bytes, base64 std
}

// NewSiteKey is newSiteKey().
func NewSiteKey() SiteKey {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return SiteKey{Priv: priv, PublicKey: base64.StdEncoding.EncodeToString(pub)}
}

// SignOpt overrides the timestamp / nonce of SignHeaders.
type SignOpt struct {
	Ts    int64
	Nonce string
}

// SignHeaders is signHeaders().
func SignHeaders(key SiteKey, deploymentID, siteSlug, method, path string, body []byte, o ...SignOpt) map[string]string {
	ts, nonce := time.Now().Unix(), ""
	if len(o) > 0 {
		if o[0].Ts != 0 {
			ts = o[0].Ts
		}
		nonce = o[0].Nonce
	}
	if nonce == "" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		nonce = base64.RawURLEncoding.EncodeToString(b)
	}
	str := canonical.SignedRequestString(method, path, ts, nonce, Sha256Hex(append([]byte{}, body...)))
	sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(key.Priv, []byte(str)))
	return map[string]string{"X-OPM-Site": deploymentID + "/" + siteSlug, "X-OPM-Ts": fmt.Sprint(ts), "X-OPM-Nonce": nonce, "X-OPM-Sig": sig}
}

// AgentClient is agentClient().
func AgentClient(key SiteKey, deploymentID, siteSlug string) *Client {
	return &Client{Auth: func(m, p string, b []byte) map[string]string {
		return SignHeaders(key, deploymentID, siteSlug, m, p, b)
	}}
}

// ── hello catalog seeding (fixed ids from shared/testdata)

// SeedHelloCatalog is seedHelloCatalog().
func SeedHelloCatalog(t testing.TB) {
	t.Helper()
	cat := Load(t, "hello-catalog.json")
	now := Now()
	audit := func(m A) A {
		m["createdAt"], m["createdBy"], m["updatedAt"], m["updatedBy"] = now, "test", now, "test"
		return m
	}
	NoErr(t, DB.Collection("registries").Doc("RegHello000000000001").Set(audit(A{"registryId": "RegHello000000000001", "label": "hello registry", "url": "https://127.0.0.1:1", "username": "u", "password": "p", "lastCheck": nil})))
	for _, slug := range cat.At("services").Keys() {
		s := cat.At("services").Key(slug)
		var icon any
		if s.At("icon").Truthy() {
			icon = s.At("icon").V
		}
		NoErr(t, DB.Collection("services").Doc(slug).Set(audit(A{"serviceSlug": slug, "label": s.At("label").V, "icon": icon, "description": ""})))
	}
	// Object.entries order of the file decides sizes (n): the keys are sorted here, nothing depends on the order
	n := 1
	for _, key := range cat.At("images").Keys() {
		img := cat.At("images").Key(key)
		i := strings.IndexByte(key, ':')
		serviceSlug, version := key[:i], key[i+1:]
		hx := func(k string) string { return Sha256Hex(key + ":" + k) }
		layers := L{A{"digest": "sha256:" + hx("l1"), "sizeBytes": 100 + n, "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip"}}
		var hc any
		if img.At("healthcheck").Exists() {
			hc = img.At("healthcheck").V
		}
		NoErr(t, DB.Collection("services").Doc(serviceSlug).Collection("images").Doc(version).Set(audit(A{
			"version": version, "serviceSlug": serviceSlug,
			"reference":  "127.0.0.1:1/test/" + serviceSlug + ":" + version,
			"source":     A{"host": "127.0.0.1:1", "repository": "test/" + serviceSlug, "tag": version, "digest": nil},
			"registryId": "RegHello000000000001",
			"digest":     "sha256:" + hx("m"),
			"platform":   "linux/arm64",
			"import":     A{"mode": "copy", "status": "ready", "error": nil, "sizeBytes": 100 + n, "manifestMediaType": "application/vnd.oci.image.manifest.v1+json", "configDigest": "sha256:" + hx("c"), "layers": layers, "startedAt": now, "finishedAt": now},
			"kind":       img.At("kind").V,
			"entrypoint": nil, "command": nil, "workdir": nil, "user": nil,
			"ports":          img.At("ports").V,
			"env":            img.At("env").V,
			"volumes":        img.At("volumes").V,
			"healthcheck":    hc,
			"stopTimeoutSec": 30, "description": "", "_configSizeBytes": 1234,
		})))
		n++
	}
	NoErr(t, DB.Collection("notFoundPages").Doc("NfpHello000000000001").Set(A{"notFoundPageId": "NfpHello000000000001", "label": "Hello 404", "htmlCode": "<h1>Hello 404</h1>", "createdAt": now, "createdBy": "test", "_sizeBytes": 18}))
}

// Hello is HELLO(): {config, banner}.
func Hello(t testing.TB) J { t.Helper(); return Load(t, "hello-config.json") }

// HelloDep is the result of HelloDeployment.
type HelloDep struct {
	Dep        J
	AccessKey  J
	TemplateID string
}

// ID is dep.deploymentId.
func (h HelloDep) ID() string { return h.Dep.At("deploymentId").Str() }

// HelloDeployment creates a deployment with the hello config in its draft, through a template. label "" = unique.
func HelloDeployment(t testing.TB, admin *Person, label ...string) HelloDep {
	t.Helper()
	l := Uniq("dep")
	if len(label) > 0 && label[0] != "" {
		l = label[0]
	}
	SeedHelloCatalog(t)
	h := Hello(t)
	banner := admin.API.Call(t, "uploadBlob", A{"body": []byte(h.At("banner").Str())})
	Eq(t, banner.At("sha256"), h.At("config.files.0.content.sha256"), "banner sha mismatch")
	tpl := admin.API.Call(t, "createTemplate", A{"body": A{"label": Uniq("tpl"), "icon": "box"}})
	id := tpl.At("templateId").Str()
	secrets := L{}
	for _, s := range h.At("config.secrets").Clone().Arr() {
		s.Map()["value"], s.Map()["rotatedAt"] = nil, nil
		secrets = append(secrets, s.V)
	}
	admin.API.Call(t, "patchTemplate", A{"templateId": id, "body": A{"ifVersion": 1, "variables": h.At("config.variables").V, "secrets": secrets, "files": h.At("config.files").V, "sites": h.At("config.sites").V}})
	// §21 #104: a deployment is created from the template HEAD commit → commit the edited working copy first
	admin.API.Call(t, "commitTemplate", A{"templateId": id, "body": A{"message": "hello config", "ifVersion": 2}})
	d := admin.API.Call(t, "createDeployment", A{"body": A{"label": l, "templateId": id, "tagSlugs": L{}, "managerAccountUids": L{}}})
	return HelloDep{Dep: d.At("deployment"), AccessKey: d.At("accessKey"), TemplateID: id}
}

// EnrollOpt are the options of EnrollSite (0 / "" = default).
type EnrollOpt struct {
	AdvertiseHost string
	AgentPort     int
}

// Enrolled is an enrolled site.
type Enrolled struct {
	Key    SiteKey
	API    *Client
	Enroll J
	Token  string
}

// EnrollSite makes an install token, enrolls a site, and returns its key + signed client.
func EnrollSite(t testing.TB, admin *Person, deploymentID, siteSlug string, opts ...EnrollOpt) *Enrolled {
	t.Helper()
	o := EnrollOpt{AdvertiseHost: "127.0.0.1", AgentPort: 7080}
	if len(opts) > 0 {
		if opts[0].AdvertiseHost != "" {
			o.AdvertiseHost = opts[0].AdvertiseHost
		}
		if opts[0].AgentPort != 0 {
			o.AgentPort = opts[0].AgentPort
		}
	}
	tok := admin.API.Call(t, "createInstallToken", A{"deploymentId": deploymentID, "body": A{"siteSlug": siteSlug}}).At("token").Str()
	key := NewSiteKey()
	res := Bearer(tok).Call(t, "enroll", A{"body": A{"publicKey": key.PublicKey, "agentVersion": "0.1.0-test", "hostname": "host-" + siteSlug, "ip": "127.0.0.1", "advertiseHost": o.AdvertiseHost, "agentPort": o.AgentPort, "os": "darwin", "arch": "arm64"}})
	return &Enrolled{Key: key, API: AgentClient(key, deploymentID, siteSlug), Enroll: res, Token: tok}
}

// Published is the result of Publish.
type Published struct{ Commit, Proposal, Revision J }

// Publish is publish(): §21 #104 the old "publish" through the new API: commit the working copy (a release) and return
// the compat proposal + revision the cloud wrote for agents < 0.14 (test agents enroll as 0.1.0-test). A working copy
// equal to the head (e.g. right after create) returns the head commit's compat proposal (written at create).
func Publish(t testing.TB, api *BrowserClient, deploymentID string, ifRevision any, summary string) Published {
	t.Helper()
	commit, e := api.Try("commitDeployment", A{"deploymentId": deploymentID, "body": A{"message": summary, "ifRevision": ifRevision}})
	if e != nil {
		if e.Code != "invalid" || e.Details.At("errors.0.code").Str() != "V_NO_CHANGE" {
			t.Fatalf("publish: %v", e)
		}
		list := api.Call(t, "listDeploymentCommits", A{"deploymentId": deploymentID, "query": A{"limit": 1}})
		commit = api.Call(t, "getDeploymentCommit", A{"deploymentId": deploymentID, "commitId": list.At("items.0.commitId").V}).At("commit")
	}
	out := Published{Commit: commit, Proposal: J{Undefined}, Revision: J{Undefined}}
	revID := commit.At("data.compat.revId")
	if revID.Truthy() {
		out.Proposal = api.Call(t, "listProposals", A{"deploymentId": deploymentID, "query": A{"limit": 50}}).At("items").Find("revId", revID)
		out.Revision = api.Call(t, "getRevision", A{"deploymentId": deploymentID, "revId": revID})
	}
	return out
}

// ── direct document access

// Doc reads a document as J (Undefined when it does not exist).
func Doc(t testing.TB, path string) J {
	t.Helper()
	s, err := DB.Doc(path).Get()
	NoErr(t, err)
	if !s.Exists() {
		return J{Undefined}
	}
	return JOf(s.Data())
}

// SetDoc is doc(path).set(data).
func SetDoc(t testing.TB, path string, data A) {
	t.Helper()
	NoErr(t, DB.Doc(path).Set(data))
}

// UpdateDoc is doc(path).update(data).
func UpdateDoc(t testing.TB, path string, data A) {
	t.Helper()
	NoErr(t, DB.Doc(path).Update(data))
}

// ListDocs is collection(path).get(): the documents as J, with ID alongside.
func ListDocs(t testing.TB, path string) []DocJ {
	t.Helper()
	q, err := DB.Collection(path).Get()
	NoErr(t, err)
	out := make([]DocJ, 0, len(q.Docs))
	for _, d := range q.Docs {
		out = append(out, DocJ{d.ID(), JOf(d.Data())})
	}
	return out
}

// DocJ is one document of ListDocs.
type DocJ struct {
	ID   string
	Data J
}

// Poll retries fn until it returns true (a 15 s budget like the TS waiters); fatal on timeout.
func Poll(t testing.TB, what string, fn func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if fn() {
			return
		}
		Sleep(50)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ── §21 #105 the browser change set, emulated for the older tests

var startsTestEdit = regexp.MustCompile(`^test edit`)
var cannotCommit = regexp.MustCompile(`^cannot commit`)

// BrowserClient is browserApi(): there is no server working copy since §21 #105: the GUI keeps a pending change set and
// sends it with Commit. The tests written for the working-copy API keep their intent through this wrapper, which plays the
// browser: the removed write calls (putDraftSection, putDraftSite, regenerateSecret, patchTemplate config fields,
// discard…Working) edit an in-memory change set per entity; commitTemplate / commitDeployment with the old body ({message,
// ifVersion | ifRevision}) commit it ({message, baseCommitId, config}). Person.Real is the plain client (tests of #105).
type BrowserClient struct{ *Client }

// Browser wraps a client.
func Browser(c *Client) *BrowserClient { return &BrowserClient{c} }

var (
	pendingDep = map[string]J{}
	pendingTpl = map[string]J{}
	tplOffset  = map[string]int{}
)

// ClearPending is clearPending().
func ClearPending() {
	pendingDep, pendingTpl, tplOffset = map[string]J{}, map[string]J{}, map[string]int{}
}

// PendingOf is pendingOf(): the emulated change set of a deployment (Undefined = none).
func PendingOf(deploymentID string) J {
	if p, ok := pendingDep[deploymentID]; ok {
		return p
	}
	return J{Undefined}
}

func dropKeys(j J, keys ...string) any {
	c := j.Clone()
	for _, k := range keys {
		delete(c.Map(), k)
	}
	return c.V
}

func secretsInput(list J, keys ...string) L {
	out := L{}
	for _, s := range list.Arr() {
		out = append(out, dropKeys(s, keys...))
	}
	return out
}

// DepInput is depInput(): the deployment draft as a ConfigInput (secret values dropped).
func DepInput(t testing.TB, api interface {
	Call(testing.TB, string, A) J
}, deploymentID string) J {
	t.Helper()
	d := api.Call(t, "getDeployment", A{"deploymentId": deploymentID})
	return JOf(A{"variables": d.At("draft.variables").V, "files": d.At("draft.files").V, "sites": d.At("draft.sites").V, "secrets": secretsInput(d.At("draft.secrets"), "value", "isSet", "rotatedAt")})
}

// TplInput is tplInput().
func TplInput(t testing.TB, api interface {
	Call(testing.TB, string, A) J
}, templateID string) J {
	t.Helper()
	d := api.Call(t, "getTemplate", A{"templateId": templateID})
	return JOf(A{"variables": d.At("variables").V, "files": d.At("files").V, "sites": d.At("sites").V, "secrets": secretsInput(d.At("secrets"), "value", "rotatedAt")})
}

func mergeSecretOps(prev, next J) J {
	out := L{}
	for _, it := range next.Arr() {
		x := JOf(dropKeys(it, "value", "rotatedAt"))
		p := prev.Find("secretSlug", x.At("secretSlug"))
		if !x.At("plainValue").Exists() && !x.At("generateNow").Truthy() && p.Exists() {
			if p.At("plainValue").Exists() {
				x.Map()["plainValue"] = p.At("plainValue").V
			}
			if p.At("generateNow").Truthy() {
				x.Map()["generateNow"] = true
			}
		}
		out = append(out, x.V)
	}
	return J{out}
}

// saveLevelIssues is the old "save level" check of a draft / template write (mode draft | template), against the live
// catalog (the validator of the cloud itself, shared.ValidateConfig via core.ValidateAgainstDB).
func saveLevelIssues(t testing.TB, config J, mode, deploymentID string) []J {
	t.Helper()
	return validateLive(t, config, mode, deploymentID)
}

func saveLevelErrors(t testing.TB, config J, mode, deploymentID string) []J {
	t.Helper()
	out := []J{}
	for _, i := range saveLevelIssues(t, config, mode, deploymentID) {
		if i.At("severity").Str() == "error" {
			out = append(out, i)
		}
	}
	return out
}

func asConfig(c J, isSet func(string) bool) J {
	o := c.Clone()
	secrets := L{}
	for _, s := range o.At("secrets").Arr() {
		d := JOf(dropKeys(s, "plainValue", "generateNow"))
		pv := s.At("plainValue")
		var value any
		if s.At("generateNow").Truthy() || (pv.Str() != "") || (!pv.Exists() && (isSet(s.At("secretSlug").Str()) || d.At("generate").Truthy())) {
			value = "x"
		}
		d.Map()["value"], d.Map()["rotatedAt"] = value, nil
		secrets = append(secrets, d.V)
	}
	o.Map()["secrets"] = secrets
	return o
}

func (b *BrowserClient) depWrite(t testing.TB, deploymentID string, next J, message string) J {
	t.Helper()
	d := b.Client.Call(t, "getDeployment", A{"deploymentId": deploymentID})
	setOf := func(slug string) bool { return d.At("draft.secrets").Find("secretSlug", slug).At("isSet").Truthy() }
	_, e := b.Client.Try("commitDeployment", A{"deploymentId": deploymentID, "body": A{"message": message, "baseCommitId": d.At("branch.head.commitId").V, "config": next}})
	if e == nil {
		delete(pendingDep, deploymentID)
	} else {
		// normalization errors (E.bad) are refused like before; only validation blockers ("cannot commit: …") may be a draft
		if e.Code != "invalid" || !cannotCommit.MatchString(e.Message) {
			panic(e)
		}
		errs := e.Details.At("errors")
		if errs.Len() > 0 && len(errs.Filter(func(x J) bool { return x.At("code").Str() != "V_NO_CHANGE" })) == 0 {
			delete(pendingDep, deploymentID)
		} else {
			if len(saveLevelErrors(t, asConfig(next, setOf), "draft", deploymentID)) > 0 {
				panic(e)
			}
			pendingDep[deploymentID] = next
		}
	}
	after := b.Client.Call(t, "getDeployment", A{"deploymentId": deploymentID})
	issues := L{}
	for _, i := range saveLevelIssues(t, asConfig(next, setOf), "draft", deploymentID) {
		if i.At("severity").Str() != "error" {
			issues = append(issues, i.V)
		}
	}
	unc := 0
	if _, ok := pendingDep[deploymentID]; ok {
		unc = 1
	}
	return JOf(A{"draftVersion": after.At("draftVersion").V, "validation": A{"ok": true, "issues": issues}, "uncommitted": unc})
}

func (b *BrowserClient) curDep(t testing.TB, id string) J {
	t.Helper()
	if p, ok := pendingDep[id]; ok {
		return p
	}
	return DepInput(t, b.Client, id)
}

// isAPIErr turns the panics of the emulation back into returned errors (the helpers need a *testing.T to read).
func catchAPI(e **APIError) {
	if r := recover(); r != nil {
		if a, ok := r.(*APIError); ok {
			*e = a
			return
		}
		panic(r)
	}
}

var emuT testing.TB = &failT{}

// failT is the testing.TB of emulated calls: a failed read inside the emulation panics.
type failT struct{ testing.TB }

func (*failT) Helper()                   {}
func (*failT) Fatalf(f string, a ...any) { panic(fmt.Sprintf(f, a...)) }
func (*failT) Fatal(a ...any)            { panic(fmt.Sprint(a...)) }

// Try is the emulated client call: the removed working-copy ops are played by the browser, everything else goes through.
func (b *BrowserClient) Try(op string, args A) (out J, e *APIError) {
	switch op {
	case "putDraftSection", "putDraftSite", "regenerateSecret", "patchTemplate", "commitTemplate", "commitDeployment", "discardTemplateWorking", "discardDeploymentWorking":
	default:
		return b.Client.Try(op, args)
	}
	defer catchAPI(&e)
	t := emuT
	body := JOf(args["body"])
	switch op {
	case "putDraftSection":
		id := fmt.Sprint(args["deploymentId"])
		sec := fmt.Sprint(args["section"])
		cur := b.curDep(t, id)
		next := cur.Clone()
		if sec == "secrets" {
			next.Map()["secrets"] = mergeSecretOps(cur.At("secrets"), body.At("items")).V
		} else {
			next.Map()[sec] = body.At("items").Clone().V
		}
		return b.depWrite(t, id, next, "test edit: "+sec), nil
	case "putDraftSite":
		id, slug := fmt.Sprint(args["deploymentId"]), fmt.Sprint(args["siteSlug"])
		next := b.curDep(t, id).Clone()
		sites := next.At("sites").V.([]any)
		i := -1
		for x := range sites {
			if (J{sites[x]}).At("siteSlug").Str() == slug {
				i = x
			}
		}
		if i < 0 {
			return J{Undefined}, &APIError{Status: 404, Code: "not-found", Message: "site " + slug + " is not in the draft", Details: J{A{}}}
		}
		sites[i] = body.At("site").Clone().V
		return b.depWrite(t, id, next, "test edit: site "+slug), nil
	case "regenerateSecret":
		id, slug := fmt.Sprint(args["deploymentId"]), fmt.Sprint(args["secretSlug"])
		next := b.curDep(t, id).Clone()
		s := next.At("secrets").Find("secretSlug", slug)
		if !s.Exists() {
			return J{Undefined}, &APIError{Status: 404, Code: "not-found", Message: "secret " + slug + " is not in the draft", Details: J{A{}}}
		}
		delete(s.Map(), "plainValue")
		s.Map()["generateNow"] = true
		pendingDep[id] = next
		return JOf(A{"value": "", "hideAfterSec": 60, "draftVersion": body.At("ifRevision").Num() + 1, "rotatedAt": Now()}), nil
	case "patchTemplate":
		return b.patchTemplate(t, args, body)
	case "commitTemplate":
		if body.Has("config") {
			return b.Client.Try(op, args)
		}
		id := fmt.Sprint(args["templateId"])
		tp := b.Client.Call(t, "getTemplate", A{"templateId": id})
		if _, ok := pendingTpl[id]; !ok {
			// the edit was committed at once by patchTemplate: re-label that commit's message is impossible — return it
			h := tp.At("branch.head")
			if h.Truthy() && startsTestEdit.MatchString(h.At("message").Str()) && commitMessageOK(body.At("message")) {
				return b.Client.Call(t, "getTemplateCommit", A{"templateId": id, "commitId": h.At("commitId").V}).At("commit"), nil
			}
		}
		config, ok := pendingTpl[id]
		if !ok {
			config = TplInput(t, b.Client, id)
		}
		c, e := b.Client.Try("commitTemplate", A{"templateId": id, "body": A{"message": body.At("message").V, "baseCommitId": tp.At("branch.head.commitId").V, "config": config}})
		if e != nil {
			return J{Undefined}, e
		}
		delete(pendingTpl, id)
		return c, nil
	case "commitDeployment":
		if body.Has("config") {
			return b.Client.Try(op, args)
		}
		id := fmt.Sprint(args["deploymentId"])
		d := b.Client.Call(t, "getDeployment", A{"deploymentId": id})
		if _, ok := pendingDep[id]; !ok {
			h := d.At("branch.head")
			if h.Truthy() && startsTestEdit.MatchString(h.At("message").Str()) && commitMessageOK(body.At("message")) {
				return b.Client.Call(t, "getDeploymentCommit", A{"deploymentId": id, "commitId": h.At("commitId").V}).At("commit"), nil
			}
		}
		config, ok := pendingDep[id]
		if !ok {
			config = DepInput(t, b.Client, id)
		}
		c, e := b.Client.Try("commitDeployment", A{"deploymentId": id, "body": A{"message": body.At("message").V, "baseCommitId": d.At("branch.head.commitId").V, "config": config}})
		if e != nil {
			return J{Undefined}, e
		}
		delete(pendingDep, id)
		return c, nil
	case "discardTemplateWorking":
		id := fmt.Sprint(args["templateId"])
		delete(pendingTpl, id)
		return JOf(A{"version": b.Client.Call(t, "getTemplate", A{"templateId": id}).At("version").V, "uncommitted": 0}), nil
	case "discardDeploymentWorking":
		id := fmt.Sprint(args["deploymentId"])
		delete(pendingDep, id)
		return JOf(A{"draftVersion": b.Client.Call(t, "getDeployment", A{"deploymentId": id}).At("draftVersion").V, "uncommitted": 0}), nil
	}
	panic("unreachable")
}

func (b *BrowserClient) patchTemplate(t testing.TB, args A, body J) (J, *APIError) {
	id := fmt.Sprint(args["templateId"])
	meta := dropKeys(body, "variables", "secrets", "files", "sites", "ifVersion").(map[string]any)
	cfg := body.Has("variables") || body.Has("secrets") || body.Has("files") || body.Has("sites")
	off := tplOffset[id]
	var template J
	if len(meta) > 0 {
		m := map[string]any{"ifVersion": body.At("ifVersion").V}
		for k, v := range meta {
			m[k] = v
		}
		r, e := b.Client.Try("patchTemplate", A{"templateId": id, "body": m})
		if e != nil {
			return J{Undefined}, e
		}
		template = r.At("template")
	} else {
		var e *APIError
		if template, e = b.Client.Try("getTemplate", A{"templateId": id}); e != nil {
			return J{Undefined}, e
		}
	}
	if cfg {
		cur, ok := pendingTpl[id]
		if !ok {
			cur = TplInput(t, b.Client, id)
		}
		next := A{"variables": cur.At("variables").V, "files": cur.At("files").V, "sites": cur.At("sites").V, "secrets": cur.At("secrets").V}
		for _, k := range []string{"variables", "files", "sites"} {
			if body.Has(k) {
				next[k] = body.At(k).V
			}
		}
		if body.Has("secrets") {
			for _, x := range body.At("secrets").Arr() {
				if !x.At("value").IsNil() {
					return J{Undefined}, &APIError{Status: 422, Code: "invalid", Message: "template secrets hold no values", Details: JOf(A{"errors": L{A{"path": "secrets", "code": "V_TEMPLATE_SECRET_VALUE", "message": "value must be null", "severity": "error"}}})}
				}
			}
			next["secrets"] = mergeSecretOps(cur.At("secrets"), body.At("secrets")).V
		}
		// a template commit validates at template level = the old PATCH level: commit at once (422 = nothing saved)
		_, e := b.Client.Try("commitTemplate", A{"templateId": id, "body": A{"message": "test edit", "baseCommitId": template.At("branch.head.commitId").V, "config": next}})
		if e == nil {
			delete(pendingTpl, id)
		} else {
			errs := e.Details.At("errors")
			if !(errs.Len() > 0 && len(errs.Filter(func(x J) bool { return x.At("code").Str() != "V_NO_CHANGE" })) == 0) {
				return J{Undefined}, e
			}
		}
		template = b.Client.Call(t, "getTemplate", A{"templateId": id})
		tplOffset[id] = off + 1
	}
	issues := L{}
	if cfg {
		for _, i := range saveLevelIssues(t, JOf(A{"variables": template.At("variables").V, "secrets": template.At("secrets").V, "files": template.At("files").V, "sites": template.At("sites").V}), "template", "template") {
			if i.At("severity").Str() != "error" {
				issues = append(issues, i.V)
			}
		}
	}
	tt := template.Clone()
	if p, ok := pendingTpl[id]; ok {
		tt.Map()["variables"], tt.Map()["files"], tt.Map()["sites"] = p.At("variables").V, p.At("files").V, p.At("sites").V
		secrets := L{}
		for _, x := range p.At("secrets").Clone().Arr() {
			x.Map()["value"], x.Map()["rotatedAt"] = nil, nil
			secrets = append(secrets, x.V)
		}
		tt.Map()["secrets"] = secrets
	}
	return JOf(A{"template": tt.V, "validation": A{"ok": true, "issues": issues}}), nil
}

// Call is the emulated call that must succeed.
func (b *BrowserClient) Call(t testing.TB, op string, args A) J {
	t.Helper()
	j, e := b.Try(op, args)
	if e != nil {
		t.Fatalf("%s: %v", op, e)
	}
	return j
}

// Rejects is rejects(api.op(args), code, status?) on the emulated client.
func (b *BrowserClient) Rejects(t testing.TB, op string, args A, code string, status ...int) *APIError {
	t.Helper()
	_, e := b.Try(op, args)
	return Rejected(t, e, code, status...)
}
