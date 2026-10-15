package expr

// §21 #103: the Mosquitto password-file hash behind the mosquitto_passwd filter (TS shared/src/hash.ts). Verified against
// the real mosquitto_passwd: shared/testdata/mosquitto-passwd-vectors.json (static) + TestDockerMosquittoPasswd (live).

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
)

const (
	// MosquittoSaltLen is SALT_LEN of Mosquitto 2.0.x (the 2.0 broker accepts ONLY 12-byte salts; 2.1 accepts 12 or 64).
	MosquittoSaltLen = 12
	// MosquittoDefaultIterations is PW_DEFAULT_ITERATIONS of Mosquitto 2.0.x (2.1 writes 1000; both verify any count).
	MosquittoDefaultIterations = 101
	// MosquittoMaxIterations caps the filter argument (cloud previews hash in the browser).
	MosquittoMaxIterations = 10000
)

// MosquittoHash is mosquitto_passwd's encoding of password with salt:
// format 7 = "$7$<iterations>$<b64 salt>$<b64 PBKDF2-HMAC-SHA512(password, salt, iterations, 64)>",
// format 6 = "$6$<b64 salt>$<b64 SHA512(password ‖ salt)>" (standard base64 with padding, no newline).
func MosquittoHash(password string, salt []byte, format, iterations int) string {
	b64 := base64.StdEncoding.EncodeToString
	if format == 6 {
		h := sha512.New()
		h.Write([]byte(password))
		h.Write(salt)
		return "$6$" + b64(salt) + "$" + b64(h.Sum(nil))
	}
	dk, err := pbkdf2.Key(sha512.New, password, salt, iterations, 64)
	if err != nil { // only for FIPS-140-only mode / invalid lengths; cannot happen with these parameters
		panic(err)
	}
	return fmt.Sprintf("$7$%d$%s$%s", iterations, b64(salt), b64(dk))
}

// MosquittoSalt is the deterministic salt of the filter: the first 12 bytes of
// HMAC-SHA256(key = password, msg = "opm-mosquitto-salt" NUL deploymentId NUL siteSlug) (TS mosquittoSalt).
func MosquittoSalt(password, deploymentID, siteSlug string) []byte {
	m := hmac.New(sha256.New, []byte(password))
	m.Write([]byte("opm-mosquitto-salt\x00" + deploymentID + "\x00" + siteSlug))
	return m.Sum(nil)[:MosquittoSaltLen]
}
