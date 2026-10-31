package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/cloud/doc"
)

// RegistryError is RegistryError; with Access set it is RegistryAccessError (the registry gave a token but no pull access
// to Repository, or answered 401 for the manifest). Status 0 = null.
type RegistryError struct {
	Message    string
	Status     int
	Access     bool
	Repository string
}

func (e *RegistryError) Error() string { return e.Message }

func regErr(msg string, status ...int) error {
	e := &RegistryError{Message: msg}
	if len(status) > 0 {
		e.Status = status[0]
	}
	return e
}

func regAccessErr(msg, repository string) error {
	return &RegistryError{Message: msg, Status: 401, Access: true, Repository: repository}
}

// IsRegistryAccessError is isRegistryAccessError: the error, or nil.
func IsRegistryAccessError(err error) *RegistryError {
	var e *RegistryError
	if errors.As(err, &e) && e.Access {
		return e
	}
	return nil
}

// RegistryCreds is RegistryCreds.
type RegistryCreds struct{ URL, Username, Password string }

// ACCEPT_MANIFEST is ACCEPT_MANIFEST.
const ACCEPT_MANIFEST = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

const (
	ociIndex        = "application/vnd.oci.image.index.v1+json"
	dockerList      = "application/vnd.docker.distribution.manifest.list.v2+json"
	ociManifest     = "application/vnd.oci.image.manifest.v1+json"
	registryTimeout = 30 * time.Second
)

func isIndexType(s string) bool { return s == ociIndex || s == dockerList }

func excerpt(s string) string {
	return doc.JSSlice(doc.JSTrim(reSpaces.ReplaceAllString(s, " ")), 200)
}

var reDockerHub = regexp.MustCompile(`^https://(?:registry-1\.docker\.io|index\.docker\.io|docker\.io)/?$`)

// IsDockerHub is isDockerHub.
func IsDockerHub(registryURL string) bool { return reDockerHub.MatchString(registryURL) }

// NormalizeRepository is normalizeRepository: Docker Hub official images live under "library/".
func NormalizeRepository(registryURL, repository string) string {
	if IsDockerHub(registryURL) && !strings.Contains(repository, "/") {
		return "library/" + repository
	}
	return repository
}

// IsAnonymous is isAnonymous: username "" and password "" (public images).
func IsAnonymous(c RegistryCreds) bool { return c.Username == "" && c.Password == "" }

// JwtPayload is jwtPayload: the payload of a JWT without a signature check; nil when the token is not a JWT.
func JwtPayload(token string) *doc.Map {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	m, err := doc.ParseMap([]byte(Utf8(doc.B64Decode(parts[1]))))
	if err != nil {
		return nil
	}
	return m
}

// TokenGrantsPull is tokenGrantsPull: false only for a JWT with an `access` claim that does not grant pull on the
// repository; nil = cannot tell.
func TokenGrantsPull(token, repository string) *bool {
	p := JwtPayload(token)
	if !p.Has("access") {
		return nil
	}
	grants := false
	for _, x := range p.Arr("access") {
		a, _ := x.(*doc.Map)
		if a.Val("type") != "repository" || a.Val("name") != repository {
			continue
		}
		for _, act := range a.Arr("actions") {
			if act == "pull" || act == "*" {
				grants = true
			}
		}
	}
	return &grants
}

// NoAccessMessage is noAccessMessage.
func NoAccessMessage(c RegistryCreds, repository string) string {
	who := "account " + c.Username
	if IsAnonymous(c) {
		who = "anonymous access"
	}
	why := "the registry answers 401 for both"
	if IsDockerHub(c.URL) {
		why = "Docker Hub answers 401 for both"
	}
	return "repository " + repository + " not found, or " + who + " has no pull access to it (" + why + ")"
}

var reChallenge = regexp.MustCompile(`([a-zA-Z_]+)="([^"]*)"`)

func parseChallenge(h string) (string, map[string]string) {
	scheme := h
	if sp := strings.IndexByte(h, ' '); sp >= 0 {
		scheme = h[:sp]
	}
	params := map[string]string{}
	for _, m := range reChallenge.FindAllStringSubmatch(h, -1) {
		params[strings.ToLower(m[1])] = m[2]
	}
	return strings.ToLower(scheme), params
}

// fetch mirrors global fetch: redirects followed (credentials dropped across hosts), the timeout covers the body too.
var (
	followClient = &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 20 {
			return errors.New("redirect count exceeded")
		}
		if req.URL.Host != via[0].URL.Host {
			req.Header.Del("Authorization")
		}
		return nil
	}}
	manualClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
)

// fetchError is the message a failed fetch gives: Go's transport texts never reach clients.
func fetchError(err error) error {
	var de interface{ Timeout() bool }
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &de) && de.Timeout()) {
		return errors.New("The operation was aborted due to timeout")
	}
	return errors.New("fetch failed")
}

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error {
	defer b.cancel()
	return b.ReadCloser.Close()
}

func fetchURL(cl *http.Client, method, rawURL string, headers map[string]string, body []byte, timeout time.Duration) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
	if err != nil {
		cancel()
		return nil, errors.New("Failed to parse URL from " + rawURL)
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "node")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := cl.Do(req)
	if err != nil {
		cancel()
		return nil, fetchError(err)
	}
	res.Body = &cancelBody{res.Body, cancel}
	return res, nil
}

func readAll(res *http.Response) ([]byte, error) {
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fetchError(err)
	}
	return b, nil
}

func resOK(res *http.Response) bool { return res.StatusCode >= 200 && res.StatusCode < 300 }

// RegistryClient is RegistryClient: a minimal OCI / Docker distribution client.
type RegistryClient struct {
	Base    string
	creds   RegistryCreds
	timeout time.Duration
	tokens  map[string]string
}

// NewRegistryClient is new RegistryClient(creds, {timeoutMs}); timeout 0 = 30 s.
func NewRegistryClient(c RegistryCreds, timeout time.Duration) *RegistryClient {
	if timeout == 0 {
		timeout = registryTimeout
	}
	return &RegistryClient{Base: strings.TrimSuffix(c.URL, "/"), creds: c, timeout: timeout, tokens: map[string]string{}}
}

func (r *RegistryClient) basic() string {
	if IsAnonymous(r.creds) {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(r.creds.Username+":"+r.creds.Password))
}

var reRepoScope = regexp.MustCompile(`^repository:(.+):pull$`)

// AuthFor is authFor: the Authorization header for a scope ("" = none), "" when the registry is open. For a repository
// pull scope the JWT access claim is checked when checkAccess is set.
func (r *RegistryClient) AuthFor(scope string, checkAccess bool) (string, error) {
	header, ok := r.tokens[scope]
	if !ok {
		var err error
		if header, err = r.fetchAuth(scope); err != nil {
			return "", err
		}
	}
	r.tokens[scope] = header
	if m := reRepoScope.FindStringSubmatch(scope); checkAccess && m != nil && strings.HasPrefix(header, "Bearer ") {
		if g := TokenGrantsPull(header[7:], m[1]); g != nil && !*g {
			return "", regAccessErr(NoAccessMessage(r.creds, m[1]), m[1])
		}
	}
	return header, nil
}

func (r *RegistryClient) fetchAuth(scope string) (string, error) {
	res, err := fetchURL(followClient, "GET", r.Base+"/v2/", nil, nil, r.timeout)
	if err != nil {
		return "", regErr("registry not reachable: " + err.Error())
	}
	readAll(res)
	if res.StatusCode == 401 {
		scheme, params := parseChallenge(res.Header.Get("www-authenticate"))
		if scheme != "bearer" || params["realm"] == "" {
			return r.basic(), nil
		}
		u, err := url.Parse(params["realm"])
		if err != nil || u.Scheme == "" || u.Host == "" {
			return "", errors.New("Invalid URL")
		}
		q := doc.ParseSearch(u.RawQuery)
		if s := params["service"]; s != "" {
			q.Set("service", s)
		}
		if scope != "" {
			q.Set("scope", scope)
		}
		if len(q.Pairs) > 0 {
			u.RawQuery = q.String()
		}
		h := map[string]string{}
		if b := r.basic(); b != "" {
			h["Authorization"] = b
		}
		t, err := fetchURL(followClient, "GET", u.String(), h, nil, r.timeout)
		if err != nil {
			return "", regErr("token endpoint not reachable: " + err.Error())
		}
		raw, err := readAll(t)
		if err != nil {
			return "", err
		}
		body := Utf8(raw)
		if !resOK(t) {
			return "", regErr("token request failed: HTTP "+strconv.Itoa(t.StatusCode)+" "+excerpt(body), t.StatusCode)
		}
		v, err := doc.ParseString(body)
		if err != nil {
			return "", regErr("token response is not JSON: "+excerpt(body), t.StatusCode)
		}
		j, _ := v.(*doc.Map)
		tok := or(j, "token", or(j, "access_token", nil))
		if !doc.Truthy(tok) {
			return "", regErr("token response has no token", t.StatusCode)
		}
		return "Bearer " + JSString(tok), nil
	}
	if !resOK(res) {
		return "", regErr("GET /v2/ answered HTTP "+strconv.Itoa(res.StatusCode), res.StatusCode)
	}
	return "", nil
}

func authHeader(a string) map[string]string {
	if a == "" {
		return nil
	}
	return map[string]string{"Authorization": a}
}

// Test is test: the login test; {ok, message, accessDenied?}. scope "" = none.
func (r *RegistryClient) Test(scope string, checkAccess bool) *doc.Map {
	fail := func(err error) *doc.Map {
		if a := IsRegistryAccessError(err); a != nil {
			return doc.Obj("ok", false, "message", a.Message, "accessDenied", a.Repository)
		}
		return doc.Obj("ok", false, "message", doc.JSSlice(err.Error(), 300))
	}
	a, err := r.AuthFor(scope, checkAccess)
	if err != nil {
		return fail(err)
	}
	res, err := fetchURL(followClient, "GET", r.Base+"/v2/", authHeader(a), nil, r.timeout)
	if err != nil {
		return fail(err)
	}
	raw, err := readAll(res)
	if err != nil {
		return fail(err)
	}
	if resOK(res) || (res.StatusCode == 401 && strings.HasPrefix(a, "Bearer ") && scope != "") {
		return doc.Obj("ok", true, "message", "login ok")
	}
	return doc.Obj("ok", false, "message", doc.JSTrim("HTTP "+strconv.Itoa(res.StatusCode)+" "+excerpt(Utf8(raw))))
}

// GetV2 is getV2: GET <base>/v2/<path> with the auth of a scope. The caller closes the body.
func (r *RegistryClient) GetV2(path, scope string) (*http.Response, error) {
	a, err := r.AuthFor(scope, false)
	if err != nil {
		return nil, err
	}
	res, err := fetchURL(followClient, "GET", r.Base+"/v2/"+path, authHeader(a), nil, r.timeout)
	if err != nil {
		return nil, regErr("GET /v2/" + path + " failed: " + err.Error())
	}
	return res, nil
}

// Get is get: GET <base>/v2/<repo>/<path>, redirects followed without forwarding credentials to another host. The caller
// closes the body.
func (r *RegistryClient) Get(repo, path, accept string) (*http.Response, error) {
	a, err := r.AuthFor("repository:"+repo+":pull", true)
	if err != nil {
		return nil, err
	}
	target := r.Base + "/v2/" + repo + "/" + path
	headers := map[string]string{}
	if a != "" {
		headers["Authorization"] = a
	}
	if accept != "" {
		headers["Accept"] = accept
	}
	baseURL, err := url.Parse(r.Base)
	if err != nil {
		return nil, errors.New("Invalid URL")
	}
	for hop := 0; hop < 6; hop++ {
		res, err := fetchURL(manualClient, "GET", target, headers, nil, 10*time.Minute)
		if err != nil {
			return nil, regErr("GET " + target + " failed: " + err.Error())
		}
		loc := res.Header.Get("location")
		if res.StatusCode >= 300 && res.StatusCode < 400 && loc != "" {
			cur, _ := url.Parse(target)
			next, err := cur.Parse(loc)
			if err != nil {
				res.Body.Close()
				return nil, errors.New("Invalid URL")
			}
			readAll(res)
			if next.Host != baseURL.Host {
				delete(headers, "Authorization")
			}
			target = next.String()
			continue
		}
		return res, nil
	}
	return nil, regErr("too many redirects for " + path)
}

// ManifestResult is what Manifest and PlatformManifest return (Manifest: the parsed OciManifest of PlatformManifest).
type ManifestResult struct {
	Bytes             []byte
	MediaType, Digest string
	Manifest          *doc.Map
}

// Manifest is manifest.
func (r *RegistryClient) Manifest(repo, ref string) (*ManifestResult, error) {
	res, err := r.Get(repo, "manifests/"+ref, ACCEPT_MANIFEST)
	if err != nil {
		return nil, err
	}
	b, err := readAll(res)
	if err != nil {
		return nil, err
	}
	switch {
	case res.StatusCode == 404:
		return nil, regErr("manifest "+repo+":"+ref+" not found", 404)
	case res.StatusCode == 401:
		return nil, regAccessErr("GET manifest "+repo+":"+ref+": "+NoAccessMessage(r.creds, repo), repo)
	case !resOK(res):
		return nil, regErr("GET manifest "+repo+":"+ref+": HTTP "+strconv.Itoa(res.StatusCode)+" "+excerpt(Utf8(b)), res.StatusCode)
	}
	mediaType, _, _ := strings.Cut(res.Header.Get("content-type"), ";")
	mediaType = doc.JSTrim(mediaType)
	v, err := doc.Parse(b)
	if err != nil || v == nil {
		return nil, regErr("manifest is not JSON")
	}
	j, _ := v.(*doc.Map)
	if j.Truthy("mediaType") {
		mediaType = JSString(j.Val("mediaType"))
	} else if mediaType == "" || mediaType == "application/json" {
		mediaType = ociManifest
		if j.Truthy("manifests") {
			mediaType = ociIndex
		}
	}
	if j.Val("schemaVersion") == 1.0 {
		return nil, regErr("schema v1 manifests are not supported")
	}
	x := sha256.Sum256(b)
	return &ManifestResult{Bytes: b, MediaType: mediaType, Digest: "sha256:" + hex.EncodeToString(x[:])}, nil
}

// PlatformManifest is platformManifest: index/list → pick linux/<arch>.
func (r *RegistryClient) PlatformManifest(repo, tag, platform string) (*ManifestResult, error) {
	_, arch, _ := strings.Cut(platform, "/")
	if i := strings.IndexByte(arch, '/'); i >= 0 {
		arch = arch[:i]
	}
	m, err := r.Manifest(repo, tag)
	if err != nil {
		return nil, err
	}
	if isIndexType(m.MediaType) {
		idx, err := doc.ParseMap(m.Bytes)
		if err != nil {
			return nil, err
		}
		var cands []*doc.Map
		for _, e := range mapsOf(idx.Arr("manifests")) {
			if p := e.Map("platform"); p.Val("os") == "linux" && p.Val("architecture") == arch {
				cands = append(cands, e)
			}
		}
		var pick *doc.Map
		for _, e := range cands {
			if v := e.Map("platform").Val("variant"); !doc.Truthy(v) || v == "v8" {
				pick = e
				break
			}
		}
		if pick == nil && len(cands) > 0 {
			pick = cands[0]
		}
		if pick == nil {
			return nil, regErr("platform " + platform + " not in the image")
		}
		digest := pick.Str("digest")
		if m, err = r.Manifest(repo, digest); err != nil {
			return nil, err
		}
		if m.Digest != digest {
			return nil, regErr("manifest digest mismatch: " + m.Digest + " ≠ " + digest)
		}
		if isIndexType(m.MediaType) {
			return nil, regErr("nested image index is not supported")
		}
	}
	man, err := doc.ParseMap(m.Bytes)
	if err != nil {
		return nil, err
	}
	if !man.Map("config").Truthy("digest") {
		return nil, regErr("manifest has no config / layers")
	}
	if _, ok := man.Val("layers").([]any); !ok {
		return nil, regErr("manifest has no config / layers")
	}
	m.Manifest = man
	return m, nil
}

// BlobBytes is blobBytes.
func (r *RegistryClient) BlobBytes(repo, digest string) ([]byte, error) {
	res, err := r.Get(repo, "blobs/"+digest, "")
	if err != nil {
		return nil, err
	}
	if !resOK(res) {
		res.Body.Close()
		return nil, regErr("GET blob "+digest+": HTTP "+strconv.Itoa(res.StatusCode), res.StatusCode)
	}
	return readAll(res)
}

// BlobStream is blobStream: the caller closes the stream. size -1 = null.
func (r *RegistryClient) BlobStream(repo, digest string) (io.ReadCloser, int64, error) {
	res, err := r.Get(repo, "blobs/"+digest, "")
	if err != nil {
		return nil, 0, err
	}
	if !resOK(res) {
		raw, _ := readAll(res)
		return nil, 0, regErr("GET blob "+digest+": HTTP "+strconv.Itoa(res.StatusCode)+" "+excerpt(Utf8(raw)), res.StatusCode)
	}
	size := int64(-1)
	if l := res.Header.Get("content-length"); l != "" {
		if n, err := strconv.ParseInt(l, 10, 64); err == nil {
			size = n
		}
	}
	return res.Body, size, nil
}
