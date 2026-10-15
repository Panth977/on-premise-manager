package agent

// §21 #100 store-and-forward transfers + the server filesystem picker and its safety policy.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/fakecloud"
	"opm/agent/internal/model"
)

func safOptions(e *upgEnv) Options {
	o := e.options("0.11.2-1")
	o.Timings.Transfers = 300 * time.Millisecond
	o.Timings.TransfersMax = time.Second
	return o
}

func (e *upgEnv) startSAF() *running {
	e.t.Helper()
	a, err := New(safOptions(e))
	if err != nil {
		e.t.Fatal(err)
	}
	r := &running{a: a, done: make(chan struct{})}
	go func() { _ = a.Run(context.Background()); close(r.done) }()
	e.t.Cleanup(func() {
		a.Stop()
		select {
		case <-r.done:
		case <-time.After(30 * time.Second):
		}
	})
	waitUntil(e.t, 20*time.Second, "agent port", func() bool {
		resp, err := http.Get(e.url("/_opm/healthz"))
		if err == nil {
			resp.Body.Close()
		}
		return err == nil && resp.StatusCode == 200
	})
	return r
}

func listTransfers(c *client, site, dir string) []model.Transfer {
	var l model.ListResponse[model.Transfer]
	if st := c.do("GET", "/_opm/api/sites/"+site+"/transfers?direction="+dir, nil, &l); st != 200 {
		c.t.Fatalf("list %s: %d", dir, st)
	}
	return l.Items
}

// Agent → cloud completes with NOBODY on the receiving end: the cloud is unreachable when the file is sent (queued on
// the site, the request does not wait), then reachable; no human request touches the cloud in between.
func TestTransferToCloudStoreAndForward(t *testing.T) {
	fc := fakecloud.Start("DepSAF1")
	defer fc.Close()
	e := newUpgEnv(t, fc, "app")
	r := e.startSAF()
	waitUntil(t, 20*time.Second, "enrolled", func() bool { _, ok := fc.Agent("app"); return ok && r.a.enrolled })
	c := login(t, r.a, e.url(""))

	src := filepath.Join(t.TempDir(), "dump.bin")
	data := bytes.Repeat([]byte("opm-store-and-forward-"), 600_000) // ~13 MiB → 2 upload parts
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	fc.BlockSite("app", true) // the site is offline for the cloud
	t0 := time.Now()
	var tr model.Transfer
	if st := c.do("POST", "/_opm/api/sites/app/transfers/from-path", model.SendFromPathRequest{Path: src, Note: "nightly"}, &tr); st != 202 {
		t.Fatalf("send %d %+v", st, tr)
	}
	if time.Since(t0) > 5*time.Second {
		t.Fatalf("the send waited for the cloud (%s)", time.Since(t0))
	}
	if !strings.HasPrefix(tr.TransferID, "local-") || tr.Delivery == nil || tr.Delivery.Phase != "queued" || tr.SourcePath == nil || *tr.SourcePath != src {
		t.Fatalf("queued %+v %+v", tr, tr.Delivery)
	}
	// retries while offline: still queued, attempts counted, error shown
	waitUntil(t, 15*time.Second, "a failed attempt", func() bool {
		l := listTransfers(c, "app", "to-cloud")
		return len(l) == 1 && l[0].Delivery != nil && l[0].Delivery.Phase == "queued" && l[0].Delivery.Attempts >= 1 && l[0].Delivery.LastError != nil
	})
	if n := len(fc.Transfers()); n != 0 {
		t.Fatalf("the cloud has %d transfers while blocked", n)
	}
	if act := activitiesOf(r.a, "transfer.sent"); len(act) != 1 || !strings.Contains(string(mustJSON(act[0].Data)), `"source":"path"`) {
		t.Fatalf("activity %+v", act)
	}
	fc.BlockSite("app", false) // back online
	var cloudID string
	waitUntil(t, 30*time.Second, "stored in the cloud", func() bool {
		for _, ct := range fc.Transfers() {
			if ct.Direction == "to-cloud" && ct.Status == "ready" {
				cloudID = ct.TransferID
				return true
			}
		}
		return false
	})
	if _, got, _ := fc.Transfer(cloudID); !bytes.Equal(got, data) {
		t.Fatalf("cloud bytes differ (%d vs %d)", len(got), len(data))
	}
	waitUntil(t, 10*time.Second, "agent shows stored", func() bool {
		l := listTransfers(c, "app", "to-cloud")
		return len(l) == 1 && l[0].TransferID == cloudID && l[0].Status == "ready" && l[0].Delivery.Phase == "stored" && l[0].Delivery.BytesDone == int64(len(data)) &&
			l[0].SourcePath != nil && *l[0].SourcePath == src
	})
	// a browser upload is queued too and goes up on its own
	req, _ := http.NewRequest("POST", e.url("/_opm/api/sites/app/transfers/upload?fileName=b.txt"), strings.NewReader("hello"))
	req.Header.Set("Cookie", "opm_session_app="+c.cookie)
	req.Header.Set("Authorization", c.auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 202 {
		t.Fatalf("browser send %v %v", err, resp)
	}
	resp.Body.Close()
	waitUntil(t, 20*time.Second, "browser file stored", func() bool {
		n := 0
		for _, ct := range fc.Transfers() {
			if ct.Direction == "to-cloud" && ct.Status == "ready" {
				n++
			}
		}
		return n == 2
	})
}

// Cloud → agent: a file sent while the agent is NOT running is fetched when it comes online; the cloud learns it (fetchedAt).
func TestTransferToSiteFetchedAfterOnline(t *testing.T) {
	fc := fakecloud.Start("DepSAF2")
	defer fc.Close()
	e := newUpgEnv(t, fc, "app")
	r := e.startSAF()
	waitUntil(t, 20*time.Second, "enrolled", func() bool { _, ok := fc.Agent("app"); return ok && r.a.enrolled })
	r.stop(t) // the site goes offline

	tr := fc.AddTransferToSite("app", "config.zip", []byte("PK-the-file"), "for you")
	if got, _, _ := fc.Transfer(tr.TransferID); got.FetchedAt != nil {
		t.Fatal("fetched before the agent ran")
	}
	r2 := e.startSAF() // back online, nobody has the agent GUI open
	waitUntil(t, 30*time.Second, "fetched (cloud)", func() bool { got, _, _ := fc.Transfer(tr.TransferID); return got.FetchedAt != nil })
	c := login(t, r2.a, e.url(""))
	l := listTransfers(c, "app", "to-site")
	if len(l) != 1 || l[0].Delivery == nil || l[0].Delivery.Phase != "on-site" || l[0].Status != "ready" {
		t.Fatalf("receive list %+v", l)
	}
	if _, lp, _ := r2.a.transfer(tr.TransferID); !fileIs(t, lp, []byte("PK-the-file")) {
		t.Fatalf("local file %s", lp)
	}
	if got, _, _ := fc.Transfer(tr.TransferID); got.Status != "ready" {
		t.Fatalf("cloud status changed: %s", got.Status) // fetched is not a status change
	}
}

// Cloud deletes a file the site already received: the agent drops it from Receive and removes its own copy.
func TestTransferToSiteDeletedInCloud(t *testing.T) {
	fc := fakecloud.Start("DepSAF3")
	defer fc.Close()
	e := newUpgEnv(t, fc, "app")
	r := e.startSAF()
	waitUntil(t, 20*time.Second, "enrolled", func() bool { _, ok := fc.Agent("app"); return ok && r.a.enrolled })
	keep := fc.AddTransferToSite("app", "keep.txt", []byte("stays"), "")
	tr := fc.AddTransferToSite("app", "old.zip", []byte("PK-old"), "")
	waitUntil(t, 30*time.Second, "both fetched", func() bool {
		_, a, _ := r.a.transfer(keep.TransferID)
		_, b, _ := r.a.transfer(tr.TransferID)
		return a != "" && b != ""
	})
	_, lp, _ := r.a.transfer(tr.TransferID)
	fc.DeleteTransfer(tr.TransferID)
	waitUntil(t, 30*time.Second, "dropped on the site", func() bool { _, _, ok := r.a.transfer(tr.TransferID); return !ok })
	if _, err := os.Stat(lp); !os.IsNotExist(err) {
		t.Fatalf("local copy still there: %v", err)
	}
	c := login(t, r.a, e.url(""))
	if l := listTransfers(c, "app", "to-site"); len(l) != 1 || l[0].TransferID != keep.TransferID {
		t.Fatalf("receive list %+v", l)
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestHostFsBlockVectors(t *testing.T) {
	raw, err := os.ReadFile("../../../shared/testdata/hostfs-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Protected []string `json:"protected"`
		Cases     []struct {
			Path    string  `json:"path"`
			Blocked *string `json:"blocked"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	kind := func(r string) string {
		switch {
		case r == "":
			return ""
		case strings.HasPrefix(r, "OPM agent"):
			return "agent"
		case strings.Contains(r, "kernel"):
			return "pseudo"
		case r == "not an absolute path":
			return "invalid"
		}
		return "secret"
	}
	for _, c := range v.Cases {
		want := ""
		if c.Blocked != nil {
			want = *c.Blocked
		}
		if got := kind(hostFsBlockReason(c.Path, v.Protected)); got != want {
			t.Errorf("%q: got %q want %q", c.Path, got, want)
		}
	}
}

func TestHostFsListAndSendPolicy(t *testing.T) {
	fc := fakecloud.Start("DepSAF3")
	defer fc.Close()
	e := newUpgEnv(t, fc, "app")
	a, err := New(safOptions(e))
	if err != nil {
		t.Fatal(err)
	}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	defer func() { a.cancel(); a.db.Close() }()
	srv := newHTTPServer(a)
	defer srv.Close()
	c := login(t, a, srv.URL)

	root, _ := filepath.EvalSymlinks(t.TempDir())
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "Logs"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "b-dir"), 0o755))
	must(os.WriteFile(filepath.Join(root, "a.txt"), []byte("aaa"), 0o644))
	must(os.WriteFile(filepath.Join(root, ".hidden"), []byte("h"), 0o644))
	must(os.Symlink(filepath.Join(root, "Logs"), filepath.Join(root, "link-dir")))
	must(os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "dangling")))
	keyLink := filepath.Join(root, "key-link")
	must(os.Symlink(filepath.Join(a.dataDir, "site.key"), keyLink))
	locked := filepath.Join(root, "locked")
	must(os.MkdirAll(locked, 0o000))
	defer os.Chmod(locked, 0o755)

	var l model.HostFsListResponse
	if st := c.do("GET", "/_opm/api/sites/app/transfers/fs?path="+root, nil, &l); st != 200 {
		t.Fatalf("list %d", st)
	}
	names := []string{}
	byName := map[string]model.HostFsEntry{}
	for _, en := range l.Entries {
		names = append(names, en.Name)
		byName[en.Name] = en
	}
	// dirs first (incl. the symlink to a dir), case-insensitive; hidden left out and counted
	if got := strings.Join(names, ","); got != "b-dir,link-dir,locked,Logs,a.txt,dangling,key-link" {
		t.Fatalf("order %s", got)
	}
	if l.HiddenCount != 1 || l.Path != root || l.Parent == nil || *l.Parent != filepath.Dir(root) || l.AgentUser == "" || l.Truncated {
		t.Fatalf("meta %+v", l)
	}
	if en := byName["a.txt"]; en.Type != "file" || en.SizeBytes == nil || *en.SizeBytes != 3 || !en.Readable || en.Blocked != nil || en.Mtime == nil {
		t.Fatalf("a.txt %+v", en)
	}
	if en := byName["link-dir"]; en.Type != "symlink" || en.TargetType == nil || *en.TargetType != "dir" || en.LinkTarget == nil {
		t.Fatalf("link-dir %+v", en)
	}
	if en := byName["dangling"]; en.TargetType == nil || *en.TargetType != "missing" {
		t.Fatalf("dangling %+v", en)
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, "site.key")); err == nil {
		if en := byName["key-link"]; en.Blocked == nil || en.TargetType == nil || *en.TargetType != "file" {
			t.Fatalf("key-link (symlink into the agent home) must be blocked %+v", en)
		}
	}
	if os.Geteuid() != 0 {
		if en := byName["locked"]; en.Readable {
			t.Fatalf("locked readable %+v", en)
		}
		var eb model.APIErrorBody
		if st := c.do("GET", "/_opm/api/sites/app/transfers/fs?path="+locked, nil, &eb); st != 403 || eb.Error.Code != "forbidden" {
			t.Fatalf("locked list %d %+v", st, eb)
		}
	}
	if st := c.do("GET", "/_opm/api/sites/app/transfers/fs?path="+root+"&hidden=true", nil, &l); st != 200 || l.HiddenCount != 0 || len(l.Entries) != 8 {
		t.Fatalf("hidden %d %+v", st, l)
	}
	var eb model.APIErrorBody
	if st := c.do("GET", "/_opm/api/sites/app/transfers/fs?path="+filepath.Join(root, "a.txt"), nil, &eb); st != 422 || eb.Error.Details["type"] != "file" {
		t.Fatalf("file path %d %+v", st, eb)
	}
	if st := c.do("GET", "/_opm/api/sites/app/transfers/fs?path=relative", nil, &eb); st != 422 {
		t.Fatalf("relative %d", st)
	}
	if st := c.do("GET", "/_opm/api/sites/app/transfers/fs?path="+filepath.Join(root, "nope"), nil, &eb); st != 404 {
		t.Fatalf("missing %d", st)
	}
	// the agent home is listed as blocked
	if st := c.do("GET", "/_opm/api/sites/app/transfers/fs?path="+a.dataDir, nil, &l); st != 200 || l.Blocked == nil {
		t.Fatalf("data dir %d %+v", st, l)
	}
	// "/" lists without a session? no: session required
	resp, _ := http.Get(srv.URL + "/_opm/api/sites/app/transfers/fs")
	if resp.StatusCode != 401 {
		t.Fatalf("no session %d", resp.StatusCode)
	}
	resp.Body.Close()

	// send policy
	send := func(p string) (int, model.APIErrorBody) {
		var eb model.APIErrorBody
		return c.do("POST", "/_opm/api/sites/app/transfers/from-path", model.SendFromPathRequest{Path: p}, &eb), eb
	}
	if st, eb := send(filepath.Join(a.dataDir, "opm.db")); st != 403 || !strings.Contains(eb.Error.Message, "OPM agent data") {
		t.Fatalf("agent db %d %+v", st, eb)
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, "site.key")); err == nil {
		if st, eb := send(keyLink); st != 403 {
			t.Fatalf("symlink to site.key %d %+v", st, eb)
		}
	}
	if st, _ := send(e.cfgPath); st != 403 {
		t.Fatalf("agent.json %d", st)
	}
	if st, _ := send("/etc/shadow"); st != 403 {
		t.Fatalf("shadow %d", st)
	}
	if st, _ := send("/proc/self/status"); st != 403 {
		t.Fatalf("proc %d", st)
	}
	if st, eb := send(filepath.Join(root, "Logs")); st != 422 || !strings.Contains(eb.Error.Message, "folders cannot be sent") {
		t.Fatalf("folder %d %+v", st, eb)
	}
	if st, _ := send(filepath.Join(root, "nope")); st != 404 {
		t.Fatalf("missing %d", st)
	}
	// unauthenticated actor: session only → 401/403
	req, _ := http.NewRequest("POST", srv.URL+"/_opm/api/sites/app/transfers/from-path", strings.NewReader(`{"path":"`+filepath.Join(root, "a.txt")+`"}`))
	req.Header.Set("Cookie", "opm_session_app="+c.cookie)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode < 400 {
		t.Fatalf("send without actor %d", resp.StatusCode)
	}
	resp.Body.Close()
	var tr model.Transfer
	if st := c.do("POST", "/_opm/api/sites/app/transfers/from-path", model.SendFromPathRequest{Path: filepath.Join(root, "a.txt")}, &tr); st != 202 || tr.Delivery == nil || tr.Delivery.Phase != "queued" {
		t.Fatalf("send ok %d %+v", st, tr)
	}
}
