package driver

// Port of backend/test/41-local-adapter.test.ts (minus what cross_test.go covers against the real TS driver).

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"opm/agent/internal/cloud/doc"
)

func rawDB(t *testing.T, file string) *sql.DB {
	db, err := sql.Open("sqlite", file)
	must(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

func TestLayout(t *testing.T) {
	d := t.TempDir()
	p := open(t, d)
	st := p.Store
	must(t, st.Doc("templates/T1").Set(doc.Obj("slug", "t")))
	must(t, st.Doc("deployments/D1").Set(doc.Obj("deleted", false, "updatedAt", "2026-01-01")))
	must(t, st.Doc("deployments/D1/commits/c1").Set(doc.Obj("seq", 1)))
	must(t, st.Doc("deployments/D1/agentBranches/s1/commits/c2").Set(doc.Obj("seq", 2)))
	must(t, st.Doc("githubMirror/config/queue/q1").Set(doc.Obj("state", "pending")))
	p.Close()
	g := rawDB(t, d+"/db/global.sqlite")
	var id, parent, ix string
	must(t, g.QueryRow("SELECT id, parent, ix_slug FROM templates").Scan(&id, &parent, &ix))
	eq(t, []string{id, parent, ix}, []string{"templates/T1", "parent"[:0] + "templates", "t"})
	dep := rawDB(t, d+"/db/deployments/D1.sqlite")
	var mode string
	must(t, dep.QueryRow("PRAGMA journal_mode").Scan(&mode))
	eq(t, mode, "wal")
	var del float64
	var upd string
	must(t, dep.QueryRow("SELECT id, parent, ix_deleted, ix_updatedAt FROM deployment").Scan(&id, &parent, &del, &upd))
	eq(t, []any{id, parent, del, upd}, []any{"deployments/D1", "deployments", 0, "2026-01-01"})
	if open(t, d).Store.Doc("deployments/bad-id/x/y").Err() == nil {
		t.Fatal("bad id")
	}
}

func TestParallelFilesNoLostUpdates(t *testing.T) {
	p := open(t, t.TempDir())
	st := p.Store
	paths := []string{"deployments/DA/counters/c", "deployments/DB/counters/c", "counters/g"}
	inc := func(path string) {
		err := st.RunTransaction(func(tx *Tx) error {
			ref := st.Doc(path)
			s, err := tx.Get(ref)
			if err != nil {
				return err
			}
			tx.Set(ref, doc.Obj("v", s.Data().Num("v")+1), Merge)
			return nil
		}, 200)
		if err != nil {
			t.Error(err)
		}
	}
	cross := func() {
		err := st.RunTransaction(func(tx *Tx) error {
			a, g := st.Doc("deployments/DA/counters/x"), st.Doc("counters/x")
			s, err := tx.GetAll(a, g)
			if err != nil {
				return err
			}
			tx.Set(a, doc.Obj("v", s[0].Data().Num("v")+1))
			tx.Set(g, doc.Obj("v", s[1].Data().Num("v")+1))
			return nil
		}, 200)
		if err != nil {
			t.Error(err)
		}
	}
	const N = 25
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		for _, f := range []func(){func() { inc(paths[0]) }, func() { inc(paths[1]) }, func() { inc(paths[2]) }, cross} {
			wg.Add(1)
			go func() { defer wg.Done(); f() }()
		}
	}
	wg.Wait()
	for _, x := range paths {
		eq(t, data(t, st.Doc(x)).Val("v"), N)
	}
	eq(t, data(t, st.Doc("deployments/DA/counters/x")).Val("v"), N)
	eq(t, data(t, st.Doc("counters/x")).Val("v"), N)
}

func TestSlugUniquenessSerializable(t *testing.T) {
	st := open(t, t.TempDir()).Store
	var mu sync.Mutex
	won := 0
	var wg sync.WaitGroup
	for _, id := range []string{"N1", "N2", "N3", "N4"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := RunTx(st, func(tx *Tx) (bool, error) {
				q, err := tx.Query(st.Collection("deployments").Where("slug", "==", "same"))
				if err != nil || !q.Empty() {
					return false, err
				}
				tx.Set(st.Doc("deployments/"+id), doc.Obj("slug", "same", "deleted", false))
				return true, nil
			}, 100)
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	eq(t, won, 1)
	eq(t, len(ids(t, st.Collection("deployments").Where("slug", "==", "same"))), 1)
}

func TestCrossFileAllOrNothing(t *testing.T) {
	st := open(t, t.TempDir()).Store
	must(t, st.Doc("counters/exists").Set(doc.Obj("v", 1)))
	err := st.RunTransaction(func(tx *Tx) error {
		tx.Set(st.Doc("deployments/DX/counters/a"), doc.Obj("v", 1))
		tx.Create(st.Doc("counters/exists"), doc.Obj("v", 2))
		return nil
	})
	code(t, err, AlreadyExists)
	if s, _ := st.Doc("deployments/DX/counters/a").Get(); s.Exists() {
		t.Fatal("deployment part rolled back")
	}
}

func TestStrictAndFallback(t *testing.T) {
	d := t.TempDir()
	p := open(t, d)
	must(t, p.Store.Doc("templates/a").Set(doc.Obj("label", "x", "slug", "s", "ts", doc.TimestampFromMillis(5000), "flag", true)))
	_, err := p.Store.Collection("templates").Where("label", "==", "x").Get()
	if err == nil || !strings.Contains(err.Error(), "strict") {
		t.Fatal(err)
	}
	if _, err = p.Store.Collection("templates").OrderBy("label").Get(); err == nil {
		t.Fatal("strict order")
	}
	if _, err = p.Store.Collection("templates").Where("tags", "array-contains", "x").Get(); err == nil {
		t.Fatal("strict array")
	}
	eq(t, len(ids(t, p.Store.Collection("templates").Where("slug", "==", "s"))), 1)
	no := false
	l := open(t, d, func(c *Config) { c.Strict = &no }).Store.Collection("templates")
	eq(t, len(ids(t, l.Where("label", "==", "x"))), 1)
	eq(t, len(ids(t, l.Where("ts", "<", doc.TimestampFromMillis(6000)))), 1)
	eq(t, len(ids(t, l.Where("flag", "==", true))), 1)
}

func TestRegistryGrowthBackfill(t *testing.T) {
	d := t.TempDir()
	p := open(t, d)
	must(t, p.Store.Doc("templates/a").Set(doc.Obj("label", "L1")))
	must(t, p.Store.Doc("templates/b").Set(doc.Obj("label", "L2")))
	p.Close()
	p2 := open(t, d, func(c *Config) { c.Indexes = IndexRegistry{"templates": {Scalar: []string{"label"}}} })
	eq(t, ids(t, p2.Store.Collection("templates").Where("label", "==", "L2")), []string{"b"})
	g := rawDB(t, d+"/db/global.sqlite")
	var s string
	must(t, g.QueryRow("SELECT group_concat(ix_label) FROM (SELECT ix_label FROM templates ORDER BY id)").Scan(&s))
	eq(t, s, "L1,L2")
}

var testTriggers = []OutboxTrigger{{"tCreated", "created", "deployments/{d}/things/{t}"}, {"tWritten", "written", "gthings/{t}"}}

func outRows(t *testing.T, file string) []string {
	db := rawDB(t, file)
	r, err := db.Query("SELECT trigger, path, before IS NOT NULL, after IS NOT NULL FROM _outbox ORDER BY seq")
	must(t, err)
	defer r.Close()
	out := []string{}
	for r.Next() {
		var a, b string
		var c, d int
		must(t, r.Scan(&a, &b, &c, &d))
		out = append(out, fmt.Sprintf("%s %s %d %d", a, b, c, d))
	}
	return out
}

func TestOutboxRows(t *testing.T) {
	d := t.TempDir()
	p := open(t, d, func(c *Config) { c.Triggers = testTriggers })
	st := p.Store
	must(t, st.Doc("deployments/D1/things/a").Set(doc.Obj("v", 1)))
	must(t, st.Doc("deployments/D1/things/a").Update(doc.Obj("v", 2)))
	must(t, st.Doc("deployments/D1/other/a").Set(doc.Obj("v", 1)))
	must(t, st.Doc("gthings/x").Set(doc.Obj("v", 1)))
	must(t, st.Doc("gthings/x").Set(doc.Obj("v", 1)))
	must(t, st.Doc("gthings/x").Update(doc.Obj("v", doc.Increment(1))))
	must(t, st.Doc("gthings/x").Delete())
	err := st.RunTransaction(func(tx *Tx) error {
		tx.Create(st.Doc("deployments/D1/things/b"), doc.Obj("v", 1))
		tx.Set(st.Doc("gthings/y"), doc.Obj("v", 1))
		tx.Update(st.Doc("gthings/missing"), doc.Obj("v", 1))
		return nil
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	eq(t, outRows(t, d+"/db/deployments/D1.sqlite"), []string{"tCreated deployments/D1/things/a 0 1"})
	eq(t, outRows(t, d+"/db/global.sqlite"), []string{"tWritten gthings/x 0 1", "tWritten gthings/x 1 1", "tWritten gthings/x 1 0"})
}

func TestDispatcher(t *testing.T) {
	d := t.TempDir()
	p := open(t, d, func(c *Config) { c.Triggers = testTriggers })
	st := p.Store
	var mu sync.Mutex
	var seen []DocEvent
	mk := func(tr OutboxTrigger, fail bool) DocTrigger {
		return DocTrigger{tr, func(e DocEvent) error {
			mu.Lock()
			seen = append(seen, e)
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			if fail {
				return fmt.Errorf("handler failure is logged and dropped")
			}
			return nil
		}}
	}
	must(t, st.Doc("deployments/Dx/things/before-start").Create(doc.Obj("v", 0)))
	disp := StartOutboxDispatcher(st, []DocTrigger{mk(testTriggers[0], false), mk(testTriggers[1], true)}, 3, 50*time.Millisecond)
	defer disp.Stop()
	for i := 0; i < 6; i++ {
		must(t, st.Doc(fmt.Sprintf("deployments/Dx/things/t%d", i)).Create(doc.Obj("i", i, "at", doc.TimestampFromMillis(1000*int64(i)))))
	}
	must(t, st.Doc("gthings/g").Set(doc.Obj("v", 1)))
	disp.Idle()
	mu.Lock()
	n := len(seen)
	var t3 *DocEvent
	for i := range seen {
		if strings.HasSuffix(seen[i].Path, "/t3") {
			t3 = &seen[i]
		}
	}
	mu.Unlock()
	eq(t, n, 8)
	eq(t, t3.Params["d"], "Dx")
	eq(t, t3.Params["t"], "t3")
	if t3.Before != nil {
		t.Fatal("before")
	}
	if _, ok := t3.After.Val("at").(doc.Timestamp); !ok {
		t.Fatal("after.at")
	}
	eq(t, outRows(t, d+"/db/deployments/Dx.sqlite"), []string{})
	eq(t, outRows(t, d+"/db/global.sqlite"), []string{})
}

func TestExpiredLeaseReruns(t *testing.T) {
	d := t.TempDir()
	p := open(t, d, func(c *Config) { c.Triggers = testTriggers })
	must(t, p.Store.Doc("gthings/l").Set(doc.Obj("v", 1)))
	db := rawDB(t, d+"/db/global.sqlite")
	_, err := db.Exec("UPDATE _outbox SET leaseUntil = ?", time.Now().UnixMilli()-1)
	must(t, err)
	var n int
	var mu sync.Mutex
	disp := StartOutboxDispatcher(p.Store, []DocTrigger{{testTriggers[1], func(DocEvent) error { mu.Lock(); n++; mu.Unlock(); return nil }}}, 8, 20*time.Millisecond)
	disp.Idle()
	disp.Stop()
	eq(t, n, 1)
}

func TestUnhandledTriggerRowsWait(t *testing.T) {
	d := t.TempDir()
	p := open(t, d, func(c *Config) { c.Triggers = testTriggers })
	must(t, p.Store.Doc("gthings/l").Set(doc.Obj("v", 1)))
	disp := StartOutboxDispatcher(p.Store, nil, 8, 20*time.Millisecond)
	disp.Idle()
	time.Sleep(60 * time.Millisecond)
	disp.Stop()
	eq(t, len(outRows(t, d+"/db/global.sqlite")), 1)
}

func TestTTLSweepAndBackup(t *testing.T) {
	d := t.TempDir()
	p := open(t, d)
	st := p.Store
	now := time.Now().UnixMilli()
	must(t, st.Doc("agentNonces/a").Set(doc.Obj("expiresAt", doc.TimestampFromMillis(now-1000))))
	must(t, st.Doc("agentNonces/b").Set(doc.Obj("expiresAt", doc.TimestampFromMillis(now+60_000))))
	must(t, st.Doc("rateLimits/r1").Set(doc.Obj("count", 1, "expireAt", doc.TimestampFromMillis(now-5))))
	must(t, st.Doc("rateLimits/r2").Set(doc.Obj("count", 1, "expireAt", doc.TimestampFromMillis(now+5000))))
	n, err := SweepDocTTL(st, now)
	must(t, err)
	eq(t, n, 2)
	eq(t, ids(t, st.Collection("agentNonces").Query), []string{"b"})
	eq(t, ids(t, st.Collection("rateLimits").Query), []string{"r2"})

	must(t, st.Doc("templates/a").Set(doc.Obj("slug", "a")))
	must(t, st.Doc("deployments/D1").Set(doc.Obj("slug", "d")))
	must(t, st.Doc("deployments/D1/commits/c").Set(doc.Obj("seq", 1, "at", doc.TimestampFromMillis(1))))
	must(t, st.Doc("zeta/z").Set(doc.Obj("v", 1)))
	restored := t.TempDir()
	out := filepath.Join(restored, "db")
	files, err := BackupSqlite(d, out)
	must(t, err)
	eq(t, len(files), 2)
	b := open(t, restored)
	eq(t, data(t, b.Store.Doc("deployments/D1/commits/c")).Val("seq"), 1)
	eq(t, len(ids(t, b.Store.Collection("templates").Where("slug", "==", "a"))), 1)
	if _, err := BackupSqlite(d, out); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatal(err)
	}
	var paths []string
	must(t, st.AllDocuments(func(p string, m *doc.Map) error { paths = append(paths, p); return nil }))
	eq(t, paths, []string{"agentNonces/b", "deployments/D1", "deployments/D1/commits/c", "rateLimits/r2", "templates/a", "zeta/z"})
	c, err := st.CountDocuments()
	must(t, err)
	eq(t, c, 6)
}

type authEnv struct {
	t *testing.T
	p *Ports
	o AuthHTTPOptions
}

func (e *authEnv) call(method, url string, body any, ip ...string) *AuthHTTPResponse {
	b := []byte{}
	if body != nil {
		b = []byte(doc.MarshalString(body))
	}
	i := "10.0.0.1"
	if len(ip) > 0 {
		i = ip[0]
	}
	return HandleAuthHTTP(AuthHTTPRequest{method, url, b, i}, e.p.Auth, e.o)
}
func j(r *AuthHTTPResponse) *doc.Map {
	if r.Body == nil {
		return doc.New()
	}
	m, _ := doc.ParseMap([]byte(*r.Body))
	return m
}
func errCode(r *AuthHTTPResponse) string { return j(r).Map("error").Str("code") }
func secBoundary()                       { time.Sleep(time.Duration(1100-time.Now().UnixMilli()%1000) * time.Millisecond) }
func acode(t *testing.T, err error, want string) {
	t.Helper()
	if AuthErrorCode(err) != want {
		t.Fatalf("expected %s, got %v", want, err)
	}
}

func TestAuthFlows(t *testing.T) {
	ResetLoginLimiter()
	var mu sync.Mutex
	var mails [][2]string
	e := &authEnv{t: t, p: open(t, t.TempDir())}
	e.o = AuthHTTPOptions{ConsoleURL: "http://console.test", SendMail: func(to, s, text string) error {
		mu.Lock()
		mails = append(mails, [2]string{to, text})
		mu.Unlock()
		return nil
	}}
	a := e.p.Auth
	st := e.p.Store
	u, err := a.CreateUser(CreateUserInput{Email: "Alice@Example.com", Password: Ptr("correct horse"), DisplayName: Ptr("Alice")})
	must(t, err)
	if !regexp.MustCompile(`^[A-Za-z0-9]{28}$`).MatchString(u.UID) || u.Email != "alice@example.com" || !u.HasPassword {
		t.Fatal(u)
	}
	eq(t, u.Providers, []string{"password"})
	raw := data(t, st.Doc("authUsers/"+u.UID))
	if !strings.HasPrefix(raw.Str("passwordHash"), "$argon2id$v=19$m=19456,t=2,p=1$") || strings.Contains(doc.MarshalString(raw), "correct horse") {
		t.Fatal(raw)
	}
	_, err = a.CreateUser(CreateUserInput{Email: "alice@example.com"})
	acode(t, err, "auth/email-already-exists")
	_, err = a.CreateUser(CreateUserInput{Email: "not-an-email"})
	acode(t, err, "auth/invalid-email")
	_, err = a.CreateUser(CreateUserInput{Email: "x@example.com", Password: Ptr("123")})
	acode(t, err, "auth/invalid-password")
	fixed, err := a.CreateUser(CreateUserInput{UID: Ptr("FirebaseUid123"), Email: "bob@example.com"})
	must(t, err)
	if fixed.UID != "FirebaseUid123" || fixed.HasPassword {
		t.Fatal(fixed)
	}
	_, err = a.CreateUser(CreateUserInput{UID: Ptr("FirebaseUid123"), Email: "carol@example.com"})
	acode(t, err, "auth/uid-already-exists")
	x, err := a.GetUserByEmail("ALICE@example.com")
	must(t, err)
	eq(t, x.UID, u.UID)
	_, err = a.GetUser("nope")
	acode(t, err, "auth/user-not-found")
	us, _ := a.GetUsers([]string{u.UID, "nope"})
	eq(t, len(us), 1)
	all, _ := a.ListAllUsers()
	eq(t, len(all), 2)
	_, err = a.UpdateUser(fixed.UID, UpdateUserInput{Email: Ptr("bobby@example.com")})
	must(t, err)
	_, err = a.GetUserByEmail("bob@example.com")
	acode(t, err, "auth/user-not-found")
	must(t, a.DeleteUser(fixed.UID))
	_, err = a.CreateUser(CreateUserInput{Email: "bobby@example.com"})
	must(t, err)

	// login / verify / limiter / disabled / revoke
	cred := doc.Obj("email", "alice@example.com", "password", "correct horse")
	eq(t, j(e.call("GET", "/auth/config", nil)), doc.Obj("provider", "local", "passwordReset", true, "oidc", nil))
	bad := e.call("POST", "/auth/login", doc.Obj("email", "alice@example.com", "password", "wrong"))
	eq(t, bad.Status, 400)
	eq(t, errCode(bad), "auth/invalid-credential")
	eq(t, errCode(e.call("POST", "/auth/login", doc.Obj("email", "nobody@example.com", "password", "x"))), "auth/invalid-credential")
	ok := e.call("POST", "/auth/login", doc.Obj("email", "ALICE@example.com", "password", "correct horse"))
	eq(t, ok.Status, 200)
	tk := j(ok)
	eq(t, tk.Val("expiresIn"), 3600)
	eq(t, tk.Keys(), []string{"idToken", "refreshToken", "expiresIn", "uid", "email"})
	dec, err := a.VerifyIDToken(tk.Str("idToken"), true)
	must(t, err)
	eq(t, dec.UID, tk.Str("uid"))
	eq(t, dec.Exp-dec.Iat, 3600)
	id := tk.Str("idToken")
	_, err = a.VerifyIDToken(id[:len(id)-2]+"xx", false)
	acode(t, err, "auth/argument-error")
	for i := 0; i < 10; i++ {
		e.call("POST", "/auth/login", doc.Obj("email", "alice@example.com", "password", "nope"), "10.9.9.9")
	}
	eq(t, errCode(e.call("POST", "/auth/login", cred, "10.9.9.9")), "auth/too-many-requests")
	eq(t, e.call("POST", "/auth/login", cred, "10.9.9.10").Status, 200)
	_, err = a.UpdateUser(u.UID, UpdateUserInput{Disabled: Ptr(true)})
	must(t, err)
	_, err = a.VerifyIDToken(id, false)
	acode(t, err, "auth/user-disabled")
	eq(t, errCode(e.call("POST", "/auth/login", cred)), "auth/user-disabled")
	eq(t, errCode(e.call("POST", "/auth/refresh", doc.Obj("refreshToken", tk.Str("refreshToken")))), "auth/user-disabled")
	_, err = a.UpdateUser(u.UID, UpdateUserInput{Disabled: Ptr(false)})
	must(t, err)
	secBoundary()
	must(t, a.RevokeRefreshTokens(u.UID))
	_, err = a.VerifyIDToken(id, true)
	acode(t, err, "auth/id-token-revoked")
	_, err = a.VerifyIDToken(id, false)
	must(t, err)
	eq(t, errCode(e.call("POST", "/auth/refresh", doc.Obj("refreshToken", tk.Str("refreshToken")))), "auth/invalid-refresh-token")

	// refresh rotation
	s0 := j(e.call("POST", "/auth/login", cred))
	s1 := j(e.call("POST", "/auth/refresh", doc.Obj("refreshToken", s0.Str("refreshToken"))))
	if s1.Str("refreshToken") == "" || s1.Str("refreshToken") == s0.Str("refreshToken") {
		t.Fatal(s1)
	}
	_, err = a.VerifyIDToken(s1.Str("idToken"), true)
	must(t, err)
	s2 := j(e.call("POST", "/auth/refresh", doc.Obj("refreshToken", s1.Str("refreshToken"))))
	eq(t, errCode(e.call("POST", "/auth/refresh", doc.Obj("refreshToken", s1.Str("refreshToken")))), "auth/invalid-refresh-token")
	eq(t, errCode(e.call("POST", "/auth/refresh", doc.Obj("refreshToken", s2.Str("refreshToken")))), "auth/invalid-refresh-token")
	s3 := j(e.call("POST", "/auth/login", cred))
	eq(t, e.call("POST", "/auth/logout", doc.Obj("refreshToken", s3.Str("refreshToken"))).Status, 204)
	eq(t, errCode(e.call("POST", "/auth/refresh", doc.Obj("refreshToken", s3.Str("refreshToken")))), "auth/invalid-refresh-token")

	// reset
	_, err = a.GeneratePasswordResetLink("nobody@example.com")
	acode(t, err, "auth/user-not-found")
	link, err := a.GeneratePasswordResetLink("alice@example.com")
	must(t, err)
	if !regexp.MustCompile(`^http://console\.test/reset-password\?token=[A-Za-z0-9_-]{43}$`).MatchString(link) {
		t.Fatal(link)
	}
	lu, _ := url.Parse(link)
	token := lu.Query().Get("token")
	session := j(e.call("POST", "/auth/login", cred))
	eq(t, j(e.call("GET", "/auth/reset/check?token="+token, nil)), doc.Obj("email", "alice@example.com"))
	eq(t, e.call("GET", "/auth/reset/check?token=nope", nil).Status, 410)
	weak := e.call("POST", "/auth/reset/confirm", doc.Obj("token", token, "password", "short"))
	eq(t, weak.Status, 400)
	eq(t, errCode(weak), "auth/weak-password")
	secBoundary()
	eq(t, e.call("POST", "/auth/reset/confirm", doc.Obj("token", token, "password", "a new password")).Status, 204)
	again := e.call("POST", "/auth/reset/confirm", doc.Obj("token", token, "password", "another password"))
	eq(t, again.Status, 410)
	eq(t, errCode(again), "auth/invalid-reset-token")
	eq(t, errCode(e.call("POST", "/auth/login", cred)), "auth/invalid-credential")
	eq(t, e.call("POST", "/auth/login", doc.Obj("email", "alice@example.com", "password", "a new password")).Status, 200)
	_, err = a.VerifyIDToken(session.Str("idToken"), true)
	acode(t, err, "auth/id-token-revoked")
	eq(t, errCode(e.call("POST", "/auth/refresh", doc.Obj("refreshToken", session.Str("refreshToken")))), "auth/invalid-refresh-token")
	l2, _ := a.GeneratePasswordResetLink("alice@example.com")
	lu, _ = url.Parse(l2)
	tok2 := lu.Query().Get("token")
	must(t, st.Doc("authReset/"+Sha256Hex(tok2)).Update(doc.Obj("expiresAt", doc.IsoPlus(-1000))))
	eq(t, errCode(e.call("POST", "/auth/reset/confirm", doc.Obj("token", tok2, "password", "whatever123"))), "auth/expired")
	eq(t, e.call("POST", "/auth/reset/request", doc.Obj("email", "nobody@example.com")).Status, 204)
	eq(t, e.call("POST", "/auth/reset/request", doc.Obj("email", "alice@example.com")).Status, 204)
	for i := 0; i < 100; i++ {
		mu.Lock()
		n := len(mails)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	eq(t, len(mails), 1)
	eq(t, mails[0][0], "alice@example.com")
	mu.Unlock()
	eq(t, e.call("GET", "/auth/nope", nil).Status, 404)
	eq(t, e.call("OPTIONS", "/auth/login", nil).Status, 204)
}

func TestOIDC(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	must(t, err)
	var nonce, challenge string
	email := "oidc.user@corp.test"
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		iss := srv.URL
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			fmt.Fprint(w, doc.MarshalString(doc.Obj("issuer", iss, "authorization_endpoint", iss+"/authorize", "token_endpoint", iss+"/token", "jwks_uri", iss+"/jwks")))
		case "/jwks":
			fmt.Fprint(w, doc.MarshalString(doc.Obj("keys", []any{doc.Obj("kty", "RSA", "kid", "k1", "n", doc.B64URL(key.N.Bytes()), "e", doc.B64URL(big.NewInt(int64(key.E)).Bytes()), "alg", "RS256", "use", "sig")})))
		case "/token":
			_ = r.ParseForm()
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			if r.PostForm.Get("code") != "the-code" || doc.B64URL(sum[:]) != challenge || r.PostForm.Get("client_secret") != "sec" {
				w.WriteHeader(400)
				return
			}
			head := b64obj(doc.Obj("alg", "RS256", "kid", "k1"))
			body := b64obj(doc.Obj("iss", iss, "aud", "cid", "sub", "sub-1", "email", email, "email_verified", true, "hd", "corp.test", "nonce", nonce, "exp", time.Now().Unix()+300, "name", "O User"))
			h := sha256.Sum256([]byte(head + "." + body))
			sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
			fmt.Fprint(w, doc.MarshalString(doc.Obj("id_token", head+"."+body+"."+doc.B64URL(sig))))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	e := &authEnv{t: t, p: open(t, t.TempDir())}
	dom := "corp.test"
	e.o = AuthHTTPOptions{ConsoleURL: "http://console.test", Oidc: &OidcConfig{srv.URL, "cid", "sec", "Corp SSO", &dom, "http://opm.test/auth/oidc/callback"}}
	a := e.p.Auth
	_, err = a.CreateUser(CreateUserInput{Email: "alice@example.com", Password: Ptr("correct horse")})
	must(t, err)
	eq(t, j(e.call("GET", "/auth/config", nil)).Val("oidc"), doc.Obj("label", "Corp SSO"))
	flow := func() *AuthHTTPResponse {
		st := e.call("GET", "/auth/oidc/start?next=/app/deployments", nil)
		eq(t, st.Status, 302)
		loc, _ := url.Parse(st.Headers["Location"])
		eq(t, loc.Query().Get("code_challenge_method"), "S256")
		eq(t, loc.Query().Get("hd"), "corp.test")
		nonce, challenge = loc.Query().Get("nonce"), loc.Query().Get("code_challenge")
		return e.call("GET", "/auth/oidc/callback?code=the-code&state="+url.QueryEscape(loc.Query().Get("state")), nil)
	}
	cb := flow()
	eq(t, cb.Status, 302)
	back, _ := url.Parse(cb.Headers["Location"])
	eq(t, back.Host+back.Path, "console.test/login")
	eq(t, back.Query().Get("next"), "/app/deployments")
	f, _ := url.ParseQuery(back.Fragment)
	code1 := f.Get("oidc")
	if code1 == "" {
		t.Fatal(cb.Headers["Location"])
	}
	ex := e.call("POST", "/auth/oidc/exchange", doc.Obj("code", code1))
	eq(t, ex.Status, 200)
	d, err := a.VerifyIDToken(j(ex).Str("idToken"), true)
	must(t, err)
	eq(t, *d.Email, email)
	u, _ := a.GetUserByEmail(email)
	eq(t, u.HasPassword, false)
	su, _ := url.Parse(srv.URL)
	eq(t, u.Providers, []string{su.Host})
	eq(t, errCode(e.call("POST", "/auth/oidc/exchange", doc.Obj("code", code1))), "auth/invalid-oidc-code")
	if !strings.HasSuffix(e.call("GET", "/auth/oidc/callback?code=the-code&state=nope", nil).Headers["Location"], "#oidcError=invalid-state") {
		t.Fatal("replay")
	}
	email = "alice@example.com"
	cb2 := flow()
	b2, _ := url.Parse(cb2.Headers["Location"])
	f2, _ := url.ParseQuery(b2.Fragment)
	eq(t, j(e.call("POST", "/auth/oidc/exchange", doc.Obj("code", f2.Get("oidc")))).Str("email"), "alice@example.com")
	al, _ := a.GetUserByEmail("alice@example.com")
	if !contains(al.Providers, su.Host) {
		t.Fatal(al.Providers)
	}
}

func TestMain(m *testing.M) {
	os.Unsetenv("OPM_LOCAL_STRICT")
	os.Exit(m.Run())
}
