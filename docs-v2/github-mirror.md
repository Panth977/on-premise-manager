# GitHub mirror + docker compose without the agent (SPEC §21 #106 · agent 0.15.0)

User request (summary): define a GitHub repository and an access token; mirror every OPM branch into it as git branches,
as a backup and a side database for configs. Every branch must also hold an equivalent setup WITHOUT the agent:
`git clone` the branch on a VM, `cd` into a site's directory, `docker compose up`, and the containers, router and edge come
up like the agent would run them — except the agent itself. The setup lives under **Admin → GitHub** in the cloud console.

Contract: `shared/src/github-mirror.ts` (API table `GITHUB_MIRROR_CLOUD_API`, wire format `ExportRequest` /
`ExportResult`, queue + branch-state documents, branch names, slugs, trailers). Exporter: Go `agent/internal/export`
(+ `agent/internal/edgecfg` for the edge). Backend: `backend/functions/src/core/{github,mirror*,slugs}.ts`,
`routes/github-mirror.ts`. Frontend: `/app/github`.

---------------------------------------------------------------------------------------------------------------------

## 1. What you do once

1. Create an EMPTY private repository on GitHub (e.g. `acme/opm-mirror`). OPM never creates repositories.
2. Create a **fine-grained personal access token**: Resource owner = the repository's owner, Repository access = **Only
   select repositories** → that repository, Permissions → Repository permissions → **Contents: Read and write** (Metadata:
   read is added automatically). Pick an expiry you will remember to renew.
3. In OPM: **Admin → GitHub** → owner + repository → Save → **Replace token…** (paste) → **Test connection** (all checks
   green) → switch **Mirroring** on. Enabling queues a catch-up of every branch; the table fills within minutes.

The token is stored like a registry password (SPEC §21 #1 / #84): only the cloud functions read it, every read shows it
masked (`github_pat_•••wxyz`), **View** shows it for 60 s (audited `github.token-revealed`), **Replace…** / **Remove**
change it. Every change on the page is in the audit log (target `githubMirror/config`).

## 2. Branches

| OPM branch | git branch | commits |
|---|---|---|
| template | `template/<slug>` | one per template commit |
| deployment | `deployment/<slug>/cloud` | one per deployment commit (= release) |
| agent branch of a site | `deployment/<slug>/agent/<site>` | one per agent commit + one per persist-to-cloud snapshot |
| uncommitted drift of a site | `deployment/<slug>/uncommitted/<site>` | ONE snapshot commit on top of the agent branch, reset on every new drift, deleted when the drift is committed or discarded (agent ≥ 0.15.0) |

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

## 3. What a branch holds (example: `template/single`)

```
.gitignore                                   **/.env  **/.opm-runtime/  **/.opm-data/
README.md                                    how to run, prerequisites, docker login, values to fill, not-equivalent list
opm/config.json                              the commit config (secret values stripped) + commit metadata
opm/export.json                              the exporter input (opm-init re-renders the .env-dependent parts from it)
opm/bin/opm-render                           sh launcher → the binary of this machine's arch
opm/bin/opm-render-linux-amd64|arm64         opm-init / health probe / env wrapper (static, reproducible)
sites/site-01/docker-compose.yml             the site (15 containers + opm-init + opm-edge)
sites/site-01/.env.example                   OPM_SECRET_PG_PASSWORD, OPM_SECRET_MQTT_PASSWORD, OPM_VAR_SERVER_IP, OPM_EDGE_HTTP_PORT
sites/site-01/edge/caddy.json                the agent's edge for the official caddy image
sites/site-01/edge/pages/404.html, 503.html  the not-found pages
sites/site-01/files/northwind-backend-config   Files rendered without .env values
sites/site-01/files/northwind-frontend-config
sites/site-01/files/mqtt-config
sites/site-01/files/northwind-admin-config.opm-template   rendered at `up` (needs .env values)
sites/site-01/files/mqtt-pwfile.opm-template            {{ secret.mqtt-password | mosquitto_passwd }} → rendered at `up`
sites/site-01/volumes/code-shell-code/…      starter content (README.md, deno.json, deno.lock, src/*.ts, start.sh)
```

## 4. Run a site on a VM

```sh
docker login docker.io                        # every private registry the README lists
git clone --branch deployment/plant-3/cloud --single-branch git@github.com:acme/opm-mirror.git opm
cd opm/sites/site-01
cp .env.example .env                          # fill every value; never commit .env
docker compose up -d
```

Prerequisites: Docker Engine ≥ 24, Docker Compose v2 ≥ 2.24, Linux amd64 or arm64.

`opm-init` (busybox + `opm-render init`) runs first and exits: it checks that every required .env value is set (a clear
list otherwise), renders what depends on .env values with the SAME Go code the agent uses (Files such as the Mosquitto
password file, secret files, derived env, the edge config when it uses such a value), seeds every EMPTY volume once with
its starter content (marker `.opm-seeded`, the agent's seed-once rule; a second `up` never re-seeds), and installs the
probe tool for the health checks. Then the containers start like on the agent (dependencies wait for healthy / completed
/ started), and `opm-edge` serves the routes.

## 5. Equivalence (what is the same)

The exporter calls the agent's own builders: `driver.Build` for every container (resolved env, mounts, ports, restart,
resources, command / entrypoint, health) and `edgecfg` for the edge. Per container:

| agent | compose |
|---|---|
| docker name = containerSlug, network `opm_<dep>_<site>`, aliases `[slug, opm_<dep>_<site>_<slug>]` | service name = `container_name` = slug, same network name + aliases |
| image `<svc>:<version>` loaded from the cloud | the registry reference pinned by digest, platform as imported |
| env with expressions resolved | literal env; secrets referenced directly → `${OPM_SECRET_X:?…}`; a value derived from a secret through a filter → entrypoint wrapper (`/.opm/opm-render exec`) reading the rendered value |
| Files (one copy per site, 0444 / 0400, read-only bind) | rendered by opm-init into `.opm-runtime/files/` with the same modes, read-only bind |
| site volumes (folders) / single-file volumes / host paths / secret files | named volumes / `.opm-data/volumes/<v>` / the same host paths / `.opm-runtime/secrets/<s>` |
| published ports, restart, cpus / memory, stop timeout, extra host `host.docker.internal` | the same |
| start order, explicit + implicit depends, readiness = the health check | `depends_on` with `service_healthy` (http / tcp / exec health), `service_completed_successfully` (jobs), `service_started` |
| health http / tcp checked from the host via the loopback publication | the same check from inside the container (probe tool, own non-loopback address); interval / timeout / retries / start period 1:1 |
| edge = embedded Caddy with OPM modules | official `caddy:2.11.7` with standard modules: nginx location order (exact → auto-redirect → longest prefix / `^~` → regex), internal rewrites (≤ 10, then 500), strip prefix / proxy rewrite / addPrefix, redirects with variables, request + response headers with nginx inheritance, body limits (413 page), websocket off (400), not-found / 503 pages, TLS (internal, ACME http-01 / tls-alpn-01, files) |

**Proof** (`agent/internal/agent/compose_equiv_docker_test.go`, real Docker 29.5.2 + Compose 2.40): one site is applied by
the real agent engine, captured, removed, then the exported tree of the SAME commit is brought up with `docker compose up
-d --wait` and captured again. 47 aspects compared, 0 differences: images, env (incl. a derived secret value), entrypoint
/ cmd, user / workdir, every mount (target, ro, file bytes + modes, folder trees incl. `.opm-seeded`), network aliases,
published ports, restart / resources / stop timeout / extra hosts, labels, start order + dependents only after the
dependency's first healthy event, 15 edge requests (status / Location / headers / body), Mosquitto login with the secret
password (and refusal of a wrong one; the password file is rendered with `mosquitto_passwd`), no re-seed on a second `up`.
Edge parity on the official image: 57 / 57 shared edge vectors + every client-nginx parity behaviour.

## 6. NOT equivalent without the agent

- No agent: no drift detection, no branches / commits / merges on the machine, no agent GUI (`/_opm`), dev tools,
  transfers or self-upgrade.
- No snapshots / backups / rollback (the volume `backup` flag has no effect), no persist-to-cloud sync (an agent branch
  starts from the last snapshot, but changes are not sent back), no approvals.
- The site address is not detected: `OPM_SITE_HOST` (and `OPM_SITE_<SITE>_HOST` for other sites on auto) go into .env.
- Containers without a health check count as started at once (the agent waits 10 s without a restart); `log` health checks
  have no compose equivalent (dependents wait only for the start).
- The edge: a stopped container answers 502 → the 503 page after the dial error (the agent: 503 at once); the client
  address the edge sees is the Docker gateway; read / write timeouts are Caddy's per-read / per-write deadlines; ACME dns-01
  needs the `edge/Dockerfile` build (xcaddy + the caddy-dns module).
- The agent's loopback-only ephemeral port publications are not created; health probes and the probe-tool mount are
  extra; `docker inspect` does not show env set by the wrapper; HOME may differ in the process env.
- File locations (`StorageLocation.path`) are ignored: Files come from the branch; images are pulled from their registry
  (log in first) for the platform OPM imported.

## 7. Push pipeline

- Enqueue: Firestore triggers on new template / deployment / agent-branch commits and on persist snapshot versions
  (origin `snapshot`); the drift push (`POST /v1/agent/branch/drift`, agent ≥ 0.15.0) and heartbeats (agent head not
  mirrored yet → catch-up) enqueue directly. Only while mirroring is enabled.
- Queue `githubMirror/config/queue/{itemId}` (idempotent ids), branch state `githubMirror/config/branches/{branch}`, blob
  cache `githubMirror/config/blobs/{sha256}`. Worker = a trigger on new items + a schedule every 5 min + inline in Sync now.
  One branch at a time (3-min lease), items in order; a failing item blocks its branch until retried (backoff 30 s → 1 h).
  401 / 403 (token) and rate limits pause the whole mirror (shown as a banner) until the reset; Sync now resumes.
- Push = GitHub REST Git Data API from Cloud Functions (no git binary): blobs (deduplicated by git sha), one tree, one
  commit, ref update (never forced, except Resync). Idempotent by the `OPM-Commit` trailer of the remote head + the branch
  state. Files > 100 MB are skipped (listed in `<dir>/.opm-skipped.txt`, note SKIPPED_LARGE).
- The tree is computed by `opm-export.wasm` (the Go exporter, run with node:wasi). The backend build compiles it and the
  `opm-render` binaries from `agent/`, so the exporter and the runtime helper always match the deployed backend.
- **Sync now**: catch-up of every branch (or one row), due and failed items retried at once. **Resync everything**:
  rebuild every branch from OPM history, force-update the refs (same shas when nothing changed).

## 8. Admin → GitHub page

Repository (owner, name, link, Mirroring on/off) · Token (masked, View 60 s, Replace…, Remove) · Test connection (token,
repository, contents read, contents write, rate limit) · help panel (what each branch holds, how to run it) · branch table
(git branch → tree link, kind, label, last pushed OPM commit, git sha → commit link, state + pending, last error, notes)
with Sync / Sync now / Resync everything… · paused banner. Only accounts with `accounts.admin`.
