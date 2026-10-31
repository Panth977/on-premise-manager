package apitest

import (
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// wfConn is a client of a public tunnel port: data() what arrived so far, closed ends when the socket closed.
type wfConn struct {
	c      *net.TCPConn
	mu     sync.Mutex
	buf    strings.Builder
	closed chan struct{}
}

func (w *wfConn) data() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func wfDial(port int) (*wfConn, error) {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+Itoa(port), 5*time.Second)
	if err != nil {
		return nil, err
	}
	w := &wfConn{c: c.(*net.TCPConn), closed: make(chan struct{})}
	go func() {
		defer close(w.closed)
		b := make([]byte, 4096)
		for {
			n, err := w.c.Read(b)
			w.mu.Lock()
			w.buf.Write(b[:n])
			w.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return w, nil
}

func wfConnect(t testing.TB, port int) *wfConn {
	t.Helper()
	w, err := wfDial(port)
	NoErr(t, err)
	t.Cleanup(func() { w.c.Close() })
	return w
}

func wfClosed(t testing.TB, w *wfConn) {
	t.Helper()
	select {
	case <-w.closed:
	case <-time.After(15 * time.Second):
		t.Fatal("the socket did not close")
	}
}

// wfUntil is until(): 5 s.
func wfUntil(t testing.TB, f func() bool) {
	t.Helper()
	for start := time.Now(); !f(); Sleep(20) {
		if time.Since(start) > 5*time.Second {
			t.Fatal("timeout")
		}
	}
}

func wfSend(t testing.TB, key SiteKey, dep, site, connID string, body A) J {
	t.Helper()
	return wfAgentPost(t, key, dep, site, "/v1/agent/tunnels/conn/"+connID+"/send", body)
}

func wfNextPath(q string) string { return "/v1/agent/tunnels/next?" + q }

// wfPoll starts the signed long-poll of a site; wfAwaited is its await.
func wfPoll(key SiteKey, dep, site, path string) <-chan wfResult {
	return wfGo("GET", path, wfSigned(key, dep, site, "GET", path, nil, nil))
}

func wfAwaited(t testing.TB, ch <-chan wfResult) J {
	t.Helper()
	r := wfAwait(t, ch)
	Eq(t, r.Status, 200, r.Text())
	return r.J
}

// wfSortedStrs is array.map(String).sort().
func wfSortedStrs(l []any) []string {
	out := []string{}
	for _, x := range l {
		s, _ := x.(string)
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// Port of backend/test/44-port-tunnels.test.ts.
func TestPortTunnels(t *testing.T) {
	t.Run("§21 #125 port tunnels: add, refuse while off, relay both ways, via a peer, remove", func(t *testing.T) {
		admin := NewPerson(t, "admin")
		id := HelloDeployment(t, admin).ID()
		app := EnrollSite(t, admin, id, "app")
		db := EnrollSite(t, admin, id, "db")
		H := Opt{Headers: map[string]string{"Authorization": admin.Bearer}}
		list := func() J { return Raw(t, "GET", "/v1/deployments/"+id+"/tunnels", H).J }
		from, to, _ := strings.Cut(TunnelPorts, "-")
		first, err := strconv.Atoi(from)
		NoErr(t, err)
		last, err := strconv.Atoi(to)
		NoErr(t, err)

		l0 := Raw(t, "GET", "/v1/deployments/"+id+"/tunnels", H)
		Eq(t, l0.Status, 200, l0.Text())
		r0 := l0.J
		Eq(t, r0.At("available"), true, r0.At("reason").Str())
		Eq(t, wfSortedStrs(r0.At("agents").Pluck("siteSlug")), L{"app", "db"})
		port := r0.At("freePorts.0").Int()
		True(t, port >= first && port <= last)

		// add: bad target / unknown port / then ok / same port again → conflict
		add := func(body A) *Resp {
			return Raw(t, "POST", "/v1/deployments/"+id+"/tunnels", Opt{Headers: map[string]string{"Authorization": admin.Bearer, "Content-Type": "application/json"}, Body: []byte(show(Plain(body)))})
		}
		Eq(t, add(A{"siteSlug": "app", "publicPort": port, "target": A{"kind": "edge", "scheme": "ftp"}}).Status, 422)
		Eq(t, add(A{"siteSlug": "app", "publicPort": 1999, "target": A{"kind": "host", "port": 22}}).Status, 422)
		created := add(A{"siteSlug": "app", "publicPort": port, "target": A{"kind": "host", "port": 5432}, "label": "db"})
		Eq(t, created.Status, 201, created.Text())
		tn := created.J
		tid := tn.At("tunnelId").Str()
		Eq(t, tn.At("listening"), true, tn.At("listenError").Str())
		Eq(t, tn.At("enabledOnSite"), nil)
		Eq(t, add(A{"siteSlug": "db", "publicPort": port, "target": A{"kind": "host", "port": 1}}).Status, 409)
		view := app.API.Call(t, "poll", A{"query": A{"wait": 0}})
		rows := L{}
		for _, x := range view.At("tunnels").Arr() {
			rows = append(rows, L{x.At("tunnelId"), x.At("publicPort")})
		}
		Eq(t, rows, L{L{tid, port}})

		// off on the site (it polls without the id) → the client is closed at once
		wfAgentGet(t, app.Key, id, "app", wfNextPath("wait=0&enabled=&remoteGui=1"))
		off := wfConnect(t, port)
		wfClosed(t, off)
		st := list()
		Match(t, st.At("items.0.lastError").Str(), `switched off`)
		Eq(t, st.At("items.0.enabledOnSite"), false)
		Eq(t, st.At("agents").Find("siteSlug", "app"), A{"siteSlug": "app", "connected": true, "enabledOnSite": true})

		// switched on: the next poll hands out the connection; bytes both ways; the target closes
		pollP := wfPoll(app.Key, id, "app", wfNextPath("wait=10&enabled="+tid+"&remoteGui=1"))
		Sleep(100)
		cli := wfConnect(t, port)
		opens := wfAwaited(t, pollP).At("opens")
		Eq(t, opens.Len(), 1)
		Eq(t, L{opens.At("0.tunnelId"), opens.At("0.siteSlug"), opens.At("0.target")}, L{tid, "app", A{"kind": "host", "port": 5432}})
		cid := opens.At("0.connId").Str()
		_, err = cli.c.Write([]byte("hello target"))
		NoErr(t, err)
		r1 := wfAgentGet(t, app.Key, id, "app", "/v1/agent/tunnels/conn/"+cid+"/recv?wait=5")
		Eq(t, wfUnB64(t, r1.At("dataB64").Str()), "hello target")
		Eq(t, r1.At("closed"), false)
		Eq(t, wfAgentGet(t, db.Key, id, "db", "/v1/agent/tunnels/conn/"+cid+"/recv?wait=0"), A{"dataB64": "", "closed": true}, "another site never gets the bytes")
		Eq(t, wfSend(t, app.Key, id, "app", cid, A{"dataB64": wfB64("hello client")}), A{"accepted": true})
		wfUntil(t, func() bool { return cli.data() == "hello client" })
		st = list()
		Eq(t, st.At("items.0.openConnections"), 1)
		wfSend(t, app.Key, id, "app", cid, A{"dataB64": "", "close": true})
		wfClosed(t, cli)

		// the client closes first → recv reports closed
		pollP2 := wfPoll(app.Key, id, "app", wfNextPath("wait=10&enabled="+tid+"&remoteGui=1"))
		Sleep(100)
		cli2 := wfConnect(t, port)
		cid2 := wfAwaited(t, pollP2).At("opens.0.connId").Str()
		_, err = cli2.c.Write([]byte("bye"))
		NoErr(t, err)
		NoErr(t, cli2.c.CloseWrite())
		got := ""
		for i := 0; i < 5; i++ {
			r := wfAgentGet(t, app.Key, id, "app", "/v1/agent/tunnels/conn/"+cid2+"/recv?wait=2")
			got += wfUnB64(t, r.At("dataB64").Str())
			if r.At("closed").Bool() {
				break
			}
		}
		Eq(t, got, "bye")

		// a tunnel of db (db does not poll) is served through app, which reaches db
		p2 := st.At("freePorts.0").Int()
		viaT := add(A{"siteSlug": "db", "publicPort": p2, "target": A{"kind": "container", "containerSlug": "pg", "portName": "db"}}).J
		wfAgentGet(t, app.Key, id, "app", wfNextPath("wait=0&enabled="+tid+"&remoteGui=1&reach=db"))
		viaP := wfPoll(app.Key, id, "app", wfNextPath("wait=10&enabled="+tid+"&remoteGui=1&reach=db"))
		Sleep(100)
		cli3 := wfConnect(t, p2)
		o3 := wfAwaited(t, viaP).At("opens.0")
		Eq(t, L{o3.At("tunnelId"), o3.At("siteSlug")}, L{viaT.At("tunnelId"), "db"})
		st = list()
		Eq(t, st.At("items").Find("tunnelId", viaT.At("tunnelId")).At("route"), A{"kind": "via", "siteSlug": "app"})
		wfSend(t, app.Key, id, "app", o3.At("connId").Str(), A{"dataB64": "", "close": true, "error": "db: connection refused"})
		wfClosed(t, cli3)

		// remove: no longer listening, the port is free again
		Eq(t, Raw(t, "DELETE", "/v1/deployments/"+id+"/tunnels/"+tid, H).Status, 200)
		_, err = wfDial(port)
		True(t, err != nil, "the port still accepts connections")
		st = list()
		True(t, st.At("freePorts").FindF(func(x J) bool { return x.Int() == port }).Exists())
		Eq(t, st.At("items").Pluck("tunnelId"), L{viaT.At("tunnelId")})
		Eq(t, Raw(t, "DELETE", "/v1/deployments/"+id+"/tunnels/"+viaT.At("tunnelId").Str(), H).Status, 200)
	})
}
