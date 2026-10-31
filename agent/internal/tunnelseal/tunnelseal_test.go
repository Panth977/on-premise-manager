package tunnelseal

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func cloudKey(t *testing.T) *ecdh.PrivateKey {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func sealed(t *testing.T, kResp []byte, status int, body []byte) []byte {
	t.Helper()
	s, err := NewResponseStream(kResp, status, map[string]string{"X-OPM-Server-Time": "7"}, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	out, err := io.ReadAll(s)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// frames splits a response into its salt and frames (each with the length prefix).
func frames(t *testing.T, data []byte) ([]byte, [][]byte) {
	t.Helper()
	salt, rest := data[:saltSize], data[saltSize:]
	var out [][]byte
	for len(rest) > 0 {
		n := 4 + int(binary.BigEndian.Uint32(rest))
		out = append(out, rest[:n])
		rest = rest[n:]
	}
	return salt, out
}

func TestRequestRoundTrip(t *testing.T) {
	priv := cloudKey(t)
	pub := priv.PublicKey().Bytes()
	env, kResp, err := Seal(pub, []byte(`{"path":"/v1/agent/poll"}`))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(env, []byte("poll")) {
		t.Fatal("plaintext in the envelope")
	}
	plain, k2, err := Open(priv, env)
	if err != nil || string(plain) != `{"path":"/v1/agent/poll"}` || !bytes.Equal(kResp, k2) {
		t.Fatalf("open: %v %q", err, plain)
	}
	env2, kResp2, _ := Seal(pub, []byte("x"))
	if bytes.Equal(kResp, kResp2) {
		t.Fatal("kResp must differ per request")
	}
	var e1, e2 Envelope
	_ = json.Unmarshal(env, &e1)
	_ = json.Unmarshal(env2, &e2)
	if e1.Epk == e2.Epk {
		t.Fatal("an ephemeral key per request")
	}
}

func TestOpenRefuses(t *testing.T) {
	priv := cloudKey(t)
	env, _, _ := Seal(priv.PublicKey().Bytes(), []byte("hello"))
	var e Envelope
	_ = json.Unmarshal(env, &e)
	mut := func(f func(*Envelope)) []byte {
		c := e
		f(&c)
		b, _ := json.Marshal(c)
		return b
	}
	if _, _, err := Open(cloudKey(t), env); !errors.Is(err, ErrKeyChanged) {
		t.Fatalf("another key: %v", err)
	}
	if _, _, err := Open(priv, mut(func(c *Envelope) { c.Kid = "0000000000000000" })); !errors.Is(err, ErrKeyChanged) {
		t.Fatalf("kid: %v", err)
	}
	ct, _ := base64.StdEncoding.DecodeString(e.Ct)
	ct[0] ^= 1
	for name, b := range map[string][]byte{
		"not json": []byte("{"),
		"version":  mut(func(c *Envelope) { c.V = 2 }),
		"bad ct":   mut(func(c *Envelope) { c.Ct = base64.StdEncoding.EncodeToString(ct) }),
		"ct b64":   mut(func(c *Envelope) { c.Ct = "***" }),
		"epk b64":  mut(func(c *Envelope) { c.Epk = "***" }),
		"short":    mut(func(c *Envelope) { c.Epk = "AAAA" }),
		"low order": mut(func(c *Envelope) {
			c.Epk = base64.StdEncoding.EncodeToString(make([]byte, 32)) // the all-zero point
		}),
	} {
		if _, _, err := Open(priv, b); !errors.Is(err, ErrUndecryptable) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestResponseRoundTrip(t *testing.T) {
	k := bytes.Repeat([]byte{7}, 32)
	for _, size := range []int{0, 1, MaxFrame - 100, MaxFrame, MaxFrame + 1, 3*MaxFrame + 17} {
		body := make([]byte, size)
		_, _ = rand.Read(body)
		r, err := OpenResponse(k, sealed(t, k, 206, body), 1<<30)
		if err != nil {
			t.Fatalf("%d: %v", size, err)
		}
		if r.Status != 206 || r.Headers["X-OPM-Server-Time"] != "7" || !bytes.Equal(r.Body, body) {
			t.Fatalf("%d: %d %v", size, r.Status, r.Headers)
		}
	}
}

func TestFrameSizeLimit(t *testing.T) {
	k := bytes.Repeat([]byte{1}, 32)
	data := sealed(t, k, 200, make([]byte, 5*MaxFrame+3))
	_, fs := frames(t, data)
	if len(fs) != 6 {
		t.Fatalf("frames %d", len(fs))
	}
	for i, f := range fs {
		if len(f) > 4+MaxFrame+tagSize {
			t.Fatalf("frame %d is %d bytes", i, len(f))
		}
	}
}

func TestResponseTampering(t *testing.T) {
	k := bytes.Repeat([]byte{2}, 32)
	body := bytes.Repeat([]byte("secret!"), 500_000)
	data := sealed(t, k, 200, body)
	salt, fs := frames(t, data)
	if len(fs) < 3 {
		t.Fatalf("need 3 frames, have %d", len(fs))
	}
	join := func(parts ...[]byte) []byte { return bytes.Join(append([][]byte{salt}, parts...), nil) }
	flipped := bytes.Clone(data)
	flipped[len(flipped)-1] ^= 1
	saltFlip := bytes.Clone(data)
	saltFlip[0] ^= 1
	bigLen := bytes.Clone(data)
	binary.BigEndian.PutUint32(bigLen[saltSize:], 0xffffffff)
	cases := map[string][]byte{
		"empty":              nil,
		"salt only":          data[:saltSize],
		"cut in a frame":     data[:len(data)-5],
		"final frame gone":   join(fs[:len(fs)-1]...),
		"only the first":     join(fs[0]),
		"reordered":          join(fs[1], fs[0], fs[2]),
		"frame dropped":      join(fs[0], fs[2]),
		"frame repeated":     join(fs[0], fs[0], fs[1], fs[2]),
		"bit flip":           flipped,
		"salt flip":          saltFlip,
		"data after final":   append(bytes.Clone(data), 1, 2, 3),
		"frame length 4 GiB": bigLen,
	}
	for name, d := range cases {
		if r, err := OpenResponse(k, d, 1<<30); err == nil {
			t.Errorf("%s: accepted (%d bytes)", name, len(r.Body))
		}
	}
	// a final frame from another response of the same key cannot end this one
	other := sealed(t, k, 200, []byte("x"))
	_, ofs := frames(t, other)
	if _, err := OpenResponse(k, join(fs[0], ofs[0]), 1<<30); err == nil {
		t.Error("a foreign final frame was accepted")
	}
}

func TestResponseOfAnotherRequest(t *testing.T) {
	data := sealed(t, bytes.Repeat([]byte{3}, 32), 200, []byte("old answer"))
	if _, err := OpenResponse(bytes.Repeat([]byte{4}, 32), data, 1<<20); err == nil {
		t.Fatal("an old response opened with a new request's key")
	}
}

// A replayed request reaches the cloud with the same kResp: the two answers must not share a keystream.
func TestReplayedRequestGetsAnotherKeystream(t *testing.T) {
	k := bytes.Repeat([]byte{5}, 32)
	a := sealed(t, k, 200, bytes.Repeat([]byte{0}, 200))
	b := sealed(t, k, 200, bytes.Repeat([]byte{0}, 200))
	_, fa := frames(t, a)
	_, fb := frames(t, b)
	if bytes.Equal(a[:saltSize], b[:saltSize]) {
		t.Fatal("salt repeated")
	}
	if bytes.Equal(fa[0], fb[0]) {
		t.Fatal("same plaintext, same ciphertext: the keystream repeated")
	}
}

func TestResponseCap(t *testing.T) {
	k := bytes.Repeat([]byte{6}, 32)
	if _, err := OpenResponse(k, sealed(t, k, 200, make([]byte, 2000)), 1999); err == nil {
		t.Fatal("cap ignored")
	}
	if _, err := OpenResponse(k, sealed(t, k, 200, make([]byte, 2000)), 2000); err != nil {
		t.Fatal(err)
	}
}

type failing struct{ left int }

func (f *failing) Read(p []byte) (int, error) {
	if f.left <= 0 {
		return 0, errors.New("disk gone")
	}
	n := min(f.left, len(p))
	f.left -= n
	return n, nil
}

func TestBodyErrorEndsWithoutFinal(t *testing.T) {
	k := bytes.Repeat([]byte{8}, 32)
	s, err := NewResponseStream(k, 200, nil, &failing{left: 3 * MaxFrame})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := io.ReadAll(s)
	if err == nil {
		t.Fatal("the read error must reach the writer")
	}
	if _, err := OpenResponse(k, got, 1<<30); err == nil {
		t.Fatal("a failed body must not open")
	}
}

type closer struct {
	io.Reader
	closed bool
}

func (c *closer) Close() error { c.closed = true; return nil }

func TestStreamClosesBody(t *testing.T) {
	c := &closer{Reader: strings.NewReader("x")}
	s, err := NewResponseStream(bytes.Repeat([]byte{9}, 32), 200, nil, c)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, s)
	s.Close()
	if !c.closed {
		t.Fatal("body not closed")
	}
}

func TestCert(t *testing.T) {
	relPub, relPriv, _ := ed25519.GenerateKey(nil)
	rel := base64.StdEncoding.EncodeToString(relPub)
	pub := cloudKey(t).PublicKey().Bytes()
	const origin = "https://cloud.example"
	c := SignCert(relPriv, pub, origin)
	got, err := VerifyCert(c, rel, origin)
	if err != nil || !bytes.Equal(got, pub) {
		t.Fatalf("verify: %v", err)
	}
	if len(c.Kid) != 16 {
		t.Fatalf("kid %q", c.Kid)
	}
	otherPub, otherPriv, _ := ed25519.GenerateKey(nil)
	attacker := cloudKey(t).PublicKey().Bytes()
	bad := map[string]func() (KeyCert, string, string){
		"swapped key": func() (KeyCert, string, string) {
			x := c
			x.PublicKey = base64.StdEncoding.EncodeToString(attacker)
			x.Kid = Kid(attacker)
			return x, rel, origin
		},
		"self signed": func() (KeyCert, string, string) { return SignCert(otherPriv, attacker, origin), rel, origin },
		"other origin": func() (KeyCert, string, string) {
			return c, rel, "https://other.example"
		},
		"origin rewritten": func() (KeyCert, string, string) {
			x := c
			x.Origin = "https://other.example"
			return x, rel, "https://other.example"
		},
		"kid lies": func() (KeyCert, string, string) {
			x := c
			x.Kid = Kid(attacker)
			return x, rel, origin
		},
		"sig cut": func() (KeyCert, string, string) {
			x := c
			x.Sig = x.Sig[:10]
			return x, rel, origin
		},
		"version": func() (KeyCert, string, string) {
			x := c
			x.V = 2
			return x, rel, origin
		},
		"wrong pin": func() (KeyCert, string, string) {
			return c, base64.StdEncoding.EncodeToString(otherPub), origin
		},
		"bad pin": func() (KeyCert, string, string) { return c, "x", origin },
		"release signature over a hex digest is not a tunnel cert": func() (KeyCert, string, string) {
			x := c
			x.Sig = base64.RawURLEncoding.EncodeToString(ed25519.Sign(relPriv, []byte(strings.Repeat("ab", 32))))
			return x, rel, origin
		},
	}
	for name, f := range bad {
		x, pin, want := f()
		if _, err := VerifyCert(x, pin, want); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"https://Cloud.Example/":        "https://cloud.example",
		"https://cloud.example:443/api": "https://cloud.example",
		"http://1.2.3.4:80":             "http://1.2.3.4",
		"http://localhost:8790/x":       "http://localhost:8790",
	} {
		if got, err := Origin(in); err != nil || got != want {
			t.Errorf("%s → %s %v", in, got, err)
		}
	}
	if _, err := Origin("cloud.example"); err == nil {
		t.Error("no scheme")
	}
}
