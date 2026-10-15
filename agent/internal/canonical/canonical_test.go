package canonical

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

type vectors struct {
	Canonical []struct {
		JSON   string `json:"json"`
		Output string `json:"output"`
	} `json:"canonical"`
	Activities []struct {
		Activity struct {
			ActivityID   string         `json:"activityId"`
			DeploymentID string         `json:"deploymentId"`
			SiteSlug     string         `json:"siteSlug"`
			Seq          float64        `json:"seq"`
			Ts           string         `json:"ts"`
			Actor        *string        `json:"actor"`
			Origin       string         `json:"origin"`
			Type         string         `json:"type"`
			Summary      string         `json:"summary"`
			Data         map[string]any `json:"data"`
			PrevHash     *string        `json:"prevHash"`
		} `json:"activity"`
		HashInput string `json:"hashInput"`
		Hash      string `json:"hash"`
	} `json:"activities"`
	Signing struct {
		SeedHex      string `json:"seedHex"`
		PublicKeyB64 string `json:"publicKeyB64"`
		Requests     []struct {
			Method, Path, Nonce, Body, BodySha256Hex, StringToSign, Signature string
			Ts                                                                int64
		} `json:"requests"`
		Responses []struct {
			Status                                                     int
			RequestNonce, Body, BodySha256Hex, StringToSign, Signature string
			Ts                                                         int64
		} `json:"responses"`
	} `json:"signing"`
}

func load(t *testing.T) vectors {
	b, err := os.ReadFile("../../../shared/testdata/canonical-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestCanonicalVectors(t *testing.T) {
	v := load(t)
	if len(v.Canonical) == 0 {
		t.Fatal("no vectors")
	}
	for _, c := range v.Canonical {
		got, err := CanonicalizeJSON([]byte(c.JSON))
		if err != nil {
			t.Errorf("%s: %v", c.JSON, err)
			continue
		}
		if got != c.Output {
			t.Errorf("%s: got %q want %q", c.JSON, got, c.Output)
		}
	}
}

func TestActivityHash(t *testing.T) {
	v := load(t)
	for _, a := range v.Activities {
		f := ActivityFields{a.Activity.ActivityID, a.Activity.DeploymentID, a.Activity.SiteSlug, a.Activity.Seq, a.Activity.Ts, a.Activity.Actor, a.Activity.Origin, a.Activity.Type, a.Activity.Summary, a.Activity.Data, a.Activity.PrevHash}
		in, err := ActivityHashInput(f)
		if err != nil {
			t.Fatal(err)
		}
		if in != a.HashInput {
			t.Errorf("input\n got %s\nwant %s", in, a.HashInput)
		}
		h, _ := ActivityHash(f)
		if h != a.Hash {
			t.Errorf("hash got %s want %s", h, a.Hash)
		}
	}
}

func TestSigning(t *testing.T) {
	v := load(t)
	seed, _ := hex.DecodeString(v.Signing.SeedHex)
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	if base64.StdEncoding.EncodeToString(pub) != v.Signing.PublicKeyB64 {
		t.Fatal("public key mismatch")
	}
	for _, r := range v.Signing.Requests {
		if Sha256Hex([]byte(r.Body)) != r.BodySha256Hex {
			t.Error("body hash")
		}
		s := SignedRequestString(r.Method, r.Path, r.Ts, r.Nonce, r.BodySha256Hex)
		if s != r.StringToSign {
			t.Errorf("string to sign %q", s)
		}
		sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(s)))
		if sig != r.Signature {
			t.Error("signature mismatch")
		}
		raw, _ := base64.RawURLEncoding.DecodeString(r.Signature)
		if !ed25519.Verify(pub, []byte(s), raw) {
			t.Error("verify failed")
		}
	}
	for _, r := range v.Signing.Responses {
		s := SignedResponseString(r.Status, r.RequestNonce, r.Ts, r.BodySha256Hex)
		if s != r.StringToSign {
			t.Errorf("response string %q", s)
		}
		if base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(s))) != r.Signature {
			t.Error("response signature mismatch")
		}
	}
	if Sha256Hex(nil) != Sha256EmptyHex {
		t.Error("empty hash")
	}
}

func TestFormatNumber(t *testing.T) {
	cases := map[float64]string{1e21: "1e+21", 1e20: "100000000000000000000", 1e-7: "1e-7", 0.000001: "0.000001", 1.5e-10: "1.5e-10", -12.5: "-12.5", 0.25: "0.25", 123: "123", 1.234e25: "1.234e+25"}
	for in, want := range cases {
		if got := MustFormatNumber(in); got != want {
			t.Errorf("%v: got %s want %s", in, got, want)
		}
	}
}
