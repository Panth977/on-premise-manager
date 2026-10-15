package agent

// APPLY start order + waits (SPEC-final §14.5 step 3, §15.5, §21 #93) and the slug-alias self-check.

import (
	"context"

	"opm/agent/internal/driver"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
)

// startPlan: the start order of the enabled containers (validate.StartOrder over explicit + implicit + suggested
// references) and, per container, what it waits for (explicit + implicit depends; suggested ones only order the start).
func (a *Agent) startPlan(cfg *model.Config, cat *model.RevisionCatalog, site *model.Site, builts []*driver.Built) ([]string, map[string][]string) {
	waits := map[string][]string{}
	if site == nil {
		return nil, waits
	}
	inf := validate.InferDepends(validate.DependsInput{Site: &site.Config, Images: driver.CatalogImages(cat), Files: cfg.Files, FileContents: a.fileContents(cfg, builts)})
	order, cycle := validate.StartOrder(&site.Config, inf)
	var enabled []string
	for _, c := range site.Config.Containers {
		if c.Enabled {
			enabled = append(enabled, c.ContainerSlug)
		}
	}
	implicit := map[string][]string{}
	for _, d := range inf {
		waits[d.ContainerSlug] = validate.WaitDepends(d, enabled)
		for _, x := range d.Implicit {
			implicit[d.ContainerSlug] = append(implicit[d.ContainerSlug], x.ContainerSlug)
		}
	}
	a.log.Info("driver", "start order", "order", order, "implicitDepends", implicit, "cycle", cycle)
	if len(cycle) > 0 {
		a.log.Warn("driver", "containers reference each other: they start in the declared order", "containers", cycle)
	}
	return order, waits
}

// checkAliases is the self-check after every apply / at start (§21 #93): every running managed container answers on its
// containerSlug on the site network; a missing alias is repaired (reconnect with the aliases) and logged.
func (a *Agent) checkAliases(ctx context.Context, why string) {
	if a.dc == nil || a.drv == nil {
		return
	}
	rep, err := a.drv.EnsureAliases(ctx)
	if err != nil {
		a.log.Warn("driver", "alias self-check failed", "when", why, "error", err.Error())
		return
	}
	a.log.Debug("driver", "alias self-check", "when", why, "repaired", len(rep))
	for _, r := range rep {
		a.log.Warn("driver", "slug alias repaired", "when", why, "container", r.ContainerSlug, "reason", r.Reason)
	}
}
