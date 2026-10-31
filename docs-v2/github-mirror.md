# GitHub mirror + running a site with Docker only (SPEC §21 #106, #115)

User request (summary): define a GitHub repository and an access token; mirror every OPM branch into it as git branches,
as a backup and a side database for configs. Every branch also runs each site with **Docker alone**, the last resort when
a client does not allow the agent or the cloud: clone the branch, keep a branch per site, fill `site.env`, `./deploy.sh`.
It is plain `docker compose` with Caddy as the router and Portainer as the GUI; no OPM code runs on the machine (§21 #115).
The setup lives under **Admin → GitHub** in the cloud console.

Contract: `shared/src/github-mirror.ts` (API table `GITHUB_MIRROR_CLOUD_API`, wire format `ExportRequest` /
`ExportResult`, queue + branch-state documents, branch names, slugs, trailers). Exporter: Go `agent/internal/export`
(+ `agent/internal/edgecfg` for the edge). Cloud: `agent/internal/cloud/core/{github,mirror*,slugs}.go`,
`agent/internal/cloud/routes/github_mirror.go`. Frontend: `/app/github`.

---------------------------------------------------------------------------------------------------------------------

## 1. What you do once

1. Create an EMPTY private repository on GitHub (e.g. `acme/opm-mirror`). OPM never creates repositories.
2. Create a **fine-grained personal access token**: Resource owner = the repository's owner, Repository access = **Only
   select repositories** → that repository, Permissions → Repository permissions → **Contents: Read and write** (Metadata:
   read is added automatically). Pick an expiry you will remember to renew.
3. In OPM: **Admin → GitHub** → owner + repository → Save → **Replace token…** (paste) → **Test connection** (all checks
   green) → switch **Mirroring** on. Enabling queues a catch-up of every branch; the table fills within minutes.

The token is stored like a registry password (SPEC §21 #1 / #84): only the cloud reads it, every read shows it
masked (`github_pat_•••wxyz`), **View** shows it for 60 s (audited `github.token-revealed`), **Replace…** / **Remove**
change it. Every change on the page is in the audit log (target `githubMirror/config`).

## 2. Branches

| OPM branch | git branch | commits |
|---|---|---|
| template | `template/<slug>` | one per template commit |
| deployment | `deployment/<slug>/cloud` | one per deployment commit (= release) |
| agent branch of a site | `deployment/<slug>/agent/<site>` | one per agent commit + one per persist-to-cloud snapshot |
| uncommitted drift of a site | `deployment/<slug>/uncommitted/<site>` | ONE snapshot commit on top of the agent branch, reset on every new drift, deleted when the drift is committed or discarded (agent ≥ 0.15.0) |
| a site run with Docker only | `deployment/<slug>/site/<site>` | written by THAT machine (`git switch -c` from the cloud branch, `git merge` of new cloud commits, local commits); the mirror never writes, rebuilds or deletes it |

`<slug>` is the entity's stable slug (`Template.slug`, `Deployment.slug`): made from the label at create (unique, shown in
the create form, editable there), never changed afterwards. Existing entities got one from `migrate-k` (Single → `single`).

Every mirrored commit keeps the OPM message, the author name / email and the date (author = committer, both dates = the
OPM commit time) and ends with trailers:

```
Add mqtt broker

OPM-Commit: 01M4G3VCSG7PRBNDTV9MN80RFT
OPM-Branch: template:7fs9F8qVIEljdiBWwZki
OPM-Seq: 3
```

The trees are deterministic, so `Resync everything` rebuilds the same commit shas when nothing changed in OPM. Nothing
else is ever pushed to these branches. An empty repository first gets one commit on `main` (a README) because GitHub's Git
Data API refuses empty repositories.

## 3. What a branch holds (example: `deployment/plant-3/cloud`)

```
.gitignore                                   **/site.env  **/.env  **/.runtime/  **/.data/
README.md                                    the branch, its sites, the layout, what is not equivalent
sites/site-01/README.md                      the exact runbook of the site (install Docker, first deploy, update, roll back, backups)
sites/site-01/deploy.sh                      the one command (same for every site)
sites/site-01/site.env.example               OPM_SECRET_PG_PASSWORD=, OPM_SECRET_MQTT_PASSWORD=, PORTAINER_ADMIN_PASSWORD=, …
sites/site-01/docker-compose.yml             the containers + init + edge (Caddy) + portainer
sites/site-01/setup.sh                       generated: values → .env + .runtime/ (runs in the official busybox image)
sites/site-01/init.sh                        generated: seeds empty volumes once, installs the health-check busybox
sites/site-01/files/northwind-backend-config the Files without machine values, byte for byte
sites/site-01/files/mqtt-pwfile.template     a File with machine values: @@{NAME}@@ placeholders, filled in by deploy.sh
sites/site-01/templates/steps/OPM_D_….in     the input of a derived value (e.g. sha256 / mosquitto_passwd of a secret)
sites/site-01/templates/secrets/<s>.template a secret mounted as a file
sites/site-01/volumes/code-shell-code/…      starter content (agent branches: the last persist-to-cloud snapshot)
sites/site-01/edge/caddy.json                the agent's edge for the official caddy image (+ pages/404.html, 503.html)
```

No OPM file, binary or label is in the tree.

## 4. Run a site on a machine

Each site's `README.md` has the commands with the real repository, branch and site names. In short:

```sh
curl -fsSL https://get.docker.com | sudo sh           # Docker Engine + compose plugin (once)
git clone https://github.com/acme/opm-mirror.git /opt/plant-3 && cd /opt/plant-3
git switch -c deployment/plant-3/site/site-01 origin/deployment/plant-3/cloud
cd sites/site-01
cp site.env.example site.env && nano site.env          # secrets, this machine's address, Portainer password
./deploy.sh
git push -u origin deployment/plant-3/site/site-01      # the repository shows what this machine runs

# a new cloud commit, later
git fetch origin && git merge origin/deployment/plant-3/cloud
(cd sites/site-01 && ./deploy.sh) && git push
```

Prerequisites: Linux amd64 / arm64, Docker Engine ≥ 24, Compose v2 ≥ 2.24, git with write access to the repository
(fine-grained token, Contents read + write, or a deploy key with write access); `docker login` for private registries.

`deploy.sh` is plain sh and uses official images only:

1. **setup.sh values** (busybox, as the calling user): reads `site.env` (`NAME=value`, taken as written), checks every
   required value (a clear list otherwise).
2. **setup.sh steps**: the values derived from machine values. The exporter evaluates filters on such values
   symbolically: `{{ secret.api-token | sha256 }}` becomes `OPM_D_<12 hex> = sha256(${OPM_SECRET_API_TOKEN})`, a step
   computed here with busybox (`upper`, `lower`, `trim`, `base64`, `url`, `sha256`, `sha512`, `int`, `json`).
   `mosquitto_passwd` steps run in the official `eclipse-mosquitto` image; a step is computed again only when its input
   changed (a mosquitto hash stays stable while the password does not change).
3. **setup.sh render**: every `@@{NAME}@@` template (Files, secret files, TLS files, the edge config, starter files) into
   `.runtime/` with the agent's modes (0444, 0400 for secrets), an unchanged file is left alone; `.env` with every value
   (double-quoted, read back by compose byte for byte), the hash of the files each service mounts, `COMPOSE_PROFILES`.
4. **docker compose up -d --remove-orphans**: `init` (busybox) seeds every EMPTY volume once (marker `.opm-seeded`, the
   agent's seed-once rule) and installs the static busybox the health checks use; the containers start in the agent's
   order, waiting for healthy / completed / started; `edge` (Caddy) serves the routes; `portainer` is the Docker GUI
   (admin password from `site.env`, so no first-run setup; `PORTAINER=off` skips it).

Values reach the compose file as `${NAME:?run ./deploy.sh}` from `.env`, so plain `docker compose ps / logs / restart`
work after the first deploy. A service whose mounted files change gets a new label `files.sha256`, so compose recreates
exactly that service, like the agent restarts a container whose files changed. `./deploy.sh --render-only` stops before
`docker compose up`.

## 5. Equivalence (what is the same)

The exporter calls the agent's own builders: `driver.Build` for every container (resolved env, mounts, ports, restart,
resources, command / entrypoint, health) and `edgecfg` for the edge. Per container:

| agent | compose |
|---|---|
| docker name = containerSlug, network `opm_<dep>_<site>`, aliases `[slug, opm_<dep>_<site>_<slug>]` | service name = `container_name` = slug, same network name + aliases |
| image `<svc>:<version>` loaded from the cloud | the registry reference pinned by digest, platform as imported |
| env with expressions resolved | literal env; machine values `${NAME}` from `.env`, derived ones (filters) computed by deploy.sh |
| Files (one copy per site, 0444 / 0400, read-only bind) | rendered by deploy.sh into `.runtime/files/` with the same modes, read-only bind |
| site volumes (folders) / single-file volumes / host paths / secret files | named volumes / `.data/volumes/<v>` / the same host paths / `.runtime/secrets/<s>` |
| published ports, restart, cpus / memory, stop timeout, extra host `host.docker.internal` | the same |
| start order, explicit + implicit depends, readiness = the health check | `depends_on` with `service_healthy` (http / tcp / exec health), `service_completed_successfully` (jobs), `service_started` |
| health http / tcp checked from the host via the loopback publication | the same check from inside the container with a static busybox (`wget -S` / `nc -z`, the container's own address); interval / timeout / retries / start period 1:1 |
| a container whose files changed is restarted | label `files.sha256` changes → compose recreates that service |
| edge = embedded Caddy with OPM modules | official `caddy:2.11.7` with standard modules: nginx location order (exact → auto-redirect → longest prefix / `^~` → regex), internal rewrites (≤ 10, then 500), strip prefix / proxy rewrite / addPrefix, redirects with variables, request + response headers with nginx inheritance, body limits (413 page), websocket off (400), not-found / 503 pages, TLS (internal, ACME http-01 / tls-alpn-01, files) |

**Proof** (`agent/internal/agent/compose_equiv_docker_test.go`, real Docker + Compose): one site is applied by the real
agent engine, captured, removed, then the exported tree of the SAME commit is brought up with `./deploy.sh` and captured
again: images, env (incl. a value derived from a secret), entrypoint / cmd, user / workdir, every mount (target, ro, file
bytes + modes, folder trees incl. `.opm-seeded`), network aliases, published ports, restart / resources / stop timeout /
extra hosts, start order + dependents only after the dependency's first healthy event, 15 edge requests (status /
Location / headers / body), Mosquitto login with the secret password (and refusal of a wrong one), no OPM labels, the
Portainer admin login with the site.env password, no re-seed on a second deploy, and an update that changes one File
recreates only the service that mounts it. 0 differences.

## 6. NOT equivalent without the agent

- No agent: no drift detection, no commit history kept by the machine itself (git is the history), no agent GUI
  (`/_opm`; Portainer is the GUI), dev tools, transfers or self-upgrade.
- No automatic snapshots / backups / rollback (the volume `backup` flag has no effect), no persist-to-cloud sync, no
  approvals. The site README shows how to back up a volume.
- The site address is not detected: `OPM_SITE_HOST` (and `OPM_SITE_<SITE>_HOST` for other sites on auto) go into site.env.
- Containers without a health check count as started at once (the agent waits 10 s without a restart); `log` health checks
  have no compose equivalent (dependents wait only for the start).
- The edge: a stopped container answers 502 → the 503 page after the dial error (the agent: 503 at once); the client
  address the edge sees is the Docker gateway; read / write timeouts are Caddy's per-read / per-write deadlines; ACME dns-01
  needs the `edge/Dockerfile` build (xcaddy + the caddy-dns module).
- Derived values use standard tools: `upper` / `lower` change ASCII letters only, `trim` removes ASCII white space only,
  `mosquitto_passwd` salts at random (the hash differs from the agent's; the password is the same).
- The agent's loopback-only ephemeral port publications are not created; the health-check busybox mount (`/.tools`) is
  extra.
- File locations (`StorageLocation.path`) are ignored: Files come from the branch; images are pulled from their registry
  (log in first) for the platform OPM imported.

## 7. Push pipeline

- Enqueue: outbox triggers on new template / deployment / agent-branch commits and on persist snapshot versions
  (origin `snapshot`); the drift push (`POST /v1/agent/branch/drift`, agent ≥ 0.15.0) and heartbeats (agent head not
  mirrored yet → catch-up) enqueue directly. Only while mirroring is enabled.
- Queue `githubMirror/config/queue/{itemId}` (idempotent ids), branch state `githubMirror/config/branches/{branch}`, blob
  cache `githubMirror/config/blobs/{sha256}`. Worker = a trigger on new items + a schedule every 5 min + inline in Sync now.
  One branch at a time (3-min lease), items in order; a failing item blocks its branch until retried (backoff 30 s → 1 h).
  401 / 403 (token) and rate limits pause the whole mirror (shown as a banner) until the reset; Sync now resumes.
- Push = GitHub REST Git Data API from `opm-cloud` (no git binary): blobs (deduplicated by git sha), one tree, one
  commit, ref update (never forced, except Resync). Idempotent by the `OPM-Commit` trailer of the remote head + the branch
  state. Files > 100 MB are skipped (listed in `<dir>/.skipped.txt`, note SKIPPED_LARGE).
- The tree is computed in-process by the Go exporter (`agent/internal/export`) inside `opm-cloud`. Nothing else is built
  for the mirror: the tree needs no OPM binary.
- **Sync now**: catch-up of every branch (or one row), due and failed items retried at once. **Resync everything**:
  rebuild every branch from OPM history, force-update the refs (same shas when nothing changed). A machine's site branch
  then merges with `--allow-unrelated-histories` (the site README says how); avoid it once machines run Docker only.

## 8. Admin → GitHub page

Repository (owner, name, link, Mirroring on/off) · Token (masked, View 60 s, Replace…, Remove) · Test connection (token,
repository, contents read, contents write, rate limit) · help panel (what each branch holds, how to run it) · branch table
(git branch → tree link, kind, label, last pushed OPM commit, git sha → commit link, state + pending, last error, notes)
with Sync / Sync now / Resync everything… · paused banner. Only accounts with `accounts.admin`.
