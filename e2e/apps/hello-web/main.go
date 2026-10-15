// hello-web: counter page for the OPM hello-world e2e (SPEC-final §20.2).
//
// env: PORT (8080), THEME (green|yellow|blue|red), API_BASE (/api), BANNER_FILE.
// GET / -> HTML <body data-theme="<THEME>" style="background:<colour>">; GET /config.json; GET /healthz.
package main

import (
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"os"
	"time"
)

var colours = map[string]string{
	"green":  "#16a34a",
	"yellow": "#eab308",
	"blue":   "#2563eb",
	"red":    "#dc2626",
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

var page = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Hello OPM</title>
<style>
  html,body{margin:0;height:100%}
  body{font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;color:#0f172a;display:flex;align-items:center;justify-content:center}
  .card{background:#ffffff;border-radius:16px;padding:40px 56px;box-shadow:0 10px 40px rgba(0,0,0,.25);text-align:center;min-width:320px}
  h1{margin:0 0 8px;font-size:40px}
  #banner{margin:0 0 24px;color:#475569;white-space:pre-wrap}
  .count{font-size:64px;font-weight:700;margin:8px 0 24px;color:{{.Colour}}}
  button{font-size:18px;padding:12px 28px;border:0;border-radius:10px;background:{{.Colour}};color:#fff;cursor:pointer;font-weight:600}
  button:disabled{opacity:.6}
  .meta{margin-top:20px;font-size:13px;color:#64748b}
  #error{color:#b91c1c;font-size:14px;min-height:1em;margin-top:12px}
</style>
</head>
<body data-theme="{{.Theme}}" style="background:{{.Colour}}">
  <main class="card">
    <h1 id="title">Hello OPM</h1>
    <p id="banner">{{.Banner}}</p>
    <div>Counter</div>
    <div class="count"><span id="count">…</span></div>
    <button id="inc" type="button">Increment</button>
    <div id="error"></div>
    <div class="meta">theme <b id="theme">{{.Theme}}</b> · api <code>{{.APIBase}}</code></div>
  </main>
<script>
  const API = {{.APIBase}};
  const countEl = document.getElementById('count');
  const errEl = document.getElementById('error');
  const btn = document.getElementById('inc');
  async function call(method) {
    errEl.textContent = '';
    try {
      const r = await fetch(API + '/count', { method, cache: 'no-store' });
      if (!r.ok) throw new Error('HTTP ' + r.status);
      const j = await r.json();
      countEl.textContent = String(j.count);
      countEl.dataset.loaded = '1';
    } catch (e) {
      errEl.textContent = 'API error: ' + e.message;
    }
  }
  btn.addEventListener('click', async () => { btn.disabled = true; await call('POST'); btn.disabled = false; });
  call('GET');
</script>
</body>
</html>
`))

func banner() string {
	p := os.Getenv("BANNER_FILE")
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		log.Printf("banner: %v", err)
		return ""
	}
	return string(b)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		resp, err := http.Get("http://127.0.0.1:" + env("PORT", "8080") + "/healthz")
		if err != nil || resp.StatusCode != 200 {
			os.Exit(1)
		}
		os.Exit(0)
	}
	port := env("PORT", "8080")
	theme := env("THEME", "green")
	colour, ok := colours[theme]
	if !ok {
		log.Printf("unknown THEME %q, using green", theme)
		theme, colour = "green", colours["green"]
	}
	apiBase := env("API_BASE", "/api")

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "healthz", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/config.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]string{"theme": theme, "apiBase": apiBase, "banner": banner()})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = page.Execute(w, map[string]any{"Theme": theme, "Colour": template.CSS(colour), "APIBase": apiBase, "Banner": banner()})
	})
	srv := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("hello-web listening on :%s theme=%s api=%s", port, theme, apiBase)
	log.Fatal(srv.ListenAndServe())
}
