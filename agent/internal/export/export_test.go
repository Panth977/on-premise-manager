package export_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"opm/agent/internal/export"
	"opm/agent/internal/export/exporttest"
)

func exportFixture(t *testing.T) (exporttest.Fixture, export.Result) {
	t.Helper()
	fx := exporttest.Build(exporttest.Options{})
	res, err := export.Export(fx.Req, export.Mode{})
	if err != nil {
		t.Fatal(err)
	}
	return fx, res
}

func file(t *testing.T, res export.Result, path string) string {
	t.Helper()
	for _, f := range res.Files {
		if f.Path == path {
			if f.Text == nil {
				t.Fatalf("%s is not text", path)
			}
			return *f.Text
		}
	}
	t.Fatalf("no %s in the tree", path)
	return ""
}

func has(res export.Result, path string) bool {
	for _, f := range res.Files {
		if f.Path == path {
			return true
		}
	}
	return false
}

func TestExportFixture(t *testing.T) {
	fx, res := exportFixture(t)
	t.Logf("tree:\n%s", exporttest.Tree(res))
	for _, n := range res.Notes {
		t.Logf("note %s: %s", n.Code, n.Message)
	}
	// deterministic
	again, _ := export.Export(fx.Req, export.Mode{})
	a, _ := json.Marshal(res)
	b, _ := json.Marshal(again)
	if string(a) != string(b) {
		t.Fatal("export is not deterministic")
	}
	// no secret value, no sentinel and nothing of the agent anywhere in the tree
	for _, f := range res.Files {
		if f.Text == nil {
			continue
		}
		for _, s := range []string{exporttest.MQTTPassword, exporttest.APIToken, exporttest.DBPassword} {
			if strings.Contains(*f.Text, s) {
				t.Fatalf("%s holds a secret value", f.Path)
			}
		}
		if strings.ContainsAny(*f.Text, "«‹") {
			t.Fatalf("%s holds a sentinel", f.Path)
		}
		for _, s := range []string{"opm-render", "/.opm/", "opm-init", "opm.export"} {
			if strings.Contains(*f.Text, s) {
				t.Errorf("%s mentions %s", f.Path, s)
			}
		}
	}
	dc := file(t, res, "sites/site-01/docker-compose.yml")
	t.Logf("compose:\n%s", dc)
	derived := regexp.MustCompile(`TOKEN_HASH: "\$\{(OPM_D_[0-9A-F]{12}):\?run \./deploy\.sh\}"`).FindStringSubmatch(dc)
	if derived == nil {
		t.Fatal("TOKEN_HASH must come from a derived value in .env")
	}
	for _, want := range []string{
		`API_TOKEN: "${OPM_SECRET_API_TOKEN:?run ./deploy.sh}"`,
		`DB_URL: "postgres://app:${OPM_SECRET_DB_PASSWORD:?run ./deploy.sh}@db:5432/app?x=$$HOME"`,
		`GREETING: "Hello from site-01"`,
		`MQTT_URL: "mqtt://cq-mqtt:1883"`,
		`condition: "service_healthy"`,
		`container_name: "cq-web-2"`,
		`source: "./.runtime/files/mqtt-pwfile"`,
		`source: "./.runtime/files/mosquitto-conf"`,
		`source: "./.runtime/files/app-config"`,
		`source: "./.runtime/secrets/db-password"`,
		`source: "./.data/volumes/state"`,
		`"/.tools/busybox", "sh", "-c"`,
		`image: "` + export.PortainerImage + `"`,
		`image: "` + export.EdgeImage + `"`,
		`profiles: ["portainer"]`,
		`"127.0.0.1:28080:8080/tcp"`,
		`name: "opm_DepComposeEquiv00001_site-01"`,
		`restart: "on-failure"`,
		`mem_limit: 134217728`,
		`cpus: 0.5`,
	} {
		if !strings.Contains(dc, want) {
			t.Errorf("compose lacks %s", want)
		}
	}
	if strings.Contains(dc, "opm.deployment") || strings.Contains(dc, "opm.site") {
		t.Error("the agent's bookkeeping labels must not be set")
	}
	env := file(t, res, "sites/site-01/site.env.example")
	for _, n := range []string{"OPM_SECRET_MQTT_PASSWORD=", "OPM_SECRET_API_TOKEN=", "OPM_SECRET_DB_PASSWORD=", "PORTAINER_ADMIN_PASSWORD="} {
		if !strings.Contains(env, n) {
			t.Errorf("site.env.example lacks %s", n)
		}
	}
	if strings.Contains(env, "OPM_SITE_HOST=") {
		t.Error("OPM_SITE_HOST is not used by this site")
	}
	setup := file(t, res, "sites/site-01/setup.sh")
	if !strings.Contains(setup, "step "+derived[1]+" sha256\n") || !regexp.MustCompile(`step OPM_D_[0-9A-F]{12} mosquitto_passwd sha512-pbkdf2 101\n`).MatchString(setup) {
		t.Errorf("setup.sh lacks the steps:\n%s", setup)
	}
	if got := file(t, res, "sites/site-01/templates/steps/"+derived[1]+".in"); got != "@@{OPM_SECRET_API_TOKEN}@@" {
		t.Errorf("step input %q", got)
	}
	for _, p := range []string{"README.md", ".gitignore", "sites/site-01/README.md", "sites/site-01/deploy.sh", "sites/site-01/init.sh",
		"sites/site-01/files/mqtt-pwfile.template", "sites/site-01/volumes/data/hello.txt", "sites/site-01/volumes/data/bin/run.sh",
		"sites/site-01/volumes/state", "sites/site-01/edge/caddy.json", "sites/site-01/edge/pages/404.html",
		"sites/site-01/templates/volumes/data/config/site.json.template", "sites/site-01/templates/secrets/db-password.template"} {
		if !has(res, p) {
			t.Errorf("tree lacks %s", p)
		}
	}
	for _, p := range []string{"opm/config.json", "opm/export.json", "opm/bin/opm-render", "sites/site-01/.env.example", "sites/site-01/volumes/data/config/site.json"} {
		if has(res, p) {
			t.Errorf("tree must not have %s", p)
		}
	}
	for _, f := range res.Files {
		if strings.HasSuffix(f.Path, ".sh") && f.Mode != "100755" {
			t.Errorf("%s is not executable", f.Path)
		}
	}
	if !strings.Contains(file(t, res, "sites/site-01/files/app-config"), `"mqtt":"cq-mqtt:1883"`) {
		t.Error("app-config not pre-rendered")
	}
	readme := file(t, res, "sites/site-01/README.md")
	for _, s := range []string{"git clone https://github.com/example/opm-mirror.git", "git switch -c deployment/compose-equiv/site/site-01 origin/deployment/compose-equiv/cloud",
		"git merge origin/deployment/compose-equiv/cloud", "./deploy.sh", "get.docker.com", ":9443"} {
		if !strings.Contains(readme, s) {
			t.Errorf("site README lacks %q", s)
		}
	}

	// the machine side for real (Docker present): deploy.sh --render-only, then docker compose config
	if exec.Command("docker", "info").Run() != nil {
		t.Skip("no Docker: the machine-side scripts are covered by TestComposeEquivalence")
	}
	// under the repository (git-ignored): Docker Desktop / Colima share the home folder with their VM, not /var/folders
	root, _ := filepath.Abs("../../../test-results-export")
	_ = os.MkdirAll(root, 0o755)
	dir, err := os.MkdirTemp(root, "run-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := exporttest.WriteTree(res, dir, fx.Blobs); err != nil {
		t.Fatal(err)
	}
	sd := filepath.Join(dir, "sites/site-01")
	senv := "# test\nOPM_SECRET_MQTT_PASSWORD=" + exporttest.MQTTPassword + "\nOPM_SECRET_API_TOKEN=\"" + exporttest.APIToken + "\"\nOPM_SECRET_DB_PASSWORD=" + exporttest.DBPassword + "\nPORTAINER_ADMIN_PASSWORD=correct-horse-battery\n"
	if err := os.WriteFile(filepath.Join(sd, "site.env"), []byte(senv), 0o600); err != nil {
		t.Fatal(err)
	}
	deploy := func() ([]byte, error) {
		cmd := exec.Command("sh", "./deploy.sh", "--render-only")
		cmd.Dir = sd
		return cmd.CombinedOutput()
	}
	if out, err := deploy(); err != nil {
		t.Fatalf("deploy.sh --render-only: %v\n%s", err, out)
	} else {
		t.Logf("deploy.sh --render-only:\n%s", out)
	}
	read := func(p string) string {
		b, err := os.ReadFile(filepath.Join(sd, p))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	h := sha256.Sum256([]byte(exporttest.APIToken))
	if v := read(".runtime/v/" + derived[1]); v != hex.EncodeToString(h[:]) {
		t.Errorf("derived sha256 %q", v)
	}
	if !strings.Contains(read(".env"), derived[1]+`="`+hex.EncodeToString(h[:])+`"`) {
		t.Error(".env lacks the derived value")
	}
	if pw := read(".runtime/files/mqtt-pwfile"); !strings.HasPrefix(pw, "northwind:$7$101$") || !strings.HasSuffix(pw, "\n") {
		t.Errorf("pwfile %q", pw)
	}
	if s := read(".runtime/secrets/db-password"); s != exporttest.DBPassword {
		t.Errorf("secret file %q", s)
	}
	if st, err := os.Stat(filepath.Join(sd, ".runtime/secrets/db-password")); err != nil || st.Mode().Perm() != 0o400 {
		t.Errorf("secret file mode %v %v", st, err)
	}
	if st, err := os.Stat(filepath.Join(sd, ".runtime/files/app-config")); err != nil || st.Mode().Perm() != 0o444 {
		t.Errorf("file mode %v %v", st, err)
	}
	if s := read(".runtime/seed/data/config/site.json"); s != `{"site":"site-01","tokenHash":"`+hex.EncodeToString(h[:])+`"}`+"\n" {
		t.Errorf("rendered starter file %q", s)
	}
	pw1 := read(".runtime/files/mqtt-pwfile")
	if out, err := deploy(); err != nil {
		t.Fatalf("second deploy.sh: %v\n%s", err, out)
	}
	if read(".runtime/files/mqtt-pwfile") != pw1 {
		t.Error("an unchanged secret must keep its mosquitto hash (random salt)")
	}
	if bin, args := composeBin(); bin != "" {
		c := exec.Command(bin, append(args, "config", "--quiet")...)
		c.Dir = sd
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("docker compose config: %v\n%s", err, out)
		}
	}
	// a missing value is named
	_ = os.WriteFile(filepath.Join(sd, "site.env"), []byte("PORTAINER=off\n"), 0o600)
	if out, err := deploy(); err == nil || !strings.Contains(string(out), "OPM_SECRET_MQTT_PASSWORD") {
		t.Errorf("missing values: %v %s", err, out)
	}
}

// composeBin finds docker compose: $OPM_COMPOSE_BIN, `docker compose`, docker-compose.
func composeBin() (string, []string) {
	if b := os.Getenv("OPM_COMPOSE_BIN"); b != "" {
		return b, nil
	}
	if exec.Command("docker", "compose", "version").Run() == nil {
		return "docker", []string{"compose"}
	}
	if p, err := exec.LookPath("docker-compose"); err == nil {
		return p, nil
	}
	return "", nil
}
