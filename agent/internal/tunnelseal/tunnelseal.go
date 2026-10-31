// Package tunnelseal is the end-to-end encryption of the browser internet tunnel (SPEC-final §9.10): the agent seals a
// whole cloud request to the cloud's X25519 key, the cloud answers with a framed AEAD stream. The browser tab between
// them carries ciphertext only. Used by the agent, the cloud and the fake cloud of the agent tests.
//
// Request:  POST /v1/agent/sealed  { v:1, kid, epk, ct }   (epk, ct: base64)
//
//	shared = X25519(ephemeral, cloud key)
//	kReq || kResp = HKDF-SHA256(shared, salt = epk || cloudPub, info = "opm-tunnel-v1", 64 bytes)
//	ct = AES-256-GCM(kReq, nonce = 12 zero bytes, plaintext = the JSON of model.RelayInner)
//
// Response: 200 application/octet-stream
//
//	salt[32] then frames [u32be length][ciphertext]
//	frame key = HKDF-SHA256(kResp, salt, info = "opm-tunnel-v1-resp", 32 bytes)
//	nonce = 11 bytes big-endian counter || final flag (0 | 1), ≤ 1 MiB plaintext per frame, the last frame has final = 1
//	plaintext of all frames = [u32be n][n bytes JSON { status, headers }][inner response body]
//
// The salt exists because a captured request can be replayed: the cloud then answers again (a nonce error) for the same
// epk, so the same kResp. Without a per-response key that second answer would reuse the key + nonce of the first one,
// and AES-GCM gives both plaintexts away.
package tunnelseal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
)

const (
	// Version of the wire format.
	Version = 1
	// MaxFrame is the largest plaintext of one frame.
	MaxFrame = 1 << 20
	// MaxHeader is the largest { status, headers } JSON.
	MaxHeader = 64 << 10
	// CodeKeyChanged is the error code of a sealed request to an unknown kid.
	CodeKeyChanged = "tunnel-key-changed"

	info       = "opm-tunnel-v1"
	infoResp   = "opm-tunnel-v1-resp"
	certDomain = "opm-tunnel-key-v1"
	keySize    = 32
	saltSize   = 32
	tagSize    = 16
)

// Errors of Open.
var (
	// ErrKeyChanged: the envelope names another key than the one given.
	ErrKeyChanged = errors.New("tunnel key changed")
	// ErrUndecryptable: anything else that is wrong with an envelope. One error on purpose: no oracle.
	ErrUndecryptable = errors.New("sealed request cannot be decrypted")
)

// KeyCert is the answer of GET /v1/agent/tunnel-key.
type KeyCert struct {
	V         int    `json:"v"`
	Kid       string `json:"kid"`
	PublicKey string `json:"publicKey"` // base64 X25519
	Origin    string `json:"origin"`
	Sig       string `json:"sig"` // base64url Ed25519 by the release key over CertMessage
}

// Envelope is the body of POST /v1/agent/sealed.
type Envelope struct {
	V   int    `json:"v"`
	Kid string `json:"kid"`
	Epk string `json:"epk"`
	Ct  string `json:"ct"`
}

// Kid is the first 16 hex characters of the sha256 of a public key.
func Kid(pub []byte) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:])[:16]
}

// Origin normalises a URL to scheme://host[:port]: lower case, default port dropped.
func Origin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("not an absolute URL: %q", raw)
	}
	scheme, host := strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	if (scheme == "http" && strings.HasSuffix(host, ":80")) || (scheme == "https" && strings.HasSuffix(host, ":443")) {
		host = host[:strings.LastIndexByte(host, ':')]
	}
	return scheme + "://" + host, nil
}

// CertMessage is the domain-separated string the release key signs.
func CertMessage(publicKeyB64, origin string) []byte {
	return []byte(certDomain + "\n" + publicKeyB64 + "\n" + origin + "\n")
}

// SignCert certifies an X25519 public key for an origin.
func SignCert(release ed25519.PrivateKey, pub []byte, origin string) KeyCert {
	b64 := base64.StdEncoding.EncodeToString(pub)
	return KeyCert{V: Version, Kid: Kid(pub), PublicKey: b64, Origin: origin,
		Sig: base64.RawURLEncoding.EncodeToString(ed25519.Sign(release, CertMessage(b64, origin)))}
}

// VerifyCert checks a certificate against the pinned release public key (base64) and the origin the agent talks to, and
// returns the X25519 public key. Everything a tab could swap is covered: key, origin, kid.
func VerifyCert(c KeyCert, releasePublicKeyB64, wantOrigin string) ([]byte, error) {
	if c.V != Version {
		return nil, fmt.Errorf("unsupported tunnel key version %d", c.V)
	}
	release, err := base64.StdEncoding.DecodeString(releasePublicKeyB64)
	if err != nil || len(release) != ed25519.PublicKeySize {
		return nil, errors.New("releasePublicKey is not a base64 Ed25519 key")
	}
	pub, err := base64.StdEncoding.DecodeString(c.PublicKey)
	if err != nil || len(pub) != keySize {
		return nil, errors.New("publicKey is not a base64 X25519 key")
	}
	if _, err := ecdh.X25519().NewPublicKey(pub); err != nil {
		return nil, errors.New("publicKey is not an X25519 key")
	}
	sig, err := base64.RawURLEncoding.DecodeString(c.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, errors.New("sig is not a base64url Ed25519 signature")
	}
	if !ed25519.Verify(ed25519.PublicKey(release), CertMessage(c.PublicKey, c.Origin), sig) {
		return nil, errors.New("the signature does not verify with releasePublicKey")
	}
	if c.Origin != wantOrigin {
		return nil, fmt.Errorf("the key is for %s, this agent talks to %s", c.Origin, wantOrigin)
	}
	if c.Kid != Kid(pub) {
		return nil, errors.New("kid is not the id of publicKey")
	}
	return pub, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

func deriveKeys(shared, epk, cloudPub []byte) (kReq, kResp []byte, err error) {
	salt := append(append(make([]byte, 0, len(epk)+len(cloudPub)), epk...), cloudPub...)
	okm, err := hkdf.Key(sha256.New, shared, salt, info, 2*keySize)
	if err != nil {
		return nil, nil, err
	}
	return okm[:keySize], okm[keySize:], nil
}

// Seal encrypts one request to the cloud key. It returns the envelope JSON and kResp, the secret that opens the answer.
// A fresh ephemeral key per call: never reuse an envelope for a second attempt on another key.
func Seal(cloudPub []byte, plaintext []byte) ([]byte, []byte, error) {
	curve := ecdh.X25519()
	pub, err := curve.NewPublicKey(cloudPub)
	if err != nil {
		return nil, nil, err
	}
	eph, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	shared, err := eph.ECDH(pub)
	if err != nil {
		return nil, nil, err
	}
	epk := eph.PublicKey().Bytes()
	kReq, kResp, err := deriveKeys(shared, epk, cloudPub)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := newAEAD(kReq)
	if err != nil {
		return nil, nil, err
	}
	ct := gcm.Seal(nil, make([]byte, gcm.NonceSize()), plaintext, nil)
	env, err := json.Marshal(Envelope{V: Version, Kid: Kid(cloudPub), Epk: base64.StdEncoding.EncodeToString(epk), Ct: base64.StdEncoding.EncodeToString(ct)})
	if err != nil {
		return nil, nil, err
	}
	return env, kResp, nil
}

// Open decrypts an envelope with the cloud key: the request plaintext and kResp.
func Open(priv *ecdh.PrivateKey, envelope []byte) ([]byte, []byte, error) {
	var e Envelope
	if err := json.Unmarshal(envelope, &e); err != nil || e.V != Version {
		return nil, nil, ErrUndecryptable
	}
	cloudPub := priv.PublicKey().Bytes()
	if e.Kid != Kid(cloudPub) {
		return nil, nil, ErrKeyChanged
	}
	epk, err := base64.StdEncoding.DecodeString(e.Epk)
	if err != nil {
		return nil, nil, ErrUndecryptable
	}
	ct, err := base64.StdEncoding.DecodeString(e.Ct)
	if err != nil {
		return nil, nil, ErrUndecryptable
	}
	pub, err := ecdh.X25519().NewPublicKey(epk)
	if err != nil {
		return nil, nil, ErrUndecryptable
	}
	shared, err := priv.ECDH(pub) // fails on low-order points
	if err != nil {
		return nil, nil, ErrUndecryptable
	}
	kReq, kResp, err := deriveKeys(shared, epk, cloudPub)
	if err != nil {
		return nil, nil, ErrUndecryptable
	}
	gcm, err := newAEAD(kReq)
	if err != nil {
		return nil, nil, ErrUndecryptable
	}
	plain, err := gcm.Open(nil, make([]byte, gcm.NonceSize()), ct, nil)
	if err != nil {
		return nil, nil, ErrUndecryptable
	}
	return plain, kResp, nil
}

func frameNonce(counter uint64, final bool) []byte {
	n := make([]byte, 12)
	binary.BigEndian.PutUint64(n[3:11], counter)
	if final {
		n[11] = 1
	}
	return n
}

func frameKey(kResp, salt []byte) (cipher.AEAD, error) {
	k, err := hkdf.Key(sha256.New, kResp, salt, infoResp, keySize)
	if err != nil {
		return nil, err
	}
	return newAEAD(k)
}

type responseHead struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
}

var framePool = sync.Pool{New: func() any { b := make([]byte, MaxFrame); return &b }}

// stream produces the response bytes on demand: at most one frame in memory, so a large download costs 1 MiB.
type stream struct {
	gcm     cipher.AEAD
	body    io.Reader
	counter uint64
	prefix  []byte // not yet framed: salt is written directly, this is the head of the first frame
	plain   *[]byte
	out     []byte // ready to send
	off     int
	done    bool
}

// NewResponseStream seals a response with the kResp of its request. The reader closes body (when it is an io.Closer);
// a read error of body is returned, which ends the stream without a final frame, so the agent refuses it.
func NewResponseStream(kResp []byte, status int, headers map[string]string, body io.Reader) (io.ReadCloser, error) {
	head, err := json.Marshal(responseHead{Status: status, Headers: headers})
	if err != nil {
		return nil, err
	}
	if len(head) > MaxHeader {
		return nil, errors.New("response headers too large")
	}
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	gcm, err := frameKey(kResp, salt)
	if err != nil {
		return nil, err
	}
	prefix := binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(head)), uint32(len(head)))
	if body == nil {
		body = strings.NewReader("")
	}
	return &stream{gcm: gcm, body: body, prefix: append(prefix, head...), plain: framePool.Get().(*[]byte), out: salt}, nil
}

func (s *stream) Read(p []byte) (int, error) {
	for s.off == len(s.out) {
		if s.done {
			return 0, io.EOF
		}
		if err := s.nextFrame(); err != nil {
			return 0, err
		}
	}
	n := copy(p, s.out[s.off:])
	s.off += n
	return n, nil
}

func (s *stream) nextFrame() error {
	pt := *s.plain
	n := copy(pt, s.prefix)
	s.prefix = nil
	m, err := io.ReadFull(s.body, pt[n:])
	final := err == io.EOF || err == io.ErrUnexpectedEOF
	if err != nil && !final {
		return err
	}
	size := n + m
	out := binary.BigEndian.AppendUint32(s.out[:0], uint32(size+tagSize))
	s.out = s.gcm.Seal(out, frameNonce(s.counter, final), pt[:size], nil)
	s.off = 0
	s.counter++
	s.done = final
	return nil
}

func (s *stream) Close() error {
	if s.plain != nil {
		framePool.Put(s.plain)
		s.plain = nil
	}
	if c, ok := s.body.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// Response is a decrypted answer.
type Response struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

// OpenResponse decrypts a whole response. maxBody caps the plaintext. Any truncated, reordered, tampered or foreign
// stream is an error; nothing of it is returned. Lengths come from the tab: they are checked before anything is used.
func OpenResponse(kResp, data []byte, maxBody int64) (*Response, error) {
	if len(data) < saltSize {
		return nil, errors.New("sealed response is truncated")
	}
	gcm, err := frameKey(kResp, data[:saltSize])
	if err != nil {
		return nil, err
	}
	data = data[saltSize:]
	var plain []byte
	var res *Response
	for counter := uint64(0); ; counter++ {
		if len(data) < 4 {
			return nil, errors.New("sealed response is truncated")
		}
		n := binary.BigEndian.Uint32(data)
		data = data[4:]
		if n < tagSize || n > MaxFrame+tagSize {
			return nil, fmt.Errorf("sealed response has a bad frame length %d", n)
		}
		if uint64(len(data)) < uint64(n) {
			return nil, errors.New("sealed response is truncated")
		}
		ct := data[:n]
		data = data[n:]
		final := len(data) == 0 // the stream ends exactly at the final frame
		pt, err := gcm.Open(nil, frameNonce(counter, final), ct, nil)
		if err != nil {
			return nil, errors.New("sealed response does not authenticate")
		}
		if res == nil {
			if res, pt, err = parseHead(pt); err != nil {
				return nil, err
			}
		}
		if int64(len(plain))+int64(len(pt)) > maxBody {
			return nil, fmt.Errorf("sealed response over %d bytes", maxBody)
		}
		plain = append(plain, pt...)
		if final {
			res.Body = plain
			return res, nil
		}
	}
}

func parseHead(pt []byte) (*Response, []byte, error) {
	if len(pt) < 4 {
		return nil, nil, errors.New("sealed response has no header")
	}
	n := binary.BigEndian.Uint32(pt)
	if n > MaxHeader || uint64(len(pt)-4) < uint64(n) {
		return nil, nil, errors.New("sealed response has a bad header length")
	}
	var h responseHead
	if err := json.Unmarshal(pt[4:4+n], &h); err != nil || h.Status < 100 || h.Status > 599 {
		return nil, nil, errors.New("sealed response has a bad header")
	}
	return &Response{Status: h.Status, Headers: h.Headers}, pt[4+n:], nil
}
