package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/sign"
)

func newCloudHTTPClient(c model.AgentConfigFile) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyFromEnvironment
	if c.Proxy.Set && c.Proxy.Value != nil && *c.Proxy.Value != "" {
		if u, err := url.Parse(*c.Proxy.Value); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	if pem, err := os.ReadFile("/etc/opm/extra-ca.pem"); err == nil {
		pool, _ := x509.SystemCertPool()
		if pool == nil {
			pool = x509.NewCertPool()
		}
		pool.AppendCertsFromPEM(pem)
		tr.TLSClientConfig = &tls.Config{RootCAs: pool}
	}
	return &http.Client{Transport: tr}
}

// apiError is a cloud / peer error answer.
type apiError struct {
	Status int
	Body   model.APIErrorBody
}

func (e *apiError) Error() string {
	if e.Body.Error.Code != "" {
		return fmt.Sprintf("%d %s: %s", e.Status, e.Body.Error.Code, e.Body.Error.Message)
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

func (e *apiError) code() string { return e.Body.Error.Code }

func asAPIError(err error) *apiError {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}
	return nil
}

// netError marks a transport failure (route fallback applies).
type netError struct{ err error }

func (e *netError) Error() string { return e.err.Error() }
func (e *netError) Unwrap() error { return e.err }

func isNetErr(err error) bool {
	var ne *netError
	return errors.As(err, &ne)
}

// cloudResp is a cloud answer.
type cloudResp struct {
	Status int
	Header http.Header
	Body   []byte
	Route  string
}

type callOpts struct {
	noTunnel bool // uploads never go through the tunnel
	noRelay  bool
	timeout  time.Duration
	direct   bool // only direct (enroll, relay outer)
	extraHdr map[string]string
}

func (a *Agent) apiURL(path string) string { return strings.TrimRight(a.cfg.APIBase, "/") + path }

func (a *Agent) adoptServerTime(h http.Header) {
	st := h.Get("X-OPM-Server-Time")
	if st == "" {
		return
	}
	srv, err := strconv.ParseInt(st, 10, 64)
	if err != nil {
		return
	}
	off := srv - time.Now().Unix()
	if off > -5 && off < 5 {
		off = 0
	}
	a.mu.Lock()
	changed := off != a.clockOffset
	a.clockOffset = off
	a.mu.Unlock()
	if changed {
		_ = a.db.SetKV("clockOffsetSec", off)
		a.log.Info("cloudlink", "clock offset", "offsetSec", off)
	}
}

// signedHeaders signs a cloud request (path from /v1/ with query).
func (a *Agent) signedHeaders(method, path string, body []byte) map[string]string {
	return sign.SignRequest(a.key, a.dep+"/"+a.site, method, path, body, a.signTs())
}

func (a *Agent) markDirect(ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ok {
		a.lastDirectOk = time.Now()
		a.lastCloudOk = time.Now()
		a.cloudRoute = "direct"
	}
}

// directDown: the last direct attempt failed less than 15 s ago (skip it, try the other routes first).
func (a *Agent) directDown() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.directFailAt.IsZero() && a.directFailAt.After(a.lastDirectOk) && time.Since(a.directFailAt) < 15*time.Second
}

func (a *Agent) hasInternet() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.lastDirectOk.IsZero() && time.Since(a.lastDirectOk) < a.tm.Internet
}

func (a *Agent) route() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lastCloudOk.IsZero() || time.Since(a.lastCloudOk) > a.tm.Internet {
		if a.tun.active() != nil {
			return "tunnel"
		}
		return "none"
	}
	return a.cloudRoute
}

// directRaw performs a direct HTTP request to the cloud.
func (a *Agent) directRaw(ctx context.Context, method, path string, body []byte, hdr map[string]string, timeout time.Duration) (*cloudResp, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, method, a.apiURL(path), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if len(body) > 0 && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, &netError{err}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, &netError{err}
	}
	a.adoptServerTime(resp.Header)
	return &cloudResp{Status: resp.StatusCode, Header: resp.Header, Body: b, Route: "direct"}, nil
}

// cloudCall sends a signed request using the route order direct → relay → tunnel (§1.4).
func (a *Agent) cloudCall(ctx context.Context, method, path string, body []byte, o callOpts) (*cloudResp, error) {
	if !a.enrolled {
		return nil, &netError{errors.New("not enrolled")}
	}
	hdr := a.signedHeaders(method, path, body)
	if len(body) > 0 {
		hdr["Content-Type"] = "application/json; charset=utf-8"
	}
	for k, v := range o.extraHdr {
		hdr[k] = v
	}
	var lastErr error
	if o.direct || !a.directDown() {
		resp, err := a.directRaw(ctx, method, path, body, hdr, o.timeout)
		if err == nil {
			a.markDirect(true)
			return resp, nil
		}
		lastErr = err
		a.mu.Lock()
		a.directFailAt = time.Now()
		a.mu.Unlock()
		a.log.Debug("cloudlink", "direct call failed", "path", pathOnly(path), "error", err.Error())
		if o.direct {
			return nil, lastErr
		}
	} else {
		lastErr = &netError{errors.New("direct route down")}
	}
	inner := model.RelayInner{Method: method, Path: path, Headers: hdr}
	if len(body) > 0 {
		b64 := base64.StdEncoding.EncodeToString(body)
		inner.BodyB64 = &b64
	}
	if !o.noRelay {
		for _, p := range a.relayCandidates(false) {
			rr, err := a.peerRelay(ctx, p, inner, o.timeout)
			if err != nil {
				lastErr = err
				continue
			}
			a.mu.Lock()
			a.lastCloudOk, a.cloudRoute = time.Now(), "relay"
			a.mu.Unlock()
			return rr, nil
		}
	}
	if !o.noTunnel {
		if l := a.tun.active(); l != nil {
			rr, err := a.tunnelCall(ctx, inner, o.timeout)
			if err == nil {
				a.mu.Lock()
				a.lastCloudOk, a.cloudRoute = time.Now(), "tunnel"
				a.mu.Unlock()
				return rr, nil
			}
			lastErr = err
		}
		for _, p := range a.relayCandidates(true) {
			rr, err := a.peerRelay(ctx, p, inner, o.timeout)
			if err != nil {
				lastErr = err
				continue
			}
			a.mu.Lock()
			a.lastCloudOk, a.cloudRoute = time.Now(), "tunnel"
			a.mu.Unlock()
			return rr, nil
		}
	}
	return nil, lastErr
}

func pathOnly(p string) string {
	if i := strings.IndexByte(p, '?'); i >= 0 {
		return p[:i]
	}
	return p
}

// relayCandidates: reachable peers (slug order) with internet (tunnelHolder=false) or holding the tunnel (true).
func (a *Agent) relayCandidates(tunnelHolder bool) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for slug, st := range a.peerState {
		if slug == a.site || st.ping == nil || time.Since(st.lastAnswer) > a.tm.Reachable {
			continue
		}
		if tunnelHolder {
			if st.ping.Tunnel != nil && st.ping.Tunnel.Status == "active" {
				out = append(out, slug)
			}
		} else if st.ping.Internet {
			out = append(out, slug)
		}
	}
	sort.Strings(out)
	return out
}

func (a *Agent) peerRelay(ctx context.Context, peer string, inner model.RelayInner, timeout time.Duration) (*cloudResp, error) {
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	var rr model.RelayResponse
	if err := a.peerJSON(ctx, peer, "POST", "/_opm/peer/relay", model.PeerRelayRequest{Request: inner}, &rr, timeout); err != nil {
		return nil, err
	}
	return relayToResp(&rr, "relay")
}

func relayToResp(rr *model.RelayResponse, route string) (*cloudResp, error) {
	h := http.Header{}
	for k, v := range rr.Headers {
		h.Set(k, v)
	}
	var body []byte
	if rr.BodyB64 != nil {
		b, err := base64.StdEncoding.DecodeString(*rr.BodyB64)
		if err != nil {
			return nil, err
		}
		body = b
	}
	if rr.Status == 0 {
		return nil, &netError{errors.New("relay: no response from the cloud")}
	}
	return &cloudResp{Status: rr.Status, Header: h, Body: body, Route: route}, nil
}

// cloudJSON calls the cloud and decodes JSON (non-2xx → *apiError).
func (a *Agent) cloudJSON(ctx context.Context, method, path string, in, out any, o callOpts) error {
	var body []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = b
	}
	resp, err := a.cloudCall(ctx, method, path, body, o)
	if err != nil {
		return err
	}
	if resp.Header != nil {
		a.adoptServerTime(resp.Header)
	}
	if resp.Status >= 300 {
		ae := &apiError{Status: resp.Status}
		_ = json.Unmarshal(resp.Body, &ae.Body)
		a.noteCloudResult(resp.Status, ae.Body) // §21 #90: a revoked site key shows on the Agent page
		return ae
	}
	a.noteCloudResult(resp.Status, model.APIErrorBody{})
	if out != nil && len(resp.Body) > 0 {
		return json.Unmarshal(resp.Body, out)
	}
	return nil
}

// cloudDownload fetches path into dst (resumable with Range on the direct route; relay route in 3 MiB ranges;
// tunnel as one request). wantHex (optional) is verified.
func (a *Agent) cloudDownload(ctx context.Context, path, dst, wantHex string, size int64, progress func(int64)) error {
	tmp := dst + ".part"
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	var have int64
	if st, err := os.Stat(tmp); err == nil {
		have = st.Size()
	}
	// direct, streaming
	err := a.directDownload(ctx, path, tmp, &have, progress)
	if err != nil && isNetErr(err) {
		// relay: ranges ≤ 3 MiB
		err = a.chunkedDownload(ctx, path, tmp, &have, size, progress)
	}
	if err != nil {
		return err
	}
	if wantHex != "" {
		got, err := fileSha256(tmp)
		if err != nil {
			return err
		}
		if got != wantHex {
			_ = os.Remove(tmp)
			return fmt.Errorf("sha256 mismatch: got %s want %s", got[:12], wantHex[:12])
		}
	}
	return os.Rename(tmp, dst)
}

func (a *Agent) directDownload(ctx context.Context, path, tmp string, have *int64, progress func(int64)) error {
	hdr := a.signedHeaders("GET", path, nil)
	req, err := http.NewRequestWithContext(ctx, "GET", a.apiURL(path), nil)
	if err != nil {
		return err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if *have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", *have))
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return &netError{err}
	}
	defer resp.Body.Close()
	a.adoptServerTime(resp.Header)
	a.markDirect(true)
	flag := os.O_CREATE | os.O_WRONLY
	switch resp.StatusCode {
	case 200:
		flag |= os.O_TRUNC
		*have = 0
	case 206:
		flag |= os.O_APPEND
	case 416:
		return nil // already complete
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		ae := &apiError{Status: resp.StatusCode}
		_ = json.Unmarshal(b, &ae.Body)
		return ae
	}
	f, err := os.OpenFile(tmp, flag, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 256*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return err
			}
			*have += int64(n)
			if progress != nil {
				progress(*have)
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return &netError{rerr}
		}
	}
}

func (a *Agent) chunkedDownload(ctx context.Context, path, tmp string, have *int64, size int64, progress func(int64)) error {
	const chunk = 3 << 20
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		end := *have + chunk - 1
		resp, err := a.cloudCall(ctx, "GET", path, nil, callOpts{extraHdr: map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", *have, end)}, timeout: 5 * time.Minute})
		if err != nil {
			return err
		}
		switch resp.Status {
		case 200:
			// whole body (tunnel or no range support)
			if err := f.Truncate(0); err != nil {
				return err
			}
			if _, err := f.WriteAt(resp.Body, 0); err != nil {
				return err
			}
			*have = int64(len(resp.Body))
			return nil
		case 206:
			if _, err := f.WriteAt(resp.Body, *have); err != nil {
				return err
			}
			*have += int64(len(resp.Body))
			if progress != nil {
				progress(*have)
			}
			total := size
			if cr := resp.Header.Get("Content-Range"); cr != "" {
				if i := strings.LastIndexByte(cr, '/'); i >= 0 {
					if n, err := strconv.ParseInt(cr[i+1:], 10, 64); err == nil {
						total = n
					}
				}
			}
			if len(resp.Body) == 0 || (total > 0 && *have >= total) {
				return nil
			}
		case 416:
			return nil
		default:
			ae := &apiError{Status: resp.Status}
			_ = json.Unmarshal(resp.Body, &ae.Body)
			return ae
		}
	}
}

func fileSha256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// detectIP returns the first non-loopback IPv4 address (127.0.0.1 if none).
func detectIP() string {
	ifs, err := net.InterfaceAddrs()
	if err == nil {
		for _, ad := range ifs {
			if ipn, ok := ad.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
				return ipn.IP.String()
			}
		}
	}
	return "127.0.0.1"
}

// detectAddresses returns the non-loopback IPs of the host (IPv4 first, then IPv6; link-local skipped) and the
// hostname (SPEC §21 #51: EnrollRequest / HeartbeatRequest.detectedAddresses).
func detectAddresses() []string {
	var v4, v6 []string
	if ifs, err := net.InterfaceAddrs(); err == nil {
		for _, ad := range ifs {
			ipn, ok := ad.(*net.IPNet)
			if !ok || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() || ipn.IP.IsLinkLocalMulticast() {
				continue
			}
			if ipn.IP.To4() != nil {
				v4 = append(v4, ipn.IP.String())
			} else {
				v6 = append(v6, ipn.IP.String())
			}
		}
	}
	out := append(append([]string{}, v4...), v6...)
	if h := hostname(); h != "" {
		out = append(out, h)
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}
