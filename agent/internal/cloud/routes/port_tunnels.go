package routes

import (
	"math"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/ulid"
)

// §21 #125 port tunnels: API side. The console lists / adds / removes a deployment's tunnels; agents long-poll for new
// connections and move the bytes (core/port_tunnels.go holds the listeners and the queues).
//
// Stored: deployments/{d}/portTunnels/{tunnelId} (the tunnel) and tunnelPorts/{port} ({deploymentId, tunnelId}: one tunnel
// per public port across every deployment, made in the same transaction).

func init() {
	cloud.Handlers(map[string]cloud.Handler{
		"listPortTunnels":     listPortTunnels,
		"createPortTunnel":    createPortTunnel,
		"deletePortTunnel":    deletePortTunnel,
		"agentPortTunnelNext": agentPortTunnelNext,
		"agentPortTunnelRecv": agentPortTunnelRecv,
		"agentPortTunnelSend": agentPortTunnelSend,
	})
	cloud.RegisterStartup(func() (func(), error) {
		all, err := allPortTunnels()
		if err != nil {
			return nil, err
		}
		core.StartPortTunnels(all)
		return core.StopPortTunnels, nil
	})
}

func tunnelPortDoc(port int) *driver.DocRef {
	return cloud.DB().Collection("tunnelPorts").Doc(strconv.Itoa(port))
}

func tunnelsCol(d string) *driver.CollectionRef {
	return cloud.Col.Deployments().Doc(d).Collection("portTunnels")
}

// unavailableReason: why tunnels do not work on this cloud, or "".
func unavailableReason() string {
	if !core.HubRunning() {
		return "Port tunnels need the single-process cloud on a VM (opm-cloud); this cloud cannot listen on ports."
	}
	if len(core.ConfiguredPorts()) == 0 {
		return "No public ports: set TUNNEL_FREE_PORTS on the cloud (e.g. 2000-2100,3000) and publish those ports from the cloud container to its VM."
	}
	return ""
}

// cloudHost: the host the tunnel addresses name: TUNNEL_PUBLIC_HOST (a name that reaches the VM directly: a proxy such as
// a CDN in front of the public URL does not carry raw TCP on other ports), else the public URL's host.
func cloudHost() string {
	if h := doc.JSTrim(os.Getenv("TUNNEL_PUBLIC_HOST")); h != "" {
		return h
	}
	if u, err := url.Parse(cloud.Env.APIBase()); err == nil && u.Scheme != "" && u.Host != "" {
		h := u.Hostname()
		if strings.Contains(h, ":") {
			return "[" + h + "]" // URL#hostname keeps the brackets of an IPv6 address
		}
		return h
	}
	return "localhost"
}

// numberOr is Number(c.query.get(k) ?? def).
func numberOr(c *cloud.Ctx, k string, def float64) float64 {
	s, ok := c.Query.Lookup(k)
	if !ok {
		return def
	}
	if f, ok := cloud.JSNumber(s); ok {
		return f
	}
	return math.NaN()
}

// waitMs is the wait parameter of the long-polls.
func waitMs(c *cloud.Ctx) int64 {
	w := numberOr(c, "wait", 25)
	if math.IsNaN(w) || math.IsInf(w, 0) {
		return 25_000
	}
	return int64(math.Max(0, math.Min(30, math.Floor(w)))) * 1000
}

func csv(c *cloud.Ctx, k string) []string {
	out := []string{}
	for _, x := range strings.Split(c.Query.Get(k), ",") {
		if e := doc.JSTrim(x); e != "" && len(out) < 500 {
			out = append(out, e)
		}
	}
	return out
}

func storedTunnels(deploymentID string) ([]*doc.Map, error) {
	q, err := tunnelsCol(deploymentID).Get()
	if err != nil {
		return nil, err
	}
	out := make([]*doc.Map, len(q.Docs))
	for i, d := range q.Docs {
		out[i] = d.Data()
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Num("publicPort") < out[b].Num("publicPort") })
	return out, nil
}

// allPortTunnels: every stored tunnel of every deployment (server start).
func allPortTunnels() ([]*doc.Map, error) {
	var out []*doc.Map
	q, err := cloud.DB().Collection("tunnelPorts").Get()
	if err != nil {
		return nil, err
	}
	for _, p := range q.Docs {
		x := p.Data()
		t, err := tunnelsCol(x.Str("deploymentId")).Doc(x.Str("tunnelId")).Get()
		if err != nil {
			return nil, err
		}
		if t.Exists() {
			out = append(out, t.Data())
		}
	}
	return out, nil
}

// portTunnelCloudItems is portTunnelCloudItems: CloudView.tunnels; nil = this cloud carries no tunnels (the agent does not
// poll).
func portTunnelCloudItems(deploymentID string) ([]any, error) {
	if unavailableReason() != "" {
		return nil, nil
	}
	host := cloudHost()
	l, err := storedTunnels(deploymentID)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(l))
	for i, t := range l {
		out[i] = t.Without("deploymentId").Set("address", host+":"+strconv.Itoa(int(t.Num("publicPort"))))
	}
	return out, nil
}

func listPortTunnels(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.config", "read"); err != nil {
		return nil, err
	}
	reason := unavailableReason()
	host := cloudHost()
	sites := []string{}
	for _, s := range dep.Map("draft").Arr("sites") {
		if m, ok := s.(*doc.Map); ok {
			sites = append(sites, m.Str("siteSlug"))
		}
	}
	stored, err := storedTunnels(dep.Str("deploymentId"))
	if err != nil {
		return nil, err
	}
	items := make([]any, len(stored))
	for i, t := range stored {
		items[i] = core.TunnelStatus(t, host)
	}
	free := []any{}
	var why any
	if reason != "" {
		why = reason
	} else {
		free = core.FreePorts()
	}
	return doc.Obj("available", reason == "", "reason", why, "host", host, "freePorts", free, "agents", core.AgentTunnels(dep.Str("deploymentId"), sites), "items", items), nil
}

func siteOfDraft(dep *doc.Map, slug any) bool {
	for _, s := range dep.Map("draft").Arr("sites") {
		if m, ok := s.(*doc.Map); ok && m.Val("siteSlug") == slug {
			return true
		}
	}
	return false
}

func createPortTunnel(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.config", "publish"); err != nil {
		return nil, err
	}
	if reason := unavailableReason(); reason != "" {
		return nil, cloud.E.Unavailable(reason)
	}
	raw, err := c.JSON()
	if err != nil {
		return nil, err
	}
	if bad := core.ValidatePortTunnel(raw); bad != "" {
		return nil, cloud.E.Bad("body", "V_FORMAT", bad)
	}
	body := raw.(*doc.Map)
	siteSlug := body.Str("siteSlug")
	if !siteOfDraft(dep, siteSlug) {
		return nil, cloud.E.NotFound("site " + siteSlug + " does not exist")
	}
	port := int(body.Num("publicPort"))
	portText := strconv.Itoa(port)
	configured := false
	for _, p := range core.ConfiguredPorts() {
		configured = configured || p == port
	}
	if !configured {
		return nil, cloud.E.Bad("publicPort", "V_RANGE", "port "+portText+" is not one of the cloud's tunnel ports (TUNNEL_FREE_PORTS)")
	}
	if core.PortTaken(port) {
		return nil, cloud.E.Conflict("port " + portText + " is already used by another tunnel")
	}
	a := c.Account
	var label any
	if s, ok := body.StrOK("label"); ok {
		if s = doc.JSTrim(s); s != "" {
			label = s
		}
	}
	deploymentID := dep.Str("deploymentId")
	t := doc.Obj("tunnelId", ulid.New(), "deploymentId", deploymentID, "siteSlug", siteSlug, "target", body.Val("target"), "publicPort", body.Val("publicPort"), "label", label, "createdAt", doc.NowISO(), "createdBy", a.Doc.Val("email"))
	depRef := cloud.Col.Deployments().Doc(deploymentID)
	target := body.Map("target")
	err = cloud.DB().RunTransaction(func(tx *driver.Tx) error {
		ds, err := tx.Get(depRef)
		if err != nil {
			return err
		}
		ps, err := tx.Get(tunnelPortDoc(port))
		if err != nil {
			return err
		}
		if ps.Exists() {
			return cloud.E.Conflict("port " + portText + " is already used by another tunnel")
		}
		d := cloud.DecodeDeployment(ds.Data())
		log := core.NewCloudLog(d.Str("deploymentId"), d)
		tx.Create(tunnelPortDoc(port), doc.Obj("deploymentId", deploymentID, "tunnelId", t.Val("tunnelId"), "port", body.Val("publicPort")))
		tx.Create(tunnelsCol(deploymentID).Doc(t.Str("tunnelId")), t)
		log.Add(tx, core.CloudEntry{Type: "tunnel.created", Summary: "Tunnel " + core.PortTunnelTargetText(target) + " of site " + siteSlug + " on port " + portText + " added (off until switched on on the site)", Data: doc.Obj("tunnelId", t.Val("tunnelId"), "siteSlug", siteSlug, "target", target, "publicPort", body.Val("publicPort")), Actor: a.UID})
		tx.Update(depRef, log.DepUpdate().Set("agentEpoch", d.Num("agentEpoch")+1))
		return nil
	})
	if err != nil {
		return nil, err
	}
	core.Listen(t)
	return core.TunnelStatus(t, cloudHost()), nil
}

func deletePortTunnel(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.config", "publish"); err != nil {
		return nil, err
	}
	deploymentID := dep.Str("deploymentId")
	ref := tunnelsCol(deploymentID).Doc(c.Params["tunnelId"])
	depRef := cloud.Col.Deployments().Doc(deploymentID)
	t, err := driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*doc.Map, error) {
		ds, err := tx.Get(depRef)
		if err != nil {
			return nil, err
		}
		ts, err := tx.Get(ref)
		if err != nil {
			return nil, err
		}
		if !ts.Exists() {
			return nil, cloud.E.NotFound("tunnel not found")
		}
		t := ts.Data()
		port := int(t.Num("publicPort"))
		ps, err := tx.Get(tunnelPortDoc(port))
		if err != nil {
			return nil, err
		}
		d := cloud.DecodeDeployment(ds.Data())
		log := core.NewCloudLog(d.Str("deploymentId"), d)
		tx.Delete(ref)
		if ps.Exists() && ps.Data().Val("tunnelId") == t.Val("tunnelId") {
			tx.Delete(ps.Ref())
		}
		log.Add(tx, core.CloudEntry{Type: "tunnel.deleted", Summary: "Tunnel " + core.PortTunnelTargetText(t.Map("target")) + " of site " + t.Str("siteSlug") + " on port " + strconv.Itoa(port) + " removed", Data: doc.Obj("tunnelId", t.Val("tunnelId"), "siteSlug", t.Val("siteSlug"), "publicPort", t.Val("publicPort")), Actor: c.Account.UID})
		tx.Update(depRef, log.DepUpdate().Set("agentEpoch", d.Num("agentEpoch")+1))
		return t, nil
	})
	if err != nil {
		return nil, err
	}
	core.Unlisten(int(t.Num("publicPort")))
	return doc.Obj("deleted", true), nil
}

func agentPortTunnelNext(c *cloud.Ctx) (any, error) {
	if unavailableReason() != "" {
		return doc.Obj("opens", []any{}), nil
	}
	var rg any
	switch v, ok := c.Query.Lookup("remoteGui"); {
	case ok && v == "1":
		rg = true
	case ok && v == "0":
		rg = false
	}
	opens := core.TunnelNext(c.Context, c.Agent.DeploymentID, c.Agent.SiteSlug, core.TunnelReport{Enabled: csv(c, "enabled"), RemoteGui: rg, Reach: csv(c, "reach")}, waitMs(c))
	return doc.Obj("opens", opens), nil
}

func agentPortTunnelRecv(c *cloud.Ctx) (any, error) {
	return core.TunnelRecv(c.Context, c.Params["connId"], c.Agent.DeploymentID, c.Agent.SiteSlug, waitMs(c)), nil
}

func agentPortTunnelSend(c *cloud.Ctx) (any, error) {
	if float64(len(c.Body)) > core.PORT_TUNNEL_CHUNK_MAX*1.4+4096 {
		return nil, cloud.E.TooLarge("at most 1048576 bytes per call")
	}
	v, err := c.JSON()
	if err != nil {
		return nil, err
	}
	b, _ := v.(*doc.Map)
	var data []byte
	if s, ok := b.StrOK("dataB64"); ok {
		data = doc.B64Decode(s)
	}
	var errText any
	if s, ok := b.StrOK("error"); ok && s != "" {
		errText = doc.JSSlice(s, 500)
	}
	return doc.Obj("accepted", core.TunnelSend(c.Params["connId"], c.Agent.DeploymentID, c.Agent.SiteSlug, data, b.Val("close") == true, errText)), nil
}
