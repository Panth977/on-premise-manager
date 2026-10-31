package apitest

import (
	"fmt"
	"testing"

	"opm/agent/internal/cloud/shared"
	"opm/agent/internal/ulid"
)

// wgMakeCommit is makeCommit() of @opm/shared.
func wgMakeCommit(t testing.TB, in A) J {
	t.Helper()
	c, err := shared.MakeCommit(wgDocMap(t, JOf(in)))
	NoErr(t, err)
	return JOf(c)
}

// Port of backend/test/36-site-head.test.ts.
func TestSiteHead(t *testing.T) {
	t.Run("O2 + O3: agent-branch head → status inHead / headSeq, persist-to-cloud blob upload", func(t *testing.T) {
		const MiB = 1024 * 1024
		admin := NewPerson(t, "admin")
		dep := HelloDeployment(t, admin)
		id := dep.ID()
		app := EnrollSite(t, admin, id, "app")
		UpdateDoc(t, "deployments/"+id+"/agents/app", A{"agentVersion": "0.16.0-test"})
		// a release with persist-to-cloud on the app volume
		d0 := admin.Real.Call(t, "getDeployment", A{"deploymentId": id})
		ci := DepInput(t, admin.Real, id)
		site := ci.At("sites").Find("siteSlug", "app")
		site.At("config.volumes.0").Map()["persist"] = A{"exclude": L{}, "maxBytes": 10 * MiB, "auto": true}
		c1 := admin.Real.Call(t, "commitDeployment", A{"deploymentId": id, "body": A{"message": "persist on", "baseCommitId": d0.At("branch.head.commitId"), "config": ci}})
		// the site merges it on its own branch and pushes the mirror
		got := app.API.Call(t, "getAgentCommit", A{"commitId": c1.At("commitId")})
		own := A{"kind": "agent", "deploymentId": id, "siteSlug": "app"}
		author := A{"kind": "person", "email": "ops@site.test", "uid": nil, "name": nil, "siteSlug": "app"}
		a1 := wgMakeCommit(t, A{"commitId": ulid.New(), "branch": own, "parent": nil, "kind": "merge", "message": fmt.Sprintf("merge cloud seq %d", c1.At("seq").Int()), "author": author, "createdAt": Now(), "config": got.At("commit.config"), "catalog": got.At("commit.catalog"), "source": A{"branch": A{"kind": "deployment", "deploymentId": id}, "commitId": c1.At("commitId"), "seq": c1.At("seq"), "label": dep.Dep.At("label"), "picks": "all", "skipped": 0}, "scope": "app"})
		app.API.Call(t, "pushAgentBranch", A{"body": A{"commits": L{a1}}})
		// §21 #117 any enrolled site uploads the bytes of its commits (no persist-to-cloud head needed)
		content := `{"x":"` + ulid.New() + `"}`
		sha := "sha256:" + Sha256Hex(content)
		// heartbeat with the agent-branch commit id as the head
		app.API.Call(t, "heartbeat", A{"body": A{"siteSlug": "app", "agentVersion": "0.16.0-test", "headRevId": a1.At("commitId"), "staging": nil, "peersSeen": L{}, "metrics": A{"cpu": 0, "memPct": 0, "diskPct": 0}, "edge": nil, "keyVersion": 1, "advertiseHost": "127.0.0.1", "agentPort": 7080, "ip": "127.0.0.1", "hostname": "h", "branch": A{"head": A{"commitId": a1.At("commitId"), "seq": a1.At("seq"), "hash": a1.At("hash")}, "mergedFrom": A{"commitId": c1.At("commitId"), "seq": c1.At("seq")}, "uncommitted": nil}}})
		// O2: status
		st := admin.Real.Call(t, "getStatus", A{"deploymentId": id})
		row := st.At("sites").Find("siteSlug", "app")
		Eq(t, row.At("inHead"), true, "inHead from the deployment branch head")
		Eq(t, row.At("headRevId"), a1.At("commitId"))
		Eq(t, row.At("headSeq"), c1.At("seq"), "headSeq = the deployment seq the site runs")
		// O3: persist-to-cloud upload from the agent-branch head
		put := app.API.Call(t, "agentPutBlob", A{"sha256": sha, "body": content})
		Eq(t, put.At("sha256"), sha)
		// the volume content view sees persist in the head the site runs
		vc := admin.Real.Call(t, "getVolumeContent", A{"deploymentId": id, "siteSlug": "app", "volumeSlug": site.At("config.volumes.0.volumeSlug")})
		Eq(t, vc.At("persistInHead"), true)
	})
}
