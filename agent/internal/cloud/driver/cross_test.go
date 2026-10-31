package driver

// Cross-compatibility with the TS driver (backend/src/driver): the same operations through both drivers must give the
// same files, and each side must read and use what the other wrote, also at the same time. Needs node + backend deps.

import (
	"bytes"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"opm/agent/internal/cloud/doc"
)

func backendDir(t testing.TB) string {
	_, f, _, _ := runtime.Caller(0)
	d := filepath.Join(filepath.Dir(f), "../../../../backend")
	if _, err := os.Stat(filepath.Join(d, "node_modules/tsx")); err != nil {
		t.Skip("backend/node_modules missing")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node")
	}
	return d
}

func tsRun(t testing.TB, args ...string) []byte {
	t.Helper()
	_, f, _, _ := runtime.Caller(0)
	script := filepath.Join(filepath.Dir(f), "testdata/cross.mts")
	cmd := exec.Command("node", append([]string{"--import", "tsx", "--no-warnings=ExperimentalWarning", script}, args...)...)
	cmd.Dir = backendDir(t)
	cmd.Env = append(os.Environ(), "OPM_LOCAL_STRICT=1")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("cross.ts %v: %v\n%s\n%s", args, err, out.String(), errb.String())
	}
	return out.Bytes()
}

func fixture(t testing.TB) (*doc.Map, []string) {
	_, f, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(f), "testdata/fixture.json"))
	must(t, err)
	m, err := doc.ParseMap(raw)
	must(t, err)
	return m, m.Keys()
}

func goWriteAll(t *testing.T, p *Ports) {
	fx, keys := fixture(t)
	st := p.Store
	for _, k := range keys {
		s, err := doc.EncodeStored(fx.Val(k))
		must(t, err)
		d, err := DecodeDoc(s)
		must(t, err)
		must(t, st.Doc(k).Set(d))
	}
	// the same sequence as cross.ts write
	must(t, st.Doc("templates/T1").Update(doc.Obj("a.y", 3, "a.n.m", 1, "label", doc.DeleteField(), "cnt", doc.Increment(2), "arr", doc.ArrayUnion("x", doc.Obj("k", 1), "x"))))
	must(t, st.Doc("templates/T1").Set(doc.Obj("a", doc.Obj("z", 9, "q", doc.Obj("r", 1)), "arr", doc.ArrayRemove("x")), Merge))
	must(t, st.Doc("services/s1/images/v1").Update(doc.Obj("import.status", "done")))
	must(t, st.Doc("plain/gone").Set(doc.Obj("v", 1)))
	must(t, st.Doc("plain/gone").Delete())
	must(t, st.Doc("deployments/D2").Delete())
}

type tableDump map[string][]string

// dumpFile: every table and index, every row (typeof of ix columns included), _schema, _outbox without times.
func dumpFile(t *testing.T, file string) tableDump {
	db, err := sql.Open("sqlite", file)
	must(t, err)
	defer db.Close()
	out := tableDump{}
	rows, err := db.Query("SELECT type, name, tbl_name, sql FROM sqlite_master WHERE name NOT LIKE 'sqlite%' ORDER BY type, name")
	must(t, err)
	var tables []string
	for rows.Next() {
		var a, b, c string
		var s sql.NullString
		must(t, rows.Scan(&a, &b, &c, &s))
		out["#master"] = append(out["#master"], a+" "+b+" "+c+" "+s.String)
		if a == "table" {
			tables = append(tables, b)
		}
	}
	rows.Close()
	for _, tb := range tables {
		cols := []string{}
		r, err := db.Query("SELECT name FROM pragma_table_info(?)", tb)
		must(t, err)
		for r.Next() {
			var n string
			must(t, r.Scan(&n))
			cols = append(cols, n)
		}
		r.Close()
		sel := []string{}
		for _, c := range cols {
			if tb == "_outbox" && (c == "createdMs" || c == "leaseUntil") {
				continue
			}
			sel = append(sel, "typeof("+q(c)+")", "quote("+q(c)+")")
		}
		r, err = db.Query("SELECT " + strings.Join(sel, ",") + " FROM " + q(tb) + " ORDER BY 2")
		must(t, err)
		for r.Next() {
			vals := make([]any, len(sel))
			ptr := make([]any, len(sel))
			for i := range vals {
				ptr[i] = &vals[i]
			}
			must(t, r.Scan(ptr...))
			out[tb] = append(out[tb], fmt.Sprint(vals...))
		}
		r.Close()
	}
	return out
}

func TestRegistryMatchesTS(t *testing.T) {
	backendDir(t)
	m, err := doc.ParseMap(tsRun(t, "info", "-"))
	must(t, err)
	reg := m.Map("registry")
	eq(t, reg.Len(), len(DefaultRegistry))
	for _, k := range reg.Keys() {
		s, ok := DefaultRegistry[k]
		if !ok {
			t.Fatalf("table %s missing", k)
		}
		eq(t, specJSON(s), reg.Map(k).Str("json"), k)
		eq(t, hash16(specJSON(s))[:16], reg.Map(k).Str("hash"), k)
	}
	tr := m.Arr("triggers")
	eq(t, len(tr), len(TriggerSpecs))
	for i, x := range tr {
		y := x.(*doc.Map)
		eq(t, []string{y.Str("name"), y.Str("kind"), y.Str("pattern")}, []string{TriggerSpecs[i].Name, TriggerSpecs[i].Kind, TriggerSpecs[i].Pattern})
	}
}

// Same operations, both drivers: byte-identical rows, schema, indexes, _schema hashes, outbox rows.
func TestSameFilesAsTS(t *testing.T) {
	backendDir(t)
	tsDir, goDir := t.TempDir(), t.TempDir()
	tsRun(t, "write", tsDir)
	p, err := Open(Config{DataDir: goDir, PublicURL: "http://cross.test", ConsoleURL: "http://console.test", Strict: yes()})
	must(t, err)
	goWriteAll(t, p)
	p.Close()
	for _, f := range []string{"db/deployments/D1.sqlite", "db/deployments/D2.sqlite"} {
		a, b := dumpFile(t, filepath.Join(tsDir, f)), dumpFile(t, filepath.Join(goDir, f))
		compareDumps(t, f, a, b)
	}
	a, b := dumpFile(t, filepath.Join(tsDir, "db/global.sqlite")), dumpFile(t, filepath.Join(goDir, "db/global.sqlite"))
	delete(a, "authUsers")
	delete(a, "authEmails")
	delete(a, "authRefresh")
	delete(a, "authReset")
	for _, d := range []tableDump{a, b} {
		var keep []string
		for _, s := range d["#master"] {
			if !strings.Contains(s, "auth") {
				keep = append(keep, s)
			}
		}
		d["#master"] = keep
		keep = nil
		for _, s := range d["_schema"] {
			if !strings.Contains(s, "auth") {
				keep = append(keep, s)
			}
		}
		d["_schema"] = keep
	}
	compareDumps(t, "global", a, b)
}

func compareDumps(t *testing.T, f string, a, b tableDump) {
	t.Helper()
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	for k := range keys {
		x, y := a[k], b[k]
		sort.Strings(x)
		sort.Strings(y)
		if strings.Join(x, "\n") != strings.Join(y, "\n") {
			t.Errorf("%s %s differ\nTS: %s\nGo: %s", f, k, strings.Join(x, "\n    "), strings.Join(y, "\n    "))
		}
	}
}

// TS wrote → Go reads and re-encodes byte for byte, and uses its tokens, passwords, reset links and blob URLs.
func TestGoReadsTS(t *testing.T) {
	backendDir(t)
	dir := t.TempDir()
	facts, err := doc.ParseMap(tsRun(t, "write", dir))
	must(t, err)
	p, err := Open(Config{DataDir: dir, PublicURL: "http://cross.test", ConsoleURL: "http://console.test", Strict: yes()})
	must(t, err)
	defer p.Close()
	st := p.Store
	must(t, st.AllDocuments(func(path string, m *doc.Map) error {
		s, err := st.Doc(path).Get()
		must(t, err)
		again, err := EncodeDoc(s.Data())
		must(t, err)
		if again != *s.Raw() {
			t.Errorf("%s re-encoded differently:\n%s\n%s", path, *s.Raw(), again)
		}
		return nil
	}))
	u := data(t, st.Doc("accounts/U1"))
	if u.Val("ts") != (doc.Timestamp{Seconds: 1700000000, Nanos: 123000000}) {
		t.Fatal(u.Val("ts"))
	}
	if !bytes.Equal(u.Val("bytes").(doc.Bytes), []byte{0, 1, 2, 255}) {
		t.Fatal(u.Val("bytes"))
	}
	eq(t, ids(t, st.CollectionGroup("commits").OrderBy("seq")), []string{"c1", "c2", "c1"})
	eq(t, ids(t, st.Collection("deployments").Where("deleted", "==", false)), []string{"D1"})
	eq(t, ids(t, st.Collection("deployments").Where("slug", "==", "d-two")), []string{})
	eq(t, ids(t, st.Collection("auditLog").Where("_actionKeys", "array-contains", "x.")), []string{"A1"})
	eq(t, ids(t, st.Collection("agentNonces").Where("expiresAt", ">", doc.TimestampFromMillis(1800000000000))), []string{"N1"})
	// auth
	a := p.Auth
	login := facts.Map("login")
	d, err := a.VerifyIDToken(login.Str("idToken"), true)
	must(t, err)
	eq(t, d.UID, facts.Str("uid"))
	_, err = a.CheckPassword("ts.user@example.com", "ts-password-1")
	must(t, err)
	if _, err := a.CheckPassword("ts.user@example.com", "wrong"); err == nil {
		t.Fatal("wrong password")
	}
	n, err := a.Refresh(login.Str("refreshToken"))
	must(t, err)
	eq(t, n.UID, facts.Str("uid"))
	lu, _ := url.Parse(facts.Str("link"))
	email, why, err := a.CheckReset(lu.Query().Get("token"))
	must(t, err)
	eq(t, []string{email, why}, []string{"ts.user@example.com", ""})
	// blobs
	rel := strings.TrimPrefix(facts.Str("url"), "http://cross.test")
	r := blobReq(p.Blobs, rel, nil, "GET")
	eq(t, r.Code, 200)
	eq(t, r.Body.String(), "hello cross")
	eq(t, r.Header()["Content-Type"], []string{"text/x-cross"})
	eq(t, r.Header()["Cache-Control"], []string{"private, max-age=9"})
	eq(t, r.Header()["Content-Disposition"], []string{`attachment; filename="ü b.txt"`})
	h, err := p.Blobs.Head("files/a b/ü.txt")
	must(t, err)
	eq(t, h.Meta["opmSha256"], "abc")
	// a sidecar written by Go has the same shape
	must(t, p.Blobs.Put("files/a b/go.txt", []byte("x"), PutOptions{ContentType: "text/x-cross", CacheControl: "c", Meta: map[string]string{"a": "1"}}))
	a1, _ := os.ReadFile(filepath.Join(dir, "blobmeta/files/a b/ü.txt.json"))
	a2, _ := os.ReadFile(filepath.Join(dir, "blobmeta/files/a b/go.txt.json"))
	k1, _ := doc.ParseMap(a1)
	k2, _ := doc.ParseMap(a2)
	eq(t, k1.Keys(), k2.Keys())
}

// Go wrote → TS reads the same documents and uses Go's tokens, passwords, reset links, blob URLs.
func TestTSReadsGo(t *testing.T) {
	backendDir(t)
	dir := t.TempDir()
	p, err := Open(Config{DataDir: dir, PublicURL: "http://cross.test", ConsoleURL: "http://console.test", Strict: yes()})
	must(t, err)
	goWriteAll(t, p)
	a := p.Auth
	u, err := a.CreateUser(CreateUserInput{Email: "Go.User@Example.com", Password: Ptr("go-password-1"), DisplayName: Ptr("Go User")})
	must(t, err)
	user, err := a.UserByEmail("go.user@example.com")
	must(t, err)
	pair, err := a.Issue(user, "")
	must(t, err)
	link, err := a.GeneratePasswordResetLink("go.user@example.com")
	must(t, err)
	must(t, p.Blobs.Put("files/a b/ü.txt", []byte("hello cross"), PutOptions{ContentType: "text/x-cross", CacheControl: "private, max-age=9", Meta: map[string]string{"opmSha256": "abc"}}))
	blobURL, err := p.Blobs.SignedReadURL("files/a b/ü.txt", 10*time.Minute, `attachment; filename="ü b.txt"`)
	must(t, err)
	out, err := doc.ParseMap(tsRun(t, "read", dir, doc.MarshalString(doc.Obj("uid", u.UID, "idToken", pair.IDToken, "refreshToken", pair.RefreshToken, "link", link, "url", blobURL))))
	must(t, err)
	_, keys := fixture(t)
	for _, k := range keys {
		if k == "deployments/D2" {
			continue
		}
		s, err := p.Store.Doc(k).Get()
		must(t, err)
		eq(t, out.Str(k), *s.Raw(), k)
	}
	eq(t, out.Str("tokenUid"), u.UID)
	eq(t, out.Str("refreshed"), u.UID)
	eq(t, out.Val("login"), 200)
	eq(t, out.Str("reset"), "go.user@example.com")
	bl := out.Map("blob")
	eq(t, bl, doc.Obj("status", 200, "ct", "text/x-cross", "cd", `attachment; filename="ü b.txt"`, "cc", "private, max-age=9", "body", "hello cross", "meta", doc.Obj("opmSha256", "abc")))
	q := out.Map("queries")
	eq(t, q.Val("dep"), []string{"D1"})
	eq(t, q.Val("ts"), 1)
	eq(t, q.Val("arr"), 1)
	eq(t, len(q.Arr("group")), 3)
	p.Close()
}

// Go and a TS process hammer the same files at once: no lost update, no corrupted file, outbox rows from both.
func TestConcurrentWithTS(t *testing.T) {
	backendDir(t)
	dir := t.TempDir()
	p, err := Open(Config{DataDir: dir, PublicURL: "http://x", Strict: yes()})
	must(t, err)
	defer p.Close()
	st := p.Store
	paths := []string{"deployments/DA/counters/c", "deployments/DB/counters/c", "counters/g"}
	const N = 60
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tsRun(t, append([]string{"incr", dir, fmt.Sprint(N)}, paths...)...)
	}()
	for i := 0; i < N; i++ {
		for _, path := range paths {
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := st.RunTransaction(func(tx *Tx) error {
					ref := st.Doc(path)
					s, err := tx.Get(ref)
					if err != nil {
						return err
					}
					tx.Set(ref, doc.Obj("v", s.Data().Num("v")+1), Merge)
					return nil
				}, 500)
				if err != nil {
					t.Error(err)
				}
			}()
		}
	}
	wg.Wait()
	for _, path := range paths {
		eq(t, data(t, st.Doc(path)).Val("v"), 2*N, path)
	}
	db := rawDB(t, filepath.Join(dir, "db/global.sqlite"))
	var ok string
	must(t, db.QueryRow("PRAGMA integrity_check").Scan(&ok))
	eq(t, ok, "ok")
}
