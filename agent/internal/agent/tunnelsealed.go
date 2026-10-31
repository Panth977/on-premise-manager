package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/tunnelseal"
)

const (
	// maxSealedBody caps a decrypted tunnel response, like the direct route's read limit; downloads come in 3 MiB ranges.
	maxSealedBody = 64 << 20
	// maxSealedParts: the 4 MiB chunks of the largest sealed response with its framing.
	maxSealedParts = maxSealedBody/(4<<20) + 1
	tunnelKeyKV    = "tunnelKeyCert"
	tunnelKeyWait  = 30 * time.Second
)

type verifiedTunnelKey struct {
	kid string
	pub []byte
}

// tunnelPlaintext: the only case the tunnel carries a request in clear (§9.10). A pinned release key always wins.
func (a *Agent) tunnelPlaintext() bool { return a.cfg.ReleasePublicKey == "" && a.cfg.TunnelPlaintext }

// tunnelKey returns the verified key of the cloud: cached, else fetched through the tab and checked against the pinned
// release key. A key the tab answers is worth nothing until that check. reject: a kid the cloud refused.
func (a *Agent) tunnelKey(ctx context.Context, reject string, timeout time.Duration) (*verifiedTunnelKey, error) {
	if a.cfg.ReleasePublicKey == "" {
		return nil, errors.New(`no pinned release key, nothing can verify the cloud's tunnel key: set "releasePublicKey" in agent.json (or accept traffic in clear through the tab with "tunnelPlaintext": true)`)
	}
	origin, err := tunnelseal.Origin(a.cfg.APIBase)
	if err != nil {
		return nil, fmt.Errorf("apiBase: %w", err)
	}
	t := a.tun
	select {
	case t.keySem <- struct{}{}:
		defer func() { <-t.keySem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(timeout):
		return nil, errors.New("no result in time")
	}
	t.mu.Lock()
	k := t.key
	t.mu.Unlock()
	if k == nil {
		var c tunnelseal.KeyCert
		if ok, err := a.db.GetKV(tunnelKeyKV, &c); ok && err == nil {
			if pub, err := tunnelseal.VerifyCert(c, a.cfg.ReleasePublicKey, origin); err == nil {
				k = &verifiedTunnelKey{c.Kid, pub}
			}
		}
	}
	if k == nil || k.kid == reject {
		cert, err := a.fetchTunnelCert(ctx, min(timeout, tunnelKeyWait))
		if err != nil {
			return nil, err
		}
		pub, err := tunnelseal.VerifyCert(*cert, a.cfg.ReleasePublicKey, origin)
		if err != nil {
			return nil, fmt.Errorf("the cloud's tunnel key is refused: %w", err)
		}
		k = &verifiedTunnelKey{cert.Kid, pub}
		if err := a.db.SetKV(tunnelKeyKV, cert); err != nil {
			a.log.Warn("tunnel", "cannot store the tunnel key", "error", err.Error())
		}
	}
	t.mu.Lock()
	t.key = k
	t.mu.Unlock()
	return k, nil
}

func (a *Agent) fetchTunnelCert(ctx context.Context, timeout time.Duration) (*tunnelseal.KeyCert, error) {
	r, err := a.tunnelSend(ctx, tunQueued{method: "GET", url: a.apiURL("/v1/agent/tunnel-key"), headers: map[string]string{}, logMethod: "GET", logPath: "/v1/agent/tunnel-key"}, timeout)
	if err != nil {
		return nil, fmt.Errorf("cannot fetch the cloud's tunnel key: %w", err)
	}
	if r.Status == http.StatusNotFound {
		return nil, errors.New("the cloud offers no tunnel key (an older cloud, or one without its release private key)")
	}
	if r.Status != http.StatusOK {
		return nil, fmt.Errorf("cannot fetch the cloud's tunnel key: HTTP %d", r.Status)
	}
	var c tunnelseal.KeyCert
	if err := json.Unmarshal(r.Body, &c); err != nil {
		return nil, errors.New("the cloud's tunnel key is not JSON")
	}
	return &c, nil
}

// sealedCall carries one request end to end encrypted. Any failure is final: there is no way back to clear text, and
// the reason shows in the lease's lastError.
func (a *Agent) sealedCall(ctx context.Context, inner model.RelayInner, timeout time.Duration) (*cloudResp, error) {
	r, err := a.sealedAttempts(ctx, inner, time.Now().Add(timeout))
	if err == nil {
		a.noteSealResult("")
		return r, nil
	}
	if ctx.Err() != nil {
		return nil, err
	}
	msg := "tunnel refused: " + err.Error()
	a.noteSealResult(msg)
	return nil, &netError{errors.New(msg)}
}

func (a *Agent) noteSealResult(msg string) {
	t := a.tun
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lease == nil {
		return
	}
	if msg != "" {
		t.lease.LastError = &msg
	} else if t.lease.LastError != nil && *t.lease.LastError == t.sealErr {
		t.lease.LastError = nil
	}
	t.sealErr = msg
}

func (a *Agent) sealedAttempts(ctx context.Context, inner model.RelayInner, deadline time.Time) (*cloudResp, error) {
	plain, err := json.Marshal(inner)
	if err != nil {
		return nil, err
	}
	key, err := a.tunnelKey(ctx, "", time.Until(deadline))
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		left := time.Until(deadline)
		if left <= 0 {
			return nil, errors.New("no result in time")
		}
		env, kResp, err := tunnelseal.Seal(key.pub, plain)
		if err != nil {
			return nil, err
		}
		b64 := base64.StdEncoding.EncodeToString(env)
		r, err := a.tunnelSend(ctx, tunQueued{method: "POST", url: a.apiURL("/v1/agent/sealed"), headers: map[string]string{"Content-Type": "application/json"}, bodyB64: &b64,
			logMethod: inner.Method, logPath: pathOnly(inner.Path), sealed: true}, left)
		if err != nil {
			return nil, err
		}
		if r.Status == http.StatusOK {
			return a.openSealed(kResp, r.Body)
		}
		// unauthenticated, from the tab: all it can cause is one more key fetch, which is verified like the first
		var ae apiError
		_ = json.Unmarshal(r.Body, &ae.Body)
		if r.Status == http.StatusConflict && ae.Body.Error.Code == tunnelseal.CodeKeyChanged && attempt == 0 {
			if key, err = a.tunnelKey(ctx, key.kid, time.Until(deadline)); err != nil {
				return nil, err
			}
			continue
		}
		if r.Status == http.StatusNotFound {
			return nil, errors.New("the cloud has no sealed tunnel route (an older cloud)")
		}
		return nil, fmt.Errorf("the cloud refused the sealed request: HTTP %d %s", r.Status, ae.Body.Error.Code)
	}
}

// openSealed turns the ciphertext the tab delivered into the cloud's answer. Only what is in there counts, also the time.
func (a *Agent) openSealed(kResp, body []byte) (*cloudResp, error) {
	res, err := tunnelseal.OpenResponse(kResp, body, maxSealedBody)
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	for k, v := range res.Headers {
		h.Set(k, v)
	}
	a.adoptServerTime(h)
	return &cloudResp{Status: res.Status, Header: h, Body: res.Body, Route: "tunnel"}, nil
}
