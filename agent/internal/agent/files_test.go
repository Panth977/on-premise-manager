package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/sign"
)

// §21 #123 shared files between two sites without the cloud: app publishes from the browser and from a server path;
// db learns the listing over the mesh, gets a copy from app only when asked (nothing is downloaded by itself), saves
// it on its server (409 when the path exists), downloads it to the browser; dropping the only copy is refused;
// unpublishing on db reaches app over the mesh and deletes app's copy.
func TestSharedFilesBetweenSites(t *testing.T) {
	app := newTestAgent(t)
	db := newTestAgentSite(t, "db")
	appSrv := httptest.NewServer(app.handlerFor("agent"))
	dbSrv := httptest.NewServer(db.handlerFor("agent"))
	defer appSrv.Close()
	defer dbSrv.Close()
	port := func(s *httptest.Server) int {
		var p int
		_, ps, _ := strings.Cut(strings.TrimPrefix(s.URL, "http://"), ":")
		for _, c := range ps {
			p = p*10 + int(c-'0')
		}
		return p
	}
	now := nowTs()
	peers := []model.PeerInfo{{SiteSlug: "db", PublicKey: sign.PublicKeyB64(db.pub), AdvertiseHost: "127.0.0.1", AgentPort: port(dbSrv), EnrolledAt: now},
		{SiteSlug: "app", PublicKey: sign.PublicKeyB64(app.pub), AdvertiseHost: "127.0.0.1", AgentPort: port(appSrv), EnrolledAt: now}}
	app.storePeers(peers)
	db.storePeers(peers)
	for _, x := range []struct {
		a    *Agent
		peer string
	}{{app, "db"}, {db, "app"}} {
		x.a.mu.Lock()
		x.a.peerState[x.peer] = &peerRuntime{lastAnswer: time.Now().Add(time.Hour)}
		x.a.mu.Unlock()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app.ctx, db.ctx = ctx, ctx

	key := "ABCDEFGHIJKLMNOPQRSTUVWX"
	login := func(a *Agent, srv *httptest.Server, slug string) *cl {
		_ = a.db.SetKV("keyHash", model.AccessKeyRef{Hash: sign.HashAccessKey(key), Version: 1})
		resp, err := http.Post(srv.URL+"/_opm/api/login", "application/json", strings.NewReader(`{"accessKey":"`+key+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		c := &cl{srv: srv, ck: resp.Cookies()[0].Value, slug: slug}
		resp.Body.Close()
		var ar model.ActorResponse
		c.do(t, "POST", "/_opm/api/actor", strings.NewReader(`{"email":"ops@acme.example"}`), &ar)
		c.auth = "Actor " + ar.ActorToken
		return c
	}
	ca, cd := login(app, appSrv, "app"), login(db, dbSrv, "db")

	// publish on app: from the browser and from a server path
	var pub model.SiteSharedFile
	body := bytes.Repeat([]byte("opm shared file "), 4096)
	if code := ca.do(t, "POST", "/_opm/api/sites/app/files/upload?fileName=report.bin&note=monthly", bytes.NewReader(body), &pub); code != 201 || !pub.Here || pub.Origin.SiteSlug != "app" || pub.State != "ready" {
		t.Fatalf("publish from browser: %d %+v", code, pub)
	}
	src := filepath.Join(t.TempDir(), "notes.txt")
	_ = os.WriteFile(src, []byte("server notes"), 0o644)
	var pub2 model.SiteSharedFile
	if code := ca.do(t, "POST", "/_opm/api/sites/app/files/from-path", strings.NewReader(`{"path":"`+src+`"}`), &pub2); code != 201 || pub2.Origin.Via != "path" {
		t.Fatalf("publish from path: %d %+v", code, pub2)
	}

	// db learns the listing over the mesh; nothing is copied by itself
	db.peerFilesOnce(ctx)
	list := func(c *cl, unpublished bool) map[string]model.SiteSharedFile {
		t.Helper()
		var l model.ListResponse[model.SiteSharedFile]
		q := ""
		if unpublished {
			q = "?unpublished=true"
		}
		if code := c.do(t, "GET", "/_opm/api/sites/"+c.slug+"/files"+q, nil, &l); code != 200 {
			t.Fatalf("list %d", code)
		}
		m := map[string]model.SiteSharedFile{}
		for _, f := range l.Items {
			m[f.FileID] = f
		}
		return m
	}
	f := list(cd, false)[pub.FileID]
	if f.FileID == "" || f.Here || f.ListedBy != "peer" || len(f.Sources.Peers) != 1 || f.Sources.Peers[0] != "app" {
		t.Fatalf("db sees %+v", f)
	}
	if _, err := os.Stat(filepath.Join(db.filesDir(), pub.FileID)); err == nil {
		t.Fatal("nothing may be downloaded by itself")
	}
	// download before a copy → 409; get a copy (from app over the mesh)
	if code := cd.do(t, "GET", "/_opm/api/sites/db/files/"+pub.FileID+"/download", nil, nil); code != 409 {
		t.Fatalf("download without a copy: %d", code)
	}
	if code := cd.do(t, "POST", "/_opm/api/sites/db/files/"+pub.FileID+"/fetch", nil, nil); code != 202 {
		t.Fatalf("fetch %d", code)
	}
	waitFor(t, "copy on db", 15*time.Second, func() bool { return list(cd, false)[pub.FileID].Here })
	var got []byte
	if code := cd.do(t, "GET", "/_opm/api/sites/db/files/"+pub.FileID+"/download", nil, &got); code != 200 || !bytes.Equal(got, body) {
		t.Fatalf("download %d (%d bytes)", code, len(got))
	}
	// save on db's server; the same path again → 409
	dst := filepath.Join(t.TempDir(), "report.bin")
	if code := cd.do(t, "POST", "/_opm/api/sites/db/files/"+pub.FileID+"/save", strings.NewReader(`{"path":"`+dst+`"}`), nil); code != 200 {
		t.Fatalf("save %d", code)
	}
	if b, _ := os.ReadFile(dst); !bytes.Equal(b, body) {
		t.Fatal("saved content")
	}
	if code := cd.do(t, "POST", "/_opm/api/sites/db/files/"+pub.FileID+"/save", strings.NewReader(`{"path":"`+dst+`"}`), nil); code != 409 {
		t.Fatalf("save over an existing file: %d", code)
	}
	// app's second file: app holds the only copy → dropping it is refused
	if code := ca.do(t, "DELETE", "/_opm/api/sites/app/files/"+pub2.FileID+"/copy", nil, nil); code != 409 {
		t.Fatalf("drop the only copy: %d", code)
	}
	// db's copy of the first can be dropped (app still holds one)
	app.peerFilesOnce(ctx) // app learns db holds a copy
	if code := cd.do(t, "DELETE", "/_opm/api/sites/db/files/"+pub.FileID+"/copy", nil, nil); code != 200 || list(cd, false)[pub.FileID].Here {
		t.Fatalf("drop db's copy: %d", code)
	}
	// unpublish on db (no cloud: tombstone) → app learns it over the mesh and deletes its copy
	if code := cd.do(t, "DELETE", "/_opm/api/sites/db/files/"+pub.FileID, nil, nil); code != 200 {
		t.Fatalf("unpublish %d", code)
	}
	if _, ok := list(cd, false)[pub.FileID]; ok {
		t.Fatal("unpublished files are hidden by default")
	}
	if list(cd, true)[pub.FileID].State != "unpublished" {
		t.Fatal("the tombstone is listed with unpublished=true")
	}
	app.peerFilesOnce(ctx)
	if g := list(ca, true)[pub.FileID]; g.State != "unpublished" || g.Here {
		t.Fatalf("app after the unpublish: %+v", g)
	}
	if _, err := os.Stat(filepath.Join(app.filesDir(), pub.FileID)); err == nil {
		t.Fatal("app's copy must be deleted")
	}
	if len(activityData(t, app, "file.published")) != 2 || len(activityData(t, db, "file.fetched")) != 1 || len(activityData(t, db, "file.unpublished")) != 1 {
		t.Fatal("activities")
	}
}

type cl struct {
	srv  *httptest.Server
	ck   string
	auth string
	slug string
}

func (c *cl) do(t *testing.T, method, path string, body io.Reader, out any) int {
	t.Helper()
	r, _ := http.NewRequest(method, c.srv.URL+path, body)
	r.Header.Set("Cookie", "opm_session_"+c.slug+"="+c.ck)
	if c.auth != "" {
		r.Header.Set("Authorization", c.auth)
	}
	if strings.HasSuffix(path, "/save") || strings.HasSuffix(path, "/from-path") || strings.HasSuffix(path, "/actor") {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	switch o := out.(type) {
	case nil:
	case *[]byte:
		*o = b
	default:
		_ = json.Unmarshal(b, o)
	}
	return resp.StatusCode
}

// §21 #123 a site's copies of legacy transfers become shared files once (fileId = transferId; a never-uploaded
// outgoing one becomes a file published from this site, registered through the outbox).
func TestTransfersBecomeSharedFiles(t *testing.T) {
	a := newTestAgent(t)
	mk := func(id, dir string, src *string) string {
		p := filepath.Join(a.dataDir, "transfers", id)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("x-"+id), 0o600)
		tr := model.Transfer{TransferID: id, SiteSlug: a.site, Direction: dir, FileName: id + ".txt", SizeBytes: int64(len("x-" + id)), Sha256: "sha256:00", CreatedAt: nowTs(), CreatedBy: "ops@acme.example", Status: "ready", SourcePath: src}
		a.putTransfer(&tr, p)
		return p
	}
	sp := "/srv/data/x.csv"
	mk("TransferInCloud00001", "to-site", nil)
	mk("local-01J9XYZ", "to-cloud", &sp)
	expired := model.Transfer{TransferID: "Gone0000000000000001", SiteSlug: a.site, Direction: "to-site", FileName: "g", Status: "expired", CreatedAt: nowTs()}
	a.putTransfer(&expired, "")
	a.migrateTransfersToFiles()
	a.migrateTransfersToFiles() // idempotent
	fr, ok := a.fileRow("TransferInCloud00001")
	if !ok || !fr.here() || fr.f.Origin.Kind != "cloud" || !fr.f.Cloud.Stored {
		t.Fatalf("to-site transfer: %+v %v", fr, ok)
	}
	var local *fileRow
	for _, x := range a.fileRows() {
		if x.f.Origin.Kind == "site" {
			local = x
		}
	}
	if local == nil || local.f.Origin.Via != "path" || local.f.Cloud.Stored || !local.here() || strings.HasPrefix(local.f.FileID, "local-") {
		t.Fatalf("outgoing transfer: %+v", local)
	}
	var n int
	_ = a.db.QueryRow(`SELECT COUNT(*) FROM transfers`).Scan(&n)
	if n != 0 || len(a.fileRows()) != 2 {
		t.Fatalf("transfers left %d, files %d", n, len(a.fileRows()))
	}
	var q int
	_ = a.db.QueryRow(`SELECT COUNT(*) FROM outbox WHERE path = '/v1/agent/files'`).Scan(&q)
	if q != 1 {
		t.Fatalf("register queued %d", q)
	}
}
