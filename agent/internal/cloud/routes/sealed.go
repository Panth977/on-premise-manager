package routes

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/model"
	"opm/agent/internal/tunnelseal"
)

// The sealed browser tunnel (§9.10): the tab carries ciphertext between agent and cloud. The outer route has no
// authentication and charges no rate limit; the inner request runs through Dispatch like a direct one (signature, nonce,
// site rate limit), restricted to the signed agent routes.
func init() {
	cloud.Handlers(map[string]cloud.Handler{
		"agentTunnelKey": agentTunnelKey,
		"agentSealed":    agentSealed,
	})
}

func agentTunnelKey(c *cloud.Ctx) (any, error) {
	k, err := cloud.TunnelKey()
	if err != nil {
		return nil, err
	}
	return k.Cert, nil
}

func undecryptable() error {
	return cloud.E.New("undecryptable", "the sealed request cannot be decrypted", nil, nil)
}

// sealedHeaders: the inner headers, lower case, with everything that describes the connection taken from the outer
// request. origin is what keeps a download streaming instead of a 302 (core.NoRedirectFor, §21 #114): a tab always sends
// one; "null" for a caller that does not.
func sealedHeaders(inner map[string]string, outer map[string]string) map[string]string {
	out := make(map[string]string, len(inner)+4)
	for k, v := range inner {
		out[strings.ToLower(k)] = v
	}
	for _, k := range []string{"host", "user-agent", "x-forwarded-for", "x-forwarded-host", "x-forwarded-proto"} {
		if v, ok := outer[k]; ok {
			out[k] = v
		} else {
			delete(out, k)
		}
	}
	out["origin"] = outer["origin"]
	if out["origin"] == "" {
		out["origin"] = "null"
	}
	return out
}

func replyReader(body any) (io.Reader, error) {
	switch b := body.(type) {
	case nil:
		return strings.NewReader(""), nil
	case string:
		return strings.NewReader(b), nil
	case []byte:
		return strings.NewReader(string(b)), nil
	case io.Reader:
		return b, nil
	}
	return nil, errors.New("sealed: reply body of an unknown type")
}

func agentSealed(c *cloud.Ctx) (any, error) {
	k, err := cloud.TunnelKey()
	if err != nil {
		return nil, err
	}
	plain, kResp, err := tunnelseal.Open(k.Private, c.Body)
	if errors.Is(err, tunnelseal.ErrKeyChanged) {
		return nil, cloud.E.New("tunnel-key-changed", "the tunnel key of this cloud changed", doc.Obj("kid", k.Cert.Kid), nil)
	}
	if err != nil {
		return nil, undecryptable()
	}
	var inner model.RelayInner
	if err := json.Unmarshal(plain, &inner); err != nil {
		return nil, undecryptable()
	}
	var body []byte
	if inner.BodyB64 != nil {
		if body, err = base64.StdEncoding.DecodeString(*inner.BodyB64); err != nil {
			return nil, undecryptable()
		}
	}
	rep := cloud.Dispatch(cloud.ReqLike{Method: inner.Method, URL: inner.Path, Headers: sealedHeaders(inner.Headers, c.Headers), Body: body, IP: c.IP, Context: c.Context, Mount: c.Mount},
		cloud.DispatchOpts{SealedInner: true})
	src, err := replyReader(rep.Body)
	if err != nil {
		return nil, err
	}
	stream, err := tunnelseal.NewResponseStream(kResp, rep.Status, rep.Headers, src)
	if err != nil {
		if cl, ok := src.(io.Closer); ok {
			cl.Close()
		}
		return nil, err
	}
	return &cloud.Reply{Status: 200, Headers: map[string]string{"Content-Type": "application/octet-stream", "Cache-Control": "no-store"}, Body: stream}, nil
}
