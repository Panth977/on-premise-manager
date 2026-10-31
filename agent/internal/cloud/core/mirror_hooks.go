package core

import (
	"context"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
)

// Enqueue side of the mirror, driven by document triggers on the OPM history and by the agent heartbeat ("agent online":
// its branch head ≠ the mirrored head → catch-up). Best effort: never fails.

func commitOpts(d *doc.Map, reason string) EnqueueOpts {
	return EnqueueOpts{Op: "commit", OpmID: JSString(d.Val("commitId")), Seq: d.Val("seq"), CreatedAt: JSString(d.Val("createdAt")), Reason: reason}
}

func hookEnqueue(name string, ref *doc.Map, o func() EnqueueOpts) {
	MirrorHook(name, func() error {
		t, err := TargetOf(ref)
		if err != nil {
			return err
		}
		_, err = Enqueue(t, o())
		return err
	})
}

// OnTemplateCommit is onTemplateCommit.
func OnTemplateCommit(templateID string, d *doc.Map) {
	hookEnqueue("template-commit", RefOf("template", templateID, "", ""), func() EnqueueOpts { return commitOpts(d, "commit") })
}

// OnDeploymentCommit is onDeploymentCommit.
func OnDeploymentCommit(deploymentID string, d *doc.Map) {
	hookEnqueue("deployment-commit", RefOf("deployment", "", deploymentID, ""), func() EnqueueOpts { return commitOpts(d, "commit") })
}

// OnAgentCommit is onAgentCommit.
func OnAgentCommit(deploymentID, siteSlug string, d *doc.Map) {
	hookEnqueue("agent-commit", RefOf("agent", "", deploymentID, siteSlug), func() EnqueueOpts { return commitOpts(d, "agent-push") })
}

// OnVolumeVersion is onVolumeVersion: a persist-to-cloud snapshot (origin snapshot only) → the site's agent branch.
func OnVolumeVersion(deploymentID string, d *doc.Map) {
	site, ok := d.StrOK("siteSlug")
	if d.Val("origin") != "snapshot" || !ok {
		return
	}
	hookEnqueue("volume-snapshot", RefOf("agent", "", deploymentID, site), func() EnqueueOpts {
		return EnqueueOpts{Op: "volume-snapshot", OpmID: JSString(d.Val("versionId")), Seq: d.Val("seq"), CreatedAt: JSString(d.Val("createdAt")), Reason: "snapshot"}
	})
}

func onAgentHeartbeat(deploymentID, siteSlug string, head any) {
	id, ok := head.(string)
	if !ok || id == "" {
		return
	}
	MirrorHook("agent-online", func() error {
		t, err := TargetOf(RefOf("agent", "", deploymentID, siteSlug))
		if err != nil {
			return err
		}
		s, err := Mcol.Branches().Doc(MirrorBranchDocId(t.GitBranch)).Get()
		if err != nil {
			return err
		}
		if s.Data().Map("last").Val("opmId") == id {
			return nil
		}
		_, err = Enqueue(t, EnqueueOpts{Op: "catch-up", OpmID: "online-" + id, CreatedAt: "9999", Reason: "agent-online"})
		return err
	})
}

func init() {
	OnAgentHeartbeat = onAgentHeartbeat
	cloud.RegisterTrigger("mirrorTemplateCommit", func(e driver.DocEvent) error {
		if e.After != nil {
			OnTemplateCommit(e.Params["templateId"], e.After)
		}
		return nil
	})
	cloud.RegisterTrigger("mirrorDeploymentCommit", func(e driver.DocEvent) error {
		if e.After != nil {
			OnDeploymentCommit(e.Params["deploymentId"], e.After)
		}
		return nil
	})
	cloud.RegisterTrigger("mirrorAgentCommit", func(e driver.DocEvent) error {
		if e.After != nil {
			OnAgentCommit(e.Params["deploymentId"], e.Params["siteSlug"], e.After)
		}
		return nil
	})
	cloud.RegisterTrigger("mirrorVolumeVersion", func(e driver.DocEvent) error {
		if e.After != nil {
			OnVolumeVersion(e.Params["deploymentId"], e.After)
		}
		return nil
	})
	cloud.RegisterTrigger("mirrorQueue", func(e driver.DocEvent) error {
		if b, ok := e.After.StrOK("gitBranch"); ok {
			DrainBranch(b, 400_000)
		}
		return nil
	})
	cloud.RegisterJob(driver.ScheduledJob{Name: "mirrorRetry", Schedule: "every 5 minutes", TimeoutSeconds: 540, Run: func(context.Context) error {
		if err := RecoverStuck(); err != nil {
			return err
		}
		n, err := DrainDue(480_000)
		if err != nil {
			return err
		}
		if n > 0 {
			driver.Log.Info("github mirror retry", "processed", n)
		}
		return nil
	}})
}
