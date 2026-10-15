// Package sign holds the site key (Ed25519), the signed request / response scheme of SPEC-final §4.4, the actor
// token (EdDSA JWT, §4.6), argon2id PHC verification of the access key and release signature checks (§19.1).
package sign

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"

	"opm/agent/internal/canonical"
	"opm/agent/internal/model"
)

// Header names.
const (
	HSite  = "X-OPM-Site"
	HTs    = "X-OPM-Ts"
	HNonce = "X-OPM-Nonce"
	HSig   = "X-OPM-Sig"
)

var b64url = base64.RawURLEncoding

// LoadOrCreateKey reads <path> (base64 std of the 32-byte seed) or creates it (0600).
func LoadOrCreateKey(path string) (ed25519.PrivateKey, bool, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, false, fmt.Errorf("site key %s: invalid content", path)
		}
		return ed25519.NewKeyFromSeed(seed), false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, err
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(seed)+"\n"), 0o600); err != nil {
		return nil, false, err
	}
	return ed25519.NewKeyFromSeed(seed), true, nil
}

// PublicKeyB64 is the wire form of a public key (raw 32 bytes, base64 std padded).
func PublicKeyB64(pub ed25519.PublicKey) string { return base64.StdEncoding.EncodeToString(pub) }

// ParsePublicKey decodes the wire form.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(s)
	}
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key")
	}
	return ed25519.PublicKey(b), nil
}

// Fingerprint = first 16 hex chars of sha256(raw public key).
func Fingerprint(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:])[:16]
}

// Nonce returns 16 random bytes, base64url without padding (22 chars).
func Nonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return b64url.EncodeToString(b)
}

// BodyHash is lowercase hex sha256 of the body (empty → e3b0…).
func BodyHash(body []byte) string { return canonical.Sha256Hex(body) }

// SignRequest returns the four X-OPM-* headers for a request.
func SignRequest(priv ed25519.PrivateKey, site, method, path string, body []byte, ts int64) map[string]string {
	nonce := Nonce()
	s := canonical.SignedRequestString(method, path, ts, nonce, BodyHash(body))
	sig := ed25519.Sign(priv, []byte(s))
	return map[string]string{HSite: site, HTs: strconv.FormatInt(ts, 10), HNonce: nonce, HSig: b64url.EncodeToString(sig)}
}

// VerifyError carries the 401 reason (clock | nonce | signature | unknown-site).
type VerifyError struct {
	Reason string
	Msg    string
}

func (e *VerifyError) Error() string { return e.Msg }

// ParsedRequest is the signature material of an incoming request.
type ParsedRequest struct {
	DeploymentID string
	SiteSlug     string
	Ts           int64
	Nonce        string
	Sig          []byte
}

// ParseHeaders reads the X-OPM-* headers (get = header getter).
func ParseHeaders(get func(string) string) (*ParsedRequest, error) {
	site := get(HSite)
	dep, slug, ok := strings.Cut(site, "/")
	if !ok || dep == "" || slug == "" {
		return nil, &VerifyError{"unknown-site", "missing or malformed " + HSite}
	}
	ts, err := strconv.ParseInt(get(HTs), 10, 64)
	if err != nil {
		return nil, &VerifyError{"clock", "missing or malformed " + HTs}
	}
	nonce := get(HNonce)
	if nonce == "" {
		return nil, &VerifyError{"nonce", "missing " + HNonce}
	}
	sig, err := b64url.DecodeString(get(HSig))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, &VerifyError{"signature", "missing or malformed " + HSig}
	}
	return &ParsedRequest{DeploymentID: dep, SiteSlug: slug, Ts: ts, Nonce: nonce, Sig: sig}, nil
}

// VerifyRequest checks clock (±60 s) and the signature. Nonce replay is checked by the caller.
func VerifyRequest(pub ed25519.PublicKey, p *ParsedRequest, method, path string, body []byte, now int64) error {
	if d := now - p.Ts; d > 60 || d < -60 {
		return &VerifyError{"clock", fmt.Sprintf("clock skew %d s (max 60 s)", d)}
	}
	s := canonical.SignedRequestString(method, path, p.Ts, p.Nonce, BodyHash(body))
	if !ed25519.Verify(pub, []byte(s), p.Sig) {
		return &VerifyError{"signature", "signature invalid"}
	}
	return nil
}

// SignResponse returns X-OPM-Site, X-OPM-Ts, X-OPM-Sig for a JSON peer response.
func SignResponse(priv ed25519.PrivateKey, site string, status int, requestNonce string, body []byte, ts int64) map[string]string {
	s := canonical.SignedResponseString(status, requestNonce, ts, BodyHash(body))
	return map[string]string{HSite: site, HTs: strconv.FormatInt(ts, 10), HSig: b64url.EncodeToString(ed25519.Sign(priv, []byte(s)))}
}

// VerifyResponse checks a signed peer response (clock ±300 s; the nonce binds it to the request).
func VerifyResponse(pub ed25519.PublicKey, get func(string) string, status int, requestNonce string, body []byte, now int64) error {
	ts, err := strconv.ParseInt(get(HTs), 10, 64)
	if err != nil {
		return errors.New("response not signed")
	}
	if d := now - ts; d > 300 || d < -300 {
		return fmt.Errorf("response clock skew %d s", d)
	}
	sig, err := b64url.DecodeString(get(HSig))
	if err != nil {
		return errors.New("response signature malformed")
	}
	s := canonical.SignedResponseString(status, requestNonce, ts, BodyHash(body))
	if !ed25519.Verify(pub, []byte(s), sig) {
		return errors.New("response signature invalid")
	}
	return nil
}

// ───────────────────────── actor token (JWT EdDSA)

var jwtHeader = b64url.EncodeToString([]byte(`{"alg":"EdDSA","typ":"JWT"}`))

// MakeActorToken signs the claims.
func MakeActorToken(priv ed25519.PrivateKey, c model.ActorTokenClaims) string {
	payload, _ := json.Marshal(c)
	si := jwtHeader + "." + b64url.EncodeToString(payload)
	return si + "." + b64url.EncodeToString(ed25519.Sign(priv, []byte(si)))
}

// ParseActorToken verifies the signature and returns the claims (expiry is checked by the caller).
func ParseActorToken(pub ed25519.PublicKey, tok string) (*model.ActorTokenClaims, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed token")
	}
	hb, err := b64url.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("malformed token header")
	}
	var h struct {
		Alg string `json:"alg"`
	}
	if json.Unmarshal(hb, &h) != nil || h.Alg != "EdDSA" {
		return nil, errors.New("unsupported token alg")
	}
	sig, err := b64url.DecodeString(parts[2])
	if err != nil || !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		return nil, errors.New("token signature invalid")
	}
	pb, err := b64url.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("malformed token payload")
	}
	var c model.ActorTokenClaims
	if err := json.Unmarshal(pb, &c); err != nil {
		return nil, errors.New("malformed token claims")
	}
	return &c, nil
}

// ───────────────────────── access key (argon2id PHC)

// NormalizeAccessKey upper-cases and keeps only A–Z 2–7.
func NormalizeAccessKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '2' && r <= '7') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// HashAccessKey makes the PHC string (m=65536, t=3, p=1, 16-byte salt, 32-byte key).
func HashAccessKey(normalized string) string {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	key := argon2.IDKey([]byte(normalized), salt, 3, 65536, 1, 32)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=1$%s$%s", enc.EncodeToString(salt), enc.EncodeToString(key))
}

// VerifyPHC checks a password against an argon2id PHC string (constant-time compare).
func VerifyPHC(phc, password string) (bool, error) {
	parts := strings.Split(phc, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("not an argon2id PHC string")
	}
	var v int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != 19 {
		return false, errors.New("unsupported argon2 version")
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, errors.New("bad argon2 parameters")
	}
	dec := func(s string) ([]byte, error) {
		if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
			return b, nil
		}
		return base64.StdEncoding.DecodeString(s)
	}
	salt, err := dec(parts[4])
	if err != nil {
		return false, errors.New("bad salt")
	}
	want, err := dec(parts[5])
	if err != nil || len(want) == 0 {
		return false, errors.New("bad hash")
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// VerifyRelease checks the release signature: base64url Ed25519 signature of the lowercase sha256 hex string.
func VerifyRelease(pubB64, shaHex, sigB64url string) error {
	pub, err := ParsePublicKey(pubB64)
	if err != nil {
		return err
	}
	sig, err := b64url.DecodeString(strings.TrimRight(sigB64url, "="))
	if err != nil {
		return errors.New("signature is not base64url")
	}
	if !ed25519.Verify(pub, []byte(strings.ToLower(strings.TrimSpace(shaHex))), sig) {
		return errors.New("release signature invalid")
	}
	return nil
}
