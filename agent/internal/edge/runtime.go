package edge

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	// Caddy modules used by the generated JSON.
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/headers"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/rewrite"
	_ "github.com/caddyserver/caddy/v2/modules/caddypki"
	_ "github.com/caddyserver/caddy/v2/modules/caddytls"
	_ "github.com/caddyserver/caddy/v2/modules/filestorage"

	"opm/agent/internal/model"
)

// The process-wide Caddy runtime. caddy.Load is global; every agent instance in the process contributes a partial
// config (production: exactly one instance). Server names are prefixed by the instance when there is more than one.
var (
	rtMu      sync.Mutex
	rtParts   = map[string]*Partial{}
	rtStorage = map[string]string{}
	rtLast    []byte
)

func sanitize(s string) string {
	return regexp.MustCompile(`[^A-Za-z0-9_]`).ReplaceAllString(s, "_")
}

func assembleLocked() map[string]any {
	keys := make([]string, 0, len(rtParts))
	for k := range rtParts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []*Partial
	storage := ""
	for _, k := range keys {
		p := rtParts[k]
		cp := *p
		if len(keys) > 1 {
			cp.Servers = map[string]any{}
			for name, v := range p.Servers {
				cp.Servers[sanitize(k)+"_"+name] = v
			}
		}
		parts = append(parts, &cp)
		if storage == "" {
			storage = rtStorage[k]
		}
	}
	if storage == "" {
		storage = filepath.Join(os.TempDir(), "opm-caddy")
	}
	return Assemble(storage, parts)
}

// Apply sets an instance's partial (nil removes it) and loads the merged config gracefully. On failure the previous
// partial of the instance is restored and the previous config keeps running. Returns the redacted JSON that was loaded.
func Apply(instance, storageRoot string, p *Partial, force bool) ([]byte, error) {
	rtMu.Lock()
	defer rtMu.Unlock()
	prev, had := rtParts[instance]
	prevStorage := rtStorage[instance]
	if p == nil {
		delete(rtParts, instance)
		delete(rtStorage, instance)
	} else {
		rtParts[instance] = p
		rtStorage[instance] = storageRoot
	}
	cfg := assembleLocked()
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	if err := caddy.Load(raw, force); err != nil {
		if had {
			rtParts[instance] = prev
			rtStorage[instance] = prevStorage
		} else {
			delete(rtParts, instance)
			delete(rtStorage, instance)
		}
		return nil, err
	}
	rtLast = raw
	red, _ := json.MarshalIndent(Redact(cfg), "", "  ")
	return red, nil
}

// Stop removes an instance (its servers stop).
func Stop(instance string) error {
	_, err := Apply(instance, "", nil, false)
	return err
}

// BindPort extracts the port from a Caddy listen error ("listen tcp :8081: bind: address already in use").
func BindPort(err error) int {
	if err == nil {
		return 0
	}
	m := regexp.MustCompile(`listen(?:ing)? (?:tcp|udp)[46]? [^:]*:(\d+)`).FindStringSubmatch(err.Error())
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// ───────────────────────── certificates (CertMagic storage)

// CertStatusFor reads the stored certificate of hostname (issuer dirs under <root>/certificates/*/<host>/).
func CertStatusFor(root, siteSlug string, h model.EdgeHost, lastErr *string) model.CertStatus {
	cs := model.CertStatus{SiteSlug: siteSlug, Hostname: h.Hostname, Mode: h.TLS.Mode, LastError: lastErr}
	if h.TLS.Mode == "acme" {
		ch := h.TLS.Challenge
		cs.Challenge = &ch
	}
	name := h.Hostname
	if strings.HasPrefix(name, "*.") {
		name = "wildcard_" + name[1:]
	}
	matches, _ := filepath.Glob(filepath.Join(root, "certificates", "*", name, name+".crt"))
	var best *x509.Certificate
	var bestMod time.Time
	for _, m := range matches {
		raw, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		blk, _ := pem.Decode(raw)
		if blk == nil {
			continue
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			continue
		}
		st, _ := os.Stat(m)
		if best == nil || c.NotAfter.After(best.NotAfter) {
			best, bestMod = c, st.ModTime()
		}
	}
	if best != nil {
		iss := best.Issuer.CommonName
		if iss == "" && len(best.Issuer.Organization) > 0 {
			iss = best.Issuer.Organization[0]
		}
		nb := best.NotBefore.UTC().Format(time.RFC3339)
		na := best.NotAfter.UTC().Format(time.RFC3339)
		lr := bestMod.UTC().Format(time.RFC3339)
		cs.Issuer, cs.NotBefore, cs.NotAfter, cs.LastRenewalAt = &iss, &nb, &na, &lr
		life := best.NotAfter.Sub(best.NotBefore)
		var next time.Time
		if h.TLS.Mode == "acme" {
			next = best.NotAfter.Add(-30 * 24 * time.Hour)
		} else {
			next = best.NotBefore.Add(life * 2 / 3)
		}
		ns := next.UTC().Format(time.RFC3339)
		cs.NextRenewalAt = &ns
	}
	return cs
}

// RemoveStoredCert deletes the stored certificate of a host (renew = remove + reload).
func RemoveStoredCert(root, hostname string) int {
	name := hostname
	if strings.HasPrefix(name, "*.") {
		name = "wildcard_" + name[1:]
	}
	dirs, _ := filepath.Glob(filepath.Join(root, "certificates", "*", name))
	for _, d := range dirs {
		_ = os.RemoveAll(d)
	}
	return len(dirs)
}

// LocalCAPath is the PEM root of the internal CA.
func LocalCAPath(root string) string {
	return filepath.Join(root, "pki", "authorities", "local", "root.crt")
}

// ValidateInput checks a site's edge config with caddy.Validate (provision only: nothing listens, nothing is issued).
// caddy.Validate is not safe to run concurrently (nor next to caddy.Load: certmagic's cache is process-wide — two
// at once panicked with "close of closed channel"), so it holds the runtime lock; a panic inside Caddy becomes an
// error instead of crashing the agent (§21 #110).
func ValidateInput(in *Input, storageRoot string) (err error) {
	rtMu.Lock()
	defer rtMu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("edge config check failed inside Caddy: %v", r)
		}
	}()
	raw, err := json.Marshal(Assemble(storageRoot, []*Partial{in.Build("")}))
	if err != nil {
		return err
	}
	var c caddy.Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return err
	}
	return caddy.Validate(&c)
}
