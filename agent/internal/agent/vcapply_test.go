package agent

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"opm/agent/internal/model"
	"opm/agent/internal/store"
	"opm/agent/internal/volcontent"
)

// A cloud version that changes other files than the site did: the plan has no conflict (cloud-only + live-only
// items, not a fast-forward), and an apply WITHOUT an intent (no 60 s wait, §21 #104 (e)) merges it per file: the
// cloud change is written, the live change is kept.
func TestVolumeSyncApplyCleanMergeNoIntent(t *testing.T) {
	a := newVSAgent(t)
	setHead(t, a, persistRev(t, "app", false, 50<<20))
	root := filepath.Join(a.dataDir, "volumes", "flows")
	_ = os.MkdirAll(root, 0o755)
	write := func(p, s string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("flows.json", "[]")
	write("state.json", `{"n":1}`)
	p := a.persistVolumes()[0]
	rec, created, err := a.snapshotVolume(&p, snapOpts{trigger: "manual-agent"})
	if err != nil || !created {
		t.Fatalf("base snapshot: %v %v", created, err)
	}
	baseHash := rec.Local.TreeHash

	// the site changes state.json; the cloud edits flows.json and adds lib/new.js on top of that snapshot
	write("state.json", `{"n":2}`)
	cloud := model.VolumeTree{Dirs: []string{}}
	for _, e := range rec.Req.Tree.Entries {
		if e.Path != "flows.json" {
			cloud.Entries = append(cloud.Entries, e)
		}
	}
	for path, body := range map[string]string{"flows.json": `[{"id":"cloud"}]`, "lib/new.js": "// cloud\n"} {
		hx, err := a.storeBlobBytes([]byte(body), "file", "test")
		if err != nil {
			t.Fatal(err)
		}
		cloud.Entries = append(cloud.Entries, model.VolumeTreeEntry{Path: path, Sha256: model.Sha256("sha256:" + hx), Size: int64(len(body)), Mode: model.DefaultFileMode, Render: "none"})
	}
	cloud = volcontent.Normalize(cloud)
	cloudHash := volcontent.Hash(cloud)
	if hx, err := a.storeBlobBytes([]byte(volcontent.HashInput(cloud)), "file", "test"); err != nil || "sha256:"+hx != string(cloudHash) {
		t.Fatalf("tree blob %s vs %s: %v", hx, cloudHash, err)
	}
	vid, bvid := model.ULID("01M4V0XTTDQ74BEVRWZ1TMNTM8"), model.ULID("01M4QV0K31974Z6E6A1EJ106FG")
	view := model.CloudView{Cursor: "c1", FetchedAt: nowTs(), Peers: []model.PeerInfo{}, VolumeContent: []model.VolumeContentCloudItem{{SiteSlug: "app", VolumeSlug: "flows",
		Push: &model.VolumeContentPush{PushID: "01M4V0YAFGWQYN9MQJ9X7054C5", SiteSlug: "app", VolumeSlug: "flows", VersionID: vid, TreeHash: cloudHash, BaseVersionID: &bvid, BaseTreeHash: &baseHash, Status: "pending", CreatedAt: nowTs(), CreatedBy: "ops@acme.example"}}}}
	if _, err := a.db.Exec(`INSERT INTO cloud_view(id, fetchedAt, source, cursor, view) VALUES(1,?,?,?,?) ON CONFLICT(id) DO UPDATE SET view = excluded.view`, view.FetchedAt, "cloud", view.Cursor, store.JSON(view)); err != nil {
		t.Fatal(err)
	}

	h := a.handlerFor("agent")
	ck, auth := agentSession(t, a, h, "ops@acme.example")
	call := func(method, path, body string, out any) (int, string) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = "10.0.0.9:1234"
		r.Header.Set("Cookie", "opm_session_app="+ck)
		r.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if out != nil {
			_ = json.Unmarshal(w.Body.Bytes(), out)
		}
		return w.Code, w.Body.String()
	}
	vs := "/_opm/api/sites/app/volume-sync/flows"
	// live ≠ base, but no file changed on both sides: "cloud ahead", never "conflict"
	var st model.VolumeSyncStatus
	if c, b := call("GET", vs, "", &st); c != 200 || st.State != "cloud-ahead" {
		t.Fatalf("state %d %s", c, b)
	}
	var plan model.VolumePushPlan
	if c, b := call("GET", vs+"/plan", "", &plan); c != 200 {
		t.Fatalf("plan %d %s", c, b)
	}
	if !plan.BaseKnown || plan.FastForward || plan.UpToDate || plan.Counts["conflict"] != 0 || plan.Counts["cloud"] != 2 || plan.Counts["live"] != 1 {
		t.Fatalf("plan: baseKnown %v ff %v upToDate %v counts %v", plan.BaseKnown, plan.FastForward, plan.UpToDate, plan.Counts)
	}
	if len(activityData(t, a, "volume-content.conflict")) != 0 {
		t.Fatal("no conflict activity without a conflict")
	}
	req, _ := json.Marshal(model.VolumeSyncApplyRequest{PushID: plan.PushID, Resolution: "per-file", LiveTreeHash: plan.LiveTreeHash})
	var ap model.VolumeSyncApplyResponse
	if c, b := call("POST", vs+"/apply", string(req), &ap); c != 200 {
		t.Fatalf("apply without an intent %d %s", c, b)
	}
	if ap.Result.Error != nil || ap.Result.Written != 2 || ap.Result.KeptLive != 1 || ap.Push.Status != "applied" {
		t.Fatalf("result %+v push %s", ap.Result, ap.Push.Status)
	}
	read := func(p string) string { b, _ := os.ReadFile(filepath.Join(root, p)); return string(b) }
	if read("flows.json") != `[{"id":"cloud"}]` || read("lib/new.js") != "// cloud\n" || read("state.json") != `{"n":2}` {
		t.Fatalf("on disk: flows %q new %q state %q", read("flows.json"), read("lib/new.js"), read("state.json"))
	}
	// an intent id that does not exist is still refused (older GUIs keep the checked path)
	if c, _ := call("POST", vs+"/apply", `{"intentId":"01M4V0XTTDQ74BEVRWZ1TMNTM9","pushId":"x","resolution":"per-file","liveTreeHash":"x"}`, nil); c == 200 {
		t.Fatal("a bad intent must not apply")
	}
}
