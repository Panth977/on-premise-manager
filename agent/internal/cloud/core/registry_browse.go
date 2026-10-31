package core

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	"opm/agent/internal/cloud/doc"
)

const (
	browseTimeout = 10 * time.Second
	hubAPI        = "https://hub.docker.com"
	arAPI         = "https://artifactregistry.googleapis.com"
)

// BrowseOpts is BrowseOpts: API bases for tests ("" = the real ones).
type BrowseOpts struct{ HubAPI, ArAPI string }

func (o BrowseOpts) hub() string {
	if o.HubAPI != "" {
		return o.HubAPI
	}
	return hubAPI
}

func (o BrowseOpts) ar() string {
	if o.ArAPI != "" {
		return o.ArAPI
	}
	return arAPI
}

var popular = []string{"library/redis", "library/postgres", "library/nginx", "library/node", "library/alpine", "library/busybox", "library/mongo", "library/mysql", "library/python", "library/traefik"}

func errMsg(err error) string { return doc.JSSlice(err.Error(), 300) }

func matches(name, q string) bool {
	return q == "" || strings.Contains(strings.ToLower(name), strings.ToLower(q))
}

func isoOf(v any) any {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	ms, ok := doc.ParseISO(s)
	if !ok {
		return nil
	}
	return doc.ISO(time.UnixMilli(ms))
}

func numOf(v any) any {
	if f, ok := v.(float64); ok {
		return f
	}
	return nil
}

func hostAndPath(raw string) (string, string) {
	u, err := url.Parse(raw)
	if err != nil {
		return raw, raw
	}
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	return u.Host, p
}

func compactText(s string) string { return doc.JSSlice(reSpaces.ReplaceAllString(s, " "), 160) }

func getJSON(rawURL string, headers map[string]string, method, body string) (any, error) {
	if method == "" {
		method = "GET"
	}
	host, path := hostAndPath(rawURL)
	var data []byte
	if body != "" {
		data = []byte(body)
	}
	res, err := fetchURL(followClient, method, rawURL, headers, data, browseTimeout)
	if err != nil {
		return nil, regErr(host + " not reachable: " + errMsg(err))
	}
	raw, err := readAll(res)
	if err != nil {
		return nil, err
	}
	text := Utf8(raw)
	if !resOK(res) {
		return nil, regErr(doc.JSTrim(method+" "+path+": HTTP "+strconv.Itoa(res.StatusCode)+" "+compactText(text)), res.StatusCode)
	}
	v, err := doc.ParseString(text)
	if err != nil {
		return nil, regErr(path + ": answer is not JSON")
	}
	return v, nil
}

func getMap(rawURL string, headers map[string]string, method, body string) (*doc.Map, error) {
	v, err := getJSON(rawURL, headers, method, body)
	if err != nil {
		return nil, err
	}
	m, _ := v.(*doc.Map)
	return m, nil
}

func repoItem(name string, o *doc.Map) *doc.Map {
	return doc.Obj("name", name, "description", or(o, "description", nil), "official", or(o, "official", false), "stars", or(o, "stars", nil), "pullCount", or(o, "pullCount", nil), "updatedAt", or(o, "updatedAt", nil))
}

func truthyOrNil(v any) any {
	if doc.Truthy(v) {
		return v
	}
	return nil
}

var enCollator = sync.OnceValue(func() *collate.Collator { return collate.New(language.AmericanEnglish) })

var collMu sync.Mutex

// localeCompare is a.localeCompare(b).
func localeCompare(a, b string) int {
	collMu.Lock()
	defer collMu.Unlock()
	return enCollator().CompareString(a, b)
}

func sortTags(items []any) []any {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i].(*doc.Map), items[j].(*doc.Map)
		x, y := a.Str("pushedAt"), b.Str("pushedAt")
		switch {
		case x != "" && y != "":
			return localeCompare(y, x) < 0
		case x != "":
			return true
		case y != "":
			return false
		}
		return localeCompare(a.Str("tag"), b.Str("tag")) < 0
	})
	return items
}

// HubLogin is hubLogin: a Docker Hub JWT for the account (password or personal access token).
func HubLogin(c RegistryCreds, o BrowseOpts) (string, error) {
	api := o.hub()
	ct := map[string]string{"Content-Type": "application/json"}
	j, err := getMap(api+"/v2/users/login", ct, "POST", doc.MarshalString(doc.Obj("username", c.Username, "password", c.Password)))
	if err != nil {
		if j2, err2 := getMap(api+"/v2/auth/token", ct, "POST", doc.MarshalString(doc.Obj("identifier", c.Username, "secret", c.Password))); err2 == nil && j2.Truthy("access_token") {
			return JSString(j2.Val("access_token")), nil
		}
		return "", regErr("Docker Hub login failed: " + errMsg(err))
	}
	if j.Truthy("token") {
		return JSString(j.Val("token")), nil
	}
	return "", regErr("Docker Hub login answered no token")
}

func bearer(jwt string) map[string]string { return map[string]string{"Authorization": "Bearer " + jwt} }

func hubNamespaceRepos(api, jwt, ns string) ([]any, error) {
	h := bearer(jwt)
	e := doc.EncodeURIComponent(ns)
	j, err := getMap(api+"/v2/namespaces/"+e+"/repositories?page_size=100", h, "", "")
	if err != nil {
		if j, err = getMap(api+"/v2/repositories/"+e+"/?page_size=100", h, "", ""); err != nil {
			return nil, err
		}
	}
	out := []any{}
	for _, r := range mapsOf(j.Arr("results")) {
		if !r.Truthy("name") {
			continue
		}
		out = append(out, repoItem(JSString(or(r, "namespace", ns))+"/"+JSString(r.Val("name")), doc.Obj("description", truthyOrNil(r.Val("description")), "stars", numOf(r.Val("star_count")), "pullCount", numOf(r.Val("pull_count")), "updatedAt", isoOf(r.Val("last_updated")))))
	}
	return out, nil
}

// each runs f over items concurrently; results keep their order.
func each[T, R any](items []T, f func(T) R) []R {
	out := make([]R, len(items))
	var wg sync.WaitGroup
	for i, x := range items {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = f(x)
		}()
	}
	wg.Wait()
	return out
}

// HubAccountRepos is hubAccountRepos: repositories the account can see, its namespace + (best effort) its organizations.
// jwt "" = log in first.
func HubAccountRepos(c RegistryCreds, o BrowseOpts, jwt string) ([]any, error) {
	api := o.hub()
	token := jwt
	if token == "" {
		var err error
		if token, err = HubLogin(c, o); err != nil {
			return nil, err
		}
	}
	user := strings.ToLower(c.Username)
	out, err := hubNamespaceRepos(api, token, user)
	if err != nil {
		return nil, err
	}
	orgs, err := getMap(api+"/v2/user/orgs/?page_size=25", bearer(token), "", "")
	if err != nil {
		return out, nil
	}
	var names []string
	for _, x := range mapsOf(orgs.Arr("results")) {
		if n := x.Str("orgname"); n != "" && strings.ToLower(n) != user && len(names) < 5 {
			names = append(names, n)
		}
	}
	lists := each(names, func(n string) []any {
		l, _ := hubNamespaceRepos(api, token, n)
		return l
	})
	for _, l := range lists {
		for _, r := range l {
			out = addRepo(out, r)
		}
	}
	return out, nil
}

func hasRepo(l []any, name any) bool {
	for _, x := range l {
		if x.(*doc.Map).Val("name") == name {
			return true
		}
	}
	return false
}

func addRepo(l []any, r any) []any {
	if hasRepo(l, r.(*doc.Map).Val("name")) {
		return l
	}
	return append(l, r)
}

func hubSearch(api, q string) ([]any, error) {
	j, err := getMap(api+"/v2/search/repositories/?query="+doc.EncodeURIComponent(q)+"&page_size=25", nil, "", "")
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, r := range mapsOf(j.Arr("results")) {
		if !r.Truthy("repo_name") {
			continue
		}
		name := JSString(r.Val("repo_name"))
		if !strings.Contains(name, "/") {
			name = "library/" + name
		}
		out = append(out, repoItem(name, doc.Obj("description", truthyOrNil(r.Val("short_description")), "official", r.Truthy("is_official") || strings.HasPrefix(name, "library/"), "stars", numOf(r.Val("star_count")), "pullCount", numOf(r.Val("pull_count")))))
	}
	return out, nil
}

func repoResp(items []any, source string, supported bool, message any) *doc.Map {
	return doc.Obj("items", items, "source", source, "supported", supported, "message", message)
}

func hubRepositories(c RegistryCreds, q string, o BrowseOpts) *doc.Map {
	items := []any{}
	var messages []string
	if !IsAnonymous(c) {
		l, err := HubAccountRepos(c, o, "")
		if err != nil {
			messages = append(messages, errMsg(err))
		}
		for _, r := range l {
			if matches(r.(*doc.Map).Str("name"), q) {
				items = append(items, r)
			}
		}
	}
	if q != "" {
		found, err := hubSearch(o.hub(), q)
		if err != nil {
			messages = append(messages, "search: "+errMsg(err))
		}
		var fresh []any
		for _, r := range found {
			if !hasRepo(items, r.(*doc.Map).Val("name")) {
				fresh = append(fresh, r)
			}
		}
		sort.SliceStable(fresh, func(i, j int) bool {
			return fresh[i].(*doc.Map).Bool("official") && !fresh[j].(*doc.Map).Bool("official")
		})
		items = append(items, fresh...)
	} else if IsAnonymous(c) {
		for _, n := range popular {
			items = append(items, repoItem(n, doc.Obj("official", true)))
		}
	}
	if len(messages) == 0 {
		return repoResp(items, "docker-hub", true, nil)
	}
	if len(items) == 0 {
		return repoResp([]any{}, "docker-hub", false, strings.Join(messages, "; "))
	}
	return repoResp(items, "docker-hub", true, strings.Join(messages, "; "))
}

var reSha = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func hubTags(c RegistryCreds, repository, q string, o BrowseOpts) (*doc.Map, error) {
	api := o.hub()
	ns, name, _ := strings.Cut(repository, "/")
	h := map[string]string{}
	if !IsAnonymous(c) {
		jwt, err := HubLogin(c, o)
		if err != nil {
			return nil, err
		}
		h = bearer(jwt)
	}
	eNs, eName := doc.EncodeURIComponent(ns), doc.EncodeURIComponent(name)
	j, err := getMap(api+"/v2/namespaces/"+eNs+"/repositories/"+eName+"/tags?page_size=100&ordering=last_updated", h, "", "")
	if err != nil {
		if j, err = getMap(api+"/v2/repositories/"+eNs+"/"+eName+"/tags/?page_size=100&ordering=last_updated", h, "", ""); err != nil {
			return nil, err
		}
	}
	items := []any{}
	for _, t := range mapsOf(j.Arr("results")) {
		n := t.Str("name")
		if !t.Truthy("name") || !matches(n, q) {
			continue
		}
		pushed := isoOf(t.Val("tag_last_pushed"))
		if pushed == nil {
			pushed = isoOf(t.Val("last_updated"))
		}
		var digest any
		if s := t.Str("digest"); reSha.MatchString(s) {
			digest = s
		}
		items = append(items, doc.Obj("tag", n, "pushedAt", pushed, "digest", digest, "sizeBytes", numOf(t.Val("full_size"))))
	}
	return repoResp(sortTags(items), "docker-hub", true, nil), nil
}

var reARHost = regexp.MustCompile(`^https://([a-z0-9-]+)-docker\.pkg\.dev/?$`)

// ArLocation is arLocation: "<location>-docker.pkg.dev" → location; "" = not Artifact Registry.
func ArLocation(u string) string {
	if m := reARHost.FindStringSubmatch(u); m != nil {
		return m[1]
	}
	return ""
}

func parseRSAKey(p string) (*rsa.PrivateKey, error) {
	b, _ := pem.Decode([]byte(p))
	if b == nil {
		return nil, errors.New("error:1E08010C:DECODER routines::unsupported")
	}
	if k, err := x509.ParsePKCS8PrivateKey(b.Bytes); err == nil {
		if r, ok := k.(*rsa.PrivateKey); ok {
			return r, nil
		}
		return nil, errors.New("not an RSA key")
	}
	return x509.ParsePKCS1PrivateKey(b.Bytes)
}

// GoogleAccessToken is googleAccessToken: an OAuth token (scope cloud-platform) from a service-account key via the JWT
// bearer grant. projectID "" = null.
func GoogleAccessToken(keyJSON string) (token, projectID string, err error) {
	v, perr := doc.ParseString(keyJSON)
	if perr != nil {
		return "", "", regErr("the _json_key password is not a service-account key JSON")
	}
	k, _ := v.(*doc.Map)
	if !k.Truthy("client_email") || !k.Truthy("private_key") {
		return "", "", regErr("the service-account key has no client_email / private_key")
	}
	aud := JSString(or(k, "token_uri", "https://oauth2.googleapis.com/token"))
	now := time.Now().Unix()
	enc := func(o *doc.Map) string { return doc.B64URL([]byte(doc.MarshalString(o))) }
	unsigned := enc(doc.Obj("alg", "RS256", "typ", "JWT")) + "." + enc(doc.Obj("iss", k.Val("client_email"), "scope", "https://www.googleapis.com/auth/cloud-platform", "aud", aud, "iat", now, "exp", now+3600))
	key, kerr := parseRSAKey(JSString(k.Val("private_key")))
	if kerr != nil {
		return "", "", regErr("cannot sign with the service-account key: " + errMsg(kerr))
	}
	sum := sha256.Sum256([]byte(unsigned))
	sig, serr := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if serr != nil {
		return "", "", regErr("cannot sign with the service-account key: " + errMsg(serr))
	}
	body := "grant_type=" + doc.FormEncode("urn:ietf:params:oauth:grant-type:jwt-bearer") + "&assertion=" + doc.FormEncode(unsigned+"."+doc.B64URL(sig))
	j, err := getMap(aud, map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, "POST", body)
	if err != nil {
		return "", "", err
	}
	if !j.Truthy("access_token") {
		return "", "", regErr("Google token endpoint answered no access_token")
	}
	return JSString(j.Val("access_token")), k.Str("project_id"), nil
}

func lastSegment(s string) string { return s[strings.LastIndexByte(s, '/')+1:] }

func arRepositories(c RegistryCreds, loc, q string, o BrowseOpts) (*doc.Map, error) {
	api := o.ar()
	token, projectID, err := GoogleAccessToken(c.Password)
	if err != nil {
		return nil, err
	}
	if projectID == "" {
		return nil, regErr("the service-account key has no project_id")
	}
	h := bearer(token)
	repos, err := getMap(api+"/v1/projects/"+projectID+"/locations/"+loc+"/repositories?pageSize=100", h, "", "")
	if err != nil {
		return nil, err
	}
	var docker []*doc.Map
	for _, r := range mapsOf(repos.Arr("repositories")) {
		if f := r.Val("format"); (!doc.Truthy(f) || f == "DOCKER") && len(docker) < 20 {
			docker = append(docker, r)
		}
	}
	type res struct {
		l   []any
		err error
	}
	lists := each(docker, func(r *doc.Map) res {
		rid := lastSegment(r.Str("name"))
		p, err := getMap(api+"/v1/"+r.Str("name")+"/packages?pageSize=200", h, "", "")
		if err != nil {
			return res{}
		}
		out := []any{}
		for _, pk := range mapsOf(p.Arr("packages")) {
			n, err := doc.DecodeURIComponent(lastSegment(pk.Str("name")))
			if err != nil {
				return res{nil, errors.New("URI malformed")}
			}
			out = append(out, repoItem(projectID+"/"+rid+"/"+n, doc.Obj("description", truthyOrNil(r.Val("description")), "updatedAt", isoOf(pk.Val("updateTime")))))
		}
		return res{out, nil}
	})
	items := []any{}
	for _, l := range lists {
		if l.err != nil {
			return nil, l.err
		}
		for _, r := range l.l {
			if matches(r.(*doc.Map).Str("name"), q) {
				items = append(items, r)
			}
		}
	}
	return repoResp(items, "artifact-registry", true, nil), nil
}

func arTags(c RegistryCreds, loc, repository, q string, o BrowseOpts) (*doc.Map, error) {
	api := o.ar()
	parts := strings.Split(repository, "/")
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" {
		return nil, regErr("Artifact Registry repositories look like <project>/<repository>/<image> (got " + repository + ")")
	}
	token, _, err := GoogleAccessToken(c.Password)
	if err != nil {
		return nil, err
	}
	h := bearer(token)
	pkg := api + "/v1/projects/" + parts[0] + "/locations/" + loc + "/repositories/" + parts[1] + "/packages/" + doc.EncodeURIComponent(strings.Join(parts[2:], "/"))
	type res struct {
		m   *doc.Map
		err error
	}
	r := each([]string{"/tags?pageSize=500", "/versions?pageSize=500"}, func(s string) res {
		m, err := getMap(pkg+s, h, "", "")
		return res{m, err}
	})
	if r[0].err != nil {
		return nil, r[0].err
	}
	when := map[string]any{}
	for _, v := range mapsOf(r[1].m.Arr("versions")) {
		n, err := doc.DecodeURIComponent(lastSegment(v.Str("name")))
		if err != nil {
			return nil, errors.New("URI malformed")
		}
		t := isoOf(v.Val("updateTime"))
		if t == nil {
			t = isoOf(v.Val("createTime"))
		}
		when[n] = t
	}
	items := []any{}
	for _, t := range mapsOf(r[0].m.Arr("tags")) {
		var digest, pushed any
		if t.Truthy("version") {
			d, err := doc.DecodeURIComponent(lastSegment(t.Str("version")))
			if err != nil {
				return nil, errors.New("URI malformed")
			}
			if d != "" {
				pushed = when[d]
			}
			if reSha.MatchString(d) {
				digest = d
			}
		}
		tag, err := doc.DecodeURIComponent(lastSegment(t.Str("name")))
		if err != nil {
			return nil, errors.New("URI malformed")
		}
		if matches(tag, q) {
			items = append(items, doc.Obj("tag", tag, "pushedAt", pushed, "digest", digest, "sizeBytes", nil))
		}
	}
	return repoResp(sortTags(items), "artifact-registry", true, nil), nil
}

func v2Get(c RegistryCreds, path, scope string) (*doc.Map, error) {
	res, err := NewRegistryClient(c, browseTimeout).GetV2(path, scope)
	if err != nil {
		return nil, err
	}
	raw, err := readAll(res)
	if err != nil {
		return nil, err
	}
	text := Utf8(raw)
	if !resOK(res) {
		p, _, _ := strings.Cut(path, "?")
		return nil, regErr(doc.JSTrim("GET /v2/"+p+": HTTP "+strconv.Itoa(res.StatusCode)+" "+compactText(text)), res.StatusCode)
	}
	v, err := doc.ParseString(text)
	if err != nil {
		return nil, err
	}
	m, _ := v.(*doc.Map)
	return m, nil
}

// V2Catalog is v2Catalog.
func V2Catalog(c RegistryCreds, q string) (*doc.Map, error) {
	j, err := v2Get(c, "_catalog?n=200", "registry:catalog:*")
	if err != nil {
		return nil, err
	}
	items := []any{}
	for _, n := range j.Strs("repositories") {
		if matches(n, q) {
			items = append(items, repoItem(n, nil))
		}
	}
	return repoResp(items, "catalog", true, nil), nil
}

// V2Tags is v2Tags.
func V2Tags(c RegistryCreds, repository, q string) (*doc.Map, error) {
	j, err := v2Get(c, repository+"/tags/list?n=500", "repository:"+repository+":pull")
	if err != nil {
		return nil, err
	}
	items := []any{}
	for _, t := range j.Strs("tags") {
		if matches(t, q) {
			items = append(items, doc.Obj("tag", t, "pushedAt", nil, "digest", nil, "sizeBytes", nil))
		}
	}
	return repoResp(sortTags(items), "registry-v2", true, nil), nil
}

// ListRepositories is listRepositories: never fails (supported:false + message).
func ListRepositories(c RegistryCreds, q string, o BrowseOpts) *doc.Map {
	if IsDockerHub(c.URL) {
		return hubRepositories(c, q, o)
	}
	if loc := ArLocation(c.URL); loc != "" && c.Username == "_json_key" {
		r, err := arRepositories(c, loc, q, o)
		if err != nil {
			return repoResp([]any{}, "artifact-registry", false, errMsg(err))
		}
		return r
	}
	r, err := V2Catalog(c, q)
	if err != nil {
		return repoResp([]any{}, "none", false, "this registry cannot list repositories ("+errMsg(err)+"); type the repository name")
	}
	return r
}

// ListTags is listTags: never fails (supported:false + message).
func ListTags(c RegistryCreds, repository, q string, o BrowseOpts) *doc.Map {
	var first []string
	var r *doc.Map
	var err error
	if IsDockerHub(c.URL) {
		r, err = hubTags(c, repository, q, o)
	} else if loc := ArLocation(c.URL); loc != "" && c.Username == "_json_key" {
		r, err = arTags(c, loc, repository, q, o)
	}
	if r != nil {
		return r
	}
	if err != nil {
		first = append(first, errMsg(err))
	}
	r, err = V2Tags(c, repository, q)
	if err != nil {
		return repoResp([]any{}, "none", false, strings.Join(append(first, errMsg(err)), "; "))
	}
	if len(first) > 0 {
		return r.Shallow().Set("message", strings.Join(first, "; "))
	}
	return r
}

// AccessibleRepositories is accessibleRepositories: up to 10 repositories the account can pull; empty when unknown.
func AccessibleRepositories(c RegistryCreds, o BrowseOpts) []string {
	var l []any
	if IsDockerHub(c.URL) {
		if IsAnonymous(c) {
			return nil
		}
		var err error
		if l, err = HubAccountRepos(c, o, ""); err != nil {
			return nil
		}
	} else {
		r := ListRepositories(c, "", o)
		if !r.Bool("supported") {
			return nil
		}
		l = r.Arr("items")
	}
	var out []string
	for _, r := range l {
		if len(out) < 10 {
			out = append(out, r.(*doc.Map).Str("name"))
		}
	}
	return out
}

// WithAccessHint is withAccessHint: appends "accessible repositories: …" to a registry access error.
func WithAccessHint[R any](c RegistryCreds, fn func() (R, error), o BrowseOpts) (R, error) {
	r, err := fn()
	if a := IsRegistryAccessError(err); a != nil {
		return r, regAccessErr(AccessHint(c, a.Message, o), a.Repository)
	}
	return r, err
}

// AccessHint is accessHint.
func AccessHint(c RegistryCreds, message string, o BrowseOpts) string {
	list := AccessibleRepositories(c, o)
	if len(list) == 0 {
		return message
	}
	who := "account " + c.Username
	if IsAnonymous(c) {
		who = "this registry"
	}
	return fmt.Sprintf("%s; repositories %s can access: %s", message, who, strings.Join(list, ", "))
}
