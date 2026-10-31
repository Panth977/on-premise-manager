package cloud

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/sign"
)

func TestOps(t *testing.T) {
	dir := t.TempDir()
	p, err := driver.Open(driver.Config{DataDir: dir, PublicURL: "http://x.test", ConsoleURL: "http://console.test", Strict: boolp(true)})
	must(t, err)
	defer p.Close()
	SetPorts(p)
	cfg := &CloudConfig{DataDir: dir}
	argv := func(a ...string) []string { return append([]string{"opm-cloud"}, a...) }
	run := func(a ...string) {
		t.Helper()
		code, err := RunOp(a[0], cfg, argv(a...))
		must(t, err)
		eq(t, code, 0, a)
	}
	bad, err := RunOp("nope", cfg, argv("nope"))
	must(t, err)
	eq(t, bad, 2)

	run("create-admin", "--email", "Boss@Example.com", "--password", "secret-pw-1", "--name", "Boss")
	u, err := p.Auth.GetUserByEmail("boss@example.com")
	must(t, err)
	acc := AccountView(func() *doc.Map { s, _ := Col.Accounts().Doc(u.UID).Get(); return s.Data() }())
	if !IsAdmin(acc) {
		t.Fatal("admin")
	}
	_, err = p.Auth.CheckPassword("boss@example.com", "secret-pw-1")
	must(t, err)
	run("create-admin", "--email", "boss@example.com", "--password", "secret-pw-2")
	_, err = p.Auth.CheckPassword("boss@example.com", "secret-pw-2")
	must(t, err)
	run("reset-link", "--email", "boss@example.com")

	run("release-key")
	raw, err := os.ReadFile(dir + "/secrets/release-key.json")
	must(t, err)
	if !strings.HasPrefix(string(raw), "{\n  \"privateKeySeedB64\": \"") {
		t.Fatal(string(raw))
	}
	st, _ := os.Stat(dir + "/secrets/release-key.json")
	eq(t, int(st.Mode().Perm()), 0o600)
	key, _ := doc.ParseMap(raw)

	bin := t.TempDir()
	must(t, os.WriteFile(bin+"/opm-agent-linux-amd64", []byte("agent bytes"), 0o755))
	must(t, os.MkdirAll(bin+"/linux-arm64", 0o755))
	must(t, os.WriteFile(bin+"/linux-arm64/opm-agent", []byte("arm bytes"), 0o755))
	must(t, os.WriteFile(bin+"/release.json", []byte(`{"version":"1.2.3","schema":3,"minSchema":2,"commit":"abc","builtAt":null,"extra":1}`), 0o644))
	must(t, os.WriteFile(bin+"/docker-28.5.2-amd64.tgz", []byte("dock a"), 0o644))
	must(t, os.WriteFile(bin+"/docker-28.5.2-arm64.tgz", []byte("dock b"), 0o644))
	run("upload-deps", "--agent-version", "1.2.3", "--agent-notes", " hello ", "--from-dir", bin)
	run("upload-agent", "--dir", bin, "--version", "1.2.3", "--latest")
	hx, _ := p.Blobs.Get("agent/1.2.3/linux-amd64/opm-agent.sha256")
	sig, _ := p.Blobs.Get("agent/1.2.3/linux-amd64/opm-agent.sig")
	must(t, sign.VerifyRelease(key.Str("publicKeyB64"), strings.TrimSpace(string(hx)), strings.TrimSpace(string(sig))))
	l, _ := p.Blobs.Get("agent/latest")
	eq(t, string(l), "1.2.3\n")
	rj, _ := p.Blobs.Get("agent/1.2.3/release.json")
	eq(t, string(rj), "{\n  \"version\": \"1.2.3\",\n  \"schema\": 3,\n  \"minSchema\": 2,\n  \"commit\": \"abc\",\n  \"builtAt\": null\n}\n")
	mf, _ := p.Blobs.Get("deps/manifest.json")
	m, err := doc.ParseMap(mf)
	must(t, err)
	eq(t, m.Keys(), []string{"docker", "minDockerApi", "agent", "updatedAt"})
	eq(t, m.Map("agent"), doc.Obj("version", "1.2.3", "notes", "hello", "schema", nil, "minSchema", nil))
	eq(t, m.Map("docker").Map("files").Map("amd64").Keys(), []string{"path", "sha256", "size"})
	run("upload-deps", "--from-dir", bin) // version from agent/latest, notes kept, bundles kept
	mf, _ = p.Blobs.Get("deps/manifest.json")
	m, _ = doc.ParseMap(mf)
	eq(t, m.Map("agent"), doc.Obj("version", "1.2.3", "notes", "hello", "schema", 3, "minSchema", 2))

	out := filepath.Join(t.TempDir(), "bk")
	run("backup", "--out", out)
	bp, err := driver.Open(driver.Config{DataDir: out, PublicURL: "http://x"})
	must(t, err)
	defer bp.Close()
	b, err := bp.Auth.GetUserByEmail("boss@example.com")
	must(t, err)
	eq(t, b.UID, u.UID)
	x, _ := bp.Blobs.Get("agent/latest")
	eq(t, string(x), "1.2.3\n")
	if _, err := os.Stat(out + "/secrets/release-key.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out + "/BACKUP.json"); err != nil {
		t.Fatal(err)
	}
}

func TestScheduler(t *testing.T) {
	setupStore := func() *driver.Ports {
		p, err := driver.Open(driver.Config{DataDir: t.TempDir(), PublicURL: "http://x", Strict: boolp(true)})
		must(t, err)
		SetPorts(p)
		t.Cleanup(func() { p.Close() })
		return p
	}
	setupStore()
	var n atomic.Int32
	job := driver.ScheduledJob{Name: "j1", Schedule: "every 1 minutes", TimeoutSeconds: 1, Run: func(context.Context) error { n.Add(1); return nil }}
	s := StartScheduler([]driver.ScheduledJob{job}, 10*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	eq(t, n.Load(), 0) // the first start only schedules
	must(t, schedDoc("j1").Update(doc.Obj("lastRunMs", time.Now().UnixMilli()-120_000)))
	for i := 0; i < 200 && n.Load() == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	s.Stop()
	eq(t, n.Load(), 1)
	d, err := schedDoc("j1").Get()
	must(t, err)
	eq(t, d.Data().Val("lastOk"), true)
	eq(t, d.Data().Val("owner"), nil)
	eq(t, d.Data().Keys(), []string{"name", "lastRunMs", "lastOk", "lastError", "lastDurationMs", "leaseUntil", "owner"})
}

// A minimal SMTP server: AUTH + the message as sent.
func TestSendMail(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	defer l.Close()
	got := make(chan string, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		var log strings.Builder
		fmt.Fprint(c, "220 fake\r\n")
		data := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			log.WriteString(line)
			switch {
			case data:
				if line == ".\r\n" {
					data = false
					fmt.Fprint(c, "250 ok\r\n")
				}
			case strings.HasPrefix(line, "EHLO"):
				fmt.Fprint(c, "250-fake\r\n250 AUTH PLAIN\r\n")
			case strings.HasPrefix(line, "AUTH"):
				fmt.Fprint(c, "235 ok\r\n")
			case strings.HasPrefix(line, "DATA"):
				data = true
				fmt.Fprint(c, "354 go\r\n")
			case strings.HasPrefix(line, "QUIT"):
				fmt.Fprint(c, "221 bye\r\n")
				got <- log.String()
				return
			default:
				fmt.Fprint(c, "250 ok\r\n")
			}
		}
	}()
	url := "smtp://user:pa%40ss@" + l.Addr().String()
	must(t, sendSMTP(url, "On-Prem Manager <no-reply@opm.example>", "a@b.c", "Réset", "line1\nline2\n"))
	s := <-got
	for _, want := range []string{"AUTH PLAIN " + base64.StdEncoding.EncodeToString([]byte("\x00user\x00pa@ss")), "MAIL FROM:<no-reply@opm.example>", "RCPT TO:<a@b.c>", "From: On-Prem Manager <no-reply@opm.example>", "To: a@b.c", "Subject: =?utf-8?q?R=C3=A9set?=", "line1\r\nline2\r\n"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
}

func TestNextRun(t *testing.T) {
	at := time.Date(2026, 10, 31, 4, 30, 0, 0, time.UTC).UnixMilli()
	n, err := driver.NextRun("every day 03:00", at)
	must(t, err)
	eq(t, n, time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC).UnixMilli())
	n, _ = driver.NextRun("every 5 minutes", at+1)
	eq(t, n, at+5*60_000)
	if _, err := driver.NextRun("nope", at); err == nil {
		t.Fatal("bad schedule")
	}
}
