package driver

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/sign"
)

// Token lifetimes of the local auth.
const (
	IDTokenTTLSec = 3600
	RefreshTTLMs  = 30 * 86_400_000
	ResetTTLMs    = 3_600_000
	authISS       = "opm-local"
	authAUD       = "opm"
	alnumChars    = idChars
	dummyHash     = "$argon2id$v=19$m=19456,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

// Sha256Hex is sha256(s) as lowercase hex.
func Sha256Hex(s string) string {
	x := sha256.Sum256([]byte(s))
	return hex.EncodeToString(x[:])
}

// RandomToken is 32 random bytes, base64url.
func RandomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return doc.B64URL(b)
}

func nowSec() int64 { return time.Now().Unix() }

func b64obj(m *doc.Map) string { return doc.B64URL([]byte(doc.MarshalString(m))) }

// AuthUser is the AuthAdmin view of a user.
type AuthUser struct {
	UID                  string
	Email                string
	DisplayName          *string
	Disabled             bool
	EmailVerified        bool
	TokensValidAfterTime string
	HasPassword          bool
	Providers            []string
}

// DecodedIDToken is verifyIdToken's result.
type DecodedIDToken struct {
	UID   string
	Email *string
	Iat   int64
	Exp   int64
}

// TokenPair is a session (JSON key order as TS).
type TokenPair struct {
	IDToken      string
	RefreshToken string
	ExpiresIn    int
	UID          string
	Email        string
}

// JSValue implements doc.JSValue.
func (t *TokenPair) JSValue() any {
	return doc.Obj("idToken", t.IDToken, "refreshToken", t.RefreshToken, "expiresIn", t.ExpiresIn, "uid", t.UID, "email", t.Email)
}

// CreateUserInput of CreateUser. Nil pointers are undefined.
type CreateUserInput struct {
	UID           *string
	Email         string
	Password      *string
	DisplayName   *string
	EmailVerified bool
	Disabled      bool
	Providers     []string
	Federated     *doc.Map
}

// UpdateUserInput of UpdateUser. Nil pointers are undefined.
type UpdateUserInput struct {
	Email         *string
	Password      *string
	DisplayName   *string
	Disabled      *bool
	EmailVerified *bool
}

// Ptr returns a pointer to v (optional fields of the inputs).
func Ptr[T any](v T) *T { return &v }

var invalidEmail = &AuthError{"auth/invalid-email", "The email address is improperly formatted."}
var userNotFound = &AuthError{"auth/user-not-found", "There is no user record corresponding to the provided identifier."}

// NormEmail is normEmail: trim, lower case, shape check.
func NormEmail(e string) (string, error) {
	v := strings.ToLower(doc.JSTrim(e))
	at := strings.IndexByte(v, '@')
	bad := v == "" || at <= 0 || at == len(v)-1 || strings.Count(v, "@") != 1 || strings.IndexFunc(v, doc.IsJSSpace) >= 0 || doc.JSLen(v) > 320
	if bad {
		return "", invalidEmail
	}
	return v, nil
}

func toUser(d *doc.Map) *AuthUser {
	return &AuthUser{
		UID:                  d.Str("uid"),
		Email:                d.Str("email"),
		DisplayName:          d.StrPtr("displayName"),
		Disabled:             d.Truthy("disabled"),
		EmailVerified:        d.Truthy("emailVerified"),
		TokensValidAfterTime: time.Unix(d.Int("validSinceSec"), 0).UTC().Format(http.TimeFormat),
		HasPassword:          d.Truthy("passwordHash"),
		Providers:            append([]string{}, d.Strs("providers")...),
	}
}

// HashPassword is hashPassword: argon2id, the encoded format of hash-wasm.
func HashPassword(pw string) string {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		panic(err)
	}
	key := argon2.IDKey([]byte(pw), salt, 2, 19456, 1, 32)
	enc := base64.RawStdEncoding.EncodeToString
	return "$argon2id$v=19$m=19456,t=2,p=1$" + enc(salt) + "$" + enc(key)
}

// Auth is the built-in user store (users in the global DB, argon2id, HS256 ID tokens, rotating refresh tokens).
type Auth struct {
	Store      *Store
	dataDir    string
	ConsoleURL string
	keyMu      sync.Mutex
	key        []byte
}

func (a *Auth) secret() ([]byte, error) {
	a.keyMu.Lock()
	defer a.keyMu.Unlock()
	if a.key == nil {
		k, err := LoadSecret("OPM_AUTH_SECRET", a.dataDir, "auth-jwt.key")
		if err != nil {
			return nil, err
		}
		a.key = k
	}
	return a.key, nil
}
func (a *Auth) users() *CollectionRef { return a.Store.Collection("authUsers") }
func (a *Auth) emailRef(email string) *DocRef {
	return a.Store.Doc("authEmails/" + Sha256Hex(email))
}
func (a *Auth) userDoc(uid string) (*doc.Map, error) {
	if uid == "" || strings.Contains(uid, "/") || doc.JSLen(uid) > 128 {
		return nil, nil
	}
	s, err := a.users().Doc(uid).Get()
	if err != nil {
		return nil, err
	}
	return s.Data(), nil
}
func (a *Auth) needUser(uid string) (*doc.Map, error) {
	d, err := a.userDoc(uid)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, userNotFound
	}
	return d, nil
}

// UserByEmail is the user document by e-mail (nil when none).
func (a *Auth) UserByEmail(email string) (*doc.Map, error) {
	e, err := NormEmail(email)
	if err != nil {
		return nil, err
	}
	s, err := a.emailRef(e).Get()
	if err != nil || !s.Exists() {
		return nil, err
	}
	return a.userDoc(s.Data().Str("uid"))
}

func (a *Auth) mac(msg string) ([]byte, error) {
	k, err := a.secret()
	if err != nil {
		return nil, err
	}
	m := hmac.New(sha256.New, k)
	m.Write([]byte(msg))
	return m.Sum(nil), nil
}

// SignIDToken signs an ID token for a user document.
func (a *Auth) SignIDToken(u *doc.Map) (string, error) {
	iat := nowSec()
	head := b64obj(doc.Obj("alg", "HS256", "typ", "JWT"))
	body := b64obj(doc.Obj("iss", authISS, "aud", authAUD, "sub", u.Str("uid"), "uid", u.Str("uid"), "email", u.Val("email"), "iat", iat, "exp", iat+IDTokenTTLSec))
	sig, err := a.mac(head + "." + body)
	if err != nil {
		return "", err
	}
	return head + "." + body + "." + doc.B64URL(sig), nil
}

// VerifyIDToken is verifyIdToken.
func (a *Auth) VerifyIDToken(token string, checkRevoked bool) (*DecodedIDToken, error) {
	badTok := &AuthError{"auth/argument-error", "Decoding ID token failed: invalid token."}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, badTok
	}
	head, err1 := doc.ParseMap(doc.B64Decode(parts[0]))
	p, err2 := doc.ParseMap(doc.B64Decode(parts[1]))
	if err1 != nil || err2 != nil {
		return nil, badTok
	}
	if head.Val("alg") != "HS256" {
		return nil, badTok
	}
	want, err := a.mac(parts[0] + "." + parts[1])
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare(want, doc.B64Decode(parts[2])) != 1 {
		return nil, badTok
	}
	iat, okI := p.NumOK("iat")
	exp, okE := p.NumOK("exp")
	sub, okS := p.StrOK("sub")
	if p.Val("iss") != authISS || p.Val("aud") != authAUD || !okS || !okI || !okE {
		return nil, badTok
	}
	if exp <= float64(nowSec()) {
		return nil, &AuthError{"auth/id-token-expired", "Firebase ID token has expired."}
	}
	u, err := a.needUser(sub)
	if err != nil {
		return nil, err
	}
	if u.Truthy("disabled") {
		return nil, &AuthError{"auth/user-disabled", "The user record is disabled."}
	}
	if checkRevoked && iat < u.Num("validSinceSec") {
		return nil, &AuthError{"auth/id-token-revoked", "The Firebase ID token has been revoked."}
	}
	return &DecodedIDToken{UID: sub, Email: p.StrPtr("email"), Iat: int64(iat), Exp: int64(exp)}, nil
}

// Issue starts a session: ID token + refresh token of a new family ("" = new).
func (a *Auth) Issue(u *doc.Map, family string) (*TokenPair, error) {
	if family == "" {
		family = RandomToken()
	}
	rt := RandomToken()
	err := a.Store.Doc("authRefresh/" + Sha256Hex(rt)).Set(doc.Obj("uid", u.Str("uid"), "family", family, "createdSec", nowSec(), "expiresAt", doc.IsoPlus(RefreshTTLMs)))
	if err != nil {
		return nil, err
	}
	id, err := a.SignIDToken(u)
	if err != nil {
		return nil, err
	}
	return &TokenPair{id, rt, IDTokenTTLSec, u.Str("uid"), u.Str("email")}, nil
}

// Refresh rotates a refresh token; reuse of an already rotated token revokes its whole family.
func (a *Auth) Refresh(token string) (*TokenPair, error) {
	invalid := &AuthError{"auth/invalid-refresh-token", "Invalid refresh token."}
	if token == "" {
		return nil, invalid
	}
	ref := a.Store.Doc("authRefresh/" + Sha256Hex(token))
	next := RandomToken()
	type res struct {
		err, family string
		u           *doc.Map
	}
	r, err := RunTx(a.Store, func(tx *Tx) (res, error) {
		s, err := tx.Get(ref)
		if err != nil {
			return res{}, err
		}
		if !s.Exists() {
			return res{err: "invalid"}, nil
		}
		d := s.Data()
		if d.Truthy("rotatedAt") {
			return res{err: "reuse", family: d.Str("family")}, nil
		}
		if d.Str("expiresAt") < doc.NowISO() {
			return res{err: "invalid"}, nil
		}
		us, err := tx.Get(a.users().Doc(d.Str("uid")))
		if err != nil {
			return res{}, err
		}
		if !us.Exists() {
			return res{err: "invalid"}, nil
		}
		u := us.Data()
		if u.Truthy("disabled") {
			return res{err: "disabled"}, nil
		}
		if d.Num("createdSec") < u.Num("validSinceSec") {
			return res{err: "invalid"}, nil
		}
		tx.Update(ref, doc.Obj("rotatedAt", doc.NowISO()))
		tx.Set(a.Store.Doc("authRefresh/"+Sha256Hex(next)), doc.Obj("uid", u.Str("uid"), "family", d.Str("family"), "createdSec", nowSec(), "expiresAt", d.Str("expiresAt")))
		return res{u: u}, nil
	})
	if err != nil {
		return nil, err
	}
	if r.u != nil {
		id, err := a.SignIDToken(r.u)
		if err != nil {
			return nil, err
		}
		return &TokenPair{id, next, IDTokenTTLSec, r.u.Str("uid"), r.u.Str("email")}, nil
	}
	switch r.err {
	case "reuse":
		if err := a.deleteWhere("family", r.family); err != nil {
			return nil, err
		}
	case "disabled":
		return nil, &AuthError{"auth/user-disabled", "The user account has been disabled by an administrator."}
	}
	return nil, invalid
}

// Logout drops the family of a refresh token.
func (a *Auth) Logout(token string) error {
	if token == "" {
		return nil
	}
	s, err := a.Store.Doc("authRefresh/" + Sha256Hex(token)).Get()
	if err != nil || !s.Exists() {
		return err
	}
	return a.deleteWhere("family", s.Data().Str("family"))
}

func (a *Auth) deleteWhere(field, v string) error {
	for {
		q, err := a.Store.Collection("authRefresh").Where(field, "==", v).Select().Limit(400).Get()
		if err != nil {
			return err
		}
		if q.Empty() {
			return nil
		}
		b := a.Store.Batch()
		for _, d := range q.Docs {
			b.Delete(d.Ref())
		}
		if err := b.Commit(); err != nil {
			return err
		}
	}
}

// CheckPassword: e-mail + password → the user document (auth/invalid-credential | auth/user-disabled).
func (a *Auth) CheckPassword(email, password string) (*doc.Map, error) {
	u, err := a.UserByEmail(email)
	if err != nil {
		u = nil
	}
	hash := u.Str("passwordHash")
	stored := hash != ""
	if !stored {
		hash = dummyHash
	}
	ok, err := sign.VerifyPHC(hash, password)
	if err != nil {
		ok = false
	}
	if u == nil || !stored || !ok {
		return nil, &AuthError{"auth/invalid-credential", "The supplied auth credential is incorrect."}
	}
	if u.Truthy("disabled") {
		return nil, &AuthError{"auth/user-disabled", "The user account has been disabled by an administrator."}
	}
	return u, nil
}

// GetUser is getUser.
func (a *Auth) GetUser(uid string) (*AuthUser, error) {
	d, err := a.needUser(uid)
	if err != nil {
		return nil, err
	}
	return toUser(d), nil
}

// GetUserByEmail is getUserByEmail.
func (a *Auth) GetUserByEmail(email string) (*AuthUser, error) {
	u, err := a.UserByEmail(email)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, userNotFound
	}
	return toUser(u), nil
}

// GetUsers: missing uids are left out.
func (a *Auth) GetUsers(uids []string) ([]*AuthUser, error) {
	out := []*AuthUser{}
	for _, id := range uids {
		d, err := a.userDoc(id)
		if err != nil {
			return nil, err
		}
		if d != nil {
			out = append(out, toUser(d))
		}
	}
	return out, nil
}

var uidRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func weakPw() error {
	return &AuthError{"auth/invalid-password", "The password must be a string with at least 6 characters."}
}

// CreateUserDoc is createUserDoc: createUser + federated identities (OIDC sign-up).
func (a *Auth) CreateUserDoc(u CreateUserInput) (*doc.Map, error) {
	email, err := NormEmail(u.Email)
	if err != nil {
		return nil, err
	}
	if u.Password != nil && doc.JSLen(*u.Password) < 6 {
		return nil, weakPw()
	}
	if u.UID != nil && !uidRe.MatchString(*u.UID) {
		return nil, &AuthError{"auth/invalid-uid", "The uid must be a non-empty string with at most 128 characters."}
	}
	var hash any
	var provs []string
	if u.Password != nil {
		hash = HashPassword(*u.Password)
		provs = append(provs, "password")
	}
	uid := RandomFrom(alnumChars, 28)
	if u.UID != nil {
		uid = *u.UID
	}
	d := doc.Obj("uid", uid, "email", email)
	if u.DisplayName != nil {
		d.Set("displayName", *u.DisplayName)
	}
	fed := u.Federated
	if fed == nil {
		fed = doc.New()
	}
	d.Set("disabled", u.Disabled).Set("emailVerified", u.EmailVerified).Set("passwordHash", hash).Set("validSinceSec", nowSec())
	d.Set("providers", uniq(provs, u.Providers)).Set("federated", fed).Set("createdAt", doc.NowISO())
	err = a.Store.RunTransaction(func(tx *Tx) error {
		s, err := tx.GetAll(a.users().Doc(uid), a.emailRef(email))
		if err != nil {
			return err
		}
		if s[0].Exists() {
			return &AuthError{"auth/uid-already-exists", "The user with the provided uid already exists."}
		}
		if s[1].Exists() {
			return &AuthError{"auth/email-already-exists", "The email address is already in use by another account."}
		}
		tx.Create(a.users().Doc(uid), d)
		tx.Create(a.emailRef(email), doc.Obj("uid", uid))
		return nil
	})
	if err != nil {
		return nil, err
	}
	n, _ := doc.ToMap(d)
	return n, nil
}

// CreateUser is createUser.
func (a *Auth) CreateUser(u CreateUserInput) (*AuthUser, error) {
	d, err := a.CreateUserDoc(u)
	if err != nil {
		return nil, err
	}
	return toUser(d), nil
}

// UpdateUser is updateUser.
func (a *Auth) UpdateUser(uid string, u UpdateUserInput) (*AuthUser, error) {
	if u.Password != nil && doc.JSLen(*u.Password) < 6 {
		return nil, weakPw()
	}
	var email string
	if u.Email != nil {
		e, err := NormEmail(*u.Email)
		if err != nil {
			return nil, err
		}
		email = e
	}
	hash := ""
	if u.Password != nil {
		hash = HashPassword(*u.Password)
	}
	out, err := RunTx(a.Store, func(tx *Tx) (*doc.Map, error) {
		ref := a.users().Doc(uid)
		s, err := tx.Get(ref)
		if err != nil {
			return nil, err
		}
		if !s.Exists() {
			return nil, userNotFound
		}
		d := s.Data()
		change := u.Email != nil && email != d.Str("email")
		if change {
			es, err := tx.Get(a.emailRef(email))
			if err != nil {
				return nil, err
			}
			if es.Exists() {
				return nil, &AuthError{"auth/email-already-exists", "The email address is already in use by another account."}
			}
		}
		next := d.Shallow()
		if change {
			next.Set("email", email)
			tx.Delete(a.emailRef(d.Str("email")))
			tx.Create(a.emailRef(email), doc.Obj("uid", uid))
		}
		if u.DisplayName != nil {
			next.Set("displayName", *u.DisplayName)
		}
		if u.Disabled != nil {
			next.Set("disabled", *u.Disabled)
		}
		if u.EmailVerified != nil {
			next.Set("emailVerified", *u.EmailVerified)
		}
		if u.Password != nil {
			next.Set("passwordHash", hash).Set("validSinceSec", nowSec())
			if !contains(next.Strs("providers"), "password") {
				next.Set("providers", append([]any{"password"}, next.Arr("providers")...))
			}
		}
		tx.Set(ref, next)
		return next, nil
	})
	if err != nil {
		return nil, err
	}
	if u.Password != nil {
		if err := a.deleteWhere("uid", uid); err != nil {
			return nil, err
		}
	}
	return toUser(out), nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// ConfirmReset sets a password from a reset token: "ok" | "invalid" | "expired".
func (a *Auth) ConfirmReset(token, password string) (string, error) {
	if token == "" {
		token = "-"
	}
	ref := a.Store.Doc("authReset/" + Sha256Hex(token))
	hash := HashPassword(password)
	type res struct{ state, uid string }
	r, err := RunTx(a.Store, func(tx *Tx) (res, error) {
		s, err := tx.Get(ref)
		if err != nil {
			return res{}, err
		}
		if !s.Exists() {
			return res{state: "invalid"}, nil
		}
		d := s.Data()
		if d.Truthy("usedAt") {
			return res{state: "invalid"}, nil
		}
		if d.Str("expiresAt") < doc.NowISO() {
			return res{state: "expired"}, nil
		}
		us, err := tx.Get(a.users().Doc(d.Str("uid")))
		if err != nil {
			return res{}, err
		}
		if !us.Exists() {
			return res{state: "invalid"}, nil
		}
		u := us.Data()
		provs := u.Arr("providers")
		if !contains(u.Strs("providers"), "password") {
			provs = append([]any{"password"}, provs...)
		}
		tx.Update(ref, doc.Obj("usedAt", doc.NowISO()))
		tx.Update(us.Ref(), doc.Obj("passwordHash", hash, "validSinceSec", nowSec(), "providers", provs))
		return res{state: "ok", uid: d.Str("uid")}, nil
	})
	if err != nil {
		return "", err
	}
	if r.state == "ok" {
		if err := a.deleteWhere("uid", r.uid); err != nil {
			return "", err
		}
	}
	return r.state, nil
}

// CheckReset: a reset token's e-mail, or why it cannot be used ("invalid" | "expired").
func (a *Auth) CheckReset(token string) (email, why string, err error) {
	if token == "" {
		return "", "invalid", nil
	}
	s, err := a.Store.Doc("authReset/" + Sha256Hex(token)).Get()
	if err != nil {
		return "", "", err
	}
	if !s.Exists() {
		return "", "invalid", nil
	}
	r := s.Data()
	if r.Truthy("usedAt") {
		return "", "invalid", nil
	}
	if r.Str("expiresAt") < doc.NowISO() {
		return "", "expired", nil
	}
	return r.Str("email"), "", nil
}

// DeleteUser is deleteUser.
func (a *Auth) DeleteUser(uid string) error {
	err := a.Store.RunTransaction(func(tx *Tx) error {
		ref := a.users().Doc(uid)
		s, err := tx.Get(ref)
		if err != nil {
			return err
		}
		if !s.Exists() {
			return userNotFound
		}
		tx.Delete(ref)
		tx.Delete(a.emailRef(s.Data().Str("email")))
		return nil
	})
	if err != nil {
		return err
	}
	return a.deleteWhere("uid", uid)
}

// RevokeRefreshTokens is revokeRefreshTokens.
func (a *Auth) RevokeRefreshTokens(uid string) error {
	if _, err := a.needUser(uid); err != nil {
		return err
	}
	if err := a.users().Doc(uid).Update(doc.Obj("validSinceSec", nowSec())); err != nil {
		return err
	}
	return a.deleteWhere("uid", uid)
}

// GeneratePasswordResetLink is generatePasswordResetLink.
func (a *Auth) GeneratePasswordResetLink(email string) (string, error) {
	u, err := a.UserByEmail(email)
	if err != nil {
		return "", err
	}
	if u == nil {
		return "", userNotFound
	}
	t := RandomToken()
	err = a.Store.Doc("authReset/" + Sha256Hex(t)).Set(doc.Obj("uid", u.Str("uid"), "email", u.Str("email"), "expiresAt", doc.IsoPlus(ResetTTLMs), "usedAt", nil, "createdAt", doc.NowISO()))
	if err != nil {
		return "", err
	}
	return a.ConsoleURL + "/reset-password?token=" + t, nil
}

// ListAllUsers is listAllUsers.
func (a *Auth) ListAllUsers() ([]*AuthUser, error) {
	q, err := a.users().Get()
	if err != nil {
		return nil, err
	}
	out := make([]*AuthUser, len(q.Docs))
	for i, d := range q.Docs {
		out[i] = toUser(d.Data())
	}
	return out, nil
}

// FedKey: map key of a federated provider (no dots: field paths).
func FedKey(provider string) string {
	return regexp.MustCompile(`[^A-Za-z0-9_-]`).ReplaceAllString(provider, "_")
}

// LinkFederated links a federated identity to a user (OIDC).
func (a *Auth) LinkFederated(uid, provider, sub string) error {
	return a.Store.RunTransaction(func(tx *Tx) error {
		ref := a.users().Doc(uid)
		s, err := tx.Get(ref)
		if err != nil || !s.Exists() {
			return err
		}
		d := s.Data()
		provs := d.Arr("providers")
		if !contains(d.Strs("providers"), provider) {
			provs = append(provs[:len(provs):len(provs)], provider)
		}
		tx.Update(ref, doc.Obj("federated."+FedKey(provider), sub, "providers", provs))
		return nil
	})
}
