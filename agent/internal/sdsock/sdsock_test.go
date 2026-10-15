package sdsock

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"testing"
)

func TestParseAndListen(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	f, _ := ln.(*net.TCPListener).File()
	ln.Close()
	env := map[string]string{"LISTEN_PID": strconv.Itoa(os.Getpid()), "LISTEN_FDS": "1", "LISTEN_FDNAMES": "opm"}
	if got := Parse(func(k string) string { return env[k] }, os.Getpid()+1, int(f.Fd())); got != nil {
		t.Fatal("another pid's sockets must be ignored")
	}
	socks := Parse(func(k string) string { return env[k] }, os.Getpid(), int(f.Fd()))
	if len(socks) != 1 || socks[0].Port != port || socks[0].Name != "opm" {
		t.Fatalf("%+v", socks)
	}
	SetForTest(socks)
	defer SetForTest(nil)
	for i := 0; i < 2; i++ { // a listener can be closed and taken again (dups)
		l, inh, err := Listen(port)
		if err != nil || !inh {
			t.Fatalf("listen %v %v", inh, err)
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })}
		go srv.Serve(l)
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("serve on the inherited socket: %v", err)
		}
		resp.Body.Close()
		srv.Close()
	}
	if !Activated() {
		t.Fatal("activated")
	}
	l, inh, err := Listen(0)
	if err != nil || inh {
		t.Fatal("other ports bind normally")
	}
	l.Close()
}
