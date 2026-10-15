package agent

import (
	"crypto/ed25519"
	"encoding/base64"
)

func signRaw(a *Agent, s string) string {
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(a.key, []byte(s)))
}

func verifyRaw(pub ed25519.PublicKey, s, sigB64 string) bool {
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, []byte(s), sig)
}
