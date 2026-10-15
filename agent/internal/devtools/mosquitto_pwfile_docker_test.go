package devtools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/docker"
	"opm/agent/internal/driver"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
)

// TestDockerMosquittoPasswordFile (§21 #103) end to end on real Docker:
//
//  1. the user's File `mqtt-pwfile` (render: expressions) is rendered ONCE for the site with the real renderer
//     (validate.SiteFileExprContext + RenderSiteFile, field file): `northwind:{{ secret.mqtt-password | mosquitto_passwd }}`
//     (+ a second user whose password is a plain value, for the static-value preset);
//  2. the rendered file is the password_file of a real Mosquitto broker (eclipse-mosquitto:2.0.18 — the 2.0 broker that
//     accepts ONLY 12-byte salts — and eclipse-mosquitto:2 = 2.1.x), allow_anonymous false, owned by root with the mode the
//     agent writes (driver.RenderedFileMode: 0444 — the secret is only a salted hash; Mosquitto opens password_file after
//     dropping to uid 1883, so a root-owned 0400 file would be unreadable — shown too);
//  3. dev-tool MQTT presets connect with the PLAIN password: `{{ secret.mqtt-password }}` (Secret mode) and a static
//     Value (literal, §21 #103); a wrong password is refused by the broker;
//  4. the static value never appears in an API read (MASK), only in Reveal, and is redacted from activities.
func TestDockerMosquittoPasswordFile(t *testing.T) {
	if testing.Short() {
		t.Skip("docker integration (-short)")
	}
	dc := docker.New(docker.DefaultHost(""))
	if err := dc.Ping(context.Background()); err != nil {
		t.Skip("no docker: ", err)
	}
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	const secretPw = "Mqtt-Secret-Pässwort-2026"
	const staticPw = "ops-Static-Value-77"

	// 1. render the file like the agent does at staging / APPLY
	pw := secretPw
	cfg := &model.Config{
		Secrets: []model.SecretDef{{SecretSlug: "mqtt-password", Value: &pw}},
		Sites:   []model.Site{{SiteSlug: "site-01"}},
	}
	ectx := validate.SiteFileExprContext(cfg, nil, nil, "JzOFFecGJv7AdGWtCjjf", nil, "site-01", nil)
	src := "northwind:{{ secret.mqtt-password | mosquitto_passwd }}\nops:{{ \"" + staticPw + "\" | mosquitto_passwd }}\n"
	out := validate.RenderSiteFile(src, ectx)
	if out.Err != nil {
		t.Fatalf("render: %+v", out.Err)
	}
	if !out.Secret || !strings.HasPrefix(out.Value, "northwind:$7$101$") || !strings.HasPrefix(out.Masked, "northwind:••••\n") {
		t.Fatalf("rendered %q masked %q secret %v", out.Value, out.Masked, out.Secret)
	}
	if again := validate.RenderSiteFile(src, ectx); again.Value != out.Value {
		t.Fatal("rendering is not stable (deterministic salt)")
	}
	t.Logf("rendered (masked in previews): %q", out.Masked)
	if !out.SecretHashed || driver.RenderedFileMode(out) != 0o444 {
		t.Fatalf("a file whose secret only goes through mosquitto_passwd must be written 0444 (got %o, hashed %v)", driver.RenderedFileMode(out), out.SecretHashed)
	}
	if clear := validate.RenderSiteFile("# {{ secret.mqtt-password }}\n"+src, ectx); driver.RenderedFileMode(clear) != 0o400 {
		t.Fatal("a secret in clear keeps 0400")
	}

	// Colima / Docker Desktop share $HOME with the VM: the bind mounts live there
	home, _ := os.UserHomeDir()
	dir, err := os.MkdirTemp(home, ".opm-mqtt-pwfile-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.WriteFile(filepath.Join(dir, "pwfile"), []byte(out.Value), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mosquitto.conf"), []byte("listener 1883\nallow_anonymous false\npassword_file /mosquitto/config/pwfile\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, image := range []string{"eclipse-mosquitto:2.0.18", "eclipse-mosquitto:2"} {
		t.Run(image, func(t *testing.T) {
			tag := fmt.Sprintf("opmmq%d", time.Now().UnixNano()%1e8)
			network := tag + "-net"
			run("network", "create", "--label", "opm.devtools.test="+tag, network)
			t.Cleanup(func() {
				ids, _ := exec.Command("docker", "ps", "-aq", "--filter", "label=opm.devtools.test="+tag).Output()
				if f := strings.Fields(string(ids)); len(f) > 0 {
					_ = exec.Command("docker", append([]string{"rm", "-f"}, f...)...).Run()
				}
				_ = exec.Command("docker", "network", "rm", network).Run()
			})
			// like the agent: a root-owned file with the agent's mode, mounted read-only (copied in as root here: the bind
			// mount of the Mac / Colima test host does not keep root ownership)
			start := func(name string, mode os.FileMode) string {
				return run("run", "-d", "--name", tag+"-"+name, "--label", "opm.devtools.test="+tag, "--network", network, "--network-alias", name,
					"-p", "127.0.0.1::1883", "-v", dir+":/src:ro", "--entrypoint", "sh", image, "-c",
					fmt.Sprintf("cp /src/pwfile /mosquitto/config/pwfile && chown root:root /mosquitto/config/pwfile && chmod %o /mosquitto/config/pwfile && exec mosquitto -c /src/mosquitto.conf", mode))
			}
			id := start("mqtt", driver.RenderedFileMode(out))
			// the old rule (0400 for every secret) would lock the broker out of its own password file
			locked := start("mqtt-0400", 0o400)
			time.Sleep(1500 * time.Millisecond)
			logs, _ := exec.Command("docker", "logs", locked).CombinedOutput()
			for _, l := range strings.Split(string(logs), "\n") {
				if strings.Contains(l, "Unable to open pwfile") {
					t.Logf("root-owned 0400 password_file (the pre-#103 rule): %s", strings.TrimSpace(l))
				}
			}
			if !strings.Contains(string(logs), "Unable to open pwfile") {
				t.Logf("0400 broker logs: %s", logs)
			}
			mq := HeadContainer{ContainerSlug: "mqtt", ServiceSlug: "mqtt", Image: image, Enabled: true, DockerID: id, Running: true, Network: network,
				Ports: []HeadPort{{Name: "mqtt", ContainerPort: 1883, Protocol: "tcp"}}}

			h := newFakeHost(t)
			h.dc = dc
			sp := secretPw
			h.ectx.Secret["mqtt-password"] = &sp
			h.secrets = append(h.secrets, secretPw) // the site's secret values (not the static value)
			none := model.DevToolCredential{Kind: "none"}
			lit := func(v string) model.DevToolCredential { return model.DevToolCredential{Kind: "literal", Value: &v} }
			opts := model.DevToolOptions{DefaultHeaders: []model.DevToolHeader{}}
			target := model.DevToolTarget{Kind: "container", ContainerSlug: strp("mqtt"), PortName: strp("mqtt")}
			mk := func(slug, user string, pass model.DevToolCredential) model.DevToolPreset {
				return model.DevToolPreset{PresetSlug: slug, Name: slug, Kind: "mqtt", Target: target, Options: opts,
					Auth: model.DevToolAuth{Username: strp(user), Password: pass, HTTPScheme: "none", Token: none}}
			}
			h.head = &Head{Seq: 3, RevID: "R3", Containers: []HeadContainer{mq}, Secrets: []string{"mqtt-password"}, Presets: []model.DevToolPreset{
				mk("mqtt-secret", "northwind", model.DevToolCredential{Kind: "expr", Expr: strp("{{ secret.mqtt-password }}")}),
				mk("mqtt-value", "ops", lit(staticPw)),
				mk("mqtt-wrong", "northwind", lit("not-the-password")),
				mk("mqtt-anon", "", none),
			}}
			s := New(h)
			srv := testServer(t, s)
			c := &client2{t: t, base: srv.URL, session: "S1", actor: "ops@acme.example"}
			test := func(id string) TestConnectionResponse {
				s.rl.mu.Lock()
				s.rl.m = map[string][]time.Time{}
				s.rl.mu.Unlock()
				var tr TestConnectionResponse
				c.do("POST", "/connections/"+id+"/test", nil, &tr)
				return tr
			}
			deadline := time.Now().Add(30 * time.Second)
			tr := test("p:mqtt-secret")
			for !tr.OK && time.Now().Before(deadline) {
				time.Sleep(time.Second)
				tr = test("p:mqtt-secret")
			}
			if !tr.OK {
				logs, _ := exec.Command("docker", "logs", id).CombinedOutput()
				t.Fatalf("secret preset refused: %s\n%s", tr.Message, logs)
			}
			t.Logf("p:mqtt-secret (northwind, {{ secret.mqtt-password }}) connected via %s", model.Str(tr.ResolvedTarget))
			if tr := test("p:mqtt-value"); !tr.OK {
				t.Fatalf("static value preset refused: %s", tr.Message)
			}
			if tr := test("p:mqtt-wrong"); tr.OK {
				t.Fatal("a wrong password must be refused by the broker")
			} else {
				t.Logf("p:mqtt-wrong refused: %s", tr.Message)
			}
			if tr := test("p:mqtt-anon"); tr.OK {
				t.Fatal("allow_anonymous false: an anonymous client must be refused")
			}
			// publish through the static-value preset; the topic carries the value → redacted in the activity
			var pr MqttPublishResponse
			if st, _, raw := c.do("POST", "/connections/p:mqtt-value/mqtt/publish", MqttPublishRequest{Topic: "t/" + staticPw, Payload: "hello", Encoding: "utf8", QoS: 1}, &pr); st != 200 {
				t.Fatalf("publish %d %s", st, raw)
			}
			// reads: MASK; reveal: the value
			var list model.ListResponse[Connection]
			_, _, raw := c.do("GET", "/connections", nil, &list)
			mustContainNone(t, "connections list", string(raw), staticPw, secretPw)
			for _, x := range list.Items {
				if x.ID == "p:mqtt-value" && (x.Auth.Password.Kind != "literal" || model.Str(x.Auth.Password.Value) != model.Mask) {
					t.Fatalf("preset static value not masked in reads: %+v", x.Auth.Password)
				}
			}
			var rv RevealResponse
			if st, hdr, raw := c.do("POST", "/connections/p:mqtt-value/reveal?field=password", nil, &rv); st != 200 || rv.Value != staticPw || hdr.Get("Cache-Control") != "no-store" {
				t.Fatalf("reveal %d %s %q", st, raw, hdr.Get("Cache-Control"))
			}
			// duplicate copies the static value into a manual connection that connects too
			var dup Connection
			if st, _, raw := c.do("POST", "/connections/p:mqtt-value/duplicate", map[string]any{}, &dup); st != 201 {
				t.Fatalf("duplicate %d %s", st, raw)
			}
			if tr := test(dup.ID); !tr.OK {
				t.Fatalf("duplicated static value refused: %s", tr.Message)
			}
			// activities: never the static value, never the secret
			for _, a := range h.activities("devtools.") {
				mustContainNone(t, a[0], a[1], staticPw, secretPw)
			}
			pub := h.activities("devtools.mqtt-published")
			if len(pub) != 1 || !strings.Contains(pub[0][1], Redacted) {
				t.Fatalf("publish activity %v", pub)
			}
			var data map[string]any
			_ = json.Unmarshal([]byte(pub[0][1]), &data)
			t.Logf("publish activity data: %v", data)
		})
	}
}
