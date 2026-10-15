package tools

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// §21 #96 volume manager — file operations: create / edit (conflict on a stale mtime), upload, mkdir, move, delete
// (typed DELETE), the volume root is never a target, no step through a symlink, locks, file-kind volumes, activities.
func TestVolumeFileOps(t *testing.T) {
	h := newFakeHost(t)
	root := filepath.Join(h.dir, "volumes", "data")
	_ = os.MkdirAll(filepath.Join(root, "conf"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "conf", "app.ini"), []byte("a=1\n"), 0o640)
	_ = os.Symlink("/etc", filepath.Join(root, "etc-link"))
	single := filepath.Join(h.dir, "volumes", "single.conf")
	_ = os.WriteFile(single, []byte("x"), 0o600)
	h.vols = []Volume{{Slug: "data", Kind: "dir", Path: root}, {Slug: "single", Kind: "file", Path: single}}
	s := New(h)
	srv := serve(t, s)
	c := call{actor: "ops@example.com", session: "S1"}
	type resp struct {
		Entry struct {
			Path  string `json:"path"`
			Mtime string `json:"mtime"`
			Mode  string `json:"mode"`
		} `json:"entry"`
		Deleted int `json:"deleted"`
	}
	var r resp
	// read the mtime, edit with it, a stale one → conflict
	var f struct {
		Mtime string `json:"mtime"`
	}
	doJSON(t, srv, c, "GET", "/volumes/data/file?path=/conf/app.ini", nil, &f)
	if st := doJSON(t, srv, c, "POST", "/volumes/data/file", map[string]any{"path": "/conf/app.ini", "text": "a=2\n", "baseMtime": f.Mtime}, &r); st != 200 || r.Entry.Path != "/conf/app.ini" {
		t.Fatalf("edit %d %+v", st, r)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "conf", "app.ini")); string(b) != "a=2\n" {
		t.Fatal(string(b))
	}
	if st, _ := os.Stat(filepath.Join(root, "conf", "app.ini")); st.Mode().Perm() != 0o640 {
		t.Fatalf("mode not kept: %v", st.Mode())
	}
	if st := doJSON(t, srv, c, "POST", "/volumes/data/file", map[string]any{"path": "/conf/app.ini", "text": "a=3\n", "baseMtime": "2000-01-01T00:00:00.000Z"}, nil); st != 409 {
		t.Fatalf("stale edit → %d", st)
	}
	// create (baseMtime null): must not exist; parents are created
	if st := doJSON(t, srv, c, "POST", "/volumes/data/file", map[string]any{"path": "/new/dir/n.txt", "text": "n", "baseMtime": nil}, &r); st != 200 {
		t.Fatalf("create %d", st)
	}
	if st := doJSON(t, srv, c, "POST", "/volumes/data/file", map[string]any{"path": "/new/dir/n.txt", "text": "n", "baseMtime": nil}, nil); st != 409 {
		t.Fatalf("create existing → %d", st)
	}
	if st := doJSON(t, srv, c, "POST", "/volumes/data/file", map[string]any{"path": "/big.txt", "text": strings.Repeat("x", 1<<20+1), "baseMtime": nil}, nil); st != 413 {
		t.Fatalf("> 1 MiB → %d", st)
	}
	// never the root, never through a symlink, never out of the volume
	for _, p := range []string{"/", "/etc-link/passwd", "/../outside.txt"} {
		st := doJSON(t, srv, c, "POST", "/volumes/data/file", map[string]any{"path": p, "text": "x", "baseMtime": nil}, nil)
		if p == "/../outside.txt" { // cleaned to /outside.txt inside the volume
			if st != 200 {
				t.Fatalf("%s → %d", p, st)
			}
			if _, err := os.Stat(filepath.Join(h.dir, "volumes", "outside.txt")); err == nil {
				t.Fatal("escaped the volume")
			}
			continue
		}
		if st != 422 {
			t.Fatalf("%s → %d", p, st)
		}
	}
	// upload (raw body), conflict without overwrite
	up := func(path, body, q string) int {
		req, _ := http.NewRequest("POST", srv.URL+"/_opm/api/sites/app/tools/volumes/data/upload?path="+path+q, strings.NewReader(body))
		req.Header.Set("X-Test-Actor", c.actor)
		req.Header.Set("X-Test-Session", c.session)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return res.StatusCode
	}
	if st := up("/uploads/a/b.bin", "BINARY", ""); st != 200 {
		t.Fatalf("upload %d", st)
	}
	if st := up("/uploads/a/b.bin", "AGAIN", ""); st != 409 {
		t.Fatalf("upload existing → %d", st)
	}
	if st := up("/uploads/a/b.bin", "AGAIN", "&overwrite=1"); st != 200 {
		t.Fatalf("upload overwrite → %d", st)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "uploads", "a", "b.bin")); string(b) != "AGAIN" {
		t.Fatal(string(b))
	}
	// mkdir, move, delete
	if st := doJSON(t, srv, c, "POST", "/volumes/data/mkdir", map[string]any{"path": "/m/n"}, nil); st != 200 {
		t.Fatalf("mkdir %d", st)
	}
	if st := doJSON(t, srv, c, "POST", "/volumes/data/mkdir", map[string]any{"path": "/m/n"}, nil); st != 409 {
		t.Fatalf("mkdir existing %d", st)
	}
	if st := doJSON(t, srv, c, "POST", "/volumes/data/move", map[string]any{"from": "/m", "to": "/m/n/inside"}, nil); st != 422 {
		t.Fatalf("move into itself %d", st)
	}
	if st := doJSON(t, srv, c, "POST", "/volumes/data/move", map[string]any{"from": "/uploads/a/b.bin", "to": "/m/b.bin"}, &r); st != 200 || r.Entry.Path != "/m/b.bin" {
		t.Fatalf("move %d %+v", st, r)
	}
	if st := doJSON(t, srv, c, "POST", "/volumes/data/delete", map[string]any{"paths": []string{"/m"}, "confirm": "yes"}, nil); st != 422 {
		t.Fatalf("delete without DELETE %d", st)
	}
	if st := doJSON(t, srv, c, "POST", "/volumes/data/delete", map[string]any{"paths": []string{"/"}, "confirm": "DELETE"}, nil); st != 422 {
		t.Fatalf("delete the root %d", st)
	}
	if st := doJSON(t, srv, c, "POST", "/volumes/data/delete", map[string]any{"paths": []string{"/m", "/etc-link"}, "confirm": "DELETE"}, &r); st != 200 || r.Deleted != 4 {
		t.Fatalf("delete %d %+v", st, r)
	}
	if _, err := os.Lstat("/etc"); err != nil {
		t.Fatal("a symlink delete must remove the link only")
	}
	// file-kind volume: only its content
	var fsingle struct {
		Mtime string `json:"mtime"`
	}
	doJSON(t, srv, c, "GET", "/volumes/single/file?path=/", nil, &fsingle)
	if st := doJSON(t, srv, c, "POST", "/volumes/single/file", map[string]any{"path": "/", "text": "y", "baseMtime": fsingle.Mtime}, nil); st != 200 {
		t.Fatalf("file volume edit %d", st)
	}
	if st := doJSON(t, srv, c, "POST", "/volumes/single/mkdir", map[string]any{"path": "/x"}, nil); st != 422 {
		t.Fatalf("mkdir in a file volume %d", st)
	}
	// §21 #110 a running update never blocks volume files; a busy volume does
	h.locked = true
	if st := doJSON(t, srv, c, "POST", "/volumes/data/mkdir", map[string]any{"path": "/z"}, nil); st != 200 {
		t.Fatalf("update running → %d (want 200)", st)
	}
	h.locked = false
	h.volBusy = "empty running"
	if st := doJSON(t, srv, c, "POST", "/volumes/data/mkdir", map[string]any{"path": "/z"}, nil); st != 409 {
		t.Fatalf("busy → %d", st)
	}
	h.volBusy = ""
	// no actor → refused by the agent wrapper (tested in the agent); activities written
	want := []string{"volume.file-written", "volume.file-uploaded", "volume.file-folder-created", "volume.file-moved", "volume.file-deleted"}
	for _, w := range want {
		if len(h.activities(w)) == 0 {
			t.Fatalf("no %s activity", w)
		}
	}
}
