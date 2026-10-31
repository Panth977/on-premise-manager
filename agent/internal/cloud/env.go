package cloud

import (
	"os"
	"strings"
)

// Env is core/env.ts: configuration read from the environment on every access (tests set it first; ApplyEnv fills it).
var Env envT

type envT struct{}

func (envT) APIBase() string { return strings.TrimSuffix(os.Getenv("OPM_API_BASE"), "/") }

func (envT) FrontendOrigins() []string {
	out := []string{}
	for _, s := range strings.Split(os.Getenv("OPM_FRONTEND_ORIGINS"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ConsoleURL is OPM_CONSOLE_URL, else the first https origin of OPM_FRONTEND_ORIGINS; "" = null.
func (e envT) ConsoleURL() string {
	if v := strings.TrimSuffix(strings.TrimSpace(os.Getenv("OPM_CONSOLE_URL")), "/"); v != "" {
		return v
	}
	for _, o := range e.FrontendOrigins() {
		if strings.HasPrefix(o, "https://") {
			return o
		}
	}
	return ""
}
func (envT) ReleasePublicKey() string { return os.Getenv("OPM_RELEASE_PUBLIC_KEY") }
func (envT) AgentVersion() string     { return os.Getenv("OPM_AGENT_VERSION") }
func (envT) SmtpURL() string          { return os.Getenv("OPM_SMTP_URL") }
func (envT) SmtpFrom() string {
	if v, ok := os.LookupEnv("OPM_SMTP_FROM"); ok {
		return v
	}
	return "On-Prem Manager <no-reply@opm.example>"
}
func (envT) BlobRedirect() bool { return os.Getenv("OPM_BLOB_REDIRECT") == "1" }

// Emulator: test hooks on (OPM_TEST_HOOKS=1 on the local test stack); never set in production.
func (envT) Emulator() bool {
	return os.Getenv("FUNCTIONS_EMULATOR") == "true" || os.Getenv("FIRESTORE_EMULATOR_HOST") != "" || os.Getenv("OPM_TEST_HOOKS") == "1"
}

// Rate limit (per uid / per site, per instance): tokens per second + burst.
const (
	ratePerSec = 20.0
	rateBurst  = 100.0
)
