package tools

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/docker"
)

type netReq struct {
	From struct {
		Kind          string `json:"kind"`
		ContainerSlug string `json:"containerSlug"`
	} `json:"from"`
	Check      string `json:"check"`
	Target     string `json:"target"`
	TimeoutSec int    `json:"timeoutSec"`
}

// NetResult mirrors the shared NetcheckResult.
type NetResult struct {
	OK         bool              `json:"ok"`
	Check      string            `json:"check"`
	From       string            `json:"from"`
	Target     string            `json:"target"`
	At         string            `json:"at"`
	DurationMs int64             `json:"durationMs"`
	Summary    string            `json:"summary"`
	Addresses  []string          `json:"addresses,omitempty"`
	Status     *int              `json:"status,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Timing     *NetTiming        `json:"timing,omitempty"`
	Output     string            `json:"output,omitempty"`
	Tool       string            `json:"tool,omitempty"`
	Error      string            `json:"error,omitempty"`
}

// NetTiming is the HTTP timing breakdown.
type NetTiming struct {
	DNSMs     *float64 `json:"dnsMs"`
	ConnectMs *float64 `json:"connectMs"`
	TLSMs     *float64 `json:"tlsMs"`
	TTFBMs    *float64 `json:"ttfbMs"`
	TotalMs   float64  `json:"totalMs"`
}

var hostRe = regexp.MustCompile(`^[A-Za-z0-9._\-]{1,253}$|^\[?[0-9A-Fa-f:.]{2,45}\]?$`)

func splitHostPort(t string) (string, int, *Error) {
	h, p, err := net.SplitHostPort(t)
	if err != nil {
		return "", 0, invalid("target: host:port (e.g. db:5432)")
	}
	port, err := strconv.Atoi(p)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, invalid("target: port 1–65535")
	}
	if !hostRe.MatchString(h) {
		return "", 0, invalid("target: invalid host")
	}
	return h, port, nil
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func (s *Service) handleNetcheck(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	var req netReq
	if e := readBody(r, &req); e != nil {
		return nil, 0, e
	}
	if e := s.rl.allow("netcheck", c); e != nil {
		return nil, 0, e
	}
	req.Target = strings.TrimSpace(req.Target)
	timeout := time.Duration(clamp(req.TimeoutSec, 1, 30, 10)) * time.Second
	// validate the target per check
	switch req.Check {
	case "dns", "ping":
		if !hostRe.MatchString(strings.Trim(req.Target, "[]")) {
			return nil, 0, invalid("target: a host name or IP address")
		}
	case "tcp":
		if _, _, e := splitHostPort(req.Target); e != nil {
			return nil, 0, e
		}
	case "http":
		u, err := url.Parse(req.Target)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(req.Target, " \n\r\t") {
			return nil, 0, invalid("target: an http:// or https:// URL")
		}
	default:
		return nil, 0, invalid("check must be dns, tcp, http or ping")
	}
	var res *NetResult
	switch req.From.Kind {
	case "", "host":
		res = s.hostCheck(r.Context(), req.Check, req.Target, timeout)
	case "container":
		if reason := s.cfg.containerExecReason(); reason != "" {
			return nil, 0, &Error{Status: 403, Code: "forbidden", Msg: "checks from inside a container need container exec, which is " + reason, Details: map[string]any{"tool": "containerExec"}}
		}
		t, e := s.target(r.Context(), req.From.ContainerSlug)
		if e != nil {
			return nil, 0, e
		}
		if t.ID == "" || t.FailedCopy {
			return nil, 0, &Error{Status: 409, Code: "conflict", Msg: t.Ref + " is not a running container"}
		}
		res = s.containerCheck(r.Context(), t, req.Check, req.Target, timeout)
	default:
		return nil, 0, invalid("from.kind must be host or container")
	}
	res.At = ts(time.Now())
	res.Output = MaskString(res.Output, s.h.SecretValues())
	s.activity(c, "tool.netcheck", fmt.Sprintf("%s %s from %s: %s", strings.ToUpper(req.Check), req.Target, res.From, res.Summary),
		map[string]any{"from": res.From, "check": req.Check, "target": req.Target, "ok": res.OK, "durationMs": res.DurationMs, "summary": res.Summary, "siteSlug": s.h.Site()})
	return res, 200, nil
}

// ───────────────────────── from the host (native)

func (s *Service) hostCheck(ctx context.Context, check, target string, timeout time.Duration) *NetResult {
	res := &NetResult{Check: check, From: "host", Target: target}
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() { res.DurationMs = time.Since(start).Milliseconds() }()
	fail := func(err error) *NetResult {
		res.OK, res.Error = false, err.Error()
		res.Summary = shortErr(err)
		return res
	}
	switch check {
	case "dns":
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, strings.Trim(target, "[]"))
		if err != nil {
			return fail(err)
		}
		for _, a := range addrs {
			res.Addresses = append(res.Addresses, a.IP.String())
		}
		sort.Strings(res.Addresses)
		res.OK = true
		res.Summary = fmt.Sprintf("%d %s in %d ms", len(res.Addresses), plural(len(res.Addresses), "address", "addresses"), time.Since(start).Milliseconds())
	case "tcp":
		var d net.Dialer
		t0 := time.Now()
		conn, err := d.DialContext(ctx, "tcp", target)
		if err != nil {
			return fail(err)
		}
		res.Addresses = []string{conn.RemoteAddr().String()}
		conn.Close()
		res.OK = true
		res.Summary = fmt.Sprintf("connected to %s in %d ms", conn.RemoteAddr().String(), time.Since(t0).Milliseconds())
	case "http":
		return s.httpCheck(ctx, res, target, start)
	case "ping":
		args := []string{"-c", "3", "-W", "2", strings.Trim(target, "[]")}
		if runtime.GOOS == "darwin" {
			args = []string{"-c", "3", "-t", strconv.Itoa(int(timeout.Seconds())), strings.Trim(target, "[]")}
		}
		out, err := exec.CommandContext(ctx, "ping", args...).CombinedOutput()
		res.Tool, res.Output = "ping", capOut(string(out))
		res.OK, res.Summary = parsePing(string(out))
		if err != nil && !res.OK {
			if res.Summary == "" {
				res.Summary = shortErr(err)
			}
			res.Error = err.Error()
		}
	}
	return res
}

func (s *Service) httpCheck(ctx context.Context, res *NetResult, target string, start time.Time) *NetResult {
	var dnsS, dnsE, conS, conE, tlsS, tlsE, first time.Time
	trace := &httptrace.ClientTrace{
		DNSStart:             func(httptrace.DNSStartInfo) { dnsS = time.Now() },
		DNSDone:              func(httptrace.DNSDoneInfo) { dnsE = time.Now() },
		ConnectStart:         func(string, string) { conS = time.Now() },
		ConnectDone:          func(string, string, error) { conE = time.Now() },
		TLSHandshakeStart:    func() { tlsS = time.Now() },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { tlsE = time.Now() },
		GotFirstResponseByte: func() { first = time.Now() },
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), "GET", target, nil)
	if err != nil {
		res.Error, res.Summary = err.Error(), shortErr(err)
		return res
	}
	req.Header.Set("User-Agent", "opm-agent-netcheck/1")
	cl := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSHandshakeTimeout: 10 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Do(req)
	total := time.Since(start)
	tm := &NetTiming{TotalMs: ms(total)}
	span := func(a, b time.Time) *float64 {
		if a.IsZero() || b.IsZero() {
			return nil
		}
		v := ms(b.Sub(a))
		return &v
	}
	tm.DNSMs, tm.ConnectMs, tm.TLSMs, tm.TTFBMs = span(dnsS, dnsE), span(conS, conE), span(tlsS, tlsE), span(start, first)
	res.Timing = tm
	if err != nil {
		res.Error, res.Summary = err.Error(), shortErr(err)
		return res
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	res.Timing.TotalMs = ms(time.Since(start))
	st := resp.StatusCode
	res.Status = &st
	res.Headers = map[string]string{}
	for k, v := range resp.Header {
		if len(res.Headers) >= 60 {
			break
		}
		val := strings.Join(v, ", ")
		if strings.EqualFold(k, "Set-Cookie") {
			val = "(" + strconv.Itoa(len(v)) + " cookies, values hidden)"
		}
		res.Headers[k] = val
	}
	res.OK = st < 500
	res.Summary = fmt.Sprintf("%s in %d ms", resp.Status, int(res.Timing.TotalMs))
	if loc := resp.Header.Get("Location"); loc != "" && st >= 300 && st < 400 {
		res.Summary += " → " + loc
	}
	return res
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func shortErr(err error) string {
	s := err.Error()
	for _, k := range []string{"no such host", "connection refused", "i/o timeout", "context deadline exceeded", "no route to host", "network is unreachable", "certificate", "connection reset"} {
		if strings.Contains(s, k) {
			if k == "context deadline exceeded" || k == "i/o timeout" {
				return "timed out"
			}
			if k == "certificate" {
				return "TLS certificate error"
			}
			return k
		}
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

func capOut(s string) string {
	if len(s) > 64*1024 {
		return s[:64*1024] + "\n… (truncated)"
	}
	return s
}

var pingRecv = regexp.MustCompile(`(\d+) packets transmitted, (\d+) (?:packets )?received`)
var pingRtt = regexp.MustCompile(`= ([0-9.]+)/([0-9.]+)/([0-9.]+)`)

func parsePing(out string) (bool, string) {
	m := pingRecv.FindStringSubmatch(out)
	if m == nil {
		return false, ""
	}
	sent, _ := strconv.Atoi(m[1])
	recv, _ := strconv.Atoi(m[2])
	sum := fmt.Sprintf("%d/%d replies", recv, sent)
	if r := pingRtt.FindStringSubmatch(out); r != nil {
		sum += ", avg " + r[2] + " ms"
	}
	return recv > 0, sum
}

// ───────────────────────── from inside a container (busybox / coreutils tools, whichever exists)

// scripts take the target as positional arguments (never interpolated into the script).
var netScripts = map[string]string{
	"dns": `if command -v getent >/dev/null 2>&1; then echo "opm-tool getent"; getent ahosts "$1"; ` +
		`elif command -v nslookup >/dev/null 2>&1; then echo "opm-tool nslookup"; nslookup "$1"; ` +
		`else echo "opm-tool none"; exit 127; fi`,
	"tcp": `if command -v nc >/dev/null 2>&1; then echo "opm-tool nc"; nc -z -w "$3" "$1" "$2"; ` +
		`elif command -v bash >/dev/null 2>&1; then echo "opm-tool bash"; bash -c 'exec 3<>"/dev/tcp/$0/$1"' "$1" "$2"; ` +
		`else echo "opm-tool none"; exit 127; fi`,
	"http": `if command -v curl >/dev/null 2>&1; then echo "opm-tool curl"; curl -sS -o /dev/null -D - -m "$2" -w 'opm-timing %{http_code} %{time_namelookup} %{time_connect} %{time_appconnect} %{time_starttransfer} %{time_total}\n' "$1"; ` +
		`elif command -v wget >/dev/null 2>&1; then echo "opm-tool wget"; wget -S -O /dev/null -T "$2" "$1" 2>&1; ` +
		`else echo "opm-tool none"; exit 127; fi`,
	"ping": `if command -v ping >/dev/null 2>&1; then echo "opm-tool ping"; ping -c 3 -W 2 "$1"; ` +
		`else echo "opm-tool none"; exit 127; fi`,
}

func (s *Service) containerCheck(ctx context.Context, t *Target, check, target string, timeout time.Duration) *NetResult {
	res := &NetResult{Check: check, From: t.Ref, Target: target}
	start := time.Now()
	args := []string{"sh", "-c", netScripts[check], "sh"}
	switch check {
	case "dns", "ping":
		args = append(args, strings.Trim(target, "[]"))
	case "tcp":
		h, p, _ := splitHostPort(target)
		args = append(args, h, strconv.Itoa(p), strconv.Itoa(int(timeout.Seconds())))
	case "http":
		args = append(args, target, strconv.Itoa(int(timeout.Seconds())))
	}
	cctx, cancel := context.WithTimeout(ctx, timeout+5*time.Second)
	defer cancel()
	code, out, err := runExec(cctx, s.h.Docker(), t.ID, args)
	res.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		res.Error, res.Summary = err.Error(), shortErr(err)
		return res
	}
	tool := "none"
	var body []string
	for _, ln := range strings.Split(out, "\n") {
		if strings.HasPrefix(ln, "opm-tool ") {
			tool = strings.TrimPrefix(ln, "opm-tool ")
			continue
		}
		body = append(body, ln)
	}
	text := strings.TrimSpace(strings.Join(body, "\n"))
	res.Tool, res.Output = tool, capOut(text)
	if tool == "none" {
		res.Error = "the container image has no tool for this check (looked for: " + map[string]string{"dns": "getent, nslookup", "tcp": "nc, bash", "http": "curl, wget", "ping": "ping"}[check] + ")"
		res.Summary = "no " + check + " tool in the image"
		return res
	}
	switch check {
	case "dns":
		seen := map[string]bool{}
		for _, ln := range body {
			fs := strings.Fields(ln)
			switch {
			case tool == "getent" && len(fs) >= 1 && net.ParseIP(fs[0]) != nil:
				seen[fs[0]] = true
			case tool == "nslookup" && strings.HasPrefix(strings.TrimSpace(ln), "Address") && len(fs) >= 2:
				ip := fs[len(fs)-1]
				if h, _, err := net.SplitHostPort(ip); err == nil {
					ip = h
				}
				if net.ParseIP(ip) != nil && !strings.Contains(ln, "#53") {
					seen[ip] = true
				}
			}
		}
		for a := range seen {
			res.Addresses = append(res.Addresses, a)
		}
		sort.Strings(res.Addresses)
		res.OK = code == 0 && len(res.Addresses) > 0
		if res.OK {
			res.Summary = fmt.Sprintf("%d %s in %d ms (%s)", len(res.Addresses), plural(len(res.Addresses), "address", "addresses"), res.DurationMs, tool)
		} else {
			res.Summary = "name not resolved (" + tool + ")"
		}
	case "tcp":
		res.OK = code == 0
		if res.OK {
			res.Summary = fmt.Sprintf("connected in %d ms (%s)", res.DurationMs, tool)
		} else {
			res.Summary = "could not connect (" + tool + ", exit " + strconv.Itoa(code) + ")"
			if text != "" {
				res.Summary = shortErr(fmt.Errorf("%s", text)) + " (" + tool + ")"
			}
		}
	case "http":
		parseContainerHTTP(res, tool, body, code)
	case "ping":
		res.OK, res.Summary = parsePing(text)
		if res.Summary == "" {
			res.Summary = "no reply (exit " + strconv.Itoa(code) + ")"
		}
	}
	return res
}

var statusLine = regexp.MustCompile(`^\s*HTTP/[0-9.]+ (\d{3})(.*)$`)

func parseContainerHTTP(res *NetResult, tool string, body []string, code int) {
	res.Headers = map[string]string{}
	var status int
	reason := ""
	for _, ln := range body {
		ln = strings.TrimRight(ln, "\r")
		if m := statusLine.FindStringSubmatch(ln); m != nil {
			status, _ = strconv.Atoi(m[1])
			reason = strings.TrimSpace(m[2])
			res.Headers = map[string]string{} // keep the last response's headers (redirects)
			continue
		}
		if strings.HasPrefix(ln, "opm-timing ") {
			fs := strings.Fields(ln)
			if len(fs) == 7 {
				p := func(s string) *float64 {
					v, err := strconv.ParseFloat(s, 64)
					if err != nil || v == 0 {
						return nil
					}
					x := v * 1000
					return &x
				}
				dns, con, tlsT, ttfb, tot := p(fs[2]), p(fs[3]), p(fs[4]), p(fs[5]), p(fs[6])
				tm := &NetTiming{DNSMs: dns}
				if con != nil && dns != nil {
					v := *con - *dns
					tm.ConnectMs = &v
				}
				if tlsT != nil && con != nil {
					v := *tlsT - *con
					tm.TLSMs = &v
				}
				tm.TTFBMs = ttfb
				if tot != nil {
					tm.TotalMs = *tot
				}
				res.Timing = tm
				if s, err := strconv.Atoi(fs[1]); err == nil && s > 0 {
					status = s
				}
			}
			continue
		}
		if k, v, ok := strings.Cut(strings.TrimSpace(ln), ":"); ok && status > 0 && !strings.Contains(k, " ") && len(res.Headers) < 60 {
			if strings.EqualFold(k, "Set-Cookie") {
				v = " (values hidden)"
			}
			res.Headers[k] = strings.TrimSpace(v)
		}
	}
	if status > 0 {
		res.Status = &status
		res.OK = status < 500
		res.Summary = fmt.Sprintf("%d %s in %d ms (%s)", status, reason, res.DurationMs, tool)
		res.Summary = strings.Replace(res.Summary, "  ", " ", 1)
		return
	}
	res.Summary = "no HTTP answer (" + tool + ", exit " + strconv.Itoa(code) + ")"
}

// runExec runs a command in a container (no TTY) and returns the exit code + combined output (≤ 1 MiB).
func runExec(ctx context.Context, dc *docker.Client, id string, cmd []string) (int, string, error) {
	execID, err := dc.ExecCreate(ctx, id, docker.ExecConfig{Cmd: cmd, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return -1, "", err
	}
	conn, err := dc.ExecAttach(ctx, execID, false)
	if err != nil {
		return -1, "", err
	}
	go func() { <-ctx.Done(); conn.Close() }()
	var out bytes.Buffer
	br := bufio.NewReader(conn)
	for out.Len() < 1<<20 {
		var hdr [8]byte
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			break
		}
		n := int(binary.BigEndian.Uint32(hdr[4:8]))
		if _, err := io.CopyN(&out, br, int64(n)); err != nil {
			break
		}
	}
	conn.Close()
	if ctx.Err() != nil {
		return -1, out.String(), fmt.Errorf("timed out")
	}
	for i := 0; i < 30; i++ {
		st, err := dc.ExecInspect(context.Background(), execID)
		if err != nil {
			return -1, out.String(), err
		}
		if !st.Running {
			if st.ExitCode != nil {
				return *st.ExitCode, out.String(), nil
			}
			return -1, out.String(), nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return -1, out.String(), nil
}
