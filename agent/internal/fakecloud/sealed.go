package fakecloud

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"opm/agent/internal/model"
	"opm/agent/internal/tunnelseal"
)

// §9.10 the sealed browser tunnel. The X25519 and release keys are made per fake cloud.
type sealState struct {
	release ed25519.PrivateKey
	key     *ecdh.PrivateKey
	off     bool // the routes do not exist (a cloud older than the feature)
}

func newSealState() *sealState {
	_, rel, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	s := &sealState{release: rel}
	s.rotate()
	return s
}

func (s *sealState) rotate() {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	s.key = k
}

// TunnelReleaseKey is the release public key (base64) that signs this cloud's tunnel key: agent.json releasePublicKey.
func (c *Cloud) TunnelReleaseKey() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return base64.StdEncoding.EncodeToString(c.seal.release.Public().(ed25519.PublicKey))
}

// RotateTunnelKey replaces the cloud's X25519 key.
func (c *Cloud) RotateTunnelKey() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seal.rotate()
}

// SetSealedRoutes false makes this a cloud without the sealed routes.
func (c *Cloud) SetSealedRoutes(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seal.off = !on
}

// serveSealed answers the two unauthenticated routes; false = not one of them.
func (c *Cloud) serveSealed(w http.ResponseWriter, r *http.Request, body []byte) bool {
	key := r.Method == http.MethodGet && r.URL.Path == "/v1/agent/tunnel-key"
	sealed := r.Method == http.MethodPost && r.URL.Path == "/v1/agent/sealed"
	if !key && !sealed {
		return false
	}
	c.mu.Lock()
	off, priv, rel := c.seal.off, c.seal.key, c.seal.release
	c.mu.Unlock()
	if off {
		writeErr(w, model.ErrNotFound, "no route "+r.Method+" "+r.URL.Path, nil)
		return true
	}
	if key {
		origin, err := tunnelseal.Origin(c.URL)
		if err != nil {
			panic(err)
		}
		writeJSON(w, 200, tunnelseal.SignCert(rel, priv.PublicKey().Bytes(), origin))
		return true
	}
	c.sealedRequest(w, r, priv, body)
	return true
}

func (c *Cloud) sealedRequest(w http.ResponseWriter, r *http.Request, priv *ecdh.PrivateKey, body []byte) {
	plain, kResp, err := tunnelseal.Open(priv, body)
	if errors.Is(err, tunnelseal.ErrKeyChanged) {
		writeErr(w, model.ErrTunnelKeyChanged, "the tunnel key changed", nil)
		return
	}
	bad := func() { writeErr(w, model.ErrUndecryptable, "the sealed request cannot be decrypted", nil) }
	if err != nil {
		bad()
		return
	}
	var in model.RelayInner
	if json.Unmarshal(plain, &in) != nil {
		bad()
		return
	}
	var inner []byte
	if in.BodyB64 != nil {
		if inner, err = base64.StdEncoding.DecodeString(*in.BodyB64); err != nil {
			bad()
			return
		}
	}
	rec := httptest.NewRecorder()
	setCommon(rec)
	path, _, _ := strings.Cut(in.Path, "?")
	if !strings.HasPrefix(path, "/v1/agent/") || path == "/v1/agent/relay" || path == "/v1/agent/sealed" || path == "/v1/agent/enroll" || path == "/v1/agent/tunnel-key" {
		writeErr(rec, model.ErrNotFound, "no route "+in.Method+" "+path, nil)
	} else if slug, reason, msg := c.verify(in.Method, in.Path, in.Headers, inner); reason != "" {
		writeErr(rec, model.ErrUnauthenticated, msg, map[string]any{"reason": reason})
	} else {
		ir := httptest.NewRequest(in.Method, in.Path, bytes.NewReader(inner)).WithContext(r.Context())
		for k, v := range in.Headers {
			ir.Header.Set(k, v)
		}
		c.mu.Lock()
		c.requests = append(c.requests, ir.URL.Path)
		c.mu.Unlock()
		c.route(rec, ir, inner, reqCtx{signer: slug})
	}
	hdr := map[string]string{}
	for k := range rec.Header() {
		hdr[k] = rec.Header().Get(k)
	}
	stream, err := tunnelseal.NewResponseStream(kResp, rec.Code, hdr, rec.Body)
	if err != nil {
		panic(err)
	}
	defer stream.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(200)
	_, _ = io.Copy(w, stream)
}
