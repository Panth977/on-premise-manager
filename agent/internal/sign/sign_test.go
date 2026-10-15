package sign

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"opm/agent/internal/canonical"
	"opm/agent/internal/model"
)

func TestKeyAndRequest(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "site.key")
	k, created, err := LoadOrCreateKey(p)
	if err != nil || !created {
		t.Fatal(err, created)
	}
	k2, created, err := LoadOrCreateKey(p)
	if err != nil || created || !k.Equal(k2) {
		t.Fatal("reload failed")
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	pub := k.Public().(ed25519.PublicKey)
	if len(Fingerprint(pub)) != 16 {
		t.Fatal("fingerprint")
	}
	pk, err := ParsePublicKey(PublicKeyB64(pub))
	if err != nil || !pk.Equal(pub) {
		t.Fatal("pubkey roundtrip")
	}
	now := time.Now().Unix()
	body := []byte(`{"a":1}`)
	h := SignRequest(k, "dep/app", "post", "/v1/agent/heartbeat?x=1", body, now)
	if len(h[HNonce]) != 22 {
		t.Fatal("nonce len")
	}
	get := func(n string) string { return h[n] }
	pr, err := ParseHeaders(get)
	if err != nil || pr.DeploymentID != "dep" || pr.SiteSlug != "app" {
		t.Fatal(err)
	}
	if err := VerifyRequest(pub, pr, "POST", "/v1/agent/heartbeat?x=1", body, now); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRequest(pub, pr, "POST", "/v1/agent/heartbeat?x=2", body, now); err == nil || err.(*VerifyError).Reason != "signature" {
		t.Fatal("tampered path accepted")
	}
	if err := VerifyRequest(pub, pr, "POST", "/v1/agent/heartbeat?x=1", body, now+61); err == nil || err.(*VerifyError).Reason != "clock" {
		t.Fatal("clock accepted")
	}
	rh := SignResponse(k, "app", 200, pr.Nonce, body, now)
	hdr := http.Header{}
	for kk, v := range rh {
		hdr.Set(kk, v)
	}
	if err := VerifyResponse(pub, hdr.Get, 200, pr.Nonce, body, now); err != nil {
		t.Fatal(err)
	}
	if err := VerifyResponse(pub, hdr.Get, 200, "other", body, now); err == nil {
		t.Fatal("wrong nonce accepted")
	}
}

func TestVectors(t *testing.T) {
	raw, err := os.ReadFile("../../../shared/testdata/canonical-vectors.json")
	if err != nil {
		t.Skip("no vectors")
	}
	var v struct {
		Signing struct {
			SeedHex      string `json:"seedHex"`
			PublicKeyB64 string `json:"publicKeyB64"`
			Requests     []struct {
				Method, Path, Nonce, Body, StringToSign, Signature string
				Ts                                                 int64
			} `json:"requests"`
		} `json:"signing"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	seed, _ := hex.DecodeString(v.Signing.SeedHex)
	priv := ed25519.NewKeyFromSeed(seed)
	if PublicKeyB64(priv.Public().(ed25519.PublicKey)) != v.Signing.PublicKeyB64 {
		t.Fatal("public key mismatch")
	}
	if len(v.Signing.Requests) == 0 {
		t.Fatal("no request vectors")
	}
	for _, r := range v.Signing.Requests {
		s := canonical.SignedRequestString(r.Method, r.Path, r.Ts, r.Nonce, BodyHash([]byte(r.Body)))
		if s != r.StringToSign {
			t.Fatalf("string to sign mismatch:\n%q\n%q", s, r.StringToSign)
		}
		if base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(s))) != r.Signature {
			t.Fatal("signature mismatch")
		}
		pr := &ParsedRequest{Ts: r.Ts, Nonce: r.Nonce}
		pr.Sig, _ = base64.RawURLEncoding.DecodeString(r.Signature)
		if err := VerifyRequest(priv.Public().(ed25519.PublicKey), pr, r.Method, r.Path, []byte(r.Body), r.Ts); err != nil {
			t.Fatal(err)
		}
	}
}

func TestActorToken(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	c := model.ActorTokenClaims{Email: "ops@example.com", Sid: "S", Dep: "D", Site: "app", Iat: 1, Exp: 2}
	tok := MakeActorToken(priv, c)
	got, err := ParseActorToken(priv.Public().(ed25519.PublicKey), tok)
	if err != nil || *got != c {
		t.Fatal(err)
	}
	_, other, _ := ed25519.GenerateKey(nil)
	if _, err := ParseActorToken(other.Public().(ed25519.PublicKey), tok); err == nil {
		t.Fatal("foreign key accepted")
	}
}

func TestArgon(t *testing.T) {
	key := NormalizeAccessKey("abcd-efgh-ijkl-mnop-qrst-uvw2")
	if key != "ABCDEFGHIJKLMNOPQRSTUVW2" {
		t.Fatal(key)
	}
	phc := HashAccessKey(key)
	ok, err := VerifyPHC(phc, key)
	if err != nil || !ok {
		t.Fatal("verify failed", err)
	}
	ok, _ = VerifyPHC(phc, "ABCDEFGHIJKLMNOPQRSTUVW3")
	if ok {
		t.Fatal("wrong key accepted")
	}
}

func TestRelease(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	sha := "abc123"
	sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(sha)))
	if err := VerifyRelease(PublicKeyB64(pub), sha, sig); err != nil {
		t.Fatal(err)
	}
	if VerifyRelease(PublicKeyB64(pub), "abc124", sig) == nil {
		t.Fatal("bad accepted")
	}
}
