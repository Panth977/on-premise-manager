package export

import (
	"fmt"
	"sort"
	"strings"
)

// NotEquivalent is the list (README + docs) of what the agent does and the Docker-only setup does not.
var NotEquivalent = []string{
	"No agent: no drift detection, no commit history kept by the machine itself (git is the history), no agent GUI (/_opm), no dev tools, no transfers, no self-upgrade. Portainer is the GUI.",
	"No automatic snapshots / backups / rollback (the volume `backup` flag has no effect) and no persist-to-cloud sync. Back up volumes yourself (README: Back up a volume).",
	"The site address is not detected: set OPM_SITE_HOST (and the other sites' OPM_SITE_<SITE>_HOST) in site.env when the config leaves them on auto.",
	"Readiness: containers without a health check count as started at once (the agent waits until they run 10 s without a restart); `log` health checks have no compose equivalent (dependents only wait for the start).",
	"http / tcp health checks run inside the container with a static busybox (mounted read-only at /.tools); the agent checks the published loopback port from the host. The agent's loopback-only ephemeral port publications are not created: the edge reaches containers on the site network.",
	"The edge runs the official caddy image with standard modules: a stopped container answers 502 → the 503 page after a dial error (the agent answers 503 at once); the remote address the edge sees is the Docker gateway / client NAT address. ACME dns-01 needs the edge/Dockerfile build (xcaddy + the caddy-dns module).",
	"Single-file volumes live in ./.data/volumes/<volume> (folders: named Docker volumes); File locations are ignored (Files are mounted from .runtime/files with the agent's modes).",
	"Images are pulled from their registry (digest-pinned, the platform OPM imported); the agent loads them from the OPM cloud. Private registries need `docker login` first.",
	"Values derived from secrets through a filter are computed by deploy.sh with standard tools: upper / lower change ASCII letters only, trim removes ASCII white space only, and mosquitto_passwd uses a random salt (the hash differs from the agent's; the password is the same).",
}

func (x *exporter) repoURL() string {
	if x.req.Repository != "" {
		return "https://github.com/" + x.req.Repository + ".git"
	}
	return "<the URL of this repository>"
}

// siteBranch is the git branch a machine keeps for one site (the mirror never writes it).
func (x *exporter) siteBranch(site string) string {
	if x.req.Branch.Kind == "template" {
		return "template/" + x.req.Branch.EntitySlug + "/site/" + site
	}
	return "deployment/" + x.req.Branch.EntitySlug + "/site/" + site
}

// cloudBranch is what a machine merges from: the deployment's cloud branch (agent / uncommitted branches: themselves).
func (x *exporter) cloudBranch() string { return x.req.Branch.GitBranch }

func (x *exporter) checkoutDir() string { return "/opt/" + sanitizeProject(x.req.Branch.EntitySlug) }

func (x *exporter) registryLogins() []string {
	hosts := map[string]bool{}
	for _, im := range x.req.Images {
		if im.Private && im.RegistryHost != nil {
			hosts[*im.RegistryHost] = true
		}
	}
	return sortedKeys(hosts)
}

func (x *exporter) readme(states []*siteState) string {
	var b strings.Builder
	br := x.req.Branch
	c := x.req.Commit
	fmt.Fprintf(&b, "# %s\n\n", mdEsc(br.Label))
	fmt.Fprintf(&b, "Git branch `%s` = the %s **%s** at commit `%s`", br.GitBranch, br.Kind, mdEsc(br.Label), c.ID)
	if c.Seq != nil {
		fmt.Fprintf(&b, " (seq %d)", *c.Seq)
	}
	fmt.Fprintf(&b, " by %s, %s:\n\n> %s\n\n", mdEsc(c.Author.Name), c.CreatedAt, strings.ReplaceAll(strings.TrimSpace(firstLine(c.Message)), "\n", " "))
	b.WriteString("Every `sites/<site>` folder runs that site with **Docker only**: plain `docker compose`, " +
		"[Caddy](https://caddyserver.com) as the router (the site's hosts, routes and TLS) and [Portainer](https://www.portainer.io) as " +
		"the GUI. No agent and no access to the cloud is needed on the machine: this is the way to run a site when the " +
		"agent is not allowed. The cloud keeps pushing new commits to this branch; each machine keeps its own branch " +
		"(`" + x.siteBranch("<site>") + "`) and merges them.\n\n")
	b.WriteString("Open `sites/<site>/README.md` for the exact steps of a site.\n\n")
	b.WriteString("## Sites\n\n| site | containers | edge | runbook |\n|---|---|---|---|\n")
	sts := append([]*siteState(nil), states...)
	sort.Slice(sts, func(i, j int) bool { return sts[i].site < sts[j].site })
	for _, st := range sts {
		n := 0
		for _, bt := range st.a.builts {
			if bt != nil {
				n++
			}
		}
		edge := fmt.Sprintf("port %d", portOr(st.a.httpPort, 80))
		if st.a.httpsPort != 0 {
			edge += fmt.Sprintf(" + %d (TLS)", st.a.httpsPort)
		}
		fmt.Fprintf(&b, "| `%s` | %d | %s | [sites/%s/README.md](sites/%s/README.md) |\n", st.site, n, edge, st.site, st.site)
	}
	b.WriteString("\n## Branches\n\n")
	b.WriteString("| branch | written by | what |\n|---|---|---|\n")
	fmt.Fprintf(&b, "| `%s` | the cloud | what the sites should run; never commit to it |\n", x.cloudBranch())
	fmt.Fprintf(&b, "| `%s` | that machine | what the site runs: the cloud commits merged + the changes made on the machine |\n\n", x.siteBranch("<site>"))
	b.WriteString("## Layout of a site folder\n\n")
	b.WriteString("| path | what |\n|---|---|\n")
	b.WriteString("| `README.md` | the runbook of the site |\n")
	b.WriteString("| `deploy.sh` | the one command: renders the values from site.env, then `docker compose up -d` |\n")
	b.WriteString("| `site.env.example` | the values that are not in git (secrets, ...): copy to `site.env` (never committed) |\n")
	b.WriteString("| `docker-compose.yml` | the containers + init (seeds volumes) + edge (Caddy) + portainer |\n")
	b.WriteString("| `setup.sh`, `init.sh` | used by deploy.sh / the init service (plain sh, official busybox image) |\n")
	b.WriteString("| `files/` | the files mounted into containers (`*.template`: filled in from site.env by deploy.sh) |\n")
	b.WriteString("| `volumes/<volume>/` | starter content, written once into an EMPTY volume |\n")
	b.WriteString("| `edge/` | the Caddy config (caddy.json) + the not-found pages |\n")
	b.WriteString("| `templates/` | the other files deploy.sh fills in (secret files, derived values, starter files) |\n")
	b.WriteString("| `.env`, `.runtime/`, `.data/` | written on the machine by deploy.sh (git-ignored) |\n\n")
	b.WriteString("## Not equivalent to the agent\n\n")
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

// siteReadme is sites/<site>/README.md: the exact runbook of one site.
func (x *exporter) siteReadme(st *siteState, required []deferredInput) string {
	site := st.site
	cloud, mine, dir := x.cloudBranch(), x.siteBranch(site), x.checkoutDir()
	var b strings.Builder
	fmt.Fprintf(&b, "# Site `%s` with Docker only\n\n", site)
	fmt.Fprintf(&b, "This folder runs site **%s** of **%s** without the agent: `docker compose` + Caddy (edge) + Portainer (GUI). ", site, mdEsc(x.req.Branch.Label))
	b.WriteString("Every step below is a plain command; nothing needs access to the cloud. Git is the history: the cloud pushes to ")
	fmt.Fprintf(&b, "`%s`, this machine keeps `%s`.\n\n", cloud, mine)

	n := 0
	for _, bt := range st.a.builts {
		if bt != nil {
			n++
		}
	}
	fmt.Fprintf(&b, "- %d container(s), the edge on port %d", n, portOr(st.a.httpPort, 80))
	if st.a.httpsPort != 0 {
		fmt.Fprintf(&b, " and %d (TLS)", st.a.httpsPort)
	}
	b.WriteString(", Portainer on 9443.\n")
	if len(required) > 0 {
		var names []string
		for _, d := range required {
			names = append(names, "`"+d.Name+"`")
		}
		fmt.Fprintf(&b, "- site.env needs: %s and `PORTAINER_ADMIN_PASSWORD`.\n", strings.Join(names, ", "))
	} else {
		b.WriteString("- site.env needs only `PORTAINER_ADMIN_PASSWORD`.\n")
	}
	b.WriteString("\n## 1. Prepare the machine (once)\n\n")
	b.WriteString("Linux (amd64 or arm64) with internet access to the image registries and to GitHub.\n\n")
	b.WriteString("```sh\n# Docker Engine + the compose plugin (official script; or your distribution's docker-ce packages)\ncurl -fsSL https://get.docker.com | sudo sh\nsudo usermod -aG docker \"$USER\"     # then log out and in again\ndocker version && docker compose version   # Docker 24+ and Compose v2.24+\nsudo apt-get install -y git   # or: sudo dnf install -y git\n```\n\n")
	if logins := x.registryLogins(); len(logins) > 0 {
		b.WriteString("Log in to the private registries the site pulls from:\n\n```sh\n")
		for _, h := range logins {
			fmt.Fprintf(&b, "docker login %s\n", h)
		}
		b.WriteString("```\n\n")
	}
	b.WriteString("Git needs write access to this repository (step 2 pushes this machine's branch): a fine-grained token with " +
		"**Contents: Read and write** on this repository only, or a deploy key with write access.\n\n")
	b.WriteString("## 2. First deploy\n\n```sh\n")
	fmt.Fprintf(&b, "sudo mkdir -p %s && sudo chown \"$USER\" %s\n", dir, dir)
	fmt.Fprintf(&b, "git clone %s %s && cd %s\n", x.repoURL(), dir, dir)
	b.WriteString("git config user.name \"<your name>\" && git config user.email \"<your email>\"\n")
	fmt.Fprintf(&b, "git switch -c %s origin/%s     # this machine's branch\n", mine, cloud)
	fmt.Fprintf(&b, "cd sites/%s\n", site)
	b.WriteString("cp site.env.example site.env && nano site.env     # fill every value (never committed)\n")
	b.WriteString("./deploy.sh\n")
	fmt.Fprintf(&b, "git push -u origin %s       # the repository now shows what this machine runs\n", mine)
	b.WriteString("```\n\n")
	b.WriteString("`./deploy.sh` reads `site.env`, fills in the files that need its values (official busybox image), writes `.env`, " +
		"then runs `docker compose up -d`: `init` seeds every empty volume once and the containers start in dependency order, " +
		"then `edge` serves the routes. It prints `docker compose ps` at the end.\n\n")
	fmt.Fprintf(&b, "If `%s` already exists (a machine was set up before, e.g. after a reinstall): `git switch %s` instead of `git switch -c …`, then copy the same `site.env` back.\n\n", mine, mine)
	b.WriteString("## 3. Update to a new cloud commit\n\n```sh\n")
	fmt.Fprintf(&b, "cd %s\ngit fetch origin\ngit merge origin/%s     # conflicts only if you changed the same lines here\n", dir, cloud)
	fmt.Fprintf(&b, "(cd sites/%s && ./deploy.sh)\ngit push\n```\n\n", site)
	b.WriteString("If the merge stops with a conflict: fix the files `git status` lists, `git add` them, `git commit`, then deploy. " +
		"A new value in `site.env.example` means: add it to `site.env` before `./deploy.sh` (it tells you which one is missing).\n\n")
	b.WriteString("## 4. Change something on this machine\n\n")
	b.WriteString("Edit the files of this folder (e.g. `docker-compose.yml`, `files/…`), then:\n\n```sh\n")
	fmt.Fprintf(&b, "cd %s/sites/%s && ./deploy.sh\ngit add -A && git commit -m \"%s: <what changed and why>\"\ngit push\n```\n\n", dir, site, site)
	b.WriteString("Make the same change in the cloud too, or the next cloud commit may conflict with it (step 3). " +
		"A change made in Portainer (or with `docker` directly) is NOT in git and is undone by the next `./deploy.sh`.\n\n")
	b.WriteString("## 5. Roll back\n\n```sh\n")
	fmt.Fprintf(&b, "cd %s\ngit log --oneline -10            # pick the commit to go back to\ngit reset --hard <commit>\n(cd sites/%s && ./deploy.sh)\ngit push --force-with-lease     # only this machine writes its branch\n```\n\n", dir, site)
	b.WriteString("The next `git merge` of the cloud branch brings the newer cloud commits back: merge again once the cloud is fixed. " +
		"Rolling back restores the configuration, not the data in the volumes: back them up before an update you are unsure about.\n\n")
	b.WriteString("## 6. Everyday commands\n\n```sh\n")
	fmt.Fprintf(&b, "cd %s/sites/%s\n", dir, site)
	b.WriteString("docker compose ps                     # state + health of every service\ndocker compose logs -f --tail 200 <service>\ndocker compose restart <service>\ndocker compose down                   # stop the site (volumes stay; `down -v` deletes them)\n```\n\n")
	b.WriteString("Portainer: `https://<this machine>:9443`, user `admin`, the password from `site.env` (set once, at the first start; change it in Portainer afterwards).\n\n")
	b.WriteString("## Back up a volume\n\n```sh\n")
	fmt.Fprintf(&b, "docker run --rm -v %s_<volume>:/v:ro -v \"$PWD\":/b busybox tar czf /b/<volume>-$(date +%%F).tgz -C /v .\n", x.projectName(site))
	fmt.Fprintf(&b, "# restore into an empty volume:\ndocker compose down && docker run --rm -v %s_<volume>:/v -v \"$PWD\":/b busybox tar xzf /b/<file>.tgz -C /v && ./deploy.sh\n```\n\n", x.projectName(site))
	b.WriteString("## Troubleshooting\n\n")
	b.WriteString("- `deploy: site.env is missing` / `set these values in site.env`: copy `site.env.example` and fill the names it lists.\n")
	b.WriteString("- A container is `unhealthy`: `docker compose logs <service>`; its health check is in `docker-compose.yml`.\n")
	b.WriteString("- `docker compose …` says a value is not set (`run ./deploy.sh`): run `./deploy.sh` once, it writes `.env`.\n")
	b.WriteString("- SELinux hosts (RHEL, Fedora): if the containers cannot read `.runtime/`, run `sudo chcon -Rt container_file_t .runtime .data`.\n")
	fmt.Fprintf(&b, "- The cloud branch was rebuilt (\"Resync everything\" in the cloud): `git merge --allow-unrelated-histories -X theirs origin/%s`, check `git diff HEAD~1`, then deploy.\n", cloud)
	return b.String()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func mdEsc(s string) string { return strings.NewReplacer("<", "&lt;", ">", "&gt;").Replace(s) }
