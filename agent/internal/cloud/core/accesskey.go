package core

import (
	"crypto/rand"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"opm/agent/internal/cloud"
	"opm/agent/internal/sign"
)

const b32 = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

// Base32 is base32: RFC 4648 without padding.
func Base32(buf []byte) string {
	var bits uint
	var value uint32
	var out strings.Builder
	for _, x := range buf {
		value = value<<8 | uint32(x)
		bits += 8
		for bits >= 5 {
			out.WriteByte(b32[(value>>(bits-5))&31])
			bits -= 5
		}
	}
	if bits > 0 {
		out.WriteByte(b32[(value<<(5-bits))&31])
	}
	return out.String()
}

// NormalizeAccessKey is normalizeAccessKey: upper-case (JS rules: ß → SS), keep only A–Z2–7. sign.NormalizeAccessKey
// uses rune-wise upper-casing, so it is not reused.
func NormalizeAccessKey(s string) string {
	var b strings.Builder
	for _, r := range cases.Upper(language.Und).String(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '2' && r <= '7') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// HashAccessKey is hashAccessKey: argon2id m=65536 KiB, t=3, p=1 → PHC string.
func HashAccessKey(key string) string { return sign.HashAccessKey(NormalizeAccessKey(key)) }

// VerifyAccessKey is verifyAccessKey.
func VerifyAccessKey(key, phc string) (bool, error) {
	return sign.VerifyPHC(phc, NormalizeAccessKey(key))
}

// AccessKey is what NewAccessKey returns.
type AccessKey struct{ Plain, Grouped, Hash string }

// NewAccessKey is newAccessKey: 15 random bytes in base32 (24 characters).
func NewAccessKey() AccessKey {
	b := make([]byte, 15)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	plain := Base32(b)
	return AccessKey{plain, cloud.GroupKey(plain), HashAccessKey(plain)}
}
