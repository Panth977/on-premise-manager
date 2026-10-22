package agent

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/sign"
)

// peerMux is the /_opm/peer/* API (agentPort only, signed both ways).
func (a *Agent) peerMux() http.Handler {
	mux := http.NewServeMux()
	j := func(pattern string, h func(w http.ResponseWriter, r *http.Request) (any, *httpError), okStatus int) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			v, e := h(w, r)
			if e != nil {
				writeErr(w, e)
				return
			}
			writeJSON(w, okStatus, v)
		})
	}
	raw := func(pattern string, h http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if sw, ok := w.(*signedWriter); ok {
				sw.direct = true
				h(sw.raw, r)
				return
			}
			h(w, r)
		})
	}
	j("GET /_opm/peer/ping", func(w http.ResponseWriter, r *http.Request) (any, *httpError) { return a.selfPing(), nil }, 200)
	j("GET /_opm/peer/cloud-view", func(w http.ResponseWriter, r *http.Request) (any, *httpError) {
		if v := a.cloudView(); v != nil {
			return v, nil
		}
		return model.CloudView{DeploymentID: a.dep, Proposals: []model.ProposalMeta{}, TransfersToSite: []model.TransferMeta{}, Peers: []model.PeerInfo{}}, nil
	}, 200)
	j("GET /_opm/peer/have", func(w http.ResponseWriter, r *http.Request) (any, *httpError) { return a.haveList(), nil }, 200)
	raw("GET /_opm/peer/blobs/{sha256}", a.servePeerBlob)
	j("GET /_opm/peer/files", a.peerFiles, 200)                          // §21 #123
	raw("GET /_opm/peer/files/{fileId}/content", a.servePeerFileContent) // §21 #123
	j("POST /_opm/peer/fetch-request", a.handleFetchRequest, 202)
	j("GET /_opm/peer/revisions/{revId}", func(w http.ResponseWriter, r *http.Request) (any, *httpError) {
		rev, ok := a.revision(r.PathValue("revId"))
		if !ok {
			return nil, herr(model.ErrNotFound, "revision not found", nil)
		}
		caller := getRC(r).peer
		if len(rev.Config.Secrets) > 0 && caller != nil {
			slugs := []string{}
			for _, s := range rev.Config.Secrets {
				slugs = append(slugs, s.SecretSlug)
			}
			a.activity(nil, "system", "secret.fetched", "revision with secrets sent to site "+caller.Site, map[string]any{"siteSlug": caller.Site, "secretSlugs": slugs, "revId": rev.RevID})
		}
		return model.PeerRevisionResponse{Revision: *rev}, nil
	}, 200)
	j("POST /_opm/peer/proposals", func(w http.ResponseWriter, r *http.Request) (any, *httpError) {
		var push model.PeerProposalPush
		if e := readJSON(r, &push); e != nil {
			return nil, e
		}
		if push.Proposal.RevID != push.Revision.RevID {
			return nil, invalid("proposal and revision differ", nil)
		}
		if err := verifyRevision(&push.Revision); err != nil {
			return nil, invalid(err.Error(), nil)
		}
		stored := a.receiveProposal(&push.Proposal, &push.Revision)
		return model.StoredResponse{Stored: stored}, nil
	}, 200)
	j("POST /_opm/peer/relay", a.handlePeerRelay, 200)
	j("GET /_opm/peer/cloud-commits/{commitId}", a.peerCloudCommit, 200) // §21 #104
	j("GET /_opm/peer/branch", a.peerBranch, 200)
	j("GET /_opm/peer/actors", func(w http.ResponseWriter, r *http.Request) (any, *httpError) {
		return model.PeerActorsResponse{Actors: a.actors(0)}, nil
	}, 200)
	j("POST /_opm/peer/update/{updateId}/step", a.handleUpdateStep, 202)
	j("GET /_opm/peer/update/{updateId}", a.handleUpdateState, 200)
	j("POST /_opm/peer/update/{updateId}/terminate", a.handlePeerTerminate, 200) // §21 #110
	j("POST /_opm/peer/snapshots", a.handlePeerSnapshotStep, 202)
	j("GET /_opm/peer/snapshots", func(w http.ResponseWriter, r *http.Request) (any, *httpError) {
		return model.ListResponse[model.SnapshotSummary]{Items: a.ownSnapshotSummaries()}, nil
	}, 200)
	raw("GET /_opm/peer/snapshots/{snapshotId}/part", a.servePeerSnapshotPart)
	j("PUT /_opm/peer/snapshots/{snapshotId}/part", a.handlePutSnapshotPart, 200)
	j("PATCH /_opm/peer/snapshots/{snapshotId}", func(w http.ResponseWriter, r *http.Request) (any, *httpError) {
		var p model.PatchSnapshotRequest
		if e := readJSON(r, &p); e != nil {
			return nil, e
		}
		if e := a.patchOwnSnapshot(r.PathValue("snapshotId"), p.Note); e != nil {
			return nil, e
		}
		return model.OkResponse{Ok: true}, nil
	}, 200)
	j("DELETE /_opm/peer/snapshots/{snapshotId}", func(w http.ResponseWriter, r *http.Request) (any, *httpError) {
		if e := a.deleteOwnSnapshot(r.PathValue("snapshotId"), nil); e != nil {
			return nil, e
		}
		return model.OkResponse{Ok: true}, nil
	}, 200)
	raw("GET /_opm/peer/site/{rest...}", a.servePeerSite)
	raw("POST /_opm/peer/site/{rest...}", a.servePeerSite)
	raw("PATCH /_opm/peer/site/{rest...}", a.servePeerSite)  // §21 #90 forwarded PATCH …/agent/config
	raw("DELETE /_opm/peer/site/{rest...}", a.servePeerSite) // §21 #95 forwarded DELETE …/intents/{intentId}
	raw("PUT /_opm/peer/site/{rest...}", a.servePeerSite)    // §21 #97 forwarded PUT …/devtools/connections|saved|environments/{id}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc, he := a.spoolBody(r, 3<<30)
		if he != nil {
			writeErr(w, he)
			return
		}
		defer rc.cleanup()
		var hashHex string
		if rc.bodyFile != "" {
			hashHex = rc.bodyHash
		}
		caller, ve := a.verifyPeer(r, rc.body, hashHex)
		if ve != nil {
			a.log.Warn("mesh", "peer request refused", "path", r.URL.Path, "reason", ve.Reason, "error", ve.Msg)
			writeErr(w, herr(model.ErrUnauthenticated, ve.Msg, map[string]any{"reason": ve.Reason}))
			return
		}
		rc.peer = caller
		rc.surface = "agent-port"
		rc.ip = clientIP(r)
		rc.ua = r.UserAgent()
		if act := r.Header.Get("X-OPM-Actor"); act != "" {
			rc.actor = strings.ToLower(strings.TrimSpace(act))
		}
		if s := r.Header.Get("X-OPM-Session"); s != "" {
			rc.sessionID = s
		}
		r = withRC(r, rc)
		if rc.body != nil {
			r.Body = io.NopCloser(strings.NewReader(string(rc.body)))
		}
		sw := &signedWriter{hdr: http.Header{}, raw: w}
		mux.ServeHTTP(sw, r)
		if sw.direct {
			return
		}
		for k, v := range sw.hdr {
			w.Header()[k] = v
		}
		st := sw.status
		if st == 0 {
			st = 200
		}
		body := sw.buf.Bytes()
		for k, v := range sign.SignResponse(a.key, a.site, st, caller.Nonce, body, time.Now().Unix()+a.offset()) {
			w.Header().Set(k, v)
		}
		w.WriteHeader(st)
		_, _ = w.Write(body)
	})
}

// handlePeerRelay forwards an inner signed cloud request (§4.5): own internet → POST /v1/agent/relay; tunnel holder →
// enqueue unchanged; else 503 no-internet.
func (a *Agent) handlePeerRelay(w http.ResponseWriter, r *http.Request) (any, *httpError) {
	var req model.PeerRelayRequest
	if e := readJSON(r, &req); e != nil {
		return nil, e
	}
	caller := getRC(r).peer
	site := ""
	if caller != nil {
		site = caller.Site
	}
	if a.hasInternet() || (!a.directDown() && a.enrolled) {
		body, _ := json.Marshal(model.RelayRequest{ForSite: site, Request: req.Request})
		hdr := a.signedHeaders("POST", "/v1/agent/relay", body)
		hdr["Content-Type"] = "application/json; charset=utf-8"
		resp, err := a.directRaw(r.Context(), "POST", "/v1/agent/relay", body, hdr, 90*time.Second)
		if err == nil {
			a.markDirect(true)
			if resp.Status >= 300 {
				ae := &apiError{Status: resp.Status}
				_ = json.Unmarshal(resp.Body, &ae.Body)
				return nil, toHTTPErr(ae)
			}
			var rr model.RelayResponse
			if err := json.Unmarshal(resp.Body, &rr); err != nil {
				return nil, herr(model.ErrInternal, "relay answer: "+err.Error(), nil)
			}
			return rr, nil
		}
		a.mu.Lock()
		a.directFailAt = time.Now()
		a.mu.Unlock()
	}
	if a.tun.active() != nil {
		cr, err := a.tunnelCall(r.Context(), req.Request, 90*time.Second)
		if err != nil {
			return nil, herr(model.ErrNoInternet, "tunnel: "+err.Error(), nil)
		}
		hdr := map[string]string{}
		for k := range cr.Header {
			hdr[k] = cr.Header.Get(k)
		}
		b64 := base64.StdEncoding.EncodeToString(cr.Body)
		return model.RelayResponse{Status: cr.Status, Headers: hdr, BodyB64: &b64}, nil
	}
	return nil, herr(model.ErrNoInternet, "site "+a.site+" has no internet", nil)
}

func (a *Agent) servePeerBlob(w http.ResponseWriter, r *http.Request) {
	h := strings.TrimPrefix(r.PathValue("sha256"), "sha256:")
	p, ok := a.blobPath(h)
	if !ok {
		writeErr(w, herr(model.ErrNotFound, "blob not here", nil))
		return
	}
	f, err := os.Open(p)
	if err != nil {
		writeErr(w, herr(model.ErrNotFound, "blob not here", nil))
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-OPM-Sha256", "sha256:"+h)
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// servePeerSite maps /_opm/peer/site/<rest> to the local per-site handler of this site.
func (a *Agent) servePeerSite(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("rest")
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + rest
	r2.URL.RawPath = ""
	a.siteMux().ServeHTTP(w, r2)
}

func errf(format string, a ...any) error { return fmt.Errorf(format, a...) }
