// Command opm-fakecloud serves internal/fakecloud standalone (dev / the upgrade smoke test, agent/test/upgrade-smoke).
// Not a production component.
//
//	opm-fakecloud --addr 127.0.0.1:9999 --admin 127.0.0.1:9998 --deployment DepSmoke --access-key KEY
//
// Admin API (plain HTTP, no auth; bind it to loopback):
//
//	POST /token?site=app                         → the install token (text)
//	POST /release?version=V&file=/path[&notes=…]  → pins the agent release (signed with this run's release key)
//	GET  /pubkey                                  → the release public key (base64)
//	POST /request-upgrade?site=&version=&by=      → a cloud "Upgrade agent" request (JSON)
//	GET  /reports?site=                           → heartbeat `upgrade` reports (JSON)
//	GET  /activities?site=                        → activities uploaded by the site (JSON)
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"

	"opm/agent/internal/fakecloud"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9999", "agent API address")
	admin := flag.String("admin", "127.0.0.1:9998", "admin address")
	dep := flag.String("deployment", "DepSmoke", "deployment id")
	key := flag.String("access-key", "ABCDEFGHIJKLMNOPQRSTUVWX", "deployment access key (24 chars)")
	goos := flag.String("os", runtime.GOOS, "release os")
	arch := flag.String("arch", runtime.GOARCH, "release arch")
	flag.Parse()
	fc := fakecloud.StartAt(*dep, *addr)
	fc.SetAccessKey(*key)
	fc.SetConsole("https://console.example", "Smoke deployment")
	_, relKey, _ := ed25519.GenerateKey(rand.Reader)
	pub := base64.StdEncoding.EncodeToString(relKey.Public().(ed25519.PublicKey))
	js := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, fc.CreateInstallToken(r.URL.Query().Get("site")))
	})
	mux.HandleFunc("GET /pubkey", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, pub) })
	mux.HandleFunc("POST /release", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		b, err := os.ReadFile(q.Get("file"))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		fc.SetAgentRelease(q.Get("version"), *goos, *arch, b, relKey)
		if n := q.Get("notes"); n != "" {
			fc.SetAgentReleaseNotes(n)
		}
		fmt.Fprintln(w, "pinned", q.Get("version"))
	})
	mux.HandleFunc("POST /request-upgrade", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		js(w, fc.RequestAgentUpgrade(q.Get("site"), q.Get("version"), q.Get("by")))
	})
	mux.HandleFunc("GET /reports", func(w http.ResponseWriter, r *http.Request) { js(w, fc.UpgradeReports(r.URL.Query().Get("site"))) })
	mux.HandleFunc("GET /activities", func(w http.ResponseWriter, r *http.Request) { js(w, fc.Activities(r.URL.Query().Get("site"))) })
	log.Printf("opm-fakecloud: API %s, admin %s, deployment %s, release key %s", fc.URL, *admin, *dep, pub)
	log.Fatal(http.ListenAndServe(*admin, mux))
}
