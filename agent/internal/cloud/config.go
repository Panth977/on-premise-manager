package cloud

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
)

// CloudConfig is server/config.ts: an optional JSON file (OPM_CONFIG or --config) overlaid by environment variables.
// OPM_ADAPTER (still set on prod) is not read: there is one adapter.
type CloudConfig struct {
	DataDir         string
	PublicURL       string
	ConsoleURL      string
	Host            string
	Port            int
	StaticDir       string // "" = API only
	FrontendOrigins []string
	BlobRedirect    bool
	RunJobs         bool
	TrustProxy      bool
	BodyLimit       int64
	Oidc            *driver.OidcConfig
}

func trimStr(v any) string {
	s, _ := v.(string)
	return doc.JSTrim(s)
}

func boolOf(v any, def bool) bool {
	if v == nil || v == "" {
		return def
	}
	return v == true || v == "1" || v == "true" || v == "yes"
}

// jsNumber is Number(string): false for NaN.
func jsNumber(s string) (float64, bool) {
	s = doc.JSTrim(s)
	if s == "" {
		return 0, true
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		n, err := strconv.ParseUint(s[2:], 16, 64)
		return float64(n), err == nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || strings.ContainsAny(s, "nN_pP") || (math.IsInf(f, 0) && !strings.Contains(s, "Infinity")) {
		return 0, false
	}
	return f, true
}

// JSNumber is Number(string): false for NaN.
func JSNumber(s string) (float64, bool) { return jsNumber(s) }

func origin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("Invalid URL: %s", raw)
	}
	h := strings.ToLower(u.Host)
	if (u.Scheme == "http" && strings.HasSuffix(h, ":80")) || (u.Scheme == "https" && strings.HasSuffix(h, ":443")) {
		h = h[:strings.LastIndexByte(h, ':')]
	}
	return strings.ToLower(u.Scheme) + "://" + h, nil
}

var googleIssuer = regexp.MustCompile(`accounts\.google\.com`)

// LoadConfig is loadConfig (argv with the program name).
func LoadConfig(argv []string) (*CloudConfig, error) {
	file := os.Getenv("OPM_CONFIG")
	for i, a := range argv {
		if a == "--config" {
			file = ""
			if i+1 < len(argv) {
				file = argv[i+1]
			}
			break
		}
	}
	f := doc.New()
	if file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("config file %s does not exist", file)
		}
		if f, err = doc.ParseMap(raw); err != nil {
			return nil, err
		}
	}
	pick := func(env, key string) string {
		if v := trimStr(os.Getenv(env)); v != "" {
			return v
		}
		return trimStr(f.Val(key))
	}
	or := func(a, b string) string {
		if a != "" {
			return a
		}
		return b
	}
	port := 8790
	if s := pick("OPM_PORT", "port"); s != "" {
		n, ok := jsNumber(s)
		if !ok {
			return nil, fmt.Errorf("OPM_PORT: not a number: %s", s)
		}
		port = int(n)
	}
	publicURL := strings.TrimSuffix(or(pick("OPM_PUBLIC_URL", "publicUrl"), "http://localhost:"+itoa(port)), "/")
	consoleURL := strings.TrimSuffix(or(pick("OPM_CONSOLE_URL", "consoleUrl"), publicURL), "/")
	dataDir, err := filepath.Abs(or(pick("OPM_DATA_DIR", "dataDir"), "/var/lib/opm-cloud"))
	if err != nil {
		return nil, err
	}
	staticDir := pick("OPM_STATIC_DIR", "staticDir")
	o1, err := origin(publicURL)
	if err != nil {
		return nil, err
	}
	o2, err := origin(consoleURL)
	if err != nil {
		return nil, err
	}
	origins := []string{}
	add := func(o string) {
		if !hasStr(origins, o) {
			origins = append(origins, o)
		}
	}
	add(o1)
	add(o2)
	for _, o := range strings.Split(pick("OPM_FRONTEND_ORIGINS", "frontendOrigins"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			add(o)
		}
	}
	for _, o := range f.Arr("frontendOrigins") {
		if s, ok := o.(string); ok {
			add(s)
		}
	}
	var oidc *driver.OidcConfig
	if issuer := pick("OPM_OIDC_ISSUER", "oidcIssuer"); issuer != "" {
		label := pick("OPM_OIDC_LABEL", "oidcLabel")
		if label == "" {
			label = "single sign-on"
			if googleIssuer.MatchString(issuer) {
				label = "Google"
			}
		}
		oidc = &driver.OidcConfig{Issuer: issuer, ClientID: pick("OPM_OIDC_CLIENT_ID", "oidcClientId"), ClientSecret: pick("OPM_OIDC_CLIENT_SECRET", "oidcClientSecret"), Label: label, RedirectURI: publicURL + "/auth/oidc/callback"}
		if d := pick("OPM_OIDC_ALLOWED_DOMAIN", "oidcAllowedDomain"); d != "" {
			oidc.AllowedDomain = &d
		}
		if oidc.ClientID == "" || oidc.ClientSecret == "" {
			return nil, errors.New("OPM_OIDC_ISSUER needs OPM_OIDC_CLIENT_ID and OPM_OIDC_CLIENT_SECRET")
		}
	}
	envOr := func(env, key string) any {
		if v, ok := os.LookupEnv(env); ok {
			return v
		}
		return f.Val(key)
	}
	limit := int64(64 * 1024 * 1024)
	if s := pick("OPM_BODY_LIMIT", "bodyLimit"); s != "" {
		if n, ok := jsNumber(s); ok {
			limit = int64(n)
		}
	}
	c := &CloudConfig{DataDir: dataDir, PublicURL: publicURL, ConsoleURL: consoleURL, Host: or(pick("OPM_HOST", "host"), "0.0.0.0"), Port: port, FrontendOrigins: origins, BlobRedirect: boolOf(envOr("OPM_BLOB_REDIRECT", "blobRedirect"), true), RunJobs: boolOf(envOr("OPM_RUN_JOBS", "runJobs"), true), TrustProxy: boolOf(envOr("OPM_TRUST_PROXY", "trustProxy"), true), BodyLimit: limit, Oidc: oidc}
	if staticDir != "" {
		if c.StaticDir, err = filepath.Abs(staticDir); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func hasStr(l []string, s string) bool { return has(l, s) }

func setIfEmpty(k, v string) {
	if os.Getenv(k) == "" {
		os.Setenv(k, v)
	}
}

// ApplyEnv is applyEnv: what Env reads, set once before any handler runs (explicit env values win).
func ApplyEnv(c *CloudConfig) error {
	setIfEmpty("OPM_API_BASE", c.PublicURL)
	setIfEmpty("OPM_CONSOLE_URL", c.ConsoleURL)
	origins := []string{}
	for _, l := range [][]string{strings.Split(os.Getenv("OPM_FRONTEND_ORIGINS"), ","), c.FrontendOrigins} {
		for _, o := range l {
			if o = strings.TrimSpace(o); o != "" && !has(origins, o) {
				origins = append(origins, o)
			}
		}
	}
	os.Setenv("OPM_FRONTEND_ORIGINS", strings.Join(origins, ","))
	if c.BlobRedirect {
		os.Setenv("OPM_BLOB_REDIRECT", "1")
	} else {
		os.Setenv("OPM_BLOB_REDIRECT", "0")
	}
	os.Setenv("OPM_DATA_DIR", c.DataDir)
	setIfEmpty("OPM_PUBLIC_URL", c.PublicURL)
	// the release public key the install script embeds: env, else the data dir's own key (opm-cloud release-key)
	if os.Getenv("OPM_RELEASE_PUBLIC_KEY") == "" {
		if raw, err := os.ReadFile(filepath.Join(c.DataDir, "secrets", "release-key.json")); err == nil {
			m, err := doc.ParseMap(raw)
			if err != nil {
				return err
			}
			os.Setenv("OPM_RELEASE_PUBLIC_KEY", m.Str("publicKeyB64"))
		}
	}
	return os.MkdirAll(c.DataDir, 0o777)
}
