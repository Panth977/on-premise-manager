package core

import (
	"os"
	"regexp"
	"strings"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
)

var reLocalHost = regexp.MustCompile(`^(?:127\.0\.0\.1|localhost|\[::1\])(?::[0-9]+)?$`)

// ApiBaseOf is apiBaseOf of routes/setup.ts.
func ApiBaseOf(c *cloud.Ctx) string {
	if b := cloud.Env.APIBase(); b != "" {
		return b
	}
	host, ok := c.Headers["x-forwarded-host"]
	if !ok {
		if host, ok = c.Headers["host"]; !ok {
			host = "localhost"
		}
	}
	proto, ok := c.Headers["x-forwarded-proto"]
	if !ok {
		proto = "https"
		if reLocalHost.MatchString(host) {
			proto = "http"
		}
	}
	proto, _, _ = strings.Cut(proto, ",")
	mount := c.Mount
	if mount == "" && cloud.Env.Emulator() && strings.HasSuffix(host, ":5001") {
		project := "undefined"
		if v, ok := os.LookupEnv("GCLOUD_PROJECT"); ok {
			project = v
		}
		mount = "/" + project + "/asia-south1/api"
	}
	return doc.JSTrim(proto) + "://" + host + mount
}
