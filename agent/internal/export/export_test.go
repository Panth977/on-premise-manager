package export_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
	// no secret value anywhere in the tree
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
	}
	dc := file(t, res, "sites/site-01/docker-compose.yml")
	t.Logf("compose:\n%s", dc)
	for _, want := range []string{
		`API_TOKEN: "${OPM_SECRET_API_TOKEN:?}"`,
		`DB_URL: "postgres://app:${OPM_SECRET_DB_PASSWORD:?}@db:5432/app?x=$$HOME"`,
		`GREETING: "Hello from site-01"`,
		`MQTT_URL: "mqtt://cq-mqtt:1883"`,
		`"/.opm/opm-render", "exec", "--env", "/opm-env/cq-web.env.json", "--", "/testsrv"`,
		`condition: "service_healthy"`,
		`container_name: "cq-web-2"`,
		`source: "./.opm-runtime/files/mqtt-pwfile"`,
		`source: "./.opm-runtime/files/mosquitto-conf"`,
		`source: "./.opm-runtime/files/app-config"`,
		`source: "./.opm-runtime/secrets/db-password"`,
		`source: "./.opm-data/volumes/state"`,
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
	if strings.Contains(dc, "TOKEN_HASH") {
		t.Error("the derived env must not be in the compose file")
	}
	env := file(t, res, "sites/site-01/.env.example")
	for _, n := range []string{"OPM_SECRET_MQTT_PASSWORD=", "OPM_SECRET_API_TOKEN=", "OPM_SECRET_DB_PASSWORD="} {
		if !strings.Contains(env, n) {
			t.Errorf(".env.example lacks %s", n)
		}
	}
	if strings.Contains(env, "OPM_SITE_HOST=") {
		t.Error("OPM_SITE_HOST is not used by this site")
	}
	for _, p := range []string{"README.md", ".gitignore", "opm/config.json", "opm/export.json", "opm/bin/opm-render", "opm/bin/opm-render-linux-amd64",
		"sites/site-01/files/mqtt-pwfile.opm-template", "sites/site-01/volumes/data/hello.txt", "sites/site-01/volumes/data/bin/run.sh",
		"sites/site-01/volumes/state", "sites/site-01/edge/caddy.json", "sites/site-01/edge/pages/404.html"} {
		if !has(res, p) {
			t.Errorf("tree lacks %s", p)
		}
	}
	if has(res, "sites/site-01/volumes/data/config/site.json") {
		t.Error("the starter entry rendered from a secret must be rendered at up, not in git")
	}
	if !strings.Contains(file(t, res, "sites/site-01/files/app-config"), `"mqtt":"cq-mqtt:1883"`) {
		t.Error("app-config not pre-rendered")
	}
	// the runtime pass writes the deferred outputs with the real values
	rt, err := export.Export(fx.Req, export.Mode{Runtime: true, Site: "site-01", Values: fx.Env})
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	for _, f := range rt.Files {
		paths = append(paths, f.Path)
	}
	t.Logf("runtime files: %v", paths)
	want := map[string]bool{".opm-runtime/files/mosquitto-conf": true, ".opm-runtime/files/app-config": true, ".opm-runtime/files/mqtt-pwfile": true, ".opm-runtime/secrets/db-password": true, ".opm-runtime/env/cq-web.env.json": true}
	for _, f := range rt.Files {
		delete(want, f.Path)
		if f.Path == ".opm-runtime/files/mqtt-pwfile" && !strings.HasPrefix(*f.Text, "northwind:$7$101$") {
			t.Errorf("pwfile %q", *f.Text)
		}
		if f.Path == ".opm-runtime/secrets/db-password" && (*f.Text != exporttest.DBPassword || f.Perm != 0o400) {
			t.Errorf("secret file %q %o", *f.Text, f.Perm)
		}
	}
	if len(want) > 0 {
		t.Errorf("runtime lacks %v", want)
	}
	if _, err := export.Export(fx.Req, export.Mode{Runtime: true, Site: "site-01", Values: map[string]string{}}); err == nil || !strings.Contains(err.Error(), "OPM_SECRET_MQTT_PASSWORD") {
		t.Errorf("missing values: %v", err)
	}
	seeds, err := export.Seeds(fx.Req, "site-01", fx.Env)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range seeds {
		t.Logf("seed %s file=%v source=%s files=%d dirs=%v", s.Volume, s.FileKind, s.Source, len(s.Files), s.Dirs)
	}
	// docker compose config accepts the file (when a compose binary exists)
	if bin, args := composeBin(); bin != "" {
		dir := t.TempDir()
		if err := exporttest.WriteTree(res, dir, fx.Blobs, nil); err != nil {
			t.Fatal(err)
		}
		envf := ""
		for k, v := range fx.Env {
			envf += k + "=" + v + "\n"
		}
		_ = os.WriteFile(filepath.Join(dir, "sites/site-01/.env"), []byte(envf), 0o600)
		cmd := exec.Command(bin, append(args, "config", "--quiet")...)
		cmd.Dir = filepath.Join(dir, "sites/site-01")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("docker compose config: %v\n%s", err, out)
		}
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
