// Command driver is the HTTP client of the upgrade smoke test (agent/test/upgrade-smoke/run.sh): it logs in to a
// local agent, declares an actor, starts an upgrade and checks the session afterwards. The session (cookie + actor
// token) is kept in a file so the same session is reused after the agent restarted.
//
//	driver login   --base URL --site S --key KEY --session FILE
//	driver upgrade --base URL --site S --session FILE --version V
//	driver info    --base URL --site S --session FILE          (prints version / pid / startedAt / .prev; checks the actor)
//	driver wait    --base URL --version V --timeout SEC         (until /_opm/api/health is ok with version V)
//	driver acts    --base URL --site S --session FILE           (agent.* activities)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type session struct {
	Cookie string `json:"cookie"`
	Actor  string `json:"actor"`
}

var (
	base, site, sessFile, key, version string
	timeout                            int
)

func call(method, path string, body any, s *session, out any) int {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, base+path, rd)
	if s != nil {
		if s.Cookie != "" {
			req.Header.Set("Cookie", "opm_session_"+site+"="+s.Cookie)
		}
		if s.Actor != "" {
			req.Header.Set("Authorization", "Actor "+s.Actor)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, method, path, err)
		return 0
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil {
		_ = json.Unmarshal(b, out)
	}
	if resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "%s %s → %d %s\n", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if method == "POST" && path == "/_opm/api/login" {
		for _, c := range resp.Cookies() {
			if c.Name == "opm_session_"+site {
				if m, ok := out.(*map[string]any); ok {
					(*m)["cookie"] = c.Value
				}
			}
		}
	}
	return resp.StatusCode
}

func load() *session {
	var s session
	b, err := os.ReadFile(sessFile)
	if err != nil {
		fail("no session file: " + err.Error())
	}
	_ = json.Unmarshal(b, &s)
	return &s
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "FAIL:", msg)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		fail("usage: driver login|upgrade|info|wait|acts …")
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.StringVar(&base, "base", "http://127.0.0.1:7080", "")
	fs.StringVar(&site, "site", "app", "")
	fs.StringVar(&sessFile, "session", "/tmp/smoke-session.json", "")
	fs.StringVar(&key, "key", "ABCDEFGHIJKLMNOPQRSTUVWX", "")
	fs.StringVar(&version, "version", "", "")
	fs.IntVar(&timeout, "timeout", 120, "")
	_ = fs.Parse(os.Args[2:])
	switch cmd {
	case "login":
		var out map[string]any = map[string]any{}
		if st := call("POST", "/_opm/api/login", map[string]string{"accessKey": key}, nil, &out); st != 200 {
			fail(fmt.Sprintf("login %d", st))
		}
		s := &session{Cookie: fmt.Sprint(out["cookie"])}
		var ar map[string]any
		if st := call("POST", "/_opm/api/actor", map[string]string{"email": "smoke@acme.example"}, s, &ar); st != 200 {
			fail(fmt.Sprintf("actor %d", st))
		}
		s.Actor = fmt.Sprint(ar["actorToken"])
		b, _ := json.Marshal(s)
		_ = os.WriteFile(sessFile, b, 0o600)
		fmt.Println("logged in; session", s.Cookie[:8]+"…", "actor smoke@acme.example")
	case "upgrade":
		var p map[string]any
		st := call("POST", "/_opm/api/sites/"+site+"/agent/upgrade", map[string]string{"version": version, "confirm": site}, load(), &p)
		if st != 202 {
			fail(fmt.Sprintf("upgrade %d", st))
		}
		fmt.Printf("upgrade started: %v → %v (upgradeId %v)\n", p["from"], p["to"], p["upgradeId"])
	case "info":
		s := load()
		var info struct {
			Version        string         `json:"version"`
			PID            int            `json:"pid"`
			StartedAt      string         `json:"startedAt"`
			PreviousBinary map[string]any `json:"previousBinary"`
			Upgrade        map[string]any `json:"upgrade"`
			Supervisor     map[string]any `json:"supervisor"`
			Build          map[string]any `json:"build"`
		}
		if st := call("GET", "/_opm/api/sites/"+site+"/agent", nil, s, &info); st != 200 {
			fail(fmt.Sprintf("info with the old session: %d", st))
		}
		var e map[string]any
		if st := call("POST", "/_opm/api/sites/"+site+"/agent/restart", map[string]string{"confirm": "not-" + site}, s, &e); st != 422 {
			fail(fmt.Sprintf("actor token check: expected 422 (actor accepted, confirm wrong), got %d", st))
		}
		phase, failed := "", ""
		if info.Upgrade != nil {
			phase = fmt.Sprint(info.Upgrade["phase"])
			if info.Upgrade["failedPhase"] != nil {
				failed = fmt.Sprint(info.Upgrade["failedPhase"])
			}
		}
		fmt.Printf("session ok + actor ok | version %s pid %d startedAt %s | prev %v (%v) | upgrade phase %s failedPhase %s | supervisor %v guard %v | commit %v builtAt %v\n",
			info.Version, info.PID, info.StartedAt, info.PreviousBinary["version"], info.PreviousBinary["exists"], phase, failed,
			info.Supervisor["kind"], info.Supervisor["restartGuard"], info.Build["commit"], info.Build["builtAt"])
	case "wait":
		until := time.Now().Add(time.Duration(timeout) * time.Second)
		start := time.Now()
		last := ""
		for time.Now().Before(until) {
			var h map[string]any
			resp, err := http.Get(base + "/_opm/api/health")
			if err == nil {
				_ = json.NewDecoder(resp.Body).Decode(&h)
				resp.Body.Close()
				v := fmt.Sprint(h["version"])
				if v != last {
					fmt.Printf("  +%4.1fs health ok=%v version=%s socketActivated=%v\n", time.Since(start).Seconds(), h["ok"], v, h["socketActivated"])
					last = v
				}
				if h["ok"] == true && v == version {
					fmt.Printf("healthy %s after %.1fs\n", version, time.Since(start).Seconds())
					return
				}
			} else if last != "down" {
				fmt.Printf("  +%4.1fs agent down (%v)\n", time.Since(start).Seconds(), err)
				last = "down"
			}
			time.Sleep(500 * time.Millisecond)
		}
		fail("timeout waiting for version " + version)
	case "acts":
		var pr struct {
			Items []struct {
				Ts, Type, Summary string
				Actor             *string
			} `json:"items"`
		}
		call("GET", "/_opm/api/sites/"+site+"/activities?type=agent&limit=50", nil, load(), &pr)
		for i := len(pr.Items) - 1; i >= 0; i-- {
			a := pr.Items[i]
			act := ""
			if a.Actor != nil {
				act = *a.Actor
			}
			fmt.Printf("  %s %-32s %-22s %s\n", a.Ts, a.Type, act, a.Summary)
		}
	case "http": // driver http METHOD URL → prints the body
		req, _ := http.NewRequest(fs.Arg(0), fs.Arg(1), nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fail(err.Error())
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		fmt.Print(string(b))
		if resp.StatusCode >= 300 {
			os.Exit(1)
		}
	case "wait-phase": // until GET …/agent/upgrade current.phase = --version (a phase name); prints the time
		s := load()
		until := time.Now().Add(time.Duration(timeout) * time.Second)
		start := time.Now()
		last := ""
		for time.Now().Before(until) {
			var us struct {
				Current map[string]any `json:"current"`
			}
			if call("GET", "/_opm/api/sites/"+site+"/agent/upgrade", nil, s, &us) == 200 && us.Current != nil {
				ph := fmt.Sprint(us.Current["phase"])
				if ph != last {
					fmt.Printf("  +%4.1fs phase %s %v\n", time.Since(start).Seconds(), ph, us.Current["error"])
					last = ph
				}
				if ph == version {
					return
				}
			}
			time.Sleep(time.Second)
		}
		fail("timeout waiting for phase " + version)
	case "hammer": // driver hammer --seconds N URL… : GETs in 4 loops per URL; counts failures + max latency
		urls := fs.Args()
		type res struct {
			n, fail  int
			max      time.Duration
			firstErr string
		}
		out := make([]res, len(urls))
		done := make(chan struct{})
		cl := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		cl.Transport = &http.Transport{DisableKeepAlives: true}
		var mu = make(chan struct{}, 1)
		mu <- struct{}{}
		for i, u := range urls {
			for k := 0; k < 4; k++ {
				go func(i int, u string) {
					for {
						select {
						case <-done:
							return
						default:
						}
						t0 := time.Now()
						resp, err := cl.Get(u)
						d := time.Since(t0)
						<-mu
						out[i].n++
						if err != nil || resp.StatusCode >= 500 {
							out[i].fail++
							if out[i].firstErr == "" {
								if err != nil {
									out[i].firstErr = err.Error()
								} else {
									out[i].firstErr = resp.Status
								}
							}
						}
						if d > out[i].max {
							out[i].max = d
						}
						mu <- struct{}{}
						if resp != nil {
							_, _ = io.Copy(io.Discard, resp.Body)
							resp.Body.Close()
						}
						time.Sleep(20 * time.Millisecond)
					}
				}(i, u)
			}
		}
		time.Sleep(time.Duration(timeout) * time.Second)
		close(done)
		time.Sleep(200 * time.Millisecond)
		<-mu
		for i, u := range urls {
			fmt.Printf("hammer %-32s requests %5d failed %4d max latency %6.0f ms %s\n", u, out[i].n, out[i].fail, float64(out[i].max.Milliseconds()), out[i].firstErr)
		}
	default:
		fail("unknown command " + cmd)
	}
}
