package agent

// Docker container naming (SPEC-final §15.1, §21 #88): the docker name of a managed container is its containerSlug
// (agent.json containerNames "plain", the default) or opm_<deploymentId>_<siteSlug>_<containerSlug> ("prefixed").
// Managed containers are always found by their labels; the name only has to follow the mode. At agent start and before
// every apply the agent renames, in place, every managed container whose name differs (no recreate: the name is not in
// the spec hash).

import (
	"context"
	"sync"
)

var renameMu sync.Mutex // one migration at a time (start goroutine vs apply)

// migrateContainerNames renames the managed containers of this site to the naming mode (best effort; a clash is
// reported by staging, never forced).
func (a *Agent) migrateContainerNames(ctx context.Context, why string) {
	if a.dc == nil || a.drv == nil {
		return
	}
	renameMu.Lock()
	defer renameMu.Unlock()
	done, skipped, err := a.drv.MigrateNames(ctx)
	if err != nil {
		a.log.Warn("driver", "container name migration failed", "when", why, "error", err.Error())
		return
	}
	if len(done) > 0 || len(skipped) > 0 {
		names := make([]string, 0, len(done))
		for _, r := range done {
			names = append(names, r.From+" → "+r.To)
		}
		a.log.Info("driver", "container names migrated", "when", why, "mode", a.cfg.ContainerNames, "renamed", names, "skipped", skipped)
	}
}
