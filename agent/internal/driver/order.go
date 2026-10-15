package driver

// Start scheduling + the slug-alias self-check (SPEC-final §14.5 step 3, §15.5, §21 #93).
//
// Root cause of the user's failed update (deployment JzOFFecGJv7AdGWtCjjf seq 3, 15 containers, depends: [] everywhere):
// APPLY started the containers ONE BY ONE in slug order and waited for each to be ready before creating the next, so
// northwind-backend-admin (e…) waited 300 s for `mqtt` (m…), which was not created yet → getaddrinfo EAI_AGAIN mqtt, 13
// restarts, rollback. Now: containers start in the start order (referenced first) and a container waits ONLY for its
// explicit + implicit depends; every other readiness wait runs in parallel.

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// StartStep is one container of an APPLY start phase.
type StartStep struct {
	Slug string
	// Wait: containerSlugs that must be completed before this one starts (explicit + implicit depends). Slugs that are
	// not EARLIER steps of this run (kept + ready containers; a later step in a reference cycle) are ignored.
	Wait []string
	// Start creates + starts the container (nil = keep: only Ready runs) and returns its id.
	Start func(ctx context.Context) (string, error)
	// ID of a kept container (Start nil).
	ID string
	// Ready waits for readiness (§15.4).
	Ready func(ctx context.Context, id string) error
}

// StartResult is the outcome of one step. Phase: "start" (Start failed), "ready" (Ready failed or ok), "dependency"
// (not started: a dependency failed), "cancelled" (not started / wait cancelled after another failure).
type StartResult struct {
	Slug  string
	ID    string
	Err   error
	Phase string
}

// ErrDependencyFailed: a step was not started because one of its Wait slugs failed.
var ErrDependencyFailed = errors.New("dependency failed")

// RunStarts runs the steps in order: each step waits for its Wait slugs, starts, and its readiness wait runs in the
// background. failFast (an update): the first failure stops launching and cancels the remaining readiness waits;
// otherwise (a rollback, best effort) every step runs and a failed dependency does not block its dependents.
// onResult is called once per step, in completion order, from any goroutine (serialised by RunStarts).
func RunStarts(ctx context.Context, steps []StartStep, failFast bool, onResult func(StartResult)) []StartResult {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	results := map[string]StartResult{}
	done := map[string]chan struct{}{}
	for _, s := range steps {
		done[s.Slug] = make(chan struct{})
	}
	var firstErr bool
	report := func(r StartResult) {
		mu.Lock()
		defer mu.Unlock()
		results[r.Slug] = r
		if r.Err != nil && r.Phase != "cancelled" && failFast && !firstErr {
			firstErr = true
			cancel()
		}
		if onResult != nil {
			onResult(r)
		}
		close(done[r.Slug])
	}
	failed := func(slug string) bool {
		mu.Lock()
		defer mu.Unlock()
		r, ok := results[slug]
		return ok && r.Err != nil
	}
	var wg sync.WaitGroup
	seen := map[string]bool{} // only EARLIER steps are awaited (a reference cycle puts a dependency later: never wait for it)
	for _, s := range steps {
		s := s
		depFailed := ""
		for _, w := range s.Wait {
			ch, ok := done[w]
			if !ok || w == s.Slug || !seen[w] {
				continue
			}
			select {
			case <-ch:
			case <-ctx.Done():
			}
			if failed(w) && depFailed == "" {
				depFailed = w
			}
		}
		seen[s.Slug] = true
		if ctx.Err() != nil {
			report(StartResult{Slug: s.Slug, Err: ctx.Err(), Phase: "cancelled"})
			continue
		}
		if depFailed != "" && failFast {
			report(StartResult{Slug: s.Slug, Err: errors.Join(ErrDependencyFailed, errors.New(depFailed)), Phase: "dependency"})
			continue
		}
		id := s.ID
		if s.Start != nil {
			var err error
			if id, err = s.Start(ctx); err != nil {
				report(StartResult{Slug: s.Slug, ID: id, Err: err, Phase: "start"})
				continue
			}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { // §21 #110 a panic in a readiness wait fails that container, not the agent
				if r := recover(); r != nil {
					report(StartResult{Slug: s.Slug, ID: id, Err: fmt.Errorf("readiness check panicked: %v", r), Phase: "ready"})
				}
			}()
			err := s.Ready(ctx, id)
			phase := "ready"
			if err != nil && ctx.Err() != nil && errors.Is(err, ctx.Err()) {
				phase = "cancelled"
			}
			report(StartResult{Slug: s.Slug, ID: id, Err: err, Phase: phase})
		}()
	}
	wg.Wait()
	out := make([]StartResult, 0, len(steps))
	for _, s := range steps {
		out = append(out, results[s.Slug])
	}
	return out
}

// AliasRepair is one repaired container of EnsureAliases.
type AliasRepair struct {
	ContainerSlug, ID, Reason string
}

// EnsureAliases is the self-check after every apply / start (§21 #93): every RUNNING managed container of the site must
// answer on its containerSlug on the site network (endpoint Aliases or DNSNames). A container that is missing from the
// network, or whose endpoint lacks the slug, is reconnected with its aliases (slug + the prefixed legacy name). A
// managed container is never left without its slug alias.
func (d *Driver) EnsureAliases(ctx context.Context) ([]AliasRepair, error) {
	ex, err := d.Existing(ctx)
	if err != nil {
		return nil, err
	}
	net := NetworkName(d.Dep, d.Site)
	var out []AliasRepair
	for _, e := range ex {
		if !e.Running {
			continue
		}
		cj, err := d.DC.ContainerInspect(ctx, e.ID)
		if err != nil || !cj.State.Running {
			continue
		}
		aliases := []string{e.ContainerSlug}
		if p := PrefixedName(d.Dep, d.Site, e.ContainerSlug); p != e.ContainerSlug {
			aliases = append(aliases, p)
		}
		ep, on := cj.NetworkSettings.Networks[net]
		reason := ""
		switch {
		case !on:
			reason = "not on the site network"
		case !hasName(ep.Aliases, e.ContainerSlug) && !hasName(ep.DNSNames, e.ContainerSlug):
			reason = "slug alias missing"
		default:
			continue
		}
		if on {
			if err := d.DC.NetworkDisconnect(ctx, net, e.ID, true); err != nil {
				d.Log.Warn("driver", "alias repair: disconnect failed", "container", e.ContainerSlug, "error", err.Error())
				continue
			}
		}
		if err := d.DC.EnsureNetwork(ctx, net, map[string]string{LDeployment: d.Dep, LSite: d.Site}); err != nil {
			d.Log.Warn("driver", "alias repair: network", "error", err.Error())
			continue
		}
		if err := d.DC.NetworkConnect(ctx, net, e.ID, aliases); err != nil {
			d.Log.Warn("driver", "alias repair: connect failed", "container", e.ContainerSlug, "error", err.Error())
			continue
		}
		d.Log.Warn("driver", "network alias repaired", "container", e.ContainerSlug, "id", e.ID[:12], "reason", reason, "aliases", aliases)
		out = append(out, AliasRepair{ContainerSlug: e.ContainerSlug, ID: e.ID, Reason: reason})
	}
	return out, nil
}

func hasName(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
