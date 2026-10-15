package export

import (
	"fmt"
	"sort"
	"strings"
)

// NotEquivalent is the list (README + docs) of what the agent does and the compose export does not.
var NotEquivalent = []string{
	"No agent: no drift detection, no branches / commits / merges on the machine, no agent GUI (/_opm), no dev tools, no transfers, no self-upgrade.",
	"No snapshots / backups / rollback (the volume `backup` flag has no effect) and no persist-to-cloud sync (an agent branch starts with the last snapshot, but changes are not sent back).",
	"The site address is not detected: set OPM_SITE_HOST (and the other sites' OPM_SITE_<SITE>_HOST) in .env when the config leaves them on auto.",
	"Readiness: containers without a health check count as started at once (the agent waits until they run 10 s without a restart); `log` health checks have no compose equivalent (dependents only wait for the start).",
	"http / tcp health checks run inside the container (the probe tool is mounted read-only at /.opm); the agent checks the published loopback port from the host. The agent's loopback-only ephemeral port publications are not created: the edge reaches containers on the site network.",
	"The edge runs the official caddy image with standard modules: a stopped container answers 502 → the 503 page after a dial error (the agent answers 503 at once); the remote address the edge sees is the Docker gateway / client NAT address. ACME dns-01 needs the edge/Dockerfile build (xcaddy + the caddy-dns module).",
	"Single-file volumes live in ./.opm-data/volumes/<volume> (folders: named Docker volumes); File locations are ignored (Files are mounted from ./files or .opm-runtime/files). File modes in git are 0644 (the agent writes 0444 / 0400).",
	"Images are pulled from their registry (digest-pinned, the platform OPM imported); the agent loads them from the OPM cloud. Private registries need `docker login` first.",
	"Env values derived from secrets through a filter are set by an entrypoint wrapper (the process sees them; `docker inspect` does not).",
}

func (x *exporter) readme(states []*siteState) string {
	var b strings.Builder
	br := x.req.Branch
	c := x.req.Commit
	fmt.Fprintf(&b, "# %s — OPM compose export\n\n", mdEsc(br.Label))
	fmt.Fprintf(&b, "Git branch `%s` mirrors the OPM %s branch **%s**. This commit = OPM commit `%s`", br.GitBranch, br.Kind, mdEsc(br.Label), c.ID)
	if c.Seq != nil {
		fmt.Fprintf(&b, " (seq %d)", *c.Seq)
	}
	fmt.Fprintf(&b, " by %s, %s:\n\n> %s\n\n", mdEsc(c.Author.Name), c.CreatedAt, strings.ReplaceAll(strings.TrimSpace(firstLine(c.Message)), "\n", " "))
	b.WriteString("Every site directory runs the same containers, volumes and edge the OPM agent would run — WITHOUT the agent.\n\n")
	b.WriteString("## Prerequisites\n\n")
	b.WriteString("- Docker Engine 24 or newer and Docker Compose v2.24 or newer (`docker version`, `docker compose version`).\n")
	b.WriteString("- Linux amd64 or arm64 (the opm-init helper ships for both).\n")
	hosts := map[string]bool{}
	for _, im := range x.req.Images {
		if im.Private && im.RegistryHost != nil {
			hosts[*im.RegistryHost] = true
		}
	}
	if len(hosts) > 0 {
		b.WriteString("- Log in to the private registries first:\n")
		for _, h := range sortedKeys(hosts) {
			fmt.Fprintf(&b, "  - `docker login %s`\n", h)
		}
	} else {
		b.WriteString("- Every image is public (no `docker login` needed); if a registry asks for credentials, `docker login <host>` first.\n")
	}
	b.WriteString("\n## Run a site\n\n```sh\ngit clone --branch " + br.GitBranch + " --single-branch <this repository> opm\n")
	b.WriteString("cd opm/sites/<site>\ncp .env.example .env    # fill every value (never commit .env)\ndocker compose up -d\n```\n\n")
	b.WriteString("`opm-init` runs first: it checks .env, renders the files that need .env values (e.g. a Mosquitto password file from a secret), seeds every EMPTY volume once with its starter content (marker `.opm-seeded`) and installs the health-check probe; the containers then start in the agent's start order, waiting for their dependencies like the agent.\n\n")
	b.WriteString("## Sites\n\n")
	sts := append([]*siteState(nil), states...)
	sort.Slice(sts, func(i, j int) bool { return sts[i].site < sts[j].site })
	for _, st := range sts {
		fmt.Fprintf(&b, "### `sites/%s`\n\n", st.site)
		n := 0
		for _, bt := range st.a.builts {
			if bt != nil {
				n++
			}
		}
		fmt.Fprintf(&b, "- %d container(s); edge on port %d", n, portOr(st.a.httpPort, 80))
		if st.a.httpsPort != 0 {
			fmt.Fprintf(&b, " and %d (TLS)", st.a.httpsPort)
		}
		b.WriteString(".\n")
		var vals []string
		for _, d := range st.inputs {
			if st.used[d.Name] {
				vals = append(vals, "`"+d.Name+"`")
			}
		}
		if len(vals) > 0 {
			fmt.Fprintf(&b, "- Fill in .env: %s.\n", strings.Join(vals, ", "))
		} else {
			b.WriteString("- .env needs no values.\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("## Layout\n\n")
	b.WriteString("| path | what |\n|---|---|\n")
	b.WriteString("| `opm/config.json` | the OPM commit config (secret values stripped) + commit metadata |\n")
	b.WriteString("| `opm/export.json` | the export input (opm-init re-renders the .env-dependent parts from it) |\n")
	b.WriteString("| `opm/bin/` | opm-render (linux amd64 / arm64): opm-init, health probe, env wrapper |\n")
	b.WriteString("| `sites/<site>/docker-compose.yml` | the site |\n")
	b.WriteString("| `sites/<site>/.env.example` | the values that are not in git |\n")
	b.WriteString("| `sites/<site>/files/` | the Files, mounted read-only (`*.opm-template`: rendered at `up` from .env) |\n")
	b.WriteString("| `sites/<site>/volumes/<volume>/` | starter content (agent branches: the last persist-to-cloud snapshot) |\n")
	b.WriteString("| `sites/<site>/edge/` | caddy.json for the official caddy image + the not-found pages |\n\n")
	b.WriteString("## Not equivalent without the agent\n\n")
	for _, l := range NotEquivalent {
		b.WriteString("- " + l + "\n")
	}
	if len(x.notes) > 0 {
		b.WriteString("\n## Notes for this commit\n\n")
		for _, n := range x.notesOut() {
			s := ""
			if n.Site != nil {
				s = " (" + *n.Site + ")"
			}
			fmt.Fprintf(&b, "- `%s`%s %s\n", n.Code, s, mdEsc(n.Message))
		}
	}
	return b.String()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func mdEsc(s string) string { return strings.NewReplacer("<", "&lt;", ">", "&gt;").Replace(s) }
