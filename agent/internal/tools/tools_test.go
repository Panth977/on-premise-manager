package tools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/tools/accesslog"
)

func TestMaskerSplitAcrossWrites(t *testing.T) {
	m := newMasker([]string{"s3cr3t-value", "abc", "tok"}) // "abc"/"tok" < 4 bytes: ignored
	var out []byte
	for _, p := range []string{"user=admin pass=s3cr", "3t-value end abc ", "tail s3cr3t-value"} {
		out = append(out, m.Write([]byte(p))...)
	}
	out = append(out, m.Flush()...)
	got := string(out)
	want := "user=admin pass=" + MaskText + " end abc tail " + MaskText
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if MaskString("a password1234 b", []string{"password1234", "password"}) != "a "+MaskText+" b" {
		t.Fatal("longest secret must win")
	}
	if MaskString("nothing here", nil) != "nothing here" {
		t.Fatal("no secrets → unchanged")
	}
}

func TestLoadConfig(t *testing.T) {
	c := LoadConfig("")
	if !c.HostShell || !c.ContainerExec {
		t.Fatal("defaults must be true")
	}
	p := filepath.Join(t.TempDir(), "agent.json")
	_ = os.WriteFile(p, []byte(`{"apiBase":"x","tools":{"hostShell":false},"hostShell":{"user":"opm","shell":"/bin/sh"}}`), 0o600)
	c = LoadConfig(p)
	if c.HostShell || !c.ContainerExec || c.ShellUser != "opm" || c.Shell != "/bin/sh" {
		t.Fatalf("got %+v", c)
	}
	if c.hostShellReason() == "" || c.containerExecReason() != "" {
		t.Fatal("reasons")
	}
	u, sh := c.shellFor()
	if u != "opm" || sh != "/bin/sh" {
		t.Fatal(u, sh)
	}
}

func TestTicketsAndRateLimit(t *testing.T) {
	ts := newTicketStore()
	id, _ := ts.issue(&ticket{kind: "terminal", ref: "x", sessionID: "S1"}, time.Minute)
	if _, e := ts.take(id, "terminal", Caller{SessionID: "S2"}); e == nil {
		t.Fatal("other session must fail")
	}
	// consumed even on a failed owner check (single use)
	if _, e := ts.take(id, "terminal", Caller{SessionID: "S1"}); e == nil {
		t.Fatal("ticket is single-use")
	}
	id, _ = ts.issue(&ticket{kind: "volume", ref: "v", via: "db"}, time.Minute)
	if _, e := ts.take(id, "volume", Caller{Via: "db", SessionID: "x@db"}); e != nil {
		t.Fatal("forwarded owner must pass:", e)
	}
	id, _ = ts.issue(&ticket{kind: "volume", ref: "v", sessionID: "S1"}, -time.Second)
	if _, e := ts.take(id, "volume", Caller{SessionID: "S1"}); e == nil || !strings.Contains(e.Msg, "expired") {
		t.Fatal("expired")
	}
	rl := newRateLimiter()
	c := Caller{SessionID: "S"}
	for i := 0; i < rateLimits["prune"]; i++ {
		if e := rl.allow("prune", c); e != nil {
			t.Fatal(e)
		}
	}
	if e := rl.allow("prune", c); e == nil || e.Status != 429 || e.Details["retryAfterSec"] == nil {
		t.Fatal("must be rate limited", e)
	}
	if e := rl.allow("prune", Caller{SessionID: "other"}); e != nil {
		t.Fatal("per session")
	}
}

func TestWebSocketRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !SameOrigin(r) {
			http.Error(w, "origin", 403)
			return
		}
		ws, err := Upgrade(w, r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		defer ws.Close()
		for {
			op, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			_ = ws.WriteMessage(op, append([]byte("echo:"), msg...))
		}
	}))
	defer srv.Close()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/x"
	ws, _, err := DialWS(u, http.Header{"Origin": {srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("z"), 70000) // 64-bit length frame
	for _, m := range [][]byte{[]byte("hi"), big} {
		if err := ws.WriteMessage(OpBinary, m); err != nil {
			t.Fatal(err)
		}
		op, got, err := ws.ReadMessage()
		if err != nil || op != OpBinary || !bytes.Equal(got, append([]byte("echo:"), m...)) {
			t.Fatalf("op %d err %v len %d", op, err, len(got))
		}
	}
	_ = ws.WriteMessage(OpPing, []byte("p"))
	_ = ws.WriteMessage(OpText, []byte(`{"type":"ping"}`))
	if op, got, _ := ws.ReadMessage(); op != OpText || string(got) != `echo:{"type":"ping"}` {
		t.Fatalf("text after ping: %d %q", op, got)
	}
	ws.CloseWith(1000, "bye")
	// cross-origin is refused before the upgrade
	if _, resp, err := DialWS(u, http.Header{"Origin": {"https://evil.example"}}); err == nil || resp == nil || resp.StatusCode != 403 {
		t.Fatal("cross-origin must be refused")
	}
}

func TestTranscriptMaskAndText(t *testing.T) {
	dir := t.TempDir()
	tr, err := openTranscript(dir, TranscriptMeta{ID: "abcdefgh12", SiteSlug: "app", Kind: "host", Target: "host (root)", StartedAt: ts(time.Now())}, 100, 30, []string{"hunter2-secret"})
	if err != nil {
		t.Fatal(err)
	}
	tr.Output([]byte("\x1b[1;32mroot@app\x1b[0m:/# echo hunter2-"))
	tr.Output([]byte("secret\r\nok\r\n"))
	tr.Resize(120, 40)
	code := 0
	tr.Close(&code)
	raw, _ := os.ReadFile(filepath.Join(dir, "abcdefgh12.cast"))
	if strings.Contains(string(raw), "hunter2-secret") {
		t.Fatal("secret leaked into the transcript")
	}
	txt, err := castToText(filepath.Join(dir, "abcdefgh12.cast"))
	if err != nil {
		t.Fatal(err)
	}
	if string(txt) != "root@app:/# echo "+MaskText+"\nok\n" {
		t.Fatalf("text %q", txt)
	}
	list := listTranscripts(dir)
	if len(list) != 1 || list[0].ExitCode == nil || list[0].EndedAt == nil || list[0].SizeBytes == 0 {
		t.Fatalf("meta %+v", list)
	}
	// 8 days old → removed by the cleanup
	m := list[0]
	m.StartedAt = ts(time.Now().Add(-8 * 24 * time.Hour))
	b, _ := json.Marshal(m)
	_ = os.WriteFile(filepath.Join(dir, m.ID+".json"), b, 0o600)
	if n := cleanupTranscriptDir(dir, nil); n != 1 || len(listTranscripts(dir)) != 0 {
		t.Fatal("old transcript not removed", n)
	}
}

func TestVolumesBrowser(t *testing.T) {
	h := newFakeHost(t)
	root := filepath.Join(h.dir, "volumes", "data")
	_ = os.MkdirAll(filepath.Join(root, "sub", "deep"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello password=topsecret1\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "bin.dat"), []byte{0, 1, 2, 3}, 0o644)
	_ = os.WriteFile(filepath.Join(root, "sub", "deep", "c.json"), []byte(`{"x":1}`), 0o644)
	_ = os.Symlink("/etc/passwd", filepath.Join(root, "escape"))
	h.vols = []Volume{{Slug: "data", Label: "Data", Kind: "dir", Path: root, UsedBy: []string{"api"}}}
	h.secrets = []string{"topsecret1"}
	s := New(h)
	srv := serve(t, s)
	c := call{actor: "ops@example.com", session: "S1"}
	var vols struct {
		Items []map[string]any `json:"items"`
	}
	if st := doJSON(t, srv, c, "GET", "/volumes", nil, &vols); st != 200 || len(vols.Items) != 1 || vols.Items[0]["sizeBytes"].(float64) < 30 || vols.Items[0]["fileCount"].(float64) != 3 {
		t.Fatalf("volumes %d %+v", st, vols)
	}
	var tree struct {
		Path    string           `json:"path"`
		Entries []map[string]any `json:"entries"`
	}
	doJSON(t, srv, c, "GET", "/volumes/data/tree?path=/", nil, &tree)
	names := []string{}
	for _, e := range tree.Entries {
		names = append(names, fmt.Sprint(e["name"], ":", e["type"]))
	}
	if strings.Join(names, ",") != "sub:dir,a.txt:file,bin.dat:file,escape:symlink" {
		t.Fatalf("tree %v", names)
	}
	if st := doJSON(t, srv, c, "GET", "/volumes/data/tree?path=/../../etc", nil, &tree); st != 404 && st != 422 {
		// "/../../etc" cleans to "/etc" inside the volume → not found
		t.Fatalf("escape %d", st)
	}
	var file map[string]any
	doJSON(t, srv, c, "GET", "/volumes/data/file?path=/a.txt", nil, &file)
	if file["text"] != "hello password="+MaskText+"\n" {
		t.Fatalf("file %+v", file)
	}
	doJSON(t, srv, c, "GET", "/volumes/data/file?path=/bin.dat", nil, &file)
	if file["binary"] != true || file["text"] != nil {
		t.Fatalf("binary %+v", file)
	}
	if st := doJSON(t, srv, c, "GET", "/volumes/data/file?path=/escape", nil, &file); st != 404 {
		t.Fatalf("symlink must not be read: %d", st)
	}
	// archive download: ticket (actor) → plain GET by the same session
	var tk map[string]any
	if st := doJSON(t, srv, c, "POST", "/volumes/data/download?path=/", nil, &tk); st != 200 {
		t.Fatalf("ticket %d %v", st, tk)
	}
	req, _ := http.NewRequest("GET", srv.URL+tk["url"].(string), nil)
	req.Header.Set("X-Test-Session", "S1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatal(err, resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Disposition"), "opm-app-data-") {
		t.Fatal(resp.Header.Get("Content-Disposition"))
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	got := map[string]string{}
	for {
		hd, err := tr.Next()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(tr)
		got[hd.Name] = string(b)
		if hd.Typeflag == tar.TypeSymlink {
			got[hd.Name] = "->" + hd.Linkname
		}
	}
	resp.Body.Close()
	if got["a.txt"] != "hello password=topsecret1\n" || got["sub/deep/c.json"] != `{"x":1}` || got["escape"] != "->/etc/passwd" {
		t.Fatalf("archive %v", got)
	}
	// the ticket is single-use
	resp2, _ := http.DefaultClient.Do(req)
	if resp2.StatusCode != 401 {
		t.Fatal("reuse must fail", resp2.StatusCode)
	}
	if a := h.activities("volume.downloaded"); len(a) != 1 || a[0].Actor != "ops@example.com" {
		t.Fatalf("activity %+v", a)
	}
}

func TestNetcheckHost(t *testing.T) {
	h := newFakeHost(t)
	s := New(h)
	srv := serve(t, s)
	c := call{actor: "ops@example.com", session: "S1"}
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "yes")
		w.WriteHeader(204)
	}))
	defer web.Close()
	var res NetResult
	doJSON(t, srv, c, "POST", "/netcheck", map[string]any{"from": map[string]string{"kind": "host"}, "check": "http", "target": web.URL + "/health"}, &res)
	if !res.OK || res.Status == nil || *res.Status != 204 || res.Headers["X-Test"] != "yes" || res.Timing == nil || res.Timing.ConnectMs == nil {
		t.Fatalf("http %+v", res)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	doJSON(t, srv, c, "POST", "/netcheck", map[string]any{"check": "tcp", "target": ln.Addr().String()}, &res)
	if !res.OK || !strings.Contains(res.Summary, "connected") {
		t.Fatalf("tcp %+v", res)
	}
	ln.Close()
	doJSON(t, srv, c, "POST", "/netcheck", map[string]any{"check": "tcp", "target": ln.Addr().String(), "timeoutSec": 2}, &res)
	if res.OK || res.Summary == "" {
		t.Fatalf("closed port %+v", res)
	}
	doJSON(t, srv, c, "POST", "/netcheck", map[string]any{"check": "dns", "target": "localhost"}, &res)
	if !res.OK || len(res.Addresses) == 0 {
		t.Fatalf("dns %+v", res)
	}
	var e map[string]any
	if st := doJSON(t, srv, c, "POST", "/netcheck", map[string]any{"check": "http", "target": "file:///etc/passwd"}, &e); st != 422 {
		t.Fatal("non-http url must be refused", st)
	}
	if st := doJSON(t, srv, c, "POST", "/netcheck", map[string]any{"check": "dns", "target": "a;rm -rf /"}, &e); st != 422 {
		t.Fatal("bad host must be refused", st)
	}
	if n := len(h.activities("tool.netcheck")); n != 4 {
		t.Fatalf("tool.netcheck activities: %d", n)
	}
	ok, sum := parsePing("3 packets transmitted, 3 packets received, 0.0% packet loss\nround-trip min/avg/max = 0.1/0.25/0.4 ms")
	if !ok || sum != "3/3 replies, avg 0.25 ms" {
		t.Fatal(sum)
	}
	r := &NetResult{}
	parseContainerHTTP(r, "wget", []string{"Connecting to x", "  HTTP/1.1 301 Moved", "  Location: /b", "  HTTP/1.1 200 OK", "  Content-Type: text/html", "  Set-Cookie: sid=abc"}, 0)
	if r.Status == nil || *r.Status != 200 || r.Headers["Content-Type"] != "text/html" || strings.Contains(r.Headers["Set-Cookie"], "abc") {
		t.Fatalf("wget parse %+v", r)
	}
}

func TestAccessLogWriterAndAddTo(t *testing.T) {
	dir := t.TempDir()
	w := accesslog.WriterFor(dir)
	for i := 0; i < 3; i++ {
		w.Write(accesslog.Line{T: ts(time.Now()), Method: "GET", URI: fmt.Sprintf("/p%d", i), Status: 200, Host: "x"})
	}
	ls, _ := tailLines(filepath.Join(dir, accesslog.FileName), 2)
	if len(ls) != 2 || !strings.Contains(string(ls[1]), "/p2") {
		t.Fatalf("tail %q", ls)
	}
	servers := map[string]any{"edge_http": map[string]any{"routes": []any{map[string]any{"handle": []any{}}}}}
	accesslog.AddTo(servers, dir)
	routes := servers["edge_http"].(map[string]any)["routes"].([]any)
	if len(routes) != 2 || !strings.Contains(fmt.Sprint(routes[0]), "opm_access_log") {
		t.Fatalf("routes %v", routes)
	}
}

func TestFollowerRotation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.log")
	_ = os.WriteFile(p, []byte("a\nb\n"), 0o600)
	fl := &follower{current: func() string { return p }, path: p, off: 4}
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("c\npart")
	f.Close()
	if got := fl.poll(); len(got) != 1 || string(got[0]) != "c" {
		t.Fatalf("%q", got)
	}
	_ = os.WriteFile(p, []byte("new\n"), 0o600) // rotated: smaller
	if got := fl.poll(); len(got) != 1 || string(got[0]) != "new" {
		t.Fatalf("after rotation %q", got)
	}
}

func TestHostShellTerminal(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no pty")
	}
	h := newFakeHost(t)
	h.secrets = []string{"very-secret-value"}
	s := New(h)
	srv := serve(t, s)
	c := call{actor: "ops@example.com", session: "S1"}
	var e map[string]any
	// typed confirmation required
	if st := doJSON(t, srv, c, "POST", "/terminal/sessions", map[string]any{"kind": "host", "confirm": "wrong"}, &e); st != 422 {
		t.Fatalf("confirm: %d %v", st, e)
	}
	var cr map[string]any
	if st := doJSON(t, srv, c, "POST", "/terminal/sessions", map[string]any{"kind": "host", "confirm": "app", "cols": 100, "rows": 30}, &cr); st != 201 {
		t.Fatalf("create %d %v", st, cr)
	}
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + cr["wsPath"].(string)
	// another session cannot use the ticket
	if _, resp, err := DialWS(u, http.Header{"X-Test-Session": {"S2"}}); err == nil || resp.StatusCode != 401 {
		t.Fatal("ticket of another session must fail")
	}
	// the failed attempt consumed the ticket: reserve again
	doJSON(t, srv, c, "POST", "/terminal/sessions", map[string]any{"kind": "host", "confirm": "app"}, &cr)
	u = "ws" + strings.TrimPrefix(srv.URL, "http") + cr["wsPath"].(string)
	ws, _, err := DialWS(u, http.Header{"X-Test-Session": {"S1"}, "X-Test-Actor": {"ops@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	op, msg, err := ws.ReadMessage()
	if err != nil || op != OpText || !strings.Contains(string(msg), `"ready"`) {
		t.Fatalf("ready: %v %s", err, msg)
	}
	_ = ws.WriteMessage(OpText, []byte(`{"type":"resize","cols":90,"rows":20}`))
	_ = ws.WriteMessage(OpBinary, []byte("echo opm-$((40+2)) very-secret-value; exit 3\n"))
	var out strings.Builder
	deadline := time.Now().Add(15 * time.Second)
	exitMsg := ""
	for time.Now().Before(deadline) && exitMsg == "" {
		op, msg, err := ws.ReadMessage()
		if err != nil {
			break
		}
		if op == OpBinary {
			out.Write(msg)
		} else if strings.Contains(string(msg), `"exit"`) {
			exitMsg = string(msg)
		}
	}
	if !strings.Contains(out.String(), "opm-42") {
		t.Fatalf("no echo output: %q", out.String())
	}
	if !strings.Contains(exitMsg, `"code":3`) {
		t.Fatalf("exit %q", exitMsg)
	}
	time.Sleep(200 * time.Millisecond)
	if a := h.activities("terminal.opened"); len(a) != 1 || a[0].Actor != "ops@example.com" || a[0].Data["kind"] != "host" {
		t.Fatalf("opened %+v", a)
	}
	cl := h.activities("terminal.closed")
	if len(cl) != 1 || cl[0].Data["exitCode"] != 3 || cl[0].Data["reason"] != "exited" {
		t.Fatalf("closed %+v", cl)
	}
	var list struct {
		Items []TranscriptMeta `json:"items"`
	}
	doJSON(t, srv, c, "GET", "/terminal/transcripts", nil, &list)
	if len(list.Items) != 1 || list.Items[0].Kind != "host" {
		t.Fatalf("transcripts %+v", list)
	}
	raw, _ := os.ReadFile(filepath.Join(h.dir, "transcripts", list.Items[0].ID+".cast"))
	if !strings.Contains(string(raw), "opm-42") || strings.Contains(string(raw), "very-secret-value") {
		t.Fatalf("transcript content: %s", raw)
	}
	var tk map[string]any
	doJSON(t, srv, c, "POST", "/terminal/transcripts/"+list.Items[0].ID+"/ticket?format=text", nil, &tk)
	req, _ := http.NewRequest("GET", srv.URL+tk["url"].(string), nil)
	req.Header.Set("X-Test-Session", "S1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "opm-42") || strings.Contains(string(b), "\x1b[") {
		t.Fatalf("text transcript %q", b)
	}
}

func TestTerminalLimitsAndSwitches(t *testing.T) {
	h := newFakeHost(t)
	p := filepath.Join(h.dir, "agent.json")
	_ = os.WriteFile(p, []byte(`{"tools":{"hostShell":false,"containerExec":false}}`), 0o600)
	h.cfgPath = p
	s := New(h)
	srv := serve(t, s)
	c := call{actor: "ops@example.com", session: "S1"}
	var e struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if st := doJSON(t, srv, c, "POST", "/terminal/sessions", map[string]any{"kind": "host", "confirm": "app"}, &e); st != 403 || e.Error.Details["tool"] != "hostShell" {
		t.Fatalf("host disabled %d %+v", st, e)
	}
	if st := doJSON(t, srv, c, "POST", "/terminal/sessions", map[string]any{"kind": "container", "containerSlug": "x"}, &e); st != 403 || e.Error.Details["tool"] != "containerExec" {
		t.Fatalf("exec disabled %d %+v", st, e)
	}
	var info map[string]any
	doJSON(t, srv, c, "GET", "/info", nil, &info)
	if info["hostShell"].(map[string]any)["enabled"] != false || info["configPath"] != p {
		t.Fatalf("info %+v", info)
	}
	// max 4 open + reserved sessions
	s.cfg.HostShell = true
	for i := 0; i < 4; i++ {
		if st := doJSON(t, srv, call{actor: "a@b.co", session: fmt.Sprint("S", i)}, "POST", "/terminal/sessions", map[string]any{"kind": "host", "confirm": "app"}, nil); st != 201 {
			t.Fatal("reserve", i, st)
		}
	}
	if st := doJSON(t, srv, c, "POST", "/terminal/sessions", map[string]any{"kind": "host", "confirm": "app"}, &e); st != 409 || e.Error.Details["max"] != float64(4) {
		t.Fatalf("5th must conflict: %d %+v", st, e)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	doJSON(t, srv, c, "GET", "/terminal/sessions", nil, &list)
	if len(list.Items) != 4 || list.Items[0]["open"] != false {
		t.Fatalf("sessions %+v", list)
	}
	_ = context.Background()
}
