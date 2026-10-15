package expr

// §21 #103: MosquittoHash reproduces the real mosquitto_passwd — static lines captured from the tool
// (shared/testdata/mosquitto-passwd-vectors.json) and, with Docker, a live run in eclipse-mosquitto containers.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// checkLine parses "<user>:$7$<it>$<salt>$<hash>" / "<user>:$6$<salt>$<hash>" and recomputes it with the line's salt.
func checkLine(t *testing.T, tool, password, line string) {
	t.Helper()
	enc := line[strings.IndexByte(line, ':')+1:]
	p := strings.Split(enc, "$")
	format, _ := strconv.Atoi(p[1])
	saltB64, iterations := p[2], 1
	if format == 7 {
		iterations, _ = strconv.Atoi(p[2])
		saltB64 = p[3]
	}
	salt, err := base64.StdEncoding.DecodeString(saltB64)
	if err != nil {
		t.Fatalf("%s: salt %q: %v", tool, saltB64, err)
	}
	if got := MosquittoHash(password, salt, format, iterations); got != enc {
		t.Fatalf("%s: password %q\n got %s\nwant %s", tool, password, got, enc)
	}
}

func TestMosquittoPasswdVectors(t *testing.T) {
	b, err := os.ReadFile("../../../shared/testdata/mosquitto-passwd-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Lines []struct{ Tool, Password, Line string } `json:"lines"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Lines) < 8 {
		t.Fatalf("only %d lines", len(v.Lines))
	}
	for _, l := range v.Lines {
		checkLine(t, l.Tool, l.Password, l.Line)
	}
}

func TestMosquittoSalt(t *testing.T) {
	a := MosquittoSalt("pw", "dep", "site-01")
	if len(a) != MosquittoSaltLen || base64.StdEncoding.EncodeToString(a) != base64.StdEncoding.EncodeToString(MosquittoSalt("pw", "dep", "site-01")) {
		t.Fatal("not deterministic")
	}
	for _, o := range [][]byte{MosquittoSalt("pw", "dep", "site-02"), MosquittoSalt("pw", "dep2", "site-01"), MosquittoSalt("pw2", "dep", "site-01")} {
		if string(o) == string(a) {
			t.Fatal("salt must differ per site / deployment / password")
		}
	}
}

// TestDockerMosquittoPasswd runs the REAL mosquitto_passwd (eclipse-mosquitto:2.0.18 = the user's 2.0 format, 12-byte
// salt / 101 iterations; eclipse-mosquitto:2 = current 2.x, 64-byte salt / 1000 iterations; both -H sha512-pbkdf2 and
// -H sha512) on random passwords and checks MosquittoHash gives the same line for the same salt.
func TestDockerMosquittoPasswd(t *testing.T) {
	if testing.Short() {
		t.Skip("docker (-short)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker CLI")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("no docker: ", err)
	}
	rnd := make([]byte, 9)
	_, _ = rand.Read(rnd)
	passwords := []string{"s3cr3t:pässwörd-" + hex.EncodeToString(rnd), "a", "with space and $dollar \\ back"}
	for _, img := range []string{"eclipse-mosquitto:2.0.18", "eclipse-mosquitto:2"} {
		for _, h := range []string{"sha512-pbkdf2", "sha512"} {
			for _, pw := range passwords {
				// password passed as an argument (no shell): -b writes the line, cat prints it
				out, err := exec.Command("docker", "run", "--rm", "--entrypoint", "sh", img, "-c",
					`mosquitto_passwd -H "$1" -b -c /tmp/pw u "$2" >/dev/null && cat /tmp/pw && (mosquitto -h 2>&1 | grep -o "version [0-9.]*" | head -1)`, "sh", h, pw).CombinedOutput()
				if err != nil {
					t.Fatalf("%s %s: %v\n%s", img, h, err, out)
				}
				lines := strings.Split(strings.TrimSpace(string(out)), "\n")
				checkLine(t, img+" -H "+h, pw, lines[0])
				t.Logf("%s (%s) -H %s: %s… matches", img, strings.TrimSpace(lines[len(lines)-1]), h, lines[0][:min(len(lines[0]), 40)])
			}
		}
	}
	// the filter's own line with the default arguments is the 2.0 shape: $7$101$<12-byte salt>$<64-byte hash>
	ctx := &Context{Var: map[string]any{}, Secret: map[string]*string{"p": strPtr(passwords[0])}}
	ctx.Deployment.ID, ctx.Self.Slug = "dep", "site-01"
	r, err := Resolve("u:{{ secret.p | mosquitto_passwd }}", ctx, FieldFile)
	if err != nil {
		t.Fatal(err)
	}
	checkLine(t, "filter", passwords[0], r.Value)
	if !strings.HasPrefix(r.Value, "u:$7$101$") || len(strings.Split(r.Value, "$")[3]) != 16 || r.Masked != "u:••••" || !r.Secret {
		t.Fatalf("filter line %s / %s", r.Value, r.Masked)
	}
}

func strPtr(s string) *string { return &s }

// TestSecretHashed: TS ResolveResult.secretHashed (§21 #103) — a rendered File with only mosquitto_passwd secrets is 0444.
func TestSecretHashed(t *testing.T) {
	ctx := &Context{Var: map[string]any{"v": "x"}, Secret: map[string]*string{"p": strPtr("pw"), "q": strPtr("pw2")}}
	ctx.Deployment.ID, ctx.Self.Slug = "d", "s"
	for src, want := range map[string]bool{
		"a:{{ secret.p | mosquitto_passwd }}\nb:{{ secret.q | default(\"z\") | mosquitto_passwd | upper }}": true,
		"a:{{ secret.p | mosquitto_passwd }} # {{ secret.p }}":                                              false,
		"{{ secret.p | sha256 }}":        false,
		"{{ var.v | mosquitto_passwd }}": false,
	} {
		r, err := Resolve(src, ctx, FieldFile)
		if err != nil {
			t.Fatal(err)
		}
		if r.SecretHashed != want {
			t.Fatalf("%q: secretHashed %v, want %v", src, r.SecretHashed, want)
		}
	}
}
