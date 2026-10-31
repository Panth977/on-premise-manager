package routes

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
)

// §21 #118 remote agent GUI: API side: the console opens a remote session (cookie + the site listed in the cloud view),
// agents long-poll for the browser requests and post the answers. The browser side is core/remote_gui_server.go.

func init() {
	cloud.Handlers(map[string]cloud.Handler{
		"openRemoteGui":        openRemoteGui,
		"agentRemoteGuiNext":   agentRemoteGuiNext,
		"agentRemoteGuiResult": agentRemoteGuiResult,
		"agentRemoteGuiWsRecv": agentRemoteGuiWsRecv,
		"agentRemoteGuiWsSend": agentRemoteGuiWsSend,
	})
}

var reHTTPS = regexp.MustCompile(`(?i)^https:`)

func openRemoteGui(c *cloud.Ctx) (any, error) {
	dep, err := cloud.LoadDep(c, "")
	if err != nil {
		return nil, err
	}
	if err := cloud.NeedDep(c, dep, "deployment.config", "publish"); err != nil {
		return nil, err
	}
	siteSlug := c.Params["siteSlug"]
	if !cloud.ReSlug.MatchString(siteSlug) || !siteOfDraft(dep, siteSlug) {
		return nil, cloud.E.NotFound("site " + siteSlug + " does not exist")
	}
	a := c.Account
	email := a.Doc.Val("email")
	emailText := core.JSString(email)
	deploymentID := dep.Str("deploymentId")
	wasActive := false
	for _, x := range core.RemoteActiveSites(deploymentID) {
		wasActive = wasActive || x.(*doc.Map).Val("siteSlug") == siteSlug
	}
	var name any
	if a.Doc.Truthy("name") {
		name = a.Doc.Val("name")
	}
	s := core.OpenRemoteSession(deploymentID, siteSlug, doc.Obj("uid", a.UID, "email", email, "name", name))
	// the cloud view lists the site: its agent starts long-polling (agentEpoch+1 wakes every poll at once)
	depRef := cloud.Col.Deployments().Doc(deploymentID)
	err = cloud.DB().RunTransaction(func(tx *driver.Tx) error {
		ds, err := tx.Get(depRef)
		if err != nil {
			return err
		}
		d := cloud.DecodeDeployment(ds.Data())
		log := core.NewCloudLog(d.Str("deploymentId"), d)
		log.Add(tx, core.CloudEntry{Type: "remote-gui.opened", Summary: emailText + " opened the agent GUI of site " + siteSlug + " through the cloud", Data: doc.Obj("siteSlug", siteSlug, "sessionId", s.SessionID[:8]), Actor: a.UID})
		up := log.DepUpdate()
		if !wasActive {
			up.Set("agentEpoch", d.Num("agentEpoch")+1)
		}
		tx.Update(depRef, up)
		return nil
	})
	if err != nil {
		return nil, err
	}
	prefix := core.RemoteGuiPrefix(deploymentID, siteSlug)
	cookie := core.RemoteCookieName(siteSlug) + "=" + s.SessionID + "; Path=" + prefix + "; Max-Age=" + strconv.Itoa(core.REMOTE_GUI_SESSION_SEC) + "; HttpOnly; SameSite=Lax"
	if strings.ToLower(c.Headers["x-forwarded-proto"]) == "https" || reHTTPS.MatchString(c.Headers["origin"]) {
		cookie += "; Secure"
	}
	body := doc.Obj("url", prefix+"/_opm/app/"+siteSlug+"/status", "expiresAt", doc.ISO(time.UnixMilli(s.ExpiresAt)), "connected", core.RemoteConnected(deploymentID, siteSlug))
	return &cloud.Reply{Status: 200, Headers: map[string]string{"Content-Type": "application/json; charset=utf-8", "Set-Cookie": cookie, "Cache-Control": "no-store"}, Body: doc.MarshalString(body)}, nil
}

func agentRemoteGuiNext(c *cloud.Ctx) (any, error) {
	return doc.Obj("items", core.RemoteNext(c.Context, c.Agent.DeploymentID, c.Agent.SiteSlug, waitMs(c))), nil
}

func agentRemoteGuiWsRecv(c *cloud.Ctx) (any, error) {
	s := core.RemoteStreamOf(c.Params["streamId"], c.Agent.DeploymentID, c.Agent.SiteSlug)
	if s == nil {
		return doc.Obj("frames", []any{}, "closed", true), nil
	}
	return core.RemoteRecv(c.Context, s, waitMs(c)).Map(), nil
}

func agentRemoteGuiWsSend(c *cloud.Ctx) (any, error) {
	if len(c.Body) > 8*1024*1024 {
		return nil, cloud.E.TooLarge("at most 8 MiB per call")
	}
	s := core.RemoteStreamOf(c.Params["streamId"], c.Agent.DeploymentID, c.Agent.SiteSlug)
	if s == nil || s.Closed() {
		return doc.Obj("accepted", false), nil
	}
	v, err := c.JSON()
	if err != nil {
		return nil, err
	}
	b, _ := v.(*doc.Map)
	if v == nil {
		return nil, errors.New("body is null")
	}
	frames := []any{}
	for _, f := range b.Arr("frames") {
		m, ok := f.(*doc.Map)
		if !ok {
			continue
		}
		op := m.Val("op")
		if _, isStr := m.StrOK("dataB64"); isStr && (op == "text" || op == "binary" || op == "close") {
			frames = append(frames, m)
		}
	}
	return doc.Obj("accepted", core.RemoteToBrowser(s, frames)), nil
}

func agentRemoteGuiResult(c *cloud.Ctx) (any, error) {
	if len(c.Body) > core.REMOTE_GUI_RESPONSE_BODY_MAX {
		return nil, cloud.E.TooLarge("a response is at most " + strconv.Itoa(core.REMOTE_GUI_RESPONSE_BODY_MAX) + " bytes")
	}
	status := 0.0
	if s, ok := c.Headers[core.REMOTE_GUI_STATUS_HEADER]; ok {
		f, isNum := cloud.JSNumber(s)
		if !isNum {
			f = -1
		}
		status = f
	}
	if status != float64(int(status)) || status < 100 || status > 599 {
		return nil, cloud.E.Bad(core.REMOTE_GUI_STATUS_HEADER, "V_RANGE", "status must be 100–599")
	}
	headers := doc.New()
	if raw := c.Headers[core.REMOTE_GUI_HEADERS_HEADER]; raw != "" {
		v, err := doc.ParseString(core.DecodeUTF8(doc.B64Decode(raw)))
		if err != nil || v == nil {
			return nil, cloud.E.Bad(core.REMOTE_GUI_HEADERS_HEADER, "V_FORMAT", "headers must be base64 JSON")
		}
		if m, ok := v.(*doc.Map); ok {
			m.Each(func(k string, x any) {
				if _, isStr := x.(string); isStr {
					headers.Set(k, x)
				}
			})
		}
	}
	ok := core.RemoteDeliver(c.Agent.DeploymentID, c.Agent.SiteSlug, c.Params["reqId"], core.RemoteResult{Status: int(status), Headers: headers, Body: append([]byte(nil), c.Body...)})
	return doc.Obj("accepted", ok), nil
}
