package apitest

import (
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"testing"

	"opm/agent/internal/canonical"
	"opm/agent/internal/cloud"
)

// Port of backend/test/01-auth.test.ts.
func TestAuth(t *testing.T) {
	t.Run("signing vectors (shared/testdata/canonical-vectors.json) verify with the backend verifier", func(t *testing.T) {
		v := Load(t, "canonical-vectors.json").At("signing")
		pub := v.At("publicKeyB64").Str()
		for _, r := range v.At("requests").Arr() {
			str := canonical.SignedRequestString(r.At("method").Str(), r.At("path").Str(), int64(r.At("ts").Num()), r.At("nonce").Str(), r.At("bodySha256Hex").Str())
			Eq(t, str, r.At("stringToSign"))
			Eq(t, cloud.VerifyEd25519(pub, []byte(str), r.At("signature").Str()), true, r.At("path").Str())
			Eq(t, cloud.VerifyEd25519(pub, []byte(str+"x"), r.At("signature").Str()), false)
		}
		// cross-check with the standard library directly
		pk, err := base64.StdEncoding.DecodeString(pub)
		NoErr(t, err)
		sig, err := base64.RawURLEncoding.DecodeString(v.At("requests.0.signature").Str())
		NoErr(t, err)
		True(t, ed25519.Verify(pk, []byte(v.At("requests.0.stringToSign").Str()), sig))
	})

	t.Run("router: every CLOUD_API route exists; unknown → 404; wrong method → 405", func(t *testing.T) {
		r := Raw(t, "GET", "/v1/definitely-not-here")
		Eq(t, r.Status, 404)
		Eq(t, r.J.At("error.code"), "not-found")
		True(t, r.Header("x-request-id") != "")
		True(t, r.Header("x-opm-server-time") != "")
		m := Raw(t, "PATCH", "/v1/not-found-pages/abc")
		Eq(t, m.Status, 405)
		Eq(t, m.J.At("error.code"), "method-not-allowed")
		put := Raw(t, "PUT", "/v1/not-found-pages/abc")
		Eq(t, put.Status, 405)
		True(t, len(endpoints) > 80)
	})

	t.Run("human auth: missing / bad token → 401; no account → 403; disabled → 403", func(t *testing.T) {
		a := Raw(t, "GET", "/v1/me")
		Eq(t, a.Status, 401)
		Eq(t, a.J.At("error.code"), "unauthenticated")
		b := Raw(t, "GET", "/v1/me", Opt{Headers: map[string]string{"Authorization": "Bearer nope"}})
		Eq(t, b.Status, 401)
		noAcc := NewPerson(t, L{}, PersonOpts{NoAccount: true})
		noAcc.API.Rejects(t, "getMe", nil, "forbidden", 403)
		dis := NewPerson(t, L{}, PersonOpts{Disabled: true})
		dis.API.Rejects(t, "getMe", nil, "forbidden", 403)
	})

	t.Run("GET /v1/me: isAdmin flag, lastLoginAt at most once per hour", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		me := admin.API.Call(t, "getMe", nil)
		Eq(t, me.At("isAdmin"), true)
		True(t, me.At("account.lastLoginAt").Truthy())
		me2 := admin.API.Call(t, "getMe", nil)
		Eq(t, me2.At("account.lastLoginAt"), me.At("account.lastLoginAt"))
		plain := NewPerson(t, L{})
		Eq(t, plain.API.Call(t, "getMe", nil).At("isAdmin"), false)
	})

	t.Run("revoked refresh tokens (account disabled through Auth) are rejected", func(t *testing.T) {
		p := NewPerson(t, L{})
		Sleep(1100)
		NoErr(t, AuthAdmin.RevokeRefreshTokens(p.UID))
		p.API.Rejects(t, "getMe", nil, "unauthenticated", 401)
	})

	t.Run("CORS: agent/install paths are open; human paths only for OPM_FRONTEND_ORIGINS", func(t *testing.T) {
		pre := Raw(t, http.MethodOptions, "/v1/agent/poll", Opt{Headers: map[string]string{"Origin": "https://evil.example", "Access-Control-Request-Method": "GET"}})
		Eq(t, pre.Status, 204)
		Eq(t, pre.Header("access-control-allow-origin"), "*")
		Match(t, pre.Header("access-control-allow-headers"), `X-OPM-Sig`)
		Match(t, pre.Header("access-control-expose-headers"), `X-OPM-Server-Time`)
		h1 := Raw(t, http.MethodOptions, "/v1/me", Opt{Headers: map[string]string{"Origin": "http://localhost:5173"}})
		Eq(t, h1.Header("access-control-allow-origin"), "http://localhost:5173")
		h2 := Raw(t, http.MethodOptions, "/v1/me", Opt{Headers: map[string]string{"Origin": "https://evil.example"}})
		Eq(t, h2.Header("access-control-allow-origin"), "")
		agent401 := Raw(t, "GET", "/v1/agent/poll")
		Eq(t, agent401.Status, 401)
		True(t, agent401.Header("x-opm-server-time") != "", "X-OPM-Server-Time also on 401")
	})
}
