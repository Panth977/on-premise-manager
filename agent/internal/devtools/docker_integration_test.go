package devtools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"opm/agent/internal/docker"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
)

// TestDockerDevTools runs every driver against real services on Docker (Colima on the Mac): postgres:17, redis:7.4,
// eclipse-mosquitto:2, go-httpbin and cassandra:5.0.3 (slow start). The containers are reached like OPM containers
// (HeadContainer → docker inspect → container IP, else the published loopback port), credentials are presets with
// {{ secret.x }} resolved on the "site". Skipped with -short or without Docker.
func TestDockerDevTools(t *testing.T) {
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
	tag := fmt.Sprintf("opmdt%d", time.Now().UnixNano()%1e8)
	network := tag + "-net"
	run("network", "create", "--label", "opm.devtools.test="+tag, network)
	t.Cleanup(func() {
		ids, _ := exec.Command("docker", "ps", "-aq", "--filter", "label=opm.devtools.test="+tag).Output()
		if f := strings.Fields(string(ids)); len(f) > 0 {
			_ = exec.Command("docker", append([]string{"rm", "-f"}, f...)...).Run()
		}
		_ = exec.Command("docker", "network", "rm", network).Run()
	})
	start := func(name string, port int, image string, env []string, cmd ...string) HeadContainer {
		args := []string{"run", "-d", "--name", tag + "-" + name, "--label", "opm.devtools.test=" + tag, "--network", network, "--network-alias", name,
			"-p", fmt.Sprintf("127.0.0.1::%d", port)}
		for _, e := range env {
			args = append(args, "-e", e)
		}
		args = append(append(args, image), cmd...)
		id := run(args...)
		return HeadContainer{ContainerSlug: name, ServiceSlug: name, Image: image, Enabled: true, DockerID: id, Running: true, Network: network,
			Ports: []HeadPort{{Name: "main", ContainerPort: port, Protocol: "tcp"}}}
	}
	const pgPass = "pg-Pa55-integration"
	pgSecret := "pg-pass"
	const redisPass = "r3dis-Pass-integration"
	pg := start("postgres", 5432, "postgres:17-alpine", []string{"POSTGRES_PASSWORD=" + pgPass})
	rd := start("redis", 6379, "redis:7.4-alpine", nil, "redis-server", "--requirepass", redisPass)
	mq := start("mqtt", 1883, "eclipse-mosquitto:2.0.14", nil, "mosquitto", "-c", "/mosquitto-no-auth.conf")
	hb := start("httpbin", 8080, "mccutchen/go-httpbin:v2.15.0", nil)
	cq := start("cassandra", 9042, "cassandra:5.0.3", []string{"MAX_HEAP_SIZE=512M", "HEAP_NEWSIZE=128M", "CASSANDRA_NUM_TOKENS=4"})

	h := newFakeHost(t)
	h.dc = dc
	pp := pgPass
	rp := redisPass
	h.ectx.Secret["pg-pass"] = &pp
	h.ectx.Secret["redis-pass"] = &rp
	h.secrets = append(h.secrets, pgPass, redisPass)
	// §21 #99: containers.<c>.env.<NAME> from the real resolution path (validate.SiteDevtoolExprContext of a head config
	// whose postgres container sets POSTGRES_USER as a plain value and POSTGRES_PASSWORD from the secret pg-pass)
	envCfg := &model.Config{
		Secrets: []model.SecretDef{{SecretSlug: "pg-pass", Value: &pp}},
		Sites: []model.Site{{SiteSlug: "app", Config: model.SiteConfig{Containers: []model.Container{{
			ContainerSlug: "postgres", ServiceSlug: "postgres", Version: "17", Enabled: true,
			Env: []model.ContainerEnv{{Name: "POSTGRES_USER", Value: strp("postgres")}, {Name: "POSTGRES_PASSWORD", SecretRef: &pgSecret}},
		}}}}},
	}
	h.ectx.Containers = validate.SiteDevtoolExprContext(envCfg, nil, nil, "D1", nil, "app", nil).Containers
	if e := h.ectx.Containers["postgres"].Env["POSTGRES_PASSWORD"]; !e.Secret || e.Value == nil || *e.Value != pgPass {
		t.Fatalf("containers.postgres.env.POSTGRES_PASSWORD = %+v", e)
	}
	none := model.DevToolCredential{Kind: "none"}
	expr := func(s string) model.DevToolCredential { return model.DevToolCredential{Kind: "expr", Expr: strp(s)} }
	target := func(c string) model.DevToolTarget {
		return model.DevToolTarget{Kind: "container", ContainerSlug: strp(c), PortName: strp("main")}
	}
	opts := model.DevToolOptions{DefaultHeaders: []model.DevToolHeader{}}
	presets := []model.DevToolPreset{
		{PresetSlug: "pg", Name: "Main DB", Kind: "postgres", Target: target("postgres"), Options: opts,
			Auth: model.DevToolAuth{Username: strp("postgres"), Password: expr("{{ secret.pg-pass }}"), HTTPScheme: "none", Token: none}},
		// §21 #99: user + password from the postgres container's env (the user's case)
		{PresetSlug: "pg-env", Name: "Main DB (container env)", Kind: "postgres", Target: target("postgres"), Options: opts,
			Auth: model.DevToolAuth{Username: strp("{{ containers.postgres.env.POSTGRES_USER }}"), Password: expr("{{ containers.postgres.env.POSTGRES_PASSWORD }}"), HTTPScheme: "none", Token: none}},
		{PresetSlug: "pg-ro", Name: "Main DB (ro)", Kind: "postgres", Target: target("postgres"), Options: opts, ReadOnly: true,
			Auth: model.DevToolAuth{Username: strp("postgres"), Password: expr("{{ secret.pg-pass }}"), HTTPScheme: "none", Token: none}},
		{PresetSlug: "cache", Name: "Cache", Kind: "redis", Target: target("redis"), Options: opts,
			Auth: model.DevToolAuth{Password: expr("{{ secret.redis-pass }}"), Database: strp("0"), HTTPScheme: "none", Token: none}},
		{PresetSlug: "mqtt", Name: "Broker", Kind: "mqtt", Target: target("mqtt"), Options: opts, Auth: model.DevToolAuth{Password: none, HTTPScheme: "none", Token: none}},
		{PresetSlug: "web", Name: "httpbin", Kind: "http", Target: target("httpbin"), Options: opts, Auth: model.DevToolAuth{Password: none, HTTPScheme: "none", Token: none}},
		{PresetSlug: "cql", Name: "Cassandra", Kind: "cassandra", Target: target("cassandra"), Options: opts, Auth: model.DevToolAuth{Password: none, HTTPScheme: "none", Token: none}},
		{PresetSlug: "cql-ro", Name: "Cassandra ro", Kind: "cassandra", Target: target("cassandra"), Options: opts, ReadOnly: true, Auth: model.DevToolAuth{Password: none, HTTPScheme: "none", Token: none}},
	}
	h.head = &Head{Seq: 7, RevID: "R7", Presets: presets, Containers: []HeadContainer{pg, rd, mq, hb, cq}, Secrets: []string{"pg-pass", "redis-pass"}}
	s := New(h)
	srv := testServer(t, s)
	c := &client2{t: t, base: srv.URL, session: "S1", actor: "ops@acme.example"}
	waitOK := func(id string, max time.Duration) {
		t.Helper()
		deadline := time.Now().Add(max)
		var tr TestConnectionResponse
		for time.Now().Before(deadline) {
			s.rl.mu.Lock()
			s.rl.m = map[string][]time.Time{}
			s.rl.mu.Unlock()
			c.do("POST", "/connections/"+id+"/test", nil, &tr)
			if tr.OK {
				t.Logf("%s: %s via %s", id, model.Str(tr.ServerVersion), model.Str(tr.ResolvedTarget))
				return
			}
			time.Sleep(2 * time.Second)
		}
		t.Fatalf("%s never answered: %s", id, tr.Message)
	}
	resetRL := func() {
		s.rl.mu.Lock()
		s.rl.m = map[string][]time.Time{}
		s.rl.mu.Unlock()
	}

	t.Run("postgres", func(t *testing.T) {
		waitOK("p:pg", 90*time.Second)
		// §21 #99: the preset whose username + password are {{ containers.postgres.env.* }} connects
		waitOK("p:pg-env", 30*time.Second)
		st, out, raw := execSQL(t, c, "p:pg", map[string]any{"queryId": "pg1", "statement": `CREATE SCHEMA app; CREATE TABLE app.items (id serial PRIMARY KEY, name text NOT NULL, meta jsonb, at timestamptz DEFAULT now());
			INSERT INTO app.items(name, meta) SELECT 'item-' || g, jsonb_build_object('g', g) FROM generate_series(1, 2500) g;
			SELECT id, name, meta FROM app.items ORDER BY id; CREATE ROLE tester LOGIN PASSWORD 'pw-literal-123'`})
		if st != 200 || !out.OK || len(out.Results) != 5 {
			t.Fatalf("pg multi %d %s", st, raw)
		}
		sel := out.Results[3]
		if sel.TotalRows != 2500 || len(sel.Rows) != 1000 || sel.Columns[2].Type != "jsonb" || sel.Columns[2].Display != "json" || *out.Results[2].RowsAffected != 2500 || *out.Results[2].CommandTag != "INSERT 0 2500" {
			t.Fatalf("pg rows %+v %+v", sel.Columns, out.Results[2])
		}
		if m, ok := sel.Rows[0][2].(map[string]any); !ok || m["g"].(float64) != 1 {
			t.Fatalf("jsonb cell %v", sel.Rows[0][2])
		}
		var page ResultPageResponse
		c.do("GET", "/results/"+*sel.ResultID+"?offset=2400&limit=1000", nil, &page)
		if len(page.Rows) != 100 || page.Rows[0][1] != "item-2401" {
			t.Fatalf("page %d", len(page.Rows))
		}
		// params, explain
		_, out, raw = execSQL(t, c, "p:pg", map[string]any{"queryId": "pg2", "statement": "SELECT name FROM app.items WHERE id = $1 AND name LIKE $2", "params": []any{42, "item-%"}})
		if !out.OK || out.Results[0].Rows[0][0] != "item-42" {
			t.Fatalf("params %s", raw)
		}
		_, out, raw = execSQL(t, c, "p:pg", map[string]any{"queryId": "pg3", "statement": "SELECT * FROM app.items WHERE id < 10", "explain": "analyze"})
		if !out.OK || out.Results[0].Type != "explain" || !strings.Contains(out.Results[0].Plan.Text, "actual time") || out.Results[0].Plan.JSON == nil {
			t.Fatalf("explain %s", raw)
		}
		// an error position is relative to the statement as written (the EXPLAIN prefix subtracted)
		_, out, _ = execSQL(t, c, "p:pg", map[string]any{"queryId": "pg3b", "statement": "SELECT nope_col FROM app.items", "explain": "plan"})
		if out.OK || out.Results[0].Error.Position == nil || *out.Results[0].Error.Position != 8 {
			t.Fatalf("explain error position %+v", out.Results[0].Error)
		}
		_, out, _ = execSQL(t, c, "p:pg", map[string]any{"queryId": "pg3c", "statement": "SELECT nope_col FROM app.items"})
		if out.Results[0].Error.Position == nil || *out.Results[0].Error.Position != 8 {
			t.Fatalf("error position %+v", out.Results[0].Error)
		}
		// transaction: rolled back on error
		_, out, _ = execSQL(t, c, "p:pg", map[string]any{"queryId": "pg4", "transaction": true, "statement": "INSERT INTO app.items(name) VALUES ('tx'); SELECT 1/0"})
		if out.OK || out.Results[1].Error == nil || model.Str(out.Results[1].Error.Code) != "22012" {
			t.Fatalf("tx %+v", out.Results)
		}
		_, out, _ = execSQL(t, c, "p:pg", map[string]any{"queryId": "pg5", "statement": "SELECT count(*) FROM app.items WHERE name = 'tx'"})
		if out.Results[0].Rows[0][0].(float64) != 0 {
			t.Fatal("transaction not rolled back")
		}
		// read-only preset: the server refuses writes (25006); tx control refused
		waitOK("p:pg-ro", 10*time.Second)
		_, out, _ = execSQL(t, c, "p:pg-ro", map[string]any{"queryId": "pg6", "statement": "INSERT INTO app.items(name) VALUES ('ro')"})
		if out.OK || model.Str(out.Results[0].Error.Code) != "25006" {
			t.Fatalf("read-only %+v", out.Results[0].Error)
		}
		_, out, _ = execSQL(t, c, "p:pg-ro", map[string]any{"queryId": "pg7", "statement": "COMMIT; INSERT INTO app.items(name) VALUES ('ro')"})
		if out.OK {
			t.Fatal("COMMIT must not escape the read-only transaction")
		}
		// destructive: confirm with the connection name
		if st, _, _ := c.do("POST", "/connections/p:pg/sql", map[string]any{"queryId": "pg8", "statement": "DROP TABLE app.items"}, nil); st != 422 {
			t.Fatalf("drop without confirm %d", st)
		}
		// statement timeout
		_, out, _ = execSQL(t, c, "p:pg", map[string]any{"queryId": "pg9", "statement": "SELECT pg_sleep(5)", "timeoutSec": 1})
		if out.OK || out.Results[0].Error == nil {
			t.Fatalf("timeout %+v", out.Results)
		}
		// cancel a running pg_sleep
		var wg sync.WaitGroup
		wg.Add(1)
		var cancelled ExecuteResponse
		go func() {
			defer wg.Done()
			_, cancelled, _ = execSQL(t, c, "p:pg", map[string]any{"queryId": "pg-sleep", "statement": "SELECT pg_sleep(30)"})
		}()
		var cr map[string]bool
		for i := 0; i < 50; i++ {
			time.Sleep(100 * time.Millisecond)
			c.do("POST", "/connections/p:pg/cancel", map[string]string{"queryId": "pg-sleep"}, &cr)
			if cr["cancelled"] {
				break
			}
		}
		wg.Wait()
		if !cr["cancelled"] || cancelled.OK || cancelled.Results[0].Error == nil || cancelled.DurationMs > 20000 {
			t.Fatalf("cancel %v %+v", cr, cancelled.Results)
		}
		if a := h.activities("devtools.query-cancelled"); len(a) == 0 || !strings.Contains(a[len(a)-1][1], `"wasRunning":true`) {
			t.Fatalf("cancel activity %v", a)
		}
		// reconnect once after the server dropped the pooled connections
		execSQL(t, c, "p:pg-ro", map[string]any{"queryId": "pg10", "statement": "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = 'opm-devtools' AND pid <> pg_backend_pid()"})
		_, out, raw = execSQL(t, c, "p:pg", map[string]any{"queryId": "pg11", "statement": "SELECT 1 AS one"})
		if !out.OK {
			t.Fatalf("reconnect %s", raw)
		}
		// schema + table detail
		var sch SchemaResponse
		c.do("GET", "/connections/p:pg/schema?refresh=1", nil, &sch)
		found := false
		for _, ns := range sch.Namespaces {
			if ns.Name == "app" {
				for _, tb := range ns.Tables {
					found = found || (tb.Name == "items" && len(tb.Columns) == 4 && tb.Columns[0].PrimaryKey && tb.SizeBytes != nil)
				}
			}
		}
		if !found || *sch.Database != "postgres" {
			t.Fatalf("schema %+v", sch.Namespaces)
		}
		var td TableDetailResponse
		c.do("GET", "/connections/p:pg/schema/table?namespace=app&table=items", nil, &td)
		if td.DDL == nil || !strings.Contains(*td.DDL, "CREATE TABLE app.items") || len(td.Indexes) != 1 || !td.Indexes[0].Primary {
			t.Fatalf("table detail %+v", td)
		}
		// activities: redacted, no secrets, no result data
		for _, a := range h.activities("devtools.query") {
			mustContainNone(t, "pg activity", a[1], pgPass, "pw-literal-123", "item-2401", `"g":`)
		}
		if a := h.activities("devtools.query"); !strings.Contains(a[0][1], "PASSWORD '"+Redacted+"'") {
			t.Fatalf("role password not redacted: %s", a[0][1])
		}
		// §21 #99: run a statement on the preset whose password is {{ containers.postgres.env.POSTGRES_PASSWORD }}
		if st, out, raw := execSQL(t, c, "p:pg-env", map[string]any{"queryId": "pgenv", "statement": "SELECT current_user"}); st != 200 || !out.OK || len(out.Results) != 1 || len(out.Results[0].Rows) != 1 || out.Results[0].Rows[0][0] != "postgres" {
			t.Fatalf("pg-env %d %s", st, raw)
		}
		for _, a := range h.activities("devtools.query") {
			mustContainNone(t, "pg-env activity", a[1], pgPass)
		}
	})

	t.Run("redis", func(t *testing.T) {
		resetRL()
		waitOK("p:cache", 60*time.Second)
		redis := func(line, confirm string) (int, ExecuteRedisResponse) {
			var out ExecuteRedisResponse
			st, _, _ := c.do("POST", "/connections/p:cache/redis", map[string]any{"queryId": "r" + line, "line": line, "confirm": confirm}, &out)
			return st, out
		}
		for _, l := range []string{"SET user:1 alice", "HSET h a 1 b 2", "RPUSH l x y z", "ZADD z 1 one 2 two", "SADD s m1 m2", "XADD st * k v", "EXPIRE user:1 300"} {
			if st, out := redis(l, ""); st != 200 || !out.OK {
				t.Fatalf("%s → %d %+v", l, st, out)
			}
		}
		if _, out := redis("GET user:1", ""); out.Reply.Type != "string" || out.Reply.Value != "alice" {
			t.Fatalf("GET %+v", out.Reply)
		}
		if _, out := redis("HGETALL h", ""); out.Reply.Type != "map" {
			t.Fatalf("HGETALL (RESP3 map) %+v", out.Reply)
		}
		if _, out := redis("AUTH "+redisPass, ""); !out.OK || strings.Contains(out.Command, redisPass) {
			t.Fatalf("AUTH %+v", out)
		}
		if st, _ := redis("FLUSHDB", ""); st != 422 {
			t.Fatalf("FLUSHDB without confirm %d", st)
		}
		var scan RedisScanResponse
		c.do("GET", "/connections/p:cache/redis/keys?pattern=*&count=100", nil, &scan)
		if len(scan.Keys) < 6 || scan.DBSize < 6 {
			t.Fatalf("scan %+v", scan)
		}
		for _, k := range scan.Keys {
			if k.Key == "user:1" && (k.Type != "string" || k.TTLSec <= 0 || k.Length == nil || *k.Length != 5 || k.SizeBytes == nil) {
				t.Fatalf("key row %+v", k)
			}
		}
		var kv RedisKeyValueResponse
		c.do("GET", "/connections/p:cache/redis/key?key=l&count=2", nil, &kv)
		if kv.Type != "list" || kv.Next == nil || *kv.Length != 3 {
			t.Fatalf("key value %+v", kv)
		}
		var info RedisInfoResponse
		c.do("GET", "/connections/p:cache/redis/info", nil, &info)
		if info.ServerVersion == nil || !strings.HasPrefix(*info.ServerVersion, "Redis 7.4") || len(info.DBs) == 0 || info.Sections["server"]["redis_version"] == "" {
			t.Fatalf("info %+v", info.ServerVersion)
		}
		if st, out := redis("FLUSHDB", "Cache"); st != 200 || !out.OK {
			t.Fatalf("FLUSHDB confirmed %d", st)
		}
		for _, a := range h.activities("devtools.query") {
			mustContainNone(t, "redis activity", a[1], redisPass) // the commands themselves are logged (SET user:1 alice), replies never
		}
	})

	t.Run("mqtt", func(t *testing.T) {
		resetRL()
		waitOK("p:mqtt", 30*time.Second)
		var sess MqttSessionResponse
		if st, _, raw := c.do("POST", "/connections/p:mqtt/mqtt/sessions", MqttSessionRequest{Subscriptions: []MqttSubscription{{Filter: "plant/#", QoS: 1}}}, &sess); st != 201 {
			t.Fatalf("session %d %s", st, raw)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		r, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+strings.TrimPrefix(sess.StreamPath, "/_opm/api/sites/app"), nil)
		r.Header.Set("X-Session", "S1")
		resp, err := http.DefaultClient.Do(r)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("stream %v %v", err, resp)
		}
		// the ticket is single-use
		r2, _ := http.NewRequest("GET", srv.URL+strings.TrimPrefix(sess.StreamPath, "/_opm/api/sites/app"), nil)
		r2.Header.Set("X-Session", "S1")
		if resp2, _ := http.DefaultClient.Do(r2); resp2.StatusCode != 401 {
			t.Fatalf("ticket reuse %d", resp2.StatusCode)
		}
		go func() {
			time.Sleep(500 * time.Millisecond)
			var pr MqttPublishResponse
			c.do("POST", "/connections/p:mqtt/mqtt/publish", MqttPublishRequest{Topic: "plant/line1/temp", Payload: `{"t":21.5}`, Encoding: "json", QoS: 1}, &pr)
		}()
		sc := bufio.NewScanner(resp.Body)
		var msg MqttMessage
		ev := ""
		for sc.Scan() {
			l := sc.Text()
			if strings.HasPrefix(l, "event: ") {
				ev = strings.TrimPrefix(l, "event: ")
			}
			if ev == "message" && strings.HasPrefix(l, "data: ") {
				_ = json.Unmarshal([]byte(strings.TrimPrefix(l, "data: ")), &msg)
				break
			}
		}
		resp.Body.Close()
		if msg.Topic != "plant/line1/temp" || msg.Text == nil || *msg.Text != `{"t":21.5}` {
			t.Fatalf("message %+v", msg)
		}
		var topics model.ListResponse[MqttTopicSeen]
		c.do("GET", "/connections/p:mqtt/mqtt/topics", nil, &topics)
		if len(topics.Items) != 1 || topics.Items[0].Messages != 1 {
			t.Fatalf("topics %+v", topics)
		}
		// empty retained payload = clear → confirm
		if st, _, _ := c.do("POST", "/connections/p:mqtt/mqtt/publish", MqttPublishRequest{Topic: "plant/x", Payload: "", Encoding: "utf8", Retain: true}, nil); st != 422 {
			t.Fatalf("clear retained without confirm %d", st)
		}
		waitUntil(t, "unsubscribed", func() bool { return len(h.activities("devtools.mqtt-unsubscribed")) == 1 })
		if len(h.activities("devtools.mqtt-subscribed")) != 1 || len(h.activities("devtools.mqtt-published")) != 1 {
			t.Fatal("mqtt activities")
		}
	})

	t.Run("http", func(t *testing.T) {
		resetRL()
		waitOK("p:web", 30*time.Second)
		var out ExecuteHTTPResponse
		c.do("POST", "/connections/p:web/http", map[string]any{"queryId": "w1", "request": map[string]any{"method": "POST", "url": "/anything?pg={{ secret.pg-pass }}",
			"headers": []map[string]any{{"name": "X-Pw", "value": "{{ secret.pg-pass }}", "enabled": true}}, "body": map[string]any{"kind": "json", "text": `{"a":1}`}}}, &out)
		if !out.OK || *out.Status != 200 || out.BodyText == nil || !strings.Contains(*out.BodyText, `"a": 1`) && !strings.Contains(*out.BodyText, `"a":1`) {
			t.Fatalf("anything %+v", out)
		}
		mustContainNone(t, "sent", fmt.Sprint(out.Sent), pgPass)
		c.do("POST", "/connections/p:web/http", map[string]any{"queryId": "w2", "request": map[string]any{"method": "GET", "url": "/redirect/3", "body": map[string]any{"kind": "none"}}}, &out)
		if len(out.Redirects) != 3 || *out.Status != 200 {
			t.Fatalf("redirects %+v", out.Redirects)
		}
		c.do("POST", "/connections/p:web/http", map[string]any{"queryId": "w3", "request": map[string]any{"method": "GET", "url": "/status/418", "body": map[string]any{"kind": "none"}}}, &out)
		if !out.OK || *out.Status != 418 {
			t.Fatalf("418 %+v", out.Status)
		}
		for _, a := range h.activities("devtools.query") {
			mustContainNone(t, "http activity", a[1], pgPass)
		}
	})

	t.Run("cassandra", func(t *testing.T) {
		resetRL()
		waitOK("p:cql", 5*time.Minute)
		_, out, raw := execSQL(t, c, "p:cql", map[string]any{"queryId": "c1", "statement": `CREATE KEYSPACE IF NOT EXISTS plant WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1};
			CREATE TABLE IF NOT EXISTS plant.readings (sensor text, at timestamp, value double, tags map<text,text>, PRIMARY KEY (sensor, at));
			INSERT INTO plant.readings (sensor, at, value, tags) VALUES ('s1', '2026-10-08T10:00:00Z', 21.5, {'unit': 'C'});
			INSERT INTO plant.readings (sensor, at, value) VALUES ('s1', '2026-10-08T10:01:00Z', 22.0);
			SELECT sensor, at, value, tags FROM plant.readings WHERE sensor = 's1'`})
		if !out.OK || out.Results[4].TotalRows != 2 || out.Results[4].Columns[3].Display != "json" {
			t.Fatalf("cql %s", raw)
		}
		_, out, raw = execSQL(t, c, "p:cql", map[string]any{"queryId": "c2", "statement": "SELECT value FROM plant.readings WHERE sensor = ?", "params": []any{"s1"}, "consistency": "ONE"})
		if !out.OK || out.Results[0].TotalRows != 2 {
			t.Fatalf("cql params %s", raw)
		}
		if st, _, _ := c.do("POST", "/connections/p:cql/sql", map[string]any{"queryId": "c3", "statement": "SELECT 1 FROM system.local", "explain": "plan"}, nil); st != 422 {
			t.Fatalf("cql explain %d", st)
		}
		if st, _, _ := c.do("POST", "/connections/p:cql-ro/sql", map[string]any{"queryId": "c4", "statement": "INSERT INTO plant.readings (sensor, at, value) VALUES ('x', '2026-10-08T10:00:00Z', 1)"}, nil); st != 403 {
			t.Fatalf("cql read-only %d", st)
		}
		if st, _, _ := c.do("POST", "/connections/p:cql/sql", map[string]any{"queryId": "c5", "statement": "DROP KEYSPACE plant"}, nil); st != 422 {
			t.Fatalf("cql drop without confirm %d", st)
		}
		var sch SchemaResponse
		c.do("GET", "/connections/p:cql/schema", nil, &sch)
		ok := false
		for _, ns := range sch.Namespaces {
			if ns.Name == "plant" && len(ns.Tables) == 1 && len(ns.Tables[0].Columns) == 4 && *ns.Tables[0].Columns[0].Role == "partition_key" {
				ok = true
			}
		}
		if !ok {
			t.Fatalf("cql schema %+v", sch.Namespaces)
		}
		var td TableDetailResponse
		c.do("GET", "/connections/p:cql/schema/table?namespace=plant&table=readings", nil, &td)
		if td.DDL == nil || !strings.Contains(*td.DDL, "PRIMARY KEY ((sensor), at)") {
			t.Fatalf("cql ddl %v", td.DDL)
		}
	})

	// every connection shows its remembered client
	var list model.ListResponse[Connection]
	c.do("GET", "/connections", nil, &list)
	for _, cn := range list.Items {
		if cn.ID == "p:pg" && (cn.State.Status != "connected" || cn.State.ServerVersion == nil || cn.ResolvedTarget == nil) {
			t.Fatalf("pg state %+v", cn.State)
		}
	}
	if r := (&client2{t: t, base: srv.URL, session: "S1", actor: "ops@acme.example"}); true {
		var d map[string]bool
		r.do("POST", "/connections/p:pg/disconnect", nil, &d)
		if !d["closed"] {
			t.Fatal("disconnect")
		}
	}
}

func waitUntil(t *testing.T, what string, fn func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if fn() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}
