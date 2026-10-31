package cloud

import (
	"crypto/rand"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
)

const opsHelp = `usage: opm-cloud <command> [options]   (config: env OPM_* or --config <file>)
  serve                                   run the server (default)
  create-admin --email E [--password P] [--name N]
                                          create / reset an admin (prints the password only when it generated one)
  reset-link --email E                    print a one-hour password-reset link for an existing user
  release-key                             create <dataDir>/secrets/release-key.json (agent signing key) if missing; print the public key
  upload-agent --dir D --version V [--latest]
                                          upload agent binaries (signed with the data dir's release key or OPM_RELEASE_PRIVATE_KEY)
  upload-deps [--docker-version 28.5.2] [--agent-version V] [--agent-notes T] [--from-dir D]
  backup --out DIR                        consistent online backup (SQLite VACUUM INTO + blobs + secrets)`

// CreateAdminWith is createAdminWith: create or reset an admin. Returns uid, email.
func CreateAdminWith(email0, password, name string) (string, string, error) {
	email := strings.ToLower(doc.JSTrim(email0))
	a := AuthAdmin()
	var uid string
	u, err := a.GetUserByEmail(email)
	if err == nil {
		uid = u.UID
		if _, err := a.UpdateUser(uid, driver.UpdateUserInput{Password: &password, DisplayName: &name, Disabled: driver.Ptr(false)}); err != nil {
			return "", "", err
		}
	} else {
		if driver.AuthErrorCode(err) != "auth/user-not-found" {
			return "", "", err
		}
		n, err := a.CreateUser(driver.CreateUserInput{Email: email, Password: &password, DisplayName: &name, EmailVerified: true})
		if err != nil {
			return "", "", err
		}
		uid = n.UID
	}
	ref := Col.Accounts().Doc(uid)
	s, err := ref.Get()
	if err != nil {
		return "", "", err
	}
	cur := s.Data()
	or := func(k string, def any) any {
		if v := cur.Val(k); v != nil {
			return v
		}
		return def
	}
	acc := doc.Obj("uid", uid, "img", cur.Val("img"), "name", name, "email", email, "phone", cur.Val("phone"), "access", FullAdminRules(), "disabled", false, "createdAt", or("createdAt", doc.NowISO()), "createdBy", or("createdBy", "bootstrap"), "lastLoginAt", cur.Val("lastLoginAt"), "lastSetPassword", nil, "lastResetLink", nil)
	if err := ref.Set(acc); err != nil {
		return "", "", err
	}
	return uid, email, nil
}

// ArgOf is argOf of server.ts.
func ArgOf(name string, argv []string) (string, bool) {
	for i, a := range argv {
		if a == "--"+name {
			if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "--") {
				return argv[i+1], true
			}
			break
		}
	}
	for _, a := range argv {
		if strings.HasPrefix(a, "--"+name+"=") {
			return a[len(name)+3:], true
		}
	}
	return "", false
}

func flagOf(name string, argv []string) bool {
	for _, a := range argv {
		if a == "--"+name {
			return true
		}
	}
	return false
}

func usage(msg string) int {
	fmt.Fprintf(os.Stderr, "%s\n\n%s\n", msg, opsHelp)
	return 2
}

var versionRe = regexp.MustCompile(`^[0-9A-Za-z._+-]{1,64}$`)
var dockerVerRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

func printJSON(m *doc.Map) { fmt.Println(doc.MarshalString(m)) }

// RunOp is run() of server/ops.ts. argv is the whole command line.
func RunOp(cmd string, cfg *CloudConfig, argv []string) (int, error) {
	arg := func(n string) string {
		v, _ := ArgOf(n, argv)
		return v
	}
	logln := func(s string) { fmt.Println(s) }
	switch cmd {
	case "help", "--help":
		fmt.Println(opsHelp)
		return 0, nil
	case "create-admin":
		email := arg("email")
		if email == "" {
			return usage("create-admin needs --email"), nil
		}
		given := arg("password")
		password := given
		if password == "" {
			b := make([]byte, 15)
			if _, err := rand.Read(b); err != nil {
				return 1, err
			}
			password = doc.B64URL(b) + "aA1"
		}
		name := arg("name")
		if name == "" {
			name = "Admin"
		}
		uid, e, err := CreateAdminWith(email, password, name)
		if err != nil {
			return 1, err
		}
		m := doc.Obj("ok", true, "uid", uid, "email", e)
		if given == "" {
			m.Set("password", password)
		}
		printJSON(m)
		return 0, nil
	case "reset-link":
		email := arg("email")
		if email == "" {
			return usage("reset-link needs --email"), nil
		}
		l, err := AuthAdmin().GeneratePasswordResetLink(strings.ToLower(doc.JSTrim(email)))
		if err != nil {
			return 1, err
		}
		fmt.Println(l)
		return 0, nil
	case "release-key":
		_, pub, created, err := releaseKey(cfg)
		if err != nil {
			return 1, err
		}
		printJSON(doc.Obj("ok", true, "created", created, "file", releaseKeyFile(cfg), "publicKeyB64", pub))
		return 0, nil
	case "upload-agent":
		dir, version := arg("dir"), arg("version")
		if dir == "" || version == "" || !versionRe.MatchString(version) {
			return usage("upload-agent needs --dir and --version"), nil
		}
		seed := os.Getenv("OPM_RELEASE_PRIVATE_KEY")
		if seed == "" {
			var err error
			if seed, _, _, err = releaseKey(cfg); err != nil {
				return 1, err
			}
		}
		if seed == "" {
			return usage("no release key: set OPM_RELEASE_PRIVATE_KEY"), nil
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return 1, err
		}
		latest := flagOf("latest", argv)
		if err := UploadAgentDir(Blobs(), abs, version, latest, seed, logln); err != nil {
			return 1, err
		}
		// --latest also moves the PIN (deps/manifest.json agent): sites are offered it and install scripts pin it
		if latest {
			raw, err := Blobs().Get("deps/manifest.json")
			if err != nil {
				return 1, err
			}
			if raw != nil {
				m, err := doc.ParseMap(raw)
				if err != nil {
					return 1, err
				}
				if m.Map("agent").Val("version") != version {
					m.Set("agent", doc.Obj("version", version, "notes", nil)).Set("updatedAt", doc.NowISO())
					if err := Blobs().Put("deps/manifest.json", []byte(doc.MarshalString(m)), driver.PutOptions{ContentType: "application/json"}); err != nil {
						return 1, err
					}
					fmt.Printf("pinned agent release = %s (deps/manifest.json)\n", version)
				}
			}
		}
		return 0, nil
	case "upload-deps":
		dv := arg("docker-version")
		if dv == "" {
			dv = DockerVersion
		}
		if !dockerVerRe.MatchString(dv) {
			return usage("--docker-version must be an exact version"), nil
		}
		av := arg("agent-version")
		if av == "" {
			raw, err := Blobs().Get("agent/latest")
			if err != nil {
				return 1, err
			}
			av = doc.JSTrim(string(raw))
		}
		var notes *string
		for _, a := range argv {
			if a == "--agent-notes" || strings.HasPrefix(a, "--agent-notes=") {
				v := arg("agent-notes")
				notes = &v
				break
			}
		}
		if _, err := UploadDeps(Blobs(), dv, av, notes, arg("from-dir"), logln); err != nil {
			return 1, err
		}
		return 0, nil
	case "backup":
		out := arg("out")
		if out == "" {
			return usage("backup needs --out <dir>"), nil
		}
		dst, err := filepath.Abs(out)
		if err != nil {
			return 1, err
		}
		if err := os.MkdirAll(dst, 0o777); err != nil {
			return 1, err
		}
		if _, err := driver.BackupSqlite(cfg.DataDir, filepath.Join(dst, "db")); err != nil {
			return 1, err
		}
		for _, d := range []string{"blobs", "blobmeta", "secrets"} {
			if _, err := os.Stat(filepath.Join(cfg.DataDir, d)); err == nil {
				if err := copyTree(filepath.Join(cfg.DataDir, d), filepath.Join(dst, d)); err != nil {
					return 1, err
				}
			}
		}
		raw, err := doc.MarshalIndent(doc.Obj("createdAt", doc.NowISO(), "from", cfg.DataDir), 2)
		if err != nil {
			return 1, err
		}
		if err := os.WriteFile(filepath.Join(dst, "BACKUP.json"), append(raw, '\n'), 0o666); err != nil {
			return 1, err
		}
		printJSON(doc.Obj("ok", true, "out", dst))
		return 0, nil
	}
	return usage("unknown command " + cmd), nil
}

// copyTree is cpSync(recursive, preserveTimestamps): modes and times kept.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		t := filepath.Join(dst, rel)
		st, err := e.Info()
		if err != nil {
			return err
		}
		if e.IsDir() {
			return os.MkdirAll(t, st.Mode().Perm()|0o700)
		}
		if !st.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(t, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, st.Mode().Perm())
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		if e := out.Close(); err == nil {
			err = e
		}
		if err != nil {
			return err
		}
		return os.Chtimes(t, st.ModTime(), st.ModTime())
	})
}
