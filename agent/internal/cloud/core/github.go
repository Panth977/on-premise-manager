package core

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
)

// GITHUB_API_DEFAULT is GITHUB_API_DEFAULT.
const GITHUB_API_DEFAULT = "https://api.github.com"

const githubTimeout = 120 * time.Second

// GithubError is GithubError: Status 0 = null (no HTTP answer), RetryAt nil = not rate limited, Auth = 401 / 403 without
// rate limiting (the token or its permissions are wrong: pause the mirror). The token is never in the message.
type GithubError struct {
	Message string
	Status  int
	RetryAt *float64
	Auth    bool
}

func (e *GithubError) Error() string { return e.Message }

// RateLimited is rateLimited.
func (e *GithubError) RateLimited() bool { return e.RetryAt != nil }

// StatusValue is the status for a document: number or null.
func (e *GithubError) StatusValue() any {
	if e.Status == 0 {
		return nil
	}
	return float64(e.Status)
}

// GitBlobSha is gitBlobSha: what `git hash-object` prints.
func GitBlobSha(b []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(b))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

// GithubClient is GithubClient: repos, user, Git Data API, contents (first commit of an EMPTY repository: the Git Data
// API answers 409 there), rate limits.
type GithubClient struct {
	LastRate           *doc.Map
	token, Owner, Repo string
	base               string
}

// NewGithubClient is new GithubClient. base "" = GITHUB_API_DEFAULT.
func NewGithubClient(token, owner, repo, base string) *GithubClient {
	if base == "" {
		base = GITHUB_API_DEFAULT
	}
	return &GithubClient{token: token, Owner: owner, Repo: repo, base: base}
}

func (g *GithubClient) r() string {
	return "/repos/" + doc.EncodeURIComponent(g.Owner) + "/" + doc.EncodeURIComponent(g.Repo)
}

// GhOpts of Req.
type GhOpts struct{ Ok404, Ok409 bool }

// GhRes is one answer.
type GhRes struct {
	Status int
	JSON   any
}

// Map is the JSON answer as an object (nil otherwise).
func (r *GhRes) Map() *doc.Map {
	m, _ := r.JSON.(*doc.Map)
	return m
}

// header is res.headers.get(name) (ok = false: null).
func ghHeader(h http.Header, name string) (string, bool) {
	v := h.Values(name)
	if len(v) == 0 {
		return "", false
	}
	return strings.Join(v, ", "), true
}

// ghNumber is Number(res.headers.get(name)): a missing header is 0, NaN when it is not a number.
func ghNumber(h http.Header, name string) float64 {
	s, ok := ghHeader(h, name)
	if !ok {
		return 0
	}
	f, ok := cloud.JSNumber(s)
	if !ok {
		return math.NaN()
	}
	return f
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

func isoMillis(ms float64) string { return doc.ISO(time.UnixMilli(int64(ms))) }

var ghRateText = regexp.MustCompile(`(?i)secondary rate limit|abuse`)

func pathOnly(p string) string {
	s, _, _ := strings.Cut(p, "?")
	return s
}

// ghFailed is the message of a fetch that got no answer (undici's wording: it ends up in lastError on the status table).
func ghFailed(cx context.Context) string {
	if errors.Is(cx.Err(), context.DeadlineExceeded) {
		return "The operation was aborted due to timeout"
	}
	return "fetch failed"
}

// Req is req; body nil = no body.
func (g *GithubClient) Req(method, path string, body any, o GhOpts) (*GhRes, error) {
	cx, cancel := context.WithTimeout(context.Background(), githubTimeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		b, err := doc.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(cx, method, strings.TrimSuffix(g.base, "/")+path, rd)
	if err != nil {
		return nil, &GithubError{Message: "GitHub " + method + " " + pathOnly(path) + ": " + err.Error()}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header["X-GitHub-Api-Version"] = []string{"2022-11-28"}
	req.Header.Set("User-Agent", "opm-github-mirror")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, &GithubError{Message: "GitHub " + method + " " + pathOnly(path) + ": " + ghFailed(cx)}
	}
	defer res.Body.Close()
	lim := ghNumber(res.Header, "x-ratelimit-limit")
	rem := ghNumber(res.Header, "x-ratelimit-remaining")
	reset := ghNumber(res.Header, "x-ratelimit-reset")
	if _, has := ghHeader(res.Header, "x-ratelimit-limit"); has && finite(lim) && finite(rem) && finite(reset) {
		g.LastRate = doc.Obj("limit", lim, "remaining", rem, "resetAt", isoMillis(reset*1000))
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		if errors.Is(cx.Err(), context.DeadlineExceeded) {
			return nil, errors.New(ghFailed(cx))
		}
		return nil, errors.New("terminated")
	}
	text := DecodeUTF8(raw)
	var js any
	if text != "" {
		if v, err := doc.ParseString(text); err == nil {
			js = v
		}
	}
	st := res.StatusCode
	if st >= 200 && st <= 299 {
		return &GhRes{st, js}, nil
	}
	if (st == 404 && o.Ok404) || (st == 409 && o.Ok409) {
		return &GhRes{st, js}, nil
	}
	var msg string
	if m, ok := js.(*doc.Map); ok && !nullish(m.Val("message")) {
		msg = JSString(m.Val("message"))
	} else {
		msg = doc.JSSlice(text, 200)
	}
	// rate limits: 429, or 403 with remaining 0 / retry-after / "secondary rate limit"
	retryAfter := ghNumber(res.Header, "retry-after")
	_, hasRetry := ghHeader(res.Header, "retry-after")
	secondary := ghRateText.MatchString(msg)
	if st == 429 || (st == 403 && (rem == 0 || (finite(retryAfter) && hasRetry) || secondary)) {
		now := float64(time.Now().UnixMilli())
		at := now + 60_000
		if hasRetry && finite(retryAfter) {
			at = now + retryAfter*1000
		} else if rem == 0 && finite(reset) {
			at = reset*1000 + 1000
		}
		return nil, &GithubError{Message: "GitHub rate limit (" + strconv.Itoa(st) + "): " + msg, Status: st, RetryAt: &at}
	}
	return nil, &GithubError{Message: "GitHub " + method + " " + pathOnly(path) + " → " + strconv.Itoa(st) + ": " + msg, Status: st, Auth: st == 401 || st == 403}
}

// GetRepo is getRepo: nil = 404.
func (g *GithubClient) GetRepo() (*doc.Map, error) {
	r, err := g.Req("GET", g.r(), nil, GhOpts{Ok404: true})
	if err != nil {
		return nil, err
	}
	if r.Status == 404 {
		return nil, nil
	}
	if m := r.Map(); m != nil {
		return m, nil
	}
	return doc.New(), nil
}

// GetUser is getUser: "" = null (fine-grained tokens may not read /user).
func (g *GithubClient) GetUser() (string, error) {
	r, err := g.Req("GET", "/user", nil, GhOpts{})
	if err != nil {
		var ge *GithubError
		if errors.As(err, &ge) && (ge.Status == 403 || ge.Status == 404) {
			return "", nil
		}
		return "", err
	}
	return r.Map().Str("login"), nil
}

// RateLimit is rateLimit.
func (g *GithubClient) RateLimit() (*doc.Map, error) {
	r, err := g.Req("GET", "/rate_limit", nil, GhOpts{})
	if err != nil {
		return nil, err
	}
	c := r.Map().Map("resources").Map("core")
	if c == nil {
		return g.LastRate, nil
	}
	return doc.Obj("limit", c.Val("limit"), "remaining", c.Val("remaining"), "resetAt", isoMillis(c.Num("reset")*1000)), nil
}

// IsEmpty is isEmpty: true when the repository has no commit (the Git Data API refuses it).
func (g *GithubClient) IsEmpty() (bool, error) {
	r, err := g.Req("GET", g.r()+"/git/matching-refs/heads/", nil, GhOpts{Ok404: true, Ok409: true})
	if err != nil {
		return false, err
	}
	if r.Status == 409 {
		return true, nil
	}
	l, ok := r.JSON.([]any)
	return ok && len(l) == 0, nil
}

func encodeBranch(b string) string {
	p := strings.Split(b, "/")
	for i := range p {
		p[i] = doc.EncodeURIComponent(p[i])
	}
	return strings.Join(p, "/")
}

// PutContents is putContents: the first commit of an empty repository (Contents API).
func (g *GithubClient) PutContents(path string, content []byte, message, branch string, person *doc.Map) error {
	body := doc.Obj("message", message, "content", base64.StdEncoding.EncodeToString(content), "branch", branch)
	if person != nil {
		body.Set("committer", person).Set("author", person)
	}
	_, err := g.Req("PUT", g.r()+"/contents/"+encodeBranch(path), body, GhOpts{})
	return err
}

// GetBlobExists is getBlobExists.
func (g *GithubClient) GetBlobExists(sha string) (bool, error) {
	r, err := g.Req("GET", g.r()+"/git/blobs/"+sha, nil, GhOpts{Ok404: true})
	if err != nil {
		return false, err
	}
	return r.Status != 404, nil
}

// CreateBlob is createBlob.
func (g *GithubClient) CreateBlob(b []byte) (string, error) {
	r, err := g.Req("POST", g.r()+"/git/blobs", doc.Obj("content", base64.StdEncoding.EncodeToString(b), "encoding", "base64"), GhOpts{})
	if err != nil {
		return "", err
	}
	return r.Map().Str("sha"), nil
}

// CreateTree is createTree; entries: {path, mode, type, sha}.
func (g *GithubClient) CreateTree(entries []any) (string, error) {
	r, err := g.Req("POST", g.r()+"/git/trees", doc.Obj("tree", entries), GhOpts{})
	if err != nil {
		return "", err
	}
	return r.Map().Str("sha"), nil
}

// CreateCommit is createCommit.
func (g *GithubClient) CreateCommit(c *doc.Map) (string, error) {
	r, err := g.Req("POST", g.r()+"/git/commits", c, GhOpts{})
	if err != nil {
		return "", err
	}
	return r.Map().Str("sha"), nil
}

// GetCommit is getCommit: nil = 404.
func (g *GithubClient) GetCommit(sha string) (*doc.Map, error) {
	r, err := g.Req("GET", g.r()+"/git/commits/"+sha, nil, GhOpts{Ok404: true})
	if err != nil {
		return nil, err
	}
	if r.Status == 404 {
		return nil, nil
	}
	return r.Map(), nil
}

// GetRef is getRef: the head sha of a branch, nil = no such branch.
func (g *GithubClient) GetRef(branch string) (any, error) {
	r, err := g.Req("GET", g.r()+"/git/ref/heads/"+encodeBranch(branch), nil, GhOpts{Ok404: true, Ok409: true})
	if err != nil {
		return nil, err
	}
	if r.Status == 404 || r.Status == 409 {
		return nil, nil
	}
	if s, ok := r.Map().Map("object").StrOK("sha"); ok {
		return s, nil
	}
	return nil, nil
}

// CreateRef is createRef.
func (g *GithubClient) CreateRef(branch, sha string) error {
	_, err := g.Req("POST", g.r()+"/git/refs", doc.Obj("ref", "refs/heads/"+branch, "sha", sha), GhOpts{})
	return err
}

// UpdateRef is updateRef.
func (g *GithubClient) UpdateRef(branch, sha string, force bool) error {
	_, err := g.Req("PATCH", g.r()+"/git/refs/heads/"+encodeBranch(branch), doc.Obj("sha", sha, "force", force), GhOpts{})
	return err
}

// DeleteRef is deleteRef.
func (g *GithubClient) DeleteRef(branch string) (bool, error) {
	r, err := g.Req("DELETE", g.r()+"/git/refs/heads/"+encodeBranch(branch), nil, GhOpts{Ok404: true, Ok409: true})
	if err != nil {
		var ge *GithubError
		if errors.As(err, &ge) && ge.Status == 422 {
			return false, nil // "Reference does not exist"
		}
		return false, err
	}
	return r.Status != 404 && r.Status != 409, nil
}

// SetRef is setRef: create or move a branch to sha (force: allow a non-fast-forward move).
func (g *GithubClient) SetRef(branch, sha string, force bool) error {
	cur, err := g.GetRef(branch)
	if err != nil {
		return err
	}
	if cur == sha {
		return nil
	}
	if cur == nil {
		return g.CreateRef(branch, sha)
	}
	return g.UpdateRef(branch, sha, force)
}
