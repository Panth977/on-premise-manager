// Package edge is the embedded Caddy v2 edge of SPEC-final §12: the custom OPM modules, the Caddy JSON builder
// (§12.3), a process-wide runtime that loads the config gracefully, and certificate status from CertMagic storage.
package edge

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// Host is the agent side of the custom modules (one per agent instance in this process).
type Host interface {
	// ServeAgent serves /_opm/* (GUI + API; never /_opm/peer/*) for the given surface.
	ServeAgent(w http.ResponseWriter, r *http.Request, surface string)
	// Ready reports whether the container of serviceID is "completed" (§15.4).
	Ready(serviceID string) bool
}

var (
	regMu    sync.RWMutex
	registry = map[string]Host{}
)

// Register makes an agent instance known to the modules.
func Register(instance string, h Host) {
	regMu.Lock()
	defer regMu.Unlock()
	registry[instance] = h
}

// Unregister removes an instance.
func Unregister(instance string) {
	regMu.Lock()
	defer regMu.Unlock()
	delete(registry, instance)
}

func lookup(instance string) Host {
	regMu.RLock()
	defer regMu.RUnlock()
	return registry[instance]
}

func init() {
	caddy.RegisterModule(AgentHandler{})
	caddy.RegisterModule(ReadyHandler{})
	caddy.RegisterModule(RedirectHandler{})
	caddy.RegisterModule(WSGuard{})
	caddy.RegisterModule(AnyPermission{})
	caddy.RegisterModule(RouteHandler{})
	caddy.RegisterModule(UpstreamHandler{})
	caddy.RegisterModule(BodyLimit{})
	caddy.RegisterModule(OPMTransport{})
}

// AgentHandler is http.handlers.opm_agent.
type AgentHandler struct {
	Surface  string `json:"surface,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// CaddyModule returns the module information.
func (AgentHandler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.opm_agent", New: func() caddy.Module { return new(AgentHandler) }}
}

func (h AgentHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, _ caddyhttp.Handler) error {
	host := lookup(h.Instance)
	if host == nil {
		return caddyhttp.Error(http.StatusServiceUnavailable, fmt.Errorf("agent instance %q not registered", h.Instance))
	}
	if r.URL.Path == "/_opm/peer" || strings.HasPrefix(r.URL.Path, "/_opm/peer/") {
		// the mesh API is only on agentPort; the agent serves its 404 page
		r = r.Clone(r.Context())
		r.URL.Path = "/_opm/__peer_not_on_edge__"
	}
	surface := h.Surface
	if surface == "" {
		surface = "edge"
	}
	host.ServeAgent(w, r, surface)
	return nil
}

// ReadyHandler is http.handlers.opm_ready: 503 (→ error route with the not-found page) while the container is not completed.
type ReadyHandler struct {
	ContainerSlug string `json:"containerSlug"`
	Instance      string `json:"instance,omitempty"`
}

// CaddyModule returns the module information.
func (ReadyHandler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.opm_ready", New: func() caddy.Module { return new(ReadyHandler) }}
}

func (h ReadyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	host := lookup(h.Instance)
	if host == nil || !host.Ready(h.ContainerSlug) {
		return caddyhttp.Error(http.StatusServiceUnavailable, fmt.Errorf("container %s is not ready", h.ContainerSlug))
	}
	return next.ServeHTTP(w, r)
}

// WSGuard is http.handlers.opm_ws_guard: 400 for WebSocket upgrades when not allowed.
type WSGuard struct {
	Allow bool `json:"allow"`
}

// CaddyModule returns the module information.
func (WSGuard) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.opm_ws_guard", New: func() caddy.Module { return new(WSGuard) }}
}

func (h WSGuard) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if !h.Allow && strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "WebSocket is not enabled for this route", http.StatusBadRequest)
		return nil
	}
	return next.ServeHTTP(w, r)
}

// AnyPermission is tls.permission.opm_any: on-demand certificates for the "*" host (internal issuer) except the
// names whose host entry has TLS mode none.
type AnyPermission struct {
	Deny []string `json:"deny,omitempty"`
}

// CaddyModule returns the module information.
func (AnyPermission) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "tls.permission.opm_any", New: func() caddy.Module { return new(AnyPermission) }}
}

// CertificateAllowed implements caddytls.OnDemandPermission.
func (p AnyPermission) CertificateAllowed(_ context.Context, name string) error {
	for _, d := range p.Deny {
		if strings.EqualFold(d, name) {
			return fmt.Errorf("host %s has TLS mode none", name)
		}
	}
	return nil
}

// Interface guards.
var (
	_ caddyhttp.MiddlewareHandler = (*AgentHandler)(nil)
	_ caddyhttp.MiddlewareHandler = (*ReadyHandler)(nil)
	_ caddyhttp.MiddlewareHandler = (*RedirectHandler)(nil)
	_ caddyhttp.MiddlewareHandler = (*WSGuard)(nil)
)
