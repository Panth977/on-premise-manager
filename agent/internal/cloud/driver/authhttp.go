package driver

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/cloud/doc"
)

// OidcConfig is the OIDC provider config.
type OidcConfig struct {
	Issuer        string
	ClientID      string
	ClientSecret  string
	Label         string
	AllowedDomain *string
	RedirectURI   string
}

// AuthHTTPRequest is one /auth request.
type AuthHTTPRequest struct {
	Method string
	URL    string
	Body   []byte
	IP     string
}

// AuthHTTPResponse is its answer.
type AuthHTTPResponse struct {
	Status  int
	Headers map[string]string
	Body    *string
}

// AuthHTTPOptions of HandleAuthHTTP.
type AuthHTTPOptions struct {
	SendMail   func(to, subject, text string) error
	Oidc       *OidcConfig
	ConsoleURL string
}

func authJSON(status int, o any) *AuthHTTPResponse {
	s := doc.MarshalString(o)
	return &AuthHTTPResponse{status, map[string]string{"Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store"}, &s}
}
func authFail(status int, code, msg string) *AuthHTTPResponse {
	return authJSON(status, doc.Obj("error", doc.Obj("code", code, "message", msg)))
}
func noContent() *AuthHTTPResponse {
	return &AuthHTTPResponse{204, map[string]string{"Cache-Control": "no-store"}, nil}
}
func redirect(to string) *AuthHTTPResponse {
	return &AuthHTTPResponse{302, map[string]string{"Location": to, "Cache-Control": "no-store"}, nil}
}

// login failure limiter (in memory, per ip + e-mail)
const (
	maxFails = 10
	windowMs = 15 * 60_000
)

type failRec struct {
	n     int
	since int64
}

var (
	failMu sync.Mutex
	fails  = map[string]*failRec{}
)

func limited(k string) bool {
	failMu.Lock()
	defer failMu.Unlock()
	f := fails[k]
	if f == nil {
		return false
	}
	if time.Now().UnixMilli()-f.since > windowMs {
		delete(fails, k)
		return false
	}
	return f.n >= maxFails
}
func failed(k string) {
	failMu.Lock()
	defer failMu.Unlock()
	now := time.Now().UnixMilli()
	f := fails[k]
	if f == nil || now-f.since > windowMs {
		fails[k] = &failRec{1, now}
	} else {
		f.n++
	}
	if len(fails) > 50_000 {
		for key, v := range fails {
			if now-v.since > windowMs {
				delete(fails, key)
			}
		}
	}
}

// ResetLoginLimiter is the test hook.
func ResetLoginLimiter() {
	failMu.Lock()
	fails = map[string]*failRec{}
	failMu.Unlock()
}

var googleHost = regexp.MustCompile(`(^|\.)accounts\.google\.com$`)

// ProviderOf: the providers id of an OIDC issuer (google.com for Google, else the issuer host).
func ProviderOf(issuer string) string {
	u, err := url.Parse(issuer)
	if err != nil {
		return issuer
	}
	if googleHost.MatchString(u.Host) {
		return "google.com"
	}
	return u.Host
}

type discovery struct {
	issuer, authorization, token, jwks string
}
type discoEntry struct {
	at int64
	d  discovery
}
type jwksEntry struct {
	at   int64
	keys []*doc.Map
}

var (
	oidcMu     sync.Mutex
	discoCache = map[string]discoEntry{}
	jwksCache  = map[string]jwksEntry{}
	oidcClient = &http.Client{Timeout: 30 * time.Second}
)

func fetchJSON(u string) (*doc.Map, error) {
	r, err := oidcClient.Get(u)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode < 200 || r.StatusCode > 299 {
		return nil, fmt.Errorf("HTTP %d", r.StatusCode)
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	return doc.ParseMap(b)
}

func discover(issuer string) (discovery, error) {
	oidcMu.Lock()
	c, ok := discoCache[issuer]
	oidcMu.Unlock()
	if ok && time.Now().UnixMilli()-c.at < 3_600_000 {
		return c.d, nil
	}
	j, err := fetchJSON(strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration")
	if err != nil {
		return discovery{}, fmt.Errorf("OIDC discovery failed: %w", err)
	}
	d := discovery{j.Str("issuer"), j.Str("authorization_endpoint"), j.Str("token_endpoint"), j.Str("jwks_uri")}
	oidcMu.Lock()
	discoCache[issuer] = discoEntry{time.Now().UnixMilli(), d}
	oidcMu.Unlock()
	return d, nil
}

func jwk(uri, kid string) (*doc.Map, error) {
	for fresh := false; ; fresh = true {
		oidcMu.Lock()
		c, ok := jwksCache[uri]
		oidcMu.Unlock()
		if !ok || fresh || time.Now().UnixMilli()-c.at > 3_600_000 {
			j, err := fetchJSON(uri)
			if err != nil {
				return nil, fmt.Errorf("JWKS fetch failed: %w", err)
			}
			c = jwksEntry{at: time.Now().UnixMilli()}
			for _, k := range j.Arr("keys") {
				if m, ok := k.(*doc.Map); ok {
					c.keys = append(c.keys, m)
				}
			}
			oidcMu.Lock()
			jwksCache[uri] = c
			oidcMu.Unlock()
		}
		for _, k := range c.keys {
			if (kid != "" && k.Str("kid") == kid) || (kid == "" && k.Str("kty") == "RSA") {
				return k, nil
			}
		}
		if fresh {
			return nil, errors.New("OIDC: no JWKS key for the token")
		}
	}
}

func verifyRS256(token string, d discovery, o *OidcConfig, nonce string) (*doc.Map, error) {
	p := strings.Split(token, ".")
	if len(p) < 3 || p[0] == "" || p[1] == "" || p[2] == "" {
		return nil, errors.New("OIDC: malformed id_token")
	}
	head, err := doc.ParseMap(doc.B64Decode(p[0]))
	if err != nil {
		return nil, err
	}
	if head.Str("alg") != "RS256" {
		return nil, fmt.Errorf("OIDC: unsupported alg %v", head.Val("alg"))
	}
	k, err := jwk(d.jwks, head.Str("kid"))
	if err != nil {
		return nil, err
	}
	if k.Str("kty") != "RSA" {
		return nil, errors.New("OIDC: not an RSA key")
	}
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(doc.B64Decode(k.Str("n"))), E: int(new(big.Int).SetBytes(doc.B64Decode(k.Str("e"))).Int64())}
	sum := sha256.Sum256([]byte(p[0] + "." + p[1]))
	if rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], doc.B64Decode(p[2])) != nil {
		return nil, errors.New("OIDC: bad id_token signature")
	}
	c, err := doc.ParseMap(doc.B64Decode(p[1]))
	if err != nil {
		return nil, err
	}
	iss := c.Val("iss")
	if !(iss == d.issuer || iss == o.Issuer || (ProviderOf(o.Issuer) == "google.com" && (iss == "accounts.google.com" || iss == "https://accounts.google.com"))) {
		return nil, errors.New("OIDC: wrong issuer")
	}
	aud := []any{c.Val("aud")}
	if a := c.Arr("aud"); a != nil {
		aud = a
	}
	found := false
	for _, x := range aud {
		found = found || x == o.ClientID
	}
	if !found {
		return nil, errors.New("OIDC: wrong audience")
	}
	if exp, ok := c.NumOK("exp"); !ok || exp*1000 < float64(time.Now().UnixMilli()-60_000) {
		return nil, errors.New("OIDC: id_token expired")
	}
	if c.Val("nonce") != nonce {
		return nil, errors.New("OIDC: nonce mismatch")
	}
	if c.Val("email_verified") != true && c.Val("email_verified") != "true" {
		return nil, errors.New("OIDC: e-mail not verified")
	}
	if o.AllowedDomain != nil && *o.AllowedDomain != "" && c.Val("hd") != *o.AllowedDomain {
		return nil, errors.New("OIDC: domain not allowed")
	}
	_, okS := c.StrOK("sub")
	_, okE := c.StrOK("email")
	if !okS || !okE {
		return nil, errors.New("OIDC: sub / email missing")
	}
	return c, nil
}

func resetGone(why string) *AuthHTTPResponse {
	if why == "expired" {
		return authFail(410, "auth/expired", "The reset link has expired.")
	}
	return authFail(410, "auth/invalid-reset-token", "The reset link is invalid or was already used.")
}

func safeNext(n string, has bool) any {
	if has && n != "" && strings.HasPrefix(n, "/") && !strings.HasPrefix(n, "//") && !strings.Contains(n, "\\") {
		return n
	}
	return nil
}

func parseBody(b []byte) *doc.Map {
	if len(b) == 0 {
		return doc.New()
	}
	m, err := doc.ParseMap(b)
	if err != nil {
		return doc.New()
	}
	return m
}

// HandleAuthHTTP serves /auth/* (see auth-http.ts). A mail failure never shows in the answer.
func HandleAuthHTTP(req AuthHTTPRequest, a *Auth, o AuthHTTPOptions) *AuthHTTPResponse {
	rawPath, rawQ, _ := strings.Cut(req.URL, "?")
	i := strings.Index(rawPath, "/auth/")
	route := ""
	if i >= 0 {
		route = strings.TrimSuffix(rawPath[i+5:], "/")
	}
	m := strings.ToUpper(req.Method)
	query := doc.ParseSearch(rawQ)
	consoleURL := strings.TrimSuffix(o.ConsoleURL, "/")
	if m == "OPTIONS" {
		return &AuthHTTPResponse{204, map[string]string{"Allow": "GET, POST, OPTIONS"}, nil}
	}
	body := doc.New()
	if m == "POST" {
		body = parseBody(req.Body)
	}
	str := func(k string) string { return body.Str(k) }
	out, err := func() (*AuthHTTPResponse, error) {
		switch m + " " + route {
		case "GET /config":
			var oidc any
			if o.Oidc != nil {
				oidc = doc.Obj("label", o.Oidc.Label)
			}
			return authJSON(200, doc.Obj("provider", "local", "passwordReset", o.SendMail != nil, "oidc", oidc)), nil
		case "POST /login":
			email, err := NormEmail(str("email"))
			if err != nil {
				return authFail(400, "auth/invalid-credential", "The supplied auth credential is incorrect."), nil
			}
			k := req.IP + "|" + email
			if limited(k) {
				return authFail(400, "auth/too-many-requests", "Too many failed sign-in attempts. Try again later."), nil
			}
			user, err := a.CheckPassword(email, str("password"))
			if err != nil {
				if AuthErrorCode(err) == "auth/invalid-credential" {
					failed(k)
				}
				return nil, err
			}
			failMu.Lock()
			delete(fails, k)
			failMu.Unlock()
			p, err := a.Issue(user, "")
			if err != nil {
				return nil, err
			}
			return authJSON(200, p), nil
		case "POST /refresh":
			p, err := a.Refresh(str("refreshToken"))
			if err != nil {
				return nil, err
			}
			return authJSON(200, p), nil
		case "POST /logout":
			if err := a.Logout(str("refreshToken")); err != nil {
				return nil, err
			}
			return noContent(), nil
		case "POST /reset/request":
			email := str("email")
			if o.SendMail != nil {
				// in the background: the answer never tells whether the account exists
				go func() {
					link, err := a.GeneratePasswordResetLink(email)
					if err == nil {
						var to string
						if to, err = NormEmail(email); err == nil {
							err = o.SendMail(to, "Reset your OPM password", "Open this link within 1 hour to choose a new password:\n\n"+link+"\n\nIf you did not ask for this, ignore this e-mail.")
						}
					}
					if err != nil && AuthErrorCode(err) == "" {
						Log.Warn("password reset mail failed", "err", err.Error())
					}
				}()
			}
			return noContent(), nil
		case "GET /reset/check":
			email, why, err := a.CheckReset(query.Get("token"))
			if err != nil {
				return nil, err
			}
			if why != "" {
				return resetGone(why), nil
			}
			return authJSON(200, doc.Obj("email", email)), nil
		case "POST /reset/confirm":
			pw := str("password")
			if doc.JSLen(pw) < 8 {
				return authFail(400, "auth/weak-password", "The password must be at least 8 characters."), nil
			}
			_, why, err := a.CheckReset(str("token"))
			if err != nil {
				return nil, err
			}
			if why != "" {
				return resetGone(why), nil
			}
			r, err := a.ConfirmReset(str("token"), pw)
			if err != nil {
				return nil, err
			}
			if r == "ok" {
				return noContent(), nil
			}
			return resetGone(r), nil
		case "GET /oidc/start":
			if o.Oidc == nil {
				return authFail(404, "auth/operation-not-allowed", "single sign-on is not configured"), nil
			}
			d, err := discover(o.Oidc.Issuer)
			if err != nil {
				return nil, err
			}
			state, nonce, verifier := RandomToken(), RandomToken(), RandomToken()
			next, has := query.Lookup("next")
			if err := a.Store.Doc("authOidcState/" + Sha256Hex(state)).Set(doc.Obj("verifier", verifier, "nonce", nonce, "next", safeNext(next, has), "expiresAt", doc.IsoPlus(10*60_000))); err != nil {
				return nil, err
			}
			base, q0, _ := strings.Cut(d.authorization, "?")
			sp := doc.ParseSearch(q0)
			sum := sha256.Sum256([]byte(verifier))
			sp.Set("response_type", "code")
			sp.Set("client_id", o.Oidc.ClientID)
			sp.Set("redirect_uri", o.Oidc.RedirectURI)
			sp.Set("scope", "openid email profile")
			sp.Set("state", state)
			sp.Set("nonce", nonce)
			sp.Set("code_challenge", doc.B64URL(sum[:]))
			sp.Set("code_challenge_method", "S256")
			if o.Oidc.AllowedDomain != nil && *o.Oidc.AllowedDomain != "" {
				sp.Set("hd", *o.Oidc.AllowedDomain)
			}
			return redirect(base + "?" + sp.String()), nil
		case "GET /oidc/callback":
			return oidcCallback(query, a, o, consoleURL)
		case "POST /oidc/exchange":
			code := str("code")
			if code == "" {
				code = "-"
			}
			ref := a.Store.Doc("authOidcCode/" + Sha256Hex(code))
			uid, err := RunTx(a.Store, func(tx *Tx) (string, error) {
				s, err := tx.Get(ref)
				if err != nil || !s.Exists() {
					return "", err
				}
				c := s.Data()
				tx.Delete(ref)
				if c.Str("expiresAt") < doc.NowISO() {
					return "", nil
				}
				return c.Str("uid"), nil
			})
			if err != nil {
				return nil, err
			}
			if uid == "" {
				return authFail(400, "auth/invalid-oidc-code", "The sign-in code is invalid or expired."), nil
			}
			s, err := a.Store.Doc("authUsers/" + uid).Get()
			if err != nil {
				return nil, err
			}
			if !s.Exists() {
				return authFail(400, "auth/user-not-found", "There is no user record corresponding to the provided identifier."), nil
			}
			user := s.Data()
			if user.Truthy("disabled") {
				return authFail(400, "auth/user-disabled", "The user account has been disabled by an administrator."), nil
			}
			p, err := a.Issue(user, "")
			if err != nil {
				return nil, err
			}
			return authJSON(200, p), nil
		}
		return authFail(404, "not-found", "no auth endpoint "+m+" "+route), nil
	}()
	if err != nil {
		var ae *AuthError
		if errors.As(err, &ae) {
			return authFail(400, ae.Code, ae.Message)
		}
		Log.Error("auth endpoint failed", "route", route, "err", err.Error())
		return authFail(500, "internal", "internal error")
	}
	return out
}

func oidcCallback(u *doc.Search, a *Auth, o AuthHTTPOptions, consoleURL string) (*AuthHTTPResponse, error) {
	next := ""
	back := func(frag string) *AuthHTTPResponse {
		q := ""
		if next != "" {
			q = "?next=" + doc.EncodeURIComponent(next)
		}
		return redirect(consoleURL + "/login" + q + "#" + frag)
	}
	if o.Oidc == nil {
		return back("oidcError=not-configured"), nil
	}
	cfg := o.Oidc
	if e := u.Get("error"); e != "" {
		return back("oidcError=" + doc.EncodeURIComponent(e)), nil
	}
	state, code := u.Get("state"), u.Get("code")
	key := state
	if key == "" {
		key = "-"
	}
	sref := a.Store.Doc("authOidcState/" + Sha256Hex(key))
	st, err := RunTx(a.Store, func(tx *Tx) (*doc.Map, error) {
		s, err := tx.Get(sref)
		if err != nil || !s.Exists() {
			return nil, err
		}
		tx.Delete(sref)
		v := s.Data()
		if v.Str("expiresAt") < doc.NowISO() {
			return nil, nil
		}
		return v, nil
	})
	if err != nil {
		return nil, err
	}
	next = st.Str("next")
	if st == nil || code == "" {
		return back("oidcError=invalid-state"), nil
	}
	r, err := func() (*AuthHTTPResponse, error) {
		d, err := discover(cfg.Issuer)
		if err != nil {
			return nil, err
		}
		form := &doc.Search{}
		for _, kv := range [][2]string{{"grant_type", "authorization_code"}, {"code", code}, {"redirect_uri", cfg.RedirectURI}, {"client_id", cfg.ClientID}, {"client_secret", cfg.ClientSecret}, {"code_verifier", st.Str("verifier")}} {
			form.Set(kv[0], kv[1])
		}
		rq, err := http.NewRequest(http.MethodPost, d.token, strings.NewReader(form.String()))
		if err != nil {
			return nil, err
		}
		rq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rq.Header.Set("Accept", "application/json")
		rs, err := oidcClient.Do(rq)
		if err != nil {
			return nil, err
		}
		defer rs.Body.Close()
		if rs.StatusCode < 200 || rs.StatusCode > 299 {
			return nil, fmt.Errorf("OIDC token endpoint: HTTP %d", rs.StatusCode)
		}
		raw, err := io.ReadAll(rs.Body)
		if err != nil {
			return nil, err
		}
		tok, err := doc.ParseMap(raw)
		if err != nil {
			return nil, err
		}
		if tok.Str("id_token") == "" {
			return nil, errors.New("OIDC: no id_token")
		}
		c, err := verifyRS256(tok.Str("id_token"), d, cfg, st.Str("nonce"))
		if err != nil {
			return nil, err
		}
		provider := ProviderOf(cfg.Issuer)
		fk := FedKey(provider)
		email, err := NormEmail(c.Str("email"))
		if err != nil {
			return nil, err
		}
		user, err := a.UserByEmail(email)
		if err != nil {
			return nil, err
		}
		if user == nil {
			// like a Google sign-in without an OPM account: an auth user exists, /v1/me answers "no account"
			user, err = a.CreateUserDoc(CreateUserInput{Email: email, EmailVerified: true, DisplayName: c.StrPtr("name"), Providers: []string{provider}, Federated: doc.Obj(fk, c.Str("sub"))})
			if err != nil {
				return nil, err
			}
		} else {
			known := user.Map("federated").Str(fk)
			if known != "" && known != c.Str("sub") {
				return nil, errors.New("OIDC: the e-mail is linked to a different identity")
			}
			if known == "" {
				if err := a.LinkFederated(user.Str("uid"), provider, c.Str("sub")); err != nil {
					return nil, err
				}
			}
		}
		if user.Truthy("disabled") {
			return back("oidcError=user-disabled"), nil
		}
		one := RandomToken()
		if err := a.Store.Doc("authOidcCode/" + Sha256Hex(one)).Set(doc.Obj("uid", user.Str("uid"), "expiresAt", doc.IsoPlus(2*60_000))); err != nil {
			return nil, err
		}
		return back("oidc=" + one), nil
	}()
	if err != nil {
		Log.Warn("OIDC sign-in failed", "err", err.Error())
		return back("oidcError=sign-in-failed"), nil
	}
	return r, nil
}
