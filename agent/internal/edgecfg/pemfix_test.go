package edgecfg

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
)

func TestRepairPEM(t *testing.T) {
	k, _ := rsa.GenerateKey(rand.Reader, 1024)
	good := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
	flat := strings.TrimSpace(strings.ReplaceAll(good, "\n", " "))
	if b, _ := pem.Decode([]byte(flat)); b != nil {
		t.Fatal("the flattened key must not decode")
	}
	fixed := RepairPEM(flat)
	b, _ := pem.Decode([]byte(fixed))
	if b == nil {
		t.Fatalf("repaired key does not decode: %q", fixed[:60])
	}
	if _, err := x509.ParsePKCS1PrivateKey(b.Bytes); err != nil {
		t.Fatal(err)
	}
	if RepairPEM(good) != good || RepairPEM("plain") != "plain" {
		t.Fatal("unchanged otherwise")
	}
	two := flat + " " + flat
	if n := strings.Count(RepairPEM(two), "-----BEGIN"); n != 2 {
		t.Fatalf("two blocks: %d", n)
	}
}
