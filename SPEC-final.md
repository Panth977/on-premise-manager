# OPM — On-Prem Manager · SPEC-final (canonical)

Status: **canonical, single source of truth** (2026-10-07). It replaces SPEC-v2.md + its addenda and resolves every
contradiction with docs-v2 (the docs stay as explanation; where they differ, THIS file wins).

Normative companions (they are part of this spec, same authority):

| file | what |
|---|---|
| `shared/src/types.ts` | every stored document and every API body (field names, types, nullability). Appendix A is a verbatim copy. |
| `shared/src/cloud-api.ts` | the cloud endpoint table (method, path, auth, permission, errors, activities). §6 is generated from it. |
| `shared/src/agent-api.ts` | the agent local API (§7) and the peer API (§8) tables. |
| `shared/src/expr.ts` + `shared/testdata/expr-vectors.json` | the expression engine (§10). The Go port `agent/internal/expr` passes the same vectors. |
| `shared/src/canonical.ts` + `shared/testdata/canonical-vectors.json` | JCS, activity hash, signed-request strings (§4.4, §13). Go: `agent/internal/canonical`. |
| `shared/src/validate.ts` | validation rules + codes (§11), graph orders, expression contexts, local-edit rules. |
| `shared/src/edge-resolve.ts` | the edge request resolver / "Test a URL" (§12). |
| `shared/testdata/edge-vectors.json` | edge decisions (§12.1); TS and Go (`agent/internal/edgeres`) pass the same 59 cases. |
| `shared/src/nginx-import.ts` + `shared/testdata/nginx-import-snapshot.json` | the nginx.conf importer (§12.7) and its snapshot of `e2e/fixtures/client-nginx.conf`. |
| `shared/testdata/required-vars-vectors.json` | required / optional variable issues per validation mode (§11, §21 #78). |
| `shared/src/access.ts` | access rule evaluation (§5.2). |
| `shared/src/diff.ts` | config diff (cloud /diff, agent previews). |
| `shared/testdata/hello-config.json`, `hello-catalog.json` | the hello-world e2e deployment (§20). |
| `agent/internal/model` | Go mirror of the wire types (json tags identical to TS). |

Product name: **On-Prem Manager (OPM)**. Generic: it knows nothing about any specific application. Example data uses neutral
names (`web`, `api`, `postgres`, `redis`, `worker`; deployment "ACME plant"; sites `app` and `db`).

---------------------------------------------------------------------------------------------------------------------

## 0. Terms

- **Deployment** — one on-prem installation for one client. Has 1..20 **sites**.
- **Site** — one machine that runs one **agent** and its **containers**. Id: `siteSlug`, unique in the deployment.
  Optional **host** (null = auto: the address the agent detected / advertises).
- **Agent** — the Go binary `opm-agent` on a site (systemd service). Embeds the GUI, SQLite, the edge (Caddy), the Docker driver.
- **agentHome** — the agent data dir of a site (`--data-dir` / `OPM_DATA_DIR`, else agent.json `dataDir`, default
  `/var/lib/opm`; §2.4, §19.3). A storage location written `./x/y` is relative to it.
- **Storage location** — where the ONE copy per site of a file or a site volume lives on the server (`StorageLocation`:
  `auto` = `<agentHome>/files/<fileSlug>` / `<agentHome>/volumes/<volumeSlug>`, or a custom `./relative` / absolute path).
- **Service** — a named kind of container (e.g. `postgres`). One identifier: `serviceSlug` = document id, immutable.
- **Container** — one **instance** of a service image on a site (`Container`). Id: `containerSlug`, unique in the site
  (default = the service slug, then `<slug>-2` …); it is the DNS name on the site network and the name in expressions, edge
  routes, depends, docker names, logs and uptime. The same service + version may run many times on one site with different
  env / ports / volumes.
- **Site volume** — a data folder (or, kind `file`, one data file) DECLARED on a site (`SiteConfig.volumes`, `SiteVolume {
  volumeSlug, label, description, backup, location, kind, seed }`). Id: `volumeSlug`, unique in the site, never renamed. On the
  agent: its location (default `<agentHome>/volumes/<volumeSlug>`). Containers mount it by volumeSlug (source kind `volume`);
  several containers may share one. `backup` = included in snapshots. `seed` = starter content = the volume's OWN file tree
  (§21 #101; never Files), written once into a new or empty volume. `persist` = persist-to-cloud (snapshots to the cloud, edits
  sent back and approved on the site).
- **Image** — one **version** of a service (e.g. `v1`; the document id) in the cloud catalog, imported from a full docker
  **reference** (e.g. `docker.io/northwind/backend:3.0.9`). Immutable content; metadata partly editable.
  On a site it is tagged `<serviceSlug>:<version>` (e.g. `northwind-backend:v1`). "Docker tag" means only the tag inside a reference.
- **Icon** — `null` (default) | `lucide:<name>` | `upload:<iconId>` (icons library); see §2.2.
- **Template** — a reusable blueprint (variables, secrets, files, sites). A deployment copies it at create.
- **Config** — `{ variables, secrets, files, sites }`, deployment-wide, identical on every site.
- **Draft** — the cloud's editable Config of a deployment (`Deployment.draft`). §21 #105: there is NO working copy any more — `draft` is a copy of the head commit config and pending edits live in the browser until Commit. §21 #104: called the **working copy**; history = **commits** of the template / deployment / agent **branch** (docs-v2/branches-commits.md).
- **Revision** — an immutable Config version (+ catalog). Linear chain on the sites. **Head** = the current revision.
- **Proposal** — a revision waiting for approval (agent GUI /updates). Origin `cloud` (publish) or `agent` (local edit).
- **Staging** — automatic per-site preparation of a proposal (validate, download, `docker load`, dry run).
- **Gate** — the 4 conditions under which the Approve / Roll back button is shown.
- **Update** — the coordinated apply of a proposal (or a rollback) on all sites: LOCK → SNAPSHOT → APPLY → VERIFY → COMMIT.
- **Coordinator** — the agent where a person typed ACCEPT; it drives the update over the mesh.
- **Snapshot** — a backup on the sites (pre-update, manual, scheduled, imported): config + image refs + archives of the
  declared site volumes with `backup = true` (host paths are never backed up).
- **Activity** — one audit record; one append-only hash-chained log per site (+ a `_cloud` log per deployment).
- **Expression** — `{{ … }}` blocks inside a Value string (and inside text files with `render: 'expressions'`), resolved ON THE SITE.
- **Edge** — the router inside each agent (embedded Caddy v2): hosts, TLS, routes → containers.
- **Mesh** — agent ↔ agent HTTP on `agentPort` under `/_opm/peer/*`, every request Ed25519-signed.
- **Cloud view** — what an agent last learned from the cloud (poll answer); exchanged with peers, *latest fetch wins*.
- **Access key** — the deployment-wide key for the agent GUI login (24 base32 chars). The cloud stores it (viewable any time
  on Setup, §21 #84) plus its argon2id hash; agents get only the hash.
- **Actor** — the email declared in the "Who is working right now?" popup; written into every activity. NOT authentication.
- **Install token** — single-use 24 h token in the install command; enrolls the site key. Stored while active, so the
  command can be viewed again (§4.2).

## 1. Architecture

### 1.1 Codebases (all under `on-premise-manager/`)

| dir | what | tech |
|---|---|---|
| `shared/` | contracts (types, API tables, expr, validate, edge resolver, access, diff) | TypeScript ESM, `@opm/shared`, no runtime deps |
| `backend/` | cloud backend: one HTTPS Cloud Function `api` (router for `/v1/*`) + Firestore trigger `importImage` + scheduled `daily`; the SAME code also runs as **opm-cloud** (one Node server, SQLite + filesystem + built-in auth) on a VM (§21 #108) | Firebase Functions gen 2, Node 22, TS; ports + adapters (firebase \| local) |
| `selfhost/` | opm-cloud packaging: Dockerfile, docker-compose.yml (+ Caddy HTTPS profile), systemd unit (§21 #108, docs-v2/self-host.md) | Docker, Node 22 |
| `backend/firestore.rules`, `storage.rules` | deny ALL client access | |
| `frontend/` | cloud frontend SPA → Firebase Hosting | SvelteKit, adapter-static (SPA fallback `index.html`) |
| `agent/` | `opm-agent` (module `opm/agent`) | Go 1.23, SQLite (pure Go driver, `modernc.org/sqlite`), Docker Engine API, Caddy v2 as library |
| `agent/gui/` | agent GUI SPA, embedded with `go:embed`, served at `/_opm/` | Svelte (SvelteKit adapter-static, base `/_opm`) |
| `ui/` | shared form components + design tokens of BOTH GUIs (one form per entity, §21 #102), consumed as source via the `$opm` alias | Svelte 5, `@opm/ui` (no build) |
| `e2e/` | hello-world images, test scripts, results | Go (images), shell/TS (scripts) |

### 1.2 Domains and hosting

- API: `https://<apiBase>/v1/...`. Production default `https://api.opm.example`. With Firebase: the Hosting site rewrites
  `/v1/**` to the function `api`, so the frontend and the API share one origin (`https://app.opm.example/v1/...` also works).
  The backend reads its public base URL from env `OPM_API_BASE` (used in install commands and `SetupResponse.apiBase`).
- Frontend: `https://app.opm.example` (Firebase Hosting). It calls `VITE_OPM_API_BASE` (default: same origin).
- The API router strips any mount prefix: route matching AND request signing use the path starting at `/v1/`.
- Firestore + Storage are touched ONLY by the backend (Admin SDK). Rules deny all client reads/writes.
- Firebase Auth is used ONLY for human login. Nothing on a site uses Firebase.
- Self-hosted (§21 #108): `opm-cloud` serves API + console + `/auth` + `/_blob` from one origin (`OPM_PUBLIC_URL`);
  the data model below is identical, stored in SQLite (one global file + one file per deployment) and files.

### 1.3 Agent listeners (one process)

| listener | port (config) | server | serves |
|---|---|---|---|
| edge HTTP | `SiteConfig.edge.httpPort` (80); before a head agent.json `edgeHttpPort` (80) | embedded Caddy | `/_opm/*` (GUI + API, except `/_opm/peer/*` → agent 404), then routes, then the not-found page. ALWAYS running (§21 #86) |
| edge HTTPS | `edge.httpsPort` (443, null = off) | embedded Caddy | same, TLS per host; bound only when a host has TLS (internal / acme / files) |
| GUI alias | `agentGuiAliasPort` (8080, null = off) | agent net/http | `/_opm/*` GUI + API only (`/` → 302 `/_opm/app`; `/_opm/peer/*` and anything else → agent 404) |
| agent port | `agentPort` (7080) | agent net/http | `/_opm/*` GUI + API (fallback) AND `/_opm/peer/*` (mesh) |

**The edge always runs** (§21 #86). Before the site has a head (fresh install) — and when this site is not in the head
config — the agent runs the built-in **default edge** on agent.json `edgeHttpPort` (default 80; null = none): host `*`, no
TLS (no 443), `/_opm/*` → GUI + API, `/` → 302 `/_opm/app`, everything else → the agent's "No configuration applied yet —
open /_opm/ to approve the first update" page (404; "Site not configured" when the site is not in the head). Next to it the
agent listens on agent.json `agentPort` (7080) and `guiAliasPort` (8080). After a head exists the listeners follow the head
SiteConfig of this site (re-bound in the APPLY step); a head whose edge has no routes still serves `/_opm/` and answers every
other path with the site's not-found page. **The agent owns the edge sockets**: it binds each edge port once and hands Caddy a
dup (`fd/<n>` listen address); a config swap (default edge → first head, every APPLY, upstream refresh) is a graceful
`caddy.Load` on the SAME socket (no unbind / rebind, no refused connection; in-flight requests get a 30 s grace period).
A port that no config uses any more is closed after the swap.

Bind failures: the failing listener is skipped, the others keep running; alert + activity `agent.port-bind-failed
{ port, purpose: edge-http|edge-https|gui-alias|agent, error, owner? }` (once per port, not per retry). `owner` = the process
holding the port, read from /proc (Linux, agent as root: "nginx (pid 812)"). A busy edge port is retried every 30 s; the GUI
stays on 8080 / 7080; heartbeat `edge.bindErrors[].owner` → cloud Setup alert "port 80 is used by nginx (pid 812) — stop it or
change edge.httpPort"; the agent GUI Status page shows the same alert. During an APPLY, an edge bind/reload failure fails
the step (→ rollback).

### 1.4 Outbound traffic of a site

Only HTTPS to the API host (+ ACME CA for hosts in TLS mode acme). Downloads use API endpoints (200 stream or 302 to a
5-minute signed Storage URL — agents MUST follow 302). Route order for every cloud call: (1) own internet, (2) a peer with
internet (`/_opm/peer/relay` → `/v1/agent/relay`), (3) the browser tunnel (§9.10). Optional `proxy` in agent.json /
`HTTPS_PROXY`; extra CA bundle `/etc/opm/extra-ca.pem`.

---------------------------------------------------------------------------------------------------------------------

## 2. Cloud data model

Storage-neutral since §21 #108: the paths below are DocStore / BlobStore paths (Firestore + Cloud Storage on Firebase;
SQLite tables `{ id, parent, value, ix_* }` + files on opm-cloud — table mapping in backend/functions/src/adapters/local/README.md).
All types: `shared/src/types.ts` (Appendix A). Firestore stores `Ts` as Timestamps; the API converts to ISO strings.
Limits: label ≤ 80, description ≤ 2000, Value ≤ 4096 chars, any document ≤ 900 KB (file contents never inline).

### 2.1 Firestore paths

| path | type | doc id | notes |
|---|---|---|---|
| `registries/{registryId}` | `Registry` | auto | password stored plain (prototype); reads mask it |
| `services/{serviceSlug}` | `Service` | serviceSlug | the only id; `create()` gives uniqueness (no `serviceSlugs` index collection) |
| `services/{serviceSlug}/images/{version}` | `Image` | version | `create()`; exists → conflict `version-exists` |
| `icons/{iconId}` | `IconDoc` | auto | uploaded icons; bytes inside the doc (internal `_bytes`, ≤ 64 KiB); de-duplicated by sha256 |
| `notFoundPages/{notFoundPageId}` | `NotFoundPage` | auto | create-only |
| `tags/{tagSlug}` | `Tag` | slug | |
| `accounts/{uid}` | `AccountDoc` | Firebase uid | email unique (Firebase Auth enforces); `lastSetPassword { value, setAt, setBy, validAfter }` and `lastResetLink { value, createdAt, createdBy, expiresAt, validAfter }` stored plain (§5.1, §21 #84); the API (`Account`) returns only their metadata |
| `auditLog/{auditId}` | `AuditLogEntry` | auto | root-scope audit, append-only (§6.2; changes/data as JSON strings) |
| `templates/{templateId}` | `Template` | auto | arrays inline |
| `deployments/{deploymentId}` | `Deployment` | auto | draft inline; `deleted` soft delete; `accessKey` = the plaintext access key (grouped, stored plain like secrets; null on pre-#84 deployments) next to `accessKeyHash` — neither is in `DeploymentView` or the cloud view (agents get only `{ hash, version }`) |
| `deployments/{id}/revisions/{revId}` | `Revision` | ULID | immutable; plain secrets inside |
| `deployments/{id}/proposals/{revId}` | `Proposal` | ULID = revision | |
| `deployments/{id}/agents/{siteSlug}` | `AgentDoc` | slug | written only by agent calls (and revoke) |
| `deployments/{id}/installTokens/{tokenHash}` | `InstallToken` | sha256 hex of the token string | `token` = the plaintext `opi_…` token, stored while the token can be used (revealInstallCommand; after enrolling it is the site's upgrade command, revealUpgradeCommand, §21 #92); nulled on revoke / Regenerate, expiry of an UNUSED token (daily job) and revokeAgent; null on pre-#84 tokens and on tokens used before #92. `kind` install (absent) \| maintenance (§21 #92: `expiresAt` null, `boundEnrolledAt`) |
| `deployments/{id}/activities/{siteSlug}:{seq}` | `Activity` | `<siteSlug>:<seq>` | upsert never overwrites; cloud log uses `_cloud:<seq>` |
| `deployments/{id}/transfers/{transferId}` | `Transfer` | auto | |
| `deployments/{id}/volumeContent/{siteSlug}:{volumeSlug}` | `VolumeContentState` (stored part) | `<site>:<volume>` | §21 #101 persist-to-cloud: latest / last snapshot, push, snapshot request, last agent report |
| `githubMirror/config` (+ `queue/{itemId}`, `branches/{branch ~}`, `blobs/{sha256hex}`) | `GithubMirrorDoc`, `MirrorQueueItem`, `MirrorBranchState` | fixed / see §21 #106 | §21 #106 GitHub mirror: settings + token (server-only), push queue, per-branch status, git blob cache. `Template.slug` / `Deployment.slug` = the stable entity slugs (immutable) |
| `deployments/{id}/volumeContent/{siteSlug}:{volumeSlug}/versions/{versionId}` | `VolumeContentVersion` | ULID | seq per volume; tree = blob `files/<treeHash hex>` |
| `deployments/{id}/uptime/{siteSlug}_{yyyymmdd}` | `UptimeDay` | | 30 days kept |
| `deployments/{id}/revokedKeys/{fingerprint}` | `{ siteSlug, publicKey, revokedAt }` | key fingerprint | keys of revoked enrollments (verify old .opmlog bundles) |
| `uploads/{uploadId}` | `Upload` | auto | multi-part uploads (blobs, transfers) |
| `agentNonces/{sha256hex(deploymentId/siteSlug/nonce)}` | `{ expiresAt }` | | replay cache; created with `create()` (fails if it exists); TTL policy on `expiresAt` |
| `loginLimits/{sha256hex(ip)}` | `{ fails, windowStart }` | | not used by the cloud (agent only); reserved |
| `rateLimits/reveal_{uid}_{yyyymmddhhmm}` | `{ count, expireAt }` | | credential-op counter (`CREDENTIAL_OPS`, `REVEAL_RATE_PER_MINUTE` = 30 per uid per fixed minute, §3); `expireAt` = window end + 2 min (TTL policy) |

Indexes (composite): `activities` (ts desc), (siteSlug, ts desc), (origin, ts desc), (type, ts desc), (actor, ts desc),
(uploadedVia, ts desc); `proposals` (createdAt desc); `revisions` (seq desc); `transfers` (siteSlug, direction, createdAt desc);
`deployments` (tagSlugs array-contains, updatedAt desc), (deleted, updatedAt desc); `auditLog` (target | actor | actorEmail |
targetType | outcome, ts desc), (_actionKeys array-contains, ts desc). Collection group `images` (registryId) for
"registry in use".

### 2.2 Document rules (beyond the types)

- **One identifier per entity.** The identifier is the document id and never changes; no entity has a second id field.
  Registry `registryId` (auto) · Service `serviceSlug` · Image `version` (within its service) · NotFoundPage `notFoundPageId`
  (auto) · Tag `tagSlug` · Template `templateId` (auto) · Deployment `deploymentId` (auto) · Site `siteSlug` (within its
  deployment) · Container `containerSlug` (within its site) · Account `uid` · Icon `iconId` (auto). References use the id: a
  container names `serviceSlug` + `version`; routes, depends and expressions name a container by `containerSlug`.
- **Registry**: create needs label, username, password; `username ""` + `password ""` = anonymous (public images); one empty
  and one set → invalid. `url` default `https://registry-1.docker.io`; https only, no path. Changing url/username/password
  resets `lastCheck` to null. Delete → `conflict` (details.usedBy = `{ serviceSlug, version }[]`) while any image uses it.
- **Service**: `serviceSlug` = doc id, unique + immutable (PATCH with serviceSlug → invalid V_IMMUTABLE). No `serviceId`, no
  `repository`: each image holds its own full reference. `icon` optional at create (default null). Delete only with 0 images.
- **Image**: `version` = doc id, `RE.version` `^[a-z0-9][a-z0-9._-]{0,63}$` (v1, v2, 3.1.0), unique in the service
  (re-registering → conflict `version-exists`). `reference` = the full docker reference as entered (≤ 512); `source` = its
  parsed form (`ImageRefParts`: host, repository, tag, digest); `registryId` = the registry whose credentials are used
  (auto-matched by host, user may override) or null (public). **Identity** (immutable, PATCH → invalid V_IMMUTABLE): version,
  serviceSlug, source, digest, platform, import (mode, status, layers …), createdAt / createdBy; there is no re-fetch of a
  ready image ("Clone as new version" registers another one). `reference` + `registryId`: PATCH only while
  `import.status = failed` (else conflict). **Editable** (PATCH, §21 #82, same rules as registration = `validateImageSpec`):
  kind, entrypoint, command, workdir, user, stopTimeoutSec, ports / env / volumes (full lists: add, remove, rename, change
  number / protocol / defaults / options / flags / mount paths / descriptions), healthcheck, description. A RENAMED or REMOVED
  port / env / volume name still referenced by a template or a deployment DRAFT → 409 conflict `in-use`
  (`ImageInUseDetails`); `renameEverywhere` rewrites renamed names in TEMPLATES only — deployment drafts are detached and
  keep the old names until a Template sync or a manual edit (§6.1, §21 #94). Editing never changes existing revisions (they
  carry an ImageSpec copy). Delete → conflict while any template, deployment draft, head revision or open proposal uses `<serviceSlug>:<version>`.
- **ImageVolume** (§21 #83): `{ name, mountPath, kind: 'dir' | 'file', description }` — `kind` = what the image keeps at
  mountPath: a Folder, or ONE File (`/data/flows.json`). Always stored explicitly (register / patch / migrate-f); a request may
  omit it (`ImageVolumeInput`) → `inferImageVolumeKind(mountPath)`: the last path segment (`{{ }}` blocks → "x") has an
  extension — a name, a dot, 1–10 letters / digits with at least one letter, not ".d" — → `file`, else `dir` (`/data`,
  `/etc/nginx/conf.d`, `/root/.config`, `/opt/app-1.2` are folders; `/data/settings.js`, `/app/env/{{ container.env.NODE_ENV }}.ts`
  are files). Registry `Volumes` entries are always `dir`. Readers of a document / revision catalog without the key use
  `imageVolumeKind(v)` (same inference); the API normalizes on read (`imageView`). Invalid kind → V_ENUM; a File at a path ending
  with "/" → V_VOLUME_KIND. A kind change is always allowed (no in-use check); the container validation re-checks rows (§11).
- **ImageEnv**: `value?: Value` (string) + `options?: string[] | null`. options = "choice": 1–50 unique non-empty plain
  strings; the default must be one of them (V_OPTION at save); a container value must resolve to one of them (V_OPTION at
  publish); secret env → no default, no options. Legacy documents may hold `value: string[]`; every reader goes through
  `normalizeImageEnv()` (options = the list, value = the first item); every write stores `options` (null or the list).
- **Icon** (`Icon = string | null`; services, templates, tags; `Account.img`, `Deployment.image`): null = the entity type
  default (`DEFAULT_ICONS`, shared/src/icons.ts); `lucide:<name>` = a Lucide icon of the curated set (`LUCIDE_ICONS`, 350
  icons in `ICON_GROUPS`, generated by shared/scripts/gen-icons.mjs); `upload:<iconId>` = an uploaded icon (`icons/{iconId}`,
  must exist); a bare `<name>` is legacy and read as `lucide:<name>`; `Account.img` / `Deployment.image` also accept a legacy
  https URL (`RE.picture`). Only `Revision.catalog.services[*].icon` holds `data:image/…` URLs (uploads embedded at publish).
- **IconDoc**: `{ iconId, name (1–80), contentType (image/png | image/svg+xml | image/webp), sizeBytes (1–65536), sha256,
  createdBy, createdAt }` + internal `_bytes`. Uploads are de-duplicated by sha256. Delete → conflict while used.
- **NotFoundPage**: htmlCode ≤ 262144 UTF-8 bytes (else `too-large`); PATCH/PUT → 405 `method-not-allowed`; delete →
  conflict while used by a template / draft / head.
- **Tag**: delete removes the slug from every `deployments.tagSlugs` and every access-rule scope (batched, ≤ 500 writes per batch).
- **Account**: created only via POST /v1/accounts (needs accounts.admin). The last account holding accounts.admin cannot lose
  it or be disabled (conflict). Nobody can disable themselves (conflict). First admin: §5.1. Stored credentials (§21 #84,
  type `AccountDoc`): `lastSetPassword { value, setAt, setBy, validAfter }` = the last password an admin set (creation or
  "Set new password…"), `lastResetLink { value, createdAt, createdBy, expiresAt (+1 h), validAfter }` = the last reset link;
  `validAfter` = the Auth user's `tokensValidAfterTime` right after the set. A later `tokensValidAfterTime` (the person changed
  their password, used a reset link, or tokens were revoked) clears both (checked on getAccount / listAccounts / the reveals).
  The API `Account` carries only `{ setAt, setBy }` (+ `expiresAt` for the link) — never the values.
- **Template**: `version` 1 at create, +1 per PATCH (`ifVersion` must equal → else conflict). `secrets[].value` always null.
  Template delete is always allowed (deployments keep `srcTemplate.snapshot` + `baseSnapshot`; their Template sync shows
  "template deleted — change the template or unlink"). Templates keep no version history (§21 #94).
- **Deployment**: create copies the template into `srcTemplate.snapshot` and `draft`, then generates a value for every
  secret with `generate` (charset alnum = `[A-Za-z0-9]`, hex = `[0-9a-f]`, base64url = `[A-Za-z0-9_-]`, crypto random,
  exact `length`), sets `rotatedAt`. Generates the access key (§5.4) and stores it twice: `accessKey` (plaintext, grouped,
  §21 #84 — returned only by createDeployment / rotateAccessKey / revealAccessKey) and `accessKeyHash` (argon2id, the only
  form agents get). `accessKey: null` = created before #84 ("not stored — regenerate to view"). `draftVersion` 0,
  `agentEpoch` 1, `head` null. `srcTemplate` = `{ templateId, templateVersion, snapshot, copiedAt, baseVersion =
  templateVersion, baseSnapshot = the template config (deep copy), lastSyncedAt / lastSyncedBy null, skipped [] }`
  (Firestore: map `srcTemplate {templateId, templateVersion, copiedAt, label, baseVersion, lastSyncedAt, lastSyncedBy}` +
  JSON strings `_srcSnapshot`, `_srcBase`, `_srcSkipped`; documents without the base fields decode to base = snapshot).
  **Detached (§21 #94):** the create is a deep copy (variables with values, secret definitions with per-deployment generated
  values, files = immutable content-addressed blob refs, sites with containers / edge / volumes / settings, 404-page refs to
  the catalog); nothing reads the live template for a deployment afterwards (validation, diff, publish, file content,
  previews, staging, agents). Template edits, file-content edits (new blobs) and template deletion never change the
  deployment, and nothing syncs automatically; template changes reach the draft only via **Template sync** (§6.1). DELETE (body `confirm` = label) sets `deleted: true`, `agentEpoch+1`; deleted
  deployments are invisible in every human endpoint (404) and agents get `deleted: true` in the cloud view.
- **InstallToken**: `token` holds the plaintext `opi_…` token while the token is active (§4.2, §21 #84); enroll (used),
  revoke and the daily job (expired) set it to null in the same write that changes the state. Pre-#84 tokens have
  `token: null` ("not stored — regenerate to view"). `InstallTokenView` never carries the token, only `stored`.
- **agentEpoch**: +1 (in the same transaction) on: publish, any proposal status change, local proposal registration,
  access-key rotate, enroll, agent revoke, to-site transfer becoming ready or expiring/deleted, deployment delete,
  reportHead that changes `head`. It is the poll cursor.
- **Revision**: `hash = "sha256:" + hex(sha256(JCS(config)))`, `catalogHash` likewise over `catalog`. The cloud recomputes
  both on POST /v1/agent/head and refuses a mismatch (invalid).
- **Proposal.sites**: initialised at publish to `{ pending }` for every site of (parent config ∪ proposed config).
- **SiteConfig.host**: `Value | null`; **null = auto** (default of `defaultSiteConfig()`; `normalizeSiteConfig` turns `""`
  into null). Auto = the site's `advertiseHost` (agent.json, else its first detected IPv4; `AgentDoc.detectedAddresses`).
  A host expression that resolves to null or `""` falls back to auto (warning V_HOST_AUTO, never an error). Auto addresses are
  not part of the revision (hash unchanged). Resolution: §10.5 `resolveSiteHosts`.
- **Container instances** (`SiteConfig.containers`): any number per site, also several of the same service + version.
  `containerSlug` `^[a-z][a-z0-9-]{1,39}$`, unique in the site (V_DUPLICATE), not `new` (`RESERVED_CONTAINER_SLUGS`,
  V_RESERVED); default = the service slug, then `<slug>-2`, `-3` … (`suggestContainerSlug`). Immutable once created (editors
  allow it only while new; "Duplicate" = `duplicateContainer`: new slug, host ports cleared, a volume named
  `<old>-<imageVolume>` becomes `<new>-<imageVolume>` (the editor DECLARES it via `undeclaredVolumes` → `newSiteVolume`,
  copying label + backup of the original), every other volume / host / file / secret source kept = shared).
  `depends[].containerSlug` = containers of the same site, no cycles.
  - **ports**: at most one row per image port (`portName` must be an image port); a missing row = not published, `edge: true`
    (`containerPorts`). `hostPort` number | `{{ }}` | null (not published); ≠ 80/443/edge/agent ports, unique per site +
    protocol (the protocol is the image port's; conflicts ignore `hostIp` — 0.0.0.0 covers every address). `hostIp`
    `0.0.0.0` (default) | `127.0.0.1`. `edge: false` = the edge may not proxy to it (V_EDGE_ROUTE).
  - **volumes** (`ContainerMount`): at most one row per image volume (`imageVolume` set; `mountPath` null = the image
    mountPath, a value overrides it); an image volume WITHOUT a row is NOT mounted (there is no implicit data volume;
    warning V_VOLUME_NOT_MOUNTED; `containerMounts` leaves it out; EffectiveMount keys `image:<vol>` / `volume[i]`); a row
    with source `none` = "Not mounted" chosen by the user (no warning). `defaultContainer()` creates NO volume rows (the user
    picks per image volume). Extra mounts: `imageVolume` null, `mountPath` required (absolute, may hold `{{ }}`, field
    mountPath), source ≠ `none`. Sources: `volume { volumeSlug }` (a volume DECLARED in `SiteConfig.volumes`, else
    V_VOLUME_UNDECLARED; several mounts / containers may share one), `host { path }` (advanced: bind of an absolute literal
    host path, `isSafeMountPath`, no `{{ }}`; never backed up), `file { fileSlug }` (the ONE copy per site at the file's
    location, shared read-only by every container that mounts it), `secret { secretSlug }` (one copy per site at
    `<agentHome>/secrets/<secretSlug>`), `none` (image
    volume not mounted: container storage, lost on recreate). `readOnly`: volume + host default false (read-write); file +
    secret MUST be true (V_READONLY). Mount paths unique per container. V_VOLUME covers the remaining source rules; the
    removed kind `data` → V_ENUM (hint: "replaced by declared site volumes").
  - **env**, **command / entrypoint** (null = image), **restart**, **resources** (cpus 0.1–64 / memoryMb 64–1048576, null = no
    limit), **healthcheck** (null = the image's).
  - Legacy one-per-service documents (`port`, `volume`, no containerSlug, `depends[].serviceSlug`, proxy `serviceSlug`) are
    read through `normalizeContainer` / `normalizeSiteConfig` / `normalizeConfig` (containerSlug = serviceSlug; legacy data
    volume `<name>` → source `volume { volumeSlug: <serviceSlug>-<name> }`; file / secret → read-only extra mounts). Agents
    refuse old-shape revisions ("publish again from the cloud").
- **Site volumes** (`SiteConfig.volumes: SiteVolume[]`, round D; field order of SiteConfig: host, agentPort,
  agentGuiAliasPort, volumes, containers, depends, notFoundPageId, edge): the data folders of a site. `volumeSlug` is the ONLY
  id, unique per site (V_DUPLICATE), `RE.volumeSlug` `^[a-z0-9][a-z0-9_.-]{0,99}$` (V_FORMAT; same pattern as the round-C
  data names, so they survive); `label` 0–80 (empty → the GUIs show the slug), `description` 0–2000 (V_LENGTH), `backup`
  boolean, default true (V_TYPE); `location` (StorageLocation, default auto = `<agentHome>/volumes/<volumeSlug>`; V_LOCATION,
  see "Storage locations"); at most `LIMITS.volumesPerSite` = 100 (V_TOO_MANY); `volumes` missing → V_REQUIRED, not a
  list → V_TYPE. A volume is never renamed (rename = a new volume; the old directory stays on disk). Several containers /
  mounts may share one volume (`volumeUsers(sc)` → `{ [volumeSlug]: VolumeUse[] }` for "used by" / "shared with"). A mount of
  an undeclared volume → V_VOLUME_UNDECLARED (error, save; `undeclaredVolumes(sc)`); a declared volume mounted by no
  container → V_VOLUME_UNUSED (warning; its data stays on disk). Helpers: `newSiteVolume(slug, label = slug, description =
  '', backup = true)`, `defaultVolumeSlug(containerSlug, imageVolume)` = `<containerSlug>-<imageVolume>` (the suggestion of
  "+ New data volume…"), `suggestVolumeSlug(base, taken)` (base, base-2 …), `normalizeSiteVolume`. Removing a volume from the
  config never deletes its directory on the sites (orphan, §2.4).
  - **Legacy normalization** (`normalizeSiteConfig(sc, images?)`, `isLegacySiteConfig`): a site config WITHOUT `volumes` is
    pre-round-D → every image volume without a row becomes an explicit row `volume { volumeSlug: <containerSlug>-<imageVolume> }`
    (when the image is known), every `{ kind: 'data', name }` becomes `{ kind: 'volume', volumeSlug: name }` with
    **readOnly reset to false** (the round-C editor stored read-only for data volumes by mistake; a deliberate choice cannot be
    told apart; the migration dry run lists every reset), and every such volume is DECLARED `{ volumeSlug, label: volumeSlug,
    description: '', backup: true, location: auto }`. A current config (with `volumes`) is never auto-declared. Idempotent. The cloud
    normalizes on every write and read (`backend/scripts/migrate-d.ts` converts stored documents); the agent decodes legacy
    `data` in old revisions the same way (so their hash is unchanged).
  - **Local edits** (agent GUI local proposal): adding, editing (label / description / backup / location / kind / seed) and
    removing site volumes IS allowed (config, like env values); removing a volume that is still mounted fails validation
    (V_VOLUME_UNDECLARED). Mount sources may change locally; containers still cannot be added / removed / renamed.
  - **Kind** (round E, `SiteVolume.kind`, `SiteVolumeKind`): `dir` (default) = a folder; `file` = ONE persistent read-write
    file at the location (auto: the file `<agentHome>/volumes/<volumeSlug>`), created empty by the agent before the bind
    mount (Docker would create a directory), mounted at a FILE path (V_VOLUME_KIND). On an image volume row (no own mount
    path) the kind must match `ImageVolume.kind` (§21 #83): a `file` volume on a Folder image volume → warning
    V_VOLUME_KIND_IMAGE; a `dir` volume on a File image volume → error V_VOLUME_KIND_MISMATCH; every read-write mount of a
    `file` volume → info V_FILE_VOLUME_RENAME (once per mount). A single-file bind mount cannot be replaced by rename (EBUSY): apps that save with temp file +
    rename (Node-RED `flows.json`) need a `dir` volume — the GUIs show `FILE_VOLUME_RENAME_WARNING` ("Apps that save by
    writing a temp file and renaming it (e.g. Node-RED flows.json) need a folder volume — a single-file Docker mount breaks on
    rename. Use a folder volume seeded with the files instead."). Backed up / restored like any volume (a file volume is
    one `./file` entry of its archive, restored in place). A kind change of a volume whose location holds data fails
    STAGING ("… use a new volume name"); the GUIs fix the kind after create.
  - **Recommended for Node-RED** (§21 #83, **updated by §21 #101**: `flows.json` AND `settings.js` are entries of the folder
    volume's OWN content — a Config file is never volume content and never mounted inside a writable volume
    (V_FILE_IN_WRITABLE_VOLUME); make the volume persist-to-cloud to edit flows in the cloud. The rest of this item is the
    round-E text, kept for history): a FOLDER data
    volume (e.g. `nodered-data`, kind `dir`) mounted at `/data` (an extra mount, or the image volume `/data` when the image
    declares one), with Starter content From files `flows.json ← <flows file>` (+ `settings.js ← <settings file>`), so
    Node-RED saves flows by rename inside a real folder; `settings.js` either seeded the same way (editable on site) or mounted
    read-only as an extra mount at `/data/settings.js` from a Config file (a read-only single file is never renamed). Single-file
    volumes on File image volumes (flows.json ← `nodered-flow`) are valid and show no warning, but a read-write one keeps the
    rename INFO: Node-RED's "Deploy" will fail with EBUSY on it.
  - **Volume content** (§21 #101, normative — supersedes the File-based kinds below): `seed` kind `tree { entries [{ path,
    sha256, size, mode, render }], dirs }` (folder volumes) / `single { sha256, size, mode, render }` (single-file volumes);
    blobs `files/<hex>`; paths, limits, canonical form and hash in §21 #101 a; render per entry (b). Legacy `files` / `file`
    (Config files as content) → V_VOLUME_SEED_FILE_REF except in revisions published before 0.12 (agent mode). The seeding rules
    below are unchanged for every kind. **Persist to cloud** (`persist { exclude, maxBytes, auto }`, omitted when off): §21 #101
    e–i. Files mounted inside a writable volume / host mount of the same container → V_FILE_IN_WRITABLE_VOLUME.
  - **Starter content** (round E, `SiteVolume.seed: VolumeSeed | null`; null = starts empty): `files { entries: [{ path,
    fileSlug }] }` (dir volumes: 1–100 entries; path relative inside the volume, segments `[A-Za-z0-9._-]`, no "." / "..",
    unique — `seedPathError`; content = a Config file, `render: 'expressions'` files rendered once per site like mounted
    files), `file { fileSlug }` (file volumes: the file's content, rendered the same way), `archive { blobSha256, name, size }`
    (dir volumes: a `.tar.gz` / `.tgz` ≤ `SEED_ARCHIVE_MAX` 200 MiB uploaded with `CreateUploadRequest.purpose
    'seed-archive'`, stored as a content-addressed blob; never rendered). **Seeding** (agent, APPLY, after snapshot restores
    and before any container starts): only when the volume is new or EMPTY (dir without entries / file of size 0) and not
    yet seeded on this site → files written 0644 (dirs 0755) / the file written in place / the archive extracted safely
    (regular files + dirs only, relative paths, no "..", no absolute paths, no symlinks / hard links / devices
    (`ErrUnsafeArchive`), ≤ 2 GiB uncompressed); then it is marked seeded (dir: marker `.opm-seeded`; both: agent kv
    `volume-seeded:<slug>`) and is NEVER seeded again — also when emptied later. A changed seed never touches existing data:
    the review shows `DiffChange.note` "seed changed — applies only to new / empty volumes" (also for a kind change).
    **Reset to starter content** (agent GUI, this site only): `resetVolumeStart` → 60 s intent (kind `volume-reset`) →
    `resetVolume` with typed ACCEPT → manual snapshot of that volume (note "before reset of <slug>") → stop the containers that
    mount it → wipe (dir: remove entries; file: truncate) → seed → start them; activities `snapshot.created`,
    `volume.reset-to-seed { siteSlug, volumeSlug, snapshotId, entries }`, `volume.seeded`. `SiteVolumeStatus.seeded { at,
    source, entries } | null`. Normalization: missing kind → `dir`, missing seed → null (`normalizeSiteVolume`; migrate-e).
- **Edge** (`SiteConfig.edge: Edge`, round E shape; semantics §12): `Edge { httpPort, httpsPort, maxBodyBytes (null =
  unlimited), hosts, routes }`; `EdgeHost { hostname, tls, redirectHttpToHttps, responseHeaders: EdgeResponseHeaders }`;
  `EdgeResponseHeaders { set, add: Record<name, Value>, remove: string[], always: string[] }`; `EdgeRoute { routeId, hosts,
  match: EdgeMatch { kind: prefix | exact | regex, path, stopRegex, methods }, action, order, responseHeaders:
  EdgeResponseHeaders | null }` (null = inherit the host's; an object replaces it; null on rewrite routes); actions
  `EdgeProxyAction { type 'proxy', containerSlug, portName, stripPrefix, rewrite: { regex, replacement } | null, addPrefix,
  setHeaders, removeHeaders, websocket, connectTimeoutSec, sendTimeoutSec, readTimeoutSec, requestBuffering,
  responseBuffering, maxBodyBytes (null = inherit, 0 = unlimited) }` · `EdgeRedirectAction { type 'redirect', to, status 301 |
  302 | 303 | 307 | 308, keepPath }` · `EdgeRewriteAction { type 'rewrite', regex, replacement }`. Helpers: `newEdgeHost`,
  `newEdgeRoute`, `newProxyAction` (websocket true, timeouts 60, buffering off, maxBodyBytes null), `moveToResponseHeaders`.
  - **Legacy normalization** (`normalizeEdge`, pure, idempotent; applied by `normalizeSiteConfig` on every cloud write /
    read and by the agent decoder): `match { pathPrefix, methods }` → `{ kind: 'prefix', path, stopRegex: false, methods }`;
    proxy `timeoutSec` → readTimeoutSec (connect / send 60); missing new proxy fields → defaults; `websocket` KEPT as stored;
    route responseHeaders → null; host responseHeaders → empty; edge maxBodyBytes → null. Stored revisions keep their bytes:
    the agent re-emits the raw JSON of an unchanged legacy edge so the revision hash stays valid (agent decision E1).
    `backend/scripts/migrate-e.ts` rewrites stored templates / drafts / revisions (schema renames only; route ids, paths,
    order and headers untouched; its dry run notes plain prefixes without a trailing "/" that now also match "/apix", and
    response-only request headers — W_RESPONSE_HEADER_AS_REQUEST, never auto-moved).
- **FileDef.render**: `'expressions' | 'none'`; default text → expressions, binary → none (binary + expressions → V_ENUM;
  legacy documents without the field read as the default, `normalizeFileDef`). `content` is always the SOURCE blob; rendered
  bytes exist only on the site (§9.5, §2.4). A file is rendered ONCE PER SITE (field `file`, roots var, secret, site, self,
  deployment — `container.*` / `file.*` → E_NOT_ALLOWED "files are shared by all containers on the site — use a variable
  instead", §10.2).
- **Storage locations** (round D part 2; `StorageLocation = { kind: 'auto' } | { kind: 'path', path }`): `FileDef.location`
  (auto = `<agentHome>/files/<fileSlug>`) and `SiteVolume.location` (auto = `<agentHome>/volumes/<volumeSlug>`). A missing
  location = auto (`normalizeFileDef`, `normalizeSiteVolume`, `newSiteVolume(…, location = auto)`, `normalizeLocation`).
  Each site holds ONE copy of every file of the config at its location (every container that mounts the file bind-mounts
  that single host file read-only), one copy of every mounted secret at `<agentHome>/secrets/<secretSlug>` (0400, not
  configurable) and one folder per declared volume at its location. Custom path (`checkLocation`, V_LOCATION): `./x/y` =
  relative to agentHome — no empty, `.`, `..` segments, no spaces, the first segment not a reserved agent entry
  (`AGENT_HOME_RESERVED`: site.key, opm.db*, blobs, snapshots, certs, edge, transfers, tunnel, logs, notfound, rendered,
  secrets, vol, files, volumes) except its own kind dir (a file may use `./files/<name>`, a volume `./volumes/<name>`, never
  the bare kind dir); or an absolute path used as is (`isSafeMountPath`: not "/", no spaces, no ".."); never `{{ }}`. Per
  site, the locations of all files (Config.files, every site) and of that site's volumes must not be the same place or nested
  in each other (V_LOCATION; two relative or two absolute paths are compared by the validator, a relative vs an absolute one
  only by the agent at staging after resolving against agentHome). Helpers: `locationPath(loc, 'files' | 'volumes', slug,
  agentHome?)`, `locationLabel(…)` ("auto · <agentHome>/files/<slug>"). Changing a location moves nothing automatically
  except the round-D `vol/` → `volumes/` move (§2.4); the old place stays on disk.
- **UptimeDay**: heartbeat increments `beats[hourUTC]` (FieldValue.increment) and sets `hours[h] = min(1, beats[h]/120)`.
  The `daily` scheduled function deletes uptime docs older than 30 days, expires uploads (transfers never expire), and sets `token` to
  null on expired install tokens (§21 #84).

### 2.3 Cloud Storage paths

| path | holds | written by |
|---|---|---|
| `blobs/sha256/<hex>` | OCI/Docker blobs: platform manifest, image config, layers (content-addressed) | import job (copy) / first agent fetch (gateway) |
| `files/<hex>` | file contents (FileDef.content), volume content files (§21 #101) and volume content trees (canonical JSON, hex = treeHash), content-addressed | POST /v1/blobs, uploads(purpose blob), agent PUT /v1/agent/volume-content/blobs/{sha256}, the cloud (trees) |
| `logos/<deploymentId>` | legacy deployment logo (new logos are icons: `Deployment.image` = `upload:<iconId>`; icon bytes live in `icons/{iconId}`, not in Storage) | API |
| `transfers/<deploymentId>/<transferId>` | send / receive files | uploads (purpose transfer) |
| `uploads/<uploadId>/part-<n>` | temporary parts (deleted after complete or expiry) | PUT …/parts/{n} |
| `activity-bundles/<deploymentId>/<ts>-<rand>.opmlog` | uploaded manual bundles (kept 90 days) | activities/upload |
| `agent/<version>/<os>-<arch>/opm-agent`, `.sha256`, `.sig` | agent binaries + signature | release CI / e2e script |
| `agent/latest` | text: the current version string | release CI |

### 2.4 Agent disk layout

```
/etc/opm/agent.json                  AgentConfigFile (types.ts)
/etc/opm/install-token               install token file (0600), deleted after enroll
/etc/opm/extra-ca.pem                optional
/etc/systemd/system/opm-agent.service
/etc/systemd/system/opm-agent.socket  §21 #91 socket activation (§19.2; absent with OPM_SOCKET_ACTIVATION=0 / systemd < 235)
/etc/systemd/system/opm-agent.service.d/upgrade-guard.conf
                                     §21 #90 boot-guard drop-in (§19.2): ExecStartPre=-/bin/sh
                                     /usr/local/lib/opm/upgrade-guard.sh <dataDir> <binary>. Written by
                                     `opm-agent upgrade-guard --install` (install script, and the agent before every upgrade)
/usr/local/lib/opm/upgrade-guard.sh  §21 #90 the boot guard (POSIX sh, 0755; one source: embedded in the Go agent)
/usr/local/bin/opm-agent
/usr/local/bin/opm-agent.prev        §21 #90 the previous (known-good) binary kept by the last upgrade = the rollback target
/usr/local/bin/opm-agent.new         §21 #90 the downloaded binary during an upgrade (verify → renamed over opm-agent at the
                                     swap; deleted when the upgrade fails before the swap)
<agentHome>/ (= the agent data dir: --data-dir / OPM_DATA_DIR, else agent.json dataDir, default /var/lib/opm; 0700)
  site.key                           Ed25519 private key: 32-byte seed, base64 std, 0600
  opm.db                             SQLite (WAL)
  volumes/<volumeSlug>/              declared site volumes with location auto (bind mounts of VolumeSource volume; one
                                     directory per volumeSlug wherever its location is, shared by every mount that names it;
                                     created 0755 on staging / apply). A volume with a custom location lives there instead.
                                     A directory under volumes/ whose volume is no longer declared is an ORPHAN: never
                                     deleted automatically, never backed up, listed (declared=false) by GET
                                     /_opm/api/sites/{s}/volumes. Round-D part-1 directories vol/<volumeSlug>/ are moved
                                     to the volume's location on the first apply when the target does not exist (the pre-C
                                     volumes/<serviceSlug>/<name>/ move is dropped: old-shape revisions are refused)
  files/<fileSlug>                   the ONE copy per site of every file with location auto: render none = the content
                                     byte for byte (0444); render expressions = rendered once for the site (0400 when the
                                     rendered content uses a secret, `RenderedFile.secret`, else 0444). Bind-mounted
                                     read-only (a single host file) into every container that mounts it. Rendered bytes
                                     never go to .opmlog / debug log / activities / snapshots
  secrets/<secretSlug>               the ONE copy per site of every secret mounted as a file, 0400 (tmpfs mounted at
                                     <agentHome>/secrets on Linux when possible)
  blobs/files/<hex>                  file blobs (deployment files, the SOURCE content), content-addressed, sha256-checked;
                                     an older files/<hex> blob store is moved here at agent start (files/ then holds only
                                     the per-site copies)
  blobs/sha256/<hex>                 image blobs (manifest, config, layers) — content-addressed, sha256-checked
  notfound/<notFoundPageId>.html     not-found pages of the head catalog
  snapshots/<snapshotId>/            manifest.json, sites/<slug>/config.json, sites/<slug>/volumes/<volumeSlug>.tar.zst
  certs/                             Caddy CertMagic storage, 0700
  edge/caddy.json                    last loaded Caddy config (secrets redacted)
  transfers/<transferId>             send / receive files
  tunnel/<reqId>.part<n>             large tunnel result parts
  logs/agent-YYYY-MM-DD.jsonl        debug log (7 days / 200 MB)
  upgrade/                           §21 #90 agent self-upgrade (0700; survives the restart; §7.5)
    state.json                       AgentUpgradeProgress of the running / last attempt (rewritten atomically per phase)
    history.json                     the last 10 attempts, newest first (AgentUpgradeProgress[])
    pending.json                     written before the restart: { upgradeId, from, to, newSha256, prevSha256, deadline
                                     (restart + AGENT_UPGRADE_HEALTH_SEC) }; read by the new agent, the watcher, the guard
    healthy                          written by the NEW agent once its startup finished (store, listeners, /_opm/healthz)
    rolled-back.json                 written by the watcher / boot guard after restoring opm-agent.prev { upgradeId, from,
                                     to, reason, by: watcher|guard, at }; the restored agent turns it into activities
    run.json                         AgentUpgradeRun of a mesh "Upgrade all" this site coordinates (§7.5)
    handled-requests.json            requestIds of cloud upgrade requests already taken (CloudView.agentUpgrades; once
                                     per requestId, also across restarts)
    download/                        resumable downloads per version (only verified bytes become opm-agent.new)
  backup/<version>-<UTC ts>/         §21 #91 copies of opm.db* taken while the agent is stopped: by the install script
                                     before every up- / down- / reinstall and by the agent before a schema migration;
                                     meta.env (version, schema, createdAt, reason); the 5 newest kept. A downgrade to an
                                     older schema restores one (§19.1)
```

### 2.5 Agent SQLite tables (one file, WAL; times INTEGER unix seconds unless "TEXT ts"; JSON = canonical JSON)

| table | columns | rules |
|---|---|---|
| `kv` | key TEXT PK, value JSON | keys: `head` {revId,seq,hash}, `ackSeq`, `pollCursor`, `lock` (UpdateLock), `keyHash` {hash,version}, `lastCloudOkAt`, `lastHeartbeatAt`, `clockOffsetSec`, `deleted` |
| `revisions` | revId PK, seq, parentRevId, origin, actor, createdAt TEXT, config JSON, hash, catalog JSON, catalogHash, summary, kind, rollbackOf JSON | linear chain; never deleted while a snapshot refers to it |
| `proposals` | revId PK, …all Proposal fields…, sites JSON, result JSON | at most one row with status approved/applying |
| `staging` | revId + siteSlug PK, staging, progress, error, detail JSON, updatedAt | this site's staging state (and the last known peer states) |
| `blobs` | sha256 PK, sizeBytes, kind (layer/config/manifest/file), source TEXT (cloud / peer:<slug> / local), haveSince, path | row exists only after the sha256 check passed |
| `snapshots` | snapshotId PK, createdAt, reason, revId, seq, manifest JSON (this site part + Snapshot head data), sizeBytes, createdBy, note, complete INTEGER | |
| `activities` | seq PK, ts TEXT, actor, origin, type, summary, data JSON, prevHash, hash | append-only; pipe uploads seq > kv.ackSeq |
| `actors` | email PK, lastUsedAt, count | merged from peers every 60 s (max lastUsedAt, max count) |
| `sessions` | sessionId PK, createdAt, expiresAt, keyVersion, ip, userAgent, loggedOutAt | rows older than 7 days deleted |
| `intents` | intentId PK, kind, revId, snapshotId, actor, sessionId, notBefore, expiresAt, usedAt | |
| `updates` | updateId PK, plan JSON, run JSON, startedAt, finishedAt | this site's view (coordinator: full run) |
| `peers` | siteSlug PK, publicKey, advertiseHost, agentPort, lastAnswerAt, rttMs, reachable, hasInternet, lastPing JSON | from cloud view + 10 s ping |
| `cloud_view` | id PK (=1), fetchedAt TEXT, source, cursor, view JSON | replaced only by a newer fetchedAt |
| `transfers` | transferId PK, …Transfer…, localPath | mirror for this site |
| `tunnel_leases` | leaseId PK, …TunnelLease… | one active lease per deployment (shared via ping) |
| `uptime_raw` | ts, subject ("site" or containerSlug), state (up/degraded/down), cpu, memMb, restarts — PK (ts, subject) | 30 s samples, 24 h |
| `uptime_hourly` | hour, subject, upFraction, downSeconds — PK (hour, subject) | 30 days |
| `debug_index` | day PK, file, sizeBytes, firstTs, lastTs, lines | |
| `login_limits` | ip PK, fails, windowStart | 5 failures / 5 min |
| `nonces` | key PK, expiresAt | peer replay cache (5 min) |
| `devtools_connections` | id PK (`c_<ULID>`), name, kind, def JSON (connection minus literal credentials), secret_json JSON (literal password / token), created_by, created_at, updated_at | §21 #97 manual dev-tool connections (presets come from the head config) |
| `devtools_saved` | id PK (`s_<ULID>`), kind, connection_id, collection, name, description, content, created_by, created_at, updated_at | §21 #97 saved queries / requests |
| `devtools_environments` | id PK (`e_<ULID>`), name, variables JSON (secret values blanked), secret_json JSON, created_by, updated_at | §21 #97 HTTP environments |

---------------------------------------------------------------------------------------------------------------------

## 3. Common API rules (cloud and agent)

- JSON bodies (`Content-Type: application/json; charset=utf-8`) unless the endpoint says raw. Unknown body fields → `invalid`
  (V_TYPE "unknown field x") for create/patch bodies of developer resources; extra fields in agent DTOs are ignored.
- Every error: `{ "error": { "code", "message", "details" } }` (types.ts `ApiErrorBody`), HTTP status from `ERROR_HTTP_STATUS`.
  `invalid` → `details.errors: ValidationIssue[]` (path, code, message, severity). `internal` → `details.requestId`.
  `conflict` on ifVersion/ifRevision → `details.current` (the current version). `rate-limited` → header `Retry-After`.
- Common errors of EVERY endpoint (not repeated in the tables): `unauthenticated`, `forbidden`, `rate-limited`, `internal`;
  human endpoints with a `{deploymentId}` also `not-found` when the deployment is deleted or invisible.
- Times: ISO 8601 UTC. Ids: Firestore auto ids (20 chars), ULIDs for revisions / snapshots / sessions / leases / requests / updates / intents.
- Lists: `{ items }`; paged lists `{ items, cursor }` (opaque cursor; `limit` default 100 unless stated).
- Masking: secret values and registry passwords are NEVER in normal reads: `value: "•••"` + `isSet` (types `SecretDefView`,
  `RegistryView`). Only the reveal endpoints return them. They never appear in logs, diffs, debug logs or activity bundles.
- Credential values (§21 #84): every credential stays viewable — no value is displayed only at creation. Every credential (access key, install command while active, secrets,
  registry passwords, the last admin-set account password, the last reset link) is stored and can be viewed again and
  regenerated any time by whoever holds the permission. Every value-returning op (`CREDENTIAL_OPS`: reveal, regenerate and
  create responses) returns `RevealResponse { value, hideAfterSec: 60 }` (`REVEAL_SECONDS`), is served with
  `Cache-Control: no-store`, writes an activity and / or audit entry, and is rate-limited to `REVEAL_RATE_PER_MINUTE` = 30
  per uid per fixed minute (Firestore counter `rateLimits/reveal_<uid>_<yyyymmddhhmm>`) → 429 `rate-limited`
  `details { retryAfterSec }` + `Retry-After`. The GUIs show values only through `RevealValue` (§17.0).
- Every response carries `X-Request-Id`. Agent-facing cloud responses also carry `X-OPM-Server-Time: <unix seconds>` (also on 401) so
  agents can correct clock skew (agent keeps `kv.clockOffsetSec` = serverTime − localTime when |offset| > 5 s and signs with local+offset).
- CORS: `/v1/agent/*` and `/v1/install/*` answer CORS for any origin (methods GET POST PUT PATCH DELETE OPTIONS; headers
  `Content-Type, X-OPM-Site, X-OPM-Ts, X-OPM-Nonce, X-OPM-Sig, Authorization, Range`; expose `X-OPM-Server-Time, X-OPM-Sha256,
  Content-Range`; no credentials). Human endpoints allow the frontend origin(s) from env `OPM_FRONTEND_ORIGINS` (comma list).
- Rate limits (cloud, per uid or per site, in-memory per instance, best effort): 20 req/s burst 100. Agent local API: login 5
  failures / 5 min / IP (429); `POST /_opm/api/actor` 30 / min / session; credential ops 30 / min / uid (cloud) and secret
  reveals 30 / min / session (agent).
- Request body limits: JSON ≤ 10 MiB (cloud), raw `POST /v1/blobs` ≤ 24 MiB, upload parts exactly 8 MiB (last part smaller),
  `activities/upload` ≤ 24 MiB. Agent: raw uploads up to 2 GiB (streamed to disk).

---------------------------------------------------------------------------------------------------------------------

## 4. Trust paths and authentication

### 4.1 Four trust paths

1. **People → cloud**: Firebase Auth ID token (`Authorization: Bearer <idToken>`).
2. **Install → cloud**: install token (`Authorization: Bearer opi_…`) only for `/v1/install/*` and `POST /v1/agent/enroll`.
3. **Agent → cloud, agent → agent**: Ed25519 signed requests with the site key.
4. **People → agent GUI**: access-key login (cookie `opm_session_<siteSlug>`, 6 h) + actor popup (actor token) — §4.6.

### 4.2 Install token + enroll

1. Setup page → "Create install command" (site of the draft; conflict if the site is enrolled). The API makes the token
   `opi_` + base64url(32 random bytes, no padding) (47 chars), stores `installTokens/{sha256hex(token)}` with
   `expiresAt = now + 24 h`, `usedAt = null`, `token` = the plaintext (stored while the token is active, §21 #84), and returns
   it inside the command (`CreateInstallTokenResponse`, `value` = the command, `hideAfterSec` 60):
   `curl -fsSL -H "Authorization: Bearer opi_…" <apiBase>/v1/install/script | sudo sh`
   **View** on the Setup page (`revealInstallCommand`, POST …/install-tokens/{tokenHash}/reveal) shows the command of an
   ACTIVE token again any time (activity `install-token.revealed` + audit `deployment.install-command-revealed`); used /
   revoked / expired / pre-#84 tokens → 410 `gone` details.reason `used | revoked | expired | not-stored`. **Regenerate**
   (`createInstallToken` with `replaceActive: true`) revokes the site's active tokens (`install-token.revoked` each) and
   makes a new one in one transaction. The token is still single use + 24 h for enrolling; revoke / expiry of an unused
   token null `token`. §21 #92: enroll KEEPS the plaintext — the used token is now the enrolled site's **Upgrade / reinstall
   command** (always viewable, see 8.).
2. Token check (script, binary, deps, release, enroll): exists, `revokedAt` null, `usedAt` null, `expiresAt > now` (else
   `gone`; unknown → `unauthenticated`), deployment not deleted. Upgrade mode (§21 #86 / #92, script / binary / deps /
   release only, never enroll, no expiry): an install token used for the CURRENT enrollment (`usedAt = agents/{site}.enrolledAt`,
   not revoked), or a `maintenance` token bound to it (`boundEnrolledAt = enrolledAt`, not revoked; else gone reason
   `revoked` / `enrollment-ended`). Enroll with a maintenance token → 403 `forbidden` details.reason `maintenance`.
3. `GET /v1/install/script` returns a POSIX sh script (template in §19.1) that: detects os/arch; downloads
   `/v1/install/binary?os&arch` with the token; checks `sha256` (header X-OPM-Sha256) and the Ed25519 signature (header
   X-OPM-Signature = base64url signature of the lowercase sha256 hex string by the release key; the release PUBLIC key is
   embedded in the script, from env `OPM_RELEASE_PUBLIC_KEY`); writes `/etc/opm/agent.json` `{ apiBase, deploymentId, siteSlug }`
   and `/etc/opm/install-token` (0600); installs the systemd unit; `systemctl enable --now opm-agent`.
4. First start: the agent creates `site.key` (if missing), calls `POST /v1/agent/enroll` (install token header) with
   `EnrollRequest`. The cloud, in one transaction: token unused (kind install) → `usedAt = now` (plaintext kept, §21 #92); `agents/{siteSlug}` must not exist (else
   `conflict`: revoke first); writes AgentDoc (publicKey, enrolledAt, versions, advertiseHost, detectedAddresses (`[]` when
   absent; non-loopback IPs, IPv4 first, then the hostname), agentPort, os, arch,
   keyVersion = current, ackSeq 0, lastSeenAt now); `agentEpoch+1`; activity `install.enrolled` (in the `_cloud` log).
   Response: `EnrollResponse { deploymentId, siteSlug, peers, accessKey { hash, version }, serverTime }`.
5. The agent stores peers + key hash, deletes the token file, writes its own first activity `install.enrolled` (seq 1, data
   `{ agentVersion, publicKeyFingerprint (first 16 hex of sha256(pub)), ip, hostname }`) and `agent.started`.
6. The site config is EMPTY (no head). Nothing runs until a person logs in to the agent GUI and approves the first proposal.
7. Re-install: Setup → "Revoke enrollment" (DELETE agents/{slug}) → new token → install again (new key). Revoked site
   signatures → `unauthenticated`.

CLI: `opm-agent enroll --config /etc/opm/agent.json --token-file /etc/opm/install-token` (also done automatically by `run`
when no key is enrolled), `opm-agent run --config …`, `opm-agent version`, `opm-agent keygen` (debug).

8. (§21 #92) **Upgrade / reinstall command** of an ENROLLED site: `revealUpgradeCommand` (POST
   …/agents/{siteSlug}/upgrade-command/reveal) shows `curl … | sudo sh` with the enrollment's token — the newest unrevoked
   maintenance token bound to the enrollment, else its own install token — for 60 s (activity `install-command.revealed`
   + audit `deployment.upgrade-command-revealed`); plaintext not stored (tokens used before #92) → 410 reason `not-stored`.
   `regenerateUpgradeCommand` (POST …/agents/{siteSlug}/upgrade-command) mints a `maintenance` token (`expiresAt` null,
   `boundEnrolledAt` = agent.enrolledAt) and revokes the previous command (the enrollment's install token and older
   maintenance tokens: `revokedAt`, `token` null) in one transaction (`install-command.regenerated { siteSlug, tokenHash,
   revoked }` + audit `deployment.upgrade-command-regenerated`). `revokeAgent` revokes the enrollment's maintenance
   tokens and nulls the plaintext of its install token. On the server the command runs the upgrade-only flow of §19.1 /
   §21 #91 (never writes the token, refuses another server).

### 4.3 Site keys

Ed25519. Private: 32-byte seed in `<agentHome>/site.key` (base64 std). Public key on the wire: raw 32 bytes, base64 std (padded)
(`PeerInfo.publicKey`, `EnrollRequest.publicKey`). Fingerprint = first 16 hex chars of sha256(raw public key).

### 4.4 Signed requests (agent → cloud and agent → agent) — exact

Headers:

| header | value |
|---|---|
| `X-OPM-Site` | `<deploymentId>/<siteSlug>` |
| `X-OPM-Ts` | unix seconds, decimal (agent local clock + kv.clockOffsetSec) |
| `X-OPM-Nonce` | 16 random bytes, base64url without padding (22 chars) |
| `X-OPM-Sig` | base64url (no padding) Ed25519 signature of the string to sign |

String to sign (UTF-8, `\n` = LF, no trailing newline) — `canonical.ts signedRequestString`:
```
METHOD\nPATH\nTS\nNONCE\nBODYHASH
```
- METHOD upper-case. PATH = the path starting at `/v1/` (cloud) or `/_opm/peer/` (peer), INCLUDING `?query` exactly as sent
  (no re-encoding, no sorting). BODYHASH = lowercase hex sha256 of the raw body bytes (empty body = `e3b0c442…b855`).
- Verify: site enrolled (cloud: `agents/{slug}` exists, deployment not deleted; peer: the site is in the peer table with that
  public key and the same deploymentId); `|now − TS| ≤ 60 s`; nonce not seen for this site in the last 5 min (cloud:
  `agentNonces` create(), peer: SQLite `nonces`); signature valid. Any failure → 401 `unauthenticated` (message says which;
  details `{ reason: 'clock'|'nonce'|'signature'|'unknown-site' }`).
- Vectors: `shared/testdata/canonical-vectors.json` (`signing`).
- The signer must be the site the body talks about: e.g. heartbeat `siteSlug` must equal the signer (relay: the inner signer).

Peer JSON responses are signed too (so a LAN attacker cannot forge a cloud view): headers `X-OPM-Site` (responder),
`X-OPM-Ts`, `X-OPM-Sig` over `RESPONSE\nSTATUS\nREQUEST_NONCE\nTS\nBODYHASH` (`signedResponseString`). The caller verifies it
with the responder's public key and the nonce it sent; failure = the peer is treated as unreachable for that call. Raw blob /
snapshot-part / site-mirror responses are not signed (content is sha256-checked or display-only).

### 4.5 Relay

A site without internet sends its already-signed cloud request to an internet peer: `POST /_opm/peer/relay { request }`.
The peer calls `POST /v1/agent/relay { forSite, request }` signed with ITS key. The cloud verifies the outer signature
(relaying site) and the inner signature (forSite: method, path, headers, sha256 of the decoded body), then runs the inner
handler as forSite and returns `{ status, headers, bodyB64 }`; sets `agents/{forSite}.relayVia = <relaying slug>` (cleared
when forSite calls directly). Inner nonce/clock rules apply. The relay never changes the inner request. Relay peer choice:
the first reachable peer in slug order whose ping says `internet: true`. Responses ≤ 4 MiB through relay; larger downloads use
smart distribution (§9.7), not the relay.

### 4.6 Agent GUI: login + actor

**Access key** (deployment-wide "login cred (common)"):
- Format: 24 chars of the RFC 4648 base32 alphabet `A–Z2–7` (120 bits, crypto random), shown grouped `XXXX-XXXX-XXXX-XXXX-XXXX-XXXX`.
  Input normalisation: upper-case, remove everything outside `A–Z2–7`; must then be 24 chars.
- Hash: argon2id, m = 65536 KiB, t = 3, p = 1, 16-byte random salt, 32-byte output, PHC string
  `$argon2id$v=19$m=65536,t=3,p=1$<b64 salt>$<b64 hash>` (standard base64 WITHOUT padding). Input = the normalised 24 chars.
  The cloud stores the plaintext `accessKey` (grouped, plain like secrets, §21 #84) next to `accessKeyHash` +
  `accessKeyVersion`; agents get ONLY `{ hash, version }` — the plaintext leaves the cloud only through createDeployment,
  rotateAccessKey and revealAccessKey. Setup page: **View** (`revealAccessKey`, permission deployment.setup.write — the key
  is full agent GUI access; activity `access-key.revealed` + audit `deployment.access-key-revealed`; shown 60 s) and
  **Regenerate…** (= rotate, typed ACCEPT). Pre-#84 deployments (`accessKey: null`) → reveal answers 410 `gone`
  reason `not-stored`; the page shows "not stored — regenerate to view".
- Agents get `{ hash, version }` at enroll and in every cloud view (latest fetch wins) → stored in `kv.keyHash`. A site adopts
  a hash only when `version` > its current version (activity `access-key.received { version }`).
- Rotate / Regenerate (`POST /v1/deployments/{id}/access-key/rotate`): new key (stored plaintext) + hash, version+1,
  rotatedAt/By, agentEpoch+1, `_cloud` activity `access-key.rotated { version }` + audit `deployment.access-key-rotated`;
  the response carries the new key (`value`, 60 s). Each site accepts the old key until it receives the new hash. Open sessions stay
  valid until their 6 h end.

**Login** `POST /_opm/api/login { accessKey }`:
- No `kv.keyHash` yet → 503 `unavailable` ("this site has no access key yet; it gets it from the cloud or a peer").
- Rate limit: per client IP, 5 failures within 5 minutes → 429 `rate-limited` (Retry-After = seconds until the window ends) —
  checked BEFORE the argon2 check.
- Wrong key → 401 `invalid-key` + activity `login.failed { siteSlug, ip, userAgent }` (actor null).
- OK → session row (sessionId ULID, expiresAt = now + 6 h, keyVersion, ip, userAgent); `Set-Cookie: opm_session_<siteSlug>=<sessionId>;
  Path=/_opm; HttpOnly; SameSite=Strict; Max-Age=21600` (+ `Secure` when the request is HTTPS). The name carries the site
  slug because browsers ignore ports: two agents on one host (e2e: 8090 / 8091) must not overwrite each other's session.
  Transition: the agent also sets the legacy `opm_session` with the same value and accepts it when `opm_session_<siteSlug>` is
  absent; `opm_session_<siteSlug>` always wins (§21 item 31); activity `login.succeeded
  { sessionId, siteSlug, keyVersion, ip, userAgent }`.
- Session check on every `session` / `session+actor` endpoint: cookie present and row exists and `loggedOutAt` null →
  else 401 `unauthenticated`; `expiresAt ≤ now` → 401 `session-expired`. No sliding renewal. Sessions are per site (cookie name); a session from site A is not valid on site B (log in there too).
- Logout: `loggedOutAt = now`, clear both cookies (`Max-Age=0`), activity `logout { sessionId, siteSlug, actor? }`.

**Actor popup** ("Who is working right now?") — after login, on EVERY page load / refresh / new tab:
- `POST /_opm/api/actor { email }`: email trimmed, lower-cased, must match `^[^@\s]+@[^@\s]+\.[^@\s]+$` and ≤ 254 chars.
- Returns `actorToken` = JWT (header `{"alg":"EdDSA","typ":"JWT"}`, base64url parts, Ed25519 site key) with claims
  `ActorTokenClaims { email, sid, dep, site, iat, exp }`, `exp` = the session expiry. Kept in page memory only.
- Writes `actor.declared { email, sessionId, siteSlug, userAgent, ip }` (actor = email) and upserts `actors` (lastUsedAt now, count+1).
- Every `session+actor` endpoint additionally needs `Authorization: Actor <token>`: signature valid (this site's key), `sid`
  = the cookie session, `exp` > now, `dep`/`site` = this agent → else 401 `actor-required`. The email becomes the `actor` of
  every activity the call writes; activity data also gets `sessionId`.
- Suggestions: browser localStorage `opm.recentActors` (max 10, newest first) first, then `GET /_opm/api/actors/recent`.
- Header chip: email · "session ends in 4 h 12 m" · Change person (popup again, same session) · Log out.
- Forwarding: when the GUI on site A acts on site B (§7.2, §8), A forwards over the mesh with headers `X-OPM-Actor: <email>` and
  `X-OPM-Session: <sessionId>@<A>`; B trusts them because the request is peer-signed.

Risk (accepted, prototype): anyone with the access key can act; mitigations: GUI on the admin network, 60 s wait + typed
ACCEPT, every action logged with email + sessionId + IP + user agent; rotate when a person leaves.

---------------------------------------------------------------------------------------------------------------------

## 5. Cloud access control

### 5.1 Human login

- `/login`: Firebase Auth email + password (Google SSO only when env `VITE_OPM_GOOGLE_DOMAIN` is set). No signup anywhere.
- API: verify the ID token (Admin SDK `verifyIdToken(token, checkRevoked=true)`), load `accounts/{uid}`; missing account
  → 403 `forbidden` ("no account"); disabled → 403. `GET /v1/me` sets `lastLoginAt` (at most once per hour).
- Bootstrap: `backend/scripts/create-admin.ts --email --password --name` (Admin SDK; works against the emulator) creates the
  Firebase user and `accounts/{uid}` with `access = fullAdminRules()` (access.ts) and `createdBy = "bootstrap"`.
- `POST /v1/accounts` creates the Firebase user with a generated 20-char alnum password (Admin SDK), the account doc with
  `lastSetPassword` (stored, §2.2), and returns `CreateAccountResponse { account, value = the password, hideAfterSec 60 }`
  (audit `account.created` with `data.passwordSet = true` + `account.password-set`). No set-password link after creation
  (§21 #84). Firebase Auth cannot return a password, so OPM stores the one it set:
  - **Password** (account page): RevealValue — **View** (`revealAccountPassword`, audit `account.password-revealed`; 410
    `gone` when none is stored or the person has changed it since) + **Set new password…** (typed ACCEPT;
    `setAccountPassword` generates and sets a new 20-char password, stores it, audit `account.password-set`; the person's
    sessions end). After every set the backend stores `validAfter` = the Auth user's `tokensValidAfterTime`; a later value
    clears the stored password and link (it is not their current password any more).
  - **Reset link…**: `resetAccountPassword` makes a new password-reset link any time (`generatePasswordResetLink`, 1 h),
    stores it as `lastResetLink` and returns it (`ResetLinkResponse`, value = the link; audit `account.reset-link-created`);
    `revealAccountResetLink` shows it again (audit `account.reset-link-revealed`; 410 `gone` when none / expired / used). If
    SMTP env (`OPM_SMTP_URL`) is set, the link is also e-mailed; the emulator just returns it.

### 5.2 Access rule evaluation (exact — `access.ts can()`)

```
can(account, resource, action, target?):
  account missing or disabled                      → deny
  action = read AND resource ∈ {services, images, notFoundPages, tags}   → allow   (catalog read for every account)
  target AND account.uid ∈ target.managerAccountUids:
      resource starts with "deployment."           → allow (every action, incl. publish)
      resource = deployments AND action = read     → allow
  for each rule in account.access (in order):
      rule.resource = resource AND action ∈ rule.actions:
          developer resource (registries … accounts): allow only if rule.scope.kind = all
          else: scope all → allow; tags → allow if target.tagSlugs ∩ scope.tagSlugs ≠ ∅;
                deployments → allow if target.deploymentId ∈ scope.deploymentIds   (no target → only scope all)
  → deny (403 forbidden)
```
Valid actions: developer resources read/write (accounts also admin); deployments read/write; deployment.config
read/write/publish; other deployment.* read/write. `validateAccessRules` enforces this + existence of tags/deployments.
- List deployments: only those where `canSeeDeployment` (deployments.read or any deployment.* read on it).
- Create deployment: `deployments.write` with scope all, or with a tags scope that contains at least one of the new `tagSlugs`.
- Account endpoints: GET own account always allowed; PATCH own name/phone/img always allowed; access changes need accounts.admin.
- Blobs: upload allowed with templates.write OR deployment.config.write on any deployment; download with templates.read OR
  deployment.config.read on any deployment.
- Frontend: hides every action the account cannot do (same `can()`), and the API checks again.

### 5.3 Permission per frontend route

| route | needs |
|---|---|
| /app/registries | registries.read (create/edit/test/reveal/delete: registries.write) |
| /app/services… | services.read (implicit) (create/edit/delete: services.write; images: images.write) |
| /app/not-found-pages… | notFoundPages.read (create/delete: notFoundPages.write) |
| /app/templates… | templates.read (edit: templates.write) |
| /app/tags | tags.read (edit: tags.write) |
| /app/accounts… | accounts.read (create, access, disable, password view / set, reset link: accounts.admin) |
| /app/audit-log | accounts.admin |
| /app/github | accounts.admin |
| /app/deployments | deployments.read per item (create: deployments.write) |
| …/{id} home | canSeeDeployment (edit: deployments.write) |
| …/setup | deployment.setup.read (actions incl. View access key / install command: deployment.setup.write) |
| …/status | deployments.read or any deployment.* read |
| …/activities, /upload | deployment.activities.read / .write |
| …/variables, /files, /sites, /sites/{slug}… | deployment.config.read (save: deployment.config.write) |
| …/secrets | deployment.config.read to see names; set / Regenerate: deployment.secrets.write; View: deployment.secrets.read |
| …/sites/{slug}/transfers (Send column: write; Receive column + lists: read; old …/send, …/receive redirect, §21 #100) | deployment.transfers.write / .read |
| …/diff | deployment.config.read (publish: deployment.config.publish; copy head: deployment.config.write) |

### 5.4 Access key generation (cloud)

`crypto.randomBytes(15)` → base32 encode (no padding) → 24 chars. Hash with argon2id (§4.6) — backend uses the npm package
`argon2` (native) or `hash-wasm` (wasm); both produce the PHC string; the Go agent verifies with `golang.org/x/crypto/argon2`
(parse PHC, recompute, constant-time compare).

---------------------------------------------------------------------------------------------------------------------

## 6. Cloud API (`/v1/*`)

The table is generated from `shared/src/cloud-api.ts` (the router registers exactly these routes). Request / response type
names refer to `types.ts`. Status 200 unless shown. "errors" lists codes besides the common ones (§3).

| op | method | path | auth | permission | request body | response | errors (+ common) | activities | behaviour |
|---|---|---|---|---|---|---|---|---|---|
| `getMe` | GET | `/v1/me` | human | — | — | `MeResponse`  | — | — | The caller account (creates nothing; updates lastLoginAt at most once per hour). |
| `listRegistries` | GET | `/v1/registries` | human | registries.read | — | `ListResponse<RegistryView>`  | — | — | List registries; password masked. |
| `createRegistry` | POST | `/v1/registries` | human | registries.write | `CreateRegistryRequest` | `RegistryView` (201) | invalid | audit:registry.created | Create a registry. |
| `getRegistry` | GET | `/v1/registries/{registryId}` | human | registries.read | — | `RegistryView`  | not-found | — | Get one registry. |
| `patchRegistry` | PATCH | `/v1/registries/{registryId}` | human | registries.write | `PatchRegistryRequest` | `RegistryView`  | not-found, invalid | audit:registry.updated | Change label / url / username / password; lastCheck is reset to null when url, username or password change. |
| `deleteRegistry` | DELETE | `/v1/registries/{registryId}` | human | registries.write | — | `Deleted`  | not-found, conflict | audit:registry.deleted | Delete a registry that no image uses (conflict details.usedBy). |
| `revealRegistryPassword` | POST | `/v1/registries/{registryId}/reveal-password` | human | registries.write | — | `RevealPasswordResponse`  | not-found, rate-limited | audit:registry.password-revealed | Return the password (RevealValue, 60 s; any number of times; audited registry.password-revealed; no-store; 30/min per uid). |
| `testRegistry` | POST | `/v1/registries/{registryId}/test` | human | registries.write | — | `RegistryTestResponse`  | not-found | audit:registry.tested | Try a token login (GET <url>/v2/) and store lastCheck. Optional JSON body { repository } (raw call; the typed client sends none): also checks pull access to that repository (Docker Hub JWT access claim). |
| `listRegistryRepositories` | GET | `/v1/registries/{registryId}/repositories` · query `{ q?: string }` | human | images.write | — | `RegistryRepositoriesResponse`  | not-found, unavailable | — | Repositories of the registry for the register-image picker (Docker Hub: the account namespace + search q; Artifact Registry: the API with the _json_key credentials; other registries: Docker v2 _catalog). supported=false when the registry cannot list (free text). Not audited (GET). |
| `listRegistryTags` | GET | `/v1/registries/{registryId}/tags` · query `{ repository: string; q?: string }` | human | images.write | — | `RegistryTagsResponse`  | not-found, invalid, unavailable | — | Tags of one repository with pushed dates where the registry tells them (Docker Hub API, Artifact Registry API, Docker v2 tags/list). Not audited (GET). |
| `listIcons` | GET | `/v1/icons` | human | — | — | `ListResponse<IconListItem>`  | — | — | Every uploaded icon (newest first) with usedBy. |
| `uploadIcon` | POST | `/v1/icons` · query `{ name?: string }` | human | any write permission | raw bytes | `UploadIconResponse` (201) | invalid, too-large | audit:icon.uploaded | Upload an icon (raw body ≤ 64 KiB, Content-Type image/png \| image/svg+xml \| image/webp). SVG is sanitized (scripts, on* handlers, external references removed). Same bytes (sha256) → the existing icon, existing=true. |
| `deleteIcon` | DELETE | `/v1/icons/{iconId}` | human | any write permission | — | `Deleted`  | not-found, conflict | audit:icon.deleted | Delete an unused icon; 409 conflict with details.users (IconUser[]) when a service / template / tag / deployment / account uses it. |
| `getIconContent` | GET | `/v1/icons/{iconId}/content` | public | — | — | bytes | not-found | — | The icon bytes (no auth, so <img> works; Cache-Control: public, max-age=31536000, immutable; X-Content-Type-Options: nosniff; SVG with a restrictive CSP). |
| `listServices` | GET | `/v1/services` | human | services.read | — | `ListResponse<ServiceListItem>`  | — | — | List services with imageCount and usedBy (deployments). |
| `createService` | POST | `/v1/services` | human | services.write | `CreateServiceRequest` | `Service` (201) | invalid, conflict | audit:service.created | Create a service; slug unique + immutable. |
| `getService` | GET | `/v1/services/{serviceSlug}` | human | services.read | — | `Service`  | not-found | — | Get one service. |
| `patchService` | PATCH | `/v1/services/{serviceSlug}` | human | services.write | `PatchServiceRequest` | `Service`  | not-found, invalid, conflict | audit:service.updated | Change label / icon / description (serviceSlug in body → invalid: immutable). |
| `deleteService` | DELETE | `/v1/services/{serviceSlug}` | human | services.write | — | `Deleted`  | not-found, conflict | audit:service.deleted | Delete a service with no images. |
| `serviceUsage` | GET | `/v1/services/{serviceSlug}/used` | human | services.read | — | `ListResponse<UsageItem>`  | not-found | — | Templates and deployments (draft or head) that use any tag of the service. |
| `listImages` | GET | `/v1/services/{serviceSlug}/images` | human | images.read | — | `ListResponse<Image>`  | not-found | — | List the images of a service, newest first. |
| `inspectImage` | POST | `/v1/services/{serviceSlug}/images/inspect` | human | images.write | `InspectImageRequest` | `InspectImageResponse`  | not-found, invalid, unavailable | audit:image.inspected | Read manifest + config of a docker reference from the registry to prefill the register form (stores nothing). |
| `registerImage` | POST | `/v1/services/{serviceSlug}/images` | human | images.write | `RegisterImageRequest` | `Image` (201) | not-found, invalid, conflict | audit:image.registered | Register a version (doc id = version; exists → 409 conflict details.reason version-exists) from a full docker reference (registry auto-matched by host unless registryId is given; null = public), import.status pending, start the import job. |
| `getImage` | GET | `/v1/services/{serviceSlug}/images/{version}` | human | images.read | — | `Image`  | not-found | — | Get one image. |
| `patchImage` | PATCH | `/v1/services/{serviceSlug}/images/{version}` | human | images.write | `PatchImageRequest` | `Image`  | not-found, invalid, conflict | audit:image.updated (+ template.updated per rewritten template, draft.updated activities) | Edit everything but the identity (kind, runtime, ports / env / volumes incl. renames, health check, stop timeout, description); identity → invalid V_IMMUTABLE; reference + registryId only while the import failed (else 409); in-use rename / removal → 409 `in-use`; `renameEverywhere` rewrites renamed names in templates + drafts (§6.1). |
| `deleteImage` | DELETE | `/v1/services/{serviceSlug}/images/{version}` | human | images.write | — | `Deleted`  | not-found, conflict | audit:image.deleted | Delete an image no template / deployment (draft, head, open proposal) uses. |
| `retryImport` | POST | `/v1/services/{serviceSlug}/images/{version}/retry-import` | human | images.write | — | `Image`  | not-found, conflict | audit:image.import-retried | Set a failed import back to pending (conflict unless status = failed). |
| `imageUsage` | GET | `/v1/services/{serviceSlug}/images/{version}/used` | human | images.read | — | `ListResponse<UsageItem>`  | not-found | — | Templates and deployments that use this image version. |
| `listNotFoundPages` | GET | `/v1/not-found-pages` | human | notFoundPages.read | — | `ListResponse<NotFoundPageListItem>`  | — | — | List pages without htmlCode. |
| `createNotFoundPage` | POST | `/v1/not-found-pages` | human | notFoundPages.write | `CreateNotFoundPageRequest` | `NotFoundPage` (201) | invalid, too-large | audit:not-found-page.created | Create a page; there is no update (PATCH / PUT → 405 method-not-allowed). |
| `getNotFoundPage` | GET | `/v1/not-found-pages/{notFoundPageId}` | human | notFoundPages.read | — | `NotFoundPageDetail`  | not-found | — | Get one page with htmlCode and usage. |
| `deleteNotFoundPage` | DELETE | `/v1/not-found-pages/{notFoundPageId}` | human | notFoundPages.write | — | `Deleted`  | not-found, conflict | audit:not-found-page.deleted | Delete an unused page. |
| `listTemplates` | GET | `/v1/templates` | human | templates.read | — | `ListResponse<TemplateSummary>`  | — | — | List template summaries. |
| `createTemplate` | POST | `/v1/templates` | human | templates.write | `CreateTemplateRequest` | `Template` (201) | invalid | audit:template.created | Create an empty template (version 1). |
| `getTemplate` | GET | `/v1/templates/{templateId}` | human | templates.read | — | `Template`  | not-found | — | Get the full template. |
| `patchTemplate` | PATCH | `/v1/templates/{templateId}` | human | templates.write | `PatchTemplateRequest` | `TemplateWriteResponse`  | not-found, conflict, invalid | audit:template.updated | Replace parts; draft-level validation errors → invalid (nothing saved); publish-level issues are returned in validation. version +1. |
| `deleteTemplate` | DELETE | `/v1/templates/{templateId}` | human | templates.write | — | `Deleted`  | not-found | audit:template.deleted | Delete a template (deployments keep their own copy). |
| `uploadBlob` | POST | `/v1/blobs` | human | templates.write or deployment.config.write (any deployment) | raw bytes | `BlobRef` (201) | too-large, invalid | audit:blob.uploaded | Upload file content (raw body ≤ 24 MiB; optional header X-OPM-Sha256 is verified). Content-addressed. |
| `getBlob` | GET | `/v1/blobs/{sha256}` | human | templates.read or deployment.config.read (any deployment) | — | bytes (200, Range) or 302  | not-found | — | Download file content: bodies ≤ 8 MiB (BLOB_STREAM_MAX) stream with 200 (Content-Type, ETag = sha256, private cache); larger ones 302 to a 5-min signed URL (bucket CORS: backend/storage.cors.json). |
| `createUpload` | POST | `/v1/uploads` | human | templates.write or deployment.config.write | `CreateUploadRequest` | `Upload` (201) | invalid, too-large | audit:upload.created | Start a multi-part blob upload (> 24 MiB, ≤ 50 MiB for blobs). |
| `getUpload` | GET | `/v1/uploads/{uploadId}` | human | creator of the upload | — | `Upload`  | not-found, gone | — | Upload state (to resume). |
| `putUploadPart` | PUT | `/v1/uploads/{uploadId}/parts/{index}` | human | creator of the upload | raw bytes | `UploadPartResponse`  | not-found, gone, invalid | — | Upload part index (0-based, exactly partSize bytes except the last). Idempotent. |
| `completeUpload` | POST | `/v1/uploads/{uploadId}/complete` | human | creator of the upload | — | `CompleteUploadResponse`  | not-found, gone, invalid | transfer.sent, audit:upload.completed | Join the parts, check size + sha256 (mismatch → invalid, upload failed). |
| `listTags` | GET | `/v1/tags` | human | tags.read | — | `ListResponse<TagListItem>`  | — | — | List tags with deployment counts. |
| `createTag` | POST | `/v1/tags` | human | tags.write | `CreateTagRequest` | `Tag` (201) | invalid, conflict | audit:tag.created | Create a tag. |
| `patchTag` | PATCH | `/v1/tags/{tagSlug}` | human | tags.write | `PatchTagRequest` | `Tag`  | not-found, invalid | audit:tag.updated | Change label / icon / color. |
| `deleteTag` | DELETE | `/v1/tags/{tagSlug}` | human | tags.write | — | `DeleteTagResponse`  | not-found | audit:tag.deleted | Delete and remove from every deployment and access scope (batch). |
| `listAccounts` | GET | `/v1/accounts` | human | accounts.read | — | `ListResponse<Account>`  | — | — | List accounts. |
| `createAccount` | POST | `/v1/accounts` | human | accounts.admin | `CreateAccountRequest` | `CreateAccountResponse` (201) | invalid, conflict, rate-limited | audit:account.created, audit:account.password-set | Create the Firebase Auth user (generated 20-char password) + account; value = the password, stored as lastSetPassword (revealAccountPassword until the person changes it). §21 #84. |
| `getAccount` | GET | `/v1/accounts/{uid}` | human | accounts.read (own account always) | — | `Account`  | not-found | — | Get one account. |
| `patchAccount` | PATCH | `/v1/accounts/{uid}` | human | accounts.write (own name/phone/img: always; access: accounts.admin) | `PatchAccountRequest` | `Account`  | not-found, invalid, conflict | audit:account.updated, audit:account.access-changed | Change profile or access (removing the last admin → conflict). |
| `disableAccount` | POST | `/v1/accounts/{uid}/disable` | human | accounts.admin | `DisableAccountRequest` | `Account`  | not-found, conflict | audit:account.disabled, audit:account.enabled | Disable / enable; disabling revokes refresh tokens (yourself or the last admin → conflict). |
| `resetAccountPassword` | POST | `/v1/accounts/{uid}/password-reset` | human | accounts.admin | — | `ResetLinkResponse`  | not-found, rate-limited | audit:account.reset-link-created | Make a new password-reset link (any time; replaces the stored one, 1 h); value = the link (stored as lastResetLink). |
| `revealAccountResetLink` | POST | `/v1/accounts/{uid}/password-reset/reveal` | human | accounts.admin | — | `ResetLinkResponse`  | not-found, gone, rate-limited | audit:account.reset-link-revealed | Show the stored reset link again (gone when none / expired / used). |
| `setAccountPassword` | POST | `/v1/accounts/{uid}/password` | human | accounts.admin | — | `SetAccountPasswordResponse`  | not-found, rate-limited | audit:account.password-set | "Set new password…": generate a strong password, set it with the Admin SDK, store it as lastSetPassword; value = the password. |
| `revealAccountPassword` | POST | `/v1/accounts/{uid}/password/reveal` | human | accounts.admin | — | `RevealAccountPasswordResponse`  | not-found, gone, rate-limited | audit:account.password-revealed | Show the last password an admin set (gone when none, or the person changed their password since: Auth tokensValidAfterTime moved → cleared). |
| `listAuditLog` | GET | `/v1/audit-log` · query `AuditLogQuery` | human | accounts.admin | — | `PageResponse<AuditLogEntry>`  | — | — | Root-scope audit entries, newest first; filters action (exact or prefix ending in "."), actor (uid / "system" / e-mail), targetType, target, outcome, from, to. |
| `listDeployments` | GET | `/v1/deployments` · query `{ tag?: string; cursor?: string; limit?: number }` | human | deployments.read (filtered per deployment) | — | `PageResponse<DeploymentSummary>`  | — | — | Deployments the caller can read. |
| `createDeployment` | POST | `/v1/deployments` | human | deployments.write (scope all or a tag scope that covers tagSlugs) | `CreateDeploymentRequest` | `CreateDeploymentResponse` (201) | invalid, not-found, rate-limited | deployment.created, audit:deployment.created | Create (optionally from a template); generates secrets with generate rules and the access key (stored plaintext + argon2id hash; revealAccessKey shows it again). |
| `getDeployment` | GET | `/v1/deployments/{deploymentId}` | human | deployments.read | — | `DeploymentView`  | not-found | — | Get one deployment (secrets masked). |
| `patchDeployment` | PATCH | `/v1/deployments/{deploymentId}` | human | deployments.write | `PatchDeploymentRequest` | `DeploymentView`  | not-found, invalid | deployment.updated, audit:deployment.updated | Change label / image / tags / managers. |
| `deleteDeployment` | DELETE | `/v1/deployments/{deploymentId}` | human | deployments.write | `DeleteDeploymentRequest` | `Deleted`  | not-found, invalid | deployment.deleted, audit:deployment.deleted | Soft delete (deleted = true); agents see deleted in the cloud view; keys and tokens stop working. |
| `putDraftSection` | PUT | `/v1/deployments/{deploymentId}/draft/{section}` | human | deployment.config.write (secrets: deployment.secrets.write) | `PutDraftSectionRequest` | `DraftWriteResponse`  | not-found, conflict, invalid | draft.updated | Replace one section (variables \| secrets \| files \| sites); ifRevision must equal draftVersion. |
| `putDraftSite` | PUT | `/v1/deployments/{deploymentId}/draft/sites/{siteSlug}` | human | deployment.config.write | `PutDraftSiteRequest` | `DraftWriteResponse`  | not-found, conflict, invalid | draft.updated | Replace one existing site (site.siteSlug must equal the path). |
| `revealSecret` | POST | `/v1/deployments/{deploymentId}/secrets/{secretSlug}/reveal` | human | deployment.secrets.read | — | `RevealSecretResponse`  | not-found, rate-limited | secret.revealed, audit:deployment.secret-revealed | Return one draft secret value (60 s); writes secret.revealed (where cloud) + audit; no-store; 30/min per uid. |
| `regenerateSecret` | POST | `/v1/deployments/{deploymentId}/secrets/{secretSlug}/regenerate` | human | deployment.secrets.write | `RegenerateSecretRequest` | `RegenerateSecretResponse`  | not-found, conflict, invalid, rate-limited | draft.updated, secret.regenerated, audit:deployment.secret-regenerated | Regenerate one draft secret with its generate rule (invalid V_SECRET_NO_GENERATE without one); draftVersion +1; value = the new secret. |
| `getDiff` | GET | `/v1/deployments/{deploymentId}/diff` | human | deployment.config.read | — | `DiffResponse`  | not-found | — | Draft vs reported head + publish blockers. |
| `publish` | POST | `/v1/deployments/{deploymentId}/publish` | human | deployment.config.publish | `PublishRequest` | `PublishResponse` (201) | not-found, conflict, invalid, stale | proposal.published, proposal.superseded | Make revision (origin cloud, parent = head) + proposal (published); supersede older open proposals. |
| `resetDraftToHead` | POST | `/v1/deployments/{deploymentId}/draft/reset-to-head` | human | deployment.config.write | `ResetToHeadRequest` | `{ draftVersion: number }`  | not-found, invalid | draft.reset-to-head | Copy the head config into the draft (no head → not-found). |
| `getTemplateSyncStatus` | GET | `/v1/deployments/{deploymentId}/template-sync/status` | human | deployment.config.read | — | `TemplateSyncStatus` | not-found | — | §21 #94 link + currentVersion / newerVersions / pendingChanges / conflicts / templateDeleted (badge, Overview banner). |
| `getTemplateSyncPlan` | GET | `/v1/deployments/{deploymentId}/template-sync?templateId=` | human | deployment.config.read + templates.read | — | `TemplateSyncPlan` | not-found, invalid, forbidden | — | 3-way plan base ↔ template now ↔ draft (masked). templateId = another template (relink / link). |
| `previewTemplateSync` | POST | `/v1/deployments/{deploymentId}/template-sync/preview` | human | deployment.config.read + templates.read | `TemplateSyncApplyRequest` | `TemplateSyncPreviewResponse` | not-found, conflict, invalid | — (dry run, not audited: AUDIT_EXEMPT_OPS) | Resulting draft + changes + validateConfig(draft) + unresolved + missing values. |
| `applyTemplateSync` | POST | `/v1/deployments/{deploymentId}/template-sync/apply` | human | deployment.config.write + templates.read (+ deployment.secrets.write for secret items) | `TemplateSyncApplyRequest` | `TemplateSyncApplyResponse` | not-found, conflict, invalid, forbidden | draft.template-synced, audit:deployment.template-synced | Write the chosen items; base := the template now; not-taken template changes → skipped. Never publishes. |
| `unlinkTemplate` | POST | `/v1/deployments/{deploymentId}/template-sync/unlink` | human | deployment.config.write | `UnlinkTemplateRequest` | `{ link: null }` | not-found, invalid | deployment.template-unlinked, audit:deployment.template-unlinked | srcTemplate = null; the draft stays as it is. |
| `listProposals` | GET | `/v1/deployments/{deploymentId}/proposals` · query `{ limit?: number }` | human | deployment.config.read | — | `ListResponse<Proposal>`  | not-found | — | Proposals, newest first (default 20). |
| `listRevisions` | GET | `/v1/deployments/{deploymentId}/revisions` · query `{ cursor?: string; limit?: number }` | human | deployment.config.read | — | `PageResponse<RevisionSummary>`  | not-found | — | Revisions, newest seq first. |
| `getRevision` | GET | `/v1/deployments/{deploymentId}/revisions/{revId}` | human | deployment.config.read | — | `RevisionView`  | not-found | — | One revision (secrets masked). |
| `getSetup` | GET | `/v1/deployments/{deploymentId}/setup` | human | deployment.setup.read | — | `SetupResponse`  | not-found | — | Access key version, per-site enrollment and install tokens. |
| `rotateAccessKey` | POST | `/v1/deployments/{deploymentId}/access-key/rotate` | human | deployment.setup.write | — | `RotateAccessKeyResponse`  | not-found, rate-limited | access-key.rotated, audit:deployment.access-key-rotated | Regenerate: new access key (stored plaintext + hash; value = the key); accessKeyVersion +1; agentEpoch +1. |
| `revealAccessKey` | POST | `/v1/deployments/{deploymentId}/access-key/reveal` | human | deployment.setup.write | — | `RevealAccessKeyResponse`  | not-found, gone, rate-limited | access-key.revealed, audit:deployment.access-key-revealed | Show the current access key (60 s); gone details.reason "not-stored" for deployments created before §21 #84 (Regenerate to view). |
| `createInstallToken` | POST | `/v1/deployments/{deploymentId}/install-tokens` | human | deployment.setup.write | `CreateInstallTokenRequest` | `CreateInstallTokenResponse` (201) | not-found, conflict, rate-limited | install-token.created, install-token.revoked, audit:deployment.install-command-created | Install command for one site of the draft (not-found when the site is not in the draft; conflict when the site is enrolled); replaceActive = Regenerate (revokes the site's active tokens first). Token stored while active. |
| `revealInstallCommand` | POST | `/v1/deployments/{deploymentId}/install-tokens/{tokenHash}/reveal` | human | deployment.setup.write | — | `RevealInstallCommandResponse`  | not-found, gone, rate-limited | install-token.revealed, audit:deployment.install-command-revealed | Show the install command of an ACTIVE token again; gone details.reason used \| revoked \| expired \| not-stored. |
| `revealUpgradeCommand` | POST | `/v1/deployments/{deploymentId}/agents/{siteSlug}/upgrade-command/reveal` | human | deployment.setup.write | — | `UpgradeCommandResponse`  | not-found, gone, rate-limited | install-command.revealed, audit:deployment.upgrade-command-revealed | §21 #92 the enrolled site's upgrade / reinstall command (60 s): the enrollment's install token or its maintenance token; not-found when not enrolled; gone details.reason not-stored (→ Regenerate). |
| `regenerateUpgradeCommand` | POST | `/v1/deployments/{deploymentId}/agents/{siteSlug}/upgrade-command` | human | deployment.setup.write | — | `UpgradeCommandResponse` (201) | not-found, rate-limited | install-command.regenerated, audit:deployment.upgrade-command-regenerated | §21 #92 Regenerate: a site-bound maintenance token (upgrade-only, never enrolls, no expiry); the previous command is revoked in the same transaction. |
| `revokeInstallToken` | DELETE | `/v1/deployments/{deploymentId}/install-tokens/{tokenHash}` | human | deployment.setup.write | — | `{ revoked: true }`  | not-found, gone | install-token.revoked | Revoke an unused token (used → gone). |
| `requestAgentUpgrade` | POST | `/v1/deployments/{deploymentId}/agents/{siteSlug}/upgrade` | human | deployment.setup.write | `RequestAgentUpgradeRequest` | `RequestAgentUpgradeResponse` (202) | not-found, invalid, conflict, stale | agent.upgrade-requested, audit:deployment.agent-upgrade-requested | §21 #90 "Upgrade agent": queue `agents/{site}.upgradeRequest` (confirm = siteSlug; version = the pinned version, else stale). invalid: not enrolled / agent < GUI_UPGRADE_MIN_VERSION (details.hint) / already on it; conflict: a request is queued or a run is going. agentEpoch +1 → CloudView.agentUpgrades. |
| `cancelAgentUpgrade` | DELETE | `/v1/deployments/{deploymentId}/agents/{siteSlug}/upgrade` | human | deployment.setup.write | — | `{ cancelled: true }`  | not-found, conflict | agent.upgrade-cancelled, audit:deployment.agent-upgrade-cancelled | Remove a queued request the agent has not started (conflict once the agent reported a phase for it). |
| `upgradeAllAgents` | POST | `/v1/deployments/{deploymentId}/agents/upgrade-all` | human | deployment.setup.write | `UpgradeAllAgentsRequest` | `AgentUpgradeRunResponse` (202) | not-found, invalid, conflict, stale | agent.upgrade-requested, audit:deployment.agent-upgrade-requested | §21 #90 "Upgrade all" (confirm "UPGRADE"): a run on `deployments/{id}.agentUpgradeRun` — every enrolled site below the pin in siteSlug order (unsupported agents = skipped with the rerun-install hint), ONE request at a time; the next site is queued when the current one reports done; the first failure stops the run. |
| `cancelAgentUpgradeRun` | POST | `/v1/deployments/{deploymentId}/agents/upgrade-all/cancel` | human | deployment.setup.write | — | `AgentUpgradeRunResponse`  | not-found, conflict | agent.upgrade-cancelled, audit:deployment.agent-upgrade-cancelled | Stop a running run: a queued, unstarted request is removed; a site already upgrading finishes; remaining sites = cancelled. |
| `revokeAgent` | DELETE | `/v1/deployments/{deploymentId}/agents/{siteSlug}` | human | deployment.setup.write | `RevokeAgentRequest` | `{ revoked: true }`  | not-found, invalid | agent.revoked | Delete the enrollment (the site key stops working; agentEpoch +1). Needed before a re-install. |
| `getStatus` | GET | `/v1/deployments/{deploymentId}/status` | human | deployments.read | — | `StatusResponse`  | not-found | — | Per-site status + 30-day uptime. |
| `listActivities` | GET | `/v1/deployments/{deploymentId}/activities` · query `ActivitiesQuery` | human | deployment.activities.read | — | `PageResponse<Activity>`  | not-found, invalid | — | Activities newest first (ts desc, then siteSlug, seq desc). |
| `uploadActivities` | POST | `/v1/deployments/{deploymentId}/activities/upload` | human | deployment.activities.write | raw bytes | `UploadActivitiesResponse`  | not-found, invalid, too-large | activities.uploaded | Upload a .opmlog file (raw body ≤ 24 MiB); verify signature + chains; insert missing (uploadedVia manual). |
| `listTransfers` | GET | `/v1/deployments/{deploymentId}/sites/{siteSlug}/transfers` · query `{ direction: TransferDirection }` | human | deployment.transfers.read | — | `ListResponse<Transfer>`  | not-found | — | Transfers of a site, newest first. |
| `createTransfer` | POST | `/v1/deployments/{deploymentId}/sites/{siteSlug}/transfers` | human | deployment.transfers.write | `CreateTransferRequest` | `CreateTransferResponse` (201) | not-found, invalid, too-large | transfer.sent | Start a cloud → site file (status uploading) + its multi-part upload; complete → ready + transfer.sent. |
| `downloadTransfer` | GET | `/v1/deployments/{deploymentId}/transfers/{transferId}/download` | human | deployment.transfers.read | — | bytes (200, Range) or 302  | not-found, gone | transfer.received | Download (Range supported); to-cloud transfers become downloaded. |
| `deleteTransfer` | DELETE | `/v1/deployments/{deploymentId}/transfers/{transferId}` | human | deployment.transfers.write | — | `Deleted`  | not-found | transfer.deleted | Delete a transfer and its blob (status expired, blob removed). |
| `installScript` | GET | `/v1/install/script` · query `{ version? }` | install | install token (unused, not expired, not revoked) OR the token of the site's current enrollment (§21 #86 upgrade, no expiry) | — | text  | gone | — | The POSIX sh install script for the token site (a used token gets the upgrade-only script). §21 #91: `?version=x` embeds that uploaded version (else a tiny script "not uploaded"); an expired / revoked / used-elsewhere / unknown token → 200 + a tiny script that prints what to do and exits 1. |
| `installDeps` | GET | `/v1/install/deps/{name}` · query `{ arch: 'amd64' \| 'arm64' }` | install | install token | — | bytes (200) or 302  | gone, invalid, not-found | — | A pinned install dependency from Storage deps/ (name "docker" = the official static Docker bundle docker-<ver>.tgz of deps/manifest.json) for arch; 200 bytes (or 302 signed URL) with X-OPM-Sha256 + X-OPM-Version. Same token rules as installBinary. |
| `installRelease` | GET | `/v1/install/release` · query `{ os; arch; version? }` | install | install token (same rules as installScript) | — | `AgentReleaseResponse` | gone, invalid, not-found | — | §21 #91 release metadata of the pin or an uploaded version (`agent/<v>/`): sha256, signature, public key, schema, minSchema, notes (pin only) — for `OPM_AGENT_VERSION=x`. |
| `installBinary` | GET | `/v1/install/binary` · query `{ os: 'linux' \| 'darwin'; arch: 'amd64' \| 'arm64'; version?: string }` | install | install token | — | bytes (200, Range) or 302  | gone, invalid, not-found | — | Agent binary (200 or 302) with headers X-OPM-Sha256 + X-OPM-Signature (Ed25519 of the sha256 hex by the release key). |
| `enroll` | POST | `/v1/agent/enroll` | install | install token (consumed) | `EnrollRequest` | `EnrollResponse` (201) | gone, conflict, invalid | install.enrolled | Register the site public key; marks the token used; agentEpoch +1. |
| `poll` | GET | `/v1/agent/poll` · query `PollQuery` | agent | site key | — | `PollResponse`  | — | — | Long-poll: answers at once when cursor ≠ agentEpoch, else holds ≤ wait s. Returns the full cloud view. |
| `agentRelease` | GET | `/v1/agent/release` · query `{ os; arch }` | agent | site key | — | `AgentReleaseResponse` | invalid, not-found | — | §21 #86 `opm-agent upgrade` + §21 #90 the agent GUI upgrade / release check: pinned version + sha256 + release signature + public key (+ §21 #90 `notes`, `sizeBytes`). |
| `agentBinary` | GET | `/v1/agent/binary` · query `{ os; arch; version }` | agent | site key | — | bytes (200, Range) or 302 | invalid, not-found | — | §21 #86 agent binary for `opm-agent upgrade` (X-OPM-Sha256 / -Signature / -Agent-Version). |
| `heartbeat` | POST | `/v1/agent/heartbeat` | agent | site key (siteSlug must equal the signer, or relayed) | `HeartbeatRequest` | `HeartbeatResponse`  | invalid | — | Liveness + state every 30 s; updates agents/{slug} and uptime. |
| `reportHead` | POST | `/v1/agent/head` | agent | site key | `ReportHeadRequest` | `ReportHeadResponse`  | invalid, conflict | proposal.stale | Report a new head (store revision if new; deployment.head = it when seq ≥ current; stale open proposals). |
| `getAgentProposal` | GET | `/v1/agent/proposals/{revId}` | agent | site key | — | `AgentProposalResponse`  | not-found | secret.fetched | Proposal + full revision (plain secrets). |
| `registerLocalProposal` | POST | `/v1/agent/proposals` | agent | site key | `RegisterLocalProposalRequest` | `{ stored: boolean }`  | invalid, conflict | proposal.superseded | Register a local proposal (origin agent) so the cloud shows it; idempotent by revId. |
| `reportProposalStatus` | POST | `/v1/agent/proposals/{revId}/status` | agent | site key | `ProposalStatusReport` | `ProposalStatusResponse`  | not-found, invalid | — | Staging per site, or the coordinator status / result; the cloud recomputes the status. |
| `getAgentRevision` | GET | `/v1/agent/revisions/{revId}` | agent | site key | — | `{ revision: Revision }`  | not-found | secret.fetched | Any revision of the deployment (plain secrets). |
| `getImageManifest` | GET | `/v1/agent/images/{serviceSlug}/{version}/manifest` | agent | site key; image version in the head or an open proposal | — | `ImageManifestResponse`  | not-found, forbidden, invalid | — | Manifest digest, config and layers. |
| `getAgentBlob` | GET | `/v1/agent/blobs/{sha256}` | agent | site key; blob of an image version or a file in the head or an open proposal | — | bytes (200, Range) or 302  | not-found, forbidden | — | Layer / config / manifest / file bytes (200 with Range, or 302 to a signed URL). Gateway mode fetches from the registry first. |
| `uploadAgentActivities` | POST | `/v1/agent/activities` | agent | site key (siteSlug = signer) | `AgentActivitiesRequest` | `AgentActivitiesResponse`  | invalid | — | Upsert ≤ 500 entries; never changes stored records; returns ackSeq. |
| `listAgentTransfers` | GET | `/v1/agent/transfers` · query `{ direction: 'to-site' }` | agent | site key | — | `ListResponse<Transfer>`  | — | — | Cloud → site transfers of the signer site. |
| `createAgentTransfer` | POST | `/v1/agent/transfers` | agent | site key | `AgentCreateTransferRequest` | `CreateTransferResponse` (201) | invalid, too-large | — | Start a site → cloud file + its upload. |
| `reportAgentTransfer` | POST | `/v1/agent/transfers/{transferId}/status` | agent | site key | `AgentTransferStatusRequest` | `{ ok: true }`  | not-found, invalid | transfer.saved | saved-on-server / downloaded / fetched (to-site; fetched = stored on the site → `fetchedAt`, §21 #100) or ready (to-cloud, after complete). |
| `downloadAgentTransfer` | GET | `/v1/agent/transfers/{transferId}/download` | agent | site key (to-site transfer of the signer) | — | bytes (200, Range) or 302  | not-found, gone | — | Transfer bytes (Range supported). |
| `getAgentUpload` | GET | `/v1/agent/uploads/{uploadId}` | agent | site key (creator) | — | `Upload`  | not-found, gone | — | Upload state. |
| `putAgentUploadPart` | PUT | `/v1/agent/uploads/{uploadId}/parts/{index}` | agent | site key (creator) | raw bytes | `UploadPartResponse`  | not-found, gone, invalid | — | Upload one part. |
| `completeAgentUpload` | POST | `/v1/agent/uploads/{uploadId}/complete` | agent | site key (creator) | — | `CompleteUploadResponse`  | not-found, gone, invalid | transfer.sent | Join + verify; the to-cloud transfer becomes ready. |
| `relay` | POST | `/v1/agent/relay` | agent | site key of the relaying site + valid inner signature of forSite | `RelayRequest` | `RelayResponse`  | invalid | — | Run the inner signed request as forSite; sets agents/{forSite}.relayVia. |

### 6.1 Endpoint behaviour details

**Registries.** Anonymous registry = `username ""` and `password ""` (public images, e.g. Docker Hub `library/redis`); else
both are required (1–255 / 1–8192). `test`: GET `<url>/v2/` → if 401 with `WWW-Authenticate: Bearer realm=…,service=…`, get a
token from the realm (basic auth with username/password; anonymous: no auth) with `scope=repository:<repo>:pull`, where
`<repo>` = the optional JSON body `{ repository?: string }` of the test call, else the repository of any image that uses this
registry, else no scope. The typed client sends no body (contract `body: 'none'`); the frontend sends the body with a raw call.
200 → ok "login ok"; else ok false + message (HTTP status + body excerpt ≤ 200 chars). Stored in `lastCheck`.
**Pull-access check (Docker Hub 401).** Docker Hub issues a token even when the account cannot pull the repository (unknown
repository or no access); the token is then a JWT whose `access` claim does not grant `pull` on it. With a repository, the
backend decodes the JWT (no signature check, `tokenGrantsPull`) and answers ok false with the message
`repository <repo> not found, or account <user> has no pull access to it (Docker Hub answers 401 for both)` (anonymous:
`anonymous access`; other registries: `the registry answers 401 for both`). The same message is used when a manifest GET
answers 401 in `inspectImage` and in the import job. An opaque (non-JWT) token cannot be checked (no extra error).
`reveal-password` is audited as `registry.password-revealed` (target `registries/<id>`, §6.2; the password is never logged).
**Registry browsing** (GET, `images.write`, not audited; the register-image pickers): `listRegistryRepositories` (`?q=`) —
Docker Hub: the account namespace + Hub search `q`; Artifact Registry: its API with the `_json_key` credentials; other
registries: Docker v2 `_catalog`. `listRegistryTags` (`?repository=&q=`) — Docker Hub API, Artifact Registry API, or Docker v2
`tags/list`; with `pushedAt` where the registry tells it (newest first, else by name). A registry that cannot list answers
`supported: false` + `message` (200); the form keeps free text.

**Icons.** `uploadIcon`: raw body ≤ `ICON_MAX_BYTES` (65536; else too-large), content type from the magic bytes (png, webp,
svg text; else invalid), SVG sanitized (scripts, `on*` handlers, external references removed), `?name=` (1–80, default the
file name). sha256 of the stored bytes equal to an existing icon → that icon with `existing: true` (nothing stored; still
audited `icon.uploaded` with `data.existing`). The bytes are stored IN the document as the internal field `_bytes` (Firestore
Bytes) — one document, atomic create / delete, never returned by the API. `listIcons`: every icon (newest first) + `usedBy`.
`deleteIcon`: 409 conflict `details.users` (`IconUser[]`) while a service / template / tag / deployment / account uses it.
`getIconContent` (auth public, so `<img>` works without a token): the bytes with `Content-Type`, `Cache-Control: public,
max-age=31536000, immutable`, `X-Content-Type-Options: nosniff`; SVG also with a restrictive `Content-Security-Policy`. Icon
fields (service / template / tag `icon`, account `img`, deployment `image`): format check (`checkIcon` / `checkPicture`) and an
`upload:<iconId>` must exist (invalid V_REF "upload it first").

**Services.** `createService` body `{ serviceSlug, label, icon?, description? }`; doc id = `serviceSlug` (`create()`; exists →
conflict). PATCH: label / icon / description only (`serviceSlug` in the body → invalid V_IMMUTABLE). Delete only with 0 images.

**Images.** `registerImage` body `RegisterImageRequest` `{ version, reference, registryId?, platform?, mode, kind, … }`. Checks:
service exists; body (`validateRegisterImage`: `version` matches `RE.version`, `reference` parses — `parseImageReference`, else
V_REFERENCE); source resolution: `registryId` omitted → the first registry (by label) whose URL host
(`registryHost(url)`, Docker Hub URLs → `docker.io`) equals the reference host (`matchRegistries`), none → null (public);
`registryId` null → public; an id → it must exist (not-found) and its host must equal the reference host (invalid). Doc id =
`version`: `create()` of `services/{serviceSlug}/images/{version}`; exists → 409 conflict `{ field: 'version', reason:
'version-exists' }`. Stores `reference` (trimmed, as entered), `source` (parsed + normalized `ImageRefParts`), `registryId`,
`digest` = `source.digest` (or null), env normalized (`normalizeImageEnv`, `options` always written: null or the list), and
`import { mode, status: 'pending', error null, sizeBytes null, manifestMediaType null, configDigest null, layers [], startedAt
null, finishedAt null }`. The Firestore trigger `importImage` (onDocumentWritten, runs when `import.status` becomes `pending`;
timeout 540 s, 1 GiB):
1. status `copying`, startedAt.
2. Credentials: the image registry (its host must still equal `source.host`), or anonymous on the reference host
   (`docker.io` → `https://registry-1.docker.io`, else `https://<host>`). Token auth as in test with
   `scope=repository:<source.repository>:pull` (+ the pull-access check above).
3. GET manifest `<url>/v2/<source.repository>/manifests/<source.digest ?? source.tag ?? 'latest'>` with Accept: OCI index,
   Docker manifest list, OCI manifest, Docker v2 manifest. Index/list → pick the entry with `platform` (os linux +
   architecture) → GET that manifest by digest. Missing platform → failed "platform linux/arm64 not in the image".
4. `digest` = sha256 of the platform manifest bytes; store manifest bytes at `blobs/sha256/<hex>`; parse config + layers.
5. copy mode: stream config + every layer blob (`<url>/v2/<repo>/blobs/<digest>`, follow redirects) into
   `blobs/sha256/<hex>`, verifying sha256 while streaming; skip blobs that already exist. gateway mode: copy only manifest + config.
6. status `ready`, layers [{digest, sizeBytes, mediaType}], sizeBytes = Σ layers, configDigest, manifestMediaType, finishedAt.
   Any error → status `failed`, error message. `retry-import` sets status back to `pending` (only from failed).
System audit entries `image.import-ready` / `image.import-failed` (§6.2). Gateway mode: the first `GET /v1/agent/blobs/{sha}`
for a missing layer streams it from the registry into Storage (then serves it).
`patchImage` (§21 #82): every field except the identity (version, serviceSlug, source, digest, platform, import / mode /
layers, created → invalid V_IMMUTABLE). `reference` / `registryId` only while `import.status = failed` (else 409 conflict): fix
a wrong source, then `retry-import`. Body fields given replace the stored ones; `ports` / `env` / `volumes` are FULL lists
(legacy partial items `{ name, description }` / env without required + secret still merge by name; mixing → V_TYPE). The merged
image is checked by `validateImageSpec` (= the registration rules: names, numbers, choice defaults V_OPTION, mount paths V_PATH,
health check port V_REF …). Renames are explicit: `renames: [{ kind: port | env | volume, from, to }]` (`imageNameChanges`;
bad entries / a name both kept and renamed → V_RENAME); an old name neither kept nor renamed is REMOVED. Impact, in the same
Firestore transaction as the write: `imageNameUses` scans every template and every non-deleted deployment DRAFT for containers
with this serviceSlug + version — port: `ports[<p>]`, container `healthcheck.portName`, edge proxy routes of that container
`edge.routes[<id>].portName`, expressions `site.<s>.port.<containerSlug>.<p>` in env values of any container; env: `env[<N>]`,
`container.env.<N>` in that container's env values / mount paths; volume: `volumes[<v>]`. Heads, revisions and open
proposals are never scanned or changed (self-contained ImageSpec copy); file contents (blobs) are not scanned (publish
validation reports stale references). A used REMOVAL → 409 conflict, details `{ reason: 'in-use', items: [{ kind, name,
change: 'removed', to: null, usedBy: [{ kind, templateId | deploymentId, label, source template | draft, siteSlug,
containerSlug, field }] }] }` — also with renameEverywhere. A used RENAME → the same 409 (change 'renamed', to) unless
`renameEverywhere: true`: then `renameImageNames` rewrites those references (inside `{{ }}` blocks only for expressions;
swaps work; an env row colliding with an existing container row → invalid V_DUPLICATE) in every affected TEMPLATE (version+1,
one root audit entry `template.updated`, data.reason `image-rename-everywhere`, same requestId), then saves the image.
**Deployment drafts are never rewritten (§21 #94, detached):** they keep the old names; the 409 details list them, the
`image.updated` audit data `renamedEverywhere.deploymentsNotRewritten [{ deploymentId, label, references }]` records them,
and their draft validation reports the stale names until a Template sync (the rewritten template shows the rename as
template changes) or a manual edit. Added names, value /
default / description / flag changes and port number / protocol changes are always allowed (containers map ports by name).
The `image.updated` audit entry carries the diff + data `{ renamed, removed, added, portRetargeted, renamedEverywhere }`.
`inspectImage` body `{ reference, registryId?, platform? }` (same source rule as register): steps 2–4 + config JSON → prefill
(`ExposedPorts` "8080/tcp" → `{ name: "p8080", port: 8080, protocol: "tcp" }`, `Env` "K=V" → `{ name: K, value: V, required:
false, secret: false }` (only names matching the env pattern), `Volumes` → `v1…`, `Entrypoint`, `Cmd`, `WorkingDir` (null if
""), `User` (null if "")). Nothing stored. The register form uses it for "Read from registry" (a diff the user accepts).
`serviceUsage` / `imageUsage`: scan templates (sites[].config.containers) and deployments (draft and head revision) — one
`UsageItem` per container instance, deduplicated by (kind, id, siteSlug, containerSlug); items carry `containerSlug`,
`serviceSlug`, `version`.

**Templates.** PATCH replaces the given arrays. Validation: `validateConfig({variables, secrets, files, sites}, { mode:
'template', deploymentId: 'template', services, images (catalog with importStatus), notFoundPageIds })`. Any severity
"error" → 422 invalid (nothing saved); else saved, version+1, response carries `validation` (warnings = publish-level issues).

**Commit stages (§21 #107).** `POST /v1/stages` · `PUT /v1/stages/{stageId}/blobs/{sha256}` · `DELETE /v1/stages/{stageId}`
(+ `POST /v1/uploads { …, stageId }`): the bytes of ONE commit attempt, in Storage `staging/<stageId>/<hex>` (never read by
anything but the commit; lifecycle rule: deleted after 1 day). The commit verifies + promotes them into `files/<hex>` before it
becomes visible and deletes the stage on every outcome. Not audited (the commit is the entry). See §21 #107.

**Blobs + uploads.** `GET /v1/blobs/{sha256}` (humans: file editors, previews): `files/<hex>` must exist (else not-found);
`sizeBytes ≤ BLOB_STREAM_MAX` (8 MiB) → 200 stream, `Content-Type: application/octet-stream`, `ETag: "<sha256>"`,
`Cache-Control: private, max-age=300`; larger → 302 to a 5-minute signed URL. The bucket CORS (`backend/storage.cors.json`:
GET/HEAD from the hosting origins + `http://localhost:5173`, `http://127.0.0.1:5173`) is applied by the backend deploy script
so the browser can follow the 302. `POST /v1/blobs`: raw body; sha256 computed; if header `X-OPM-Sha256` is present and differs → invalid;
store `files/<hex>` if missing; `{ sha256, sizeBytes }`. Uploads (`purpose: 'blob'` up to 50 MiB, transfers up to 2 GiB):
`partSize = 8388608`, `partCount = max(1, ceil(sizeBytes / partSize))`; PUT part `index` (0-based) must be exactly partSize
bytes (last = remainder; size 0 file = one empty part) else invalid; parts stored `uploads/<id>/part-<n>`; idempotent.
Complete: all parts present (else invalid with details.missing), compose (GCS compose ≤ 32 per call, iteratively) into the
target path (`files/<hex>` or `transfers/<dep>/<tid>`), stream-verify sha256 + size (mismatch → upload `failed`, invalid),
delete parts, status complete. Transfer uploads: transfer becomes `ready` (+ activity `transfer.sent`, `agentEpoch+1` for
to-site). Expiry: uploads 24 h (`daily` deletes parts; status failed "expired").

**Accounts** (§5.1, §21 #84). `createAccount` / `setAccountPassword`: generate a 20-char alnum password, set it with the
Admin SDK ("Set new password…" ends the person's sessions), read the Auth user back and store
`lastSetPassword { value, setAt, setBy, validAfter = tokensValidAfterTime }`; response `{ value, hideAfterSec: 60, … }`.
`resetAccountPassword`: `generatePasswordResetLink` → `lastResetLink { value, createdAt, createdBy, expiresAt = +1 h,
validAfter }` (replaces the previous one). `getAccount` / `listAccounts` (batched `getUsers`) / the reveals compare the
stored `validAfter` with the current `tokensValidAfterTime`: later → both stored values are cleared (the person changed their
password / used a link). `revealAccountPassword` → 410 `gone` when none is stored; `revealAccountResetLink` → 410 `gone` when
none / expired / used. The API `Account` carries only `{ setAt, setBy }` (+ `expiresAt`).

**Deployments.** `createDeployment`: validate label, tags exist, managers exist; template (if given) exists. Response holds
the plaintext access key (`CreateDeploymentResponse.accessKey`, `hideAfterSec` 60); it is stored (`deployment.accessKey`)
and viewable again any time with `revealAccessKey` (§4.6, §21 #84). `_cloud` activity `deployment.created` + audit.
`putDraftSection` (section ∈ variables | secrets | files | sites): `ifRevision` must equal `draftVersion` (else 409 conflict,
details.current). Items for `secrets` are `SecretDefInput` (plainValue semantics; `generateNow`), the server merges with the
stored values, sets `rotatedAt` on every value change. Validation `mode: 'draft'` on the WHOLE resulting draft; errors → 422
(nothing saved). Saves `draft[section]`, `draftVersion+1`, updatedAt/By. Response: `{ draftVersion, validation }`.
`putDraftSite`: same, for one existing site (path slug = body slug; unknown site → not-found; add/remove sites via the
section `sites`). Removing a site whose agent is enrolled is allowed (the agent becomes idle after the apply).
`revealSecret`: `{ value, hideAfterSec: 60 }` of the DRAFT secret (not set → not-found), any number of times;
`Cache-Control: no-store`; `_cloud` activity `secret.revealed { secretSlug, actor: uid, where: 'cloud' }` + audit
`deployment.secret-revealed`. `regenerateSecret` (`{ ifRevision }`, deployment.secrets.write): the secret must have a
`generate` rule (else 422 invalid V_SECRET_NO_GENERATE; templates hold no values, so there is nothing to regenerate there);
`ifRevision` ≠ draftVersion → 409 conflict; generates a new value, sets `rotatedAt`, `draftVersion+1`; activities
`draft.updated` + `secret.regenerated { secretSlug, draftVersion }` + audit `deployment.secret-regenerated`; response
`RegenerateSecretResponse { value, hideAfterSec: 60, draftVersion, rotatedAt }`. The new value reaches the sites with the
next publish.

**Diff** (`getDiff`): head = `deployment.head` → its revision config; `changes = diffConfigs(head.config, draft)` with secrets
compared by plain value (cloud has both) and rendered as set / changed / not changed; `blockers` = errors of
`validateConfig(draft, mode 'publish')` + "draft equals head" (code `V_NO_CHANGE`, only when head exists and changes = []);
`warnings` = warnings; `publishable` = blockers empty; `pending` = open proposals.

> **SUPERSEDED by §21 #104 (agent ≥ 0.14, cloud with branches)** — kept for agents < 0.14 (compat proposals) and as history. Current model: docs-v2/branches-commits.md.

**Publish** (transaction): `ifRevision` = draftVersion (else conflict); recompute blockers (else 422 invalid with them);
parent = `deployment.head?.revId ?? null`; if an open proposal is `approved` or `applying` → 409 `conflict` ("an update is
running"); build `catalog` (services keyed by serviceSlug with uploaded icons embedded as data: URLs + ImageSpec for every container `<serviceSlug>:<version>` + not-found pages referenced) from the current
catalog; revision `{ revId: ULID, seq: (head?.seq ?? 0) + 1, parentRevId, origin 'cloud', actor uid, createdAt, config: draft,
hash, catalog, catalogHash, summary, kind 'change', rollbackOf null }`; proposal `{ status 'published', sites: pending for
(parent config sites ∪ draft sites), … }`; every open proposal with status published | staging | ready → `superseded`,
`supersededBy` = new revId (+ activity `proposal.superseded`); `agentEpoch+1`; activity `proposal.published { revId,
parentRevId, seq, summary }`. Note: seq is relative to the cloud head; the sites refuse it as stale if their head moved.

> **SUPERSEDED by §21 #104 (agent ≥ 0.14, cloud with branches)** — kept for agents < 0.14 (compat proposals) and as history. Current model: docs-v2/branches-commits.md.

**Template sync** (§21 #94; `shared/src/template-sync.ts`; the ONLY path that reads a template for an existing deployment,
and only on a person's request). Deployments are detached: nothing syncs in the background and no notification ever
updates a draft. 3-way: **base** = `srcTemplate.baseSnapshot` (the template config at `baseVersion`, stored in the
deployment — templates keep no version history), **theirs** = the template now (or the template picked in "Change
template…" / "Link a template…"; base = the old base, or empty when unlinked), **ours** = the draft. Items (lists keyed by
slug / name, never position): `variables/<slug>` (definition without the value) + `variables/<slug>/value` (kind `default`:
only when the template changed its default since the base and it differs from the deployment value; never pre-selected —
"use template default"), `secrets/<slug>` (definition + generate rule; values are never compared, returned or copied),
`files/<slug>` (metadata + content sha256), `sites/<site>` (whole site when one side lacks it), `sites/<site>/settings/<label |
host | agentPort | agentGuiAliasPort | notFoundPageId | depends>`, `sites/<site>/edge/<httpPort | httpsPort | maxBodyBytes>`,
`…/edge/hosts/<hostname>`, `…/edge/routes/<routeId>`, `…/volumes/<volumeSlug>`, `…/containers/<slug>` (whole container when
one side lacks it: a rename = remove + add), `…/containers/<slug>/<image | enabled | command | entrypoint | restart |
resources | healthcheck | depends | ports | volumes>` and `…/containers/<slug>/env/<NAME>` (value string / null or
`{ secretRef }`, scalar). A site / container present in both theirs and ours is compared field by field. Kinds per item
(canonical JSON equality, absent ≠ null): theirs = base ≠ ours: **theirs** (clean); only ours moved: **ours** ("your change",
greyed); both moved the same: **same** (no-op); both moved differently: **conflict**; an earlier not-taken template change
whose template value is unchanged (skip record `{ key, theirsHash }`): **skipped**. `templateSyncHash` = cyrb53 of the
canonical value ("-" = absent).
Plan (`GET …/template-sync`): perms deployment.config.read + templates.read; not linked and no templateId → 422 V_REQUIRED;
template missing → 404. Returns masked base / theirs / ours, `changes`, `counts`. Preview / apply body
`{ ifRevision, templateId, templateVersion, choices: [{ key, take: theirs | ours | custom, value? }], values? }`:
ifRevision ≠ draftVersion → 409 `{ current }`; templateVersion ≠ the template's version → 409 `{ reason: 'template-changed',
current }`; a bad choice (unknown key = the plan moved, duplicate, custom on a non-scalar, values for an unknown variable) →
422 `V_SYNC_CHOICE`. Merge = ours + every chosen item (theirs value / the merged scalar); a taken variable definition keeps
the deployment value (a NEW variable starts with the template default, `values` override); a taken secret definition keeps
the draft value; a secret the sync ADDS with a generate rule gets a generated value (the create rule); list additions go
after their predecessor in the template order. Preview = the merged draft (masked), `diffConfigs(draft → merged)`,
`validateConfig(merged, mode 'draft')` (errors + warnings), unresolved conflicts, `missing` (required variables without a
value — the review asks for them inline — and required secrets without generate and value — set on the Secrets page);
writes nothing. Apply (transaction, re-reads the template + deployment): additionally conflicts without a choice → 422
`V_SYNC_UNRESOLVED`; secret items taken need deployment.secrets.write (403); draft validation errors → 422 invalid; writes
the draft (draftVersion+1 only when the config changed) and `srcTemplate` = (relink → new templateId / templateVersion /
snapshot / copiedAt) + `baseVersion` = the template version, `baseSnapshot` = its config, `lastSyncedAt/By`, `skipped` =
every template change not taken (theirs / conflict kept mine / skipped; existing records kept while their hash matches);
`_cloud` activity `draft.template-synced { templateId, fromVersion, toVersion, relinked, previousTemplateId?, taken, skipped,
conflicts, draftVersion, keys[≤100] }` + root audit `deployment.template-synced` (AUDIT_DEPLOYMENT_OPS; every attempt, also
the 409 / 422 ones). It never publishes: the UI links to Diff → Publish. Status (`…/template-sync/status`, config.read):
currentVersion, `newerVersions = max(0, version − baseVersion)`, `pendingChanges` = theirs + conflict + default,
`conflicts`, `templateDeleted`. Unlink (`{ confirm: true }`, config.write): srcTemplate = null (and `_srcBase` / `_srcSkipped`),
activity `deployment.template-unlinked { templateId, baseVersion }` + audit; not linked → 422.

**Reset to head**: draft = head revision config (secrets plain), `draftVersion+1`, activity `draft.reset-to-head`.

**Setup.** `createInstallToken` (§4.2) stores the token while active and returns the command (`value`, 60 s) →
`install-token.created { siteSlug, tokenHash, expiresAt }` + audit `deployment.install-command-created`; with
`replaceActive: true` (Regenerate) it first revokes the site's active tokens in the same transaction (`install-token.revoked`
each). `revealInstallCommand` → the command of an active, stored token (`install-token.revealed { siteSlug, tokenHash }` +
audit `deployment.install-command-revealed`; else 410 gone reason used / revoked / expired / not-stored).
`revealAccessKey` → the stored key (`access-key.revealed { version }` + audit `deployment.access-key-revealed`; 410 gone
reason not-stored when `accessKey` is null). `rotateAccessKey` = Regenerate (§4.6). `revokeInstallToken` sets
`revokedAt` and nulls `token` (used → gone). `revokeAgent` (confirm = siteSlug) deletes `agents/{slug}` → `agent.revoked`, agentEpoch+1.
`getSetup` lists every site of draft ∪ head ∪ enrolled agents with tokens (newest first, max 10 per site; `kind`,
`upgradeCommand` = the site's current upgrade command), per enrolled site `upgradeCommand { tokenHash, kind, stored,
createdAt, createdBy }` and `agent.clockSkew`, and `serverTime` (§21 #92). `revealUpgradeCommand` /
`regenerateUpgradeCommand`: §4.2 item 6.

`getSetup` also returns `agentRelease` (the pin: deps/manifest.json `agent` version + `notes`), `agentUpgradeRun` and per
enrolled site `upgrade: SiteAgentUpgradeView` (installed = agents/{slug}.agentVersion, latest = the pin, `available` =
`agentUpgradeAvailable`, `supported` = `supportsGuiUpgrade`, `hint` when unsupported, the queued `request`, the last
reported `progress`); `getStatus` carries the same `upgrade` per site.

**Agent upgrades** (§21 #90; never automatic). `requestAgentUpgrade` (confirm = siteSlug, version = the pin): checks in a
transaction — the site is enrolled (else invalid), `supportsGuiUpgrade(agentVersion)` (else invalid, `details.hint` = "rerun
the install command of this site once (it stays valid for upgrades)"), `version` = the current pin (else 409 stale),
installed < pin (else invalid "already on it"), no unexpired `upgradeRequest` and no running run (else conflict) — then
writes `agents/{slug}.upgradeRequest = AgentUpgradeRequest { requestId: ULID, version, requestedBy: email, requestedByUid,
requestedAt, expiresAt = +AGENT_UPGRADE_REQUEST_TTL_SEC (24 h), runId: null }`, agentEpoch +1, `_cloud` activity
`agent.upgrade-requested { requestId, siteSlug, version }` + audit `deployment.agent-upgrade-requested`.
`cancelAgentUpgrade` removes a request no progress has been reported for (else conflict) → `agent.upgrade-cancelled`.
`upgradeAllAgents` (confirm "UPGRADE", version = the pin) writes `deployments/{id}.agentUpgradeRun` (origin cloud;
`sites` = every enrolled site in siteSlug order: below the pin + supported → pending, unsupported → skipped with the hint,
already on it → done) and queues the request of the first pending site (`runId` set). The heartbeat handler (below) moves
the run: the current site's progress done → site done and the next pending site's request is queued; failed / rolled-back
→ site failed, run failed (no further requests); every site done / skipped → run done. `cancelAgentUpgradeRun` removes a
queued, unstarted request, marks the remaining sites cancelled and the run cancelled (a site already upgrading finishes).
Expired requests are dropped from `CloudView.agentUpgrades` (and cleared by the next heartbeat of that site).

**Status.** `online` = now − lastSeenAt < 90 s. `uptime[].days` = last 30 UTC days (missing doc → fraction 0, hours all 0),
`onlineFraction` = mean of hours (current day: mean of the elapsed hours).

**Activities upload** (manual .opmlog, §13.3): parse tar; verify `signature.sig` with the public key of `manifest.signedBy.siteSlug`
(an enrolled site, or a revoked one: revoke copies the key to `deployments/{id}/revokedKeys/{fingerprint}` `{ siteSlug, publicKey, revokedAt }`);
verify `activities.ndjson` sha256 + size; per site: entries sorted by seq, each `hash` recomputed (`activityHashInput`), chain
`prevHash` = previous entry hash (or the stored record seq−1 when the bundle starts later; first entry seq 1 needs prevHash null).
First broken entry and all later ones of that site → rejected (errors[]). Others: create if missing (`uploadedVia: 'manual'`,
`uploadedAt`), existing → skipped (never changed). Updates `agents/{slug}.ackSeq` to the highest contiguous seq. Bad signature →
422 invalid (nothing inserted). Stores the file under `activity-bundles/`.

**Transfers.** `createTransfer` (to-site): `sizeBytes ≤ 2 GiB`, fileName 1–255 without `/`, `\`, NUL; creates Transfer
(status uploading, expiresAt +30 d) + Upload(purpose transfer). Download marks to-cloud transfers `downloaded` and writes
`transfer.received`. Delete: status expired, Storage object removed, agentEpoch+1 (to-site).

**Agent: poll** (`GET /v1/agent/poll?cursor&wait`): resolve the signer; load the deployment; if `cursor !== String(agentEpoch)`
or `wait = 0` → answer now; else hold: re-read `agentEpoch` every 1 s (or a Firestore listener) until it changes or `wait`
seconds pass (default 25, max 50), then answer. Answer = `CloudView { fetchedAt: server now, source: 'cloud', cursor:
String(agentEpoch), deploymentId, deleted, proposals: open proposals + 5 newest terminal (ProposalMeta), transfersToSite: every
to-site transfer of the deployment with status ready (TransferMeta), draftHead: deployment.head (revId, seq) | null, accessKey:
{ hash, version }, peers: PeerInfo of every enrolled site, agentRelease: the pin (version + notes), agentUpgrades: every
unexpired agents/*.upgradeRequest (§21 #90) }`. The poll also counts as liveness (`lastSeenAt`).

**Agent: heartbeat** (every 30 s, also relayed for peers without internet): updates `agents/{slug}`: lastSeenAt (ALWAYS the
cloud's receive time, never an agent-reported time, §21 #92), clockSkew (§21 #92: optional `agentTime` = the agent's raw
clock → `{ skewSec: round(agentTime − receive time), measuredAt }`; absent = unchanged; bad format → 422 invalid), agentVersion,
headRevId, staging, metrics, edge, peersSeen, keyVersion, ip, hostname, advertiseHost, agentPort, detectedAddresses (absent →
`[]`); `relayVia` (null when direct); §21 #90 `upgrade` (AgentUpgradeProgress, absent = unchanged) → `agents/{slug}.upgrade`;
when `upgrade.requestId` = the queued `upgradeRequest.requestId` and the phase is done / failed / rolled-back (or the
reported agentVersion = request.version) the request is cleared and the run (if any) advanced or stopped (above);
increments uptime beats. If advertiseHost/agentPort changed → agentEpoch+1 (peers learn it).

**Agent: head** (`reportHead`): verify hash + catalogHash + `revision.seq = parent.seq + 1` (parent known or seq 1 with parent
null); store the revision if missing (local revisions included; `stored`); if `revision.seq > (deployment.head?.seq ?? 0)` →
`deployment.head = { revId, seq, hash, reportedAt: now }`, agentEpoch+1, and every open proposal whose `parentRevId ≠ revId`
and whose revId ≠ revId becomes `stale` (+ activity `proposal.stale { revId, parentRevId, headRevId }`); the proposal with
revId = the new head becomes `applied` if it is not already. Same seq but different revId → 409 conflict (fork; should not
happen — agents refuse non-linear heads).

**Agent: proposals.** `getAgentProposal` / `getAgentRevision` return plain secrets and write `_cloud` activity
`secret.fetched { siteSlug, secretSlugs, revId }` (only when secrets[] is non-empty). `registerLocalProposal` stores the
revision + proposal (origin agent) if new, supersedes older open proposals (same rule as publish), agentEpoch+1.
`reportProposalStatus` merges: `staging`/`progress`/`error` into `sites[siteSlug]` (updatedAt now); `status` (+ approvedBy,
approvedAt, updateId, result) from the coordinator. Then the cloud recomputes the derived status (§14.2): published →
staging when any site ≠ pending; staging → ready when every site is ready; ready → staging if a site falls back; never
overrides approved/applying/terminal states except by an explicit coordinator status. Any change → agentEpoch+1.

**Agent: images + blobs.** `getImageManifest` (`/v1/agent/images/{serviceSlug}/{version}/manifest`): the version must be in
the head revision config or an open proposal of the deployment
(else forbidden); import not ready → invalid. `getAgentBlob`: sha256 must belong to (a) manifest/config/layer of such an image,
or (b) a file content sha of such a config (else forbidden); serve `blobs/sha256/<hex>` (images) or `files/<hex>` (files);
200 stream with `Content-Length`, `Accept-Ranges: bytes`, Range support, or 302 to a 5-minute signed URL (production). Gateway
mode missing layer: fetch from the registry first.

**Agent: activities** (pipe): entries must be contiguous ascending seq for the signer site; first entry seq must be ≤ ackSeq+1
(a gap → invalid "missing seq n"); chain + hash verified like the manual upload; create if missing (`uploadedVia: 'pipe'`),
existing → skipped; response ackSeq = highest contiguous seq stored; `agents/{slug}.ackSeq` updated.

**Agent: transfers.** `listAgentTransfers` → to-site transfers of the signer with status ready | saved-on-server | downloaded
(not expired). `createAgentTransfer` → to-cloud Transfer (createdBy = actor email) + Upload; complete → ready + `transfer.sent`.
`reportAgentTransfer`: `saved-on-server` (savedPath) / `downloaded` for to-site → activity `transfer.saved` / `transfer.received`.
§21 #100: `fetched` (to-site only; not while uploading; expired → 422) sets `fetchedAt` = server time once — no status change, no
activity, idempotent; `saved-on-server` / `downloaded` also set `fetchedAt` when it is still null. Agents ≥ 0.11.2 report
`fetched` as soon as the file is stored on the site (agents < 0.11.2 never do: `fetchedAt` stays null until a save / download).


---------------------------------------------------------------------------------------------------------------------

**GitHub mirror (§21 #106; table `GITHUB_MIRROR_CLOUD_API` in shared/src/github-mirror.ts, spread into CLOUD_API).** `GET|PATCH /v1/github-mirror`, `POST /v1/github-mirror/test | reveal-token | sync | resync`, `GET /v1/github-mirror/branches | queue` (accounts.admin, audited github.*), agent `POST /v1/agent/branch/drift` (site key, agent ≥ 0.15.0).

**Volume content (§21 #101; table `VOLUME_CONTENT_CLOUD_API` in shared/src/volume-content.ts, spread into CLOUD_API).**
Human (deployment scope, `_cloud` activities): listVolumeContent / getVolumeContent / listVolumeContentVersions /
getVolumeContentVersion (deployment.config.read); createVolumeContentVersion (edit; baseVersionId = latest else 409 conflict
`{ latest }`; blobs must exist → 422 V_BLOB_MISSING `{ missing }`; paths under the excludes → 422 V_VOLUME_CONTENT_EXCLUDED; tree
issues → 422; identical to the latest → 200 with the latest), restoreVolumeContentVersion, requestVolumeSnapshot
(deployment.config.write); sendVolumeContent / cancelVolumeContentPush (deployment.config.publish; base = the newest snapshot
ancestor of the version; a new send supersedes the pending one). Agent (site key): agentCheckBlobs, agentPutBlob (raw ≤ 24 MiB,
sha verified, only for a persist volume of the signer's head), agentCreateVolumeSnapshot (idempotent by snapshotId; treeHash
verified; clears the answered snapshot request), agentReportVolumePush. agentEpoch+1 on send / cancel / snapshot request / push
report. Poll: `CloudView.volumeContent` = volumes with a pending push or an open snapshot request. Heartbeat `volumeSync` →
state.live. getAgentBlob also serves tree / single seed blobs of the head / open proposals / site heads and the tree JSON + blobs
of a pending push's version and its base. Daily: AUTO snapshot versions beyond the newest 200 per volume are deleted (never one
referenced by a push; blobs stay).

### 6.2 Root audit log (`auditLog/{auditId}`, `GET /v1/audit-log`)

**What is logged.** EVERY human request whose method is not GET and whose path is NOT deployment-internal
(`/v1/deployments/{deploymentId}/…`) writes exactly one `AuditLogEntry` — on success AND on failure (403, 4xx, 5xx with
`errorCode`). Root deployment create / patch / delete (`/v1/deployments`, `/v1/deployments/{id}`) are audited in addition to
their `_cloud` activities. Deployment-internal actions (drafts, publish, secrets, setup, transfers, activities upload) are NOT
in the audit log; each writes a `_cloud` activity of that deployment instead (Appendix B) — EXCEPT the deployment-scoped
credential ops `AUDIT_DEPLOYMENT_OPS` (`revealAccessKey`, `rotateAccessKey`, `createInstallToken`, `revealInstallCommand`,
`revealSecret`, `regenerateSecret`; §21 #84), which write BOTH the `_cloud` activity and a root audit entry (target
`deployments/{id}`; actions `deployment.access-key-revealed`, `deployment.access-key-rotated`,
`deployment.install-command-created`, `deployment.install-command-revealed`, `deployment.secret-revealed`,
`deployment.secret-regenerated`). Exceptions:
- unauthenticated requests (401: no actor) are not logged; a signed-in person without a usable account (403 "no account" /
  "account disabled") IS logged with that uid;
- `putUploadPart` (per-part PUTs) is not logged; the upload is logged at `createUpload` and `completeUpload` (`data.partCount`);
- unknown routes (404 no route / 405) have no op and are not logged.

**Catalog.** `AUDIT_ACTIONS` + `AUDIT_OPS` (op → default action + target type) in `types.ts`; the CLOUD_API `activities` column
lists `audit:<action>` for every audited op. Handlers refine the action through `c.audit`: `patchAccount` →
`account.access-changed` when the access rules change, else `account.updated`; `disableAccount` → `account.disabled` /
`account.enabled`. Credential actions (§21 #84): `createAccount` → `account.created` (`data.passwordSet = true`) +
`account.password-set`; `setAccountPassword` → `account.password-set`; `revealAccountPassword` → `account.password-revealed`;
`resetAccountPassword` → `account.reset-link-created`; `revealAccountResetLink` → `account.reset-link-revealed`;
`revealRegistryPassword` → `registry.password-revealed`. The legacy action `account.password-reset` stays in the catalog for
old entries only. System entries (actor `"system"`, op `system:<job>`): `image.import-ready` / `image.import-failed` (import
trigger, with the diff of the image doc), `system.daily-cleanup` (one summary entry per daily run, data = counts; a failed
run writes outcome error), `system.migration` (op `system:migration`, one entry per `--apply` run of a data migration script,
e.g. `backend/scripts/migrate-v2.ts`; data = `{ migration, dryRun, changes }` + the id → slug / tag → version mapping).
Icons: `uploadIcon` → `icon.uploaded` (also when the bytes existed: `data.existing = true`), `deleteIcon` → `icon.deleted`
(target type `icon`, target `icons/{iconId}`).

**Entry fields.** `op` (CLOUD_API op), `actor` (uid / `system`), `actorEmail`, `action`, `outcome` (`ok` / `error`), `status`
(HTTP), `errorCode` + `errorMessage` (≤ 500 chars), `targetType`, `target` (document path: `registries/{id}`,
`services/{serviceSlug}`, `services/{serviceSlug}/images/{version}`, `icons/{iconId}`, `notFoundPages/{id}`, `templates/{id}`, `tags/{slug}`, `accounts/{uid}`,
`deployments/{id}`, `uploads/{id}`, `blobs/sha256:…`, `system/daily`; a failed create without an id = the collection, e.g.
`templates`), `requestId` (= `X-Request-Id`), `ip`, `userAgent` (≤ 300), `changes`, `changesTotal`, `data`.

**Before / after.** The router maps op + params to the target document, reads it before the handler and again after it
(codecs applied, `_` fields removed) and stores `auditDiff(before, after)` (`shared/src/audit.ts`): objects recurse by key, lists
by an identity key (siteSlug, variableSlug, secretSlug, fileSlug, serviceSlug, routeId, hostname, name, tagSlug, …; else by index), paths like
`sites[app].containers[redis].env.FOO`; `updatedAt` / `updatedBy` / `cloudActivitySeq` are left out. Create = one change per
top-level field of the created doc (before null); delete = the deleted doc (after null). Caps: 200 changes (`changesTotal` =
the real count) and 2 KB per value (larger values become `{ $truncated: true, bytes, preview }`).

**Redaction (always, in `changes` and `data`).** A value is secret when its key matches
`/password|secret|token|accessKey|privateKey|resetLink/i` (identifier keys excluded: …Slug(s), …Id(s), …Version, …At, …By, …Count,
…Fingerprint, `tokenHash`), when it is `value` / `plainValue` of a `secrets` list item, or `value` of an object with
`secret: true`. Secrets are stored as `"***"` (null / "" stay visible); a changed secret is `{ path, before: "***", after: "***",
secret: true }` (the diff compares raw values, then masks). Every non-identifier leaf inside an object stored under a
secret-named key is masked too (e.g. `lastSetPassword.value`, `lastResetLink.value`; `setAt` / `setBy` stay visible).
Passwords, reset links, access keys and install commands are never logged — neither in audit `data` / `changes` nor in
activity data.

**Reliability.** The audit write is awaited after the handler; a failed read or write is logged with `logger.error` and never
changes the answer.

**Storage.** `changes` / `data` are JSON strings (`_changes`, `_data`; Firestore rejects nested arrays) and `_actionKeys` holds
the action and its prefixes (`template.`, `template.updated`) for prefix filters.

**`GET /v1/audit-log`** (accounts.admin) — newest first (`ts` desc), `limit` default 100 / max 500, opaque `cursor`. Filters
(`AuditLogQuery`): `action` (exact or a prefix ending in "."), `actor` (uid, `system`, or an e-mail when it contains "@",
case-insensitive), `targetType`, `target` (exact path), `outcome`, `from` / `to` (ISO, inclusive). ONE equality filter runs in
Firestore (priority target > actor > action > targetType > outcome; indexes `(field, ts desc)` and `(_actionKeys
array-contains, ts desc)`) plus the `ts` range; the others are applied in memory with over-fetch. Bad values → 422 invalid.

## 7. Agent local API (`/_opm/api/*`)

Served on the edge (every host), on agentGuiAliasPort and on agentPort. Auth kinds: `public`, `session` (cookie
`opm_session_<siteSlug>`), `session+actor` (cookie + `Authorization: Actor <token>`). GET endpoints with `{siteSlug}` accept `*`
(merged over the mesh). Generated from `shared/src/agent-api.ts`.

| op | method | path | auth | permission | request body | response | errors (+ common) | activities | behaviour |
|---|---|---|---|---|---|---|---|---|---|
| `healthz` | GET | `/_opm/healthz` | public | — | — | text  | — | — | "ok" (text/plain). |
| `health` | GET | `/_opm/api/health` | public | — | — | `AgentHealthResponse` (503 with the same body while not ok) | — | — | §21 #91 liveness + readiness: ok (store open, listeners bound, startup finished), version, schema, pid, startedAt, cloud { connected, route, lastContactAt }, docker, socketActivated, checks. Used by the install script, the upgrade engine, the watcher and the GUI restart overlay. |
| `info` | GET | `/_opm/api/info` | public | — | — | `AgentInfoResponse`  | — | — | Deployment + site identity for the login page (+ `consoleUrl`, `apiBase`, `deploymentLabel` for console links). |
| `login` | POST | `/_opm/api/login` | public | — | `LoginRequest` | `LoginResponse`  | invalid-key, rate-limited, unavailable | login.succeeded, login.failed | Check the access key (argon2id) → Set-Cookie opm_session_<siteSlug> (6 h). unavailable = the site has no access-key hash yet. |
| `logout` | POST | `/_opm/api/logout` | session | — | — | `{ loggedOut: true }`  | — | logout | End the session; clear the cookie. |
| `session` | GET | `/_opm/api/session` | session | — | — | `SessionResponse`  | session-expired | — | Current session. |
| `declareActor` | POST | `/_opm/api/actor` | session | — | `ActorRequest` | `ActorResponse`  | invalid, rate-limited | actor.declared | "Who is working right now?" → actor token (exp = session expiry). |
| `recentActors` | GET | `/_opm/api/actors/recent` · query `{ limit?: number }` | session | — | — | `RecentActorsResponse`  | — | — | Up to 10 most recently declared emails of the deployment. |
| `overview` | GET | `/_opm/api/overview` | session | — | — | `OverviewResponse`  | — | — | All sites (tiles), mesh link matrix, cloud route, lock. |
| `config` | GET | `/_opm/api/config` | session | — | — | `AgentConfigResponse`  | — | — | Head config (secrets masked) + catalog. |
| `listRevisions` | GET | `/_opm/api/revisions` · query `{ limit?: number }` | session | — | — | `ListResponse<RevisionSummary>`  | — | — | Local revision chain, newest first. |
| `getRevision` | GET | `/_opm/api/revisions/{revId}` | session | — | — | `RevisionView`  | not-found | — | One revision (masked). |
| `revealSecret` | POST | `/_opm/api/secrets/{secretSlug}/reveal` | session+actor | — | — | `RevealSecretResponse`  | not-found, rate-limited | secret.revealed | Head secret value (RevealValue 60 s; hideAfterSec 60; Cache-Control no-store; 30/min per session → 429 rate-limited). |
| `getFileContent` | GET | `/_opm/api/files/{fileSlug}/content` | session | — | — | bytes (200, Range) or 302  | not-found | — | Head content of a deployment file. |
| `uploadBlob` | POST | `/_opm/api/blobs` | session+actor | — | raw bytes | `BlobRef` (201) | too-large | — | Store new file content locally (≤ 50 MiB) for a local proposal; it reaches peers by smart distribution. |
| `previewLocalProposal` | POST | `/_opm/api/proposals/local/preview` | session+actor | — | `LocalProposalRequest` | `LocalProposalPreviewResponse`  | stale, invalid | — | Diff + validation (incl. local-edit rules) without creating anything. |
| `createLocalProposal` | POST | `/_opm/api/proposals/local` | session+actor | — | `LocalProposalRequest` | `LocalProposalResponse` (201) | stale, invalid, locked | proposal.created | Revision (origin agent) + proposal; pushed to every reachable peer and to the cloud. |
| `listProposals` | GET | `/_opm/api/proposals` · query `{ limit?: number }` | session | — | — | `ListResponse<ProposalListItem>`  | — | — | Proposals (open first, then the 20 newest terminal ones). |
| `getProposal` | GET | `/_opm/api/proposals/{revId}` | session | — | — | `ProposalDetailResponse`  | not-found | — | Diff vs head, staging per site, gate. |
| `restage` | POST | `/_opm/api/proposals/{revId}/restage` | session+actor | — | — | `{ ok: true }`  | not-found, conflict | — | Run staging again on every site where it failed (conflict when the proposal is terminal). |
| `approveStart` | POST | `/_opm/api/proposals/{revId}/approve/start` | session+actor | — | — | `ApproveStartResponse` (201) | not-found, gate-closed, locked | — | Gate check → intent; the confirm is possible after notBefore (60 s). |
| `approve` | POST | `/_opm/api/proposals/{revId}/approve` | session+actor | — | `ApproveRequest` | `ApproveResponse` (201) | not-found, invalid, gate-closed, locked, conflict | proposal.approved, update.started | typed must be "ACCEPT", now ≥ notBefore, gate open again → this agent coordinates the update. |
| `cancelIntent` | DELETE | `/_opm/api/intents/{intentId}` | session+actor | — | — | `{ cancelled: true }`  | not-found | — | Cancel a pending approve / rollback intent. |
| `getIntent` | GET | `/_opm/api/intents/{intentId}` | session | — | — | `IntentProgress`  | not-found | — | §21 #95: the authoritative wait of a pending intent of THIS session (effective / remaining s, speed, ready). |
| `intentSpeed` | POST | `/_opm/api/intents/{intentId}/speed` | session+actor | — | `IntentSpeedRequest` | `IntentProgress`  | not-found, invalid | — | §21 #95 hold-to-fast-forward: speed 1 \| 2 \| 4 from now (agent clock); heartbeat every 500 ms while > 1, else ×1 after 1.5 s. |
| `siteIntent` / `siteIntentSpeed` / `siteCancelIntent` | GET / POST …/speed / DELETE | `/_opm/api/sites/{siteSlug}/intents/{intentId}` | session / session+actor | — | — / `IntentSpeedRequest` / — | `IntentProgress` / `{ cancelled: true }` | not-found, invalid, unavailable | — | §21 #95: the same for an intent held by that site (volume reset / empty / restore; forwarded over the mesh). |
| `listVolumeOps` / `getVolumeOp` | GET | `/_opm/api/sites/{siteSlug}/volume-ops[/{opId}]` | session | — | — | `ListResponse<VolumeOp>` / `VolumeOp` | not-found, unavailable | — | §21 #96: volume operations of the site (running first, then the 50 newest) / one (the GUI polls it). |
| `emptyVolumeStart` / `emptyVolume` | POST | `/_opm/api/sites/{siteSlug}/volumes/{volumeSlug}/empty[/start]` | session+actor | — | — / `VolumeEmptyRequest` | `ApproveStartResponse` (201) / `VolumeOp` (202) | not-found, invalid, locked, conflict, rate-limited, unavailable | volume.emptied, volume.op-failed, snapshot.created | §21 #96: intent volume-empty (60 s, ⏩) → typed = the volume slug → stop users (reverse start order), optional snapshot, delete the contents (folder / file, owner, mode stay), start users. |
| `copyVolume` | POST | `/_opm/api/sites/{siteSlug}/volumes/{volumeSlug}/copy` | session+actor | — | `VolumeCopyRequest` | `VolumeOp` (202) | not-found, invalid, locked, conflict, rate-limited, unavailable | volume.copied, volume.op-failed | §21 #96: to a safe host path (default `<agentHome>/volume-copies/<volume>-<yyyymmdd-hhmmss>`) or into another declared volume (empty, or overwrite = its slug). |
| `uploadVolumeRestore` / `restoreVolumeStart` / `restoreVolume` | POST | `/_opm/api/sites/{siteSlug}/volumes/{volumeSlug}/restore/upload` · `/restore/start` · `/restore` | session+actor | — | raw .tar.gz / — / `VolumeRestoreRequest` | `VolumeRestoreUpload` (201) / `ApproveStartResponse` (201) / `VolumeOp` (202) | not-found, invalid, too-large, locked, conflict, rate-limited, unavailable | volume.restored, volume.op-failed, snapshot.created | §21 #96: from a copy or a checked .tar.gz (≤ 2 GiB, kept 1 h): typed slug → stop users, snapshot of the volume, replace contents, start users. |
| `snapshotVolume` | POST | `/_opm/api/sites/{siteSlug}/volumes/{volumeSlug}/snapshot` | session+actor | — | `VolumeSnapshotRequest` | `VolumeSnapshotResponse` (201) | not-found, locked, conflict, rate-limited, unavailable | snapshot.created | §21 #96: a manual snapshot of only this volume on this site. |
| `listVolumeCopies` / `deleteVolumeCopy` | GET / POST …/{copyId}/delete | `/_opm/api/sites/{siteSlug}/volume-copies` | session / session+actor | — | — / `DeleteVolumeCopyRequest` | `ListResponse<VolumeCopy>` / `Deleted` | not-found, invalid, conflict, unavailable | volume.copy-deleted | §21 #96: copies made by Copy to path (size, date, exists) / delete one (confirm = copyId). |
| `currentUpdate` | GET | `/_opm/api/updates/current` | session | — | — | `{ run: UpdateRun \| null }`  | — | — | The running update (any coordinator), or the last finished one of the last 10 min. |
| `getUpdate` | GET | `/_opm/api/updates/{updateId}` | session | — | — | `UpdateRun`  | not-found | — | One update run. |
| `terminateUpdate` | POST | `/_opm/api/updates/{updateId}/terminate` | session+actor | `TerminateUpdateRequest` | — | `TerminateUpdateResponse` | not-found | update.terminated, update.recovered, agent.restarted | §21 #110: stop the update on every site of its plan now (any site; "current" = the one this site holds); rollback / restartAgent options. Never refused. |
| `abandonUpdate` | POST | `/_opm/api/updates/{updateId}/abandon` | session+actor | — | — | `{ ok: true }`  | not-found, conflict | update.abandoned | Only when the coordinator has been unreachable for 10 min: roll THIS site back to the pre-update snapshot and unlock it. |
| `listSnapshots` | GET | `/_opm/api/snapshots` | session | — | — | `ListResponse<SnapshotSummary>`  | — | — | Snapshots of the deployment (merged over the mesh). |
| `createSnapshot` | POST | `/_opm/api/snapshots` | session+actor | — | `CreateSnapshotRequest` | `CreateSnapshotResponse` (202) | locked, gate-closed, invalid | snapshot.created | Manual snapshot on every site of the head config (all must be reachable). |
| `getSnapshot` | GET | `/_opm/api/snapshots/{snapshotId}` | session | — | — | `SnapshotDetailResponse`  | not-found | — | Snapshot detail (masked). |
| `patchSnapshot` | PATCH | `/_opm/api/snapshots/{snapshotId}` | session+actor | — | `PatchSnapshotRequest` | `{ ok: true }`  | not-found, invalid | — | Edit the note (all sites). |
| `deleteSnapshot` | DELETE | `/_opm/api/snapshots/{snapshotId}` | session+actor | — | — | `{ ok: true }`  | not-found, conflict | snapshot.deleted | Delete on all reachable sites (conflict: the pre-update snapshot of a running update). |
| `downloadSnapshot` | GET | `/_opm/api/snapshots/{snapshotId}/download` · query `{ includeImages?: boolean }` | session | — | — | bytes (200, Range) or 302  | not-found, unavailable | snapshot.downloaded | .opmsnap (tar) of every site part; unavailable when a part is on an unreachable site. |
| `importSnapshot` | POST | `/_opm/api/snapshots/import` | session+actor | — | raw bytes | `SnapshotSummary` (201) | invalid, conflict, too-large | snapshot.imported | Upload .opmsnap; verify signature + every part; distribute site parts to peers. |
| `rollbackPlan` | GET | `/_opm/api/snapshots/{snapshotId}/plan` | session | — | — | `RollbackPlanResponse`  | not-found | — | What a rollback changes + the gate. |
| `rollbackStart` | POST | `/_opm/api/snapshots/{snapshotId}/rollback/start` | session+actor | — | — | `ApproveStartResponse` (201) | not-found, gate-closed, locked | — | Gate → intent (60 s). |
| `rollback` | POST | `/_opm/api/snapshots/{snapshotId}/rollback` | session+actor | — | `ApproveRequest` | `ApproveResponse` (201) | not-found, invalid, gate-closed, locked | snapshot.rolled-back, update.started | ACCEPT → rollback update (pre-update snapshot first). |
| `siteStatus` | GET | `/_opm/api/sites/{siteSlug}/status` | session | — | — | `ListResponse<SiteStatus>`  | not-found | — | Containers, host, uptime, certs (one item per site). |
| `siteVolumes` | GET | `/_opm/api/sites/{siteSlug}/volumes` | session | — | — | `SiteVolumesResponse`  | not-found, unavailable | — | Declared volumes of the head config (+ orphan directories) with size on disk, backup flag, kind, seed, seeded, users, last snapshot; host-path mounts (never backed up). |
| `resetVolumeStart` | POST | `/_opm/api/sites/{siteSlug}/volumes/{volumeSlug}/reset/start` | session+actor | — | — | `ApproveStartResponse` (201) | not-found, invalid, gate-closed, locked | — | Round E: intent (kind `volume-reset`, 60 s) to reset a volume of THIS site to its starter content (invalid: no seed). |
| `resetVolume` | POST | `/_opm/api/sites/{siteSlug}/volumes/{volumeSlug}/reset` | session+actor | — | `ApproveRequest` | `ResetVolumeResponse` (201) | not-found, invalid, gate-closed, locked | snapshot.created, volume.reset-to-seed, volume.seeded | Round E: typed ACCEPT ≥ notBefore → manual snapshot of the volume, stop its containers, wipe, re-seed, start them again. |
| `containerLogs` | GET | `/_opm/api/sites/{siteSlug}/containers/{containerSlug}/logs` · query `{ tail?: number; q?: string; since?: string }` | session | — | — | `ContainerLogsResponse`  | not-found, unavailable | — | Container log lines (tail default 200, max 5000; q = substring filter). |
| `siteActivities` | GET | `/_opm/api/sites/{siteSlug}/activities` · query `{ cursor?: string; limit?: number; type?: string; actor?: string }` | session | — | — | `PageResponse<Activity>`  | not-found, unavailable | — | Activity log newest first (* = merged by ts). |
| `downloadActivities` | GET | `/_opm/api/sites/{siteSlug}/activities/download` · query `DownloadActivitiesQuery` | session+actor | — | — | bytes (200, Range) or 302  | not-found, unavailable | debug.downloaded | .opmlog bundle signed by THIS site. |
| `siteDebug` | GET | `/_opm/api/sites/{siteSlug}/debug` · query `{ level?: string; module?: string; since?: string; limit?: number; q?: string }` | session | — | — | `ListResponse<DebugLine>`  | not-found, unavailable | — | Debug log lines newest first (limit default 500, max 5000). |
| `listCerts` | GET | `/_opm/api/certs` · query `{ site?: string }` | session | — | — | `ListResponse<CertStatus>`  | — | — | Edge certificate status (site default this site, * = all). |
| `renewCert` | POST | `/_opm/api/certs/{hostname}/renew` · query `{ site?: string }` | session+actor | — | — | `CertRenewResponse`  | not-found, invalid, rate-limited | cert.renewed, cert.failed | Renew one host certificate now. |
| `localCa` | GET | `/_opm/api/certs/local-ca.crt` · query `{ site?: string }` | public | — | — | bytes (200, Range) or 302  | not-found | cert.local-ca-downloaded | PEM root of the site local CA. |
| `listSiteTransfers` | GET | `/_opm/api/sites/{siteSlug}/transfers` · query `{ direction: TransferDirection }` | session | — | — | `ListResponse<Transfer>`  | not-found, unavailable | — | to-site = files from the cloud (receive); to-cloud = files this site sent. |
| `sendFromBrowser` | POST | `/_opm/api/sites/{siteSlug}/transfers/upload` · query `{ fileName: string; note?: string }` | session+actor | — | raw bytes | `Transfer` (202) | invalid, too-large, unavailable | transfer.sent | Browser file → stored on that site → queued → uploaded to the cloud in the background (≤ 2 GiB; returns at once, `delivery.phase` queued, §21 #100). |
| `sendFromPath` | POST | `/_opm/api/sites/{siteSlug}/transfers/from-path` | session+actor | — | `SendFromPathRequest` | `Transfer` (202) | invalid, not-found, forbidden, too-large | transfer.sent | A regular file on that server → stored on the site → queued → cloud (§21 #100 safety policy: forbidden). |
| `hostFs` | GET | `/_opm/api/sites/{siteSlug}/transfers/fs` · query `{ path?: string; hidden?: boolean }` | session | — | — | `HostFsListResponse` | invalid, not-found, forbidden, unavailable | — | §21 #100: one directory level of the server filesystem (default `/`) for the Send › Path picker; read-only; not logged. |
| `saveTransfer` | POST | `/_opm/api/sites/{siteSlug}/transfers/{transferId}/save` | session+actor | — | `SaveTransferRequest` | `Transfer`  | not-found, invalid, conflict, unavailable | transfer.saved | Write a received file to an absolute path (exists → conflict; mode 0640, owner root). |
| `downloadSiteTransfer` | GET | `/_opm/api/sites/{siteSlug}/transfers/{transferId}/download` | session | — | — | bytes (200, Range) or 302  | not-found, unavailable | transfer.received | Browser download of a received (or sent) file. |
| `tunnelState` | GET | `/_opm/api/tunnel` | session | — | — | `TunnelStateResponse`  | — | — | The active lease of the deployment, if any. |
| `tunnelStart` | POST | `/_opm/api/tunnel/start` | session+actor | — | `TunnelStartRequest` | `TunnelLeaseResponse` (201) | conflict | internet.tunnel.started | Start a lease (conflict details.active = the active lease). |
| `tunnelStop` | POST | `/_opm/api/tunnel/stop` | session+actor | — | `{ leaseId: string }` | `TunnelLeaseResponse`  | not-found, conflict | internet.tunnel.stopped | Stop the own lease. |
| `tunnelRevoke` | POST | `/_opm/api/tunnel/revoke` | session+actor | — | `{ leaseId: string }` | `TunnelLeaseResponse`  | not-found, conflict | internet.tunnel.revoked | Revoke another tab lease. |
| `tunnelNext` | GET | `/_opm/api/tunnel/next` · query `{ leaseId: string; max?: number }` | session+actor | — | — | `TunnelNextResponse`  | conflict | — | Long-poll ≤ 25 s, ≤ 4 requests. |
| `tunnelResult` | POST | `/_opm/api/tunnel/result` | session+actor | — | `TunnelResult` | `{ accepted: boolean }`  | not-found, conflict, too-large | — | Result for a reqId (idempotent). |
| `tunnelChunk` | POST | `/_opm/api/tunnel/result/{reqId}/chunk` · query `{ leaseId: string; index: number }` | session+actor | — | raw bytes | `{ received: number }`  | not-found, invalid | — | One ≤ 4 MiB part of a large body, in order. |
| `tunnelComplete` | POST | `/_opm/api/tunnel/result/{reqId}/complete` | session+actor | — | `TunnelCompleteRequest` | `{ accepted: boolean }`  | not-found, invalid | — | Join parts + sha256 check. |

### 7.1 GUI static routes

`/_opm/` → 302 `/_opm/app`. `/_opm/login`, `/_opm/app` and `/_opm/app/**` → the SPA `index.html` (client routing);
`/_opm/assets/**` → embedded files (`Cache-Control: public, max-age=31536000, immutable` for hashed names). Any other
`/_opm/…` → the agent 404 page (HTML, status 404) — never the site not-found page. SPA rule: no session → the SPA redirects
to `/_opm/login?next=<path>`.

### 7.2 Behaviour details

- `info` is public (login page). `surface` = which listener answered. `consoleUrl` = the console origin the cloud sent last
  (enroll answer / cloud view `consoleUrl`; else agent.json `consoleUrl`; null when unknown), `apiBase` = agent.json,
  `deploymentLabel` = cloud view `deploymentLabel` (Deployment.label) — the GUI links "open in the cloud console".
- `overview`: `sites` = union of head config sites, enrolled peers (cloud view) and self; for each the last ping (§9.2).
  `links[a][b]`: rtt ms if a reached b in the last 30 s (self row from own pings; other rows from each peer's ping `peersSeen`).
- `config` / `revisions` / `getRevision`: local store; secrets masked.
- `revealSecret`: the HEAD secret value, any number of times (§21 #84): `{ value, hideAfterSec: 60 }`, `Cache-Control:
  no-store`, 30 reveals per session per minute (`REVEAL_RATE_PER_MINUTE`) → 429 `rate-limited` `details { retryAfterSec }` +
  `Retry-After`; local activity `secret.revealed { secretSlug, actor, where: 'agent', siteSlug }`. The agent never holds the
  access-key plaintext (only `kv.keyHash`), so there is no access-key reveal on the agent.
> **SUPERSEDED by §21 #104 (agent ≥ 0.14, cloud with branches)** — kept for agents < 0.14 (compat proposals) and as history. Current model: docs-v2/branches-commits.md.

- `previewLocalProposal` / `createLocalProposal`: `baseRevId` must equal the head (else 409 `stale`); the body config is
  `ConfigInput` (secrets with plainValue semantics); the agent builds the full Config (keep / set / clear values; generateNow
  uses the generate rule), runs `checkLocalEdit(head, new)` + `validateConfig(new, mode 'agent', catalog = head catalog,
  notFoundPageIds = head catalog keys)`; no changes → invalid `V_NO_CHANGE`. Create: lock held → 409 `locked`; revision
  `{ origin 'agent', actor email, parent head, seq head.seq+1, catalog = head catalog (unchanged), kind change }`, proposal
  `published` with sites = head sites; older open proposals → superseded (locally and on peers by the same rule); push to
  every reachable peer (`POST /_opm/peer/proposals`) and register in the cloud (`POST /v1/agent/proposals`, queued until a
  cloud route exists); activity `proposal.created { revId, parentRevId, seq, summary, sections }`. New file contents of a
  local proposal must be uploaded first with `uploadBlob` (stored as a blob of kind file, then distributed by §9.7).
- `getProposal`: changes = diffConfigs(head, proposal); staging = this site's detail + peers' (from ping; detail of peers via
  `GET /_opm/peer/site/proposals/{revId}/staging`); gate (§14.4).
- `approveStart`: gate must be open (else 409 `gate-closed` with `details.gate`), no lock (else `locked`) → intent
  `{ notBefore = now + OPM_APPROVE_WAIT_SEC (default 60, env min 1 for tests), expiresAt = notBefore + 600 s }`.
- `approve`: intent exists, unused, same session, not expired (else invalid), `now ≥ notBefore` (else invalid "wait"),
  `typed === "ACCEPT"` exactly (else invalid), gate open again (else gate-closed) → marks intent used, starts the update with
  THIS agent as coordinator (§14.5) and returns the run. Activities `proposal.approved { revId, approver }`, `update.started`.
- `cancelIntent`: delete the intent (the 60 s restarts on the next Approve).
- **Intent wait = effective seconds (§21 #95).** Every intent wait (approve, rollback, volume reset / empty / restore) counts
  effective = Σ speed × real dt on the agent's own clock (in memory, written through to SQLite `intent_waits`; an intent
  without a row = created at notBefore − OPM_APPROVE_WAIT_SEC at ×1). `intentSpeed {speed}` (1 \| 2 \| 4 only, else 422)
  applies from the moment the agent receives it until the next speed message or until 1.5 s pass without one (then ×1;
  a lost release never keeps it fast); the GUI sends a heartbeat every 500 ms while > 1. The confirm (`approve`, `rollback`,
  `resetVolume`, `emptyVolume`, `restoreVolume`) needs effective ≥ waitSec (60 or OPM_APPROVE_WAIT_SEC) — else 422 "wait n s"
  — so the minimum real wait is waitSec / 4. `notBefore` stays the latest ready time (all at ×1); `ApproveIntent.waitSec`
  is new. When speed > 1 was used, `FastForwardUse { usedBy, maxSpeed, realSeconds (start → wait complete), effectiveSeconds }`
  is written to `proposal.approved` / `update.started` (approve + rollback), `volume.reset-to-seed`, `volume.emptied`,
  `volume.restored` data (`fastForward`, summary suffix "· fast-forwarded (×4, 18 s real)"), `UpdateRun.fastForward`,
  `Proposal.approvedFastForward` (agent-local) and `VolumeOp.fastForward`.
- **Volume manager (§21 #96).** One operation per volume at a time (`conflict` while one runs; `locked` while an update
  runs — and `startUpdate` answers `locked` while a volume operation runs). Steps `stop → snapshot → copy | empty | restore →
  start`; the containers that mount the volume and RUN (state running / restarting) are stopped in REVERSE start order
  (§15.5 `startPlan`) and started again in start order, also after a failure; history in `<agentHome>/volume-ops.json`
  (50); an operation interrupted by an agent stop is failed at start (`volume.op-failed`) and its stopped containers are
  started. Copy targets: absolute or `./` (agentHome); refused: `/`, /bin /boot /dev /etc /lib* /proc /root/.ssh /run /sbin
  /sys /usr /var/lib/docker /var/run, the agent home except `volume-copies/<name>`, any declared volume path (inside or
  around), symlinked ancestors resolving there, an existing non-empty target; a failed copy removes its half copy. Copy
  keeps (preserve) owner, mode, times, xattrs (Linux); symlinks are copied as links, special files skipped (warning).
  Copies are listed in kv `volumeCopies` (copyId, path, createdBy, opId) with their live size. Restore archives: only
  regular files, folders and symlinks, no absolute / `..` names, extraction never writes through a symlink; a file volume
  takes an archive with exactly one file. Empty = remove every entry (a file volume: truncate to 0). Rate: 6 actions per
  session per minute (429 + Retry-After). Another site: forwarded like every per-site route; the forwarding site writes
  `volume.action-forwarded { siteSlug, forwardedTo, method, action, status }`, the target the action's own activity.
- `abandonUpdate`: allowed only when this site holds a lock whose coordinator has not contacted it for 10 min; restores the
  pre-update snapshot locally if the APPLY step had changed anything, unlocks, activity `update.abandoned`.
- Snapshots: `createSnapshot` needs all head-config sites reachable and no lock (else gate-closed / locked); the agent
  generates the snapshotId and asks every site (`POST /_opm/peer/snapshots`); 202 + poll `getSnapshot`. `downloadSnapshot`
  streams the `.opmsnap` (§13.1) built from its own part + peers' parts (`GET /_opm/peer/snapshots/{id}/part`); the response
  header `X-OPM-Warning: contains-secrets`. `importSnapshot`: verify (§13.1), store own part, PUT other parts to peers (a peer
  that is unreachable → its part is kept locally under `snapshots/<id>/pending/<slug>` and pushed later).
- `rollbackPlan`, `rollbackStart`, `rollback`: same gate + 60 s + ACCEPT; the target snapshot must be complete on every site
  of its config (else invalid); see §14.6.
- Per-site endpoints for another site are forwarded over the mesh (`/_opm/peer/site/<rest>`); `*` merges: status = list of
  all sites (unreachable ones `reachable: false` with their last cached data), activities = merge by ts desc, debug = merge by
  ts desc. Unreachable target (non-*) → 503 `unavailable`.
- `siteVolumes` (round D; agent GUI Backups page): `{siteSlug}` = one site (the response describes one site; the GUI calls it
  once per site; another site → peer `site/<rest>` mirror, unreachable → 503 `unavailable`). `SiteVolumesResponse { siteSlug, measuredAt, agentHome, volumes:
  SiteVolumeStatus[], hostPaths[] }` (`agentHome` = this site's data dir; "./" locations are relative to it). `volumes` =
  every volume DECLARED in the head config of that site (in declared order) followed by every orphan directory under
  `<agentHome>/volumes/` that the head no longer declares (`declared: false`, `backup: false`, label "", location auto,
  `usedBy` []). `SiteVolumeStatus { volumeSlug, label, description, backup, declared, path (the absolute directory: location
  resolved against agentHome), location (as configured), usedBy[{ containerSlug, imageVolume,
  mountPath (resolved), readOnly }], sizeBytes (apparent bytes of the directory, measured at most every 60 s; null = no
  directory yet), lastSnapshot { snapshotId, createdAt, sizeBytes } | null (newest snapshot of the site containing it), kind
  (as configured; orphans dir), seed (as configured), seeded { at, source, entries } | null (when the starter content was
  applied on this site; null = never / started empty / created before round E) }`.
- `resetVolumeStart` / `resetVolume` (round E, §2.2 "Starter content"): `{siteSlug}` must be THIS site (the agent GUI offers it
  only there); unknown / undeclared volume → not-found; a volume without `seed` → `invalid`; an update running → `locked`
  (the agent answers 422 / 423). Start creates an `ApproveIntent { kind: 'volume-reset', volume: { siteSlug, volumeSlug } }` (notBefore = now +
  60 s, same session; `cancelIntent` cancels it). Reset: intent unused, same session, `now ≥ notBefore`, `typed === "ACCEPT"`
  (else invalid) → manual snapshot containing only that volume (note "before reset of <volumeSlug>") → stop the running
  containers that mount it → wipe (dir: remove every entry; file: truncate) → seed (§2.2) → start them →
  `ResetVolumeResponse { siteSlug, volumeSlug, snapshotId, entries }`.
  `hostPaths` = the host-path mounts of the head config `{ containerSlug, mountPath, path, readOnly }` — never backed up.
  No head → `volumes` [] and `hostPaths` [].
- `containerLogs`: `{containerSlug}` = the container instance (found by the labels `opm.site` + `opm.container`, §15.1; unknown →
  not-found); `ContainerLogsResponse.containerSlug`. Docker logs API (`timestamps=1`, `tail`), filtered by `q` (case-insensitive substring); secret-tainted env
  values are NOT redacted in container logs (they are the application's output).
- `downloadActivities`: builds `.opmlog` (§13.3) for the site (or `*` = every reachable site's entries, fetched via
  `GET /_opm/peer/site/activities/export?from&to`), signed by THIS site; `includeDebug` adds `debug/<site>.jsonl` (secrets never
  present) + activity `debug.downloaded`.
- Transfers (store-and-forward both ways, §21 #100): `sendFromBrowser` streams the body to `<agentHome>/transfers/<tmp>` on
  the target site (forwarded raw over the mesh), computes sha256, stores it as `local-<id>` (status uploading, delivery
  `queued`) and returns 202 AT ONCE — the request never waits for the cloud. The transfers loop (every 15 s and on every cloud
  view) creates the cloud transfer (`POST /v1/agent/transfers`, the file is renamed to the cloud id) and uploads the missing
  parts (resumable: `GET /v1/agent/uploads/{id}` lists the parts the cloud has), direct or relay (never the browser tunnel:
  its request bodies are ≤ 4 MiB and a part is 8 MiB). A failed attempt backs off 15 s × 2ⁿ (max 5 min; a 4xx refusal other
  than 401 / 408 / 429 waits the maximum at once); an expired / failed cloud upload (404 / 410) starts a new cloud transfer
  from the same local file. Nothing needs anyone on the receiving end: the cloud stores the parts and the transfer becomes
  `ready` on complete. `delivery` (agent API only) = `{ phase queued | uploading | stored | pending | downloading | on-site,
  bytesDone, attempts, lastError, lastAttemptAt, nextAttemptAt }` from memory (after a restart bytesDone is re-read on the
  next attempt). `sendFromPath`: path must be absolute and clean, a regular file (a folder → 422 "folders cannot be sent"),
  readable by the agent (else 403), ≤ 2 GiB, and allowed by the safety policy below (403 forbidden + `details.reason`); the
  file is copied first (the copy is what is sent; `sourcePath` is kept on the transfer, `transfer.sent` data
  `{ source: 'browser' | 'path', sourcePath? }`).
  **Server filesystem picker + safety policy (`hostFs`, §21 #100).** The agent is a host binary (systemd, root on a
  standard install; not a container), so `/` is the host's real root read with the agent's own user. `hostFs` lists ONE
  directory level: path absolute (default `/`, cleaned; NUL → 422); a file path → 422 `details.type = "file"`; missing → 404;
  not listable → 403 forbidden ("the agent (user X) cannot list …"). Entries are lstat'ed (symlinks are described with
  `linkTarget` + `targetType`, never followed for the type), directories first then by name (case-insensitive), hidden
  (`.name`) left out unless `hidden=true` (`hiddenCount`), at most 5000 entries (`truncated`; names read ≤ 50 000), each with
  size (files), mtime, mode, `readable` (access(2) R_OK, + X_OK for directories) and `blocked` (reason). Session only; reads
  are not activities. Sending is refused (403, checked on the requested path AND its resolved symlink target) for: the
  agent's own files — `<agentHome>` (site key, database, certificates, secrets, blobs, transfers), its agent.json and its
  install-token file; the kernel / runtime filesystems `/proc`, `/sys`, `/dev`, `/run`; `/etc/shadow`, `/etc/shadow-`,
  `/etc/gshadow`, `/etc/gshadow-`, `/etc/sudoers`, `/etc/security/opasswd`; SSH host private keys `/etc/ssh/ssh_host_*_key`;
  and anything that is not a regular file. Shared rule: `shared/src/transfers.ts hostFsBlockReason` = Go
  `agent/internal/agent/hostfs.go` (vectors `shared/testdata/hostfs-vectors.json`). Everything else readable is allowed
  (the operator already has the host shell on a standard install; the policy keeps machine secrets out of the cloud, where
  every deployment.transfers.read user could download them). `saveTransfer`:
  path absolute, parent dir exists, target must not exist (409 conflict), written via temp file + rename, mode 0640;
  reports `saved-on-server` to the cloud. To-site files are downloaded by the agent in the background as soon as the cloud
  view lists them — whenever the agent is next online, nobody needs the agent GUI open (stored `<agentHome>/transfers/<id>`,
  sha256-checked, resumable `.part`, same backoff) — and reported `fetched` (outbox) → cloud `fetchedAt` (§21 #100). A file
  already on the site that was never reported (downloaded by an agent < 0.11.2) is reported on the next cloud view.
- Certs: `renewCert` (acme: new order; internal: new leaf; files: re-read) → `cert.renewed` / `cert.failed`; mode none →
  invalid. `localCa`: PEM of the Caddy local CA root of that site (no internal host → not-found).
- Tunnel: §9.10.

### 7.4 Operator toolbox (`/_opm/api/sites/{siteSlug}/tools/*`, §21 #89)

Normative contract: `shared/src/agent-tools.ts` (`TOOLS_API`, `createToolsClient`, types). Go: `agent/internal/tools`
(handlers, WebSocket, PTY, transcripts, logs, netcheck, volumes, Docker housekeeping), `agent/internal/metrics` (sampler +
SQLite), `agent/internal/tools/accesslog` (Caddy module `opm_access_log`), `agent/internal/docker/tools.go` (Engine API
calls), mounted by `agent/internal/agent/tools_hook.go`. `{siteSlug}` = this site (local), another site (forwarded over the
mesh like every per-site route), or `*` for the merged GETs (`info`, `containers`, `metrics/summary`, `docker/images`,
`volumes`, `terminal/sessions`, `terminal/transcripts` → `{items}` with one entry per reachable site / the concatenated rows).

| op | method + path (under …/tools) | auth | activity | notes |
|---|---|---|---|---|
| toolsInfo | GET /info | S | — | enabled tools (agent.json switches), limits, agent user, Docker version, configPath |
| toolsContainers | GET /containers | S | — | OPM containers of the site + kept failed copies: state, health, restarts, OOM, manuallyStopped, expectedRunning, latest CPU/mem |
| restart / stop / startContainer | POST /containers/{ref}/restart\|stop\|start `{reason?}` | SA | container.restarted / stopped / started | 409 `locked` during an update; failed copies refused; stop → manuallyStopped |
| inspectContainer | GET /containers/{ref}/inspect | S | — | docker inspect, secret env values + every known secret value masked; docker HEALTHCHECK log + OPM check history |
| containerTop | GET /containers/{ref}/top | S | — | docker top; 409 when not running |
| containerEvents | GET /containers/{ref}/events?since | S | — | Docker events of the container, last 24 h (max 1000), newest first |
| logStream | GET /logs/containers?containers&stdout&stderr&since&until&tail&follow | S | — | SSE; tail default 500 / max 10000 per container; follow ≤ 6 h |
| logDownload | GET /logs/containers/download?… | S | — | `.log` attachment `<ts> <ref> <stream> <text>` (max 100000 lines per container) |
| agentLogStream | GET /logs/agent?level&q&tail&follow | S | — | SSE of the debug log (already redacted) |
| accessLogStream | GET /logs/access?q&tail&follow | S | — | SSE of `<agentHome>/logs/edge-access.log` |
| createTerminal | POST /terminal/sessions `{kind, containerSlug?, shell?, command?, confirm?, cols, rows}` | SA | (terminal.opened at attach) | 201 `{sessionId, ticket, expiresAt, wsPath}`; 403 tool disabled; 422 confirm ≠ site slug; 409 4 open |
| (WebSocket) | GET /terminal/ws?ticket | S + ticket | terminal.opened / terminal.closed | same origin only; ticket single-use, 60 s, bound to the GUI session (or the forwarding site) |
| terminalSessions | GET /terminal/sessions | S | — | open + reserved sessions |
| closeTerminal | POST /terminal/sessions/{id}/close | SA | terminal.closed (closedBy) | any operator may end any session |
| transcripts | GET /terminal/transcripts | S | — | 7 days |
| transcriptTicket → download | POST /terminal/transcripts/{id}/ticket?format=cast\|text → GET …/download?ticket | SA → S + ticket | — | asciicast v2 or plain text (ANSI removed) |
| metricsSummary | GET /metrics/summary | S | — | latest host + container samples, 60 × 1-min sparklines, alerts, thresholds, dbBytes |
| metricsSeries | GET /metrics/series?subject&range&metrics | S | — | subject host \| container:<ref> \| disk:<mount>; 1h = 15 s raw, 24h = 1 min, 7d = 15 min |
| toolsVolumes | GET /volumes | S | — | declared site volumes, host path, size + file count (walk ≤ 200k entries / 3 s, cached 60 s), users |
| volumeTree / volumeFile | GET /volumes/{slug}/tree?path · /file?path | S | — | read-only; one level, ≤ 2000 entries, sub-folder sizes within a 2 s budget; text ≤ 1 MiB (known secret values masked) |
| volumeDownloadTicket → download | POST /volumes/{slug}/download?path → GET …?ticket | SA → S + ticket | volume.downloaded | file (Range) or streamed tar.gz (symlinks stored, never followed) |
| writeVolumeFile | POST /volumes/{slug}/file `{path, text, baseMtime}` | SA | volume.file-written | §21 #96: create (baseMtime null; must not exist) / replace a text file ≤ 1 MiB (409 when changed since baseMtime); temp + rename, keeps mode + owner |
| uploadVolumeFile | POST /volumes/{slug}/upload?path&overwrite (raw) | SA | volume.file-uploaded | §21 #96: ≤ 2 GiB per file; missing folders created; 409 when it exists without overwrite |
| mkdirVolume / moveVolumeEntry / deleteVolumeEntries | POST /volumes/{slug}/mkdir `{path}` · /move `{from, to, overwrite?}` · /delete `{paths, confirm:"DELETE"}` | SA | volume.file-folder-created / -moved / -deleted | §21 #96: never the volume root, never through a symlink (a link is moved / deleted itself); file volumes: only their content (file / upload of "/") |
| netcheck | POST /netcheck `{from, check, target, timeoutSec}` | SA | tool.netcheck | dns / tcp / http / ping from the host (native Go, ping binary) or inside a container (busybox / coreutils tools: getent\|nslookup, nc\|bash /dev/tcp, curl\|wget, ping) |
| dockerImages | GET /docker/images | S | — | images with users (in use / unused / OPM / protected) + `docker system df` (cached 30 s) |
| pruneImages | POST /docker/prune-images `{typed:"PRUNE"}` | SA | docker.images-pruned | removes images no container uses, never the head revision's images; 409 `locked` during an update |

**Streams.** SSE events `meta` `{containers?, follow, tail}`, `line` (LogStreamLine `{c,s,t,x}` \| AgentLogLine \|
AccessLogLine), `error` `{message}`, `end` `{reason}`; a `: ping` comment every 15 s; flushed every 100 ms. Container logs:
the initial tail of every container is merged by timestamp, later lines pass through as they arrive; known secret values are
masked. WebSocket (one terminal per socket): binary frames = terminal bytes both ways; text frames = JSON control
(`resize`, `ping` → `pong`; server `ready`, `idle-warning {secondsLeft}`, `error`, `exit {code, reason}`). Host shell = a PTY
(no cgo: /dev/ptmx) running `<shell> -l` as `hostShell.user` (default: the agent's user, root in production; another user
needs the agent to run as root) with TERM=xterm-256color, OPM_SITE; container = `docker exec` with a TTY (`/bin/sh`,
`/bin/bash` or a custom argv ≤ 32 args) and resize via the Engine API.

**Over the mesh.** JSON calls are forwarded like every per-site route (writes need the actor; the target trusts the signed
`X-OPM-Actor`). SSE is copied with a flush per read. The terminal WebSocket is bridged: the browser's site checks the session
and same origin, opens a SIGNED upgrade request to `/_opm/peer/site/tools/terminal/ws?ticket` (the ticket was issued on the
target by the forwarded createTerminal, bound to the forwarding site), answers the browser's 101 and copies frames both
ways. Both sites write `terminal.opened` / `terminal.closed` (target: `via`, origin: `forwardedTo`).

**Limits.** 4 open + reserved terminals per site; idle timeout 30 min without input (warning 60 s before); rate limits per
session (or forwarding site) per minute: terminal 10, container actions 20, netcheck 30, downloads 20, prune 2 → 429
`rate-limited {retryAfterSec}` + Retry-After.

**Security model / threat model.**
- Every tools endpoint needs the session cookie; writes and sensitive reads (terminal, transcript / volume links, actions,
  netcheck, prune) need the actor token too. Peers are trusted only with a signed request; host shell and exec over the mesh
  require the actor identity in the forwarded create and are logged on both sites.
- Cross-site WebSocket hijacking: the upgrade is refused unless `Origin` equals the agent's own origin (CSP is unchanged: the
  agent GUI sets no CSP; if one is added, `connect-src 'self'` covers same-origin `ws:`/`wss:`). Browsers cannot attach the
  actor token to a WebSocket, so the WebSocket is authorised by the single-use ticket from an actor-authorised POST, bound to
  the same GUI session.
- A terminal is root on the server (host) or inside the container: it is opt-out per site (`tools.hostShell: false`,
  `tools.containerExec: false` in agent.json; the GUI hides the tool and says how to enable it), needs the typed site slug
  for the host shell, is limited (4 sessions, 30 min idle) and leaves an audit trail (activities with actor, target,
  duration, exit code; transcripts).
- Transcripts: OUTPUT only (keystrokes are not recorded; prompts do not echo passwords), asciicast v2 under
  `<agentHome>/transcripts/<id>.cast` + `.json` meta, 0600, kept 7 days and ≤ 200 MiB in total (≤ 50 MiB per session),
  NEVER uploaded (not in `.opmlog`, not in snapshots). Masking guarantee: every secret value the site knows (head config
  secrets + every secret-tainted value the driver rendered for its containers, ≥ 4 characters) is replaced by `••••••`
  before the bytes reach the disk, also when a value is split across two reads. Not masked: secrets the agent does not know
  (e.g. a password the application prints, a key read from a file in the container), encodings of a secret (base64, URL
  encoding) and values shorter than 4 characters — operators must treat transcripts as sensitive.
- Read-only volumes browser: paths are cleaned and must stay inside the volume; symlinks are listed, never followed; no
  upload / delete / edit (next step).
- Inspect / logs / volume text / netcheck output / top: known secret values masked; inspect also masks env values whose
  name looks secret (pass, secret, token, key, credential, private, auth, cookie, session, dsn, conn) or whose value comes
  from a secret. Downloads (logs .log, volume files / archives) are the operator's data: the volume download is logged.
- Netcheck targets are validated (host names / IPs, host:port, http(s) URLs) and passed to container tools as arguments,
  never interpolated into a shell string.

### 7.5 Agent self-management (`/_opm/api/sites/{siteSlug}/agent/*`, §21 #90)

Normative contract: `shared/src/agent-self.ts` (`AGENT_SELF_API`, `createAgentSelfClient`, `validateAgentSettings`,
`SUPPORT_BUNDLE_SECTIONS`, `uninstallCommands`) + types.ts (`AgentUpgradeProgress`, `ReleaseVerification`,
`AgentUpgradeRun`, `GUI_UPGRADE_MIN_VERSION` = 0.10.0, `AGENT_UPGRADE_HEALTH_SEC` = 60). Everything `opm-agent` does on the
command line is here, except `keygen` / `enroll` / `install` (pre-enrollment, CLI only). `{siteSlug}` = this site, another
site (forwarded over the mesh with the actor identity, like §7.4), or `*` for `agentSelf` (`{items}`: one per reachable
site). S = session, SA = session + actor.

| op | method + path | auth | ok | errors | activity | notes |
|---|---|---|---|---|---|---|
| agentSelf | GET …/agent | S | 200 | not-found, unavailable | — | `AgentSelfInfo`: version, build (go version, os/arch, commit, build time), pid, uptime, config path, data dir, binary + `opm-agent.prev` (version, sha256), ports, Docker, supervisor (systemd state + boot guard installed), enrollment (state, date, site key fingerprint `SHA256:<b64>`, re-enroll help), cloud link, pinned release, upgrade, `busy`, uninstall commands |
| checkRelease | POST …/agent/release/check | S | 200 | unavailable, invalid | — | `opm-agent verify-release` parity: signed `GET /v1/agent/release`, verify the Ed25519 signature of the sha256 hex (agent.json `releasePublicKey`, else the cloud's key); no download → `AgentSelfRelease` |
| startUpgrade | POST …/agent/upgrade `{version, confirm, force?}` | SA | 202 | invalid, stale, conflict, locked, unavailable, not-found | agent.upgrade-started / upgraded / upgrade-failed / rolled-back | the upgrade flow below → `AgentUpgradeProgress` |
| upgradeState | GET …/agent/upgrade | S | 200 | unavailable | — | `{current, history}` (≤ 10, newest first); survives the restart |
| restartAgent | POST …/agent/restart `{confirm, reason?}` | SA | 202 | invalid, locked, conflict, unavailable | agent.restarted | `systemctl restart --no-block opm-agent`, else graceful stop + `execve` of the same path and argv; containers keep running → `{restarting, via}` |
| agentConfig | GET …/agent/config | S | 200 | unavailable | — | `AgentConfigView`: path, `masked` agent.json, the safe `settings`, `writable` (+ reason), `restartPending` |
| patchAgentConfig | PATCH …/agent/config `{settings, confirm, restart?}` | SA | 200 | invalid, locked, conflict, forbidden | agent.config-changed (+ agent.restarted) | only `tools.hostShell`, `tools.containerExec`, `tools.devTools` (§21 #97), `containerNames`, `edgeHttpPort`, `guiAliasPort`; `validateAgentSettings` errors → 422 `invalid` with the issues; forbidden = file not writable |
| supportBundle | POST …/agent/support-bundle | SA | 200 | rate-limited, unavailable | agent.support-bundle-downloaded | single-use 5-min download link (`DownloadTicketResponse`) to `opm-support-<site>-<yyyymmdd-hhmmss>.tar.gz` |
| startUpgradeAll | POST /_opm/api/agent/upgrade-all `{version, confirm:"UPGRADE"}` | SA | 202 | invalid, stale, conflict, locked, unavailable | agent.upgrade-started (per site) | mesh run (below) → `{run}` |
| upgradeAllState | GET /_opm/api/agent/upgrade-all | S | 200 | — | — | the current / last run of this coordinator (`run.json`) |
| cancelUpgradeAll | POST /_opm/api/agent/upgrade-all/cancel | SA | 200 | conflict | — | the site upgrading now finishes; the rest = cancelled |

**Errors.** `locked` (409): an update / rollback / snapshot / restore runs (`AgentSelfInfo.busy`). `conflict` (409): an
upgrade runs (upgrade, restart, config save) or a mesh run is going. `stale` (409): `version` ≠ the pinned version (the pin
moved: reload). `invalid` (422): `confirm` ≠ the site slug (`"UPGRADE"` for upgrade-all), already on that version without
`force`, agent not enrolled, settings issues. `unavailable` (503): the cloud (direct or relay) cannot be reached for the
release / binary.

**Upgrade flow** (ONE code path for the GUI, a cloud request (§9.3 `agentUpgrades`), the mesh run and `opm-agent upgrade`;
`via` gui | cloud | mesh | cli; actor = the GUI actor / `requestedBy` / the CLI user). Phases are written to
`<agentHome>/upgrade/state.json` (+ `history.json`) on every change, and sent to the cloud at once in a heartbeat (§9.9):

1. **download** — signed `GET /v1/agent/release` + `GET /v1/agent/binary` (direct or relay; Range-resumable) into
   `opm-agent.new` next to the binary; `progress` = bytes / `sizeBytes`.
2. **verify** — Ed25519 release signature of the sha256 hex (agent.json `releasePublicKey`, else the cloud's key; a
   mismatch refuses), the file sha256 = the pinned sha256, self-test `opm-agent.new version` prints the pinned version →
   `ReleaseVerification`.
3. **swap** — fsync; the running binary is hard-linked (else copied) to `opm-agent.prev`; `rename(opm-agent.new,
   opm-agent)` (atomic). The boot guard (+ drop-in, `daemon-reload`) is (re)installed. `upgrade/pending.json` is written
   (deadline = restart + `AGENT_UPGRADE_HEALTH_SEC`).
4. **restart** — the watcher is started (below), then `systemctl restart --no-block opm-agent` (systemd) or graceful
   stop + `execve` (no systemd).
5. **health** — the NEW agent finds `pending.json`, finishes its startup (store, listeners, local `GET /_opm/healthz`),
   writes `upgrade/healthy`, sets the phase **done** and writes `agent.upgraded { upgradeId, from, to, durationSec, via }`.

A failure in 1–3 sets **failed** (`agent.upgrade-failed { phase, error }`), deletes `opm-agent.new` and leaves the old agent
running (no restart). A cloud request whose `version` ≠ the current pin fails at once with "the pinned release moved to X;
request again" (no download). Rollback = two independent layers; both restore `opm-agent.prev` when `healthy` is missing at
the deadline:

- **Watcher** — the old agent starts the KNOWN-GOOD binary as `opm-agent.prev upgrade-watch --data-dir <dir>` outside the
  service's lifetime (`systemd-run --unit opm-agent-upgrade-watch-<id> --collect`, else a detached `setsid` process). It
  polls `upgrade/healthy` + the local `GET /_opm/api/health` (ok with the target version); on timeout it stops the agent (`systemctl stop` / kill the
  pid), renames `opm-agent.prev` back over `opm-agent`, starts it, writes `upgrade/rolled-back.json` (`by: watcher`).
- **Boot guard** — `/usr/local/lib/opm/upgrade-guard.sh` (POSIX sh), run as `ExecStartPre=-` by the drop-in (§19.2) on
  every (re)start: when `pending.json` is past its deadline without `healthy` and the binary's sha256 is still the new one,
  it restores `.prev` and writes `rolled-back.json` (`by: guard`). Covers a crash-looping new binary and a reboot during
  the watch. The `-` prefix: a failing guard never blocks the start.

The restored old agent reads `rolled-back.json` and writes `agent.upgrade-failed` + `agent.rolled-back { from, to, reason,
by }` (actor = the requester); the phase becomes **rolled-back**. Sessions survive the restart (SQLite `sessions`; cookie
unchanged; actor tokens are site-key JWTs): the GUI polls `GET /_opm/api/health` (fallback `/_opm/api/info`) every 2 s while
restarting (≤ 120 s); a 401 afterwards → login with an explanation.

**Mesh "Upgrade all".** The coordinating site upgrades every reachable site below the pin ONE at a time, in siteSlug order
with itself LAST (it restarts), by forwarded `POST sites/{s}/agent/upgrade` (via mesh, runId set) + polling `GET
sites/{s}/agent/upgrade` until done / failed / rolled-back (timeout 10 min per site). The first failure stops the run (the
remaining sites = cancelled); sites below `GUI_UPGRADE_MIN_VERSION` or unreachable = skipped. State
`<agentHome>/upgrade/run.json` (`AgentUpgradeRun`, origin agent), finished by the coordinator after its own restart.

**agent.json view + editor.** `masked`: every key matching `/token|secret|password|key$/i` except `releasePublicKey` →
`"••••" + last 4`. The save validates with `validateAgentSettings` (same rules in the GUI and Go), writes agent.json
atomically (temp + rename, the previous file kept as `agent.json.bak`), writes
`agent.config-changed { changed, before, after }` (never secrets) and restarts (default; `restart: false` → `restartPending`).
A `containerNames` change renames the running containers in place at the restart. Everything else is read-only in the GUI.

**Support bundle** (`SUPPORT_BUNDLE_SECTIONS`): `version.json` (AgentSelfInfo, no keys), `agent.json` (masked), `debug/`
(the last 24 h of the debug log, already redacted), `docker-info.json` (no registry auths), `containers.json` (OPM
containers; env values removed), `activities.jsonl` (the last 500 of this site), `status.json`, `upgrade/` (state +
history). Never secrets, site.key, transcripts or volumes. Rate-limited per session (429 `rate-limited`).

**Uninstall** = copyable commands only (`uninstallCommands`: disable `opm-agent.socket` first (§21 #91: a held socket
would start the service again), then the service; remove the unit, the socket unit and `opm-agent.service.d`, the binary +
`.prev` + `.new`, the guard script; optional data wipe). Never a GUI action.

---------------------------------------------------------------------------------------------------------------------

### 7.6 Dev tools (`/_opm/api/sites/{siteSlug}/devtools/*`, §21 #97)

Service GUIs whose queries the AGENT runs on the site server: HTTP client (Postman-like), PostgreSQL, SQLite, Cassandra
(CQL), Redis, MQTT. Normative contract: `shared/src/devtools.ts` (`DEVTOOLS_API`, `createDevToolsClient`, types, pure
helpers `splitSqlStatements` / `classifyStatement` / `classifyRedis` / `parseRedisCommandLine` / `redactStatement` /
`redactHttpRequest`, vectors `shared/testdata/devtools-vectors.json`). Go: `agent/internal/devtools` (drivers pgx v5,
gocql, go-redis v9, paho.mqtt.golang, modernc sqlite, net/http), mounted by `agent/internal/agent/devtools_hook.go`
exactly like the toolbox (§7.4): `{siteSlug}` = this site or another site (forwarded over the mesh with the actor; the
MQTT SSE stream is copied with flushes); `*` → 422 (a connection lives on one site).

**Connections.** `presets` = `SiteConfig.devTools` of the head revision of that site (id `p:<presetSlug>`, read-only,
"Duplicate to edit locally"); `manual` = created in the agent GUI, SQLite table `devtools_connections` (id `c_<ULID>`).
Target: a container + port name (the agent dials the container IP on the site network — docker inspect of the OPM
container — and falls back to the published host port on 127.0.0.1 when the IP is not reachable from the agent, e.g.
Colima / Docker Desktop), host:port, an http(s) base URL, or for SQLite a file in a declared site volume (relative
path, no `..`, never through a symlink) or a container path mapped through that container's volume / host mounts (a path
on no mount → 422). Credentials: none, an expression resolved on the site from the head config (`{{ secret.x }}`,
`{{ var.y }}`, `{{ containers.<c>.env.<NAME> }}` …, field `devtool` over `siteDevtoolExprContext`, §21 #99; an unresolvable
expression → 422 with the expression error), or a literal = a static **Value** (§21 #103): a manual connection stores it in
`devtools_connections.secret_json`; a preset holds it in the config (`{ kind: 'literal', value }`, plain text — warning
V_DEVTOOL_LITERAL, agents ≥ 0.13.1). Either way every API read returns `MASK` (presets: `maskedAuth`; the agent's config
views mask it too), `revealCredential` returns it (RevealValue 60 s, `Cache-Control: no-store`, 30/min per session,
activity `devtools.credential-revealed`), Duplicate copies it, and the resolved value joins the redaction set of every
activity / log line like any credential. Update with `MASK` keeps the stored literal.

**Remembered clients.** One pooled client per connection (pgxpool max 4, gocql session, go-redis client, one paho
client, `*sql.DB` for SQLite, `http.Client` with its own transport), created on first use and reused; closed after
`idleTimeoutSec` (600) idle, on `disconnect`, when the connection changes, when a preset changes with a new head, and
recreated once automatically when an execution fails with a connection error (reconnect on failure). State
(`DevToolConnState`) is in memory.

**Execution.** `executeSql` splits statements (`splitSqlStatements`), classifies each (`classifyStatement`):
destructive → 422 `invalid` `details { confirmRequired: true, reasons[] }` unless `confirm` = the connection name (exact);
readOnly → postgres runs inside `BEGIN READ ONLY` (the server refuses writes), sqlite opens the file with
`mode=ro` + `PRAGMA query_only`, cassandra refuses write statements (403 `forbidden`, classifier). Statement errors are
results (`ok:false`, pg code / position / detail / hint), not HTTP errors; the remaining statements are skipped after an
error. `transaction: true` wraps postgres / sqlite statements in one transaction (rolled back on error). Explain: pg
`EXPLAIN (FORMAT JSON[, ANALYZE, BUFFERS])` (+ text), sqlite `EXPLAIN QUERY PLAN`, cassandra → 422. Timeout per
execution `timeoutSec` (default 30, max 3600; pg also `SET LOCAL statement_timeout`). Rows: up to `bufferRows` (10 000) /
`maxResultBytes` (64 MiB) are buffered per statement (`truncated` beyond), the first `maxRows` (1000) are returned and the
rest paged with `resultPage` (15 min, same GUI session); cells > `maxCellBytes` (64 KiB) are `CellTruncated` with a
`resultValue` link; `resultExport` CSV / JSON / NDJSON of the buffered rows. Columns carry the driver type + a display
hint. `cancel {queryId}` cancels the context (pgx sends a cancel request; gocql / redis / http abort) →
`devtools.query-cancelled`. At most `maxConcurrent` (8) executions per site (409 `conflict` above), 120 executions per
session per minute (429). Redis: one command (argv or a redis-cli line), `classifyRedis`: blocked commands → 422,
destructive (FLUSHALL / FLUSHDB / CONFIG SET / DEL|UNLINK with a pattern-like key or > 100 keys / EVAL…) → confirm,
readOnly → write commands 403; `redisScan` / `redisKey` / `redisInfo` are reads (session) but still go through the
pooled client. HTTP: `executeHttp` resolves `{{name}}` from the environment / inline variables and OPM expressions on the
site; refuses `/_opm/api/*` and `/_opm/peer/*` paths (the agent's own API, any host), drops any Cookie value named
`opm_session*`, never forwards the browser's cookies or headers; readOnly → only GET / HEAD / OPTIONS; follows ≤ 10
redirects when enabled (the chain is returned), `tlsInsecure` per request / connection, timing via httptrace, body read
up to `maxResponseBytes` (10 MiB; inline ≤ 2 MiB, else `resultBody`). MQTT: `mqttPublish` (qos 0–2, retain; readOnly →
403; empty retained payload = clear → confirm), live sessions (`mqttSession` → own paho client per session, ≤ 4 per GUI
session, single-use 60 s ticket → SSE `mqttStream`, ≤ 6 h, 1000-message buffer with `dropped` events), add / remove
filters, topics seen (`mqttTopics`, last 1000).

**Activities (both sites when forwarded).** EVERY execution writes `devtools.query` (`DevToolQueryActivity`: actor,
connection id / name / kind / source, resolved target, op, the statement / command / request REDACTED — `redactStatement`
/ `redactHttpRequest` + every known secret value of the site (≥ 4 chars) + the connection's own resolved credentials →
`••••••` — truncated to 4 KiB, duration, rows / rowsAffected / HTTP status, ok / error (≤ 500 chars, masked), readOnly,
destructive, confirmed, queryId). NEVER result data. Schema introspection is not logged (a read of metadata);
`redisKey` / `redisScan` are not logged (browsing) — `executeRedis` always is. Others: `devtools.connection-created |
updated | deleted | tested`, `devtools.credential-revealed`, `devtools.mqtt-published`, `devtools.mqtt-subscribed`,
`devtools.mqtt-unsubscribed`, `devtools.query-cancelled`; the forwarding site of a mesh write writes
`devtools.action-forwarded { siteSlug, forwardedTo, method, action, connectionId, status }`.

**History + saved.** `history` = `devtools.query` activities of the site (newest first; filters connectionId / kind /
actor / mine). Saved queries / requests (`devtools_saved`, per site, shared by operators, `collection` = folder) and HTTP
environments (`devtools_environments`; secret variables stored like literal passwords, masked, revealable) live in the
agent SQLite. Neither is in snapshots / `.opmlog` (dev tooling state, not deployment state).

**Switch + security.** agent.json `tools.devTools` (default true; false → every devtools endpoint 403 `forbidden` with
the reason, the GUI says how to enable it) and `devTools.{statementTimeoutSec, maxRows, bufferRows, maxCellBytes,
maxResultBytes, maxResponseBytes, idleTimeoutSec, maxConcurrent}`. Reads = session; executions, writes, reveals, MQTT
sessions = session + actor (the Actor header is a CSRF guard: a cross-site form cannot set it; the cookie is SameSite=Strict);
the MQTT stream needs the single-use ticket bound to the GUI session (or the forwarding site). There is no shell escape:
SQLite `ATTACH` is limited to files inside the same volume; pg `COPY … PROGRAM` / `COPY … TO|FROM '<file>'` and
`lo_import` / `lo_export` / `pg_read_file` are executed by the server as the DB user (the operator's database, not the
agent host) and are flagged destructive; the agent never runs a process for a dev tool.

### 7.7 Volume sync (`/_opm/api/sites/{siteSlug}/volume-sync/*`, §21 #101)

Table `VOLUME_SYNC_API` (shared/src/volume-content.ts); another site = forwarded over the mesh like the volume manager (both sites
log). listVolumeSync / getVolumeSync (session) = every persist-to-cloud volume of the head with `VolumeSyncStatus` (state, last
snapshot, live fingerprint / hash, pending push, queue, history, users). snapshotVolumeSync (session+actor; manual-agent; 413-style
`too-large` details `{ totalBytes, maxBytes, biggest[] }`). planVolumeSync = `VolumePushPlan` of the pending push (base / live /
cloud, `planVolumePush`). volumeSyncFile (?side=live|cloud|base&path=) = bytes for the per-file diff. An apply is a merge
(§21 #104 (e)): one confirm, NO intent wait — applyVolumeSync { pushId, resolution overwrite | keep-live | per-file, choices,
restart, liveTreeHash } (`intentId` optional: applyVolumeSyncStart → intent `volume-content-apply` stays for older GUIs and is
then checked; 409 conflict when live changed since the plan; locked during an update or another volume action). A plan with a
known base and no file changed on both sides is a clean merge (per-file, nothing to choose: cloud-only changes taken, live-only
kept) and the sync state is `cloud-ahead`, not `conflict` (the agent plans from the trees it holds; the cloud's
cloudSyncState trusts the agent's reported `cloud-ahead` for the same live hash): before-apply snapshot (when live changed) → Backups snapshot of the volume → [stop users, reverse start order] →
converge live → target (atomic writes, modes, owner = parent's; deletes non-excluded files not in the target; excluded paths and
`.opm-seeded` untouched) → [start users] → after-apply snapshot → report the push. keepLiveVolumeSync = decline (nothing
written, a live snapshot is uploaded).

## 8. Peer API (`/_opm/peer/*`, agentPort only, signed both ways)

| op | method | path | auth | permission | request body | response | errors (+ common) | activities | behaviour |
|---|---|---|---|---|---|---|---|---|---|
| `ping` | GET | `/_opm/peer/ping` | peer | — | — | `PeerPing`  | — | — | State every 10 s (head, lock, staging, internet, ports). |
| `cloudView` | GET | `/_opm/peer/cloud-view` | peer | — | — | `CloudView`  | — | — | This agent cloud view (latest fetch wins). |
| `have` | GET | `/_opm/peer/have` | peer | — | — | `HaveList`  | — | — | Blob have list. |
| `blob` | GET | `/_opm/peer/blobs/{sha256}` | peer | — | — | bytes (200, Range) or 302  | not-found | — | Blob bytes (Range supported, response not signed: content-addressed). |
| `fetchRequest` | POST | `/_opm/peer/fetch-request` | peer | — | `FetchRequest` | `FetchRequestResponse` (202) | conflict, no-internet | — | Ask the fetcher to download a blob from the cloud. |
| `revision` | GET | `/_opm/peer/revisions/{revId}` | peer | — | — | `PeerRevisionResponse`  | not-found | secret.fetched | Full revision (plain secrets) for staging without internet. |
| `pushProposal` | POST | `/_opm/peer/proposals` | peer | — | `PeerProposalPush` | `{ stored: boolean }`  | invalid | — | Distribute a local proposal (idempotent by revId). |
| `relay` | POST | `/_opm/peer/relay` | peer | — | `PeerRelayRequest` | `RelayResponse`  | no-internet | — | Forward an inner signed cloud request via POST /v1/agent/relay. |
| `actors` | GET | `/_opm/peer/actors` | peer | — | — | `PeerActorsResponse`  | — | — | The actors table (merged by every agent every 60 s). |
| `updateStep` | POST | `/_opm/peer/update/{updateId}/step` | peer | signer must be the plan coordinator | `UpdateStepRequest` | `{ accepted: true }` (202) | locked, invalid, conflict | — | Start one update step on this site (async). |
| `updateState` | GET | `/_opm/peer/update/{updateId}` | peer | — | — | `UpdateSiteState`  | not-found | — | Step state of this site (also refreshes lock.lastContactAt). |
| `snapshotStep` | POST | `/_opm/peer/snapshots` | peer | — | `{ snapshotId: string; reason: 'manual' \| 'scheduled'; note: string; createdBy: string }` | `{ accepted: true }` (202) | locked, invalid | — | Take this site part of a manual snapshot (async; state in snapshotPart). |
| `snapshotIndex` | GET | `/_opm/peer/snapshots` | peer | — | — | `ListResponse<SnapshotSummary>`  | — | — | Snapshot parts held by this site. |
| `snapshotPart` | GET | `/_opm/peer/snapshots/{snapshotId}/part` · query `PeerSnapshotPartQuery` | peer | — | — | bytes (200, Range) or 302  | not-found | — | tar of this site part (sites/<slug>/… entries, optional images/blobs/…). |
| `putSnapshotPart` | PUT | `/_opm/peer/snapshots/{snapshotId}/part` | peer | — | raw bytes | `{ stored: true }`  | invalid, conflict | — | Store an imported part (tar, verified against the manifest in the first entry). |
| `patchSnapshot` | PATCH | `/_opm/peer/snapshots/{snapshotId}` | peer | — | `PatchSnapshotRequest` | `{ ok: true }`  | not-found | — | Note change. |
| `deleteSnapshot` | DELETE | `/_opm/peer/snapshots/{snapshotId}` | peer | — | — | `{ ok: true }`  | not-found, conflict | — | Delete this site part. |
| `siteGet` | GET | `/_opm/peer/site/{rest}` | peer | — | — | bytes (200, Range) or 302  | not-found | — | Read-only mirror of GET /_opm/api/sites/{self}/<rest> for GUI data of another site ({rest} may contain "/"). |
| `sitePost` | POST | `/_opm/peer/site/{rest}` | peer | X-OPM-Actor + X-OPM-Session headers carry the actor | raw bytes | bytes (200, Range) or 302  | not-found | — | Forwarded site action (save transfer, send from path, renew cert, upload). |

Peer request authorisation: the signer must be an enrolled site of the SAME deployment (peer table from the cloud view /
enroll). `updateStep` additionally requires the signer to be the `plan.coordinator` of the lock (or of the plan in a `lock`
step). `siteGet` / `sitePost` map `{rest}` to the local handler `/_opm/api/sites/<self>/<rest>` (and the extra internal reads
`proposals/{revId}/staging`, `activities/export?from&to` (NDJSON of Activity), `status-cache`); sitePost writes use the
`X-OPM-Actor` / `X-OPM-Session` headers as actor.

---------------------------------------------------------------------------------------------------------------------

## 9. Agent ↔ cloud sync protocol

### 9.1 Loops and timings (every agent)

| loop | period | what |
|---|---|---|
| poll | continuous long-poll (`wait=25`) when a cloud route exists; retry backoff 1 s → 60 s | update cloud view (§9.3) |
| heartbeat | 30 s | POST /v1/agent/heartbeat (direct or relay) |
| peer ping | 10 s per peer, timeout 5 s | GET /_opm/peer/ping; reachable = answered < 30 s ago; `site.unreachable` after 90 s, `site.reachable` when back |
| cloud-view exchange | 10 s per reachable peer | GET /_opm/peer/cloud-view; adopt if newer |
| actors merge | 60 s | GET /_opm/peer/actors |
| stager | on new proposal / restage / blob arrival | §9.5 |
| upload pipe | on append + every 10 s | §9.8 |
| outbox | 5 s | queued cloud reports (proposal status, head, local proposals, transfer status) — FIFO, retried with backoff, idempotent |
| sampler | 30 s | uptime_raw rows (site + each container) |
| rollup | 1 h | uptime_hourly; delete raw > 24 h, hourly > 30 d |
| scheduled snapshot | daily 02:00 local time | only the site that is first in slug order among reachable head-config sites starts it (coordinated like a manual snapshot, reason scheduled, createdBy "system") |
| retention | after every snapshot | §13.2 |
| tunnel expiry | 5 s | lease without poll for 30 s → expired |
| debug rotation | daily | §13.4 |

"internet" for a site = its last DIRECT cloud call succeeded within 90 s.

### 9.2 Ping (`PeerPing`)

Answer: head, lock, open proposals with this site's staging, internet, cloudRoute, cloudFetchedAt, keyVersion, ports, edge state,
inConfig. The GUI tiles and the gate use the latest ping of each peer (self = live values).

### 9.3 Cloud view (latest fetch wins)

- Own poll answer → `cloud_view` row with `source 'cloud'`, fetchedAt = the answer's `fetchedAt` (server time).
- Peer view (`GET /_opm/peer/cloud-view`): adopt iff `peer.fetchedAt > own.fetchedAt` (strictly), then set
  `source = 'peer:<slug>'`, keep the peer's cursor. Equal → keep own.
- On every adoption (own or peer) the agent reconciles: (1) peers → `peers` table; (2) accessKey → adopt if version higher;
  (3) proposals → for each open proposal not known locally: fetch the revision (cloud `GET /v1/agent/proposals/{revId}`, or a
  peer `GET /_opm/peer/revisions/{revId}` when no cloud route) and store it + start staging if this site is in
  (parent ∪ proposal) sites; status updates from the view are applied to local rows (cloud is authoritative for
  `superseded`; local terminal states are never reverted); (4) transfersToSite of this site → download queue; (5) deleted →
  stop polling (keep peers/GUI; banner "deployment deleted in the cloud"; containers keep running); (6) §21 #90
  `agentRelease` → the pin shown on the Agent page (`AgentSelfRelease.source = cloud-view`); `agentUpgrades` → the entry
  whose siteSlug = this site, not expired and whose requestId is not in `<agentHome>/upgrade/handled-requests.json` is
  recorded there and started as an upgrade with via cloud, actor = `requestedBy`, requestId / runId set (§7.5; a version ≠
  the pin → failed "the pinned release moved to X; request again"). Other sites' entries are ignored (each agent takes only
  its own; a relayed site gets the same view from a peer).

### 9.4 Proposal arrival rules (agent)

> **SUPERSEDED by §21 #104 (agent ≥ 0.14, cloud with branches)** — kept for agents < 0.14 (compat proposals) and as history. Current model: docs-v2/branches-commits.md.


A proposal whose `parentRevId ≠` local head → local status `stale` (report `status: stale`, activity `proposal.stale`).
A proposal with the same parent as another open one: the one with the greater revId (ULID order) wins; older → superseded.
Staging of newer proposals continues while an update runs; only one update runs at a time (the lock).

### 9.5 Staging (every site in parent ∪ proposed config; automatic; no approval)

State per site: `pending → downloading → ready | failed` (+ progress 0..1). Steps:
1. **Validate**: `validateConfig(config, mode 'agent', deploymentId, services/images from revision.catalog, notFoundPageIds =
   catalog keys, detectedHosts = own advertiseHost + `PeerInfo.advertiseHost` of the peers)` + expression resolution for THIS
   site's containers and edge (full context with plain secrets). Host resolution is automatic (`resolveSiteHosts`, §10.5): a
   site with host null uses its advertised address; one that has not reported an address yet → `site.<s>.host` / `self.host`
   give E_NULL ("site X has host auto and has not reported its address yet"); a host expression resolving to null / "" falls
   back to auto (warning V_HOST_AUTO, never blocks). Any error → failed (error = first message; detail.issues = all). Sites not
   in the proposed config (being removed) skip to step 6.
2. **Images**: for each container of this site: `catalog.images["<serviceSlug>:<version>"]` (manifest, config, layers digests). Missing
   blobs (manifest + config + layers) → §9.7. progress = bytes have / bytes needed (images + files).
3. **docker load**: if `docker image inspect <configDigest>` is missing, build a docker-archive tar stream from the blobs:
   `manifest.json` = `[{"Config":"blobs/sha256/<cfg>","RepoTags":["<serviceSlug>:<version>","opm/cfg:<cfgHex>"],"Layers":["blobs/sha256/<l1>",…]}]`
   (names: §15.1.1)
   + the blob files at those paths (layers as stored, gzip is accepted by docker load) → `POST /images/load`. Check the loaded
   image id = configDigest. Never run it.
4. **Files**: every file mounted by this site's containers and every `tls.files` cert → blob of kind file (§9.7, stored
   under `blobs/files/<hex>`). Then the **site copy**, ONCE PER SITE per mounted file (not per container): render none →
   the blob bytes; render expressions → `renderSiteFile(source, siteFileExprContext(config, ctx, siteSlug))` (field `file`:
   var, secret (plain), site, self, deployment). The result is written to a STAGED temp file next to the file's location
   (`locationPath(FileDef.location, 'files', fileSlug, agentHome)`), mode 0400 when the rendered content holds a secret
   in clear (`RenderedFile.secret` and not `secretHashed`) else 0444 (§21 #103: a secret that only went through
   `mosquitto_passwd` is a salted hash → 0444, readable by a broker that drops root); APPLY swaps it into place atomically (rename) before the containers that mount it are
   (re)created. Secrets mounted as files are written the same way to `<agentHome>/secrets/<secretSlug>` (0400). The same check
   runs as validation (`ValidationContext.fileContents`): a block that does not resolve → **V_FILE_RENDER**
   `"<fileSlug>:<line>:<col>: <code> <message> (site <s>)"` (once per site; `container.*` / `file.*` → E_NOT_ALLOWED "files
   are shared by all containers on the site — use a variable instead"); a file with mime `application/json` / `*+json` whose
   rendered output does not parse → **V_FILE_JSON**. Both fail the staging. Locations: a relative vs an absolute location that
   resolve to the same place or nested ones → V_LOCATION (staging fails). The TLS `certFileSlug` is never rendered. Rendered
   bytes never go to .opmlog, the debug log, activities or snapshot metadata.
5. **Not-found page**: write `notfound/<id>.html` from the catalog.
6. **Dry run**: build every container spec (§15.2) (expressions resolved), check: every hostPort / edge / agent port the NEW
   config needs is free on the host OR currently held by this agent / an OPM container of this deployment that the update will
   replace; mount sources exist (host paths; site volume directories at their locations are created); docker connectivity;
   round E: a volume whose kind changed while its location holds data (or a location of the wrong type with data) fails
   ("… use a new volume name"); seed blobs (files and archives, by sha256 like file blobs, peers share them) are fetched and
   rendered seed files validated; §21 #88: the docker name of every enabled container is free or held by a container
   labelled for this deployment + site (else the clash error of §15.1). Failure → failed.
7. ready (progress 1). Report `POST /v1/agent/proposals/{revId}/status { siteSlug, staging, progress, error }` (outbox) and in
   the ping. Activity `proposal.staged { revId, result, error, durationMs }`.
Network errors in step 2/4 are retried (backoff 5 s → 5 min) and keep `downloading`; validation / dry-run errors are final until
`restage`. Staging of a proposal stops when it becomes terminal.

### 9.6 Image manifests

The catalog in the revision already holds the ImageSpec (digest, configDigest, layers, ports, env, volumes, healthcheck …), so
an agent with no cloud route can stage. `GET /v1/agent/images/{serviceSlug}/{version}/manifest` is used only to cross-check digests
when the cloud is reachable (mismatch → failed "catalog digest mismatch").

### 9.7 Smart distribution of blobs (layers, configs, manifests, files)

For each missing blob B (sha256 hex h):
1. **Peer copy first**: ask every reachable peer's have list (`GET /_opm/peer/have`, cached 10 s). If a peer has B →
   `GET /_opm/peer/blobs/sha256:<h>` (Range resume), verify sha256, store (`source peer:<slug>`).
2. **Fetcher**: the set F = reachable sites (incl. self) whose ping says `internet: true`. Fetcher = the site in F with the
   highest `sha256(h + ":" + siteSlug)` (hex string compare, i.e. rendezvous hashing; `h` = 64 hex chars without prefix).
   If self is the fetcher → download from the cloud (`GET /v1/agent/blobs/sha256:<h>`, follow 302, Range resume), verify,
   store (`source cloud`).
3. Else → `POST /_opm/peer/fetch-request { sha256, sizeBytes, kind, ref }` to the fetcher (202 `{ fetcher, status }`), then
   poll its have list every 5 s and copy when present. A peer that is NOT the fetcher by its own computation answers 409
   `{ fetcher }` → ask that one. 503 `no-internet` / unreachable → recompute F without it (failover to the next in order).
4. F empty → wait (state `waiting`; staging stays downloading; retried every 10 s). With the browser tunnel active the
   tunnel-holding site counts as internet for the fetcher choice.
Every stored blob passed its sha256 check; a bad copy is deleted and fetched again. `blobs` rows are the have list.
Blobs referenced by a kept snapshot, the head, or an open proposal are never pruned; others are pruned after 7 days unused.

### 9.8 Activities upload pipe

Find a route (direct → relay). Send entries with seq > `kv.ackSeq` in batches of ≤ 500 (`POST /v1/agent/activities`);
store the returned `ackSeq`. Backoff 1 s → 5 min on errors; never loses entries. A 422 for a broken chain is logged (debug,
error) and retried after 10 min (manual repair by an operator: download + upload bundle).

### 9.9 Heartbeat body

`HeartbeatRequest` (types.ts): head, current staging (the newest open proposal this site stages), peersSeen (last answer of
each peer), metrics, edge report (bound ports, bind errors, certs), keyVersion, advertiseHost, agentPort, ip, hostname,
`detectedAddresses` (non-loopback IPs, IPv4 first, then the hostname; optional for old agents), `guiPorts` (§21 #86: every
host port the GUI answers on now: edge http/https, gui-alias, agent-port) and `edge.defaultEdge` / `edge.bindErrors[].owner`.
The cloud Setup page builds `guiUrls` (port 80 first), `defaultEdge` and `portAlerts` from them. An address change alone does
not recreate containers; the new auto address is used at the next update.
§21 #90 `upgrade` = the running / last `AgentUpgradeProgress` (`<agentHome>/upgrade/state.json`): sent in an extra heartbeat
AT ONCE on every phase change and with every normal heartbeat until 24 h after it
finished; the new agent sends `done` (or the restored agent `rolled-back`) as its first heartbeat after the restart. The cloud
uses it to clear `upgradeRequest` and advance an Upgrade-all run (§6.1); absent on agents < 0.10.0.
A site with no direct internet sends its heartbeat via the relay; via the tunnel when no relay exists.

### 9.10 Browser internet tunnel

- One active lease per DEPLOYMENT (the agents share lease state via ping: `cloudRoute = tunnel` and the lease in the overview).
  `tunnelStart` when another lease is active anywhere → 409 conflict `details.active`.
- The lease-holder agent queues TunnelRequests for its own cloud calls AND for the cloud calls of peers that have no other
  route: such a peer sends `POST /_opm/peer/relay { request }` to the holder, and the holder enqueues that inner request
  UNCHANGED (its signed X-OPM-* headers, url = apiBase + inner path); the cloud sees the peer's own signature.
- `tunnelNext`: long-poll ≤ 25 s, returns ≤ `max` (default 4, max 4) requests not yet handed out (a handed-out request without a
  result is handed out again after its timeoutSec). Updates `lastPollAt`. Lease not active → 409 conflict.
- The tab: `fetch(url, { method, headers, body: base64-decoded bodyB64 })`, then `tunnelResult` (status, headers, body ≤ 4 MiB
  inline as base64) or `chunked: true` + `tunnelChunk` parts (≤ 4 MiB each, index 0..n-1 in order) + `tunnelComplete
  { parts, sha256 }`. Network error → status 0 + error text. At most 4 requests in flight per tab.
- What goes through the tunnel: polls, heartbeats, reports (proposal status, head, local proposals, transfer status),
  activities, revision / proposal fetches and blob / transfer DOWNLOADS (large bodies as chunked results); these always stream to a tunnelled request, never 302 (§21 #114). NOT through the
  tunnel: uploads (to-cloud transfers and upload parts; a tunnel request body is ≤ 4 MiB, a part 8 MiB) — they stay queued on
  the site and go up when a direct or relay route exists (§21 #100: "wait for a route" means the SITE's own way to the
  cloud; it never needed anyone on the receiving end).
- Lease ends: `tunnelStop` (stopped), `tunnelRevoke` by another tab (revoked; the holder tab gets 409 on its next call),
  no poll for 30 s (expired). Activities `internet.tunnel.started|stopped|revoked|expired`.
- Security note (shown in the GUI): payloads (incl. proposals with secret values) pass through the browser tab.

---------------------------------------------------------------------------------------------------------------------

## 10. Expressions (normative; `expr.ts`, vectors `expr-vectors.json`)

### 10.1 Grammar (EBNF)

```
template  = { text | block } ;
text      = any characters except the sequence "{{" ;           (* newlines allowed in text *)
block     = "{{" , ws , expr , ws , "}}" ;                       (* no CR / LF anywhere inside a block *)
expr      = primary , { ws , "|" , ws , filter } ;
primary   = path | string | number | boolean ;
path      = root , "." , segment , { "." , segment } ;
root      = "var" | "secret" | "file" | "site" | "self" | "deployment" | "container" | "containers" ;
segment   = ( letter | digit ) , { letter | digit | "-" | "_" } ;
filter    = name , [ "(" , ws , [ arg , { ws , "," , ws , arg } ] , ws , ")" ] ;   (* no space between name and "(" *)
name      = "default" | "join" | "upper" | "lower" | "trim" | "base64" | "url" | "int" | "json"
          | "sha256" | "sha512" | "mosquitto_passwd" ;                 (* §21 #103; lexed as [a-z] { [a-z0-9_] } *)
arg       = string | number | boolean ;
string    = '"' , { char - ( '"' | "\" | CR | LF ) | "\" , ( '"' | "\" ) } , '"' ;
number    = [ "-" ] , digit , { digit } , [ "." , digit , { digit } ] ;
boolean   = "true" | "false" ;                                    (* only as a whole identifier without dots *)
ws        = { " " | "\t" } ;
letter    = "A".."Z" | "a".."z" ;  digit = "0".."9" ;
```
- An identifier starts with a letter; if it is not a root → `E_UNKNOWN_PATH` (at its position), e.g. `{{ foo.x }}`, `{{ Var.x }}`, `{{ true.x }}`.
- A root without "." → `E_SYNTAX`. A `"{{"` inside a string literal is text: escape a literal `{{` as `{{ "{{" }}`. `}}` outside a block needs no escape.
  This is the only escape, also in rendered files (field `file`): e.g. a Handlebars / Go-template file writes
  `{{ "{{" }} name }}` to get `{{ name }}`. No grammar change was made for files.
- Arity (checked at parse, `E_SYNTAX` at the filter name): default exactly 1 arg; join 0–1 string arg; mosquitto_passwd
  0–2 number args `(format 7 | 6, iterations 1–10000)` (iterations only with format 7); all others none.
- A filter name is lexed as `[a-z]` followed by `[a-z0-9_]` (§21 #103: `mosquitto_passwd`); an unknown name →
  `E_UNKNOWN_FILTER`. Agents older than 0.13.1 do not know the §21 #103 filters (they refuse such a revision at staging).
- Positions: 0-based Unicode code point index. Messages are informative; codes + positions are normative.

### 10.2 Fields, roots, result type

| field kind | where | roots | form | result |
|---|---|---|---|---|
| `env` | Container.env[].value | var secret file site self deployment container containers | mixed text + blocks | string |
| `imageEnv` | ImageEnv.value default (resolved in the container's context) | var file site self deployment container containers | mixed | string |
| `mountPath` | ImageVolume.mountPath, Container.volumes[].mountPath | var deployment self container | mixed; must start with a literal "/" (else V_PATH at save) | string: absolute, not "/", no spaces, no ".." (else V_PATH at publish) |
| `host` | SiteConfig.host (null = auto, not an expression) | var deployment | mixed | string, must be an IP or DNS name (V_HOST); null / "" result → auto + warning V_HOST_AUTO (never an error) |
| `hostPort` | Container.ports[].hostPort | var deployment | ONE whole block (spaces around allowed) | integer 1–65535 |
| `imagePort` | ImagePort.port | var deployment | ONE whole block | integer 1–65535 |
| `edge` | edge.hosts[].hostname, redirect.to, rewrite replacements (rewrite routes + proxy `rewrite`), proxy.setHeaders values, response header values (host + route `set` / `add`) | var deployment self | mixed (+ the edge variables `$uri $args … $1` and single-brace `{path} {query} {host}` placeholders, left untouched — expanded per request, §12.6) | string |
| `file` | FileDef content with `render: 'expressions'`, rendered ONCE PER SITE (one copy shared by every container that mounts it) | var secret site self deployment containers (`container.*` / `file.*` → E_NOT_ALLOWED with the hint "files are shared by all containers on the site — use a variable instead"; `containers.*` are site-level facts, §21 #88) | multi-line text + blocks (a block stays on one line); errors carry line:col (`exprLineCol`) | text (written 0400 when it holds a secret in clear, else 0444 — also when every secret only went through `mosquitto_passwd`, §21 #103; read-only mount) |
| `devtool` | (§21 #99) dev-tool preset values: username, database, clientId, apiKeyHeader, password / token `expr`, default header values, url target; agent manual connections + HTTP-client OPM placeholders — resolved ON THE SITE from the head config (`siteDevtoolExprContext`) | var secret site self deployment containers — the ONLY field with `containers.<containerSlug>.env.<NAME>` (`CONTAINERS_ENV_FIELDS`); `container.*` / `file.*` → E_NOT_ALLOWED | mixed | string (secret when it uses `secret.*` or a secret-valued env) |

All other fields are literal (commands, slugs, agentPort, agentGuiAliasPort, edge ports, path prefixes, TLS fields …).
Number fields accept a JSON number or a string with exactly one block; a string without "{{" in a number field → E_TYPE.

### 10.3 Paths

| path | value | errors |
|---|---|---|
| `var.<variableSlug>` | the variable value (string / number / boolean; enum → string; list → string[]; json → object/array; null) | unknown slug → E_UNKNOWN_PATH |
| `secret.<secretSlug>` | the secret value (only on the site) | unknown → E_UNKNOWN_PATH; no value → E_NULL; outside env / file → E_NOT_ALLOWED |
| `file.<fileSlug>.path` | the mountPath of that file volume in THIS container | unknown file → E_UNKNOWN_PATH; not mounted by this container → E_FILE_NOT_MOUNTED |
| `site.<siteSlug>.host` | the site address (`resolveSiteHosts`): configured host, or auto (detected / advertised address; cloud placeholder `<auto:siteSlug>`) | unknown site → E_UNKNOWN_PATH; auto and not reported yet (on a site) → E_NULL |
| `site.<siteSlug>.port.<containerSlug>.<portName>` | resolved hostPort (number) of that container instance's port | unknown site/container/port → E_UNKNOWN_PATH; hostPort null → E_NO_HOSTPORT |
| `self.slug` / `self.host` | this site's slug / address (as `site.<self>.host`) | host auto and not reported yet (on a site) → E_NULL |
| `deployment.id` | deploymentId | — |
| `container.slug` | the container's containerSlug (instance name) | outside container fields → E_NOT_ALLOWED |
| `container.env.<NAME>` | the FINAL value of env NAME in THIS container: the container value if set, else the image default (incl. the options default); its own expressions resolved first | allowed in env, imageEnv, mountPath, file; NAME not an env of the image or the container → E_UNKNOWN_PATH; no value → null (E_NULL unless `\| default`); a loop → E_CYCLE; NAME is secret-valued (secretRef or `secret.*`) and the field is imageEnv or mountPath → E_NOT_ALLOWED |

| `containers.<containerSlug>.host` | (§21 #88) the DNS name of that container on THIS site's network = its network alias = the containerSlug (resolves in both docker naming modes, §15.1). It resolves ONLY from containers on the same site network; from another site use `site.<s>.host` + `site.<s>.port.…` | env, imageEnv, file, devtool (else E_NOT_ALLOWED); no such container on this site → E_UNKNOWN_PATH; disabled → resolves + warning V_CONTAINER_REF_DISABLED; a reference = an implicit depend (start after + wait ready, §15.5) |
| `containers.<containerSlug>.port.<portName>` | the container's INTERNAL port (number) of that image port (`imagePortNumber`) — container-to-container traffic on the site network, no host port needed | as above; unknown port → E_UNKNOWN_PATH; the image port does not resolve → E_NULL |
| `containers.<containerSlug>.env.<NAME>` | (§21 #99) the FINAL value of env NAME of that container: the container value if set, else the image default (incl. the options default), its own expressions resolved (`resolveContainer`, exactly what the driver applies); secret-valued (secretRef / `secret.*`) → the result is secret (masked `••••`) | field `devtool` ONLY — every other field → E_NOT_ALLOWED ("only allowed in dev-tool presets"; no container value can read another container's env, so there are no cross-container cycles); no such container → E_UNKNOWN_PATH; NAME not an env of the image or the container → E_UNKNOWN_PATH; no value → null (E_NULL unless `\| default`); a failed env value (E_CYCLE …) → the same code at the reference |

**One rule (§21 #88):** `container.*` (singular) is ALWAYS this container (`container.slug`, `container.env.<NAME>`);
`containers.<containerSlug>.*` (plural) is ANY container of this site, self included. `container.<otherSlug>.host` →
E_UNKNOWN_PATH. No slug is reserved. Example: `MQTT_URL = mqtt://{{ containers.mqtt.host }}:{{ containers.mqtt.port.mqtt }}`
→ `mqtt://mqtt:1883`.
`container.serviceId` does not exist (services have one id): it gives E_UNKNOWN_PATH.
Check order for a path: root allowed for the field (E_NOT_ALLOWED) → shape (E_UNKNOWN_PATH) → existence/value.

### 10.4 Filters (left to right; wrong input type → E_TYPE at the filter)

| filter | input → output |
|---|---|
| `default(x)` | null or "" → x, else unchanged |
| `join(sep=",")` | list of string/number/boolean → joined text (other items → E_TYPE) |
| `upper`, `lower` | string / number → string (null, boolean, list → E_TYPE) |
| `trim` | string → string |
| `base64` | string / number → standard Base64 (with padding) of the UTF-8 bytes |
| `url` | string / number → percent-encoding of every UTF-8 byte except `A–Z a–z 0–9 - _ . ~` (upper-case hex) |
| `int` | number → truncate; string matching `^\s*-?\d+(\.\d+)?\s*$` → truncate; else E_TYPE |
| `json` | any → canonical JSON (JCS) text |
| `sha256`, `sha512` | (§21 #103) string / number → lower-case hex digest of the UTF-8 bytes (e.g. `{{ "hello" \| sha256 }}` → `2cf24dba…9824`). Every field. |
| `mosquitto_passwd(format=7, iterations=101)` | (§21 #103) string / number (non-empty, no NUL) → a Mosquitto password-file hash: format 7 = `$7$<iterations>$<base64 salt>$<base64 PBKDF2-HMAC-SHA512(password, salt, iterations, 64 bytes)>` (Mosquitto 2.x `sha512-pbkdf2`), format 6 = `$6$<base64 salt>$<base64 SHA512(password ‖ salt)>` (1.x). salt = 12 bytes, DETERMINISTIC: the first 12 bytes of HMAC-SHA256(key = password, msg = `"opm-mosquitto-salt" NUL deploymentId NUL siteSlug`) — the same password on the same site always renders the same line (stable file hash: no re-render churn, no restart on apply); another site / deployment gets another salt. Base64 = standard with padding. Only in fields that allow secrets (env, file, devtool; elsewhere → E_NOT_ALLOWED at the filter). `shared/src/hash.ts` / Go `expr.MosquittoHash`; reproduces the real `mosquitto_passwd` byte for byte (`testdata/mosquitto-passwd-vectors.json`, Go `TestDockerMosquittoPasswd`). |

Block result → text: string as is; number → ECMAScript Number::toString (`1e+21`, `1e-7`, `0.25`); boolean → `true`/`false`;
null → E_NULL (at the block start); list/object → E_TYPE (at the block start). A value that uses `secret.*` is a secret
value: stored and shown masked, never logged (`ResolveResult.secret`, `masked` uses `••••`). A block whose input is
secret stays secret after any filter (§21 #103: `northwind:{{ secret.mqtt-password | mosquitto_passwd }}` previews as
`northwind:••••`; the cloud hashes the `•••` stand-in or yields the deferred placeholder, the site renders the real line).

### 10.5 Contexts (how the agent / cloud builds `ExprContext`) — `validate.ts`

- `var` = every Config variable value. `deployment.id` = deploymentId (templates validate with "template").
- **Deferred values** (round E, §21 #78; `expr.ts DEPLOY_PLACEHOLDER = "‹set at deployment›"`, `isDeferredValue`): outside agent
  mode (`varValues` / `secretValues` with `defer`, default on; template / draft / publish previews) a REQUIRED variable or
  secret whose value is null / "" evaluates as the placeholder text. Expressions keep evaluating with it; a result that
  contains it is `deferred` (`ResolveResult.deferred`); a filter that cannot use it (`| int`, a number field) gives
  `E_DEFERRED`, which is never reported; a block whose input was deferred yields the placeholder. Previews render it as a
  dashed "set at deployment" token. Agent mode never defers (staging has every value; publish is blocked before).
- `site[slug].host` = `resolveSiteHosts(config, deploymentId, { detected, placeholder })[slug].host`: host null → the
  detected address (`ValidationContext.detectedHosts`: cloud = `AgentDoc.advertiseHost` of enrolled sites; agent = its own
  advertiseHost + `PeerInfo.advertiseHost` of the peers) → else cloud (`placeholder: true`) `autoHostPlaceholder(slug)` =
  `<auto:siteSlug>` (`AUTO_HOST_RE`; GUIs show "auto · detected on install"; never sent to a site), agent (`mode: 'agent'`,
  `placeholder: false`) null → E_NULL when used. A host expression = `resolve(SiteConfig.host, {var, deployment}, 'host')`;
  a null / "" result → the auto address + `fallback` (V_HOST_AUTO warning); other errors → `error`.
  `site[slug].port[containerSlug][portName]` = resolved hostPort (null if null; `siteExprMap`). Hosts and hostPorts can only use
  var/deployment, so there are no cycles between sites.
- **Dev-tool presets** (field `devtool`, §21 #99): `siteDevtoolExprContext(config, ctx, siteSlug)` = the site file context +
  `containers[<c>].env` = `siteContainersEnv()` (per container `resolveContainerConfig` → `{ value, secret, error? }`; cloud
  secretsSet → `•••`). Go `validate.SiteDevtoolExprContext` (the agent's devtools hook resolves presets, manual connections
  and HTTP placeholders with it, `validate.DevToolExprField` = `expr.FieldDevtool`).
- `containers` (§21 #88) = `siteContainersMap(config, ctx, siteSlug)`: every container of the site (self and disabled ones
  included) → `{ host: containerSlug, enabled, port: { <image portName>: imagePortNumber | null } }` (unknown image → no
  ports). Built into the container context AND the site file context (`siteFileExprContext`); not in edge / host / port fields.
- For a container: `self = { slug: site, host: resolved host | null }`, `container = { slug: containerSlug, env }`,
  `file[f].path` = the RESOLVED mountPath of the mount with source `file` = that fileSlug in THIS container (else null →
  E_FILE_NOT_MOUNTED), `secret` = plain values (cloud previews show `•••` / null).
- **Files** (field `file`): the SITE context `siteFileExprContext(config, ctx, siteSlug)` = `{ deployment, self (the site),
  var, secret, site }` — no `container`, no `file` root (a file is shared by every container of the site that mounts it, so
  it cannot depend on one container); rendered once per site with `renderSiteFile(src, ectx)` (replaces the round-C
  `renderContainerFile`). Editors check the source with `checkFileContent(src)` (syntax, roots, line:col);
  `ValidationContext.fileContents` (fileSlug → source text; agent staging, cloud preview) enables V_FILE_RENDER (once per site,
  `(site <s>)`) / V_FILE_JSON. Cloud "Preview for site…" (site picker only) resolves with secrets masked (`•••`).
- **Container resolution** (`resolveContainer(base, env: EnvSource[], mounts: MountSource[])`; sources built by
  `validate.ts containerSources()` / `resolveContainerConfig()`; mount keys `volume[i]` and `image:<name>` for the effective
  mounts of `containerMounts`: image volumes without a row or with source none are not mounted and have no key). Values resolve lazily in dependency order (DFS with memo and a stack): `container.env.X` resolves X first,
  `file.<slug>.path` resolves that file's mount first. So env values resolve before the mount paths that use them. A cycle
  gives E_CYCLE (message = the chain) at the reference that closes the cycle, for every value on it. A value that uses a
  failed value fails with the same code at its own reference position. Go `expr.ResolveContainer` has the same semantics.
  Example (the user's case): `NODE_ENV` is a choice env; the config volume mount path is
  `/app/helper/config/env/{{ container.env.NODE_ENV }}.ts` → `/app/helper/config/env/worker.ts` when NODE_ENV = worker.
- Edge: `{ deployment, self, var }`.
- Resolution happens at staging (every value must resolve) and again when a container spec is built (APPLY).

### 10.6 Security

`secret.*` only in Container.env[].value and rendered files (field `file`; `SECRET_FIELDS`); whole secrets via `secretRef`;
secret files via mount source `secret` (one copy per site, `<agentHome>/secrets/<secretSlug>`, 0400). A rendered file that
holds a secret in clear is written 0400 at its location (else 0444 — §21 #103: a secret that reaches the file only
through `mosquitto_passwd` is a salted hash, `ResolveResult.secretHashed`, and the file is 0444 because Mosquitto opens
password_file after dropping to its own user) and mounted read-only; rendered bytes are never logged, uploaded or
put in snapshots. A
secret-valued env (secretRef or `secret.*`) referenced through `container.env.*` from an image default or a mount path →
E_NOT_ALLOWED (a secret never goes into a path or a default). The cloud never
resolves secrets (previews mask). Expressions cannot run code, read the host or call the network. The hash filters
(§21 #103) are pure functions of their input (+ deploymentId / siteSlug for the Mosquitto salt); `mosquitto_passwd` is
limited to the secret fields, iterations ≤ 10000 (browser previews hash synchronously).

---------------------------------------------------------------------------------------------------------------------

## 11. Validation (shared `validate.ts`)

`validateConfig(config, ctx)` returns issues with stable codes (`VALIDATION_CODES`). Levels: **save** (structural; API refuses
the write) and **publish** (stored as a warning in template/draft mode; an error in publish/agent mode). Severities: `error`,
`warning`, `info` (round E: a note only — never counted as a warning, never a blocker, shown with a neutral icon; the API
filters severities by exact value). Paths look like `sites[app].config.containers[hello-api].env[REDIS_ADDR].value`.
**Required variables are set at deployment** (§21 #78): outside agent mode a REQUIRED variable / secret without a value
evaluates as `DEPLOY_PLACEHOLDER` ("‹set at deployment›", §10.5); usage checks never report it (E_DEFERRED is dropped) and
value checks skip deferred results. Agent mode is strict (no stand-in).

| code | level | rule |
|---|---|---|
| V_TYPE / V_REQUIRED / V_FORMAT / V_ENUM / V_RANGE / V_LENGTH / V_TOO_MANY | save | shapes, patterns (`RE` in validate.ts), ranges, `LIMITS` |
| V_DUPLICATE | save | slugs unique per section; `containerSlug` unique per site ("container name X is used twice on this site"; the same service + version may appear many times); env / port row / image-volume row / route id / host names unique; two edge routes with the same (match kind, path, hosts, methods); volume seed entry paths |
| V_RESERVED | save | site slug ∈ `RESERVED_SITE_SLUGS` (backups, files, internet, revisions, secrets, sites, updates, variables); containerSlug ∈ `RESERVED_CONTAINER_SLUGS` (`new`: GUI route …/containers/new); prefix / exact route path `/_opm` or under `/_opm/` |
| V_REF | save | serviceSlug, image `<serviceSlug>:<version>`, `upload:<iconId>` icons, notFoundPageId, fileSlug, secretSlug, secretRef, site depends, container depends (containerSlug of the same site), route hosts, proxy target containerSlug + port, port row `portName` / volume row `imageVolume` ∈ the image, health-check port |
| V_VOLUME | save | mount source rules: an extra mount needs a source ≠ none (volume, host, file or secret); host path absolute, not "/", without spaces, ".." or `{{ }}` (`isSafeMountPath`); a volume source's `volumeSlug` must match `RE.volumeSlug` `^[a-z0-9][a-z0-9_.-]{0,99}$` (V_FORMAT); unknown source kind → V_ENUM — the removed round-C kind `data` → V_ENUM with the hint "source kind "data" was replaced by declared site volumes ({ kind: "volume", volumeSlug })" |
| site volumes | save | `SiteConfig.volumes` missing → V_REQUIRED, not a list → V_TYPE; `volumeSlug` duplicate → V_DUPLICATE, bad pattern → V_FORMAT; label > 80 / description > 2000 → V_LENGTH; `backup` not boolean → V_TYPE; > 100 volumes → V_TOO_MANY |
| dev-tool presets (§21 #97) | save | `SiteConfig.devTools` (absent = []; not a list → V_TYPE; normalizeSiteConfig drops an empty list): V_DEVTOOL_SLUG (presetSlug `^[a-z0-9][a-z0-9-]{0,62}$`), V_DEVTOOL_DUP, V_DEVTOOL_MAX (> 50 per site), name 1–80 (V_REQUIRED / V_LENGTH), V_DEVTOOL_KIND, V_DEVTOOL_TARGET (target kind not allowed for the kind / field missing; host ≤ 253 without `{{`, port 1–65535, url http(s) ≤ 4096), V_DEVTOOL_CONTAINER (missing → error, disabled → warning), V_DEVTOOL_PORT (not a port of the container's image), V_DEVTOOL_VOLUME (sqlite: a declared DIR volume + safe relative path 1–1024 without leading "/", "\\", NUL, `{{`, ".."; container-file: absolute mount-path rule), V_DEVTOOL_LITERAL (WARNING since §21 #103: a static Value — literal password / token, an expression without `{{ }}` or one that references nothing — "stored in plain text in the config — anyone who can read the config sees it; use a secret for production"; an empty literal → V_DEVTOOL_AUTH), V_DEVTOOL_SECRET / V_DEVTOOL_VAR (refs from the parsed expressions), V_DEVTOOL_CONTAINER (a `containers.<c>.*` ref to a container not on the site) / V_DEVTOOL_ENV (`containers.<c>.env.<NAME>`: NAME not an env of the image or the container; skipped when the image is unknown) / V_DEVTOOL_PLAIN_ENV (warning: password / token from a container env that holds a plain value) (§21 #99), E_* (field `devtool`: roots var, secret, site, self, deployment, containers), V_DEVTOOL_AUTH (pg username; cassandra user + password both or none; redis db "0"–"15"; http basic → username, bearer / api-key → token; mqtt clientId ≤ 65535 bytes; fields the kind does not use → warning), V_DEVTOOL_OPTION (ranges, enums, ≤ 50 default headers with valid names). Local proposals: devTools ≠ head → V_LOCAL_DEVTOOLS. Paths `sites[<s>].config.devTools[<presetSlug>].<field>`; vectors `shared/testdata/devtools-validate-vectors.json` (Go `validate/devtools.go`). |
| V_VOLUME_KIND | save | `SiteVolume.kind` ∉ dir \| file; a mount of a `file` volume whose mount path is empty or ends with "/" ("volume X is a single file: mount it at a file path (e.g. /data/flows.json), not a folder"); an `ImageVolume` of kind `file` whose mount path ends with "/" ("image volume X is a File: its mount path must not end with "/" (or set it to Folder)"); `ImageVolume.kind` ∉ dir \| file → V_ENUM |
| V_VOLUME_KIND_IMAGE | warning (every mode) | a `file` site volume on an image volume row WITHOUT its own mount path whose `ImageVolume.kind` is `dir` (§21 #83; legacy absent kind = inferred from the path): "image volume X (P) is a Folder in image S:V; the single-file volume Y replaces the whole folder with one file — pick a folder volume here, or set image volume X to File on the image page if P is a file" (path `….volumes[X].source.volumeSlug`). Never blocks. The cloud container page adds "+ New folder volume…" (volume drawer, kind Folder) and "Set image volume X to File on the image page" (`/app/services/S/image/V?volume=X&from=<container page>`) |
| V_VOLUME_KIND_MISMATCH | save | a `dir` site volume on an image volume row WITHOUT its own mount path whose `ImageVolume.kind` is `file`: "folder volume Y cannot be mounted on file path P (image volume X is a File) — use a single-file volume, or change the image volume to Folder on the image page" (path `….volumes[X].source.volumeSlug`). Container page links: "+ New single-file volume…" (drawer, kind Single file) and "Set image volume X to Folder on the image page". A `file` / `secret` / `host` source on any image volume and any kind on an extra mount / a row with its own mount path: no kind check (current rule) |
| V_FILE_VOLUME_RENAME | info (every mode) | a `file` site volume mounted read-WRITE (image volume row or extra mount), once per mount: "Y is a single-file volume mounted read-write: apps that save by rename (e.g. Node-RED flows.json) fail on single-file mounts (EBUSY) — use a folder volume seeded with the file" (`FILE_VOLUME_RENAME_INFO`). Never counted as a warning; the container page shows the "Recommended for Node-RED" tip next to it |
| V_VOLUME_SEED | save (rendering: publish, as V_FILE_RENDER) | `SiteVolume.seed` (`checkSeed`): not null / files / file / archive; kind mismatch (files / archive → dir only, file → file only); files: 1–100 entries, entry path relative, segments `[A-Za-z0-9._-]`, no "." / ".." (`seedPathError`), unique (V_DUPLICATE), not a file and a folder at once, fileSlug exists (V_REF); archive: name `.tar.gz` / `.tgz` ≤ 200 chars, size 1 B – 200 MiB (`SEED_ARCHIVE_MAX`), blobSha256 format; a seed file without content → V_FILE_CONTENT (publish) |
| V_VOLUME_SEED_FILE_REF | save (agent mode: none) | §21 #101: `seed` kind `files` / `file` (Config files as volume content) in a template / draft / publish: "Files are static and read-only and are never volume content — give the volume its own content"; accepted only in revisions published before 0.12 |
| V_VOLUME_CONTENT_PATH | save | §21 #101: a tree entry / empty-folder path is not a safe relative path (`contentPathError`) |
| V_TOO_LARGE | save | §21 #101: a content file > 50 MiB, starter content > 200 MiB |
| V_VOLUME_PERSIST | save | §21 #101: `persist` on a single-file volume, a bad exclude pattern (`excludePatternError`); maxBytes 1–200 MiB → V_RANGE |
| V_FILE_IN_WRITABLE_VOLUME | save (expression paths: publish; agent mode: warning) | §21 #101: a File mount at or inside a writable volume / host mount of the same container |
| V_LOCAL_VOLUME_CONTENT | local edit | §21 #101: a local proposal changes a volume's tree / single content or persist setting |
| V_VOLUME_CONTENT_EXCLUDED / V_BLOB_MISSING | API 422 | §21 #101: a cloud edit holds a path under the excludes / references blobs that were not uploaded |
| V_VOLUME_UNDECLARED | save | a mount with source `volume` names a volumeSlug that is not declared in the site's `volumes` ("volume X is not declared on site S: add it under the site's Volumes") |
| V_READONLY | save | a file or secret mount with `readOnly` ≠ true ("a file / secret mount is always read-only") |
| V_LOCATION | save (relative vs absolute collisions: staging) | `FileDef.location` / `SiteVolume.location` (`checkLocation`): not `{kind:'auto'}` / `{kind:'path', path}`; a path that is empty, has `{{ }}`, is neither `./…` nor a safe absolute path; a `./` path with empty / `.` / `..` segments or spaces, or whose first segment is a reserved agent entry (`AGENT_HOME_RESERVED`) other than its own kind dir, or that is the bare kind dir (`./files`, "pick a name inside ./files/"). Per site: two locations (all files + that site's volumes) that are the same place or nested ("file X (…) and volume Y (…) are the same place or nested on site S") |
| V_CYCLE | save | site depends / container depends (incl. self) |
| V_DEPENDS_SUGGESTED | info | (§21 #93, §15.5) a container mentions another enabled container of the site as a hostname and does not depend on it — "add it to Depends on?" |
| V_START_ORDER_CYCLE | warning | (§21 #93) implicit / suggested references form a cycle: those containers start in the declared order |
| V_TEMPLATE_SECRET_VALUE | save | templates hold no secret values |
| V_VALUE_TYPE / V_VALUE_RULE | save | variable value vs type (null always allowed), min/max, whole-value pattern, enum options |
| V_PATTERN | save | regex invalid or not RE2 (look-around, back-references) |
| V_ENV_ONE_OF | save | env item has exactly one of value / secretRef |
| V_PORT_CONFLICT | save | per site: agentPort, agentGuiAliasPort, edge.httpPort, edge.httpsPort and every enabled container hostPort (same protocol = the image port's) differ; `hostIp` is ignored (0.0.0.0 covers every address) |
| V_PORT_EDGE | save | container hostPort ∈ {80, 443, edge.httpPort, edge.httpsPort} |
| V_JOB_RESTART | save | job images: restart never / on-failure |
| V_MOUNT | save | literal mount paths: absolute, not "/", no spaces / "..", unique per container (effective mounts only: image volumes without a row or with source none are not mounted); an extra mount needs a mountPath (V_REQUIRED) |
| V_PATH | save / publish | a mount path with `{{ }}` must start with a literal "/" (save: "mount path must be absolute: start with "/" (e.g. /app/config/{{ container.env.NODE_ENV }}.ts)"); the RESOLVED path must be absolute, not "/", without spaces or ".." (publish; skipped for a deferred value); uniqueness is checked on resolved paths |
| V_OPTION | save / publish | image env default not one of `options` (save); container env value, after resolution, not one of the image env `options` (publish; skipped for a deferred value). Replaces V_ENV_LIST |
| V_REFERENCE | save | `reference` of register / inspect / patch image does not parse as `[host/]repository[:tag][@sha256:digest]` |
| V_IMMUTABLE | save | an immutable field in a PATCH (serviceSlug, image version, …) |
| V_HEALTHCHECK / V_IMAGE_ENV | save | image rules (V_IMAGE_ENV: secret env with a default or with options; options size / unique / no expression) |
| V_EDGE_HOST / V_EDGE_TLS / V_EDGE_ROUTE | save | §12.4 (V_EDGE_ROUTE includes a proxy to a port whose `ContainerPort.edge` is false: "port P of container X is closed to the edge"; stopRegex / keepPath / stripPrefix / rewrite rules) |
| V_EDGE_REGEX / V_EDGE_HEADER / V_EDGE_BODY | save | §12.4: a route / rewrite regex outside the RE2 ∩ JavaScript dialect (`checkEdgeRegex`); response header lists (set + add of one name, `always` of an unknown name, response headers on a rewrite route); `maxBodyBytes` range |
| E_* (expression) | save when `checkExpr` fails (syntax, filter, root not allowed, path shape, numeric form); publish when resolution fails (unknown slug, null, no host port, file not mounted, type) |
| E_DEFERRED | never reported | an expression error caused by `DEPLOY_PLACEHOLDER` (a required variable / secret without value, outside agent mode; also a number field that would hold it). `Issues` drops it; previews show the placeholder token |
| V_VAR_REQUIRED / V_SECRET_REQUIRED | publish (not in template mode) | required variable value null / "" ; required secret without value — ONCE on the variable / secret ("X is required — set it before publishing"), never on its usages. Template: no issue. Draft: severity warning (the draft saves), publish: error (blocker). Agent: error |
| V_VAR_OPTIONAL_NULL | info (template mode only) | an OPTIONAL variable has no value and no template default: ONE info on the variable ("optional var X has no value; add \| default(…) where it is used, or a template default"); its usages may still report E_NULL / E_TYPE |
| V_FILE_CONTENT | publish (size: save) | a mounted file has no content; content > maxBytes |
| V_IMAGE_NOT_READY | publish | import status ≠ ready |
| V_ENV_REQUIRED / V_ENV_SECRET | publish | required image env without default not set; secret image env with a plain value |
| V_HOST | publish | resolved host is not an IP or DNS name (skipped for a deferred value) |
| V_HOST_AUTO | warning (draft / publish / agent; not in template mode) | the host expression resolved to null / "" (an OPTIONAL variable; a required one is deferred) → the site uses its auto address: "var X has no value — leave host on auto or set the variable (the site uses its detected address)". Never blocks |
| V_FILE_RENDER | publish (needs `ctx.fileContents`: agent staging / APPLY, cloud preview) | a file with `render: 'expressions'` does not resolve for a site where a container mounts it (rendered once per site): `"<fileSlug>:<line>:<col>: <code> <message> (site <s>)"`. `container.*` / `file.*` in a file → E_NOT_ALLOWED (save, `checkFileContent`) "files are shared by all containers on the site — use a variable instead" |
| V_FILE_JSON | publish (as V_FILE_RENDER) | mime `application/json` or `*+json`: the rendered output is not valid JSON (skipped when the output is deferred) |
| V_NO_SITES | publish | at least one site |
| V_NO_CHANGE | publish | publish / local proposal identical to head |
| V_VOLUME_UNUSED | warning (every mode) | a declared site volume is mounted by no container: "volume X is declared but no container mounts it (its data stays on disk and is still backed up)" (the last part only with backup on). Never blocks |
| V_VOLUME_NOT_MOUNTED | warning (every mode) | an image volume has no row: "image volume X (/path) is not mounted: its data is lost when the container is recreated. Pick a data volume, or choose "Not mounted"". An explicit `none` row does not warn. Never blocks |
| warnings | — | V_EDGE_NO_HOSTS, V_EDGE_TLS_NO_HTTPS, V_EDGE_ACME_INTERNET, V_EDGE_TARGET_DISABLED, V_EDGE_HOP (hop-by-hop request header ignored), W_RESPONSE_HEADER_AS_REQUEST (a response-only header set as a request header; fix `moveToResponseHeaders`). V_EDGE_SHADOW is no longer raised (round E: order-free nginx matching) |

Local edits (`checkLocalEdit(head, new)`, agent only; §21 #113 a site is standalone): only V_LOCAL_SITES "sites cannot be
added or removed on a site" and V_LOCAL_VOLUME_CONTENT (a volume's persist-to-cloud setting). Everything else is a local
edit: variables / secrets / files (add, redefine, remove), containers (add, duplicate, rename, remove, every field, the
image — a catalog version, a docker reference or a host image, §21 #113 b), the site volumes, the whole edge, settings
incl. the label, dev-tool presets. Validation still blocks a broken result (V_REF, V_PORT_CONFLICT, …).
(Superseded: V_LOCAL_ADD_REMOVE, V_LOCAL_DEFINITION, V_LOCAL_CONTAINERS, V_LOCAL_TAG, V_LOCAL_DEVTOOLS.)

Image volumes without a row are NOT mounted (warning V_VOLUME_NOT_MOUNTED; no implicit data volume since round D). A row
sets the source (volume / host / file / secret / none) and may override the mountPath (resolved; it may hold expressions) —
`containerMounts`. Defaults + legacy conversion applied on write and read: `normalizeSiteConfig` (§2.2 "Legacy
normalization").

Other document validators: `validateRegisterImage`, `validateHealthCheck`, `validateVariable`, `validateSecretDef`,
`validateFileDef`, `validateEdge`, `validateAccessRules` (access.ts). Simple bodies (registry, service, tag, account,
not-found page, deployment) use the `RE` patterns + `LIMITS` the same way (label 1–80, description ≤ 2000, icon pattern,
color `^#[0-9a-f]{6}$`, email, E.164 phone, https URLs, `RE.icon` / `RE.picture` via `checkIcon` / `checkPicture`, `RE.version`,
repository pattern for the test-login / browsing `repository` parameter). Image references: `parseImageReference` (V_REFERENCE).

---------------------------------------------------------------------------------------------------------------------

## 12. Edge (embedded Caddy) — matching, Caddy mapping, TLS

### 12.1 Request decision (normative: `edge-resolve.ts resolveEdgeRequest`; Go `edgeres.Walk`; vectors `edge-vectors.json`)

The edge decides like nginx `location` blocks (round E, §21 #68–#75). The edge given to the resolver has its expressions
resolved (hostnames, redirect `to`, rewrite replacements, request and response header values).

1. **Port.** Only edge.httpPort / httpsPort use these rules (agentPort / alias serve only `/_opm/`).
2. **Host.** Exact (case-insensitive) > wildcard `*.x.y` (exactly one extra label) > `*`. No host: HTTP → 404 site not-found
   page; HTTPS → TLS handshake fails (no certificate). HTTPS on a host with tls none → handshake fails.
3. **HTTPS redirect.** HTTP + `redirectHttpToHttps` (tls ≠ none, httpsPort set) → 308 `https://<host>[:httpsPort]<path><query>`.
   Routes are not checked; no response headers are added.
4. **`/_opm`** and `/_opm/…` → the agent (known GUI/API paths → GUI handler; `/_opm/peer/*` and unknown → agent 404). Routes
   are not checked; no response headers are added.
5. **Visible routes.** `route.hosts` empty = every host, else it must contain the host entry's hostname. `match.methods`
   null / [] = every method; a route whose methods do not contain the request method is invisible for this request.
   Matching uses the DECODED path (`%20` → space) without the query; no slash merging, no case folding (except `(?i)`).
6. **Selection** (`selectRoute`), over the visible routes:
   a. an **exact** route (`kind exact`, nginx `location =`) whose path equals the path wins;
   b. find the **longest prefix**: `kind prefix` routes whose path is a plain string prefix of the path (`/api` matches
      `/api`, `/api/x` AND `/apix`; `/api/` does not match `/api`). Ties: (order, routeId);
   c. **nginx auto-redirect**: when no prefix route equals the path and a PROXY prefix route (path longer than "/") equals
      path + "/" → 301 `Location: <path>/[?query]` (regex routes are not checked);
   d. if the longest prefix has `stopRegex` (nginx `^~`) → it wins;
   e. **regex** routes (`kind regex`, nginx `~`; leading `(?i)` = `~*`) by (order, routeId): the first that matches wins;
      its captures `$1…$9` are kept for the redirect `to`;
   f. else the longest prefix (b);
   g. none → site not-found page, 404.
   `order` only orders regex routes (and breaks ties). The GUIs show `effectiveRouteOrder`: exact (by path) → prefixes with
   stopRegex (longest first) → regexes (by order) → prefixes (longest first), each with a `why` text. A ^~ prefix wins only
   when it is the LONGEST matching prefix (6d), so the list is a reading aid, not "first match wins".
7. **Rewrite route** (`action.type rewrite`, nginx `rewrite … last`): its regex runs against the current path; no match →
   404 not-found page. The replacement (captures of the rewrite regex + variables, §12.6) becomes the path; if it contains
   "?", the part after it REPLACES the query (nginx would append the original args unless the replacement ends with "?";
   with `$request_uri` that duplicates them — §21 #70). Then selection (6) runs again on the new path. More than 10
   rewrites in one request → 500 (built-in page). `$request_uri` stays the original.
8. **Redirect route**: `Location = expandEdgeVars(to)` with the request variables and the captures of a regex route.
   `keepPath` (prefix routes only): strip a trailing "/" from the result and append the rest of the path after the prefix
   (unless it is "/") + "?query". Status as configured (301 302 303 307 308). Response headers (12) apply.
9. **Proxy route**, checks in this order:
   1. `websocket` false and the request has `Upgrade: websocket` → 400;
   2. body over the effective limit (13) → 413;
   3. target container not "completed" (§15.4) or not running → 503 (not-found page with status 503);
   4. upstream path: `rewrite {regex, replacement}` (nginx `rewrite … break`: regex against the current path, captures of
      THAT regex; no match → path unchanged; "?" in the result replaces the query) OR `stripPrefix` (prefix / exact routes:
      the rest after the matched path without its trailing "/", ensured to start with "/") — never both; then `addPrefix`
      (trailing "/" removed) is prepended. The query is kept unless replaced. An unchanged path keeps the client's raw encoding;
   5. request headers (10), then the call `http://127.0.0.1:<loopback port>` (§12.2) over HTTP/1.1 with keep-alive;
   6. dial / upstream errors (502 / 504) → the 503 page.
10. **Request headers → container.** Host is preserved (the client's). Defaults: `X-Real-IP` = client IP,
    `X-Forwarded-For` = client IP (REPLACED: Caddy trusts no proxies; nginx `$proxy_add_x_forwarded_for` appends to a
    client-sent list), `X-Forwarded-Proto` = scheme, `X-Forwarded-Host` = request host[:port]. Then `setHeaders` (override
    case-insensitively; `Host: $host` keeps the client host, any other Host value replaces it), then `removeHeaders`
    (delete). Hop-by-hop names in setHeaders (Connection, Upgrade, Keep-Alive, TE, Transfer-Encoding, Trailer,
    Proxy-Connection) are ignored (warning V_EDGE_HOP): Caddy forwards websocket upgrades and keep-alive itself.
11. **WebSockets.** `websocket` true (default for new routes, `newProxyAction`; stored routes keep their value) → the upgrade
    is passed through (Caddy `reverse_proxy` handles Upgrade / Connection). After the upgrade the connection uses the same
    timeouts (14).
12. **Response headers → browser** (`EdgeResponseHeaders {set, add, remove, always}`; effective list =
    `effectiveResponseHeaders`: the route's own list when `route.responseHeaders` ≠ null — it REPLACES the host's entirely,
    nginx add_header inheritance — else the host's). Order on a response: upstream headers → `remove` (nginx
    proxy_hide_header; every proxied response) → `set` (replaces any value; = proxy_hide_header + add_header of the same
    name) → `add` (appends; duplicates allowed, nginx add_header). `set` / `add` apply only to statuses 200 201 204 206 301
    302 303 304 307 308 (`ADD_HEADER_STATUSES`) unless the name is listed in `always` (nginx `add_header … always`: every
    status, incl. the edge's own 404 / 413 / 500 / 503 pages for that host / route). Applies to proxy and redirect routes
    (incl. the auto-redirect 301 of the route that caused it); not to the 308 https redirect or `/_opm`. The not-found 404
    (no route) gets the host's `always` headers. Rewrite routes carry none (`responseHeaders` must be null: the final
    route's list applies).
13. **Body size + buffering.** Effective limit (`effectiveMaxBody`): route `maxBodyBytes` (0 = unlimited; null = inherit) else
    `edge.maxBodyBytes` (null = unlimited). A Content-Length over the limit → 413 BEFORE the upstream is contacted; a
    chunked body that grows past it → 413 if nothing was sent to the client yet (with `Connection: close`), else the
    connection is aborted. `requestBuffering` true → up to 8 MiB (`EDGE_DEFAULTS.requestBufferBytes`) buffered in memory
    before the upstream call, the rest streams; false (default) → streamed. `responseBuffering` false (default) → every
    write is flushed at once; true → Caddy's default copy (no response buffer in memory beyond that). `text/event-stream`
    is ALWAYS flushed at once. The edge never writes temp files; uploads and downloads stream with bounded memory.
14. **Timeouts** (nginx semantics): `connectTimeoutSec` (1–3600, 60) = dial; `readTimeoutSec` (1–86400, 60) = time to the
    response header AND the idle time between two reads from the upstream (a stream that keeps sending — SSE, a download,
    a websocket — is never cut; one idle longer than readTimeoutSec is closed); `sendTimeoutSec` (1–86400, 60) = idle time
    between two writes to the upstream. Legacy `timeoutSec` → readTimeoutSec (connect / send 60).
15. **Trace** ("Test a URL", `EdgeResolution`): status, kind (proxy · redirect · agent · not-found · not-ready · tls-fail ·
    bad-url · not-edge-port · rewrite-loop 500 · too-large 413 · ws-refused 400), route, `order[]` (effective order with
    `why`), `rewrites[]` (from → to per rewrite route), `captures`, `autoRedirect`, Location, upstream
    (`http://<containerSlug>:<containerPort><path>`), steps, and `trace { match {kind, path, why}, upstreamPath {how none |
    strip | rewrite, from, to}, requestHeaders [{name, value, source default | route}], removedRequestHeaders,
    responseHeaders + responseHeadersFrom (route | host | none), timeouts, buffering, maxBodyBytes + maxBodyFrom (route |
    edge), websocket }`. Options: method, ports, notReady, `bodyBytes` (→ 413), `upgrade` (→ 400 when websocket is off).
    `edgeVectorView` is the part both implementations must agree on (59 vectors).

### 12.2 Upstream addressing

The agent runs on the host, so container DNS names are not resolvable by it. Every container port of an OPM container is ALSO
published on `127.0.0.1:<ephemeral>` (Docker `HostIp 127.0.0.1`, `HostPort ""`); the edge dials `127.0.0.1:<that port>`
(looked up from the running container after each start). Health checks of type http/tcp use the same loopback port. These
loopback bindings are not part of the user's port model (not validated, not in `site.*.port`). The "Test a URL" box shows the
logical upstream `http://<containerSlug>:<containerPort><path>` (`EdgeProxyAction.containerSlug`; the DNS name / network
alias of the instance). A port row with `edge: false` is not proxied (V_EDGE_ROUTE); its loopback binding still exists for
health checks.

### 12.3 Caddy JSON (built from the head SiteConfig after expressions resolve)

```json
{
  "admin": { "disabled": true },
  "storage": { "module": "file_system", "root": "<agentHome>/certs" },
  "logging": { "logs": { "default": { "level": "WARN" } } },
  "apps": {
    "http": {
      "http_port": <httpPort>, "https_port": <httpsPort or 443>,
      "servers": {
        "edge_http":  { "listen": [":<httpPort>"],  "automatic_https": { "disable": true }, "routes": [ …HOST ROUTES… ], "errors": { "routes": [ …ERROR ROUTES… ] } },
        "edge_https": { "listen": [":<httpsPort>"], "automatic_https": { "disable": true }, "tls_connection_policies": [ …per host… ],
                        "routes": [ …HOST ROUTES (no https redirect)… ], "errors": { "routes": [ …ERROR ROUTES… ] } }
      }
    },
    "tls": {
      "certificates": { "automate": [ <hostnames with internal or acme, not "*"> ], "load_pem": [ { "certificate": "<PEM>", "key": "<PEM>", "tags": ["<hostname>"] } … files mode ] },
      "automation": { "policies": [
        { "subjects": [<internal hosts>], "issuers": [ { "module": "internal", "lifetime": "12h" } ] },
        { "subjects": [<acme hosts with email E>], "issuers": [ { "module": "acme", "email": "E", "challenges": { "http": {"disabled": <not http-01>}, "tls-alpn": {"disabled": <not tls-alpn-01>}, "dns": <dns-01 only: { "provider": <DNS provider config, §12.4> }> } } ] },
        { "on_demand": true, "issuers": [ { "module": "internal", "lifetime": "12h" } ] }   ← only when host "*" has tls internal (permission module opm_any)
      ] }
    }
  }
}
```
HOST ROUTES — one entry per edge host (both servers), in host-priority order (exact hosts first, then wildcards, then "*").
The DECISION is not expressed in Caddy matchers: one handler (`opm_route`) runs the shared algorithm (§12.1 5–7, Go
`edgeres.Walk`, the same function as Test a URL) and the per-route Caddy routes only match its result.
```json
{ "match": [ { "host": ["<hostname>"] } ],        // omitted for "*"
  "terminal": true,
  "handle": [ { "handler": "subroute", "routes": [
     // 1. http server only, when redirectHttpToHttps (routes not checked, no response headers):
     { "handle": [ { "handler": "static_response", "status_code": 308, "headers": { "Location": ["https://{http.request.host}<:httpsPort if ≠ 443>{http.request.uri}"] } } ] },
     // 2. the agent:
     { "match": [ { "path": ["/_opm", "/_opm/*"] } ], "handle": [ { "handler": "opm_agent", "surface": "edge" } ] },
     // 3. the decision: nginx selection, method filter, auto-redirect, internal rewrites (≤ 10). Sets the vars
     //    opm_route (routeId) | opm_auto (routeId of an auto-redirect) + opm_location, opm_uri ($uri), opm_args ($args),
     //    opm_is_args, opm_query ("?args" | ""), opm_caps ([]string). No route / rewrite regex no match → error 404;
     //    > 10 rewrites → error 500 (rendered by the ERROR ROUTES).
     { "handle": [ { "handler": "opm_route", "routes": [ <EdgeRoute JSON of this host, effective order> ] } ] },
     // 4. per route, in effective order; auto-redirect candidates (proxy prefix routes whose path ends with "/", ≠ "/"):
     { "match": [ { "vars": { "opm_auto": ["<routeId>"] } } ], "terminal": true,
       "handle": [ …RESPONSE HEADER HANDLERS…, { "handler": "opm_redirect", "auto": true, "status": 301 } ] },
     //    redirect route:
     { "match": [ { "vars": { "opm_route": ["<routeId>"] } } ], "terminal": true,
       "handle": [ …RESPONSE HEADER HANDLERS…, { "handler": "opm_redirect", "to": "<to>", "status": <301|302|303|307|308>, "keepPath": <bool>, "prefix": "<path>" } ] },
     //    proxy route (handlers in this order):
     { "match": [ { "vars": { "opm_route": ["<routeId>"] } } ], "terminal": true,
       "handle": [
         …RESPONSE HEADER HANDLERS…,
         { "handler": "opm_ws_guard", "allow": <websocket> },                       // 400 for Upgrade: websocket when off
         { "handler": "opm_body_limit", "max": <bytes> },                            // only when the effective limit is set
         { "handler": "opm_ready", "containerSlug": "<containerSlug>" },             // 503 page while not completed
         { "handler": "opm_upstream", "matched": "<route path>", "stripPrefix": <bool>, "rewrite": { "regex": "…", "replacement": "…" }, "addPrefix": "<p>" },
         { "handler": "reverse_proxy", "upstreams": [ { "dial": "127.0.0.1:<loopback port>" } ],
           "transport": { "protocol": "opm_http", "versions": ["1.1"], "dial_timeout": "<connect>s",
                          "response_header_timeout": "<read>s", "read_idle_timeout": "<read>s", "write_idle_timeout": "<send>s" },
           "headers": { "request": {
             "set": { "X-Real-IP": ["{http.request.remote.host}"], "X-Forwarded-For": ["{http.request.remote.host}"],
                      "X-Forwarded-Proto": ["{http.request.scheme}"], "X-Forwarded-Host": ["{http.request.hostport}"],
                      <setHeaders: override case-insensitively, hop-by-hop dropped, minus removeHeaders> },
             "delete": [ <removeHeaders> ] } },
           "flush_interval": -1,          // responseBuffering false (Caddy also flushes text/event-stream + unknown length)
           "request_buffers": 8388608 }   // requestBuffering true only (8 MiB in memory, then streamed)
       ] },
     //    rewrite routes get no Caddy route (they never answer; opm_route followed them).
     // 5. safety net:
     { "handle": [ { "handler": "error", "status_code": 404 } ] }
  ] } ] }
```
RESPONSE HEADER HANDLERS (the route's own list, else the host's; outermost first — Caddy runs the innermost wrapper first, so
a response gets remove → set → add):
`{"handler":"headers","response":{"set":{<non-always>},"add":{<non-always>},"require":{"status_code":[200,201,204,206,301,302,303,304,307,308]}}}`,
`{"handler":"headers","response":{"set":{<always>},"add":{<always>},"deferred":true}}`,
`{"handler":"headers","response":{"delete":[<remove>],"deferred":true}}` (proxy routes only).
After the per-host entries, a final catch-all route serves the not-found page (404) when no host matched (HTTP only).
ERROR ROUTES (per server): per host (host matcher) a subroute: for each route with its own responseHeaders that has `always`
names: `{ match vars opm_route = <id>, handle [headers response set / add (always only)] }`; then the host's `always`
headers for every other request (`not vars opm_route ∈ [ids with own lists]`); then the pages by
`{http.error.status_code}`: 404 → site not-found page (404) · 413 → built-in 413 page · 500 → built-in 500 page (rewrite
loop) · anything else → the 503 page (status 503: opm_ready, dial / upstream errors). After the hosts: the same pages
without headers.
**Header values** (request set, response set / add) map variables to Caddy placeholders: `$host` / `{host}` →
`{http.request.host}`, `$uri` / `{path}` → `{http.vars.opm_uri}`, `$args` / `$query_string` → `{http.vars.opm_args}`, `$is_args` →
`{http.vars.opm_is_args}`, `{query}` → `{http.vars.opm_query}`, `$request_uri` → `{http.request.orig_uri}`, `$scheme` →
`{http.request.scheme}`, `$remote_addr` → `{http.request.remote.host}`, `$server_port` → `{http.request.local.port}` (the port
that accepted the connection), `$http_x_y` → `{http.request.header.X-Y}` (missing header → ""), `$1…$9` → "" (no captures in
header values), unknown `$name` literal; a literal `{` / `}` in a user value is escaped `\{` / `\}`. `$uri` / `$args` are the
values after internal rewrites and before the upstream rewrite, as in the resolver. Redirect `to` and rewrite replacements
do NOT go through placeholders: `opm_redirect` / `opm_route` / `opm_upstream` expand them with the shared Go functions
(`RedirectLocation`, `Walk`, `UpstreamPathQuery`).
**opm_http transport**: embeds Caddy's `HTTPTransport` (every field works) and wraps the dialed connection. Read: each Read gets
a `read_idle_timeout` deadline; when it expires and a write happened less than `read_idle_timeout` ago (or one is in
progress) the Read is re-armed instead of failing, so the timer starts after the last write (a slow 100 MB upload is not cut
by a pending response read). Write: each Write gets a `write_idle_timeout` deadline that restarts while the write makes
progress. `response_header_timeout` = readTimeoutSec. Websockets after the upgrade use the same wrapped connection, so an
idle websocket (no data either way) is closed after readTimeoutSec, as in nginx. (Caddy's own `read_timeout` /
`write_timeout` are per-operation deadlines and would cut long uploads; they are not used.)
TLS connection policies: one per host with `match.sni = [hostname]` (files mode: `certificate_selection.any_tag = [hostname]`),
then a default policy (no match) for "*". A host with TLS files on 443 and plain HTTP on 80 (no redirect) works.
Custom Caddy modules registered by the agent (Go, in process, `internal/edge`): `http.handlers.opm_agent` (serves the agent GUI
mux; never `/_opm/peer/*`), `http.handlers.opm_route` (the decision), `http.handlers.opm_upstream` (upstream path + query),
`http.handlers.opm_body_limit` (413), `http.handlers.opm_ready` (503 while not completed), `http.handlers.opm_redirect`
(variables, captures, keepPath, auto-redirect), `http.handlers.opm_ws_guard` (400), `http.reverse_proxy.transport.opm_http`
(idle timeouts), `tls.permission.opm_any` (on-demand for "*"). The loaded JSON (secrets redacted) is written to
`<agentHome>/edge/caddy.json`. Load with `caddy.Load(json, false)` (graceful). Failure → previous config keeps running, activity
`edge.reload-failed { revSeq, error }`, the APPLY step fails. Success → `edge.reloaded { revSeq, hosts, routes, durationMs }`.
Not-found page body: `catalog.notFoundPages[notFoundPageId].htmlCode`, or (null) the built-in page:
`<!doctype html><html><head><meta charset="utf-8"><title>Not found</title></head><body style="font-family:sans-serif;text-align:center;padding:10vh">
<h1>404</h1><p>This page does not exist.</p></body></html>` (503: same page, title "Not available", h1 "503", text "This service is starting. Try again in a moment.").
The built-in 413 page: title "Too large", h1 "413", text "The request body is too large."; the 500 page: h1 "500", text
"The request could not be routed (rewrite loop)." (same layout).
The agent 404 page (unknown /_opm paths): a small page "OPM agent · 404 · Back to /_opm/app".

### 12.4 Edge validation rules (`validateEdge`; Go `validate/edge_routes.go` mirrors it)

**Ports + body.** httpPort 1–65535; httpsPort null or 1–65535; ≠ each other, ≠ agentPort, ≠ agentGuiAliasPort.
`edge.maxBodyBytes` null (unlimited) or an integer 0 – 1 TiB (V_EDGE_BODY).
**Hosts.** Hostnames non-empty, unique (case-insensitive; V_DUPLICATE), a DNS name / `*.x` wildcard / IPv4 / IPv6 / `*` (or an
expression) (V_EDGE_HOST). TLS (V_EDGE_TLS): acme not for IP or `*`; acme needs email; wildcard needs dns-01; dns-01 needs
dnsProvider + an existing dnsTokenSecretSlug (DNS providers below); http-01 needs httpPort 80; tls-alpn-01 needs httpsPort 443
(+ warning V_EDGE_ACME_INTERNET); files needs an existing certFileSlug + keySecretSlug; redirectHttpToHttps needs tls ≠ none
and httpsPort. Host `responseHeaders` → the response header rules below.
**Routes.** routeId `^[a-z0-9][a-z0-9-]{0,62}$` (V_FORMAT), unique (V_DUPLICATE); order integer ≥ 1 (V_RANGE); `match.kind` ∈
prefix | exact | regex (V_ENUM); prefix / exact path starts with "/", no space / "?" / "#" (V_EDGE_ROUTE), not `/_opm` or
under `/_opm/` (V_RESERVED); regex path → `checkEdgeRegex` (V_EDGE_REGEX); `stopRegex` boolean (V_TYPE), only on prefix
routes (V_EDGE_ROUTE); two routes with the same (kind, path, hosts, methods) → V_DUPLICATE "route B has the same match as
route A (it would never be used)"; route hosts ∈ edge hosts (V_REF); methods ⊆ GET POST PUT PATCH DELETE HEAD OPTIONS
(V_ENUM); action.type ∈ proxy | redirect | rewrite (V_ENUM).
- **proxy**: target = a container (containerSlug) of this site (V_REQUIRED / V_REF) + a port of its image (V_REQUIRED /
  V_REF) whose port row has `edge: true` (missing row = true; else V_EDGE_ROUTE "port P of container X is closed to the
  edge"); disabled container → warning V_EDGE_TARGET_DISABLED. connectTimeoutSec 1–3600, sendTimeoutSec / readTimeoutSec
  1–86400 (V_RANGE). stripPrefix / websocket / requestBuffering / responseBuffering boolean (V_TYPE). maxBodyBytes null
  (inherit), 0 (unlimited) or ≤ 1 TiB (V_EDGE_BODY). addPrefix null or starts with "/" (V_EDGE_ROUTE). stripPrefix on a
  regex route → V_EDGE_ROUTE ("use rewrite"); stripPrefix + rewrite → V_EDGE_ROUTE; rewrite regex → V_EDGE_REGEX,
  replacement non-empty (V_REQUIRED), may hold `edge` expressions. setHeaders / removeHeaders names are HTTP tokens
  (V_FORMAT); values strings (V_TYPE), may be `edge` expressions; a hop-by-hop name → warning **V_EDGE_HOP** (value ignored);
  a response-only header (`isResponseOnlyHeader(name, value)`: Cache-Control with public / private / max-age / s-maxage /
  no-store / must-revalidate / proxy-revalidate / immutable, Expires, Pragma, Set-Cookie, Content-Security-Policy(-Report-Only),
  Strict-Transport-Security, X-Frame-Options, Access-Control-Allow-* / -Expose-Headers / -Max-Age) → warning
  **W_RESPONSE_HEADER_AS_REQUEST** ("… is set as a REQUEST header (sent to the container), but it only means something on
  responses …"); one-click fix `moveToResponseHeaders(route, name, host)` (moves it to `route.responseHeaders.set`; a
  route that inherited the host's list starts from a COPY of it). Data is never auto-moved.
- **redirect**: `to` non-empty (V_REQUIRED), may be an `edge` expression; status ∈ 301 302 303 307 308 (V_ENUM); keepPath
  boolean (V_TYPE), only on prefix routes (V_EDGE_ROUTE "use $uri / $1 in to").
- **rewrite**: regex → V_EDGE_REGEX; replacement non-empty (V_REQUIRED); `responseHeaders` must be null (V_EDGE_HEADER).
- **Response headers** (host and route, **V_EDGE_HEADER**): `set` / `add` objects of name → string (V_TYPE; names V_FORMAT;
  values may be `edge` expressions); a name in both set and add → V_EDGE_HEADER; `remove` a list of names; `always` lists
  only names present in set / add.
**V_EDGE_REGEX** (`checkEdgeRegex`): empty or > 1024 chars; lookahead / lookbehind, backreferences (`\1`, `\k<…>`), atomic
groups, possessive quantifiers, `(?P<…>)` (write `(?<…>)`), inline flags other than a leading `(?i)`, `\A \z \Z \Q \E \G \h \p{…}
\u \x{…} \c \0` and the other escapes RE2 and JavaScript read differently, POSIX classes `[[:alpha:]]`; then it must compile.
Warnings: V_EDGE_NO_HOSTS, V_EDGE_TLS_NO_HTTPS, V_EDGE_ACME_INTERNET, V_EDGE_TARGET_DISABLED, V_EDGE_HOP,
W_RESPONSE_HEADER_AS_REQUEST. V_EDGE_SHADOW is no longer raised (order-free nginx matching; an identical match is
V_DUPLICATE). Rules of a stored legacy edge are checked after `normalizeEdge` (§2.2).

**DNS providers (dns-01).** Compiled into the agent (Caddy modules `dns.providers.*`). The secret named by
`dnsTokenSecretSlug` holds either the plain token (single-token providers) or a JSON object with the provider's fields:

| dnsProvider | plain secret → field | JSON object fields (required **bold**) |
|---|---|---|
| `cloudflare` | `api_token` | **api_token**, zone_token |
| `digitalocean` | `auth_token` | **auth_token** |
| `duckdns` | `api_token` | **api_token**, override_domain, resolver |
| `godaddy` | `api_token` (`<key>:<secret>`) | **api_token** |
| `route53` | — (JSON only) | **access_key_id**, **secret_access_key**, region, profile, session_token, hosted_zone_id, … |
| `googleclouddns` | — (JSON only) | **gcp_project**, gcp_application_default (service-account JSON) |
| `namecheap` | — (JSON only) | **api_key**, **user**, api_endpoint, client_ip |

The agent builds `{ "name": "<dnsProvider>", …fields }`; an unknown provider, a missing required field, or a provider config
that Caddy refuses (`caddy.Validate` of the site's edge config in the staging dry run) fails STAGING with a clear error, so it
never reaches the APPLY reload. The cloud keeps `dnsProvider` a free string (the list is enforced on the site).

### 12.5 Certificates

Per host status (`CertStatus`) from CertMagic: issuer, notBefore/After, lastRenewalAt, lastError, nextRenewalAt (acme: 30 days
before expiry; internal: 12 h lifetime, renew at 2/3). Activities `cert.issued`, `cert.renewed`, `cert.failed` (alert). Reported
in the heartbeat (`edge.certs`) and `GET /_opm/api/certs`.

### 12.6 Variables (`expandEdgeVars`, Go `edgeres.ExpandEdgeVars`)

Expanded in redirect `to`, rewrite replacements (rewrite routes and proxy `rewrite`), request `setHeaders` values and
response header values — after the `{{ }}` expressions resolved (§10.2 field `edge`). nginx-style names; an unknown
`$name` stays literal (no escaping needed for other "$").

| variable | value |
|---|---|
| `$uri` · `{path}` | current path, decoded, after internal rewrites (nginx `$uri`) |
| `$args` · `$query_string` | current query without "?" |
| `$is_args` | "?" when there is a query, else "" |
| `{query}` | "?args" or "" |
| `$request_uri` | the original path + query as the client sent them (before rewrites, not decoded) |
| `$host` · `{host}` | request host without port, lower case |
| `$scheme` | http / https |
| `$remote_addr` | client IP (Test a URL shows `{client-ip}`) |
| `$server_port` | the edge port that accepted the request |
| `$http_<name>` | request header `<name>` (`_` → `-`, case-insensitive); missing → "" (Test a URL shows `{header x}`) |
| `$1` … `$9` | captures: redirect `to` = of the regex route; rewrite replacement = of the rewrite's own regex; missing → ""; header values have none |
| `${name}` | = `$name` (to write `${uri}x`) |

A "?" in a rewrite result starts the new query and REPLACES the old one (§12.1 7). Example (client fixture):
`rewrite ^ /x/nodered$request_uri last` → `/api/v0002/device/types?x=1` becomes `/x/nodered/api/v0002/device/types?x=1`.

### 12.7 nginx import (`shared/src/nginx-import.ts`; both GUIs "Import from nginx.conf…")

`parseNginx(text)` → directive tree + parse errors (never throws; quotes, escapes, comments, `${name}` kept inside words).
`importNginx(text, { containers })` → `NginxImportResult { edge, rows, certs, parseErrors, summary }`.
`applyNginxImport(current, result, mode 'append' | 'replace', certBindings)` → Edge. Pure; no I/O; the GUIs write the result
into the site working copy only (saved / proposed as usual).
- **Rows** (one per directive / block, file order, line + context `http › server x › location y`): status `ok` (1:1) · `note`
  (mapped with a difference the user should read) · `default` (OPM does it by itself, nothing to set) · `ignored` (no
  meaning for OPM) · `unsupported` (not mapped; `note` says how to do it) · `error` (mapped, needs input: unknown upstream,
  regex outside the dialect). `target` names the OPM field ("route x-ez-apis · stripPrefix", "host h · responseHeaders.add",
  "edge.maxBodyBytes").
- **Inheritance is evaluated** (http → server → location): scalars (client_max_body_size, proxy_*buffering,
  proxy_*_timeout, proxy_http_version) inner overrides outer; arrays (proxy_set_header, add_header, proxy_hide_header): a
  level with ANY of one directive replaces the outer list of that directive; nginx defaults are made explicit
  (client_max_body_size 1m when http { } has none, proxy_buffering on, proxy_request_buffering on, 60 s timeouts). A route
  body limit equal to the edge's → null (inherit); nginx 0 → 0 (unlimited); http-level 0 → edge null.
- **Servers → hosts**: `server_name` names → EdgeHost (`_` / "" → `*`; `.x.y` → `x.y` + `*.x.y`; regex / trailing wildcard →
  unsupported). A server's add_header / proxy_hide_header → host `responseHeaders`. Routes get `hosts` = the server's names
  unless that server covers every host (or is `*`). `listen` → httpPort / httpsPort (`ssl` flag; address ignored; unix
  socket unsupported). `ssl_certificate` / `ssl_certificate_key` → tls files + an `NginxCertRef` the user binds to an OPM
  file (certificate PEM) + secret (key PEM) (`NginxCertBindings`; unbound → files mode with empty slugs = V_EDGE_TLS until
  picked). A server-level `return 301|302|307|308 https://$host$request_uri` → `redirectHttpToHttps`.
- **Locations → routes**: modifiers `=` exact · none prefix · `^~` prefix + stopRegex · `~` regex · `~*` regex with `(?i)`;
  named / nested locations unsupported. Route ids from the path (`/x/ez-apis/` → `x-ez-apis`, `/` → `root`), regex →
  `regex-<n>`, unique. First rewrite-module directive decides the action: `return 301|302|303|307|308 <url>` (and
  `return <url>` = 302) → redirect; `rewrite <re> <rep> last` (or no flag) without proxy_pass → rewrite route;
  `rewrite … break` + proxy_pass → proxy `rewrite`, or `stripPrefix` when it is exactly `^<loc>(/.*)$ $1` under a prefix
  location `<loc>/`; `rewrite ^|^(.*)$ <url> permanent|redirect` → redirect 301 / 302 (`$1` → `$uri`). `proxy_pass
  http://<name>[:port][/uri]` → container `<name>` + its edge port numbered `<port>` (unresolved → empty target, row error,
  V_REQUIRED); a URI part → stripPrefix + addPrefix (note when nginx would join without "/"); variables or https in
  proxy_pass → unsupported. No `proxy_set_header Host` → `setHeaders.Host = <proxy_pass host[:port]>` (nginx `$proxy_host`).
  proxy_set_header defaults (Host $host / $http_host, X-Real-IP $remote_addr, X-Forwarded-For
  $proxy_add_x_forwarded_for / $remote_addr, X-Forwarded-Proto $scheme, X-Forwarded-Host $host) → `default`; Upgrade
  $http_upgrade + Connection upgrade → `websocket` true (only when both are in the effective list, else websocket false with
  a note); other hop-by-hop → not mapped; empty value → removeHeaders; anything else → setHeaders. `add_header` → response
  `add` (+ `always`), or `set` when the same name is in proxy_hide_header; `proxy_hide_header` → `remove`. add_header never
  becomes a request header.
- **Apply**: replace = the imported edge (ports, maxBodyBytes, hosts, routes) with certificates bound. append = keep the
  current ports / maxBodyBytes / hosts, add the imported hosts that are new, add the imported routes (ids "-2", "-3" … on a
  collision; orders after the current ones; `hosts` scoped to the imported server names unless "*"); an imported proxy
  route that inherited the imported body limit gets it explicitly when it differs from the current edge's; an imported
  route that inherited its server's response headers gets them explicitly when an existing host has different ones. The
  result goes through `normalizeEdge` and `validateEdge` like any edit; the GUIs show it in effective order with its issues
  before Apply.
- Tests: `nginx-import.test.ts` (client fixture → `nginx-import-snapshot.json`; the replace result equals
  `edge-vectors.json edges.client`; the client vectors resolve the same through the imported edge; modifiers, sizes /
  times, inheritance, proxy_pass URI, return / rewrite flags, hosts / listen / https server, append, parser errors).

### 12.8 nginx compatibility matrix

How each nginx directive maps, and how it is verified. Test keys: **V** = `shared/testdata/edge-vectors.json` (59 cases;
TS `edge-vectors.test.ts` + Go `internal/edgeres` TestVectors) · **J** = Go Caddy JSON tests (`internal/edge`
TestBuildCaddyJSON / TestToPlaceholders, every config passes `caddy.Validate`) · **P** = `TestClientEdgeParity` (real Caddy in
the agent process + httptest upstreams) · **D** = `TestEdgeParityDocker` (real agent, fake cloud, real Docker containers per
upstream, fixture imported by `e2e/edge-parity/import.mjs`) · **S** = importer test + snapshot (`nginx-import.test.ts`,
`nginx-import-snapshot.json`). "fixture" = the directive is in `e2e/fixtures/client-nginx.conf`.

| nginx directive | OPM feature | difference | tested |
|---|---|---|---|
| `worker_processes`, `worker_connections`, `events { }` (fixture), `worker_*`, `pid`, `user`, `multi_accept` | — (ignored) | Caddy runs inside the agent process | S |
| `http { }` (fixture) | the site edge | one edge per site | S |
| `server { }` (fixture) | one EdgeHost per server_name; its locations = routes | several servers: routes get `hosts` = the server's names | S |
| `listen 80` / `listen 443 ssl` (fixture) | `edge.httpPort` / `edge.httpsPort` | the address part is ignored (edge listens on every address); unix sockets unsupported | S, V (TLS files on 443 + HTTP on 80), P, D |
| `server_name` (fixture) | `EdgeHost.hostname` | `_` → `*`; `.x.y` → `x.y` + `*.x.y`; regex names / trailing `*` unsupported | S, V |
| `ssl_certificate` / `ssl_certificate_key` (fixture) | tls files: certFileSlug (OPM file) + keySecretSlug (OPM secret) | the path is bound to an OPM file / secret in the import page | S, J, P, D |
| `ssl_protocols`, `ssl_ciphers`, `ssl_session_*`, `ssl_stapling*`, `http2` | — (ignored) | Caddy's modern TLS defaults; HTTP/2 always on | S |
| `return 301 https://$host$request_uri` (server level) | `redirectHttpToHttps` | always 308 | S, V |
| `location /p` (fixture) | route `kind prefix` | plain string prefix like nginx (`/api` matches `/apix`) | V, S, P, D |
| `location = /p` | route `kind exact` | — | V, S |
| `location ^~ /p` | route `kind prefix` + `stopRegex` | — | V, S |
| `location ~ re` (fixture) | route `kind regex` | regex dialect = RE2 ∩ JavaScript (no lookaround / backrefs …, V_EDGE_REGEX) | V, S, P, D |
| `location ~* re` | route `kind regex`, path `(?i)re` | as `~` | V, S |
| `location @name`, nested locations | — (unsupported) | make separate routes | S |
| nginx auto-redirect `/x` → `/x/` (fixture: `/x/ez-terminal`) | built in (proxy prefix routes ending in "/") | — | V, P, D |
| `return 301 $uri/$is_args$args` (fixture), `return 302` / `303` / `307` / `308 <url>`, `return <url>` (= 302) | redirect route; variables + `$1…$9` | `return <code> <text>` (body answers) and 4xx/5xx returns unsupported | V, S, P, D |
| `rewrite ^ /x$request_uri last` (fixture) | rewrite route (internal, re-match, ≤ 10) | a "?" in the result replaces the query (nginx appends args unless it ends with "?") | V, S, P, D |
| `rewrite ^/x(/.*)$ $1 break` + proxy_pass (fixture) | `stripPrefix` (exact strip pattern) or proxy `rewrite` | "?" replaces the query (as above) | V, S, P, D |
| `rewrite … permanent` / `redirect` | redirect 301 / 302 (pattern `^` or `^(.*)$` only) | other patterns: use a regex route with return | S |
| `rewrite … last` next to proxy_pass, server-level `rewrite` | — (unsupported) | use `break`, or a separate location / route | S |
| `proxy_pass http://name:port` (fixture) | proxy `containerSlug` + port numbered `port` (`edge: true`) | the upstream is a container of the site, dialled on its loopback port | V, S, P, D |
| `proxy_pass http://name:port/uri` | `stripPrefix` + `addPrefix` | location `/a/` + `/b` (no trailing "/"): nginx gives `/brest`, OPM `/b/rest` (row note) | S |
| `proxy_pass` with variables, `https://`, `upstream { }` | — (unsupported) | one container per route; edge → container is plain HTTP on the site network | S |
| `proxy_set_header Host $host` (fixture) | default (Host kept) | — | V, S, P, D |
| `proxy_set_header X-Real-IP $remote_addr` (fixture) | default | — | V, S, P, D |
| `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for` (fixture) | default | X-Forwarded-For = the client IP (a client-sent list is replaced, not appended) | V, S, P, D |
| `proxy_set_header X-Forwarded-Proto $scheme` (fixture) | default (+ X-Forwarded-Host) | — | V, S, P, D |
| `proxy_set_header Upgrade $http_upgrade` + `Connection "Upgrade"` (fixture) | `websocket` true | only when both are in the effective list; Caddy handles the upgrade | V, S, P, D |
| `proxy_set_header X-Script-Name /x/pg-admin` (fixture), any other | `setHeaders` (values may use variables) | other hop-by-hop names are ignored (V_EDGE_HOP); empty value → `removeHeaders` | V, S, P, D |
| no `proxy_set_header Host` in the effective list | `setHeaders.Host` = `<proxy_pass host[:port]>` | nginx `$proxy_host` made explicit | S |
| `proxy_http_version 1.1` (fixture) | default | upstreams always HTTP/1.1 + keep-alive (1.0 → note) | S, J |
| `proxy_pass_request_headers on` (fixture) | default | `off` unsupported (use removeHeaders) | S |
| `add_header N v` (fixture, server level) | `responseHeaders.add` (host) | one value per name per level (a 2nd add_header of the same name is unsupported) | V, S, P, D |
| `add_header N v always` | `responseHeaders.add` + `always` | also on the edge's own 404 / 413 / 500 / 503 pages | V, J, P |
| `proxy_hide_header N` (fixture) | `responseHeaders.remove` | — | V, S, P, D |
| `proxy_hide_header N` + `add_header N v` (fixture, s3) | `responseHeaders.set` | — | V, S, P, D |
| add_header inheritance (location list replaces the server's; fixture s3) | route `responseHeaders` ≠ null replaces the host's | — | V, S, P, D |
| `client_max_body_size 100M` (fixture) / `0` | `edge.maxBodyBytes` / route `maxBodyBytes` (0 = unlimited) | ≤ 1 TiB; 413 before the upstream | V, S, J, P, D |
| `proxy_request_buffering on` (fixture) / `off` | `requestBuffering` true / false | `on` buffers ≤ 8 MiB in memory, the rest streams (no temp file) | V, S, J |
| `proxy_buffering off` (fixture) / `on` | `responseBuffering` false / true | text/event-stream is always flushed | V, S, J, P, D (SSE) |
| `proxy_max_temp_file_size 0` (fixture) | default | the edge never writes temp files | S |
| `proxy_connect_timeout 60s` (fixture) | `connectTimeoutSec` (1–3600) | — | V, S, J |
| `proxy_send_timeout 60s` (fixture) | `sendTimeoutSec` (1–86400): idle between writes | — | V, S, J |
| `proxy_read_timeout 60s` (fixture) | `readTimeoutSec` (1–86400): header wait + idle between reads | — | V, S, J, P, D (SSE kept alive, idle cut) |
| `proxy_buffer_size`, `proxy_buffers`, `client_body_buffer_size`, `*_hash_*` | — (ignored) | Caddy manages buffers | S |
| `resolver 127.0.0.11` (fixture) | — (ignored) | the agent resolves containers itself | S |
| `client_body_timeout`, `send_timeout`, `keepalive_*` | — (ignored) | managed by Caddy | S |
| `proxy_redirect off` / `default` | default / note | Location headers of the container are never rewritten | S |
| `limit_except` | route `methods` (by hand) | not imported | S |
| `if`, `map`, `upstream`, `include` (except mime.types), `try_files`, `root`, `alias`, `index`, `error_page`, `auth_*`, `allow` / `deny`, `limit_req` / `limit_conn`, `proxy_cache*`, `sub_filter`, `expires`, `stream`, `fastcgi_*` / `uwsgi_*` / `grpc_*` | — (unsupported) | do it in the container (static files, auth, caching, error pages) or write fixed routes | S |
| `gzip*`, `brotli` | — (unsupported) | the edge does not compress; the container can | S |
| `access_log`, `error_log`, `log_format`, `sendfile`, `tcp_*`, `server_tokens`, `types`, `default_type`, `charset` | — (ignored) | — | S |
| Range requests (s3 store in the fixture) | passed through | — | P, D |
| SSE (`/x/*/real-time/`, fixture) | proxy, buffering off | — | V, P, D |
| WebSockets | proxy `websocket` true | refused with 400 when off | V, P, D |
| 100 MB upload | streamed | bounded memory (~23 MiB heap growth measured) | P, D |

---------------------------------------------------------------------------------------------------------------------

## 13. File formats

### 13.1 `.opmsnap` (snapshot download / import)

A POSIX ustar archive (uncompressed outer tar), entries in this order:
1. `manifest.json` — `OpmsnapManifest` (types.ts) as UTF-8 canonical JSON. `parts[]` lists every following entry except
   `signature.sig` with sizeBytes + sha256. `snapshot.sites[*].config` contains plain secret values (download warns).
2. `sites/<siteSlug>/config.json` — canonical JSON of the site's `SnapshotSite` (config + images + volumes).
3. `sites/<siteSlug>/volumes/<volumeSlug>.tar.zst` — one per archived site volume (`SnapshotVolume { volumeSlug,
   containers, sizeBytes, sha256, path }`; `containers` = the containerSlugs mounting it at snapshot time, informational; a
   volume shared by several containers is archived once): a ustar of the volume's directory WHEREVER its location is (default
   `<agentHome>/volumes/<volumeSlug>/`, or the custom `./…` / absolute path) (relative paths `./…`,
   numeric uid/gid, modes, mtimes, symlinks kept, no device files), zstd-compressed (level 3). Snapshots written before
   round D carry `name` instead of `volumeSlug`; readers take `name` as the volumeSlug.
4. `images/blobs/sha256/<hex>` — only with includeImages: manifest, config and layer blobs of every image in `manifest.images`.
5. `signature.sig` — ASCII base64url (no padding) Ed25519 signature of the exact bytes of `manifest.json`, by
   `manifest.signedBy` (the site that built the download; its public key is in the manifest and must belong to an enrolled
   site of this deployment at import).
Import checks (any failure → 422 invalid, nothing stored): format `opmsnap/1`; deploymentId = this deployment; signature;
every part present with matching size + sha256; no extra entries; every image digest is present on the sites, in the file,
or in the head/catalog of a stored revision (else invalid "image … missing"). An imported snapshot keeps its original
snapshotId; importing an id that already exists → 409 conflict. reason becomes `imported`, createdBy
= the importing actor, note kept.

### 13.2 Snapshots on the sites

- Directory `snapshots/<snapshotId>/`: `manifest.json` (this site's view: the Snapshot with ONLY this site's entry in
  `sites` + `revision`), `sites/<slug>/config.json`, `sites/<slug>/volumes/…tar.zst`.
- Taking a site part: free-space check first (free ≥ 1.2 × Σ current volume sizes to archive; else fail "free space X <
  1.2 × Y"); stop the containers that mount the volumes to archive (reverse depends order) — for manual / scheduled
  snapshots the containers mounting a volume are stopped around that volume's archive and restarted at once; for pre-update
  snapshots they stay stopped (they will change anyway); archive; record sha256 + size.
- Volumes — keyed by `volumeSlug`; only DECLARED site volumes with `backup = true` are ever archived. Host paths, `none`,
  file and secret mounts, rendered files, `backup = false` volumes and orphan directories are NEVER archived.
  - manual + scheduled → EXACTLY the declared volumes of the site (head config) with `backup = true`; a volume whose
    directory does not exist yet is skipped (not listed).
  - pre-update → the `backup = true` volumes of the HEAD config mounted by containers that the update recreates or removes,
    plus the `backup = true` volumes of the head that the NEW config mounts in a container that is created or recreated
    (all mounting containers listed in `SnapshotVolume.containers`).
  - imported → as in the file.
  Rendered files are not archived: a restore re-renders them from the snapshot config + file blobs. A restore puts every
  volume of the snapshot back into the location that volume has in the config being restored (default
  `<agentHome>/volumes/<volumeSlug>/`; whatever container mounts it now) and leaves every other directory untouched; moving a
  volume to another container keeps its data (same directory). Host-path mounts that are not declared volumes and the
  per-site file / secret copies are never backed up (files and secrets come from the config). Plans report
  `volumesRestored { siteSlug, volumeSlug, sizeBytes }` / `volumesKept { siteSlug, volumeSlug, backup }` (§14.6).
- Retention (per site, after each snapshot): keep the 10 newest pre-update, the 7 newest scheduled, all manual + imported;
  never delete the pre-update snapshot of a running update.
- Images referenced by a kept snapshot are never pruned (blobs + docker images).

### 13.3 `.opmlog` (activity bundle)

ustar archive, entries:
1. `manifest.json` — `OpmlogManifest` (canonical JSON): sites with seq ranges, `activities { path, sizeBytes, sha256 }`,
   `debug[]`, `signedBy { siteSlug, publicKey }`.
2. `activities.ndjson` — one `Activity` per line (canonical JSON, `\n` after every line), sorted by siteSlug then seq;
   `uploadedVia`/`uploadedAt` null.
3. `debug/<siteSlug>.jsonl` — optional, the debug lines (§13.4) of the range.
4. `signature.sig` — base64url Ed25519 signature of the exact `manifest.json` bytes by `signedBy`.
File name: `<deploymentId>-<siteSlug|all>-<fromSeq>-<toSeq>.opmlog`.

### 13.4 Debug log

`logs/agent-YYYY-MM-DD.jsonl`, one JSON per line `DebugLine { ts, level, module, msg, fields }`; modules: cloudlink, mesh,
stager, distrib, expr, driver, edge, health, coordinator, snapshot, pipe, transfer, tunnel, auth, gui. Rotate daily; keep 7
days or 200 MB total (delete oldest). Never secret values (the logger redacts fields named *password*, *secret*, *token*,
*key* and every secret-tainted env value).

### 13.5 Activity hash chain

`hash = "sha256:" + hex(sha256(activityHashInput(entry)))` where the input is `(prevHash ?? "") + JCS({ activityId,
deploymentId, siteSlug, seq, ts, actor, origin, type, summary, data })` (`canonical.ts`). seq 1 has prevHash null. Vectors:
`canonical-vectors.json` (`activities`). `data` ≤ 16 KB canonical JSON, never secret values.

---------------------------------------------------------------------------------------------------------------------

## 14. State machines

### 14.1 Revisions (per deployment, on the sites)

Linear chain `head → child`: a revision is applied only if its `parentRevId` equals the head of EVERY participating site;
`seq = parent.seq + 1` (first = 1, parent null). Revisions are immutable. Kinds: `change` (cloud publish / local edit),
`rollback` (summary `rollback to <snapshotId> (seq n)`, rollbackOf).

### 14.2 Proposal status

> **SUPERSEDED by §21 #104 (agent ≥ 0.14, cloud with branches)** — kept for agents < 0.14 (compat proposals) and as history. Current model: docs-v2/branches-commits.md.


| from | to | who / when |
|---|---|---|
| (new) | published | cloud publish, or agent local create |
| published | staging | any site reports staging ≠ pending |
| staging | ready | every site in `sites` reports ready |
| ready | staging | a site re-stages / falls back (restage, blob lost) |
| ready | approved | coordinator, after ACCEPT (approvedBy, approvedAt, updateId) |
| approved | applying | coordinator, after LOCK succeeded on every site |
| approved | ready | coordinator, LOCK failed (no change made) — result `{ ok:false, message }` kept in `result` |
| applying | applied | coordinator, after COMMIT on every site |
| applying | failed-rolled-back | coordinator, after ROLLBACK (result.message = reason, snapshotId) |
| published · staging · ready | stale | head ≠ parentRevId (cloud on reportHead; agents on arrival / after another apply) |
| published · staging · ready | superseded | a newer proposal (greater revId) exists |
Terminal: applied, failed-rolled-back, stale, superseded. A site with staging `failed` keeps the proposal in `staging`
(the gate is closed) until `restage` or a newer proposal. At most ONE proposal per deployment is approved/applying.

### 14.3 Staging per site

`pending → downloading → ready | failed`; `failed → downloading` on restage; any → (ignored) when the proposal is terminal.

### 14.4 The gate (Approve / Roll back button visible)

> **SUPERSEDED by §21 #104 (agent ≥ 0.14, cloud with branches)** — kept for agents < 0.14 (compat proposals) and as history. Current model: docs-v2/branches-commits.md.


All true, evaluated by the agent the person uses, from live peer pings:
(a) **parent** — every participating site's head = proposal.parentRevId (rollback: no condition);
(b) **reachable** — every site of (head config ∪ target config) is enrolled (in the peer table) and answered a ping in the
last 30 s (self always) — a new site must be installed first;
(c) **staged** — every such site reports staging `ready` for this revId (rollback: the snapshot is complete on every site of
its config and every needed image exists there);
(d) **idle** — no site holds a lock;
(+) **status** — the proposal is `ready` (not terminal).
`GateCheck.conditions` lists each with ok + detail; the GUI shows them as a checklist.

### 14.5 Update protocol (coordinator = the approving agent)

Plan (`UpdatePlan`): updateId (ULID), kind, revision, fromRevId, preSnapshotId (ULID), sites (= head ∪ target sites that are
enrolled), order = sites removed by the target first (slug order), then `siteOrder(target config)`. Steps, each sent with
`POST /_opm/peer/update/{updateId}/step` (self: in process) and awaited by polling `GET /_opm/peer/update/{updateId}` every
2 s (timeouts: lock 10 s, snapshot 30 min, apply 30 min per site (§21 #110; was 15), verify 5 min, commit 30 s, rollback 30 min; a site cancels its own step at its timeout + 1 min, §21 #110):
1. **LOCK** (all sites, parallel): each site checks head = plan.fromRevId (a lock of ANOTHER update is stopped there, never refused — §21 #110), stores the plan, `kv.lock`. Any refusal or
   unreachable → UNLOCK the locked ones; proposal back to `ready` with result message; activity `update.failed { step: lock }`.
   Success → proposal `applying` (reported to the cloud by the coordinator; peers via ping).
2. **SNAPSHOT** (all sites, parallel): pre-update snapshot `preSnapshotId` (§13.2). Failure → ROLLBACK (nothing changed yet:
   restart stopped containers) → failed-rolled-back.
3. **APPLY** (sites one by one in `order`): the site computes its container actions against the running containers
   (§15.3): removed → stop + remove (reverse start order); recreated/created → in the START ORDER (§15.5), each started
   once its explicit + implicit depends are completed — every other readiness wait runs IN PARALLEL (§21 #93: never one
   container after the other); unchanged → keep; then writes not-found pages, reloads the edge, re-binds agentPort / alias if changed. The step ends
   when every enabled container of the site is completed (§15.4; per container 300 s or the health check's start period, §21 #109; unhealthy = fail fast) → ok, else failed.
   A site removed from the config stops + removes all its OPM containers (volume directories stay) and shuts its edge.
   Site volumes: the directory (kind `file`: an empty file) at the location of every volume the new config mounts is created
   (0755) if missing, then new / empty volumes with a seed are seeded once (§2.2 "Starter content", before any container
   starts, after snapshot restores) (a
   round-D part-1 `vol/<volumeSlug>/` is moved to the location first when the target does not exist); a volume removed from
   the config keeps its directory (orphan; never deleted automatically). Files and secrets: the staged per-site copies
   (§9.5 step 4) are swapped into their locations atomically (rename) before the containers that mount them start; a
   rollback restores the previous copies the same way.
4. **VERIFY** (all sites): every enabled container still completed (running jobs: exited 0) — catches crash loops.
5. **COMMIT** (all sites): head = revision, store revision + catalog, unlock, activity `update.applied { revId, seq,
   durationMs }` (each site in its own log); coordinator reports `applied` + result `{ ok: true, message: "applied", snapshotId:
   preSnapshotId }` and `POST /v1/agent/head` (every site reports its head too).
ROLLBACK (any failure in 2–4, or coordinator restart mid-update): on every site in REVERSE order: stop containers of the
target revision that changed, restore the pre-update snapshot (previous config, previous images (by digest), restore the
archived volumes, restart in depends order, reload the previous edge), then unlock. Proposal `failed-rolled-back`, result
`{ ok: false, message: "<step> failed on <site>: <error>", snapshotId }`, activities `update.failed { step, siteSlug, containerSlug?,
error }` and `update.rolled-back { revId, snapshotId, durationMs }`. A site that cannot be reached during rollback keeps its
lock; it rolls itself back when it sees (ping) that the coordinator's lock is gone and its head ≠ new revision (the
`abandonUpdate` path, automatic after the coordinator answers again).
Every step writes an activity on the site that ran it: `update.started` (coordinator), `update.snapshot`, `update.applied` /
`update.failed`, `update.rolled-back`.

### 14.6 Rollback to a snapshot

Gate (b)(c)(d) + 60 s + ACCEPT. The coordinator builds a revision `{ kind: 'rollback', parentRevId: head, seq: head.seq + 1,
config: snapshot config, catalog: the snapshot revision's catalog, origin 'agent', actor email, summary "rollback to
<snapshotId> (seq <n>)" }` and runs §14.5 with kind rollback: SNAPSHOT (pre-update of the current state) → APPLY = restore the
snapshot images + config + volumes of the snapshot and restart → VERIFY → COMMIT. Volumes are restored BY `volumeSlug`:
every volume in the snapshot is restored into its location (default `<agentHome>/volumes/<volumeSlug>/`; whichever container
mounts it now — a volume that
moved to another container keeps working; a pre-D snapshot's `name` is read as the volumeSlug); every other directory is
untouched. The plan (`rollbackPlan`) lists `volumesRestored { siteSlug, volumeSlug, sizeBytes }` and `volumesKept { siteSlug,
volumeSlug, backup }` (declared volumes of the current head not in the snapshot: backup off, new since, or no data then).
Activities `snapshot.rolled-back { snapshotId, newRevId }`. Data written after the snapshot is lost on the restored volumes.

### 14.7 Other machines

- Image import: `pending → copying → ready | failed`; `failed → pending` (retry).
- Transfer: `uploading → ready → saved-on-server | downloaded`; any → `expired` (= deleted; transfers never expire, only Delete removes one). to-site `ready →
  saved-on-server` (agent save) or `downloaded` (browser download in the agent GUI); to-cloud `ready → downloaded` (cloud download).
  §21 #100 store-and-forward: to-site `ready` + `fetchedAt` null = stored in the cloud, waiting for the site; `fetchedAt` set =
  on the site (not a status). Agent-local `delivery.phase`: to-cloud `queued ⇄ uploading → stored` (stored = cloud status
  ready / downloaded); to-site `pending ⇄ downloading → on-site`.
- Upload: `open → complete | failed` (expired after 24 h → failed).
- Install token: `active → used | revoked | expired`.
- Agent session: `active → expired (6 h) | logged-out`.
- Tunnel lease: `active → stopped | revoked | expired`.
- Intent: `waiting (< notBefore) → confirmable → used | cancelled | expired (notBefore + 10 min)`.

---------------------------------------------------------------------------------------------------------------------

## 15. Container driver (Docker Engine API)

### 15.1 Names

- Network per site: `opm_<deploymentId>_<siteSlug>` (bridge; created if missing; never removed while containers use it).
- Container name (§21 #88): agent.json `containerNames` — `"plain"` (default): the docker name = `<containerSlug>`, exactly
  what the site config declares (`docker ps` shows `mqtt`, `backend`); `"prefixed"`: `opm_<deploymentId>_<siteSlug>_<containerSlug>`
  (several sites / deployments on ONE Docker host: the e2e harness, the mesh tests, opm-devmesh). Network aliases on the site
  network = `<containerSlug>` (the DNS name, hashed) AND `opm_<deploymentId>_<siteSlug>_<containerSlug>` (backward
  compatibility; not hashed). Two instances of the same service on one site are two containers with two names / aliases and
  the same image.
- The name is never part of the spec hash (§15.2) and never used to FIND a managed container: every lookup (plan, status,
  logs, exec / health checks, tools, failure capture, snapshots, prune) goes by labels (`opm.deployment` + `opm.site` +
  `opm.container`) and then by docker id. A name lookup (create, staging check) matches the exact docker name in
  `GET /containers/json` — never `GET /containers/{name}/json`, which also matches id prefixes.
- **Name migration**: at agent start (after the engine answers) and before every APPLY plan, every managed container of the
  site (by labels; kept `-failed-` copies excluded) whose docker name ≠ the name of the mode is renamed in place
  (`POST /containers/{id}/rename?name=…`): same id, same state, same spec hash → kept, never recreated. A desired name held
  by another container is left alone (logged; staging reports the clash). A rename keeps the endpoint aliases (verified on
  Docker 28.5.2 and 29.5.2, §21 #93: Aliases and DNSNames still hold `<containerSlug>`; DNSNames swaps the old name for the
  new one), so a migrated container answers on `<containerSlug>` and its new name. **Alias self-check** (§21 #93): after
  every APPLY (also a rollback) and at agent start (after the migration) every RUNNING managed container must hold
  `<containerSlug>` in its site-network endpoint (Aliases or DNSNames); a container missing from the network or without the
  slug is disconnected + reconnected with `[slug, opm_<dep>_<site>_<slug>]` (its IP may change; warn in the debug log
  "slug alias repaired"). A managed container is never left without its slug alias.
- **Name clash** (staging dry run §9.5 + create): the docker name is free, or held by a container labelled for THIS
  deployment + site (a stale one is removed at create). Held by anything else (an unmanaged `docker run --name mqtt …`, or a
  managed container of another deployment / site) → staging fails: "Docker container name `mqtt` is already used by a
  container not managed by this site (id …, image …[, managed by OPM deployment D site S]) — remove or rename it, or set
  containerNames: prefixed in agent.json". Unmanaged containers are never touched.
- Labels: `opm.deployment`, `opm.site`, `opm.container` (containerSlug), `opm.service` (serviceSlug), `opm.rev` (revId),
  `opm.spec_hash`. Status, logs and uptime find a container by `opm.container`.
  Containers without `opm.deployment` = this deployment are never touched ("not managed" on the status page).

#### 15.1.1 Docker image names

- Every loaded image has two references:
  - **internal** `opm/cfg:<configHex>` (config digest without `sha256:`): the integrity anchor. After `docker load` the agent checks
    that the loaded rootfs `diff_ids` equal the config's `diff_ids`.
  - **readable** `<serviceSlug>:<version>` (e.g. `northwind-backend:v1`): this is the name that `docker ps` / `docker images` show.
    It is the OPM version (Image document id), not the source docker tag. The agent sanitizes the name to the docker
    reference rules: the repository is lower case `[a-z0-9._/-]`, the tag is `[A-Za-z0-9_.-]{1,128}` and does not start with
    `.` or `-`. Invalid characters become `-`. Valid slugs and versions never change.
- Containers are created from the readable reference, but only after the agent checks that it has the same image id as
  `opm/cfg:<configHex>`. If the readable reference was overwritten (a manual `docker tag` / `pull`), the agent tags it again
  from the internal reference and logs `image re-tagged`.
- The readable name is not part of the spec hash (§15.2 hashes the config digest). Registering the same image as a
  new version therefore does not recreate the container.
- `ContainerStatus.image` = the readable reference (unmanaged containers: their docker image).
- Pruning (hourly housekeeping): an image is pruned when its config digest is used by none of the head, an open proposal or a
  kept snapshot, and no container uses it. The agent then removes the readable tag(s) and `opm/cfg:<configHex>`. The image
  can be loaded again from the blobs while they exist.
- Old-shape revisions are refused with a clear error and are not migrated: containers with `serviceId` / `tag`, or a catalog
  not keyed by `serviceSlug` / `serviceSlug:version`.

### 15.2 Container spec (what is hashed)

`spec = { image: configDigest, entrypoint: container.entrypoint ?? image.entrypoint, cmd: container.command ?? image.command,
workdir, user, env: sorted "NAME=value" (image defaults overlaid by container env; resolved; secretRef → value), mounts:
sorted [{ type bind, source, target, readOnly, sha256 (file mounts: sha256 of the bytes of the site copy — the content for
render none, the RENDERED bytes for render expressions; secret mounts: sha256 of the value) }], ports: sorted [{ containerPort, protocol, hostIp ("0.0.0.0" | "127.0.0.1" from the port row), hostPort }]
(+ loopback publications for every image port), healthcheck: container.healthcheck ?? image healthcheck, restart: { always |
on-failure | no }, resources: { nanoCpus, memoryBytes }, stopTimeoutSec, extraHosts: ["host.docker.internal:host-gateway"],
network + alias (containerSlug), labels (without spec_hash) }`.
`spec_hash = sha256(JCS(spec))` hex. Env values from secrets and rendered file contents enter the hash (hash only, never
stored). The auto site address is not hashed by itself: only values that USE it (resolved env / rendered files) change the hash.
Mounts (`containerMounts`: one per image volume that has a row with a source ≠ none, plus the extra mounts; an image volume
WITHOUT a row gets NO mount — no implicit data volume), by source kind:
`volume` → bind the directory at the declared site volume's location (`locationPath(location, 'volumes', volumeSlug,
agentHome)`, default `<agentHome>/volumes/<volumeSlug>/`; created 0755; the same volumeSlug on several mounts / containers =
one shared directory);
`host` → bind of the literal host path (must exist, else the dry run fails); `file` → bind of the SINGLE host file at the
file's location (default `<agentHome>/files/<fileSlug>`; render none = the content, 0444; render expressions = rendered once
for the site, 0400 when it holds a secret in clear else 0444 — §21 #103), read-only — every container of the site that mounts the file shares that
one copy; `secret` → bind of the single host file `<agentHome>/secrets/<secretSlug>` (0400), read-only, shared the same way;
`none` → no mount (Docker keeps the
image VOLUME as container storage, lost on recreate; the same for an image volume without a row). `readOnly` from the row
(volume + host default false = read-write; file + secret always true). Every mount target is the RESOLVED mount path (§10.5 container resolution: env
first, then mount paths); env values in the spec are the resolved values (image defaults incl. the options default, with
container overrides on top). A resolution error or an unsafe resolved path (V_PATH) fails the build (staging dry run / apply).

### 15.3 Recreate rule

Running container with same spec_hash → keep. Different hash → stop (SIGTERM, stopTimeoutSec, SIGKILL), remove, create, start.
In config, not running → create, start. Running, not in config (or disabled) → stop, remove (data stays on disk).

### 15.4 Readiness ("completed")

| kind | health check | completed when |
|---|---|---|
| service | set | passes once after start; failures during startPeriodSec do not count; `retries` failures in a row after it → unhealthy (fail fast) |
| service | null | running for 10 s with no restart |
| job | — | exited with code 0 (non-zero → fail) |
| any | — | not completed within max(300 s, startPeriodSec + retries × (intervalSec + timeoutSec)) → fail (§21 #109) |
| site | — | every enabled container completed (empty site: at once) |
Checks are run by the agent: http = GET `http://127.0.0.1:<loopback port><path>` expects status; tcp = connect; exec =
Docker exec, exit code; log = RE2 match on stdout/stderr since start. Interval / timeout from the image health check
(fractional seconds allowed). After VERIFY the agent keeps checking every interval; the edge `opm_ready` uses the latest result.
**Exact semantics (§21 #87):** the port map is re-read with `docker inspect` before EVERY check (never cached); the loopback
port = the 127.0.0.1 publication, else a publication on 0.0.0.0 / "" / "::" reached via 127.0.0.1. A container Docker is
restarting (`State.Restarting`, restart-policy backoff: Docker then reports Running = true and an EMPTY port map) is a
failed check "container is restarting: last exit code N, M restarts" — never a port message. No host publication yet (port
map empty) = "port X has no host publication yet (docker ports: …)". Inside startPeriodSec nothing counts; after it `retries`
consecutive failed checks → unhealthy ("N checks failed in a row after the S s start period: <last>"); type none (no check)
= running 10 s without a restart (a restart resets the wait). Every check is recorded (time, pass/fail, counted, message;
last 30) for the failure diagnostics below.
**Failure diagnostics (§21 #87):** when a container fails APPLY (create / start / readiness) or VERIFY, the agent captures —
BEFORE the rollback touches it — the last 200 log lines (stdout + stderr, deployment secrets redacted), the inspect state
(status, exit code, OOMKilled, restart count, error, started / finished), the port map and the health check history into
`UpdateSiteState.containers[].failure` (`ContainerFailure`), the debug log (warn, last 20 lines) and the `update.failed`
activity (`data.failures[]`: containerSlug, error, exitCode, oomKilled, restartCount, ports, keptAs, logTail ≤ 40 lines,
the list ≤ 12 KiB — the activity reaches the cloud Activities through the pipe). The failed container is stopped and
renamed `<name>-failed-<unix>` (`<name>` = its docker name in the current mode, e.g. `mqtt-failed-1760000000`; it keeps its
labels, so it stays label-managed: excluded from every plan and from the name migration; removed after the next
successful update of the site or after 24 h by housekeeping).
**DNS hint (§21 #93):** the captured log tail (ANSI removed) is scanned newest-first for a DNS failure that names ANOTHER
container of the site (`dnsFailureHint` in shared/src/depends.ts, Go `validate.DNSFailureHintOf`; patterns `getaddrinfo
EAI_AGAIN|ENOTFOUND <host>`, Go `lookup <host>[ on …]: no such host|server misbehaving|Temporary failure in name
resolution`, curl `Could not resolve host: <host>`, `<host>: Name or service not known|Temporary failure in name
resolution`, and a host-less error line with a slug mentioned as a hostname within 3 lines). External hosts and the
container itself never match. A match sets `ContainerFailure.hint` = "northwind-backend-admin could not resolve `mqtt`
(EAI_AGAIN) — is mqtt running/healthy? add it to Depends on", `failures[].hint`, and the step error becomes
"<hint> (<readiness error>)" (→ the `update.failed` summary and the proposal result).

### 15.5 Start order, implicit and suggested depends (§21 #93)

Normative: `shared/src/depends.ts` (Go `agent/internal/validate/depends.go`; vectors `shared/testdata/depends-vectors.json`).
- **Explicit** = `Container.depends` (V_CYCLE when they form a cycle — unchanged).
- **Implicit** = another ENABLED container of the site referenced by `containers.<slug>.*` in the container's env values,
  its image env defaults (variables the container does not set) or a mounted file with render `expressions` (when the
  content is known: agent staging / apply, cloud editor preview). Implicit depends behave like explicit ones (start after
  + wait until completed); the GUIs show them as read-only "implicit (from expression)" chips with their sources
  ("env REDIS_HOST", "image env BROKER", "file app-conf").
- **Suggested** = a literal mention of another enabled container's slug that looks like a hostname, in `NAME=value` of
  the env (expression blocks skipped) or the text of a mounted file: the slug as a whole hostname (no `[A-Za-z0-9_.-]`
  before, no `[A-Za-z0-9_-]` / `.x` after) AND (a URL authority `scheme://[user@]slug`, or `slug:<1–5 digits>`, or after a
  key containing host / server / broker / addr / endpoint / upstream / url / uri / dsn: `host: "mqtt"`, `MQTT_HOST=mqtt`).
  Neither explicit nor implicit. Validation: info `V_DEPENDS_SUGGESTED` "northwind-backend-admin mentions `mqtt` (env
  MQTT_URL) — add it to Depends on?" (container editor + publish review offer a one-click Add; nothing changes
  automatically). Suggested depends only ORDER the start (no wait).
- **Start order** (`startOrder`): Kahn over explicit ∪ implicit ∪ suggested edges; among the ready containers the first
  in the DECLARED order (`site.containers`) goes next. When none is ready (a cycle through implicit / suggested edges) the
  left-over container with the lowest declared index whose explicit depends are placed goes next; those containers are
  reported (warning `V_START_ORDER_CYCLE`, debug log) and start in the declared order. A container never waits for a
  container that starts after it.
- **APPLY** (§14.5 step 3): containers are created / started one by one in the start order; a container waits only for
  its explicit + implicit depends (`waitDepends`) to be completed; every readiness wait runs in parallel; the first
  failure stops launching (an update) and cancels the other waits; a rollback runs best effort. Kept + ready containers
  are not waited for.

---------------------------------------------------------------------------------------------------------------------

## 16. Agent status + uptime

Samples every 30 s: site (up when every enabled container is ready), each container instance (subject = containerSlug;
up = running + ready; degraded = running not ready; down). Kept raw 24 h, hourly 30 days. `SiteStatus.uptime24h` = 144 cells of 10 min, `uptime30d` = 720 cells of
1 h (cell `up` = fraction of up samples, samples 0 = no data → grey). Host metrics from /proc (Linux) or sysctl (darwin):
CPU %, memory, disk of agentHome, load. Cloud: online = heartbeat < 90 s, UptimeDay hourly fractions.

---------------------------------------------------------------------------------------------------------------------

## 17. Cloud frontend (SvelteKit SPA) — every route

### 17.0 UI rules (both GUIs)

- **One form per entity (§21 #102).** Every entity form that exists in both GUIs is ONE component of the shared `ui/`
  package (`$opm/forms/...`), rendered by the cloud (create / edit / read) and the agent GUI (read / local edit) through a
  `ConfigHost` adapter; read = the same form disabled, locked fields show a lock + the reason. New forms for such entities go
  into `ui/` — never a second copy in one app.
- **Container rule** for every add / edit form: ≤ 4 fields → **modal**; 5–12 fields → **side drawer** (right, 480 px, 720 px
  for code editors); > 12 fields or nested lists → **full page** (own route). Confirmations are modals; destructive ones have
  a red primary button; "typed confirm" means the primary button is enabled only when the typed text matches.
- Every list: loading skeleton, empty state (text + the primary add button), error state (message + Retry).
- Every write: button spinner, success toast, error toast with the API message; `invalid` → the issues are shown next to
  the fields (path → field mapping) and in a summary list at the top of the form.
- Client validation uses the same functions as the API (`@opm/shared` validate / access / expr). Save buttons are disabled
  while there are errors; warnings never block. Severity `info` (round E, e.g. V_VAR_OPTIONAL_NULL) shows as a neutral note
  ("n notes"), never counted as a warning (issue headers, sidebar / tree badges, publish-warning chip).
- **Required variables** (round E, §21 #78): a required variable / secret without a value shows `DEPLOY_PLACEHOLDER` as a
  dashed "set at deployment" token in every resolved preview (value inputs, container resolved table, file previews, site
  host) — never a warning in a template.
- Optimistic concurrency: template PATCH (ifVersion) and draft PUT / publish (ifRevision). On 409 conflict → modal
  "Someone else changed this (now version N)": [Reload] (discard mine) · [Keep my edits] (reload server state and re-apply my
  edited sections, then save again).
- Unsaved changes + navigation → modal "Leave without saving?" [Stay] [Leave].
- **RevealValue** (§21 #84 — every credential stays viewable; frontend `src/lib/ui/RevealValue.svelte`, the agent GUI has the same
  file name): the ONLY way a credential value (access key, install command, secret, registry password, account password,
  reset link) is shown. Masked by default (`••••` + a length hint such as "24 chars" when known). **View** calls the reveal
  endpoint (which logs: activity and / or audit) and shows the value with a visible 60 s countdown ring (`REVEAL_SECONDS`,
  the response's `hideAfterSec`), then masks again automatically — also on route change and when the tab becomes hidden.
  **Copy** while visible. The value lives in component memory only (never localStorage / sessionStorage / the URL).
  **Regenerate…** where it applies: a confirm dialog (typed ACCEPT for the access key and the account password; plain confirm
  for install commands and secrets) → the regenerate endpoint (logged) → the new value shows for 60 s with the same
  countdown. A value that is not stored (pre-#84 access key / install token) shows "not stored — regenerate to view" +
  Regenerate. After a create (deployment, account, install command) the returned value simply shows in RevealValue with its
  timer — no modal, no checkbox. 429 `rate-limited` → error toast with the wait (`retryAfterSec`).
- Actions the account may not do are hidden (`can()`); read-only screens show a "read only" chip.
- Dates: relative ("5 min ago") with the ISO time in a tooltip. Ids: monospace with a copy button.
- Routes use SvelteKit path params (SPA; Firebase Hosting rewrites `**` → `/index.html`). Every route param is the entity's
  one identifier (`{serviceSlug}`, `{version}`, `{registryId}`, …).
- **Pickers rule.** A field that refers to something that exists (service, image version, registry, repository, docker tag,
  icon, variable, secret, file, site, container, port name, not-found page, tag, account) is a picker (select, combobox with
  search, or chip list), never free text. Free text only for labels, descriptions, paths, hostnames, HTML / code and values.
  The field → control table is in `frontend/TEAM.md`.
- **IconPicker** (every icon field: service, template, tag, deployment logo, account avatar): a button with a preview opens a
  modal with two tabs: **Icons** (the curated Lucide set, searchable, grouped; stores `lucide:<name>`) and **Uploads** (every
  uploaded icon from `listIcons`, to reuse; **Upload** button + drag-drop, png / svg / webp ≤ 64 KiB, checked before the
  upload; same bytes → the existing icon; delete of an unused upload; stores `upload:<iconId>`). **None** stores null (the
  entity default icon; avatars: initials).
- **Overlay rule.** Every overlay (modal, drawer, popover, listbox, menu, picker, tooltip, toast) renders at `document.body`
  (the `portal` action; popovers are positioned with `position: fixed` next to their anchor), so no ancestor (sticky header,
  transform, `overflow: hidden`) can clip or cover it. One z-index scale (tokens.css): `--z-sticky 20 < --z-header 30 <
  --z-scrim 35 < --z-sidebar 40 < --z-overlay 1000` (modals / drawers stack +10 per level, `overlay.ts`) `< --z-popover 5000 <
  --z-toast 6000 < --z-tooltip 7000`. Esc closes only the top overlay; focus is trapped in it; the body does not scroll.

### 17.1 Root layout

**Scopes.** The left sidebar shows ONE scope at a time: the **root scope** (below), the **template scope** (inside
`/app/templates/{templateId}/…`, §17.2) or the **deployment scope** (inside `/app/deployments/{deploymentId}/…`, §17.3). There
are no navigation tab rows in the main area (round D: the template tab row and the site tab row were replaced by the
sidebar; in-page tabs such as the Diff sections stay).

Root sidebar: Deployments · Templates · Services · Registries · Not-found pages · Tags · Accounts · Audit log (admins only).

**Sidebar footer** (every scope, always present): account chip (avatar, name) → menu: My profile → **modal** (Name 1–80,
Phone E.164 or empty, Avatar (IconPicker with uploads; None = initials; a legacy https URL is kept); PATCH /v1/accounts/{me}) ·
Log out (Firebase signOut → /login); and the light / dark **theme toggle**. The account menu exists only there (never also in
a top bar). The menu and the My profile modal portal to the body (Overlay rule), above the sidebar.

**Top bar.** Inner scopes (template, deployment) have NO global top bar: their pages start with the **breadcrumb row**
(e.g. Templates › Single › site-01 › Containers) followed by the **page header** (title + that page's actions: Save template /
Save draft, Add container …), full height. The root scope keeps at most a slim top bar, and only when it carries something
other than the account menu (search / brand; decided and recorded by the frontend team in frontend/TEAM.md).

**Mobile (≤ 390 px):** the sidebar is a drawer; its toggle sits at the start of the breadcrumb row (root scope: the slim top
bar or the page header row). The drawer closes on navigation.

### 17.2 Routes (root scope)

**`/login`** — page. Fields: Email (required, email), Password (required). Buttons: **Log in** (primary) → Firebase
`signInWithEmailAndPassword` → `GET /v1/me` → redirect to `?next` or `/app/deployments`; **Continue with Google** (only when
`VITE_OPM_GOOGLE_DOMAIN` is set; `hd` restricted); **Forgot password** → modal (Email) → Firebase `sendPasswordResetEmail`,
always answers "If the account exists, an email was sent". Errors: wrong credentials → "Email or password is wrong"; API 403
"no account" → "There is no OPM account for this login. Ask an admin."; disabled → "This account is disabled". No signup link.

**`/app`** — redirect to `/app/deployments`.

**`/app/registries`** — table: Label · URL · Username (or "anonymous") · Password (RevealValue) · Last check (ok/failed badge,
time, message tooltip) · actions. `?registry={id}` highlights and scrolls to that row. Buttons:
- **+ New registry** → modal: Label (req, 1–80) · URL (optional, `https://host[:port]`, placeholder "Docker Hub") · Username +
  Password / token (both empty = anonymous, for public images; else both required, ≤ 255 / ≤ 8192). → POST /v1/registries.
- Row **Test login** → modal "Test login · <label>": Repository (optional; combobox from `listRegistryRepositories`, free text)
  → POST …/test with `{ repository }` (raw JSON body) → result in the modal + row update. A missing pull access shows the §6.1
  message ("repository X not found, or account Y has no pull access to it …").
- Row **Edit** → modal (same fields; Password empty = unchanged) → PATCH (only changed fields).
- Row Password **View** (RevealValue, registries.write) → POST …/reveal-password → shows the value with the 60 s countdown
  ring, then masks again; any number of times (audit `registry.password-revealed`). Changing it = **Edit**.
- Row **Delete** → confirm modal; 409 → "Used by images: <serviceSlug>:<version> …".

**`/app/services`** — table: icon · Label · Name (id) · Description · Versions · Used by. Row → service page.
- **+ New service** → side drawer: Label (req, 1–80) · **Name (id)** (req; `SlugInput` derived from the label while untouched,
  slug pattern, "fixed after create"; 409 → "name in use") · Icon (**IconPicker**, default None) · Description (≤ 2000).
  There is no repository field: each image has its own full reference.

**`/app/services/{serviceSlug}`** — header (icon, label, serviceSlug, description). Buttons:
- **Edit service** → modal: Label · Icon (IconPicker) · Description (Name (id) is read-only).
- **Used by** → `/app/services/{serviceSlug}/used`.
- **Delete service** → confirm modal (disabled with tooltip while images exist).
- **+ Register image** → full page `/app/services/{serviceSlug}/register-image`.
Images table: Version · Reference (mono, ellipsis + tooltip) · Registry (label or "public") · Kind · Platform · Digest (short,
copy) · Size · Import (pending/copying/ready/failed badge; failed → error tooltip) · Mode · Created. Row → image page. Polls
every 3 s while any import is pending/copying.

**`/app/services/{serviceSlug}/register-image`** — full page (nested lists). `?cloneFrom=<version>` preselects Clone. Sections:
0. **Clone from…** (when the service has versions): select a version → **Clone** copies everything (runtime, ports, env,
   volumes, health check, description) and suggests the next version; the user changes the tag of the reference. Nothing is
   applied until Clone is clicked.
1. Source: **Version** (req, `RE.version`, prefilled with `nextVersion(latest, taken)`: v3 → v4, 3.1.0 → 3.1.1, none → v1;
   hint "sites tag the image <serviceSlug>:<version>"; 409 `version-exists` → field error) · **Image reference** (req;
   combobox: paste a full reference, or pick host → repository (`listRegistryRepositories`) → tag (`listRegistryTags`, with
   pushed dates); the parsed parts are shown as chips host · repository · tag · digest; V_REFERENCE errors inline) ·
   **Registry** (auto: `matchRegistries(host)`; one match → that registry, several → select, none → "public · no
   credentials"; the user may override) · Platform (select, default linux/amd64) · Mode (radio copy / gateway) · **Read from
   registry** → POST …/images/inspect → a diff of the form values vs the image config; the user applies all or some (nothing
   changes without a click).
2. Runtime: Kind (radio service / job) · Entrypoint (string list editor, empty = image default) · Command (list editor) ·
   Workdir (absolute path or empty) · User (`name` or `uid:gid` or empty) · Stop timeout s (1–600, default 30).
3. Ports (inline rows + Add port, remove icon): Name (slug, unique) · Port (1–65535 or one `{{ }}` block) · Protocol
   (tcp/udp) · Description.
4. Env (inline rows + Add env): Name (`^[A-Z_][A-Z0-9_]*$`, unique, upper-cased while typing) · Default kind (segmented): **none**
   · **value** (plain text or expression, `ExprInput` field imageEnv) · **choice** (options as chips 1–50 + the default picked
   from them → `options` + `value`) · **list** (a real list value stored as ONE comma-separated string, options null) ·
   Required · Secret (disables Default) · Description. A legacy `value: string[]` opens as choice (options = the list,
   default = the first item).
5. Volumes (rows + Add volume): Name (slug) · Mount path (`ExprInput` field mountPath; help: "/app/helper/config/env/{{
   container.env.NODE_ENV }}.ts") · **Kind** (segmented control, §21 #83: a new row starts on **Auto · Folder|File** — follows
   `inferImageVolumeKind(mount path)` live — plus **Folder** / **File**; once Folder or File is picked Auto disappears; rows
   read from the registry / stored rows show Folder / File only; the body always carries the explicit kind,
   `resolveVolumeRows`) · Description. A path that does not start with "/" (e.g. `helper/config/{{ … }}.ts`) shows
   V_PATH next to the field; a File at a path ending with "/" shows V_VOLUME_KIND.
6. Health check: Type (none / http / tcp / exec / log) → http: Port (select of port names), Path (starts with /), Expect
   status (100–599, 200); tcp: Port; exec: Command (list), Expect exit code (0–255, 0); log: Pattern (RE2). Common: Interval
   (1–3600, 10) · Timeout (1–600, 5) · Retries (1–100, 3) · Start period (0–3600, 30).
7. Description (≤ 2000).
Buttons: **Register and import** (primary; `validateRegisterImage` must pass) → POST → navigate to the image page ·
**Cancel** (unsaved-changes modal). Sections 2–7 are the shared `ImageSpecEditor` (also the image page, §21 #82). Note shown: "Everything but the fetched image (reference, digest, platform, layers) can be edited later on the image page."

**`/app/services/{serviceSlug}/image/{version}`** — page (old `?tag=` links resolve to it). Locked panel: version, site image
name `<serviceSlug>:<version>`, reference (copy), parsed source (host, repository, tag, digest), registry (label or public),
digest (copy), platform, kind, import (status, mode, size, finishedAt, error, layers list collapsible), entrypoint, command,
workdir, user, volumes table (mount paths with expressions shown as written). **Superseded by §21 #82:** the page is ONE
"Image" form (> 12 fields → page): a locked **Image identity** card (version, reference + copy, registry, digest, platform,
import status / mode / size, created, layers collapsible; "locked — use Clone as new version to fetch another one") and the
SAME editor as register-image sections 2–7 (`ImageSpecEditor`: Runtime kind / entrypoint / command / workdir / user / stop
timeout · Ports add / remove / edit name, number, protocol, description · Env add / remove / edit name, default
none/value/choice/list, options, required, secret, description · Volumes add / remove / edit name, mount path (ExprInput),
kind Folder / File (§21 #83), description · Health check · Description), numbered 1–6. `?volume=<name>` (the container page fix
link) scrolls to and outlines that volume row and shows a banner "Fixing image volume X at P: set Kind … then Save" with
"Back to the container" (`&from=<container page path>`, same-app `/app/…` paths only). A kind change is always allowed (no
impact check, like a port number change); containers re-check their rows at once. A changed row name shows "renamed from <old>" and is sent as a rename;
renaming a port also moves the image's own health check port. A "Name changes" strip lists renamed / removed / added names.
Note: "Existing deployments keep their published revisions … until published again; drafts and templates pick up new image
defaults at the next validate / publish." A port whose number / protocol changed while the image is used shows a warning
under the row (mappings follow the name). Sticky bar: **Save changes** (dirty, no errors) → PATCH · **Reset**; leave guard.
409 `in-use` → **impact dialog** (each item: change badge, kind, old → new, references with template/draft badge, link to the
container page or edge route page, field): removals → "blocked" banner + **Cancel** only; renames → **Cancel** · **Rename
in templates** (PATCH again with renameEverywhere); the dialog splits the references into "Templates — will be rewritten"
and "Deployments — not changed (detached): they need a Template sync or a manual edit" (§21 #94). Read-only accounts see the form disabled. Buttons: **Clone as new version** →
`register-image?cloneFrom=<version>` · **Edit source** (only when the import failed) → modal: Image reference + Registry →
PATCH { reference, registryId } → **Save and retry import** (POST …/retry-import) · **Retry import** (only when failed) ·
**Used by (n)** → …/used · **Delete image** → confirm modal (409 → usage list).

**`/app/services/{serviceSlug}/image/{version}/used`** — table: Kind · Name (link) · Site · Container (containerSlug, link to the container page) · Version.
Empty: "Not used. You can delete this image." Back link.

**`/app/services/{serviceSlug}/used`** — same table for every version of the service.

**`/app/not-found-pages`** — table: Label · Size · Created by · Created · Used by. Row → preview.
- **+ New page** → modal (large): Label (req) · HTML code (code editor, req, ≤ 256 KB with live byte counter; "one
  self-contained document, inline CSS/JS/images"). Note: "Pages cannot be edited later."

**`/app/not-found-pages/{notFoundPageId}`** — page: label + "read-only"; preview in `<iframe sandbox="allow-scripts"
srcdoc=…>` (no same-origin); read-only code viewer; Used by list. Buttons: **Create a copy** → the New page modal prefilled
("Copy of <label>") · **Delete** → confirm modal (disabled with tooltip while used).

**`/app/templates`** — table: icon · Label · Version · Sites · Variables · Secrets · Files · Used by · Updated.
- **+ New template** → modal: Label (req) · Icon (IconPicker, default None) · Description. → POST → navigate to the overview.

**Template scope sidebar** (replaces the root sidebar on every `/app/templates/{templateId}/…` page; no tab bars):
"← All templates" · template icon + label + `v<version>` · **Overview** · **Variables** (count) · **Secrets** (count) ·
**Files** (count) · **Sites** (→ the sites table) with a **Sites tree**: one node per site (slug, label) that expands to
**Containers** (count; children = the containerSlugs, each linking to its container page) · **Edge** · **Volumes** (count) ·
**Settings** (host / agent ports / depends / 404 page), and **+ Add site** (the Add site modal). Every tree node shows
validation badges (error / warning counts of `validateConfig(mode template)` for its paths). The current item is highlighted;
the expanded state of the tree is remembered per template in localStorage (key per templateId; every access in try/catch, the
page works without it). Pages start with the breadcrumb (Templates › <label> › <site> › <section>) + page header.
**Unsaved changes**: the existing leave guard ("Leave without saving?") runs on sidebar navigation away from a dirty working
copy; the working copy of a SITE is shared by all pages of that site (Containers, container pages, Edge, Volumes, Settings),
so moving between them keeps the edits without a prompt; leaving the site (another site, Variables, …) prompts.

**`/app/templates/{templateId}`** — Overview: inline form Label · Icon · Description + **Save** (PATCH ifVersion); site
dependency graph (SVG, arrow from a site to what it depends on, cycles in red); validation panel (issues of
`validateConfig(mode template)`); JSON preview (collapsible, Copy, Download `.json`). **Delete template** → confirm modal with
typed label.

**`/app/templates/{templateId}/variables`** — table: Slug · Label · Type · Default value · Required · Options / rules ·
Description · actions. Buttons:
- **+ Add variable** → side drawer: Slug (req, slug, unique, fixed after create) · Label (req) · Description · Type (select) ·
  Default value (typed: text / number / toggle / select of options / chip list / JSON code editor) · Required · Options (enum
  only, chips 1–100) · Min / Max (number only) · Pattern (string only, RE2) → save = PATCH { ifVersion, variables }.
- Row **Edit** → the same drawer (slug read-only). Row **Remove** → confirm modal (lists the expressions that use
  `var.<slug>`, found with `exprRefs`).
- Round E: a **Required** variable without a default is normal in a template (its value is set at deployment): no issue, and
  every preview that uses it shows the "set at deployment" token. An optional variable without a value gives one info note
  (V_VAR_OPTIONAL_NULL). In a deployment draft a required variable / secret without value is one warning on the variable and
  blocks publish (V_VAR_REQUIRED / V_SECRET_REQUIRED).

**`/app/templates/{templateId}/secrets`** — table: Slug · Label · Required · Value source ("generate alnum 32" / "a person
enters it") · Description. Value column: "set per deployment" (templates hold no values, so there is nothing to View or
Regenerate here). Text: "Templates never hold secret values." Buttons: **+ Add secret** → side drawer: Slug · Label ·
Description · Required · Generate (toggle) · Charset (alnum / hex / base64url) · Length (8–128). Row **Edit** (drawer) ·
**Remove** (confirm).

**`/app/templates/{templateId}/files`** — table: Slug · Label · Mime · Editor · Max size · Default content (sha short + size or
"—") · **On the server** ("auto · <agentHome>/files/<slug>" via `locationLabel`, or the custom path). Text above the table:
"Each site keeps ONE copy of every file; every container that mounts it shares that copy (read-only)." Buttons: **+ Add file**
→ side drawer: Slug · Label · Description · Mime (select of common types + custom) · Editor (text / binary) · Max bytes
(1 B – 50 MiB, default 1 MiB) · **Resolve {{ }} expressions** toggle (`render`; text files only, default on; binary → always
off) · **Location on the server**: Auto (recommended; shows `<agentHome>/files/<slug>`) / Custom path (input; help "./ = agent
home (e.g. ./config/app.json), or an absolute path"; `checkLocation` + V_LOCATION inline). Table column "Render" (expressions
/ none). Row actions: **Edit definition** (drawer) · **Edit
content** (text files) → wide side drawer with a code editor + **Save content** (POST /v1/blobs (≤ 24 MiB) else uploads API,
then PATCH files[].content). With render expressions the editor highlights `{{ }}` blocks, autocompletes paths (var / secret /
site / self / deployment roots, `site.<s>.port.<containerSlug>.<portName>`; no `container.*` / `file.*`: E_NOT_ALLOWED with the
hint "files are shared by all containers on the site — use a variable instead"), validates live with `checkFileContent`
(errors at line:col, inline) and shows a hint "write a literal {{ as {{ "{{" }}"; **Preview for site…** → site picker only →
the rendered result for that site (`renderSiteFile` + `siteFileExprContext`, secrets masked `•••`, auto hosts as "auto ·
detected on install") with V_FILE_RENDER / V_FILE_JSON errors. · **Upload** (binary) → file picker → upload → PATCH · **Download** · **Clear content** (confirm) · **Remove**
(confirm). Content loads with `GET /v1/blobs/{sha256}` (200 ≤ 8 MiB, else 302 → signed URL); a failure shows the real text
("download failed: 403 …", "network error / CORS: …"), never a bare "Failed to fetch".

**`/app/templates/{templateId}/sites`** — table: Site · Label · Host · Containers · Depends on · Not-found page · Edge (ports ·
hosts · routes). Host column: the value, or "auto" (+ the detected address when an agent reported one). Buttons: **+ Add
site** → modal: Site slug (req, slug, not reserved, unique) · Label (req) · **Host: Auto-detect (recommended)** toggle (on by
default → host null; off → Host input: IP / DNS / `{{ var.… }}`) → new site with `defaultSiteConfig()` (volumes []); row →
the site's Containers page;
**Remove** → confirm modal.

**Site pages** (components shared with the deployment). Each site section is its own route under
`/app/templates/{templateId}/sites/{siteSlug}/`: **`containers`** · **`edge`** · **`volumes`** · **`settings`**.
`…/sites/{siteSlug}` (no section) redirects to `…/containers`; old links with `?tab=` redirect to the matching section
(`containers` → containers, `edge` → edge, `site` → settings, anything else → containers). Every site page has the breadcrumb,
a page header with the site slug, label (**Edit label** → modal, 1 field), dirty dot, **Save template** (PATCH sites, ifVersion;
disabled with errors) and the validation counter; the validation summary of the site stays on the page.
1. **`…/containers`** — table: Name (containerSlug) · Service (icon + label) · Version · Enabled · Published ports · Volumes ·
   Depends · Restart. **+ Add container** → full page `…/containers/new` (no modal; any service, also one already on the
   site). Row → container page `…/containers/{containerSlug}`. Row actions: **Edit** · **Duplicate** (→ `…/containers/new?
   duplicate=<containerSlug>`) · **Remove** → confirm modal (also lists edge routes and container depends that target it).
2. **`…/edge`** (routes · TLS) — **Ports & limits**: HTTP port, HTTPS port (empty = off), **Max request body**
   (`edge.maxBodyBytes`: Unlimited / size + unit KiB · MiB · GiB) (inline). **Hosts** table: Hostname · TLS mode · TLS details ·
   HTTP→HTTPS · Response headers (count) · Certificate (deployment: last agent report). **+ Add host** / row **Edit** → wide side
   drawer: Hostname (req; DNS / `*.x` / IP / `*` / expression) · TLS mode (none / internal / acme / files) · ACME email ·
   Challenge (http-01 / tls-alpn-01 / dns-01) · DNS provider · DNS token secret (select secrets) · Cert file (select files) ·
   Key secret (select secrets) · Redirect HTTP→HTTPS (only fields of the chosen mode are shown) · **Response headers →
   browser** (host list, editor below). Row **Remove** → confirm (also removes it from route host filters).
   **Routes** table in EFFECTIVE order (`effectiveRouteOrder`, §12.1 6): rank · group badge (exact / prefix ^~ / regex #n /
   prefix; tooltip = the `why` text) · kind + path · route id · hosts · methods · action summary · icons (websocket, streaming /
   buffering, own timeouts, own body limit, own response headers, W_RESPONSE_HEADER_AS_REQUEST marker) · **Edit** (link to the
   route page). ↑ ↓ only on regex rows (renumber the regex orders 1..n; the position of the others follows from the path).
   Buttons **+ Proxy route** / **+ Redirect route** / **+ Rewrite route** / **Import from nginx.conf…**.
   **Route page** (full page, > 12 fields + nested lists; static path + query `…/edge/route?id=<routeId>` (edit) /
   `?new=proxy|redirect|rewrite`; breadcrumb … › Edge › Route; it edits a COPY: **Apply** writes the site working copy and
   returns to Edge (disabled while the route has errors), **Cancel** drops it, leaving with un-applied edits asks first):
   1 **Match** — Match kind (Prefix / Exact / Regex) · Path or regex (regex: live `checkEdgeRegex` result "valid in RE2 and
   JavaScript" or the message; "case-insensitive (nginx ~*)" = leading `(?i)`; prefix hint "/api also matches /apix") · Stop
   regex (nginx ^~; prefix only) · Route id (slug from the path) · Hosts (multi-select; empty = all) · Methods (chips; empty =
   all). 2 **Action** — Proxy / Redirect / Rewrite (switching keeps a per-type stash).
   Proxy: Container (containerSlug picker) · Port (image ports; `edge: false` ports disabled) · **Path to the container**:
   Keep path / Strip prefix (not for regex) / Rewrite (regex + replacement, prefilled `^<path>(/.*)$` → `$1`) · Add prefix.
   3 **Connection** (proxy) — WebSocket (default on) · **Streaming (SSE / uploads)** (collapsible): presets *SSE* (request
   and response buffering off + read-timeout hint) and *Uploads* (request streamed + a body limit when inherited); Request
   buffering · Response buffering · Max body (Inherit edge (shows its value) / Unlimited / size) · **Timeouts** (collapsible):
   Connect (1–3600) · Send · Read (1–86400; help "idle time between two reads / writes, a stream that keeps sending is never
   cut"). 4 **Request headers → container** (proxy; nginx proxy_set_header) — the edge defaults as chips (Host kept,
   X-Real-IP, X-Forwarded-For / Proto / Host; struck through when overridden or removed) · rows Name (combobox of REQUEST
   header names only) + Value (edge expression input + variables strip) · Remove headers (chips). A hop-by-hop row shows
   V_EDGE_HOP; a response-only row shows the W_RESPONSE_HEADER_AS_REQUEST banner with **Move to response headers**
   (`moveToResponseHeaders`; opens the response group, toast). 5 **Response headers → browser** (proxy / redirect; nginx
   add_header / proxy_hide_header) — Inherit host (the host list read-only) / Own list (starts as a COPY of the host list;
   hint "replaces the host's list, like nginx") · rows Name (RESPONSE header names) + Value + Set / Add + Always · Remove
   (proxy_hide_header chips). Redirect: To (edge expression; variables strip `$uri $args $is_args $request_uri $host $scheme
   $1…$9 {path} {query} {host}`) · Status (301 / 302 / 303 / 307 / 308; new routes 301) · Keep path (prefix only). Rewrite:
   Regex · Replacement · banner "internal: the routes are matched again (nginx rewrite … last)". Side panel: **Try a path**
   (sample path → `resolveEdgeRequest` on the CANDIDATE edge: answer, route — "answered by route X instead" when another route
   wins —, rewrite chain + where it lands, container URL, Location), where the route sits in the effective order, its
   issues, a summary.
   **Import from nginx.conf…** (full page `…/edge/import`; §12.7): paste or upload (≤ 2 MiB) → Analyze → summary counts per
   status + filter chips · mapping table (line · context · directive · status · OPM target · note; error rows red,
   unsupported amber; sticky header, scroll box) · unresolved-upstream banner · certificate binding per `NginxCertRef`
   (file combobox = certificate, secret combobox = key; unbound allowed, then a validation error) · mode **Replace** (asks a
   confirm) / **Append** (explained) · result preview in effective order + the merged edge's issues → **Apply** writes the
   working copy and returns to Edge (toast).
   **Test a URL** box (inline): method · URL · body size (+ unit) · WebSocket upgrade · "not ready" checkbox per container →
   `resolveEdgeRequest(resolvedEdge, url, { method, ports, notReady, bodyBytes, upgrade })` (expressions resolved with
   `edgeExprContext` over the draft / template values, incl. rewrite replacements and response header values) → status +
   kind, route, effective order with the winner marked, rewrite chain, captures, auto-redirect, Location or upstream URL +
   upstream path (how: none / strip / rewrite), **request headers sent** (name · value · source default / route; removed),
   **response headers to the browser** (set / add / remove, from route / host / none; greyed "not on <status>" when the status
   gets no add_header and the name is not `always`), timeouts, buffering, body limit (+ from), websocket, steps (collapsed).
   Errors (block Save) and warnings listed below.
3. **`…/settings`** (host · ports · depends · 404) — inline form: Host: **Auto-detect (recommended)** toggle (on = null; shows "auto · detected
   on install" or the address the agent reported) / off → Host input (Value; IP, DNS or `{{ var.… }}`; preview of the resolved
   host; a null / empty result shows the V_HOST_AUTO warning, not an error — not in a template, and never for a REQUIRED
   variable, which previews as "set at deployment") · Site depends (multi-select of
   other sites; cycle error inline) · Not-found page (select: none = built-in, + pages; Preview link) · Agent port (1024–65535)
   · Agent GUI alias port (1024–65535 or empty = off). Hints: the three ways to the agent GUI.
4. **`…/volumes`** (round D) — the declared site volumes (`SiteConfig.volumes`). Info line: "Volumes are folders on the site
   server (auto: <agentHome>/volumes/<name>). Back up = included in snapshots. Host paths are never backed up." Table: Name
   (volumeSlug, monospace) · Label (empty → the slug) · Description · **Kind** (folder / single file) · **Starts with**
   (`seedLabel`: empty / n files / file X / archive name; "seed changed" when it differs from the head) · **On the server**
   (`locationLabel`: "auto · <agentHome>/volumes/<slug>" or the custom path) · **Back up** (toggle, inline) · **Used by**
   (container chips from `volumeUsers`, each linking to the container page; none → the V_VOLUME_UNUSED warning) · row actions
   **Edit** / **Delete**. **+ Add volume** → side drawer (round E; was a modal): Name (volumeSlug; req, `RE.volumeSlug`, unique
   on the site; read-only after create — a rename is a new volume) · Label (0–80) · Description (0–2000) · **Kind**: Folder /
   Single file (fixed after create; Single file shows `FILE_VOLUME_RENAME_WARNING`) · **Starter content**: folder → Empty /
   From files (rows: path inside the volume (`seedPathError` live) + file picker; + Add) / Archive (.tar.gz / .tgz ≤ 200 MiB,
   uploaded with purpose `seed-archive`, multi-part); single file → Empty / File (file picker); note "applies only when the
   volume is created or empty — never overwrites data" · Back up (toggle, default on) · **Location on the server**: Auto
   (recommended) / Custom path (help "./ = agent home, or an absolute path"; V_LOCATION inline; changing it later does not move
   the data) → `newSiteVolume`. Row **Edit** → the same drawer (Name and Kind read-only). Row **Delete**: a volume used by any container is BLOCKED → dialog "Volume X is
   used by" + the list of containers (links) and only a Close button; an unused volume → confirm modal ("the folder stays on the
   server"). Changes go into the site working copy (saved with Save template / Save draft).

5. **`…/devtools`** (§21 #97, template + deployment scope, after Volumes) — the site's dev-tool presets
   (`SiteConfig.devTools`): info banner (the agent GUI lists them ready-to-use under Dev tools; credentials as a secret
   (recommended), a container env, an expression, or a typed Value stored in plain text; operators can always add manual
   connections on the agent), table Preset (kind icon, name, `p:<slug>`, description, read-only
   badge) · Kind · Target (`devToolTargetText`) · Auth summary ("user postgres · password: secret pg-password · db acme") ·
   Edit / Duplicate / Delete (confirm); issues from `validateSiteDevTools` inline + in the site validation card; sidebar node
   "Dev tools" with count + badge. **+ Add preset** → kind picker (6 kinds) → drawer: Preset (slug — fixed after create, name,
   description, read-only) · Connect to (container + port picker from the image ports, default port per kind | host : port |
   http base URL | SQLite volume + relative path | container + path) · Authentication (kind-specific; password / token =
   None | Value (§21 #103: typed, masked input + show / hide toggle, amber note "Stored in plain text in the config — anyone
   who can read the config sees it; use a secret for production."; saved as `{ kind: 'literal', value }`; the Diff /
   Template sync show it as `••••`) | Secret (picker + "+ New secret…") | Container env (§21 #99, only when the target is a
   container: picker of that container's env, `{{ containers.<c>.env.<NAME> }}`) | Expression) · Options (kind-specific). Every
   expression is field `devtool` (completion one level at a time from `{{`: roots → `containers.` → `containers.<c>.` →
   `env.` → names, with resolved values; secrets masked). With a container target, chips "Use env POSTGRES_USER of
   postgress" (`suggestDevToolEnv`: per-kind well-known names, then name patterns) fill username / password / database /
   client id / token in one click. An empty list deletes
   the key. Diff tab "Dev tools", Template sync group "Site X · Dev tools". Publish / Diff / site page warn per site whose last
   reported agent version < 0.11.0 (§21 #97 a; does not block), < 0.11.1 when a preset of the site uses
   `{{ containers.<c>.env.<NAME> }}` (§21 #99 e), and < 0.13.1 when a preset holds a static Value or the site uses a
   §21 #103 filter (`sha256` / `sha512` / `mosquitto_passwd`) in its config or in a rendered File one of its containers
   mounts (`siteFilterAgentNeed`; the Diff page loads the File sources).

**`/app/templates/{templateId}/sites/{siteSlug}/containers/new`** and **`…/containers/{containerSlug}`** — full page
**container editor** (shared with the deployment draft). `new` (`?duplicate=<containerSlug>` prefills `duplicateContainer`:
new suggested name, host ports cleared, a `<old>-<imageVolume>` volume renamed to `<new>-<imageVolume>` and DECLARED on the
site with the original's label + backup). Breadcrumb (Templates › <label> › <site> › Containers › <containerSlug>). Header: service icon + Name · image
`<serviceSlug>:<version>` · Enabled toggle · sticky **Save template** (saves the whole site; disabled with errors). Sections:
1. **Service + name**: Service (picker, any service) · Version (picker, ready first, import status shown; a change re-checks
   env / ports / volumes) · **Name** (containerSlug; `suggestContainerSlug`: the service slug, then `-2`, `-3` …; editable only
   while new; slug rules, unique on the site, not `new`; hint "DNS name on the site network, used in expressions and routes").
2. **Ports** — one row per image port: Port name + container port + protocol (locked, from the image) · **Published** toggle
   · Host port (number stepper or `{{ }}` expression; empty = not published) · Bind address (0.0.0.0 all / 127.0.0.1 this
   machine only) · **Edge may proxy** toggle (`edge`). Hint: "HTTP goes through the edge; host ports 80/443 belong to the edge."
3. **Volumes** — one row per image volume, then the extra mounts, then **+ Extra mount**. Row: "image volume `<name>` ·
   <image mountPath>", then ONE grouped dropdown (a listbox, not a segmented control): group **Data volumes** (the declared
   volumes of the site, each with "shared with X, Y" when other containers use it (`volumeUsers`), then **+ New data
   volume…** → the SAME Add volume drawer as the Volumes page (single-file volumes are listed with "single file"; one on an
    image volume row or at a path ending with "/" is flagged inline: V_VOLUME_KIND_IMAGE / V_VOLUME_KIND_MISMATCH /
    V_VOLUME_KIND with the fix links of §11; the image volume cell shows a **Folder** / **File** badge = `imageVolumeKind`;
    "+ New data volume…" on an image volume row starts with that image volume's kind), Name prefilled `suggestVolumeSlug(defaultVolumeSlug(container,
   imageVolume), declared)`; it declares the volume on the site and selects it) · group **Files** · group **Secrets** ·
   **Advanced: host path…** (reveals an absolute literal path input) · **Not mounted** (image volume rows only). Then the
   **Read-only** toggle ONLY for volume / host sources (default OFF = read-write). Column **Inside the container** (the mount
   path): volume / host rows show the effective mount path (the image path by default); file / secret rows show TEXT
   "mounted read-only at <path>" (no required-looking field) — the file's place on the server is set on the Files page, not
   here. Both have a small **Edit path** link that reveals the expression-aware input (field mountPath, resolved preview,
   V_PATH inline; **Reset** = back to the image path). A file / secret / volume that other containers of the site also mount
   shows "shared copy · also used by X, Y". An image
   volume without a row shows "Not mounted — pick a data volume" with the V_VOLUME_NOT_MOUNTED warning. Errors
   V_VOLUME_UNDECLARED / V_READONLY and the warnings V_VOLUME_UNUSED / V_VOLUME_NOT_MOUNTED show on the row and in the live
   issues of the summary sidebar. Extra mount row: Mount path (req) + the same dropdown without "Not mounted".
4. **Env** — the image env list with defaults / options / required / secret shown; per row an override input
   (expression-aware with autocomplete; live resolved preview with secrets masked; error underline at the position); a choice
   env → dropdown of its options; a secret env → secret picker (secretRef); **+ Extra env** (Name). Missing required env →
   warning.
5. **Runtime** — Command / Entrypoint (list editors; empty = image) · Restart picker (always / on-failure / never; job: never
   default) · Depends on (multi-select of the OTHER containers of this site; the site depends are shown read-only with a link
   to the site's Settings page) · CPUs (0.1–64 or empty) · Memory MB (64–1048576 or empty) · Health check override (empty = the image's).
6. **Summary sidebar** (sticky): name, image, published ports (`hostIp:hostPort → port`), **Mounts** lines "<container path>
   ← file <slug> (server: <location>)" (likewise "← volume <slug> (server: <location>)", "← secret <slug> (server:
   <agentHome>/secrets/<slug>)", "← host <path>"), and the
   live validation issues of this container (validateConfig paths `…containers[<containerSlug>]…`).
Expression preview: every env value and mount path resolved (`resolveContainerConfig`: env first, then mount paths; E_CYCLE
shows the loop). Buttons: **Back to containers** · **Save template** · **Duplicate** · **Remove container** (confirm).

**`/app/tags`** — table: Tag · Label · Icon · Color · Deployments. **+ New tag** → modal: Tag slug (req) · Label · Icon (IconPicker) · Color
(picker + hex). Row **Edit** → modal (Label · Icon · Color). Row **Delete** → confirm modal ("removed from N deployments and M
access rules").

**`/app/accounts`** — table: avatar · Name · Email · Phone · Access (n rules / "admin") · State · Last login. **+ New account**
→ modal: Name (req) · Email (req) · Phone · Avatar (IconPicker) → POST → navigate to the account page, where the generated
password shows in the Password row's RevealValue with its 60 s timer (no result modal, no set-password link; §21 #84). Row → account page. Row **Disable / Enable** → confirm modal.

**`/app/accounts/{uid}`** — full page (nested list): profile form (Name · Phone · Avatar (IconPicker); Email read-only) + **Save
profile**; Access rules editor (rows + **Add rule**: Resource (select) · Actions (checkboxes of the valid actions) · Scope (all /
tags / deployments; developer resources fixed to all) · Scope values (multi-select of tags / deployments)); **Make full admin**
(confirm modal, fills `fullAdminRules()`); **Save access** (accounts.admin; `validateAccessRules`); **Password** row
(accounts.admin): RevealValue of the last password an admin set ("set <time> by <name>"; **View** → POST …/password/reveal,
audit `account.password-revealed`; "No stored password — the person has changed it, or none was set" when absent / 410) +
**Set new password…** (typed ACCEPT; the person's sessions end) → POST …/password → the new
password shows for 60 s (audit `account.password-set`); **Reset link…** → POST …/password-reset → a new 1 h link in a
RevealValue (audit `account.reset-link-created`); while a stored link is valid the row shows it masked with **View**
(POST …/password-reset/reveal, `account.reset-link-revealed`); **Disable / Enable** (confirm).

**`/app/github`** (§21 #106) — admins: Admin → GitHub: repository owner / name + Mirroring toggle, token (masked, View 60 s, Replace…, Remove), Test connection (checks list), help panel (branch layout, how to run compose), branch table (git branch, kind, label, last pushed OPM commit, git sha link, state + pending, last error, notes) with Sync / Sync now / Resync everything…, paused banner. Template / deployment create forms have the slug field (from the label, immutable afterwards). Details: docs-v2/github-mirror.md.

**`/app/audit-log`** — admins: table Time · Actor (e-mail) · Action · Target (linked) · Outcome badge (ok / `<status> <errorCode>`);
filters (mirrored in the URL, sent to the API): action (all / prefix / exact), actor (uid, e-mail, system), target type, outcome,
date range; **Load more** (cursor). Row → side drawer: full entry (op, status, error, request id, ip, user agent, data) + a
readable before → after diff of `changes` (secret changes as a lock badge with ***; truncated values as preview + size).
**History** (admins only): a button on the detail pages of registry (row action), service, image, not-found page, template,
tag (row action), account and deployment opens a drawer with that target's audit entries (`?target=`) and their diffs.

**`/app/deployments`** — cards: logo (or initials) · Label · tag chips · sites online x / y (dots) · head `rev <seq>` · pending
proposal badge (status colour) · updated. Filter chips by tag (one or "all") + search box (label). Card → deployment home.
- **+ New deployment** → side drawer: Label (req) · Source (radio: from a template / empty) · Template (select, shows version;
  only for "template") · Tags (multi-select) · Managers (multi-select accounts) · Logo (IconPicker, optional) → POST →
  navigate to Setup, where the new access key shows in the Access key panel's RevealValue for 60 s (from the create response;
  no modal, no checkbox — §21 #84; it can be viewed again any time with **View**).

### 17.3 Deployment scope (`/app/deployments/{deploymentId}/…`)

Layout: deployment scope sidebar ("← All deployments" (= Exit deployment → `/app/deployments`) · logo + label · Overview ·
Setup · Status · Activities · Variables · Secrets · Files · **Sites** with the same **Sites tree** as the template scope
(draft sites → Containers (container names) · Edge · Volumes · Settings, + Add site; validation badges from
`validateConfig(mode draft)`; expanded state per deployment in localStorage) · Diff · **Template sync** (§21 #94; badge =
`newerVersions` when > 0); the sidebar footer of §17.1). No top
bar: pages start with the breadcrumb (Deployments › <label> › <site> › <section>) + page header; the deployment identity
(deploymentId copy chip, tag chips, head `rev <seq>`, pending badge) is shown in the Overview header. Leave guard as in the
template scope (working copy per site shared by its pages). Draft pages show the banner "Cloud draft. Edits change only the cloud draft. Publish on Diff; the sites change only after a person
approves in the agent GUI."

**`…/{deploymentId}`** (Overview) — cards: Sites online x/y · Head (seq, reported at) · Pending proposal (seq, status, summary
→ Diff) · Source template (label, cloned vX, synced vY, current vZ — or "empty"; §21 #94: a non-blocking banner
"Template has N newer versions — review in Template sync" when `newerVersions` > 0). Last 10 activities (→ Activities). Last 5 proposals (seq, status, origin, by,
time). Buttons: **Edit deployment** → modal: Label · Logo (IconPicker) · Tags · Managers → PATCH · **Delete deployment** → confirm modal
with typed label (danger).

> **SUPERSEDED by §21 #104 (agent ≥ 0.14, cloud with branches)** — kept for agents < 0.14 (compat proposals) and as history. Current model: docs-v2/branches-commits.md.

**`…/template-sync`** (§21 #94; needs deployment.config.read + templates.read to view the plan, config.write to apply) —
header card: source template (link), cloned at vX (templateVersion, copiedAt), last synced vY (baseVersion, by / when or
"never"), current template vZ, "N changes in the template since your last sync"; actions **Refresh**, **Change template…**
(modal with a template Select → plan with `?templateId=`; banner "Applying links this deployment to <label>; the base stays
the old one"), **Unlink template** (confirm: "the deployment stays exactly as it is"). Unlinked → **Link a template…** (base
empty: equal items are no-ops, differences conflicts). Template deleted → card with Change template… / Unlink. Body: the 3-way
review grouped Variables · Secrets · Files · per site Settings / Edge / Volumes / Containers, filter chips (All · Template
changes · Conflicts · Your changes · Skipped · No-op); each row: label, kind badge (Template · Conflict · Your change (greyed) ·
Same change · Skipped · Template default), what the template / you did; expand = base | template | yours side by side in the
diff colors; files: metadata + text content diff of the three blobs; secrets: definitions only. Checkbox per takeable item;
conflicts need Take template / Keep mine / (scalars) Merged value inline; **Select all clean template changes** · Clear.
Preview panel (bottom bar + drawer on phones): `previewTemplateSync` on every choice change (debounced) → validation errors /
warnings, unresolved conflicts, missing required variables (inline value inputs → `values`), required secrets to set later;
**Apply to draft** (disabled while conflicts are unresolved or errors exist) → success card "Review the diff → then publish a
proposal" (link to Diff). 409 → "the draft or the template changed — Refresh". Never publishes.

> **§21 #105 (agent 0.14.1)**: the working-copy bar below is replaced by the ChangeBar of the BROWSER change set ("N changes
> (not committed)" · Discard · Review · Commit…, on every config page; restore / stale banner; rebase on 409). No Save buttons;
> drawers / modals Apply into the change set; Commit writes `{ message, baseCommitId, config }`. See §21 #105.

**`…/history`, `…/compare`, `…/branches`** (§21 #104, as built) — sidebar groups **Working copy** (Variables · Secrets ·
Files · Sites) and **Branch** (Compare with the uncommitted count · History · Agent branches); the scope card shows "commit n ·
N uncommitted". Every scope page shows the working-copy bar while something is uncommitted: "N uncommitted" · Review (→
`…/compare?review=working`, read-only head ↔ working copy with blockers / warnings / suggested depends) · Discard… (confirm) ·
**Commit…** (shared CommitDialog: message required, Suggest, changes, publish blockers, per-site agent-version warnings).
`…/compare?source=<branchKey>[@commitId]` = the shared MergeView with a source picker (templates, deployments, this deployment's
agent branches + commit picker; default = the origin) → Merge… (message). `…/history` = the shared HistoryView (open, compare two,
Revert…, Reset to this commit…; deployments add read-only "Legacy proposals"). `…/branches` = one row per site: head commit,
behind / ahead / differs, "N changes made on the server", agent version + "needs agent ≥ 0.14", Compare, History (drawer).
Redirects: `…/diff` → `…/compare?review=working`, `…/template-sync` → `…/compare?source=template:<origin>`. Overview cards: Sites,
Head commit, Uncommitted (+ origin), Agent branches; "Last commits". New template / deployment: Source Empty · Template ·
Deployment · Agent branch + commit picker (+ "Copy secret values without a generate rule").

**`…/setup`** — Access key panel: the key in a RevealValue (masked, "24 chars"; **View** → POST …/access-key/reveal, needs
deployment.setup.write, activity `access-key.revealed` + audit; 60 s countdown ring; **Copy**), version, rotated at / by,
per-site key version (from heartbeats). **Regenerate…** (= rotate) → typed ACCEPT modal ("The old key stops working at each
site as soon as it receives the new hash. Open sessions stay valid until their 6 h end.") → the new key shows in the same
RevealValue for 60 s. Pre-#84 deployment (`accessKey.stored` false) → "not stored — regenerate to view" + Regenerate. Sites
table: Site · In draft · Agent (enrolled:
version, hostname, ip, os/arch, last seen) · Key version · Install. Not enrolled → **Create install command** (direct action) →
the command in a RevealValue (shown 60 s, **Copy**) + token expiry countdown; while the token is active the command stays
viewable (**View** → POST …/install-tokens/{tokenHash}/reveal, `install-token.revealed`) and **Regenerate…** (plain
confirm) makes a new one and revokes the old (`createInstallToken` with `replaceActive: true`); tokens list (state active /
used / expired / revoked — only active stored tokens have View; an active pre-#84 token shows "not stored — regenerate to
view"; **Revoke** → confirm modal). §21 #92: every ENROLLED site card has the **Upgrade / reinstall command**
(`upgrade-command`: RevealValue View 60 s → `revealUpgradeCommand`, Copy, Regenerate… → `regenerateUpgradeCommand`; not
stored → "not stored — Regenerate to view a new one" + the shell-history hint; Options: Unattended `OPM_YES=1`, Agent
version `OPM_AGENT_VERSION=x` (+ `OPM_ALLOW_DOWNGRADE=1`), put after `sudo` in the shown + copied command); for agents
< 0.10.0 it sits inline in the rerun-install upgrade path. Token rows: badges "upgrade only" (maintenance, "no expiry")
and "upgrade command". Agent rows: Clock = `agent.clockSkew` (> 2 min → amber "agent clock is 5 h ahead — fix NTP on
the server"; else "in sync"; null → "not reported (agent < 0.11)"); the Status page shows the same warning. Relative
times use the cloud's clock (X-OPM-Server-Time offset); a browser clock > 2 min off shows a note. Enrolled → **Revoke enrollment** → confirm modal with typed site slug. Hint per site: agent GUI at
`http://<host>:<agentGuiAliasPort>/_opm/` (or `:<agentPort>` before the first setup). "Offline install bundle" button shown
disabled ("later").
§21 #90 **Agent upgrades** (enrolled sites; `SetupResponse.agentRelease`, `agentUpgradeRun`, `sites[].upgrade`): the Agent
column shows `installed / latest` (latest = the pin; "—" when none is pinned) and a badge: **upgrade available** (amber;
`upgrade.available`), **up to date**, **upgrading: <phase>** (+ download %; from `upgrade.progress`), **queued** (a request
the agent has not started; "requested by <email>, <time>"), **failed** / **rolled back** (red; the error in a tooltip).
Release notes of the pin open in a popover from the latest version. Row action **Upgrade agent** (deployment.setup.write;
shown when available) → confirm modal with the from → to versions, the notes, "the agent restarts (containers keep
running); it rolls back by itself when the new version is not healthy within 60 s" and a typed site slug → POST
…/agents/{slug}/upgrade; a queued request shows **Cancel request** (DELETE; conflict once started → toast). Old agents
(`supported` false, e.g. 0.7.2): the button is disabled and the hint reads "Rerun the install command of this site once to
reach agent ≥ 0.10.0 (the command stays valid for upgrades); later upgrades are one click here." with a **Show install
command** link to the site's command. Above the table, when ≥ 1 site is upgradeable: **Upgrade all (N)** → modal listing
each site (from → to, or "skipped: rerun the install command") + typed UPGRADE → POST …/agents/upgrade-all; while a run is
going a run panel shows each site's status (pending / requested / running / done / failed / skipped / cancelled) +
**Cancel run** (confirm). Never automatic. The page refreshes every 5 s while a request or run is active.

**`…/status`** — table: Site · Online (dot) · Last seen · Via (direct / relay via X) · Agent · Head seq · Staging (state +
progress bar) · Key version · CPU / Mem / Disk · Edge (bound ports, bind errors, cert errors). 30-day heatmap per site (30
cells; tooltip date + %; green ≥ 99 %, amber < 99 %, red < 95 %, grey no data). **Refresh**; auto-refresh 15 s.
§21 #90 the Agent column shows `installed / latest` + the same upgrade badge as Setup (`SiteCloudStatus.upgrade`); the
badge links to Setup (the Upgrade buttons live there).

**`…/activities`** — filter bar: Site (sites + "cloud") · Origin · Type (prefix) · Actor · From · To · Via. Table: Time · Site · Seq
· Origin · Actor · Type · Summary · Via. **Load more**. Row → side drawer (read-only record incl. data JSON, prevHash, hash).
**Upload a bundle** → `…/activities/upload`.

**`…/activities/upload`** — page: drop zone / **Choose file** (`.opmlog`, ≤ 24 MiB) → **Upload** → result: signature ok, per-site
ranges, inserted / skipped / rejected, errors table; **Upload another**.

**`…/variables`** — table with inline typed value editors · Required · "changed since head" marker (head value in a tooltip) ·
inline validation. Buttons: **Save draft** (PUT variables, ifRevision; enabled when dirty) · **+ Add variable** (side drawer as
in templates) · row **Edit definition** (drawer) · row **Remove** (confirm). Add/edit/remove save at once.

**`…/secrets`** — table: Slug · Label · Required · Source · State (set / not set — red "blocks publish") · Rotated · Value
(RevealValue: **View** needs deployment.secrets.read, 60 s, `secret.revealed` + audit; **Regenerate…** only with a generate
rule and deployment.secrets.write → plain confirm → POST …/secrets/{slug}/regenerate (ifRevision) → the new value shows for
60 s, `secret.regenerated` + audit) · actions. Row **Set value** → modal: Value (password input with show toggle, ≤ 8 KiB)
→ PUT secrets (plainValue) · **Clear value** → confirm · **Edit definition** → drawer (Label · Description · Required · Generate · Charset · Length) ·
**Remove** → confirm. **+ Add secret** → drawer (with Slug). Every action saves at once (PUT whole section, ifRevision).

**`…/files`** — as template files, saving to the draft (PUT files).

**`…/sites`** — table: Site · Label · Host (+ resolved) · Containers · Depends · Agent (enrolled / online) · **Send** · **Receive**.
**+ Add site** → modal (Site slug · Label · Host auto-detect toggle / Host). Row → the site's Containers page. **Remove** → confirm modal (warns when an agent is enrolled).

**`…/sites/{siteSlug}/containers | edge | volumes | settings`** — the site pages on the draft (Save draft = PUT
draft/sites/{slug}, ifRevision); `…/sites/{siteSlug}` and `?tab=` links redirect as in the template scope; Edge hosts show
certificate status from the last report; Test a URL uses draft values.

**`…/sites/{siteSlug}/containers/new`**, **`…/sites/{siteSlug}/containers/{containerSlug}`** (`?duplicate=`) — the container
editor on the draft (Save draft).

**`…/sites/{siteSlug}/transfers`** (§21 #100; the site's sidebar item "Transfers", one link in the Sites table; old `…/send`
and `…/receive` redirect keeping the query) — header "Transfers · <site>" + text: store-and-forward in both directions, the cloud
keeps every file until someone deletes it, the site does not need to be online; files appear in the agent GUI at /_opm/app/<site>/transfers (Agent ›
Transfers) once the agent has fetched them · **Refresh** (auto-refresh 15 s while visible). Two columns (stacked ≤ 1000 px, Send
first). **Send to <site>** (deployment.transfers.write; read-only users do not see the button) — **Send a file** → modal: File (req,
≤ 2 GiB) · Note (≤ 500) → POST transfers + multi-part upload with a progress bar in the list (resumable after reload via GET
/v1/uploads/{id}); rows: file · note · size · sent by · when · expires · state badge (`transferState(t,'cloud')`: uploading from the
browser / stored · waiting for the site / on the site (fetched hh:mm) / saved on server + path / downloaded / expired) · **Download** ·
**Delete** (confirm). **Received from <site>** — rows: file · note · size · sent by · when · state (arriving from the site / stored /
stored · downloaded / expired) · **Download** (primary while stored) · **Delete** (confirm).

> **SUPERSEDED by §21 #104 (agent ≥ 0.14, cloud with branches)** — kept for agents < 0.14 (compat proposals) and as history. Current model: docs-v2/branches-commits.md.

**`…/diff`** — header: draft version, head (seq, revId, reported) or "No head yet (first setup)". Banners: stale proposals ("The
sites changed after proposal N was published. Copy head into draft, check, publish again."); open proposal (status → link).
Section tabs with counts: Variables · Secrets (changed / not changed only) · Files (hash) · Sites · Volumes (`<site>/volumes/<volumeSlug>`) · Containers · Edge — each a
table: Path · Head · Draft · kind badge · note (`DiffChange.note`, round E: e.g. "seed changed — applies only to new / empty
volumes"). Blockers (errors with links to the field) and warnings panels. Buttons: **Publish
proposal** (deployment.config.publish; disabled while blockers or an update runs) → modal: Summary (req, 1–200, prefilled from
the changes, e.g. "theme green → yellow") → POST publish · **Copy head into draft** → confirm modal → POST reset-to-head.
Proposals history table: Seq · Status · Origin · By · Created · Result.

---------------------------------------------------------------------------------------------------------------------

## 18. Agent GUI (SvelteKit SPA at `/_opm/`) — every route

Same UI rules as §17.0. Every page shows ALL sites (data of other sites over the mesh); a site that does not answer shows
"stale · last seen hh:mm". Writes to another site are forwarded by the agent you opened.

**Layout (nav2, §21 #98)** — ONE site selection for the whole GUI: every module lives at `/_opm/app/{site|*}/{module}/…`
(the selection is part of the URL; the tab remembers it in sessionStorage `opm.site`; `/_opm/app` and old URLs land on
it, else on this site). Sidebar header: deployment label + id · **Site selector** (custom Select: "All sites" `*` + every
site with a reachability dot, "this site" / "unreachable" badges) · this site + version · head `seq n` · reachability
· cloud route. Sidebar = four **accordion sections** (button headers with aria-expanded, Enter / Space; open / closed in
localStorage `opm.nav.open`; by default only the section of the current page is open and a page of a collapsed section
opens it; a collapsed header shows its badges): **Docker** — Containers (badge: containers not ready) · Volumes ·
Docker · Network; **Agent** — Agent (badge: sites with an upgrade) · Debug · Transfers (§21 #100; was Send + Receive) · Internet (badge "on"
while this tab tunnels); **Deployment** — Sites · Variables · Secrets · Files · Diff (badge: open proposals; dot:
unsaved local edits) · Backups; **Site** — Status · Activities · Config (badge: errors / warnings of the local edits).
Deployment-wide modules ignore the selection or use it as a filter (Sites / Diff staging highlight the selected site);
a module that needs ONE site shows under `*` "Choose a site to see X" with an inline site chooser + "This site" (Transfers,
Network, a container page, Agent › Host shell, Debug › Live agent log, Config sections). Changing the site keeps
the module (config section, activities/download; a container page → that site's Containers; deployment modules keep
their query). **Sidebar footer** (always): actor chip (email · "session ends in 4 h 12 m" · **Change person** → actor
popup · **Log out** → POST logout → /_opm/login) and the light / dark theme toggle. There is no top bar: every page starts
with a breadcrumb (site › module › sub-page; deployment modules: Deployment › module) + page header; the lock banner
while an update runs (→ Diff) is shown at the top of every page. At ≤ 900 px the sidebar is a drawer whose toggle sits
in the breadcrumb row; picking a module closes it; menus and popups portal to the body.

**Old URLs (307 redirects, nav2):** `/_opm/app` → `{site}/sites` · `/variables` `/secrets` `/files` `/backups`
`/internet` → `{site}/…` · `/updates[?rev=]` → `{site}/diff[?rev=]` · `/sites` → `*/config` · `/sites/{s}/send|receive` →
`{s}/transfers` (§21 #100) · `{s}/send` and `{s}/receive` → `{s}/transfers` (§21 #100) · `{s}/tools/logs?c=x` → `{s}/containers/x?tab=logs` (`?src=agent` → `{s}/debug?tab=agent`,
`?src=access` → `{s}/network?tab=access`, none → `{s}/containers`) · `tools/terminal?c=x` → `?tab=terminal` ·
`tools/monitoring` → `{s}/status` (`?c=x` → `containers/x?tab=monitoring`) · `tools/containers[?c=x]` →
`{s}/containers[/x]` · `tools/volumes|docker|network` → the module (query kept) · `{s}` → `{s}/status` ·
`{s}/config[?tab=]` → its section (as before).

**`/_opm/login`** — page: deployment id, site slug + label (`GET /_opm/api/info`); Access key (req; grouped input, paste
friendly; normalised client-side like the server) · **Log in**. Errors: invalid-key "Wrong access key (n of 5)";
rate-limited "Too many attempts. Try again in m:ss"; unavailable "This site has no access key yet. It gets it from the cloud
or from another site." Text: "After login the agent asks who is working right now." Help text under the field: "Don't
have the key? In the cloud, open this deployment's Setup page and click **View** next to the access key (or Regenerate it)."
(the agent never holds the plaintext key, only its hash).

**Actor popup** (blocking modal on every page load after login; not closable) — Email (req, email) · suggestions (localStorage
first, then GET actors/recent; click fills) · **Continue** → POST actor. Text: "This does not log you in. It names the person
who works now; the agent writes this email into the activity log."

**`/_opm/app/{site}/sites`** (Deployment › Sites; the selected site's tile is highlighted) — site tiles: slug + label · host · online / stale · head seq · staging (proposal seq + state) · cloud route ·
containers ready x/y · agent version · "this site" marker · lock. Mesh link matrix (sites + cloud; ✓ rtt / ✗). Auto-refresh 5 s.

**`/_opm/app/{site}/variables`** — table: Slug · Type · Value (typed inline editor; type, required read-only) · head value. Add /
remove are shown disabled with tooltip "cloud only". **Review changes** → diff panel + validation (`previewLocalProposal`);
**Create local proposal** → modal: Summary (req, 1–200) → POST proposals/local → navigate to `/_opm/app/{site}/diff?rev=<revId>`.

**`/_opm/app/{site}/secrets`** — table: Slug · Label · State · Value (RevealValue, same component as the cloud: **View** → POST
`/_opm/api/secrets/{slug}/reveal`, 60 s countdown ring, local `secret.revealed`; no Regenerate here — a new value goes
through a local proposal) · New value (password input per row; **Generate**
when a generate rule exists). **Review changes** · **Create local proposal** (modal Summary).

**`/_opm/app/{site}/files`** — table: Slug · Mime · Editor · Size · sha256 · **On the server** (the location resolved for this
site: "auto · <agentHome>/files/<slug>" or the custom path; read-only here — the location is cloud only, V_LOCAL_DEFINITION).
Row **Edit** (text) → wide side drawer with code editor
(**Use this content**; uploads to POST /_opm/api/blobs; with render expressions: `{{ }}` highlight + live `checkFileContent`
errors at line:col; the render mode itself is cloud only, V_LOCAL_DEFINITION) · **Upload** (binary) → file picker ·
**Download** · **Preview for site…** (render expressions: the file rendered once for a picked site, secrets masked).
**Review changes** · **Create local proposal** (modal Summary; staging renders the file once per site — V_FILE_RENDER /
V_FILE_JSON).

**`/_opm/app/*/config`** (nav2: the old "Sites config" module `/_opm/app/sites`, which redirects here) — one card per site: host ("auto · <advertiseHost>" when null) · depends · containers (name =
containerSlug, service, version "cloud only", enabled) · volumes (count, backup on / off) · edge summary
(ports, hosts, routes) · **Edit config** → `/_opm/app/<site>/config`. Add/remove site and container: disabled ("cloud only").

**`/_opm/app/{siteSlug}/config`** (`*` = the site cards above; one site → redirects to its first section) — one **Config** page with the
sections as a tab row (`lib/ui/ConfigTabs.svelte`: count, "edited" dot, error / warning counts; on the edge pages a
secondary link **Import nginx.conf**; nav2: no nested sidebar items): **`…/config/containers`** (table; row → container page; add / remove / duplicate / rename /
version disabled with tooltips "cloud only") · **`…/config/edge`** (the same edge editor + Test a URL as the cloud, local
values; the route page `…/config/edge/route?id=` / `?new=proxy|redirect|rewrite` and **Import from nginx.conf…**
`…/config/edge/import` as §17.2 — the edge is locally editable, so an import becomes a local proposal; the route page works
at 390 px) · **`…/config/volumes`** (round D: the site volumes — table Name (volumeSlug, monospace) · Label · Description ·
Kind · Starts with · On the server (location label + the absolute path from `siteVolumes`) · Back up (toggle) · Used by
(containers); **+ Add volume** / row **Edit** → side drawer (Name and Kind immutable after create · Label · Description ·
Kind Folder / Single file + FILE_VOLUME_RENAME_WARNING · Starter content Empty / From files / File (an archive seed is shown
read-only "cloud only") · Back up, default on · Location: Auto / Custom path, "./ = agent home"); row **Delete** only when no container uses it (else a dialog listing the users); the info line of §17.2
"Volumes are folders on the site server …"; allowed locally, §2.2) · **`…/config/settings`** (Host: Auto-detect toggle (null)
or a value · Depends · Not-found page (pages of the head catalog + none) · Agent port · Agent GUI alias port). The working copy
of the site is shared by its sections and its container pages. **Review changes** · **Create local proposal** (modal
Summary).

**`/_opm/app/{siteSlug}/config/containers/{containerSlug}`** — the container editor (the cloud sections; Name (containerSlug),
service and version locked (V_LOCAL_CONTAINERS / V_LOCAL_TAG); editable: ports (published, host port, bind address, edge),
volumes (the same single grouped dropdown per image volume as the cloud §17.2 — Data volumes with "shared with X" and
**+ New data volume…**, Files, Secrets, Advanced: host path…, Not mounted; Read-only toggle only for volume / host, default
off; column "Inside the container": file / secret rows as text "mounted read-only at <path>" + Edit path, volume / host rows
the effective path + Edit path → expression input field mountPath with the resolved preview; "shared copy · also used by X"
hints; extra mounts; the image volume cell shows a Folder / File badge (`imageVolumeKind`); inline flags: a single-file volume
on a Folder image volume ("… or set image volume X to File on the cloud image page"), a folder volume on a File image volume
(V_VOLUME_KIND_MISMATCH text), a single-file volume at a path ending with "/"; the validator issues incl. the V_FILE_VOLUME_RENAME
info under the row — images are not editable on the agent), env (a
choice env → a select of its options), command / entrypoint, restart, resources, health check, depends, enabled) · **Back** ·
**Review changes** · **Create local proposal** (modal Summary). No `new` route on the agent.

> **SUPERSEDED by §21 #104 (agent ≥ 0.14, cloud with branches)** — kept for agents < 0.14 (compat proposals) and as history. Current model: docs-v2/branches-commits.md.

**`/_opm/app/{site}/diff`** (Deployment › Diff, nav2 — replaces Updates) — the newest OPEN proposal (or `?rev=` = any
proposal; **Current proposal** goes back) compared with the head = what runs now ("Diff · head seq n → proposal seq m");
no open proposal → "No proposal yet" + the cloud next steps (CloudNextStep) + **What runs now** (head seq, summary, applied).
**Change the proposal** card: links to Variables · Secrets · Files · Config — local edits add up into ONE local proposal
made from the head (the existing local-edit bar at the bottom of Diff too); with unsaved edits the page lists them
("Your local changes", DiffTables) and warns "Creating a local proposal supersedes proposal seq m" (the newer proposal
wins, §21 #10; cloud-only changes of m are not in it). **History** (every proposal, newest first; row → `?rev=`): Seq ·
Origin · By · Summary · Status · Staging · Created. Detail: changes (diff
tables per section, with `DiffChange.note` hints such as "seed changed — applies only to new / empty volumes") · staging per site (state, progress, layers x/y, files x/y, blob sources (cloud / peer X), issues) ·
**Retry staging** (when a site failed) · Gate checklist (parent · reachable · staged · idle · status, each ✅/❌ with detail).
**Approve** (only when the gate is open) → POST approve/start → countdown modal (60 s, the changes summary, **Cancel**) → at 0
the modal shows: "Type ACCEPT to start the update on all sites. A snapshot is taken first; any failure rolls back every site.
Actor: <email>" · input · **Start update** (enabled only when the input is exactly ACCEPT) · **Cancel** → POST approve →
progress panel: steps LOCK · SNAPSHOT · APPLY · VERIFY · COMMIT per site with container actions (live from
GET updates/{id}, 1 s) → result banner (applied / failed-rolled-back + message) · **Failed containers** (§21 #87; one
expandable block per `containers[].failure`: site · container · exit code / OOM / restarts · the error · port map · "kept as
`<name>-failed-<unix>`" + `docker logs` hint · health history table (time · pass / fail / start period · message) · the
captured log tail) · **OK**. No schedule, no deny button.

> **§21 #105 (agent 0.14.1)**: GUI edits are NOT stored on the agent any more (no `PUT /_opm/api/branch/working`, no "edited
> here" rows): they stay in the browser (localStorage) until Commit… = `POST /_opm/api/branch/commit { message, baseCommitId,
> config }` applies them with the engine and writes the commit only on success. "Uncommitted" on the Branch page = changes made
> on the server (drift) only; they must be committed first (409 uncommitted-drift → "Commit server changes first"). See §21 #105.

**`/_opm/app/{site}/branch`** (§21 #104, as built; Deployment › Branch replaces Diff, badge = uncommitted + behind; `…/diff`
and `?rev=` redirect here) — tabs **Overview** (Cloud card: head seq, message, author, time, staged per site · This site card:
head, merged from cloud seq, ahead / behind / differs · Uncommitted card: edits · server changes · notes, last scan, Rescan,
Review, Commit… · **Merge from cloud** (disabled with the reason while anything is uncommitted; 409 dirty-working-copy reads
"This site has N uncommitted changes — commit or discard them first") · **Fast-forward all sites** · the update progress +
failed-containers panels while a merge runs), **Uncommitted** (rows by origin: edited here / changed on the server — docker,
file, secret-file, volume-content — with detail + Discard; drift notes with Discard / Accept / Take bytes; Commit… = CommitDialog
with item ticks), **History** (shared HistoryView; revert / reset run on the engine). `…/branch/merge?source=<commitId>` = the
shared MergeView (removals = "remove locally"; text diffs via GET /_opm/api/branch/blobs/{sha256}) → "Merge & apply". Variables /
Secrets / Files / Config edits go to the working copy (`PUT /_opm/api/branch/working`); the change bar offers Commit…; there is no
local proposal. Backups → Roll back keeps IntentFlow (60 s, ⏩, ACCEPT) and asks for a commit message in the last step.

**`/_opm/app/{site}/backups`** — **Volumes** (round D; per site, from `GET /_opm/api/sites/{siteSlug}/volumes`, site selector or one
block per site; header "agent home: <agentHome>"): table Name (volumeSlug) · Label · On the server (`path`, absolute) · Size
on disk · **Back up** on / off · Kind · Starts with (+ "seeded <time>" from `seeded`) · Used by (containerSlug → mount path,
read-only marker) · Last snapshot (time, size; none → "never") · **Reset to starter content…** (danger; only for declared
volumes with a seed and only on THIS site — other sites: disabled "open the agent GUI of this site") → POST reset/start →
countdown modal (60 s, "a snapshot of this volume is taken first; the containers that mount it stop, its content is
replaced by the starter content") → type ACCEPT → POST reset → result banner (entries written + the snapshot taken). Orphan directories (`declared: false`) are listed greyed with
"not declared — kept on disk, never backed up"; host-path mounts are listed below with "never backed up". Text: "Snapshots
contain the declared volumes with Back up on. Change Back up under Config › Volumes (local proposal) or in the cloud."
Snapshots table: Snapshot id · When · Reason · Head seq · Size · Sites (complete / missing) · Note · By · actions.
Buttons: **Take snapshot now** → modal: Note (≤ 500) → POST snapshots (progress toast) · **Import .opmsnap** → file picker →
upload progress → result (or the 422 message). Row: **Download** → modal: Include images (checkbox) + warning "contains the
config with secret values" → download · **Details** → side drawer (per site: config seq, images, archived volumes
by volumeSlug with containers, size and sha256 short) · **Edit note** → modal (Note) · **Roll back** → rollback planner panel (images from → to per
container (`containerSlug`), config changes, volumes restored (volumeSlug, size) / kept (volumeSlug, backup on / off), "data written after <time> is lost", new seq) + gate checklist + **Roll back** → the
same 60 s countdown + ACCEPT flow + progress panel · **Delete** → confirm modal.

**`/_opm/app/{site}/internet`** (Agent › Internet; deployment-wide) — one of: Free → **Give internet from this browser** (POST tunnel/start) · In use by another tab
(actor, site, since) → **Revoke the other tab** → confirm modal → then Start · Active in this tab → counters (requests, bytes,
last error), table of the last requests (reqId, method + path, step), **Stop**. Warning: "Payloads (also proposals with secret
values) pass through this tab. Keep it open; closing it ends the tunnel after 30 s." `beforeunload` warns while active.

**`/_opm/app/{siteSlug}/status`** (`*` = all) — containers table (Site · Container (`ContainerStatus.containerSlug`) · Service · Image (`ContainerStatus.image` = `<serviceSlug>:<version>`) · Kind · State · Health · Ready ·
Restarts · CPU · RAM · Ports · Started; "not managed" containers greyed) · host metrics · uptime heatmaps (24 h: 144 cells × 10
min; 30 days: 720 cells shown as 120 groups of 6 h) · certificates (Host · Mode · Issuer · Valid until · Last renewal · Last
error · **Renew** (confirm modal) · **Download local CA** (internal)) · container logs (container select, tail 200 / 1000 /
5000, search, **follow** (refresh every 3 s, keeps the newest line in view), **Download** (`opm-<site>-<container>-<ts>.log`)) —
the place to read the logs of a RUNNING container; a failed update's logs are on the Diff page. nav2: for ONE site the page also shows **Host** monitoring (moved from the
old Tools › Monitoring: CPU / memory / swap / load / uptime / disk cards with sparklines and amber / red badges, alert rows,
card → drawer with 1 h / 24 h / 7 d charts); container names link to the container page; quick actions open its tabs. Alerts on top: an edge
port the agent cannot bind ("port 80 is used by <owner> — stop it or change edge.httpPort") and the default-edge note.

**`/_opm/app/{siteSlug}/activities`** — filters (Type prefix, Actor) · table (Site · Seq · Time · Actor · Origin · Type ·
Summary) · row → side drawer (full record) · **Download bundle…** → …/activities/download · **Load more**.

**`/_opm/app/{siteSlug}/activities/download`** — page: From seq · To seq · Include debug log (inline form) · manifest preview ·
**Download .opmlog**. Text: send the file to the operations team; they upload it at /app/deployments/<id>/activities/upload.

**`/_opm/app/{siteSlug}/debug`** (Agent › Debug) — tabs **Debug log** | **Live agent log** (`?tab=agent`, the streamed
agent log of one site — the old Tools › Logs "Agent log"). Debug log: filters: minimum level · module · search · since; JSON lines list (monospace, newest first);
auto-refresh; **Load older**.

**`/_opm/app/{siteSlug}/transfers`** (Agent › Transfers; one site; §21 #100 — `{s}/send`, `{s}/receive` redirect) — two
columns (stacked ≤ 1100 px, Send first). **Send · <site> → cloud**: ONE **Send** button → choice **Browser** (a file on this
computer → modal File · Note → POST upload with progress) | **Path** (the server filesystem picker → Note → POST from-path). Rows:
file · state badge (`transferState(t,'agent')`: queued + retry n — error / uploading n % with a bar / stored in the cloud / stored ·
downloaded / expired) · size · when · by · source path (or "from the browser") · note · **Download**. **Receive · cloud → <site>**:
rows: file · state (pending + retry / downloading n % / on this site / saved on server + path / downloaded / expired) · size · when ·
from · note · expires · **Save on server** → modal Path (req, absolute; must not exist) → POST save · **Download** (both only once
the file is on the site). Files expire after 30 days.
**Server filesystem picker** (`lib/transfers/HostFsPicker.svelte`, modal "Pick a file on <site>"): text "The whole filesystem of the
server, read-only. Folders cannot be sent — pick a file (or tar it on the server first). At most 2 GiB." · up button · typed path
box + Go (a file path opens its folder with the file selected) · breadcrumbs · Hidden switch (+ hidden count) · refresh · list
Name · Size · Modified (folders first; folders expand inline lazily or open on click; symlinks show → target, a link to a folder
opens the target, dangling = "broken link"; lock + "no permission" and shield + reason entries and files > 2 GiB cannot be
picked) · banners: blocked folder, "showing the first 5000", "The agent (user X) cannot list this folder" + Back · footer:
selected path · size, "Reading as <agentUser>", Cancel · **Next**.

**Agent 404** — any unknown `/_opm/…` path: "OPM agent · page not found" + link to /_opm/app.

---------------------------------------------------------------------------------------------------------------------

### 18.T Docker section + the container page (§7.4, §21 #89; nav2 §21 #98)
Sidebar section **Docker** (Tools group and the global Logs / Terminal / Monitoring pages are gone, §21 #98):
`app/{site|*}/containers` (live table: state + health, image, published ports, uptime, restarts, CPU and memory with
60-min sparklines, "stopped manually by X" badge, quick actions logs / terminal / monitoring / inspect (= tabs of the
container page) + restart / stop / start with confirm — stopping an expected-running container says the health system
expects it running —; a row opens the container page; `*` merged) ·
**`app/{site}/containers/{ref}`** — the container as a FULL page (no drawer; `?tab=`; ← / → between tabs): **Overview**
(state, status, started / uptime, exit, restarts, restart policy, expected, CPU / memory with sparklines, image, service,
docker name / id, command, depends (explicit + implicit, links) and start order of the working copy, ports, health
history (OPM checks + Docker HEALTHCHECK log), mounts, environment with masked secret values) · **Logs** (the log viewer
of the old Tools › Logs for THIS container: stdout/stderr, 15 m / 1 h / 24 h / custom, tail, follow (EventSource,
reconnect with `since`), search + highlight + next/prev, timestamps, wrap, ANSI colours, Clear, Download .log; **Merge
with…** adds other containers merged by time) · **Terminal** (container exec: shell sh / bash / custom; xterm.js loaded
on demand from `/_opm/assets/vendor/` (bundled, no CDN); tabs ≤ 4 per site; copy / paste / font size / fit / fullscreen
/ End; idle warning; disabled tool = explanation with the agent.json setting; open sessions + transcripts (7 days) of
this container; none for a failed copy) · **Monitoring** (SVG charts 1 h / 24 h / 7 d) · **Inspect** (labels + the JSON
tree with copy) · **Processes & events** (docker top + Docker events 24 h) · **Config** (the container editor of
`config/containers/{c}` embedded; "not in the config" for a failed copy). `ref` = containerSlug, or the docker name of a
kept failed copy. Under `*` the page shows "Choose a site". ·
`app/{site|*}/volumes` (§21 #96 volume manager: volume cards (size, files, kind, busy) + a panel
with Snapshot now / Download / Copy… / Restore… / Empty… and the tabs Overview (size, files, kind, backup, location, last
snapshot, starter content + Reset, Duplicate guidance, users with their live state), Files (browser, viewer, edit ≤ 1 MiB
with a diff preview, upload files / folders + drag-drop with per-file progress and an overwrite question, new folder / file,
rename / move, multi-select delete with typed DELETE), Copies (restore from / delete), History (operations; row → progress
drawer); `*` = every site's volumes with Manage links) · `app/{site}/network` (tabs **Checks**: dns / tcp / http / ping
from host or container, result cards with timing bar, headers, output, tool, link to the edge Test-a-URL | **Edge access
log** (`?tab=access`, live, status / method / host+path / duration / size / client / upstream); one site) ·
`app/{site|*}/docker` (system df tiles, images table with In use / Unused / OPM / Protected, Prune with typed PRUNE).
**Host shell** = Agent › Agent › tab **Host shell** (`agent?tab=shell`: Modal where the site slug is typed; runs as the
agent.json user; host transcripts). The Status page container rows have quick actions (the container page tabs logs / terminal / monitoring / inspect,
restart / stop / start) and **Open Containers**. Every dropdown in the agent GUI is the custom Select / MultiSelect (`lib/ui/Select.svelte`, cloud console
behaviour; no native `<select>` anywhere, asserted by `tests/no-native-select.spec.ts` on every route).

**Intent waits (§21 #95).** Approve, Roll back, Reset to starter content, Empty and Restore use ONE modal
(`lib/ui/IntentFlow.svelte`): ring + mm:ss of the agent's remaining time, a speed badge ×1 / ×2 / ×4 (pop animation +
speed meter) and **⏩ Hold to fast-forward** next to Cancel (pointer / touch with `touch-action: none` and no context menu,
Space / Enter held; release, pointer leave / cancel, blur, hidden tab → ×1; the shown speed eases down over 400 ms; aria-live
"Speed ×4"). `lib/ui/intentClock.svelte.ts` animates between the agent's answers (speed / heartbeat / 1 s poll): ahead of the
agent it slows down, behind it catches up, > 3 s off it snaps; ready = the agent says ready. The confirm word is ACCEPT, or
the volume name for Empty / Restore (with options such as "snapshot first" in the same modal).
**Data-directory mismatch (§21 #96).** The Status page reads the last 300 log lines of a managed container that keeps
restarting (or is exited while enabled) and mounts a volume; the custom-pg banner "DATA DIRECTORY / IMAGE VERSION
MISMATCH" or "database files are incompatible with server" shows a hint with **Copy data volume aside, then empty it** →
a guided drawer: 1. copy to `volume-copies/` with the containers stopped, 2. Empty (wait + name; snapshot off by default
because the copy exists), 3. the container starts with an empty data directory.

### 18.D Dev tools (§7.6, §21 #97)

Accordion section **DEV TOOLS** after DOCKER: Connections `app/{site}/devtools` · SQL `…/devtools/sql` (PostgreSQL, SQLite,
Cassandra in one workbench; `devtools/postgres|sqlite|cassandra` → 307 `devtools/sql?kind=`) · Redis · MQTT · HTTP client ·
History. The connection is `?c=<connectionId>` (else the last used on that site + tool, else the first). `*` → ChooseSite
("a connection lives on one site"); switched off → a page linking the Agent page switch (`#set-devtools`). Every tool page
has the frame: connection picker grouped by kind, state badge (connected / idle / error, server version), "preset · from
revision seq n" / read-only badges, Test / Disconnect / Edit / Duplicate to edit locally, problem banner, Add connection.
- **Connections**: presets + manual; drawer (kind-specific target pickers from `devtoolsInfo`, credentials none / expression
  with autocomplete / literal with RevealValue, options, read-only), draft Test, Delete (confirm).
- **SQL**: CodeMirror 6 (lazy `assets/vendor/codemirror.js`), dialects, schema completion (columns of the referenced tables
  first), hover cards, lint of unknown tables / columns, tabs (localStorage per site + connection; never credentials), Run
  (selection or statement under the cursor) / Run all, Explain / Analyze (plan tree + text), params, transaction toggle,
  timeout, rows per page, cancel; results per statement (grid: virtualised, sort, filter, resize, selection, TSV copy, Load
  more, Export CSV / JSON / NDJSON, value viewer + full value download), inline edit of `SELECT * FROM t` with a known PK
  (generated statements shown, run in one transaction through executeSql); schema sidebar + table drawer (indexes,
  constraints, DDL, first 100 rows); Saved + History panels; destructive → typed connection name.
- **Redis**: SCAN (pattern, type, load more), tree on ":" / flat list, value pane per type with edits as commands,
  TTL / persist / rename / delete, console (history ↑↓, never storing credential lines, RESP-typed replies), Info.
- **MQTT**: live session (SSE), many filters with QoS, message list (≤ 5000, pause / clear / filter, retained / dup / qos),
  topic tree, payload JSON / text / hex / base64, publish (QoS, retain, clear retained → confirm), topics seen, dropped.
- **HTTP client**: request tabs, `{{var}}` / OPM expression highlighting + resolved preview, params ↔ URL, headers, body
  kinds, auth inherit / override, environments (secret vars), collections, history, response Body (pretty / raw / preview in
  a sandboxed srcdoc / hex), Headers, Cookies, Timing, Redirects, Request as sent, full body download, copy as curl.
- **History**: `devtools.query` of the site (kind / connection / mine / failed), expand, Open in the workbench.

### 18.A Agent page (§7.5, §21 #90)

**`/_opm/app/{siteSlug}/agent`** — Agent › **Agent** (badge = number of sites below the pin; nav2: no in-page site
picker — the global selector; tabs **Agent** | **Host shell** (§18.T)). `*` = one card per reachable site (version, installed / latest, badge, uptime, supervisor, **Open**) + **Upgrade
all**; one site = the sections below (another site is served over the mesh; writes forwarded with the actor). Everything
`opm-agent` does on the command line is here except `keygen` / `enroll` / `install`.

- **Overview** (`GET …/agent`, refresh 10 s): version · build (go version, os/arch, commit, build time) · pid + uptime ·
  config path · data dir · binary + `opm-agent.prev` (version, or "none") · ports (agent, GUI alias, default edge, edge
  http/https + last error) · Docker (version, API, host, error) · systemd (unit, active/sub state; **boot guard**
  installed / missing — missing → "rerun the install command or upgrade once to install it") · enrollment (state, enrolled
  date, site key fingerprint `SHA256:<b64>` with Copy; revoked / not enrolled → the re-enroll lines, copyable) · cloud link
  (route, last fetch, last heartbeat, last error, **Open console**).
- **Release + upgrade**: installed vs latest (the pin from the cloud view) + badge (up to date / upgrade available / pin
  older than installed) · release notes · **Check release** (`POST …/agent/release/check`) → the verification card
  (signature ok, key source agent.json / cloud, key fingerprint, sha256 match, self-test; red with the error when it fails)
  · **Upgrade to X** (enabled when available and not `busy`; disabled with the reason: "an update is running" / "an upgrade
  is running") → modal: from → to, notes, "The agent restarts; the containers keep running. If the new version is not
  healthy within 60 s it is rolled back to <from> automatically." + typed site slug (+ **Reinstall** checkbox = `force`
  when already on it) → `POST …/agent/upgrade`. 409 stale → "The pinned version changed to Y — reload" (reload button).
- **Progress + restart overlay**: phase stepper Download (bar, bytes / total) → Verify (the verification card) → Swap →
  Restart → Health (60 s countdown) → Done / Failed (phase + error) / Rolled back (from → to restored, reason, watcher /
  guard). During Restart / Health a full-page overlay "Agent restarting…" polls `GET /_opm/api/health` (fallback info) every 2 s (≤ 120 s;
  then "still not back — check `systemctl status opm-agent` on the server") and continues on the same session; the result
  banner shows the new version. **History** (last 10: when, via, actor, from → to, result).
- **Upgrade all** (mesh; this site coordinates): modal with every site (from → to / "skipped: below 0.10.0 — rerun the
  install command on that server" / "unreachable") and the order (this site last) + typed UPGRADE → `POST
  /_opm/api/agent/upgrade-all`; run panel (per site status) + **Cancel run**; after this site's own restart the run panel
  resumes from `GET /_opm/api/agent/upgrade-all`.
- **Restart agent** → modal (reason ≤ 200, typed site slug; "the containers keep running") → `POST …/agent/restart` →
  the same overlay.
- **agent.json**: read-only JSON view of `masked` (copy) + path; **Edit settings** → side drawer: Host shell (on/off) ·
  Container exec (on/off) · Container names plain / prefixed (+ the rename info) · Default edge HTTP port · GUI alias port
  (empty = none) · Restart now (default on); inline `validateAgentSettings` issues; **Save** → modal with the changed keys
  + typed site slug → `PATCH …/agent/config`. Not writable → the drawer is read-only with `writableReason`.
  `restartPending` → banner "agent.json changed on disk — restart to apply" + **Restart agent**.
- **Health** (§21 #91, `GET /_opm/api/health`): ok / not ok + each check, schema, cloud connected (route, last contact),
  Docker, socket activation on / off.
- **Hard fallback** note: "If an upgrade from here or the cloud cannot run (or you need a downgrade), rerun the site's
  install command on the server: it asks UPGRADE / DOWNGRADE / SAME, keeps the setup, backs up the DB and rolls back by
  itself (§19.1)."
- **Support bundle**: the section list (SUPPORT_BUNDLE_SECTIONS) + "no secrets, keys, transcripts or volumes" →
  **Download support bundle** (`POST …/agent/support-bundle` → link → download `opm-support-<site>-<ts>.tar.gz`).
- **Uninstall**: text "Uninstall runs on the server, never from here." + the copyable `uninstall` lines (the data wipe lines
  marked optional).
- **CLI map** (collapsible): `opm-agent version` / `status` → Overview · `verify-release` → Check release · `upgrade` →
  Upgrade to X · `snapshot list / export / import` → Backups · `keygen` / `enroll` / `install` → CLI only (the install
  command from the cloud Setup page).

Agents below 0.10.0 have no Agent page: they upgrade ONCE by rerunning their original install command on the server
(§19.1); from then on upgrades are a button here or in the cloud console. **Backups parity**: Backups (above) already covers
`opm-agent snapshot list / export / import` (list, Download .opmsnap with / without images, Import .opmsnap).

## 19. Install, operations, configuration

### 19.1 Install script (`GET /v1/install/script`)

One copy-paste on the server (the Setup page shows it in RevealValue, §21 #84):

```sh
curl -fsSL -H "Authorization: Bearer opi_…" <apiBase>/v1/install/script | sudo sh
# "Advanced: skip Docker install" (the env must survive sudo, so it goes AFTER sudo):
curl -fsSL -H "Authorization: Bearer opi_…" <apiBase>/v1/install/script | sudo OPM_SKIP_DOCKER_INSTALL=1 sh
```

The cloud generates a POSIX `sh` script for ONE token (source: `backend/functions/src/routes/install-script.ts`, `sh -n` +
shellcheck clean). **Everything it installs is pinned at generation** — nothing resolves "latest" at run time:
- agent: exact version (`deps/manifest.json` `agent.version`, else `OPM_AGENT_VERSION` / `agent/latest` at generation) +
  per-arch sha256 + release signature embedded; downloaded from `/v1/install/binary?os=linux&arch=…&version=<pinned>`.
- Docker Engine: the official static bundle `docker-<ver>.tgz` (download.docker.com) re-hosted in Storage
  (`deps/docker/<ver>/<arch>/docker-<ver>.tgz`, `backend/scripts/upload-deps.ts`, `deps/manifest.json` = `DepsManifest`),
  version + per-arch sha256 embedded; downloaded from `GET /v1/install/deps/docker?arch=amd64|arm64` (install token,
  same rules as installBinary; headers `X-OPM-Sha256`, `X-OPM-Version`). Pinned: **Docker 28.5.2**,
  `minDockerApi` **1.41** (user decision: Docker 20.10 is the minimum; the agent negotiates the Engine API between 1.41 and
  1.47 → an existing Docker fits when its max API ≥ 1.41 and its MinAPIVersion ≤ 1.47: Docker 20.10 and newer, incl. 29.x).

Steps (numbered `[n/7]` on screen, every line + every command's output with timestamps in `/var/log/opm-install.log`):
1. **Preflight** — each reported OK / MISSING: root, Linux, systemd, arch amd64 / arm64, curl, tar, kernel ≥ 3.10,
   cgroups, ≥ 2 GiB free in /var/lib, iptables or nftables (missing tools: the exact package-manager command is printed —
   apt-get / dnf / yum / zypper / apk; distro packages are never installed by the script).
2. **Docker** (§21 #91 b) — `docker` + a running daemon with API ≥ minDockerApi (and a daemon minimum ≤ 1.47, the agent's
   cap) → used as is WITHOUT asking (manual installs of any method are fine), version printed; installed but stopped →
   `systemctl enable --now docker` (printed; 30 s wait, else a `journalctl -u docker` hint); installed but too old / too new
   → **not touched**, the script fails with a manual instruction. Missing → the question "Docker Engine is not installed.
   Install the pinned Docker 28.5.2 now? [y/N]" (`/dev/tty`; `OPM_INSTALL_DOCKER=1|0`, `OPM_YES=1` answers yes;
   `OPM_SKIP_DOCKER_INSTALL=1` = alias of `OPM_INSTALL_DOCKER=0`). Yes → the pinned static bundle: download, sha256 check,
   extract to /usr/local/bin, `docker` group, systemd units `containerd.service`, `docker.socket`, `docker.service`
   (upstream units adapted to /usr/local/bin), `/etc/docker/daemon.json` (json-file logs max-size 10m / max-file 3,
   live-restore true) when absent, enable + start, wait ≤ 60 s for `docker info`; recorded in
   `/etc/opm/installed-deps.json`. No → manual instructions (any Docker ≥ 20.10 from https://docs.docker.com/engine/install/
   or the distro package, then rerun the same command), exit 1. The docker CLI is only ever called for version / info.
3. **OPM agent** (§21 #91 c) — target = the pin embedded at generation, `?version=x` on the script URL (embedded by the
   cloud; only versions uploaded under `agent/<v>/`, else a tiny script "agent x is not uploaded", exit 1) or
   `OPM_AGENT_VERSION=x` (fetched from `GET /v1/install/release`); target schema / minSchema from the release metadata
   (absent = legacy 1 / 0). No binary → fresh install, no question. Installed agent (binary + unit + agent.json + enrolled
   data dir): installed version (`opm-agent version --json` from 0.10.0, else the text line) and its DB schema (json
   `schema`, else `AGENT_LEGACY_SCHEMA` = 1) → `agentInstallAction` → "Installed A → available B: UPGRADE | DOWNGRADE |
   SAME (reinstall)" + "[y/N]" (`OPM_YES=1`); no → "Nothing changed", exit 0. **DOWNGRADE** needs a second confirmation
   (`OPM_ALLOW_DOWNGRADE=1`; `OPM_YES` does not answer it) and is refused when `agentSchemaCompatible(dbSchema, target)` is
   false, unless the newest backup under `<dataDir>/backup/*/meta.env` with a compatible schema is restored (own
   confirmation, `OPM_RESTORE_DB_BACKUP=1`; activities / sessions written after it are lost); no such backup → "downgrade
   refused", exit 1, nothing changed. Download from `/v1/install/binary?…&version=<target>`; sha256 checked; the release
   signature verified BEFORE the new binary runs (openssl Ed25519 → else the installed agent's `verify-release` → else the
   new binary's `verify-release`).
4. **Configuration** — `/etc/opm/agent.json` written when absent (an existing one for another deployment / site → fail,
   nothing overwritten); an existing one is kept and only gains missing keys with defaults (`apiBase`,
   `releasePublicKey`; printed as `+ "key": "value"`; `agent.json.bak`). Enrolled (the running agent's `/_opm/api/info`,
   else `opm-agent status`) → the token is NEVER written (no re-enroll; a revoked enrollment needs /var/lib/opm moved
   away); else the token goes to `/etc/opm/install-token` (0600; the agent deletes it after enrolling). Kept on every
   rerun: agent.json, site key, enrollment, data dir, volumes, files, certificates, transcripts; app containers are never
   touched.
5. **Service** — for an up- / down- / reinstall: the old agent's health is read first (`/_opm/api/health`
   `cloud.connected`) → `systemctl stop opm-agent` (containers keep running) → `<dataDir>/*.db*` copied to
   `<dataDir>/backup/<oldVersion>-<UTC ts>/` + `meta.env` (version, schema, createdAt, reason; the 5 newest kept) → optional
   restore (step 3) → `opm-agent.prev` kept, the new binary renamed over `opm-agent`. Units are written by the NEW binary:
   `opm-agent systemd-units --install --socket|--no-socket` (service §19.2 + `opm-agent.socket` + the boot-guard drop-in;
   `--no-socket` when `OPM_SOCKET_ACTIVATION=0` or systemd < 235) when `systemd-units --help` exits 0; else the legacy §19.2
   unit (+ `upgrade-guard --install` when available) and any `opm-agent.socket` disabled + removed. `systemctl
   daemon-reload`, socket enable + start (when the file exists), service enable + restart. `DRY_RUN=1` prints these.
6. **Verify** (§21 #91 e) — ≤ 60 s for `GET http://127.0.0.1:<agentPort>/_opm/api/health` = 200 with `"version":"<target>"`
   (+ `cloud.connected` true when the old agent was connected before the swap; targets < 0.10.0: `/_opm/api/info` with that
   agentVersion; a fresh install: `/_opm/api/info` reports `enrolled: true`). Not healthy → automatic rollback: stop,
   `opm-agent.prev` back, the DB backup restored when the schema changed (or a backup was restored), units rewritten for
   the old binary, restart, wait for it, print what happened + the log path, exit 1. The agent's own boot guard (§7.5)
   stays the second layer.
7. **GUI addresses and firewall** (§21 #86; never fails the script, never changes firewall rules) — listening TCP ports
   (`ss -ltnH`, else `netstat -ltn`, else /proc/net/tcp{,6}); GUI URLs only for ports that listen, **port 80 first**:
   `http://<ip>/_opm/`, `https://<ip>/_opm/` (443), then the edge port, `:8080`, `:7080` (the first IPv4 of `hostname -I`).
   :80 not listening (or held by another program, `ss -p`) → "port 80 is not listening — another program may hold it
   (`ss -ltnp 'sport = :80'`); the agent retries every 30 s; GUI also on :8080 :7080". Firewall report: ufw active → the
   exact `sudo ufw allow <p>/tcp` per blocked port of 80 / 443 / 7080 / 8080; firewalld running → `firewall-cmd --permanent
   --add-port=<p>/tcp` + `firewall-cmd --reload`; iptables / nft input policy drop → a generic hint; else "no host firewall
   active (ufw / firewalld)". Self-check: `curl -m 3 <url>healthz` on the first IPv4 and on SEEN_IP (the address the cloud
   saw when the script was requested) → "reachable / NOT reachable from this host", then the note: a cloud firewall
   (DigitalOcean Cloud Firewall, AWS security group) or NAT in front of the server is invisible from inside it — open the
   port there too. Last line (§21 #90): "Upgrade later: cloud console → Setup → Upgrade agent, or the agent GUI → Agent page
   (agent >= 0.10.0). Older agents: rerun this same command (it stays valid for upgrades of this enrolled site)"; §21 #91:
   the same command stays the hard fallback for any up- / downgrade.

**Upgrade** (§21 #86): rerun the SAME install command on the server. A token that enrolled the site's CURRENT enrollment
(`agents/{site}.enrolledAt == token.usedAt`, not revoked) keeps working for `installScript` / `installBinary` /
`installDeps` without expiry; it returns the **upgrade-only** script (`UPGRADE_ONLY=1`): it never writes the token and fails
unless /etc/opm/agent.json + the enrolled data dir belong to that site; it installs the agent version pinned in
`deps/manifest.json` and restarts the service. Revoke (or a re-enroll with a newer token) ends it. Agents ≥ 0.8 also have
`sudo opm-agent upgrade [--check] [--force] [--no-restart] [--pubkey B64]`: the site signs `GET /v1/agent/release?os&arch`
(→ `AgentReleaseResponse`: pinned version, sha256, release signature, public key), verifies the signature with agent.json
`releasePublicKey` (written by new install scripts; else the cloud's key; a mismatch refuses), downloads
`GET /v1/agent/binary?os&arch&version` (signed), checks the sha256, replaces the binary atomically and runs
`systemctl restart opm-agent`. No install token is needed.
§21 #90: from agent **0.10.0** (`GUI_UPGRADE_MIN_VERSION`) the upgrade is a button — agent GUI Agent page (§18.A) or cloud
console Setup → **Upgrade agent** / **Upgrade all** (§17.3) — with the same code path as `opm-agent upgrade` (§7.5: verify,
keep `opm-agent.prev`, restart, health ≤ 60 s, automatic rollback). An agent below 0.10.0 (e.g. 0.7.2, which has no
`upgrade` command) upgrades ONCE by rerunning its original install command on the server (the upgrade-only script above,
which also installs the boot guard); after that, upgrades are a button.

**Hard fallback** (§21 #91): whatever state the GUI / CLI upgrade is in, rerunning the site's install command is the
fallback for ANY up- / down- / reinstall — it asks (steps 2–3), keeps the setup (step 4), backs up the DB, swaps, and rolls
back by itself when the target is not healthy (steps 5–6). **Prompts under `curl | sh`** read `/dev/tty`; without a
terminal the script never blocks: an unanswered question = "no", and it prints the exact rerun command with the flag
placed after `sudo` (terminal only; the log never gets the token), e.g. `curl -fsSL -H "Authorization: Bearer opi_…"
<apiBase>/v1/install/script | sudo OPM_YES=1 sh`. Flags: `OPM_YES=1` (install / up- / reinstall + Docker),
`OPM_INSTALL_DOCKER=1|0` (`OPM_SKIP_DOCKER_INSTALL=1` = 0), `OPM_ALLOW_DOWNGRADE=1`, `OPM_RESTORE_DB_BACKUP=1`,
`OPM_AGENT_VERSION=x`, `OPM_SOCKET_ACTIVATION=0`. **Unusable tokens** (§21 #91 g): an expired / revoked / used-elsewhere /
unknown token or a deleted deployment → `installScript` answers 200 with a tiny `#!/bin/sh` script that prints e.g. "OPM
install: token expired (site db) — open the cloud Setup page → Install command → Regenerate (or View) and run the new
command." + "Nothing was changed on this server." and exits 1 (`curl -f` hides error bodies); no Authorization header →
401; installBinary / installDeps / installRelease keep 410 gone.

Failure: the step that failed, the last 20 log lines, the log path and a short MANUAL instruction for that component, exit 1.
Rerunning the same command is safe: before enrolling the token is valid 24 h; after enrolling it only up- / down- / reinstalls that site
(above). Completed steps are detected and skipped. `DRY_RUN=1` changes no systemd unit / service (used by the container e2e test). Offline bundle (`?offline=1`, a
.tar with script + agent + Docker bundle) is a later step. (`verify-release` checks the Ed25519 signature of the sha256 hex
string with the public key the script passes; the binary cannot fake it.) Release signing key: env
`OPM_RELEASE_PRIVATE_KEY` in CI only.

### 19.2 systemd unit

```
[Unit]
Description=On-Prem Manager agent
After=network-online.target docker.service
Wants=network-online.target
Requires=docker.service
[Service]
Type=simple
ExecStart=/usr/local/bin/opm-agent run --config /etc/opm/agent.json
Restart=always
RestartSec=5
LimitNOFILE=65536
KillMode=process
[Install]
WantedBy=multi-user.target
```
(Containers keep running when the agent restarts; on start the agent adopts the running OPM containers by label.)
The main unit is unchanged by §21 #90. Next to it, the boot-guard drop-in
`/etc/systemd/system/opm-agent.service.d/upgrade-guard.conf` (written by `opm-agent upgrade-guard --install`: by the
install script and by the agent before every upgrade, followed by `systemctl daemon-reload`; idempotent; a note and exit 0
when not root / no systemd):
```
[Service]
ExecStartPre=-/bin/sh /usr/local/lib/opm/upgrade-guard.sh /var/lib/opm /usr/local/bin/opm-agent
```
(arguments = the data dir and the binary path of this install). The `-` prefix: a failing guard never blocks the start. The
guard restores `opm-agent.prev` when `upgrade/pending.json` is past its deadline without `upgrade/healthy` and the binary
still has the new sha256 (§7.5). Upgrade watchers run as transient units `opm-agent-upgrade-watch-<id>` (`systemd-run
--collect`), outside the service's lifetime.

**Socket activation** (§21 #91 f; edge continuity during a restart). `/etc/systemd/system/opm-agent.socket` — ONE socket
unit, because systemd applies `FileDescriptorName=` to every fd of a unit; the agent matches the inherited fds
(`LISTEN_PID` / `LISTEN_FDS` / `LISTEN_FDNAMES`) by their local port:
```
[Unit]
Description=On-Prem Manager agent sockets
[Socket]
ListenStream=80          # agent.json edgeHttpPort (default 80; omitted when null)
ListenStream=7080        # agentPort
ListenStream=8080        # guiAliasPort (omitted when null)
FileDescriptorName=opm
Service=opm-agent.service
[Install]
WantedBy=sockets.target
```
With the socket installed the service gains `After=opm-agent.socket` + `Requires=opm-agent.socket`. The agent serves those
ports from the inherited fds (the edge fd is handed to Caddy) and binds every other port itself (443, head edge ports,
alias changes) — also the fallback when not socket-activated. While the process restarts the kernel queues connections in
the systemd-held socket instead of refusing them (measured: 0 / 2184 failed requests with the socket vs 4 / 2144 refused
without, §20). Requires systemd ≥ 235; `OPM_SOCKET_ACTIVATION=0` (or an older systemd / a failed socket start) → no
socket unit. A GUI change of edgeHttpPort / guiAliasPort rewrites the socket unit and runs `systemctl restart --no-block
opm-agent.socket opm-agent.service` (brief rebind of that port). With the socket enabled, a connection to a held port
starts a stopped service again (socket semantics): uninstall disables the socket first. All units (service, socket,
guard drop-in) have ONE source, the Go binary: `opm-agent systemd-units --install [--config P] [--socket|--no-socket]
[--no-reload] [--binary B] [--data-dir D]` (`--print` dumps them; `opm-agent install` and the install script use it).

### 19.3 Environment / configuration

| where | name | meaning |
|---|---|---|
| backend | `OPM_API_BASE` | public API base used in install commands (e.g. `https://api.opm.example`) |
| backend | `OPM_FRONTEND_ORIGINS` | CORS origins for human endpoints |
| backend | `OPM_RELEASE_PUBLIC_KEY` | base64 Ed25519 public key embedded in the install script |
| backend | `OPM_AGENT_VERSION` | default agent version served by /v1/install/binary (else `agent/latest`) |
| backend | `OPM_CONSOLE_URL` | cloud console origin sent to agents (CloudView / EnrollResponse `consoleUrl`, shown as links in the agent GUI); default = the first https entry of `OPM_FRONTEND_ORIGINS` (prod: https://opm.example.com) |
| backend | `OPM_SMTP_URL` | optional, mail for password-reset links (Reset link…) |
| backend | `OPM_BLOB_REDIRECT` | `1` = downloads answer 302 to signed URLs (production); default `0` = stream (emulator). Human `GET /v1/blobs/{sha}` always streams bodies ≤ 8 MiB (`BLOB_STREAM_MAX`) |
| backend | `backend/storage.cors.json` | bucket CORS (GET/HEAD from the hosting origins + localhost:5173 / 127.0.0.1:5173), applied by the deploy script (`gcloud storage buckets update gs://<bucket> --cors-file=storage.cors.json`) |
| frontend | `VITE_OPM_API_BASE`, `VITE_FIREBASE_*`, `VITE_OPM_GOOGLE_DOMAIN` | API base, Firebase web config, optional SSO domain |
| agent | agent.json | §2.4 / `AgentConfigFile` (§21 #86: `edgeHttpPort` default 80 / null = no default edge; `releasePublicKey`; `consoleUrl` fallback) |
| agent | `--data-dir` / `OPM_DATA_DIR` | **agentHome** = the agent data dir: the flag, else the env, else agent.json `dataDir`, else `/var/lib/opm`. Holds everything of §2.4; a storage location `./x/y` (files, site volumes) is relative to it. Reported to the GUI in `SiteVolumesResponse.agentHome` |
| agent | `OPM_APPROVE_WAIT_SEC` | every intent wait — approve / rollback / volume reset / empty / restore (default 60; tests may set ≥ 1); effective seconds, fast-forward up to ×4 (§21 #95) |
| agent | `OPM_COMPLETE_TIMEOUT_SEC` | readiness timeout per container (default 300) |
| agent | `DOCKER_HOST` | Docker socket (overrides agent.json dockerHost) |
| agent | `OPM_LOG_LEVEL` | debug log minimum level (default info) |
| agent | agent.json `tools` / `hostShell` | §21 #89: `"tools": {"hostShell": true, "containerExec": true}` (defaults; false hides + refuses the tool), `"hostShell": {"user": "<agent user>", "shell": "<login shell>"}`; read at agent start |
| agent | agent.json `tools.devTools` + `devTools` (§21 #97) | `"tools": {"devTools": true}` (default; false → every dev-tools endpoint 403 with the reason), `"devTools": {"statementTimeoutSec": 30, "maxRows": 1000, "bufferRows": 10000, "maxCellBytes": 65536, "maxResultBytes": 67108864, "maxResponseBytes": 10485760, "idleTimeoutSec": 600, "maxConcurrent": 8}`; read at agent start |
| agent | agent.json safe settings (§21 #90) | `tools.hostShell`, `tools.containerExec`, `tools.devTools` (§21 #97), `containerNames`, `edgeHttpPort`, `guiAliasPort` are also editable from the agent GUI Agent page (validated, atomic write, `agent.json.bak`, restart); every other key: edit the file + restart |
| agent | agentHome `upgrade/` (§21 #90) | upgrade state, history, pending / healthy markers, rollback record, mesh run, handled cloud requests (§2.4) |
| install script | `OPM_YES`, `OPM_INSTALL_DOCKER` (`OPM_SKIP_DOCKER_INSTALL` = alias of 0), `OPM_ALLOW_DOWNGRADE`, `OPM_RESTORE_DB_BACKUP`, `OPM_AGENT_VERSION`, `OPM_SOCKET_ACTIVATION`, `DRY_RUN` (§21 #91) | answers to the `/dev/tty` questions without a terminal (env after `sudo`); `OPM_YES` does not answer the downgrade or the DB restore; tests only: `OPM_NO_TTY`, `OPM_HEALTH_TIMEOUT` |
| install script | `?version=x` on the script URL (§21 #91) | target an uploaded agent version instead of the pin |
| agent | agentHome `backup/` (§21 #91) | DB backups `<version>-<ts>/` + meta.env, 5 kept (§2.4) |
| backend | `upload-agent.ts` `<dir>/release.json` → `agent/<v>/release.json` (§21 #91) | AgentBuildMeta (schema, minSchema; absent = legacy 1); headers `X-OPM-Agent-Schema` / `-Min-Schema`; `upload-deps.ts` copies them into `deps/manifest.json` agent |
| backend | `upload-deps.ts --agent-notes` / `--agent-notes-file` (§21 #90) | release notes of the pinned agent → `deps/manifest.json` `agent.notes` → Setup + Agent page |
| agent | agentHome files (§21 #89) | `metrics.db` (monitoring, ≤ 64 MiB), `transcripts/` (terminal transcripts, 7 days, ≤ 200 MiB, 0600), `logs/edge-access.log` (+ .1–.5, 20 MiB each), `tools/manual-stops.json` |

Firebase: `firebase.json` — hosting `public: frontend/build`, rewrites `{ "source": "/v1/**", "function": { "functionId": "api" } }`
then `{ "source": "**", "destination": "/index.html" }`; functions source `backend`; firestore rules + indexes; storage rules.
Emulators (dev): auth 9099, firestore 8085, functions 5001, storage 9199, hosting 5000, UI 4000.

---------------------------------------------------------------------------------------------------------------------

## 20. Test plan

### 20.1 Unit tests (must pass in CI)

- `shared`: `npm test` — expression vectors (TS), check vectors, JCS + activity hash + Ed25519 signing vectors, edge resolver,
  access rules, validation (hello config valid; each rule family), diff.
- `agent`: `go vet ./... && go test ./...` — the SAME vector files (`internal/expr`, `internal/canonical`), model round trips;
  later: driver spec hash, edge Caddy JSON golden files, update coordinator with a fake driver, smart-distribution fetcher choice.
- `backend`: route handlers against the Firestore emulator: auth (ID token, disabled, signed requests incl. clock/nonce/
  signature failures, relay), access rules per route, publish/stale/superseded, activities upsert (skip / reject chain),
  uploads compose + sha256, install token lifecycle, enroll conflict.
- `frontend`: svelte-check + Playwright smoke on the emulator (login, create registry/service/image, template, deployment, diff).
- Phase U additions:
  - `shared/testdata/expr-vectors.json` has three sections: `cases` (resolve), `check` (save-level check) and `containers`
    (`resolveContainer`: env + mount sources → resolved values or errors with code + position; covers `container.env.<NAME>`,
    mount paths that use env, `file.<slug>.path` = a resolved mount, E_CYCLE, a failed dependency, secret env from imageEnv /
    mountPath → E_NOT_ALLOWED, `container.serviceId` → E_UNKNOWN_PATH). TS (`expr.test.ts`) and Go (`internal/expr`) run all
    three sections.
  - `validate.test.ts`: V_OPTION (default and container value), legacy list normalization, V_PATH (save and publish),
    V_REFERENCE + `parseImageReference` / `registryHost` / `matchRegistries` / `nextVersion`, icon formats.
  - `agent`: `TestCatalogOptionsAndIconsRoundTrip`, `TestImageEnvOptions`, `TestReadableNameNotHashed`, old-shape revision
    refused, `TestTwoSiteMesh` (readable tags through the Docker API; kv runs from `kv:1`; a mount at
    `/{{ container.env.DATA_DIR }}` with a choice env).
  - `backend` `test/10-phase-u.test.ts`: services by slug, images by version (`version-exists`), source auto-match / public /
    host mismatch, icons (upload, dedupe, sanitizing, public content headers, delete conflict), registry browsing (Docker v2 and
    Docker Hub API stubs), the Docker Hub JWT access-claim check, and the migration: old-shape data → dry run changes nothing
    → apply → `verifySchema` reports 0 violations → a second run plans 0 changes.
- Round C additions:
  - `shared`: expr vectors for field `file` (multi-line text, block positions → line:col, secrets allowed), context
    `auto-host` (`<auto:slug>` placeholder in the cloud, E_NULL in agent mode) and the `{{ "{{" }}` escape; `validate.test.ts`:
    two instances of one service on a site (V_DUPLICATE only on the same containerSlug), V_RESERVED `new`, V_VOLUME (extra
    mount without source, host path with `{{ }}`), a volume shared by two mounts, port row `edge: false` +
    proxy route → V_EDGE_ROUTE, port conflicts ignoring hostIp, host null / host expression without value → V_HOST_AUTO
    warning (not an error), V_FILE_RENDER with line:col and the container name, V_FILE_JSON, `normalizeContainer` of the
    legacy shape, `duplicateContainer`, `suggestContainerSlug`, `checkLocalEdit` (V_LOCAL_CONTAINERS / V_LOCAL_TAG).
  - `agent`: the Go expr port passes the new vectors; spec hash changes when hostIp / entrypoint / healthcheck / a rendered
    file changes and NOT when only the auto address changes and nothing uses it. (The round-C `volumes/<serviceSlug>/<name>/`
    move and the per-container `rendered/<containerSlug>/` files are superseded by round D part 2.)
  - **Mesh test** (`TestTwoSiteMesh` + e2e): the same image twice on one site (`hello-web` + `hello-web-2` with another THEME,
    two routes, two docker containers `opm_<dep>_app_hello-web` / `…_hello-web-2` from one image tag `hello-web:1.0.0`); a
    site with host null that resolves to its advertiseHost (`site.<s>.host` in a peer's env); a text file with
    `render: 'expressions'` mounted by both instances (round D part 2: rendered ONCE for the site and shared — per-container
    content now comes from env, not from the file) and a JSON file renders to valid JSON.
  - `backend`: `GET /v1/blobs/{sha}` 200 ≤ 8 MiB with ETag / cache headers, 302 above; heartbeat / enroll store
    `detectedAddresses`; usage items per container instance; `migrate-c.ts` dry run / apply / idempotent.
  - `frontend`: container full page (new / edit / duplicate), host auto toggle, file editor render toggle + preview.
- Round D part 2 additions (one copy per site, storage locations):
  - `shared`: `checkLocation` / V_LOCATION (bad `./` segments, reserved agent dirs, bare kind dir, unsafe absolute path,
    `{{ }}`, same / nested locations of files + volumes per site; relative vs absolute skipped), `locationPath` /
    `locationLabel` / `normalizeLocation`, missing location = auto; expr vectors: field `file` with `container.*` / `file.*` →
    E_NOT_ALLOWED with the hint; `renderSiteFile` + `siteFileExprContext`; V_FILE_RENDER `(site <s>)`.
  - `agent`: StorageLocation model mirror; validate / expr mirror (same vectors); the driver binds the single host file /
    secret / volume location; staging renders once per site into a staged temp + APPLY atomic rename (modes 0400 / 0444); blob
    store `files/<hex>` → `blobs/files/<hex>` at start; `vol/<slug>` → volume location on first apply; snapshot + restore by
    location; `siteVolumes` `agentHome` / `path` / `location`.
  - **Mesh test**: two containers mount the same file → exactly ONE host file exists and both see it; a file with a `./`
    location; a volume with a custom `./` location is backed up and restored there; `container.env` in a file → staging fails
    E_NOT_ALLOWED.
  - `backend`: location normalized on write / read; `verify-schema.ts` (locations present + valid); `migrate-d.ts` sets
    location auto on files and volumes and REPORTS (no rewrite) every file with render expressions whose content uses
    `container.*` / `file.*` (it becomes a validation error on the template).
  - `frontend` / `agent/gui`: Files "On the server" column + Location control, "Preview for site…"; Volumes location column
    and modal control; container rows "Inside the container", file / secret as text + Edit path, "shared copy · also used by";
    summary Mounts lines with the server location. Prod check `tests/prod/d-files.spec.ts` on the template copy.
- Round D additions (declared site volumes, sidebar navigation):
  - `shared` `validate.test.ts`: site volume fields (V_REQUIRED / V_TYPE / V_DUPLICATE / V_FORMAT / V_LENGTH / V_TOO_MANY),
    V_VOLUME_UNDECLARED, V_VOLUME_UNUSED (warning), V_VOLUME_NOT_MOUNTED (warning; an explicit `none` row does not warn),
    V_READONLY, the removed kind `data` → V_ENUM with the hint; `normalizeSiteConfig` of a pre-D config (implicit image volumes
    → explicit rows, `data` → `volume` with readOnly reset to false, every volume declared, idempotent; a current config is
    never auto-declared); `volumeUsers` / `undeclaredVolumes` / `suggestVolumeSlug` / `duplicateContainer`; diff section
    `volumes` (added / changed / removed, also for a new site); `checkLocalEdit` allows adding / editing / removing volumes.
  - `agent`: model mirror decodes legacy `data` sources (old revisions keep their hash); validate + diff mirrors; driver: a
    `volume` source binds the volume's location (part 2), an image volume without a row gets no mount; snapshot selection (manual /
    scheduled = declared backup-on volumes, missing directory skipped; pre-update rule of §13.2); manifest keyed by volumeSlug
    and a pre-D manifest with `name` read; restore by volumeSlug; rollback plan fields; `siteVolumes` incl. orphans + host
    paths and the peer mirror.
  - **Mesh test** (`TestTwoSiteMesh` + e2e): two containers share ONE declared volume (a file written by one is read by the
    other); a `backup = false` volume is absent from the snapshot; a restore by volumeSlug works, also after the volume was
    moved to another container.
  - `backend`: write + read normalization (template PATCH / draft PUT / create), `verify-schema.ts` (every site config has
    `volumes`; no `data` sources; every volume source declared), `migrate-d.ts` dry run / apply / idempotent (reports per site
    the declared volumes and every readOnly reset; keeps template version / updatedAt; one `system.migration` audit entry).
  - `frontend` (mock Playwright): sidebar tree (template + deployment scope, validation badges, expanded state), per-section
    site routes + `/sites/{s}` and `?tab=` redirects, no top bar in inner scopes, account menu + theme toggle in the sidebar
    footer, 390 px drawer with its toggle in the breadcrumb row, leave guard; Volumes page (add / edit / backup toggle /
    delete blocked while used); container Volumes dropdown (shared with, + New data volume…, read-write by default, read-only
    badge for file / secret, Edit path / Reset, not-mounted warning). Prod checks `tests/prod/d-volumes.spec.ts` +
    `d-nav.spec.ts` on a copy of a template (deleted afterwards).
  - `agent/gui` (mock Playwright): Config › Volumes local edits (add / edit / delete-if-unused / backup), container volume
    dropdown, Backups volumes table (size, backup, used by, last snapshot, orphans, host paths "never backed up"), snapshot
    detail + rollback plan by volumeSlug, sidebar sections per site.
- Round E additions (edge nginx parity, required variables, volume kind + starter content):
  - `shared`: `edge-vectors.test.ts` (59 cases of `testdata/edge-vectors.json`: the client fixture edge — routing, regex 301 +
    captures, internal rewrite + re-match, auto-redirect, longest prefix, s3 response headers, 413 at 100 MiB + 1, ws allowed
    / refused, TLS files on 443 — and a rules edge — exact, plain prefix `/loginx`, ^~, `(?i)`, methods, keepPath, regex
    redirect captures, rewrite loop 500, rewrite no-match 404, proxy rewrite with "?", body limits, host filter, `always`
    headers, decoded path); `checkEdgeRegex`; `validateEdge` round E rules (V_EDGE_REGEX / HEADER / HOP / BODY,
    W_RESPONSE_HEADER_AS_REQUEST + `moveToResponseHeaders`, duplicates); `normalizeEdge` legacy shapes (idempotent);
    `nginx-import.test.ts` (§12.7); `required-vars-vectors.json` (template / draft / publish / agent cases); `checkSeed`,
    V_VOLUME_KIND (+ _IMAGE), `seedPathError`, diff notes.
  - Volume kind fix (§21 #83): `testdata/volume-kind-vectors.json` (infer 13 paths, 4 image-volume cases, 12 mount cases ×
    template / publish) run by `shared/src/volume-kind.test.ts` AND `agent/internal/validate/volumekind_test.go`; backend
    `test/15-volume-kind.test.ts` (register / patch / read kinds, V_ENUM, inspect prefill dir, draft API mismatch error + rename
    info, migrate-f dry run → apply → rerun 0 + verify-schema + audit); frontend `tests/volume-kind.spec.ts` (Folder / File
    control + Auto, register Auto, the user's case = info only, mismatch error + "+ New single-file volume…", warning → image
    page link → File → back, disabled container error); agent GUI kind badge + corrected inline flag
    (`tests/rounde-volumes.spec.ts`); TestEdgeParityDocker mounts the single-file volume on a File image volume row
    (`/data/flows.json`) through real Docker.
  - `agent`: `internal/edgeres` TestVectors (the same 59 cases), regex checker, variables; `internal/validate` round E edge
    rules, response-header-as-request, required-vars vectors (6/6), volume seed; `internal/model` legacy edge normalize +
    round trip, kind / seed round trip; TestLegacyEdgeRevisionHash; TestSeedRules, TestExtractSeedArchiveSafety;
    `internal/edge` TestBuildCaddyJSON (every feature's JSON + `caddy.Validate`), TestToPlaceholders, TestEdgeRouting,
    **TestClientEdgeParity** (real Caddy in process + httptest upstreams: SSE not cut while data flows / idle cut, websocket
    echo, 100 MB upload with bounded heap, 413 by Content-Length and chunked, Range 206, response headers incl. `always` on an
    upstream 404 and on the edge 503 page, regex 301, internal rewrite, longest prefix, TLS files + HTTP on one host, request
    headers); **TestEdgeParityDocker** (real agent + Docker: the fixture imported by `e2e/edge-parity/import.mjs`, one
    `opm-edge-testsrv` container per upstream, every client vector + the checks above; volume kinds, seeds (files with a
    rendered file, archive, single file), Node-RED-like rename over a single-file mount fails with EBUSY, seed change leaves
    data, snapshot of a single-file volume, reset to starter content).
  - `backend`: `13-round-e.test.ts` — new edge shapes saved as sent, legacy edges normalized on write and read, V_EDGE_REGEX
    422 + W_RESPONSE_HEADER_AS_REQUEST warning, `migrate-e` dry run / apply / idempotent / verify-schema 0, required-vars
    vectors through the API, seed-archive uploads + agent blob access, kind / seed defaults.
  - `frontend` / `agent/gui` (mock Playwright): the user's mistake (Cache-Control as a request header) → warning → **Move to
    response headers** → Test a URL shows it on the response; route page kinds, regex check, redirect / rewrite previews,
    path modes, presets, timeouts, header groups; importer (paste the fixture → mapping → bind certificate → Replace →
    effective order → traces); required-variable placeholder + info severity; volume drawer (kind, starter content,
    single-file warning), Volumes table, reset to starter content (agent GUI). Prod specs `tests/prod/e-*.spec.ts` on a copy.
- Round G additions (§21 #84, every credential viewable and regenerable any time):
  - `shared`: `REVEAL_SECONDS` 60 / `RevealResponse` on every credential response, `CREDENTIAL_OPS` / `AUDIT_DEPLOYMENT_OPS`
    / `AUDIT_OPS` / activity catalog entries, audit redaction masks leaves inside secret-named objects
    (`lastSetPassword.value`, `lastResetLink.value`) — `reveal.test.ts`, `access.test.ts`.
  - `backend`: each credential op (create / reveal / regenerate of access key, install command, secret, registry password,
    account password, reset link): value returned with `hideAfterSec` 60 and `Cache-Control: no-store`, activity + audit
    written without the value, permissions per §21 #84, 410 `gone` reasons (not-stored, used / revoked / expired, password
    changed by the person), install token nulled on enroll / revoke / expiry, `replaceActive` revokes the old tokens, the 31st
    value request in one minute → 429 `rate-limited` with `retryAfterSec`; agents still get only `{ hash, version }`;
    `migrate-g` dry run / apply / idempotent / verify-schema 0.
  - `agent`: `revealSecret` → `hideAfterSec` 60, no-store, 31st reveal per session per minute → 429.
  - `frontend` / `agent/gui`: RevealValue (masked → View → value + 60 s ring → masked; re-masks on route change and hidden
    tab; nothing in localStorage / sessionStorage); create deployment / account / install command show the value in
    RevealValue (no modal, no checkbox); **Bundle grep**: the test suites grep the built bundles for
    `/shown once|only once|one-time|I saved/i` and must find nothing.
- Round I additions (§21 #90, agent self-management in the GUIs):
  - `shared`: `compareAgentVersions` (core, build stamp, unparsable), `supportsGuiUpgrade` (0.7.2 / 0.9.x false, 0.10.0 and
    0.10.0-<stamp> true), `agentUpgradeAvailable`, `validateAgentSettings` (port range, agent-port clash, edge = alias clash,
    enum, types, rename info, no-default-edge warning), `uninstallCommands` (systemd / none), AGENT_SELF_API + CLOUD_API
    tables, activity catalog + AUDIT_OPS / AUDIT_DEPLOYMENT_OPS entries.
  - `agent` (Go): upgrade phases against an httptest cloud (download with Range resume, signature from agent.json vs cloud
    key, bad signature / sha256 / self-test → failed, old agent still running, `.new` removed); swap keeps `opm-agent.prev`;
    `pending.json` → `healthy` → done + `agent.upgraded`; the watcher restores `.prev` when `healthy` is missing at the
    deadline; the boot guard script (`sh` in a temp dir: past deadline + new sha → restored, healthy → untouched, other sha
    → untouched); the restored agent turns `rolled-back.json` into `agent.upgrade-failed` + `agent.rolled-back`; errors
    locked / conflict / stale / invalid; cloud request taken once per requestId (also after a restart), pin moved → failed
    without download; heartbeat `upgrade` sent on every phase; mesh run order (coordinator last), stop on first failure,
    skipped < 0.10.0; config view masking (`releasePublicKey` kept), PATCH validation + atomic write + `.bak`; support bundle
    sections without secrets; `upgrade-guard --install` idempotent (non-root → note, exit 0). Linux smoke test: a real
    systemd host upgrades X → Y via the GUI API, then a broken Y (exits at start) is rolled back by the watcher and, with the
    watcher killed, by the boot guard after a reboot / restart.
  - `backend`: requestAgentUpgrade checks (not enrolled, < 0.10.0 + hint, stale pin, already on it, conflict), audit +
    `_cloud` activities, agentEpoch +1, CloudView.agentUpgrades (expired dropped), heartbeat `upgrade` clears the request on
    done / failed / rolled-back, upgrade-all run sequencing (next site only after done, stop on failure, cancel), Setup /
    Status `upgrade` view, release notes from `deps/manifest.json`; install script calls `opm-agent upgrade-guard --install`
    (DRY_RUN prints it) and the container test upgrades a 0.7.x install by rerunning the install command.
  - `frontend` (mock Playwright): Setup installed / latest + badges, Upgrade agent typed slug → queued → upgrading phases →
    done; failed / rolled-back badge; 0.7.2 site: button disabled + rerun-install hint; Upgrade all typed UPGRADE + run panel
    + Cancel run; Status badge.
  - `agent/gui` (mock Playwright): Agent page overview, Check release card, Upgrade modal (typed slug) → stepper → restart
    overlay → new version on the same session; rolled-back result; stale pin → reload; Upgrade all modal + run panel; Restart
    agent; agent.json masked view + settings drawer validation + save; support bundle download; uninstall lines; CLI map;
    `*` = one card per site.
  - §21 #91 extension (install command = hard fallback):
    - `backend` 22-install-fallback: sh `ver_cmp` vs `compareAgentVersions` on 28×28 vectors (sh + dash), `schema_ok` vs
      `agentSchemaCompatible`, actions vs `agentInstallAction`; script `sh -n` / `dash -n` / shellcheck; used token →
      upgrade-only script with the schema; `?version=` embedding + "not uploaded" tiny script; installRelease (pin,
      version, 404, 422); schema headers; expired / revoked / used-elsewhere / unknown token → tiny scripts (exit 1 +
      text), binary / release / deps 410; upload-agent release.json → upload-deps manifest schema.
    - `backend` 23-install-fallback-e2e (ubuntu:24.04, fake TTY): Docker missing (no terminal → no + exact rerun command;
      TTY n; OPM_INSTALL_DOCKER=1); upgrade 0.7.2 → pin (TTY n = nothing changed; no terminal → OPM_YES hint; TTY y →
      signature, agent.json key added, backup + meta.env, .prev, `systemd-units --install --socket`, healthy, no token);
      SAME reinstall; unhealthy target → rollback + DB restored, exit 1; downgrade refused without a compatible backup,
      OPM_ALLOW_DOWNGRADE hint, restore declined / accepted (legacy unit, socket removed); 5 backups kept; the docker CLI only
      for version / info. Total `npm test`: 115 pass + 1 skip.
    - `agent`: `internal/sdsock` (inherited fd served twice), `internal/store` schema (backup + migrate + refuse a newer
      DB, prune), `internal/selfupgrade` units (§19.2 unit, socket ports, socket on / off), TestInstallActionAndSchema,
      `/_opm/api/health` 503 before startup; TestTwoSiteMesh: an agent restart keeps every container (same id +
      StartedAt). Linux smoke `agent/test/upgrade-smoke/run.sh both` (exec + systemd): `systemd-units --install` writes
      service + guard + socket (80 / 7080 / 8080), health `socketActivated: true`. Edge continuity during `systemctl
      restart opm-agent` (12 s hammer, keep-alive off): with the socket 0 / 2184 failed (:80, max 16 ms) and 0 / 2184
      (:7080); without it 4 / 2144 refused on each port.

- §21 #97 dev tools: shared `devtools.test.ts` (vectors `devtools-vectors.json`: split / classify SQL + CQL / classify Redis /
  parse redis-cli lines / redact statements + HTTP requests) and `devtools-validate.test.ts` (+ `devtools-validate-vectors.json`,
  diff + template sync); Go `internal/devtools` (same vectors, switch / auth, mask + reveal + rate limit, SQLite end to end incl.
  read-only + ATTACH + container-file, HTTP refusals + cookies + redirects, Redis refusals, error position shift, fetchable),
  `TestDockerDevTools` (real postgres:17, redis:7.4, eclipse-mosquitto:2, go-httpbin, cassandra:5.0.3: connect, introspect,
  multi-statement + params + explain, paging, cancel, read-only, destructive confirm, activities without secrets),
  `internal/agent` TestDevToolsRevisionHash, TestDevToolsThroughAgentAndMesh, TestDevToolsMQTTStreamThroughMesh,
  TestLocalEditDevTools, `cmd/opm-devmesh` TestDevmeshDevToolsPresetsEndToEnd (presets from a real revision over HTTP);
  backend `26-devtools-presets`; frontend `devtools-presets.spec.ts`; agent GUI `devtools.spec.ts`, `devtools-mobile.spec.ts`,
  `devtools-real.spec.ts` (real agent, manual + preset connections).
- §21 #95 / #96: shared `intent-wait.test.ts` (×1, min = wait / 4, heartbeat decay, piecewise, only 1 \| 2 \| 4, clock
  backwards, interpolated ready) + `volume-ops.test.ts`; Go `intentwait_test.go` (the same cases, the endpoints with a fake
  clock: cap, decay, ACCEPT refused before effective ≥ wait, restart survival, cancel, other session; mesh forward),
  `volops_test.go` (copy default / custom path + perms + symlinks, path safety incl. symlinked ancestors, copy into a volume
  with overwrite, empty with wait + typed slug + snapshot, restore from a copy and from an archive, unsafe archives, file
  volumes, locks (busy volume, update running, update while an action runs), rate limit, copy delete, interrupted op;
  Docker: stop order web → db and start order db → web around an empty, copy with containers stopped; mesh forward with
  both sites' activities), `tools/volumes_write_test.go` (edit / conflict / create / 1 MiB, root + symlink + escape,
  upload + overwrite, mkdir, move into itself, delete typed DELETE incl. a symlink, file volume, locks, activities); agent
  GUI Playwright `fastforward*.spec.ts`, `volumes*.spec.ts` (mock: overview, files, copy, restore, empty, upload restore,
  failure, `*`, mismatch helper, 390 px light / dark).

- §21 #100 transfers: shared `transfers.test.ts` (hostfs vectors, transferState, contract); Go `transfer_saf_test.go`
  (store-and-forward to the cloud with the cloud unreachable then reachable and nobody on the receiving end; a cloud file
  fetched after the agent comes online + fetchedAt; hostfs vectors; listing + send policy); backend `06-transfers` (fetched);
  agent GUI `transfers.spec.ts` + `transfers-mobile.spec.ts`; cloud `transfers-100.spec.ts`.

### 20.2 Hello-world end-to-end (the acceptance test)

Machine: one macOS host with Colima (Docker), Firebase project (emulators for the dev loop, deployed for the final run),
Google Artifact Registry repo `opm` in the same project. Two agents run on the Mac host (darwin build) as two sites with their
own data dirs and ports. Files: `e2e/`.

**Images** (built for the Colima architecture, e.g. linux/arm64; pushed to `<region>-docker.pkg.dev/<project>/opm/…`):
- `hello-api:1.0.0` (Go, static, `FROM scratch`): env `PORT` (8080), `REDIS_ADDR` (host:port; empty → file store),
  `DATA_DIR` (/data), `API_TOKEN`, `GREETING` (hello), `FAIL_HEALTH` ("1" → /healthz 500).
  `GET /count` → `{"count":N}`; `POST /count` → increments (redis `INCR hello:count`, else `DATA_DIR/count` file with fsync) →
  `{"count":N}`; `GET /info` → `{"greeting","tokenLength","store":"redis"|"file","hostname"}`; `GET /healthz` → 200 `ok`
  (500 when FAIL_HEALTH=1). The counter persists in redis's AOF volume (or in its own `/data` volume in file mode).
  Redis client: minimal RESP over TCP (no library needed).
- `hello-web:1.0.0` (Go, static): env `PORT` (8080), `THEME` (green), `API_BASE` (/api), `BANNER_FILE`. `GET /` → HTML
  `<body data-theme="<THEME>" style="background:<colour>">` with colours green `#16a34a`, yellow `#eab308`, blue `#2563eb`, red
  `#dc2626`, `<h1 id="title">Hello OPM</h1>`, `<p id="banner">` (BANNER_FILE content or ""), `<span id="count">` + a button;
  JS fetches `API_BASE + "/count"` and POSTs on click. `GET /config.json` → `{"theme","apiBase","banner"}`. `GET /healthz` → 200.
- `redis:7.4-alpine` — pulled from Docker Hub and pushed unchanged to the AR repo.
Registry in OPM: url `https://<region>-docker.pkg.dev`, username `_json_key`, password = service-account key JSON (reader).
Services: `hello-web` (repo `<project>/opm/hello-web`), `hello-api`, `redis`; images registered in copy mode with the
platform of the build; metadata = `shared/testdata/hello-catalog.json` (ports, env, volumes, health checks).

**Deployment config** = `shared/testdata/hello-config.json` (ids replaced by the real ones; the template holds the secret with
generate alnum 24 and value null; the deployment generates it):
- variables: `theme` (enum green/yellow/blue/red = green), `greeting`, `redis-host-port` (16379), `app-host` (127.0.0.1),
  `db-host` (`host.docker.internal`); secret `api-token`; file `banner` (text, `render: 'expressions'`, mounted into hello-web).
- site **db**: host `{{ var.db-host }}`, agentPort 7081, alias 8091, edge httpPort 8082 / https off / host `*` / no routes
  (built-in not-found page), site volume `redis-data` (label "Redis data", backup on), container `redis` (command
  `redis-server --appendonly yes`, image volume `data` → volume `redis-data`, read-write, port row `tcp` hostPort `{{ var.redis-host-port }}`, health exec `redis-cli ping`).
- site **app**: host `{{ var.app-host }}`, agentPort 7080, alias 8090, depends on db, notFoundPageId = the "Hello 404" page,
  edge httpPort 8081 / https off / host `*`, site volume `hello-api-data` (label "API data", backup on), routes: `api` `/api` → hello-api:http strip prefix (setHeaders `X-Opm-Site:
  {{ self.slug }}`), `old` `/old` → redirect 308 `/`, `web` `/` → hello-web:http (targets by containerSlug). Containers:
  hello-api (`REDIS_ADDR = {{ site.db.host }}:{{ site.db.port.redis.tcp }}` — cross-site expression with containerSlug
  `redis`, `API_TOKEN` secretRef, `GREETING {{ var.greeting }}`, image volume `data` → volume `hello-api-data`),
  hello-web (`THEME {{ var.theme }}`, `BANNER_FILE {{ file.banner.path }}`, extra mount `/etc/hello/banner.txt` ← file
  `banner` read-only (rendered once for the site, location auto = `<agentHome>/files/banner`), depends on hello-api).
Expected orders: sites db → app; containers hello-api → hello-web.

**Steps and checks** (script `e2e/run.sh`, results in `e2e/results/<ts>.md`):
1. Bootstrap admin (`create-admin`), log in (Firebase REST signInWithPassword for the script; the browser flow is checked
   with Playwright once). `GET /v1/me` → isAdmin.
2. Registry: create, **Test login** → ok.
3. Services + images: register 3 images (copy) → wait until `import.status = ready` (≤ 10 min) — layers in Storage.
4. Not-found page "Hello 404" (`<h1>Hello 404</h1>`).
5. Template "Hello world" (PATCH with the config; secret value null) → validation ok (warnings allowed).
6. Deployment from the template → access key returned (stored; viewable again with revealAccessKey, never in
   getDeployment); `draft.secrets[api-token].isSet = true`.
7. Setup: install tokens for app and db; start both agents (`agent.json`: apiBase, deploymentId, siteSlug, dataDir
   `e2e/run/<site>`, agentPort 7080/7081, guiAliasPort 8090/8091, advertiseHost 127.0.0.1, dockerHost = Colima socket) with the
   token files → both enrolled; cloud Status: both online within 60 s. Before the first proposal: `GET http://127.0.0.1:8090/_opm/`
   → login page; nothing runs (`docker ps --filter label=opm.deployment` empty).
8. Diff → publish "release 1" → both sites staging → ready (blobs: one site downloads each layer from the cloud, the other
   copies it from the peer when it has it — staging detail shows sources).
9. Agent GUI API on app (port 8090): login (wrong key once → 401 invalid-key + activity login.failed), actor
   `ops@example.com`, gate open, approve/start, wait `OPM_APPROVE_WAIT_SEC` (60 in the final run), approve with typed
   "ACCEPT" (a wrong word → invalid) → run: LOCK, SNAPSHOT, APPLY db (redis ready) then app (hello-api ready before hello-web
   starts), VERIFY, COMMIT → proposal applied; head seq 1 on both sites and in the cloud.
10. Checks: `GET http://127.0.0.1:8081/` → 200 with `data-theme="green"`; `/config.json` theme green and banner text;
    `POST /api/count` ×3 → 3; `GET /api/count` → 3; `GET /api/info` → greeting "Hello from OPM", store redis, tokenLength 24;
    `GET /nope` → 404 body contains "Hello 404"; `GET /old/x` → 308 Location `/`; `GET /_opm/nope` → agent 404;
    `GET http://127.0.0.1:8082/anything` (db edge) → 404 built-in page; `GET http://127.0.0.1:8081/_opm/app` → GUI; activities
    `update.applied` for app and db visible in the cloud within 60 s (pipe); cloud Diff → blocker V_NO_CHANGE only.
11. Manual snapshot S1 (note "count 3") on the app GUI → complete on both sites (db holds the redis volume).
12. **Release 2**: cloud Variables → theme = yellow → Diff shows `theme green → yellow` → publish → staged → approve (60 s +
    ACCEPT) → applied (seq 2). Checks: page `data-theme="yellow"`; ONLY hello-web was recreated (hello-api + redis container ids
    unchanged); count still 3; POST → 4.
13. **Local change** on the db GUI (8091): Variables → greeting "Hi from db" → Review → local proposal (origin agent) →
    approve on db (db is the coordinator) → applied seq 3 → `/api/info` greeting "Hi from db"; the cloud Diff shows head seq 3
    (origin agent) ≠ draft (greeting).
14. Copy head into draft → Diff has no changes (blocker V_NO_CHANGE only).
15. **Failure + automatic rollback**: draft hello-api env `FAIL_HEALTH = "1"` → publish → approve → APPLY on app fails
    (hello-api unhealthy, fail fast) → ROLLBACK on both sites → proposal failed-rolled-back with the message; page still yellow,
    count 4, head still seq 3; activities update.failed + update.rolled-back. Copy head into draft.
16. **Stale proposal**: draft theme = blue → publish P → staged, ready — do NOT approve.
17. **Rollback to S1** (Backups → Roll back, gate, 60 s, ACCEPT) → new revision seq 4 (kind rollback): page green, count 3
    (redis volume restored), greeting "Hello from OPM"; P becomes `stale` (cloud Diff banner). Copy head into draft.
18. Activities bundle: download `.opmlog` (app, include debug) from the agent GUI → upload in the cloud → inserted 0, skipped
    > 0 (already piped), rejected 0. A tampered copy (one summary changed) → rejected ≥ 1.
19. Snapshot download (`.opmsnap`) of S1 → import on the db GUI → 409 conflict (the id exists); delete S1 (all sites) → import
    again → listed with reason imported, complete on both sites.
20. Access key rotate in the cloud → after the next poll both sites have keyVersion 2: the old key → invalid-key, the new key
    logs in; a session opened before the rotation still works.
Pass = every check above green; results file lists timings and the activity counts per site.

---------------------------------------------------------------------------------------------------------------------

## 21. Decision log (gaps closed in this spec; later decisions win over SPEC-v2 / docs-v2)

1. Secrets are stored plain like variables (no KMS / Secret Manager / sealing); masked in every read; reveals and agent fetches
   are audited (`secret.revealed`, `secret.fetched`, `registry.password-revealed`).
2. Agent GUI auth = access-key login (argon2id hash from the cloud view, cookie `opm_session_<siteSlug>` 6 h, no sliding) + actor popup
   (actor token exp = session expiry). No deployment credential, no two-phase renewal. Rotation is "each site switches when it
   receives the new hash". Sessions are per site.
3. Edge = embedded Caddy in the agent (80/443 configurable), GUI on the edge `/_opm/`, alias port 8080, agentPort 7080 for mesh.
   Containers may not publish 80/443/edge ports. The "no edge / port-80 takeover / notFoundPage volume kind" ideas are dropped.
   (Amended by #86: the edge ALWAYS runs — the built-in default edge before a head — instead of a separate takeover listener.)
4. Edge upstreams + agent health checks use loopback publications `127.0.0.1:<ephemeral>` of every container port (the agent
   runs on the host and cannot resolve container DNS names; works on Linux and macOS/Colima).
5. Containers get `ExtraHosts host.docker.internal:host-gateway`; docker names include the site slug
   (`opm_<dep>_<site>_<serviceSlug>`, network `opm_<dep>_<site>`) so two sites can share one Docker host (e2e).
6. Mesh address of a peer = `advertiseHost` (agent.json / detected, reported at enroll + heartbeat) + its agentPort; it is
   independent of `SiteConfig.host` (which is for containers and users).
7. The poll answer IS the cloud view (full snapshot each time; cursor = `agentEpoch`); hold ≤ 25 s by default (proxy idle limits,
   Cloud Functions), max 50.
8. Revisions carry a `catalog` (service slugs, ImageSpecs, referenced not-found pages) + `catalogHash`, so revisions are
   self-contained (offline staging, rollbacks, image metadata edits never change running deployments).
9. Revision hash = sha256(JCS(config)); the cloud verifies reported heads.
10. One proposal at a time is relevant: any newer proposal (greater ULID) supersedes older open ones (cloud and local).
    Staging failure keeps status `staging` with the site `failed`; `restage` retries.
11. 60 s wait is enforced server-side (intent `notBefore`); `OPM_APPROVE_WAIT_SEC` lowers it for tests only.
12. Apply: site order = topological by site depends (removed sites first); per site containers in depends order, each waits
    for its dependencies to be completed; unhealthy = fail fast; VERIFY re-checks; any failure in SNAPSHOT/APPLY/VERIFY rolls
    every site back from the pre-update snapshot; coordinator restart mid-update → rollback.
13. Update steps are async over the mesh (POST step → 202, poll state every 2 s).
14. Uploads > 24 MiB use a chunked upload API through the backend (8 MiB parts, GCS compose) — no direct client access to
    Storage; downloads stream (200 + Range) or 302 to signed URLs in production; clients handle both.
15. Transfer download/delete paths are deployment-scoped (`/v1/deployments/{id}/transfers/{transferId}/…`).
16. Install token format `opi_` + base64url(32 bytes); doc id = sha256 hex of the token string.
17. Signed requests: body hash = lowercase hex WITHOUT the `sha256:` prefix; PATH starts at `/v1/` or `/_opm/peer/`; peer JSON
    responses are signed too. Nonce cache in Firestore `agentNonces` (cloud) and SQLite (agents). Clock offset from
    `X-OPM-Server-Time`.
18. Access: every enabled account can READ services, images, not-found pages and tags (catalog); managers also get
    deployments.read; first admin via `create-admin` script; `fullAdminRules()`.
19. Expressions: newlines allowed in text, never inside a block; `json` filter outputs canonical JSON; `int` is strict
    (regex) then truncates; number text uses ECMAScript formatting; positions are code points; arity errors are E_SYNTAX;
    `self.*` allowed in edge fields; `file.*.path` for unmounted files = E_FILE_NOT_MOUNTED.
20. Reserved site slugs: backups, files, internet, revisions, secrets, sites, updates, variables (agent GUI route collisions).
21. Validation has two levels (save / publish); drafts and templates can be saved with publish-level issues.
22. Image volumes are always mounted (implicit data volume) unless the container maps the name; container port protocol must
    equal the image port protocol; jobs cannot use restart always.
23. Not-found page HTML travels in the revision catalog; built-in pages defined in §12.3.
24. Cloud-origin activities live in a per-deployment `_cloud` log (hash chained like site logs).
25. New frontend routes needed by the container rule: `/app/services/{serviceSlug}/register-image`,
    `/app/templates/{templateId}/sites/{siteSlug}/containers/{serviceSlug}`,
    `/app/deployments/{deploymentId}/sites/{siteSlug}/containers/{serviceSlug}`, `/app/accounts/{uid}`, `/app/audit-log`; agent:
    `/_opm/app/{siteSlug}/config/containers/{serviceSlug}`.
26. Tunnel: one lease per deployment; no uploads through the tunnel.
27. Snapshot ids are shared by all sites (coordinator-generated); `.opmsnap` / `.opmlog` are ustar archives with a signed
    `manifest.json`; imports keep the snapshot id (duplicate → conflict).
28. Scheduled snapshots are started by the first reachable head-config site (slug order) at 02:00 local time.
29. Deployment delete is soft (`deleted: true`); agents stop polling and keep containers running.
30. The browser-tunnel, ACME / files TLS, gateway import mode, offline install bundle and install-from-peer are built after the
    hello-world e2e passes (in this order); the contracts above already cover them.
31. Agent session cookie = `opm_session_<siteSlug>` (browsers ignore ports; two agents on one host would overwrite one
    `opm_session`). The legacy `opm_session` is still set and accepted as a fallback during a transition; the site cookie wins.
32. ACME dns-01 providers compiled into the agent: cloudflare, digitalocean, duckdns, godaddy, route53, googleclouddns,
    namecheap (§12.4 table; secret = plain token or JSON object). Validated at staging (incl. `caddy.Validate`).
33. The scheduler (02:00), the 10-min coordinator timeout of abandonUpdate and the 30 s "fresh lock" guard of the automatic
    abandon / commit path use an injectable clock in the agent (tested with a fake clock).
34. Audit everything (user requirement 2026-10-07): one central router hook writes an audit entry for every human non-GET
    request outside `/v1/deployments/{id}/…`, success and failure (§6.2); 401s have no actor and are skipped; a signed-in
    person without an account (403) is logged. Deployment-internal writes are covered by `_cloud` activities instead, and every
    such op now writes one (new types `draft.updated`, `transfer.deleted`, `activities.uploaded`, `deployment.deleted`).
35. `createTransfer` writes `transfer.sent` with `data.stage = "started"`; the completion keeps writing `transfer.sent` with
    `stage = "uploaded"` (no new type; both are visible in Activities).
36. Audit diffs leave out `updatedAt`, `updatedBy`, `cloudActivitySeq` (the entry has its own ts / actor); list items are matched
    by identity keys for readable paths; values > 2 KB are truncated with a preview; ≤ 200 changes per entry.
37. Secret redaction is key-based plus SecretDef / `secret: true` context (§6.2) and is applied to both `changes` and `data`;
    env vars whose NAME matches the secret pattern (e.g. `API_TOKEN`) are masked too (safe side).
38. `patchAccount` with an unchanged access list is `account.updated`; failed requests keep the intended action
    (e.g. a 409 template patch is `template.updated` with outcome error) so filters by action show refusals too.
39. The audit-log list runs ONE equality filter in Firestore (+ ts range) and the rest in memory (like listActivities); old
    entries without the new fields are returned with defaults (outcome ok, changes []).
40. **One identifier per entity** (user rule, phase U). The identifier is the document id; there is no second id field.
    Registry `registryId` · Service `serviceSlug` (no `serviceId`, no `slug` field, no `serviceSlugs` index collection) · Image
    `version` within its service · NotFoundPage `notFoundPageId` · Tag `tagSlug` · Template `templateId` · Deployment
    `deploymentId` · Site `siteSlug` within its deployment · Account `uid` · Icon `iconId`. Containers, depends, edge proxy
    actions, ImageSpec, catalog keys, snapshots, blob refs, manifests, usage, logs and API paths use `serviceSlug` + `version`.
41. **Image = version + reference.** `version` (doc id, `^[a-z0-9][a-z0-9._-]{0,63}$`, e.g. v1) is the OPM version; the docker
    source is the full `reference` as entered (e.g. `docker.io/northwind/backend:3.0.9`) + its parsed
    `source` (`ImageRefParts`). "Tag" now means only the docker tag inside a reference. `registryId: Id | null` is auto-matched
    by host (`matchRegistries`; first by label; none → null = public, anonymous pull); the user may override. A service has no
    repository. `reference` / `registryId` can change only while the import failed (then retry-import).
42. **Icons.** `Icon = string | null`: null = entity default, `lucide:<name>` (curated set of 350, shared/src/icons.ts),
    `upload:<iconId>` (icons library `icons/{iconId}`, bytes inside the doc, ≤ 64 KiB, png / svg / webp, SVG sanitized,
    de-duplicated by sha256, public immutable content URL); bare names are legacy Lucide; https URLs stay valid for
    `Account.img` / `Deployment.image`. At publish, uploaded service icons are embedded in `Revision.catalog` as `data:image/…`
    URLs so the offline agent GUI shows them.
43. **ImageEnv choice.** `options: string[] | null` (1–50 unique plain strings) + `value` (must be one of them; V_OPTION at save;
    container values checked after resolution, V_OPTION at publish; replaces V_ENV_LIST). Legacy `value: string[]` is read by
    `normalizeImageEnv` (options = the list, default = the first item); writes always store `options`. The frontend env editor
    has a fourth kind **list** = a real list value stored as ONE comma-separated string (options null), not a choice.
44. **container.env + mountPath.** New field kind `mountPath` (roots var, deployment, self, container; must start with a
    literal "/"; resolved: absolute, not "/", no spaces / ".."; V_PATH). `container.env.<NAME>` = the final value of NAME in THIS
    container. `resolveContainer` resolves lazily in dependency order (DFS + memo + stack): env before the mounts that use it;
    `file.<slug>.path` = the resolved mount. A cycle → E_CYCLE at the reference that closes it, for every value on it; a value
    that uses a failed value gets the same code at its own reference position. Secret-valued env from imageEnv / mountPath →
    E_NOT_ALLOWED. `container.serviceId` removed (E_UNKNOWN_PATH). The user's case:
    `/app/helper/config/env/{{ container.env.NODE_ENV }}.ts`.
45. **Readable docker names on the sites.** After `docker load` the image is tagged `<serviceSlug>:<version>` (e.g.
    `northwind-backend:v1`) next to the integrity anchor `opm/cfg:<configHex>`; containers run from the readable name after an
    image-id check (re-tag when it was overwritten); the name is not hashed; `ContainerStatus.image` shows it (§15.1.1).
46. **Old-shape revisions are refused** by the agent before hashing (containers with `serviceId` / `tag`, or a catalog not keyed
    by `serviceSlug` / `<serviceSlug>:<version>`), with an error that says to publish again from the cloud. Nothing is
    migrated on the sites (no live sites existed).
47. **Docker Hub 401 = "not found or no pull access".** Docker Hub gives a token even for a repository the account cannot pull;
    the token is a JWT whose `access` claim lacks `pull`. The backend decodes the claim (test login with an optional
    repository, inspect, import) and answers "repository X not found, or account Y has no pull access to it (Docker Hub answers
    401 for both)" instead of a bare 401.
48. **Production migration.** `backend/scripts/migrate-v2.ts` (`--dry-run` / `--apply`, whole database, idempotent: new docs and
    rewrites first, deletes last; revisions get hash + catalogHash recomputed; `serviceSlugs/*` deleted; one `system.migration`
    audit entry per apply with the counts and the id → slug / tag → version mapping) + `backend/scripts/verify-schema.ts` (reads
    every document, must report 0 violations). The user's service `4WczlsAAaexWRXrzPvq4` became `services/northwind-backend`, its
    image became version `v1` with reference `docker.io/northwind/backend:3.0.9` (the old repository
    `northwind/backend` was wrong) and NODE_ENV / LOG_IN_CONSOLE became choice envs. No other production entity was changed by
    hand.
49. **Overlays portal to the body.** Every overlay (modal, drawer, popover, listbox, menu, picker, tooltip, toast) renders at
    `document.body` with one z-index scale (§17.0), so sticky headers, transforms and `overflow: hidden` can never clip or
    cover it.
50. **Pickers instead of free text** (user feedback "why does it ask for repository"): every field that refers to an existing
    thing is a picker; free text only for labels, descriptions, paths, hostnames, HTML / code and values (§17.0;
    field → control table in `frontend/TEAM.md`). The register-image form browses registry → repository → tag.
51. **Round C — optional site host (auto-detect).** User: "don't make Host name for each site mandatory, we will not know this
    ahead of time". `SiteConfig.host: Value | null`; **null = auto** is the default (`defaultSiteConfig()`; `normalizeSiteConfig`
    turns `""` into null). Agents report `detectedAddresses` (non-loopback IPs, IPv4 first, then the hostname) next to
    `advertiseHost` at enroll and in every heartbeat (`AgentDoc.detectedAddresses`, `[]` for old agents). The auto address of a
    site = its `advertiseHost` (agent.json `advertiseHost`, else the first detected IPv4). Resolution (`resolveSiteHosts`):
    configured host → that value; null → the detected address (`ValidationContext.detectedHosts`; cloud: AgentDoc.advertiseHost
    of enrolled sites; agent: its own advertiseHost + `PeerInfo.advertiseHost` of the peers) → else in the CLOUD the placeholder
    `<auto:siteSlug>` (`autoHostPlaceholder`, shown as "auto · detected on install"; never sent to a site) and on a SITE null →
    `site.<s>.host` / `self.host` give E_NULL ("site X has host auto and has not reported its address yet") at staging. A host
    EXPRESSION that resolves to null or "" (e.g. `{{ var.server-ip }}` with no value) does **not** block: it falls back to the
    auto address and the cloud shows the warning **V_HOST_AUTO** "var server-ip has no value — leave host on auto or set the
    variable" (warning in every mode, also publish / agent). Other host expression errors keep their rules. The agent mesh still
    uses advertiseHost (unchanged). Auto addresses are not part of the revision (hash unchanged); an address change alone does
    not recreate containers (it is used at the next update). GUIs: Host is optional with an "Auto-detect (recommended)" toggle,
    on by default; off shows the host input (IP, DNS or `{{ var.… }}`).
52. **Round C — containers are instances, not one per service.** User: "i can reuse the same img multiple times as diff env set
    for diff configurations; in container form you should ask for the container name, which port to map to, which volume …".
    `Container = { containerSlug, serviceSlug, version, enabled, env, ports, volumes, command, entrypoint, restart, resources,
    healthcheck, depends }` (shared/src/types.ts). **containerSlug is the only identifier** of a container: slug rules, unique
    per site, not `new` (V_RESERVED, route …/containers/new), default = the service slug, then `<slug>-2`, `-3` …
    (`suggestContainerSlug`). It is the DNS name (network alias) on the site network, `container.slug`, the key of
    `site.<site>.port.<containerSlug>.<portName>`, the edge target (`EdgeProxyAction.containerSlug`), `depends[].containerSlug`,
    the docker name (= the containerSlug; `opm_<deploymentId>_<siteSlug>_<containerSlug>` with containerNames prefixed, §21 #88) (labels `opm.container` = containerSlug, `opm.service` =
    serviceSlug), the uptime subject, `ContainerStatus.containerSlug`, the agent logs path
    `/_opm/api/sites/{siteSlug}/containers/{containerSlug}/logs`, `UpdateSiteState.containers[].containerSlug`, `UsageItem.containerSlug`.
    The image tag stays `<serviceSlug>:<version>` and is shared by every instance of that version. The same service + version
    may run any number of times per site with different env / ports / volumes. Container depends name containers of the same
    site; site-level ordering stays in `SiteConfig.depends` (the editor shows them in the Runtime section with a link).
    - **ports**: one row per image port `{ portName, hostPort: number | {{ }} | null (not published), hostIp: '0.0.0.0' |
      '127.0.0.1', edge: boolean }`; a missing row = not published, edge true (`containerPorts`); protocol = the image port's;
      listener conflicts ignore hostIp (0.0.0.0 covers every address); a proxy route to a port with `edge: false` → V_EDGE_ROUTE.
    - **volumes**: rows `{ imageVolume: name | null, mountPath: string | null, source, readOnly }`. One row per image volume
      (`imageVolume` set; `mountPath` null = the image mountPath, a value overrides it); a missing row = data volume
      `<containerSlug>-<volume>` (`defaultDataVolumeName`, `containerMounts`). Extra mounts: `imageVolume` null, `mountPath`
      required (may hold `{{ }}`, field mountPath), source not `none`. Sources: `{kind:'data', name}` (site data volume
      `<dataDir>/vol/<name>/`, RE.dataVolume `^[a-z0-9][a-z0-9_.-]{0,99}$`; two mounts with the same name SHARE it),
      `{kind:'host', path}` (bind of an absolute literal host path, isSafeMountPath, no `{{ }}`), `{kind:'file', fileSlug}`,
      `{kind:'secret', secretSlug}` (file + secret are always read-only), `{kind:'none'}` (image volume not mounted: container
      storage, lost on recreate). V_VOLUME covers the source rules. Snapshots archive data volumes by name:
      `sites/<siteSlug>/volumes/<name>.tar.zst`, `SnapshotVolume { name, containers, … }`.
    - **env**: unchanged (overrides of the image env + extra names; choice → dropdown; expressions; secretRef).
    - **command / entrypoint** overrides (null = image), **restart** picker, **resources** cpus / memoryMb (null = no limit),
      **healthcheck** override (null = the image health check).
    - "Duplicate container" (`duplicateContainer`) copies a container under a new name: host ports cleared, default data
      volumes renamed to the new slug, shared / host / file / secret sources kept.
    - Old one-per-service documents (`port`, `volume`, no containerSlug, `depends[].serviceSlug`, proxy `serviceSlug`) are
      converted by `normalizeContainer` / `normalizeSiteConfig` (containerSlug = serviceSlug; legacy data volume `<name>` →
      row source data `<serviceSlug>-<name>`; file / secret → read-only extra mounts). Agents refuse old-shape revisions (as #46)
      with "publish again from the cloud"; on first apply an agent moves a legacy `<dataDir>/volumes/<serviceSlug>/<name>/` to
      `<dataDir>/vol/<serviceSlug>-<name>/` when the new directory does not exist.
    - Local edits on a site (agent GUI) may change everything of an existing container except containerSlug, serviceSlug and
      version, and cannot add / remove / duplicate containers (V_LOCAL_CONTAINERS, V_LOCAL_TAG).
53. **Round C — container editor is a full page.** "Add container" opens `/app/templates/{templateId}/sites/{siteSlug}/containers/new`
    (deployment draft: `/app/deployments/{deploymentId}/sites/{siteSlug}/containers/new`; `?duplicate=<containerSlug>` prefills a
    copy); the same page edits `…/containers/{containerSlug}`. Sections: 1 Service + version pickers + Name (containerSlug,
    auto-suggested, editable only while new) · 2 Ports (a row per image port: published toggle, host port stepper or
    expression, bind address 0.0.0.0 / 127.0.0.1, "edge may proxy" toggle) · 3 Volumes (a row per image volume with a source
    picker data / host path / file / secret / not mounted; data offers the existing data volume names of the site to share;
    "+ Extra mount") · 4 Env (image env list with defaults shown; override input; choice → dropdown; secret → secret picker;
    expressions with autocomplete) · 5 Runtime (command / entrypoint override, restart picker, depends = containers of this
    site + the site depends shown, cpu / memory limits, health check override) · 6 summary sidebar (name, image, published
    ports, mounts, live validation issues of this container). Row actions on the site page: Edit · Duplicate · Remove.
54. **Round C — expressions in files.** `FileDef.render: 'expressions' | 'none'` (default: text → expressions, binary → none;
    binary + expressions → V_ENUM; legacy docs without the field read as the default via `normalizeFileDef`). A file with
    `render: 'expressions'` is a template resolved **per mounting container** at staging on the site (field `file`: roots var,
    secret, file, site, self, deployment, container incl. `container.env.*` = that container's final env). Text outside
    `{{ }}` may span lines; a block may not. Literal `{{` is written `{{ "{{" }}` (the existing string-literal grammar; no new
    syntax). Secrets ARE allowed: the rendered bytes are written mode 0400 under the agent data dir
    (`<dataDir>/rendered/<containerSlug>/<fileSlug>`, dir 0700) and mounted read-only. Rendered content never appears in
    .opmlog, the debug log, activities or snapshot metadata; snapshots keep the source config + content blobs (re-rendered on
    restore). The spec hash of a container includes the sha256 of each rendered file it mounts. Staging (and APPLY) errors:
    **V_FILE_RENDER** `"<fileSlug>:<line>:<col>: <code> <message> (container X on site Y)"`; a mime `application/json` or
    `+json` file whose rendered output does not parse → **V_FILE_JSON** (staging error). Shared helpers: `checkFileContent`
    (editor live check with line:col), `renderContainerFile`, `ValidationContext.fileContents`, `exprLineCol`. The TLS
    `certFileSlug` of edge files mode is never rendered. Cloud file editor: highlights `{{ }}` blocks, autocompletes paths,
    validates live, toggle "Resolve {{ }} expressions", and "Preview as container…" (site + container picker) shows the rendered
    result with secrets masked (`•••`). Migration: text files get `expressions` unless their content contains `{{` that fails
    `checkFileContent` (then `none`, reported by the dry run).
55. **Round C — blob download hardening.** The Storage bucket CORS is committed as `backend/storage.cors.json` (GET/HEAD from
    https://opm.example.com, https://opm.example.com, http://localhost:5173, http://127.0.0.1:5173) and
    applied by the backend deploy script (`gcloud storage buckets update gs://<bucket> --cors-file=storage.cors.json`).
    `GET /v1/blobs/{sha256}` for humans streams bodies ≤ 8 MiB (`BLOB_STREAM_MAX`) with 200 (Content-Type
    application/octet-stream, ETag `"<sha256>"`, `Cache-Control: private, max-age=300`); larger → 302 to a 5-min signed URL.
    The frontend shows the real error text ("download failed: 403 …", network / CORS) instead of "Failed to fetch".
56. **Round C — production migration** `backend/scripts/migrate-c.ts` (`--dry-run` / `--apply`, idempotent): every template,
    deployment draft and open proposal / stored revision config: containers → instance shape (containerSlug = serviceSlug,
    ports and volumes mapped from the old `port` / `volume` with the image volumes made explicit), proxy routes
    `serviceSlug` → `containerSlug`, container depends → containerSlug, empty host → null, files get `render`. Stored revisions
    are rewritten only when their config changes, with `hash` recomputed (no live sites). One `system.migration` audit entry per
    apply; `verify-schema.ts` must report 0 violations afterwards.
57. **Round C — team decisions.** Backend: `invalid` stays 422 (binary + expressions → 422 V_ENUM); template PATCH / draft PUT
    normalize legacy shapes on write and templates / drafts / snapshots on read (revisions are returned as stored — agents
    verify the hash; human revision views use a normalized copy); a heartbeat without `detectedAddresses` keeps the stored
    list and never bumps agentEpoch; `SiteCloudStatus` + `SetupResponse.sites[].agent` carry `advertiseHost` +
    `detectedAddresses`; getBlob answers 304 on `If-None-Match`; storage.cors.json also exposes Range / Accept-Ranges /
    Content-Range (signed-URL ranged downloads > 8 MiB); migrate-c keeps template `version` / `updatedAt` (a migration is not an
    edit). Go agent: `FileDef.render` is optional in Go so revisions written before round C keep their hash (the cloud always
    writes it now); rendering happens in the driver build and render errors never contain content; staging validates twice
    (before and after file blobs arrive); the auto address of a site is its agent.json `advertiseHost` (detectedAddresses are
    only reported); label `opm.slug` dropped (pre-round-C containers are found by `opm.service` and recreated on first apply);
    rollback plan from / to are `<service>:<version>`; `update.failed` carries `containerSlug` when one container failed.
    Cloud frontend: a new container joins the site only on Save (the whole site is saved; refused → its edit page with the
    unsaved container); old `…/containers/<serviceSlug>` links redirect when exactly one container uses that service; the
    file preview treats template secrets as set (`•••`). Agent GUI: port / volume rows are written only when the user changes
    them (an untouched container gives no diff).
58. **Round D — data volumes are declared on the site.** User: "the volume section is very complex, what is this dropdown
    that has 'cassandra-data'? if it's data then in site I should declare all the data folders, and then select a data volume
    from that, so it's easier on the agent code to know what exactly to back up." `SiteConfig.volumes: SiteVolume[]`,
    `SiteVolume = { volumeSlug (the ONLY id, unique per site, ^[a-z0-9][a-z0-9_.-]{0,99}$, never renamed), label (0–80),
    description (0–2000), backup (default true), location (#65) }`, ≤ 100 per site. On the agent a volume lives at its
    location, auto = `<agentHome>/volumes/<volumeSlug>/` (#65 supersedes the first round-D choice `vol/<volumeSlug>/`; such
    dirs are moved on first apply). Undeclaring a volume never deletes its directory (orphan, shown on the Backups page).
59. **Round D — mount sources.** `VolumeSource = volume {volumeSlug} | host {path} | file {fileSlug} | secret {secretSlug} |
    none`; the implicit per-container `data` kind is removed (V_ENUM with a hint). readOnly defaults to false for volume and
    host; file and secret must be read-only (V_READONLY). Several containers / mounts may share one volume ("shared with X").
    An image volume without a row is NOT mounted (warning V_VOLUME_NOT_MOUNTED); an explicit `none` row = "Not mounted"
    chosen by the user (no warning). `defaultContainer()` returns `volumes: []` — the user picks a source per image volume.
    Undeclared volume → V_VOLUME_UNDECLARED (error); declared but unused → V_VOLUME_UNUSED (warning). Diff section `volumes`
    (`<site>/volumes/<volumeSlug>`). The round-C editor showed / stored READ-ONLY ON for data volumes (bug): fixed.
60. **Round D — legacy normalization + migration.** A site config without `volumes` is pre-D: missing image-volume rows become
    explicit `volume <container>-<imageVolume>` rows (image known), every `data {name}` becomes `volume {volumeSlug: name}`
    with readOnly RESET to false (a deliberate read-only cannot be told apart from the round-C bug), and every such volume
    is declared `{volumeSlug, label: volumeSlug, description: "", backup: true}` (`normalizeSiteConfig`, idempotent; a
    current config is never auto-declared). Prod migration `backend/scripts/migrate-d.ts` (dry run → apply → verify-schema
    0 violations → dry run 0 changes; one `system.migration` audit entry; template version / updatedAt kept).
61. **Round D — backups by volumeSlug.** Manual + scheduled snapshots archive exactly the declared volumes with backup = true
    (a missing directory is skipped). Pre-update snapshots: the backup = true volumes of the head mounted by containers the
    update recreates or removes, plus head volumes (backup = true) the new config mounts in a created / recreated container.
    Host paths, none, file, secret, rendered files and backup = false volumes are never archived (the GUIs say so).
    `SnapshotVolume.volumeSlug` (legacy `name` read as volumeSlug); restore / rollback by volumeSlug into
    the volume's location wherever it is mounted now — moving a volume to another container keeps its data.
    `RollbackPlanResponse.volumesRestored {siteSlug, volumeSlug, sizeBytes}`, `volumesKept {siteSlug, volumeSlug, backup}`.
    New agent endpoint `GET /_opm/api/sites/{siteSlug}/volumes` → `SiteVolumesResponse` (size, backup, users, last
    snapshot, orphans, host paths) for the Backups page.
62. **Round D — local edits of site volumes are allowed** on a site (agent GUI → local proposal): add, edit label /
    description / backup, remove an unused volume (removing a used one fails with V_VOLUME_UNDECLARED). Containers still
    cannot be added / removed / renamed locally; mount sources may change.
63. **Round D — sidebar navigation, no second tab row, no top bar in inner scopes.** User: "instead of 2 nav bar, make the
    inner scope (site) into side bar" and "remove the top bar if we are in inner scopes". Template scope: the left sidebar
    becomes "← All templates", template name, Overview, Variables, Secrets, Files (counts) and a Sites tree (site →
    Containers (count; container children), Edge, Volumes, Settings; "+ Add site"; validation badges; expanded state per
    template in localStorage). Deployment scope: the same Sites tree inside its sidebar. Each site section is its own route
    `…/sites/{site}/containers | edge | volumes | settings`; `/sites/{site}` and old `?tab=` links redirect. Main area:
    breadcrumb + page header with the page actions, no tab rows. Inner scopes hide the global top bar; the account menu
    + theme toggle live in the sidebar footer. At 390 px the sidebar is a drawer whose toggle sits in the breadcrumb row.
64. **Round D — team decisions.** Backend: normalize on read needs the image volumes (a missing row is no longer a data
    volume), so the functions keep a 15 s image-volume cache primed before every handler; V_READONLY never reaches the API
    (the normalizer forces file / secret mounts read-only) — only un-normalized checks (GUI live issues, verify-schema)
    report it; migrate-c now also yields the D shape. Agent: siteVolumes is per site only (`*` → 404); for manual /
    scheduled snapshots the containers mounting a volume are stopped around that volume's archive.
65. **Round D — one copy per site (files, secrets, volumes) + storage locations.** User: "why does it ask me for path when
    i select the file, that should be mentioned in files. i can reuse the same file in multiple containers, that doesn't
    mean you need to store it multiple times in the VM! in the file you can ask or auto decide where the files will be. if
    user types in host ./ then it means the home path for agent." **agentHome** = the agent data dir (`--data-dir` /
    OPM_DATA_DIR, default /var/lib/opm). `StorageLocation = {kind:'auto'} | {kind:'path', path}` on `FileDef.location`
    (auto = `<agentHome>/files/<fileSlug>`) and `SiteVolume.location` (auto = `<agentHome>/volumes/<volumeSlug>` — this
    supersedes `vol/<volumeSlug>` of #58; the agent moves an existing `vol/<slug>` there on first apply). "./…" = relative
    to agentHome (no "", ".", ".." segments, no spaces, not a reserved agent dir except its own kind dir); an absolute path
    is used as is. Per site the file locations + that site's volume locations are unique and never nested (V_LOCATION).
    Every container that mounts a file / secret bind-mounts the SINGLE host copy read-only: files at their location
    (mode 0400 when the rendered content uses secrets, else 0444), secrets at `<agentHome>/secrets/<secretSlug>` (0400).
    File blobs move to `blobs/files/<hex>`. Snapshots back up each backup = true volume wherever its location is.
    A site writes copies only of the files / secrets mounted by its own enabled containers (+ edge TLS files). The round-C
    per-container dirs `rendered/<containerSlug>/` and `secrets/<containerSlug>/` are removed on the first apply after the
    new copies exist. agentHome order: `--data-dir` > OPM_DATA_DIR > agent.json `dataDir` > /var/lib/opm. FileDef.location is
    cloud only (V_LOCAL_DEFINITION); a site volume's location may change in a local edit (volumes are locally editable).
66. **Round D — files render once per site.** Field `file` roots: var, secret, site, self, deployment. `container.*` and
    `file.*` → E_NOT_ALLOWED "files are shared by all containers on the site — use a variable instead" (`file.<slug>.path`
    is a per-container mount path, so it is not allowed either — lead decision). `renderSiteFile` + `siteFileExprContext`
    replace renderContainerFile; V_FILE_RENDER `<fileSlug>:<line>:<col>: … (site <s>)`. The agent renders at staging into
    a temp next to the location and APPLY swaps it atomically. The cloud editor previews "for site…". Container page:
    the column is "Inside the container"; file / secret rows show "mounted read-only at <path>" + "Edit path" and
    "shared copy · also used by X, Y"; Files page column "On the server"; summary "Mounts: <path> ← file <slug> (server:
    <location>)". Migration: files + volumes get location auto; a file using container.* / file.* is reported (it becomes
    a validation error on the template, never a data rewrite).
67. **Round D part 2 — agent decisions.** A file / secret copy is staged at staging (`<location>.opm-staged-<revId>`) and
    renamed into place at APPLY; identical content is not rewritten. The spec hash includes the sha256 of a mounted
    secret's value, so rotating a secret recreates the containers that mount it as a file (a renamed file is a new inode a
    running container would not see). A changed volume location moves the data from the old head location (rename; if it
    fails, e.g. across file systems, APPLY fails "move it by hand" — never a silent copy). Edge TLS files mode still reads
    the certificate from its blob. A missing render / location counts as its default in diffs (old revisions keep their
    hash). Every container mounting a file or secret is recreated once on its first apply with agent 0.4.1 (new source
    paths). Test host ports are picked in 20000–29999 (outside the ephemeral range).
68. **Round E — the edge matches like nginx** (user: "this is a valid nginx file that i run on client so make sure your edge
    supports all this … we use http sse connections we also host a small s3 like … and yes we can let websockets also";
    fixture `e2e/fixtures/client-nginx.conf`). `EdgeRoute.match = {kind: prefix | exact | regex, path, stopRegex, methods}`.
    Order: exact (= path) → longest PLAIN STRING prefix (nginx: "/api" also matches "/apix"; the old segment rule is gone) →
    if that prefix has `stopRegex` (nginx `^~`) it wins → regex routes by (order, routeId), first match wins → the longest
    prefix → not-found 404. Host and method filters first (a route whose methods exclude the request method is invisible).
    nginx auto-redirect kept: no prefix equals the path and a PROXY prefix route equals path + "/" → 301 path/ (+query).
    Matching uses the decoded path, no slash merging. `order` only orders regex routes; GUIs show the effective order
    (`effectiveRouteOrder`: exact, ^~ prefixes, regexes, prefixes by length) with a "why" text. Legacy `{pathPrefix}` = prefix.
69. **Round E — regex dialect** = what RE2 (agent, Go regexp) and JavaScript (GUIs) both understand with the same meaning;
    a leading `(?i)` = case-insensitive (nginx `~*`). `checkEdgeRegex` (TS) and its Go port refuse lookaround, backrefs,
    atomic / possessive, `(?P<…>)`, other inline flags, `\A \z \Q \p \u \x{} \c \0` and POSIX classes (V_EDGE_REGEX).
70. **Round E — rewrites.** `action.type 'rewrite' {regex, replacement}` = nginx `rewrite … last` (internal, then the routes
    are matched again; no match of its regex → 404; > 10 rewrites per request → 500). Proxy routes get optional
    `rewrite {regex, replacement}` = nginx `rewrite … break` (exclusive with stripPrefix; no match → path unchanged). In
    both, a "?" in the result REPLACES the query — deviation from nginx, which appends the original args unless the
    replacement ends with "?" (with `$request_uri` nginx would duplicate them; the client fixture relies on this).
71. **Round E — variables** in redirect `to`, rewrite replacements, request and response header values: `$uri $args
    $query_string $is_args $request_uri $host $scheme $remote_addr $server_port $http_<name> $1…$9 ${name}` plus the old
    `{path} {query} {host}`; an unknown `$name` stays literal (no escaping needed for other "$"). Redirect status 303 added;
    `keepPath` only for prefix routes.
72. **Round E — response headers** (`EdgeResponseHeaders {set, add, remove, always}` on EdgeHost and EdgeRoute): upstream →
    remove (nginx proxy_hide_header) → set (hide + add_header) → add (add_header, appends). set/add only on 200 201 204 206
    301 302 303 304 307 308 unless listed in `always` (nginx `always`: every status incl. the edge's own error pages).
    Inheritance = nginx: route `responseHeaders: null` inherits the host's list; an object REPLACES it entirely (even empty).
    Rewrite routes carry none (the final route's apply). /_opm and the 308 https redirect are not affected.
73. **Round E — body, buffering, timeouts.** `Edge.maxBodyBytes` (null = unlimited) and proxy `maxBodyBytes` (null =
    inherit, 0 = unlimited) → 413 before the upstream is contacted (Content-Length) / when a chunked body passes it.
    `requestBuffering` (default false = stream; true = ≤ 8 MiB in memory, the rest streams — OPM never uses temp files, unlike
    nginx), `responseBuffering` (default false = flush every write; text/event-stream always flushes). `timeoutSec` is split
    into `connectTimeoutSec` (1–3600), `sendTimeoutSec` and `readTimeoutSec` (1–86400; default 60 each) with nginx
    semantics: read = time to the response header AND idle time between two reads, send = idle between two writes — a
    streaming SSE response or websocket is never cut while data flows. Legacy timeoutSec → readTimeoutSec.
74. **Round E — request headers + websockets.** Host is preserved; X-Real-IP, X-Forwarded-For (= client IP: Caddy trusts no
    proxies, so it does not append to a client-sent list like nginx `$proxy_add_x_forwarded_for`), X-Forwarded-Proto and
    X-Forwarded-Host are set by default; setHeaders override (case-insensitive), removeHeaders delete. Hop-by-hop names in
    setHeaders are ignored (warning V_EDGE_HOP): Caddy forwards websocket upgrades itself. New routes default to
    websocket true; stored routes keep their value. Upstreams: HTTP/1.1 with keep-alive.
75. **Round E — W_RESPONSE_HEADER_AS_REQUEST** (real user mistake: Cache-Control "public, max-age=3600" put into a proxy
    route's request headers to mimic nginx add_header): warning when a request header only makes sense on responses
    (`isResponseOnlyHeader`); one-click fix `moveToResponseHeaders` (route list starts from a copy of the host's). Both
    GUIs label the groups "Request headers → container" / "Response headers → browser" with the nginx equivalents, and
    Test a URL shows the response headers that reach the browser. Data is never auto-moved.
76. **Round E — nginx import** (`shared/src/nginx-import.ts`, both GUIs "Import from nginx.conf…"): parse → mapping table
    (ok / note / default / ignored / unsupported / error per directive) → bind ssl_certificate(_key) paths to an OPM file +
    secret → Append or Replace (`applyNginxImport`). nginx inheritance is evaluated by the importer, so imported routes
    behave as in nginx (array directives replace outer lists; nginx defaults such as client_max_body_size 1m and
    proxy_buffering on are made explicit). `proxy_pass http://<name>:<port>` → container `<name>` + its edge port numbered
    `<port>`; unresolved → empty target (V_REQUIRED). `rewrite ^/x/foo(/.*)$ $1 break` under `location /x/foo/` →
    stripPrefix; other rewrites → proxy rewrite / rewrite route. add_header never becomes a request header.
77. **Round E — migration** (`backend/scripts/migrate-e.ts`): schema renames only (match kind prefix + path, timeoutSec →
    readTimeoutSec, new fields at their defaults, websocket kept); route ids, paths, order and headers are untouched.
78. **Round E — required variables are set at deployment** (user: "i already told this is a template and the variable value
    will be filled in deployment time … i also set it as required … there will never be a null value!"). Outside agent mode
    a REQUIRED variable / secret without a value evaluates as `DEPLOY_PLACEHOLDER` ("‹set at deployment›"); results that
    contain it are `deferred` (previews show the token), errors it causes are `E_DEFERRED`, which is never reported, and
    value checks (V_HOST, V_PATH, V_OPTION, V_FILE_JSON, numbers) skip deferred values. Template: no issue at all for them
    (no V_VAR_REQUIRED, no V_HOST_AUTO, no E_NULL); an OPTIONAL variable without value gives ONE `info` V_VAR_OPTIONAL_NULL
    on the variable (new severity `info`: never counted as a warning or blocker). Draft: V_VAR_REQUIRED / V_SECRET_REQUIRED
    once on the variable ("… is required — set it before publishing"), a publish blocker (severity warning in draft so a
    draft can still be saved; error at publish); usages are not repeated. V_HOST_AUTO only in draft / publish, for optional
    variables. Agent staging stays strict (nothing is null there: publish is blocked). Vectors: required-vars-vectors.json.
79. **Round E — volume kind + starter content** (user: "make it so that i can write up the starter volume … useful for my
    nodered where the flow is prefilled but then in future on site we can change the flow … one single file as a volume").
    `SiteVolume.kind` dir | file (a file volume = one persistent read-write file, mounted at a file path; the agent creates
    the file before the bind mount) and `SiteVolume.seed` (null | files {path ← fileSlug} | file | archive .tar.gz ≤ 200 MiB
    via upload purpose `seed-archive`). Seeds apply at APPLY, before containers start, ONLY to a new or empty volume, then
    the volume is marked seeded (`.opm-seeded` + agent DB) and never seeded again; later seed changes never touch data (the
    review says so: DiffChange.note). Seed files render once per site like mounted files; archives never render and are
    extracted safely (regular files + dirs only). The agent GUI "Reset to starter content…" = intent (60 s) + ACCEPT →
    snapshot of that volume first → wipe → seed (activities volume.reset-to-seed, volume.seeded). A kind change of a volume
    with data fails staging. Single-file volumes cannot be replaced by rename (Node-RED flows.json): the GUIs warn and
    suggest a folder volume seeded with the files. Migration: existing volumes → kind dir, seed null.
80. **Round E — importer "Upstreams" step** (prod evidence: the user's containers are `northwind-backend-terminal` …, the
    config says `ez-terminal:8080`, so 18 routes imported without a target). `importNginx(text, {containers, upstreams})`:
    `upstreams` maps "<name>:<port>" → container + port and wins over the same-name match; the result lists every upstream
    (`NginxUpstreamRef`: routes, resolved, how name / mapped, `suggestion`). `suggestUpstream`: same name, or equal without
    "-" (nodered ~ node-red), or a single container ending in "-<name without ez-/northwind->" (ez-terminal ~
    northwind-backend-terminal); a fuzzy match needs an edge port with the same number. Suggestions only PREFILL the step
    (marked "suggested", changeable); on prod "Single" 9 of 12 upstreams were suggested (18 → 3 error rows).
81. **Round E — GUI pages with a query param.** The route editor (`…/edge/route?id=<routeId>` / `?new=proxy|redirect|rewrite`)
    and the importer (`…/edge/import`) are static pages in both GUIs (route editor > 12 fields → page); the route id is a
    query param (exception to §17.0 "path params"), so links survive a route rename. Volume kind is fixed after create in
    both GUIs. The "Uploads" preset fills a body limit only when the route inherits one (cloud 100 MiB, agent GUI 1 GiB —
    a GUI default, not a contract value). Volume reset on a peer site goes through the normal per-site forwarding of the
    agent API (the peer resets its own volume); the agent GUI offers it on its own site only.
82. **Image metadata fully editable; only the identity is locked** (user, on /app/services/node-red/image/v1: "in image you
    let me allow the ports and stuff; similarly allow me to edit the rest of the things, only don't allow me to refetch the
    img"). Identity = version, serviceSlug, source/reference/registry (except while the import failed), digest, platform,
    import (mode, layers, status), created — never re-fetched; "Clone as new version" fetches another image. Everything else is
    PATCHable with the registration rules (`validateImageSpec` shared by registerImage + patchImage) and the same editor
    (`frontend/src/lib/images/ImageSpecEditor.svelte`). Contract: `PatchImageRequest` gains kind / entrypoint / command / workdir /
    user / full ports / env / volumes lists + `renames` + `renameEverywhere`; types `ImageRename`, `ImageNameKind`, `ImageNameUse`,
    `ImageImpactItem`, `ImageInUseDetails`; `shared/src/image-edit.ts` (`imageNameChanges`, `imageNameUses`,
    `renameImageNames`, `renameImageSelfRefs`, `IMAGE_IDENTITY_FIELDS`). The Image / ImageSpec SHAPE is unchanged (no Go
    change). Decisions: (a) renames are explicit (the GUI tracks each row's stored name); a changed name without a rename entry
    is remove + add. (b) Impact counts templates and deployment DRAFTS only; heads / revisions / open proposals keep their own
    ImageSpec copy and are never changed, so they never block. (c) In-use removals are always blocked (409 `in-use`, also with
    renameEverywhere) until the containers stop using the name; in-use renames → 409 unless renameEverywhere, which rewrites
    templates (version+1, audit template.updated) and drafts (draftVersion+1, activity draft.updated) in the image's
    transaction; concurrent editors then get the usual ifVersion / ifRevision conflict. (d) Port number / protocol changes, env
    flag / default / option changes, additions and description changes are always allowed (containers reference names); the
    GUI warns for a retargeted port of a used image. (e) Expressions are rewritten only inside `{{ }}` blocks; file contents
    (blobs) are not scanned. (f) Legacy partial PATCH items (port `{name, description}`, env without required/secret) still
    merge by name. (g) A rename that collides with an existing container env row → invalid V_DUPLICATE (nothing saved).
83. **Image volumes have a kind: Folder | File** (user, on Single → site-01 → node-red: image node-red:v1 declares `flow` →
    /data/flows.json and `settings` → /data/settings.js; mounting the single-file volume `nodered-flow` on `flow` warned
    V_VOLUME_KIND_IMAGE "… is a folder in the image …" — "how am I supposed to solve this warning?"; the validator assumed every
    image volume is a folder). Decisions: (a) `ImageVolume.kind: 'dir' | 'file'` (TS required; Go `Kind *string` omitempty so
    older revision catalogs keep their hash; readers use `imageVolumeKind` / `validate.ImageVolumeKind`, which infer a missing
    kind from the path with `inferImageVolumeKind`). Requests may omit it (`ImageVolumeInput`): inferred, then stored explicitly.
    (b) Inference: a last path segment with an extension (letters required, ".d" excluded) = file; everything else (no
    extension, trailing "/", dot-dirs, version numbers) = folder; the editor's **Auto** shows the inferred kind for NEW rows
    only, and Folder / File are always explicit. Registry `Volumes` entries are folders (Docker VOLUME is a directory).
    (c) Kind matrix, only on an image volume row WITHOUT its own mount path (a row with its own path and an extra mount are
    the user's own paths): file volume on File = ok; dir volume on Folder = ok; file volume on Folder = warning
    V_VOLUME_KIND_IMAGE (as before, now names both controls); dir volume on File = ERROR V_VOLUME_KIND_MISMATCH (save level:
    Docker cannot mount a folder on a file the image has); file / secret / host sources: no kind check (current rule).
    (d) Every message names the control to change, and the cloud container page shows one-click fixes: "+ New single-file
    volume…" / "+ New folder volume…" (the volume drawer with that kind preselected, declares + selects it) and "Set image
    volume X to Folder|File on the image page" (`/app/services/S/image/V?volume=X&from=<container page>`: the row is outlined,
    a banner explains, "Back to the container"). The agent GUI cannot edit images: its flags point to the cloud image page.
    (e) Rename caveat: every read-WRITE mount of a single-file volume gets ONE info V_FILE_VOLUME_RENAME (not a warning; Node-RED
    saves flows.json by rename → EBUSY), with the "Recommended for Node-RED" tip (folder volume at /data seeded with flows.json,
    settings.js seeded or a read-only extra mount; §2.2). (f) "+ New data volume…" on an image volume row starts with that image
    volume's kind. (g) A kind change on the image page is always allowed (no in-use 409: it changes no name); templates / drafts
    re-validate at once; revisions keep their ImageSpec copy. (h) Migration `backend/scripts/migrate-f.ts`: only image documents,
    only missing kinds (inferred), updatedAt kept, notes per template / draft row whose result changes (warning gone / new
    mismatch error), one `system.migration` audit (op system:migrate-f); verify-schema requires kind on every image volume.
    (i) Agent compatibility: revision catalogs now carry `kind`; the agent decodes it (omitempty pointer) so catalog hashes
    round-trip; an agent older than this release drops the unknown key and refuses such a revision (catalog hash mismatch) →
    update the agent (agent/latest) before publishing. The driver does not use the image kind (a file site volume is still
    created as a file before the bind mount); TestEdgeParityDocker mounts the single-file volume on a File image volume row.
84. **Nothing is "shown once" — every credential is viewable and regenerable any time** (user, 2026-10-08, on the "Agent GUI
    access key" modal after creating a deployment: "i don't want anything in the entire website that is a one-time show. i
    should be able to view, regenerate new key/pass anytime, just make it visible for 60s and add it to activity logs. make
    sure of this!!!"). Extends #1 (secrets stored like variables, masked, every reveal logged). Supersedes every "shown once",
    "I saved the key" checkbox and show-once modal in this spec, docs-v2 and both GUIs.
    (a) **Storage.** Every credential-like value is stored retrievably in Firestore, plain like secrets (no KMS, prototype):
    `deployments/{id}.accessKey` (grouped plaintext) next to `accessKeyHash` (argon2id; agents still get ONLY `{hash, version}`
    — the plaintext never leaves the cloud except through the endpoints below); `installTokens/{hash}.token` while the token is
    active (nulled on enroll / revoke / expiry by the daily job; single use + 24 h unchanged); `accounts/{uid}.lastSetPassword
    {value, setAt, setBy, validAfter}` and `.lastResetLink {value, createdAt, createdBy, expiresAt, validAfter}` (`AccountDoc`);
    registry passwords and deployment secrets as before. Templates never hold secret values (only generate rules, #1) → there
    is nothing to reveal on a template; the template secret editor says "set per deployment".
    (b) **One UI component `RevealValue`** in both GUIs (frontend `src/lib/ui/RevealValue.svelte`, agent GUI the same file
    name): masked by default (`••••` + a length hint "24 chars" when known); **View** calls the reveal endpoint (which logs);
    the value shows with a visible 60 s countdown ring (`REVEAL_SECONDS`), then re-masks automatically — also on route change
    and when the tab becomes hidden; never cached in localStorage / sessionStorage; **Copy** while visible. **Regenerate…**
    (where it applies): a confirm dialog (typed ACCEPT for the access key and the account password; plain confirm for install
    commands and secrets) → the regenerate endpoint (logged) → the new value shows for 60 s with the same countdown. A value
    that is not stored shows "not stored — regenerate to view" + Regenerate. After a create (deployment, account, install
    command) the value simply shows in RevealValue with its timer — no modal, no checkbox.
    (c) **Endpoints** (all POST, human auth, `Cache-Control: no-store`, listed in `CREDENTIAL_OPS`, rate limit
    `REVEAL_RATE_PER_MINUTE` = 30 value-returning requests per uid per fixed minute, Firestore counter
    `rateLimits/reveal_<uid>_<yyyymmddhhmm>` (expireAt +2 min) → 429 `rate-limited` details `{retryAfterSec}` + Retry-After;
    every response has `value` + `hideAfterSec: 60`, `RevealResponse`):

    | Op | Path | Permission | Logs |
    |---|---|---|---|
    | revealAccessKey | /v1/deployments/{id}/access-key/reveal | deployment.setup.write (the key = full agent GUI access) | activity access-key.revealed + audit deployment.access-key-revealed; 410 gone reason not-stored |
    | rotateAccessKey (= Regenerate) | …/access-key/rotate | deployment.setup.write | access-key.rotated + audit deployment.access-key-rotated |
    | createInstallToken (replaceActive = Regenerate) | …/install-tokens | deployment.setup.write | install-token.created (+ install-token.revoked per replaced) + audit deployment.install-command-created |
    | revealInstallCommand | …/install-tokens/{tokenHash}/reveal | deployment.setup.write | install-token.revealed + audit deployment.install-command-revealed; 410 gone reason used / revoked / expired / not-stored |
    | revealUpgradeCommand (§21 #92) | …/agents/{siteSlug}/upgrade-command/reveal | deployment.setup.write | install-command.revealed + audit deployment.upgrade-command-revealed; 410 gone reason not-stored |
    | regenerateUpgradeCommand (§21 #92) | …/agents/{siteSlug}/upgrade-command | deployment.setup.write | install-command.regenerated + audit deployment.upgrade-command-regenerated |
    | revealSecret | …/secrets/{secretSlug}/reveal | deployment.secrets.read (the resource that exists to gate secret values) | secret.revealed + audit deployment.secret-revealed |
    | regenerateSecret | …/secrets/{secretSlug}/regenerate `{ifRevision}` | deployment.secrets.write | draft.updated + secret.regenerated + audit deployment.secret-regenerated; invalid V_SECRET_NO_GENERATE without a generate rule; 409 on ifRevision mismatch |
    | revealRegistryPassword | /v1/registries/{id}/reveal-password | registries.write | audit registry.password-revealed |
    | createAccount | /v1/accounts | accounts.admin | audit account.created (data.passwordSet = true) |
    | setAccountPassword ("Set new password…") | /v1/accounts/{uid}/password | accounts.admin | audit account.password-set |
    | revealAccountPassword | /v1/accounts/{uid}/password/reveal | accounts.admin | audit account.password-revealed; 410 gone when none / changed by the person |
    | resetAccountPassword (new link) | /v1/accounts/{uid}/password-reset | accounts.admin | audit account.reset-link-created |
    | revealAccountResetLink | /v1/accounts/{uid}/password-reset/reveal | accounts.admin | audit account.reset-link-revealed; 410 gone when none / expired / used |
    | createDeployment | /v1/deployments | as before | deployment.created + audit (value = access key) |

    Deployment-scoped credential ops (`AUDIT_DEPLOYMENT_OPS`) write BOTH the `_cloud` activity and a root audit entry (the
    router audit hook audits ops in AUDIT_DEPLOYMENT_OPS even under /v1/deployments/{id}/…); audit `data` / `changes` never
    contain a value (redaction now also masks every non-identifier leaf inside an object stored under a secret-named key,
    e.g. `lastSetPassword.value`; `resetlink` joins the secret-key pattern). Activity data never contain values.
    (d) **Accounts.** Firebase Auth cannot return a password, so the account flow is: creation and "Set new password…" set a
    generated 20-char alnum password with the Admin SDK and store it (above); after the set the backend reads the Auth user
    and stores `validAfter` = `tokensValidAfterTime`; on getAccount / listAccounts (batched getUsers) / the reveals, a later
    `tokensValidAfterTime` (the person changed their password, used a reset link, or tokens were revoked) clears
    `lastSetPassword` (and `lastResetLink`) — it is not their current password any more. The admin panel's **Password** row:
    RevealValue (View → account.password-revealed) + "Set new password…" (typed ACCEPT; the person's sessions end). "Reset
    link…" makes a new link any time (stored 1 h, RevealValue) — `account.reset-link-created`. No set-password link is shown
    after creation any more; legacy audit action `account.password-reset` stays in the catalog for old entries.
    (e) **Agent GUI** (`/_opm/api/secrets/{slug}/reveal`): `hideAfterSec` 60, `Cache-Control: no-store`, 30 reveals per session
    per minute → 429 `rate-limited` {retryAfterSec}; local activity `secret.revealed` as before; the GUI uses the same
    RevealValue (no Regenerate there — secrets change through a local proposal). The agent never holds the access-key
    plaintext (only the hash), so the agent GUI shows none; its login page points to the cloud Setup page ("View" the key).
    (f) **Migration** `backend/scripts/migrate-g.ts` (dry run → apply → verify-schema 0): adds `accessKey: null` to deployments
    without it, `token: null` to install tokens without it, `lastSetPassword: null` / `lastResetLink: null` to accounts; one
    `system.migration` audit. Never rotates or regenerates anything: pre-#84 deployments / tokens show "not stored —
    regenerate to view".
    (g) **No text anywhere** may say "shown once", "only once", "one-time", "I saved": the frontend and agent GUI test suites
    grep the built bundles for `/shown once|only once|one-time|I saved/i` and must find nothing.
85. **Install script installs only pinned dependencies** (user, from a real Ubuntu server where the script stopped at "Docker
    Engine is required": "make the script do everything for us … auto detects all the services it needs; if already there,
    then best, else fetch them all and install them. if script fails it can log the errors and let user take over manually …
    make sure to only use specific versions not the latest version … if i am doing it manually then it should not matter").
    (a) Storage `deps/manifest.json` (`DepsManifest`, written by `backend/scripts/upload-deps.ts`): the official STATIC Docker
    bundle (download.docker.com/linux/static/stable) pinned per arch with sha256 — prod: Docker 28.5.2 (amd64 sha256
    ea90cfd1…6300d, arm64 9e4f8299…0044d); `minDockerApi` 1.41 (user: "min docker 20.10 is fine, keep that"); the pinned
    agent version. Served by `GET /v1/install/deps/{name}?arch=` (install token, same rules as installBinary). 28.x, not
    29.x, as the pinned bundle; an EXISTING Docker 20.10 … 29.x is used as is: the agent negotiates the Engine API (GET
    /version → min(server API, 1.47), floor 1.41) and never sends a field newer than the negotiated version (fields that need
    > 1.41 are gated; opt-in test OPM_TEST_DIND20=1 runs the driver against a real docker:20.10-dind).
    (b) The generated script embeds the exact agent + Docker versions and sha256 at generation (never "latest" at run time):
    POSIX sh, `set -eu`, numbered steps, everything logged with timestamps to `/var/log/opm-install.log`. Preflight
    OK/MISSING (root, Linux, systemd, arch, curl, tar, kernel ≥ 3.10, cgroups, ≥ 2 GiB free in /var/lib, iptables|nftables);
    missing distro tools → the exact package-manager command is printed, never auto-installed. An existing Docker with a
    usable API is used as is (any install method); stopped → `systemctl enable --now docker`; too old / unusable → never
    touched, the script fails with a manual instruction. Missing → the pinned static bundle (sha256 checked) into
    /usr/local/bin + containerd.service / docker.service / docker.socket + /etc/docker/daemon.json (json-file 10m×3,
    live-restore) + docker group, recorded in `/etc/opm/installed-deps.json`. `OPM_SKIP_DOCKER_INSTALL=1` (placed after
    `sudo`; the Setup page toggle "Advanced: skip Docker install") never installs Docker. `DRY_RUN=1` for tests.
    (c) Failure = the failed step, the last 20 log lines, the log path, a MANUAL instruction for that component and a non-zero
    exit; rerunning the same command is safe (the token is consumed only at enroll / expires after 24 h); completed steps are
    detected and skipped; an already enrolled data dir is upgraded + restarted, not re-enrolled. Verification polls the local
    `/_opm/api/info` until enrolled, then prints the GUI URLs (first IP of `hostname -I`). Offline bundle (`?offline=1`) = next
    step (backend/TEAM.md). Full text: §19.1.
86. **Port 80 is always up: the edge always runs; enrolled sites upgrade without a new token** (user, from a real
    DigitalOcean Ubuntu 24.04 droplet 139.59.2.56: the installer succeeded, "local GUI answers on port 7080", but
    http://139.59.2.56/_opm gave ERR_CONNECTION_REFUSED — before the first head the agent listened only on 7080/8080;
    refined: "make sure the 80 port is always up and used, even when edge has nothing in it").
    (a) The embedded Caddy edge ALWAYS runs. No head (or this site not in the head) → the built-in **default edge** on
    agent.json `edgeHttpPort` (default 80; null = none): host `*`, no TLS, `/_opm/*` GUI + API, `/` → 302 `/_opm/app`, all
    else → "No configuration applied yet — open /_opm/ to approve the first update". A head whose edge has no routes still
    serves `/_opm/` + the site's not-found page. 443 is bound only when a host has TLS. A separate "takeover" listener was
    built first and dropped by the user's refinement (one edge process always owns :80).
    (b) No port dance on updates: the agent binds each edge port ONCE and hands Caddy a dup (`fd/<n>`); every config swap
    is a graceful `caddy.Load` on the same socket (grace period 30 s); a hammer test (8 workers, 40 swaps/s) sees zero
    refused connections. Caddy's own reuseport listeners closed the old socket on each swap (a refused / reset connection
    at the swap edge) — that is why the agent owns the sockets. Requests caught exactly in an old server's shutdown may
    still see EOF (≈0.2 % under the hammer; a real update swaps once).
    (c) Busy edge port (e.g. nginx on :80): `agent.port-bind-failed` once (with `owner` from /proc: "nginx (pid 812)"),
    retry every 30 s silently, GUI stays on 8080/7080; alert in the agent GUI Status page, heartbeat
    (`edge.bindErrors[].owner`) and the cloud Setup page (`portAlerts`: "port 80 is used by <owner> — stop it or change
    edge.httpPort"). During an APPLY an edge bind failure still fails the step (→ rollback).
    (d) Heartbeat `guiPorts` + `edge.defaultEdge` → Setup `guiUrls` (detectedAddresses IPv4 × ports, port 80 first),
    `defaultEdge`, `portAlerts`; old agents → fallback :8080/:7080 in the frontend.
    (e) Installer step 7 (§19.1): listening ports (ss / netstat / /proc), URLs port-80 first, ufw / firewalld commands
    (report only), self-check with curl (3 s) on the host IP and the IP the cloud saw + the cloud-firewall note.
    (f) Upgrade: the token of the site's current enrollment keeps working (no expiry) for script / binary / deps and gets
    the upgrade-only script (never writes the token); `opm-agent upgrade` (agent ≥ 0.8) uses the signed site identity
    (`GET /v1/agent/release`, `GET /v1/agent/binary`; release signature checked against agent.json `releasePublicKey`, else
    the cloud's key). Document: "rerun the same install command to upgrade".
    (g) Add-on (agent GUI redesign request): CloudView / EnrollResponse `consoleUrl` (env OPM_CONSOLE_URL, else the first
    https OPM_FRONTEND_ORIGINS) + `deploymentLabel`; AgentInfoResponse `consoleUrl`, `apiBase`, `deploymentLabel`.
    Released as agent 0.8.2-10080340 (= agent/latest = deps/manifest.json agent).
87. **Readiness follows the image definition exactly; failed containers keep their logs** (user's real server: the first
    update of deployment JzOFFecGJv7AdGWtCjjf (15 containers, Docker 28.5.2, agent 0.7.2) rolled back with
    "northwind-backend-admin: unhealthy: 3 checks failed in a row: port server is not published on loopback"; "how can i see
    the container logs?").
    (a) **Root cause** (reproduced on a docker:28.5.2-dind): the container crash-looped (exited, restart policy always).
    During Docker's restart backoff `docker inspect` reports `State.Running = true`, `State.Restarting = true` and an EMPTY
    `NetworkSettings.Ports` ({}). The agent only looked at `Running`, ran the http check against the empty port map and
    reported "port server is not published on loopback" ×3 after the start period — the real cause (the app exiting) was
    hidden. The dual publication itself (127.0.0.1::8080 + 0.0.0.0:6007:8080) inspects correctly on Docker 28.5.2 at API
    1.41 / 1.44 / 1.47 (not the cause), and the loop map was already re-read before each check.
    (b) Fix: Restarting (or not running with a restart policy) = "container is restarting: last exit code N(, OOM killed),
    M restarts"; the port map is re-read before every check, the loopback port falls back to a 0.0.0.0 / "" / "::"
    publication; an empty map = "port X has no host publication yet (docker ports: …)" (not ready yet, counted only after
    startPeriodSec); retries = consecutive failures after startPeriod; interval honoured (fractional seconds); type none =
    10 s without a restart. VERIFY also reports restarting containers and re-reads the port before its retry. The monitor
    marks a restarting container unhealthy with the same message.
    (c) Diagnostics before the rollback (§15.4): last 200 log lines (secrets redacted), inspect state, port map, health
    history → `UpdateSiteState.containers[].failure` (`ContainerFailure`, `HealthCheckRecord`), debug log, `update.failed`
    `data.failures` (≤ 12 KiB; visible in the cloud Activities drawer as "Failed containers"). The failed container is
    stopped + renamed `<name>-failed-<unix>` (`docker logs` on the server), excluded from plans, removed after the next
    successful update or 24 h.
    (d) Agent GUI: Updates page "Failed containers" (expandable: error, state, ports, kept-as + `docker logs` hint, health
    history, log tail); Status page container logs = live tail with search, **follow**, **Download**.
    (e) Tests: driver unit tests on a scripted daemon (loop map empty at first, crash loop, startPeriod / retries /
    interval, type none, port fallback); opt-in `OPM_TEST_DIND28=1` TestDocker28HealthReplay run INSIDE docker:28.5.2-dind
    (healthy app after 5 s → completed; /health 500 → clear failure + the app's log line; crash loop → "container is
    restarting: last exit code 3"); agent TestCaptureFailureKeepsContainer (real Docker: logs with a secret redacted, kept
    container, plans skip it, capped activity data, cleanup).
88. **Docker container names = the OPM container names; `containers.<slug>.*` expressions** (user, real server, Docker 28.5.2,
    agent 0.8.x: "`docker ps` shows `opm_JzOFFecGJv7AdGWtCjjf_site-01_mqtt` … in my backend container I reference the mqtt
    container as simply `mqtt` … I cannot have these random prefixed container names!"; then: "i don't mind the random
    container name prefix, but then you need to give me the capability to reference them in expressions").
    - Verified first (Docker integration, `TestDockerNamesIntegration`): the agent already gave every container the network
      alias = containerSlug, so `mqtt` DID resolve from a container on the site network with the old prefixed docker names;
      the new names do not change DNS (the slug alias stays; `nslookup mqtt` from `backend` → the mqtt IP in both modes).
    - agent.json `containerNames: "plain" | "prefixed"` (default plain; `ContainerNames` in Go, `containerNames?` in
      `AgentConfigFile`); any other value → the agent refuses to start. Plain = docker name = containerSlug; prefixed =
      the old `opm_<dep>_<site>_<slug>`, for several sites on one Docker host (e2e harness, mesh tests and opm-devmesh set it).
    - Network name unchanged (`opm_<dep>_<site>`); aliases `[slug, opm_<dep>_<site>_<slug>]` at create (not hashed).
    - Migration in place (`MigrateNames`, start + before every APPLY plan): rename by id, no recreate (the name is not in
      the spec hash — `TestNameNotHashed`; the integration test checks the same id, the same StartedAt and a `keep` plan).
      Containers renamed by the migration lose the OLD docker name from DNS (Docker behaviour) until their next recreate;
      the slug never stops resolving. Not done: a network disconnect / connect to add the alias (Docker 28 refuses a fixed IP on
      a network without a user subnet; a new IP breaks live connections).
    - Clash = staging error (§15.1 text; shown as the staging error of the site in both GUIs' staging views); the same for
      a managed container of another deployment / site. Unmanaged containers are never touched. Name lookups use the exact
      name from the container list (`docker.FindNamed`), never `/containers/{name}/json` (id-prefix matches: a slug such as
      `cafe` could hit an unrelated container id).
    - Everything else finds managed containers by labels (already true: plan, status, logs, health exec, tools targets,
      failure capture, snapshots, prune by image id).
    - Expressions: `containers.<containerSlug>.host` (= the alias = containerSlug) and `containers.<containerSlug>.port.<portName>`
      (internal port). Spelling decision: a PLURAL root instead of `container.<slug>.*` with reserved slugs — `container.*`
      stays self, `containers.*` is any container of the site, no reserved container names, one rule. Fields: env, imageEnv,
      file (site-level facts); NOT mountPath / host / hostPort / imagePort / edge (E_NOT_ALLOWED). Unknown container / port
      → E_UNKNOWN_PATH; unresolvable image port → E_NULL; a reference to a disabled container → warning
      V_CONTAINER_REF_DISABLED (env values, image env defaults of enabled containers, rendered files of the site). Shared
      vectors (`expr-vectors.json` context `site-peers`), TS + Go; autocomplete in both GUIs lists the container hosts first,
      then their ports.
89. **Agent operator toolbox: logs, terminal, monitoring, live container ops, volumes, network, Docker** (user, with screenshots
    of the agent GUI container config page: "make sure the agent GUI has every tool necessary like logs, ssh, monitoring,
    etc. and make the GUI a bit better and cleaner, not an afterthought"; later: "in the agent GUI make sure dropdowns are
    custom like we have in the cloud GUI"). Normative API: §7.4 + `shared/src/agent-tools.ts`.
    (a) **Logs.** Container logs are streamed from the Docker logs API as SSE (EventSource, cookie auth) — not a WebSocket:
    one-way, proxy-friendly, reconnects with `since`. Multi-container view = one stream, initial tails merged by timestamp.
    The edge access log is a Caddy middleware `opm_access_log` (own package, prepended to every edge server by one hook in
    reloadEdgeQ; edge/* untouched) writing JSON lines to `<agentHome>/logs/edge-access.log`, rotated at 20 MiB × 5. The agent
    log stream tails the existing debug log files.
    (b) **Terminal.** xterm.js bundled in the GUI; a WebSocket per terminal, authorised by a single-use 60 s ticket from
    `createTerminal` (session + actor; browsers cannot send the actor header on a WebSocket) and an Origin check. Host shell =
    PTY without cgo, `hostShell.user` (default the agent's user = root in production), typed site slug per session.
    Container exec = `docker exec` with a TTY (sh / bash / custom argv). `tools.hostShell` / `tools.containerExec` in
    agent.json (default true) switch them off; the GUI hides a disabled tool and shows how to enable it. Limits: 4 open +
    reserved sessions per site, 30 min idle (no input), rate 10 creates / min / session. Activities `terminal.opened` /
    `terminal.closed` (actor, target, duration, exit code, reason, closedBy). Transcripts: output only, asciicast v2, 7 days,
    ≤ 200 MiB, never uploaded, known secrets masked before writing (guarantee + limits in §7.4).
    (c) **Monitoring.** Every 15 s: per container one-shot `docker stats` (CPU % from consecutive samples, memory used /
    limit, net rx/tx and block I/O as rates) + inspect (restarts, OOMKilled); host CPU, memory, swap, disk per mount (/ and
    agentHome, one card when the same file system), load 1/5/15, uptime. Stored in a SEPARATE SQLite `<agentHome>/metrics.db`
    (opm.db is untouched): raw 1 h, 1-minute rollups 24 h, 15-minute rollups 7 d (avg; CPU peak = max), incremental vacuum
    hourly, capped at 64 MiB (oldest half dropped above it). Thresholds are GUI badges only: CPU > 85 % / memory > 90 % =
    amber when the latest sample is above, red when every sample of the last 5 min is above; disk > 90 % = red. Charts are
    hand-rolled SVG (no chart library in the bundle).
    (d) **Container actions.** restart / stop / start with confirm; `container.restarted|stopped|started` (reason, and for
    stop `expectedRunning`). A stopped container is marked **manuallyStopped** (`<agentHome>/tools/manual-stops.json`: at,
    actor, head seq) until a start / restart (toolbox or anything else that starts it) or a new head revision (the next
    update recreates / starts it as configured). The agent never restarts it in between (it has no reconciler that starts
    containers; the Docker restart policy does not restart a stopped container); the GUI shows "stopped manually by X", not
    "failed"; the readiness monitor still reports it not ready (the edge answers 503 for its routes). Actions answer 409
    `locked` while an update runs. Kept failed copies (§21 #87) get read-only tools only. Unmanaged containers on the host
    are not in the toolbox (use the host shell).
    (e) **Volumes browser** is read-only this round (tree, text ≤ 1 MiB, download file / tar.gz, sizes). Upload / delete /
    edit = next step (needs snapshot-before-write + activities).
    (f) **Network tools.** dns / tcp / http / ping from the host (native) or inside a container (the image's own tools; "no
    tool in the image" is a result, not an error); `tool.netcheck`. HTTP shows status, headers (Set-Cookie values hidden),
    DNS / connect / TLS / TTFB timing; redirects are not followed (Location shown).
    (g) **Docker housekeeping.** Images with users + `docker system df`; "Prune unused images" (typed PRUNE) removes images no
    container uses except the head revision's images (older revisions' images are re-loaded from the agent's blobs when a
    rollback needs them); `docker.images-pruned`.
    (h) **Mesh.** `*` merges the list GETs; other sites are forwarded (JSON as usual, SSE with flushes, the terminal
    WebSocket bridged with a signed upgrade); shells / exec over the mesh only with the actor identity and logged on both
    sites.
    (i) **GUI.** Sidebar group "Tools" (per site): Logs, Terminal, Monitoring, Containers, Volumes, Network, Docker; the
    Status page links into them and every container row has quick actions (logs, terminal, restart, monitoring). Config
    pages polished (no clipped volumes column, "shared · N containers" badge + popover, one-line hints with ⓘ, sticky
    Summary). No native `<select>` anywhere: the cloud console's custom Select / MultiSelect (search, keyboard, groups,
    badges, portal) — a Playwright test asserts zero `select` elements on every route.
90. **Everything the agent CLI does is in the GUIs, including upgrading the agent** (user, real server 139.59.2.56 on agent
    0.7.2: "`sudo opm-agent upgrade` → unknown command: upgrade … in the next build make sure to have all of this in the GUI,
    even the upgrade"). Contract: `shared/src/agent-self.ts` (AGENT_SELF_API, §7.5) + types.ts (`GUI_UPGRADE_MIN_VERSION`
    = 0.10.0, `AGENT_UPGRADE_HEALTH_SEC` = 60, `AgentReleaseInfo`, `AgentUpgradeProgress`, `ReleaseVerification`,
    `AgentUpgradeRequest`, `AgentUpgradeRun`, `SiteAgentUpgradeView`, `compareAgentVersions`, `supportsGuiUpgrade`,
    `agentUpgradeAvailable`) + CLOUD_API `requestAgentUpgrade` / `cancelAgentUpgrade` / `upgradeAllAgents` /
    `cancelAgentUpgradeRun`.
    (a) **CLI → GUI map.** `version` / `status` → agent GUI **Agent** page (sidebar, last item): version, build (go
    version, commit, build time), pid + uptime, config path, data dir, binary + opm-agent.prev, ports, Docker version + API,
    systemd state + the boot guard, enrollment (state, enrolled date, site key fingerprint `SHA256:<b64>`), cloud link
    (route, last fetch, last heartbeat, error, console). `verify-release` → "Check release" + the verification card of the
    upgrade (signature ok, key source agent.json / cloud, key fingerprint, sha256 match, self-test). `upgrade` → "Upgrade
    to X". `snapshot list / export / import` → Backups (list, download .opmsnap with / without images, import). `keygen` /
    `enroll` / `install` stay CLI-only (pre-enrollment): the page shows the fingerprint + enrollment date, and for a revoked
    site the re-enroll lines (revoke in the cloud → new install command → `sudo mv /var/lib/opm /var/lib/opm.revoked-<date>`
    → run the new command). New GUI-only operations: **Restart agent** (typed site slug; `agent.restarted`), **agent.json**
    view (secrets masked) + editor of the safe settings (`tools.hostShell`, `tools.containerExec`, `tools.devTools` (§21 #97), `containerNames`,
    `edgeHttpPort`, `guiAliasPort`; `validateAgentSettings`; written atomically with `agent.json.bak`; restart; activity
    `agent.config-changed`), **Download support bundle** (tar.gz: SUPPORT_BUNDLE_SECTIONS; `agent.support-bundle-downloaded`),
    **Uninstall** = copyable commands only (`uninstallCommands`), never a GUI action.
    (b) **Upgrade flow (one code path for GUI, cloud request, mesh run and `opm-agent upgrade`).** Preconditions: enrolled;
    no update / rollback / snapshot / restore running (409 `locked`); no other upgrade (409 `conflict`); `version` = the
    pinned version (409 `stale`); typed site slug (422 `invalid`). Phases persisted in `<agentHome>/upgrade/state.json`
    (+ `history.json`, 10): **download** (signed `GET /v1/agent/release` + `GET /v1/agent/binary`, Range-resumable, the
    relay works; progress = bytes / `sizeBytes`) → **verify** (Ed25519 release signature of the sha256 hex with agent.json
    `releasePublicKey`, else the cloud's key — a mismatch refuses; file sha256; self-test `opm-agent.new version` prints the
    pinned version) → **swap** (`opm-agent.new` written next to the binary + fsync; the current binary hard-linked / copied
    to `opm-agent.prev`; `rename(new, opm-agent)` = atomic) → **restart** (systemd: `systemctl restart --no-block
    opm-agent`; not under systemd: graceful stop + `execve` of the same path and argv) → **health** (the new agent, on start,
    finds `upgrade/pending.json`, finishes its startup — store, listeners, local `GET /_opm/healthz` — and writes
    `upgrade/healthy`, state done, `agent.upgraded`). Activities `agent.upgrade-started` (old agent), `agent.upgraded` (new
    agent), `agent.upgrade-failed` (any phase), `agent.rolled-back`. A failure before the swap leaves the old agent running
    (no restart).
    (c) **Rollback mechanism = two independent layers**, both restore `opm-agent.prev` when the new binary is not healthy
    within `AGENT_UPGRADE_HEALTH_SEC` (60 s) after the restart: (1) the **watcher**: before restarting, the old agent starts
    the KNOWN-GOOD binary (`opm-agent.prev upgrade-watch --data-dir …`) outside the service's lifetime (`systemd-run
    --unit opm-agent-upgrade-watch-<id> --collect`, else a detached `setsid` process); it polls `upgrade/healthy` + the local
    `/_opm/api/info` version; on timeout it stops the agent (systemctl stop / kill the pid), renames `opm-agent.prev` back,
    starts it, writes `upgrade/rolled-back.json`; (2) the **boot guard**: a POSIX sh script `/usr/local/lib/opm/upgrade-guard.sh`
    run as `ExecStartPre=-` by the systemd drop-in `/etc/systemd/system/opm-agent.service.d/upgrade-guard.conf` (written by
    the agent before every upgrade + `systemctl daemon-reload`, and by the install script); on every (re)start it restores
    `.prev` when `pending.json` is past its deadline without `healthy` and the binary is still the new sha256 — this covers a
    crash-looping new binary and a reboot during the watch. The restored old agent reads `rolled-back.json` and writes
    `agent.upgrade-failed` + `agent.rolled-back` (actor = the requester). Chosen over a systemd WatchdogSec (needs
    sd_notify in every old version + Type=notify) and over a guard inside the new binary (a broken binary cannot guard itself).
    (d) **Sessions survive** the restart (SQLite `sessions`; cookie unchanged; actor tokens are site-key JWTs): the GUI keeps
    polling `GET /_opm/api/info` every 2 s while restarting (≤ 120 s) and continues on the same session.
    (e) **Mesh "Upgrade all"** (agent GUI): the coordinating site upgrades every reachable site below the pin, ONE at a time,
    in siteSlug order with itself LAST (it restarts), by forwarded `POST sites/{s}/agent/upgrade` + polling `GET
    sites/{s}/agent/upgrade` until done / failed / rolled-back (timeout 10 min); the first failure stops the run; agents
    below 0.10.0 / unreachable sites = skipped. State `<agentHome>/upgrade/run.json`, finished by the coordinator after its
    own restart.
    (f) **Cloud console.** Setup + Status show per site `installed / latest` and an "upgrade available" badge
    (`SiteAgentUpgradeView`); **Upgrade agent** (typed site slug) queues `agents/{site}.upgradeRequest` (agentEpoch +1 →
    `CloudView.agentUpgrades`); the agent starts it on the next poll (once per requestId, actor = requestedBy email, via
    cloud) and sends `HeartbeatRequest.upgrade` at once on every phase change; the heartbeat handler clears the request when
    the progress for that requestId is done / failed / rolled-back. **Upgrade all** (typed UPGRADE) = a run on the
    deployment doc (`agentUpgradeRun`), sequential: the next site's request is queued only when the previous one reports done;
    the first failure stops the run. Never automatic. Requests expire after 24 h. Audit `deployment.agent-upgrade-requested`
    / `-cancelled` + `_cloud` activities `agent.upgrade-requested` / `agent.upgrade-cancelled`. Agents < 0.10.0 cannot do it:
    the button is disabled and the hint says "rerun the install command of this site once (it stays valid for upgrades)".
    (g) **Release notes**: `deps/manifest.json` `agent.notes` (upload-deps.ts `--agent-notes` / `--agent-notes-file`) →
    `AgentReleaseResponse.notes`, `CloudView.agentRelease`, `SetupResponse.agentRelease`.
    (h) **Install script** stays the bootstrap; 0.7.x / 0.8.x / 0.9.x reach ≥ 0.10.0 by rerunning the same install command
    (upgrade-only script); it also writes the boot guard + drop-in. Tested (backend container test 0.7.x → new).
    (i) Implementation notes (round I teams): the CLI `opm-agent upgrade` runs the same engine in its own process (no systemd
    or `--no-restart` → pending.json with deadline 0: no watcher, the guard ignores it, the next start finishes it); downloads go
    to `<agentHome>/upgrade/download/<version>` (resumable), only verified bytes become `<bin>.new`; the self-test reads only the
    `opm-agent …` stdout line; the watcher and the guard share a mkdir lock and re-check (pending, not healthy, binary still
    the new sha256) → never a double rollback; `.prev` missing → no rollback + a clear error; the guard restarts the watcher
    after a reboot inside the window; `failedPhase` = `restart` when the new process never reached health, else `health`; a
    cloud request is marked handled BEFORE it starts (a restart never reruns it), left queued while an update / snapshot runs,
    `done` at once when already on the version; revoked = cloud 401 unknown-site / signature or 403 (any 2xx clears it); the
    mesh run marks the rest `cancelled` after the first failure (the cloud run does the same). Cloud: one pin helper
    `agentReleaseInfo()` (no cache); a request expires only if never started; agentVersion ≥ requested clears it as done
    (CLI / install-script upgrades); a site with an unfinished upgrade < 15 min old → 409 conflict; cancelling a run's
    queued request cancels the run. Frontend: agents < 0.10.0 get the disabled button + inline path (`sudo opm-agent
    upgrade` for 0.8+, else rerun the original install command — the used token is badged "valid for upgrades" and cannot
    be revealed because #84 clears it at enroll). Agent GUI route `/_opm/app/{siteSlug}/agent` (+ `*`); restart overlay
    probes `GET /_opm/api/health` (fallback /info), continues on the same session, 401 → login with an explanation.
91. **The install command is the hard fallback for any up- / downgrade** (user: "if I cannot install the latest version via GUI
    or terminal like right now, I should have one hard fallback: I can run the first setup agent install cmd again … smart: no
    docker → ask … agent already running → ask upgrade / downgrade by version compare … install the new agent in the same
    setup … no downtime"). Contract: types.ts `AgentBuildMeta`, `AGENT_LEGACY_SCHEMA` = 1, `agentInstallAction`,
    `agentSchemaCompatible`, `AgentReleaseResponse.schema/minSchema`, `DepsManifest.agent.schema/minSchema`,
    `AgentHealthResponse` + `GET /_opm/api/health` (public; 503 while not ok), `AgentInfoResponse.schema`, CLOUD_API
    `installRelease` (`GET /v1/install/release?os&arch&version`, install token) + `installScript?version=`.
    (a) **Prompts under `curl | sh`** read `/dev/tty`. No TTY → never block: `OPM_YES=1` (yes to all), `OPM_INSTALL_DOCKER=1|0`,
    `OPM_ALLOW_DOWNGRADE=1`; without a flag the answer is "no" → exit with the exact rerun command including the flag
    (env after `sudo`).
    (b) **Docker**: missing → "Docker Engine is not installed. Install the pinned Docker 28.5.2 now? [y/N]"; no → manual
    instructions (any Docker ≥ 20.10, then rerun the same command), exit 1. This replaces round G's silent auto-install;
    `OPM_SKIP_DOCKER_INSTALL=1` stays as an alias of `OPM_INSTALL_DOCKER=0`. An existing Docker with API ≥ 1.41 is used without
    asking; a stopped one is started (printed).
    (c) **Installed agent** (binary + unit + /etc/opm/agent.json + enrolled data dir): installed version (`opm-agent version`,
    `--json` from 0.10.0) vs the target → "Installed A → available B: UPGRADE | DOWNGRADE | SAME (reinstall)" + [y/N]; no →
    "nothing changed", exit 0. DOWNGRADE needs a second confirmation (`OPM_ALLOW_DOWNGRADE=1` without a TTY) and is refused
    when `agentSchemaCompatible(dbSchema, target)` is false (dbSchema = the installed build's `schema`, legacy = 1), unless the
    operator restores a DB backup taken for a compatible schema (`/var/lib/opm/backup/<version>-<ts>/`, offered by the
    script with its own confirmation; activities / sessions after it are lost). Target version: the pin, or
    `OPM_AGENT_VERSION=x` / `?version=x` on the script URL (token-gated; only versions uploaded under `agent/<v>/`; sha256 +
    signature verified as usual; schema from `agent/<v>/release.json`).
    (d) **Same setup**: agent.json, the site key, the enrollment, the data dir, volumes, files, certificates and transcripts
    are kept; the token is never written for an enrolled site (never re-enrolled); app containers are never touched;
    agent.json only gains missing keys with defaults (the script prints the added keys). Every up/downgrade first backs up
    `/var/lib/opm/*.db*` to `/var/lib/opm/backup/<oldVersion>-<ts>/` (taken while the agent is stopped for the swap; the 5
    newest kept). The binary is swapped keeping `/usr/local/bin/opm-agent.prev`; units are (re)written by the NEW binary's
    `opm-agent systemd-units --install` (unit + optional socket + guard drop-in; one source of truth in Go; a target without
    the subcommand gets the legacy §19.2 unit and no socket unit); `systemctl daemon-reload`; restart.
    (e) **Health after the swap**: ≤ 60 s for `GET /_opm/api/health` ok with the target version (+ cloud connected when the old
    agent was connected before the swap); else automatic rollback: `.prev` back, the DB backup restored when the schema
    changed, restart, print what happened + the log path, exit 1. (The agent-side boot guard of #90 remains as the second layer.)
    (f) **No downtime for apps**: containers are Docker processes; the agent never stops or recreates containers on shutdown
    or start (start adopts the running containers by label + spec hash; tested: ids and StartedAt unchanged across an agent
    restart). **Edge continuity**: systemd socket activation `opm-agent.socket` (ListenStream for the default edge port,
    agentPort and guiAliasPort from agent.json; ONE socket unit with `FileDescriptorName=opm`, because systemd applies the name to every fd of a unit — the agent matches inherited fds by their local port; `Service=opm-agent.service`); the agent uses
    inherited fds (`LISTEN_FDS` / `LISTEN_FDNAMES`) for those listeners (the round-H socket-ownership layer hands the edge fd
    to Caddy) and binds every other port itself (443, head edge ports, alias changes) — the fallback when not
    socket-activated. While the process restarts the kernel queues connections in the systemd-held socket instead of refusing
    them. Optional: `OPM_SOCKET_ACTIVATION=0` (or systemd < 235 / a failed socket start) → no socket unit, the agent binds
    itself. A port change in the GUI config editor rewrites the socket unit (brief rebind of that port only).
    (g) **Unusable tokens**: an enrolled site rerunning with its used token gets this flow (#86). An expired / revoked / foreign
    token: `installScript` answers 200 with a tiny script that prints "token expired — open the cloud Setup page → Install
    command → Regenerate (or View) and run the new command" (or revoked / used-elsewhere text) and exits 1, because `curl -f`
    hides error bodies; installBinary / installDeps / installRelease keep 410 gone.
    (h) Implementation notes: `OPM_YES=1` answers the install / upgrade / reinstall + Docker questions but NOT the downgrade's
    second confirmation (`OPM_ALLOW_DOWNGRADE=1`) nor the DB-backup restore (`OPM_RESTORE_DB_BACKUP=1`, data loss); the rerun
    hint carries the token, so it goes to the terminal only (the log gets it without the token); signature check before
    anything new runs: openssl (Ed25519 raw key) → the installed agent's verify-release → the new binary (last resort);
    sh `ver_cmp` = `compareAgentVersions` on 28×28 vectors (sh + dash); backups `<dataDir>/backup/<oldVersion>-<ts>/` +
    `meta.env` (version, schema, createdAt, reason), the 5 newest with meta.env kept; healthy = `/_opm/api/health` 200 with the
    target version (targets < 0.10.0: `/_opm/api/info`); agent: schema 1 / minSchema 0, refuses to start on a newer DB,
    backs up before migrating. Measured (systemd PID 1 in a privileged debian:12, 12 s hammer during `systemctl restart
    opm-agent`): with the socket 0 failed of 1944 / 1945 (:80 / :7080, max 40 ms); without it 8 of 1952 refused on each.
    Released as agent 0.10.0-10080625 (= agent/latest = deps/manifest.json agent, with notes).
92. **Nothing one-time, part 2: every enrolled site's upgrade / reinstall command is always viewable; agent clock skew;
    the console uses the cloud's clock** (user, Dev Server Setup screenshot: site-01 agent 0.7.2, "Last seen in 5 h ·
    online", install-commands table "used" with no way to view it: "how can I get the download cmd to manually force
    upgrade?"). Contract: types.ts `InstallTokenKind` (`install` | `maintenance`), `InstallToken.kind / boundEnrolledAt`,
    `expiresAt: Ts | null`, `installTokenState()` (revoked wins over used), `InstallTokenView.kind / upgradeCommand`,
    `SiteUpgradeCommandView`, `SetupResponse.sites[].upgradeCommand`, `SetupResponse.serverTime`, `UpgradeCommandResponse`,
    `AgentClockSkew`, `AgentDoc.clockSkew`, `HeartbeatRequest.agentTime`, `SiteCloudStatus.clockSkew`, `CLOCK_SKEW_WARN_SEC`
    120, `clockSkewWarning()`, `formatSkewDuration()`; CLOUD_API `revealUpgradeCommand` / `regenerateUpgradeCommand`
    (CREDENTIAL_OPS + AUDIT_DEPLOYMENT_OPS); activities `install-command.revealed` / `install-command.regenerated`; audit
    `deployment.upgrade-command-revealed` / `-regenerated`.
    (a) **Upgrade / reinstall command** (§4.2 item 8): the enroll transaction keeps the install token's plaintext (it was
    nulled since #84) — the used token of the current enrollment IS the site's upgrade command (#86 rule unchanged: valid
    for script / binary / deps / release in upgrade-only mode, no expiry, never enrolls). Its plaintext is nulled only by
    Regenerate, revokeAgent or (unused tokens only) the daily expiry job. Older tokens (used before #92 — the user's Dev
    Server: migrate-g nulled it) → "not stored — Regenerate". **Regenerate** mints a SITE-BOUND `maintenance` token
    (`expiresAt` null, `boundEnrolledAt` = agent.enrolledAt): upgrade-only script / binary / deps / release of THIS
    enrolled site, never enrolls (enroll → 403 forbidden reason maintenance), no expiry; it ends when regenerated again
    or when the enrollment is revoked (revokeAgent revokes it; a token whose boundEnrolledAt ≠ the current enrollment →
    gone reason `enrollment-ended`, tiny script). Decision: **Regenerate also revokes the enrollment's original install
    token** (like the access-key rotate: "Regenerate" means the old command stops working; the tiny script then says
    "token revoked — open the cloud Setup page…"). The reveal is a 60 s RevealValue, rate-limited + no-store like every
    credential op, logged as activity `install-command.revealed` + audit. Decision: the user's wording
    "install-command.revealed" became a NEW activity type for the enrolled-site command; `install-token.revealed` stays for
    fresh-site install commands (`revealInstallCommand` keeps its #84 semantics; a maintenance token → 410 reason
    `maintenance`). The script for a maintenance token says "This upgrade command belongs to the enrolled site X".
    Fresh-site install tokens are unchanged: 24 h, single enroll, Regenerate = replaceActive.
    (b) **"Last seen in 5 h" — root cause = the VIEWING BROWSER's clock, not the cloud and not the agent.** Evidence
    (prod, read-only, 2026-10-08 05:34Z): `agents/site-01.lastSeenAt` 05:34:23.445Z vs the Firestore updateTime of that
    write 05:34:23.456Z vs real UTC 05:34:42 → the stored value is the server's receive time (the heartbeat / poll handlers
    always wrote `new Date()` on the server; nothing agent-reported is stored there). The agent clock is in sync: site-01
    activities `ts` (raw agent clock) are 57–198 ms before the cloud's `uploadedAt` (seq 88–127). The screenshot's three
    relative times are consistent only with a browser clock ≈ 5 h 30 min (exactly the IST offset) BEHIND: token created
    19:51:28Z shown "3 h ago", used 21:07:57Z "2 h ago", lastSeenAt ≈ real now shown "in 5 h", and "online" (computed by
    the cloud) was true. Nothing stored was wrong → no data correction. Fixes: every API response carries
    `X-OPM-Server-Time`; the console keeps the offset (≥ 5 s, else 0) and RelTime / Countdown use the cloud's clock; a
    browser more than 2 min off shows a note ("This computer's clock is 5 h 30 min behind the OPM cloud…") on Setup and
    Status. lastSeenAt stays the server receive time (now documented + tested). Agent skew is stored separately: agents
    ≥ 0.11 send `agentTime` (raw clock, NOT corrected by the signing offset — the cloud refuses signatures > 60 s off, and
    agents since #4.4 sign with the X-OPM-Server-Time offset, so skew was invisible before) → `clockSkew { skewSec =
    agentTime − receive time, measuredAt }`; |skew| > 120 s → amber "agent clock is 5 h ahead — fix NTP on the server
    (e.g. sudo timedatectl set-ntp true)" on the Setup site card and the Status table; older agents → "not reported".
    Relayed heartbeats add the relay delay (seconds) to the measurement — far below the 2 min threshold.
    (c) The cloud console has **no native `<select>`** anywhere (like the agent GUI, #89): one `Select.svelte` (wrapper of
    GroupedSelect: listbox, search-free, keyboard incl. type-ahead, `data-value`, `{testid}-opt-<value>`) replaces the 18
    native selects (Activities filters, audit-log filters, registries, account rules, image env / health / reference,
    variables, Test-a-URL, size units, file preview); `frontend/tests/no-native-select.spec.ts` scans the source and every
    route.
93. **Start order without depends, implicit + suggested depends, DNS hint, slug-alias self-check, ANSI in log views**
    (user's real server, agent 0.10.0, Docker 28.5.2, deployment JzOFFecGJv7AdGWtCjjf site-01: (A) Tools → Logs shows
    "[0m [1m [31merror [0m: Uncaught (in promise) …"; (B) update seq 3 failed: "northwind-backend-admin: not completed within
    300 s (last: container is restarting: last exit code 1, 13 restarts)", log "MQTT connection error: Error: getaddrinfo
    EAI_AGAIN mqtt"; all 15 containers depends: []; the update started 16 min after the agent upgrade 0.7.2 → 0.10.0).
    (a) **Root cause of B — the APPLY order, not the rename.** Evidence (prod, read-only): the head before seq 3 was seq 2
    = "rollback to seq 0" (no containers), so the site had NO container and the name migration renamed nothing; seq 3
    CREATED all 15. The agent started them ONE AFTER THE OTHER in slug order (containerOrder tie-break, no depends) and
    waited for each to be completed before creating the next: cassandra, northwind-admin, northwind-analytics (~34 s), then
    northwind-backend-admin — which needs `mqtt` (m… sorts after e…) — waited the full 300 s (05:59:10 → 06:04:44Z) while
    mqtt did not exist yet → EAI_AGAIN (Docker's embedded DNS had no such name), exit 1, 13 restarts → rollback. The
    update.failed log tail (with \u001b intact) is the user's evidence. Reproduced in `TestDockerStartOrderDNS` on
    Colima Docker 29.5.2 AND inside docker:28.5.2-dind: the old serial apply fails with "container is restarting: last
    exit code 1" and the hint names mqtt; between the stop and the create of a recreated mqtt the name does not resolve
    (the gap exists, but only matters when mqtt is created after its user is awaited). The rename does NOT break DNS:
    after `MigrateNames` (prefixed → plain) the endpoint keeps Aliases [slug, prefixed] and DNSNames [new name, prefixed,
    short id] on 28.5.2 and 29.5.2, and a new backend resolves `mqtt` at its first instruction. (#88's "Docker drops the
    OLD name from DNS" is true only for a container whose old name was not also an alias.)
    (b) **Fix (§14.5 step 3, §15.5):** start order = referenced containers first (explicit, implicit `containers.<slug>.*`,
    suggested literal hostnames), ties in the DECLARED order; a container waits only for its explicit + implicit depends;
    all other readiness waits run in parallel (`driver.RunStarts`); the first failure stops launching and cancels the
    other waits. With seq 3 unchanged the user's site would now start mqtt while northwind-backend-admin restarts, and the
    restart policy brings the backend up once mqtt answers (2b of the test: both ready in 3–4 s).
    (c) **Implicit depends** (`containers.<slug>.*` in env, image env defaults, rendered files) = start after + wait ready;
    **suggested depends** (literal hostname heuristic) = info `V_DEPENDS_SUGGESTED` + one-click Add in the container
    editor and the publish review of both GUIs, never applied automatically; reference cycles → declared order + warning
    `V_START_ORDER_CYCLE`. Decision: suggested edges order the start but are not awaited (a false positive must never
    block an apply); the declared order (not the slug order) breaks ties — `containerOrder` (explicit only, V_CYCLE) is
    unchanged for validation.
    (d) **Readiness hint (§15.4):** DNS failure naming a container of THIS site → `ContainerFailure.hint`,
    `failures[].hint`, and the error/summary "northwind-backend-admin could not resolve `mqtt` (EAI_AGAIN) — is mqtt
    running/healthy? add it to Depends on (northwind-backend-admin: not completed within 300 s …)". Both GUIs show the hint
    above the log tail.
    (e) **Slug-alias self-check (§15.1):** after every apply / rollback and at agent start (after the migration) each
    running managed container must hold its slug (endpoint Aliases or DNSNames) on the site network, else disconnect +
    reconnect with `[slug, prefixed]` + debug warn "slug alias repaired" (`Driver.EnsureAliases`; tested by removing the
    alias of a prefixed-name container).
    (f) **ANSI in log views (both GUIs).** The agent's pipeline keeps the ESC byte (Docker logs → SSE JSON `\u001b`;
    failure capture → activity data `\u001b`): nothing to fix there — the GUIs printed the text and the browser hid ESC.
    One shared parser `shared/src/ansi.ts` (no dependency; tests `ansi.test.ts`): SGR → spans (16 / 256 / truecolor fg + bg,
    bold, dim, italic, underline, inverse, strike, resets incl. 22–29 / 39 / 49, colon form), state carried across lines;
    every other escape removed with its text kept (CSI cursor / erase, OSC title, OSC 8 hyperlink, charset); C0 controls
    removed. **ESC lost** (a pipeline that dropped 0x1b): decided per STREAM by `detectEscLost` (no ESC anywhere and a bare
    `[0m` at a line start / after whitespace, or two bare SGRs adjacent); only then a bare `[<digits;…>m` is SGR — "[1]",
    "[x]", "[1m]" text is never touched otherwise. Palette = CSS variables `--ansi-0…15`, `--ansi-fg`, `--ansi-bg` from each
    GUI's design tokens (light + dark); 16–255 = the xterm table. Every log view has "Colors" (default on) and "Raw" (ESC
    shown as ␛); search matches the visible text (`stripAnsi`); Download keeps the raw bytes.
    (g) Tests: shared 454 (ansi + depends vectors + validate info / warning); Go `validate/depends_test.go` (same vectors),
    `driver/order_test.go` (RunStarts: parallel readiness, waits, fail-fast, rollback best effort, cycle no-deadlock),
    `driver/startorder_docker_test.go` (Colima 29.5.2 + docker:28.5.2-dind opt-in), `agent/startorder_test.go`
    (startPlan on the user's site shape).
    (h) GUIs: both use the shared parser in every log view (agent GUI: Tools → Logs, Status logs, Updates failed
    containers, activity drawer, transcripts, network output, debug log; cloud: Activities drawer "Failed containers") with
    Colors / Raw toggles (agent GUI: one setting for all views, remembered in the browser; Raw wins); `hint` = a banner
    above the log tail. Container editors show implicit chips + suggestion rows with Add; the publish review (cloud diff
    page; agent GUI review drawer + create-proposal modal) lists V_DEPENDS_SUGGESTED with Add (cloud: computed client-side
    from the saved draft, Add saves through the normal draft save) and V_START_ORDER_CYCLE; the site views show the start
    order. Dim text with its own color keeps the color (no opacity) — Deno's gray stack lines stay readable on dark logs.
    The agent's container-logs `q` filter matches the text without escape codes. Shipped as agent 0.10.1-10081328 (agent/latest +
    deps manifest pin).
94. **Deployments are DETACHED from their template; manual Template sync (3-way)** (user: "once we create a deployment from a
    template it should now be detached, i.e. if I change anything in the template file or anything it should not affect the
    deployment config, nor should it auto update. It should show a reclone tab separately from where I can check the diff
    between template config <> deployment desired cloud config and update that, then I can make the proposal.")
    (a) **Audit of leaks.** createDeployment was already a deep copy (variables + values, secret definitions with values
    generated per deployment, files = sha256 blob refs — immutable content, a template file edit makes a NEW blob, sites with
    containers / edge / volumes / settings, 404-page refs to the catalog), and no deployment path (validation, diff, publish,
    file content / previews, staging, agent poll / cloud view, setup, status) reads `templates/{id}`. The one leak was image
    **Rename everywhere**, which rewrote deployment drafts: it now rewrites TEMPLATES only; drafts keep the old names (listed
    in the 409 impact + audit `renamedEverywhere.deploymentsNotRewritten`); their draft validation shows the stale names
    until a Template sync takes the rename or a manual edit; heads never change (own ImageSpec copy). The impact dialog says
    "Templates — will be rewritten / Deployments — not changed (detached): need a Template sync or a manual edit"; its
    button is "Rename in templates". Removals in use stay blocked. Proof test: backend 25-template-sync (template edit incl.
    file content + template delete → draft, `_srcSnapshot`, `_srcBase`, head revision and diff byte-identical).
    (b) **Data.** `Deployment.srcTemplate: SrcTemplate | null` = `{ templateId, templateVersion, snapshot, copiedAt,
    baseVersion, baseSnapshot, lastSyncedAt, lastSyncedBy, skipped: TemplateSyncSkip[] }`. Templates keep NO version history,
    so the merge base is self-contained in the deployment (`baseSnapshot` = the template config at `baseVersion`; Firestore
    `_srcBase` / `_srcSkipped` JSON strings next to `_srcSnapshot`; pre-#94 documents decode to base = snapshot, baseVersion =
    templateVersion — no migration). `DeploymentView.srcTemplate: SrcTemplateView` (+ baseVersion, lastSynced*, skippedCount).
    (c) **Unlinked / no template:** the tab always shows; "Link a template…" plans with an EMPTY base (equal items =
    no-ops, differences = conflicts). "Change template…" plans another template against the OLD base; Apply relinks
    (templateId / templateVersion / snapshot / copiedAt = the new template). "Unlink template" sets srcTemplate null; the
    draft never changes.
    (d) **Merge rules** (`shared/src/template-sync.ts`, §6.1 "Template sync"): items keyed by slug / name; sites and
    containers present on both sides compare field by field (base fields absent when the base lacks them); a container
    rename = remove + add. Kinds theirs / ours / same / conflict / skipped / default. Variable VALUES belong to the
    deployment: a taken definition keeps the value; the template default only via the opt-in "use template default" item
    (kind default: the template changed its default since the base). Secrets: definitions + generate rules only; values never
    compared, shown or copied; a secret ADDED by a sync with a generate rule gets a generated value. Conflicts need an
    explicit choice (take template / keep mine / merged scalar). Env items compare as their value (string / null /
    `{ secretRef }`) so env conflicts can be merged inline.
    (e) **Skip semantics:** Apply moves the WHOLE base to the template now (baseVersion = its version); every template change
    not taken is recorded `{ key, theirsHash, templateVersion, skippedAt, skippedBy }` and shows as "skipped" (still
    takeable) until the template changes that item again (different hash) → it is a new change (normally a conflict with the
    kept value). Variable-default items are not recorded (the base already holds the new default).
    (f) **Never automatic:** no background sync, no notification-driven update; Apply writes the draft only (activity
    `draft.template-synced` + root audit `deployment.template-synced`, both deployment-audited ops like the credential ones) and
    links to Diff → Publish. The Overview shows a non-blocking banner; the sidebar item shows a badge = newer versions.
    Preview is a dry run (`previewTemplateSync`, AUDIT_EXEMPT_OPS, no activity). Permissions: plan / preview =
    deployment.config.read + templates.read; apply = deployment.config.write + templates.read (+ deployment.secrets.write when
    a secret item is taken); status = deployment.config.read; unlink = deployment.config.write.
    (g) Activity types `draft.template-synced`, `deployment.template-unlinked` appended to ACTIVITY_TYPES (Go mirror list
    appended too, cloud-only); audit actions `deployment.template-synced`, `deployment.template-unlinked`. Tests: shared
    template-sync.test.ts (every kind × section, rename = remove + add, list identity + order, skip, link without base,
    errors); backend 25-template-sync (detachment proof, plan / preview / apply, 409 draftVersion / templateVersion, 422
    unresolved / bad choice, skip, permissions, activities + audit, unlink / link / change template), 14-image-edit
    (templates-only rename + Template sync repairs the draft), 09-audit recipes; frontend mock Playwright template-sync.spec.
95. **Hold to fast-forward an intent wait** (user, on the agent GUI "Approve proposal seq 1" countdown: "let's add a fast
    forward button which will do x2 & x4 on button hold, on release slide down to x1.")
    (a) **Server-side, tamper-proof.** The wait counts EFFECTIVE seconds = Σ speed × real dt on the agent's clock (§7.2
    "Intent wait"): speeds 1 \| 2 \| 4 only; a speed lasts until the next message or 1.5 s without a heartbeat (then ×1);
    ACCEPT needs effective ≥ waitSec, so the floor is waitSec / 4 (15 s of 60). A browser cannot shorten it further: it can
    only ask for ×4, and the agent integrates on its own clock from the moment it receives each message. The state is kept
    in memory and written through to SQLite `intent_waits` (a new table, created like every table at open — no schema
    version bump; an older agent ignores it); `cleanupSessions` drops rows of used / gone intents.
    (b) **Every intent wait, one component.** Approve, Roll back to a snapshot, Reset to starter content and (#96) Empty /
    Restore a volume all use `IntentFlow.svelte` + `intentClock.svelte.ts`. The agent self-upgrade has NO intent wait (typed
    site slug only, §7.5) and the secret reveal countdown is a hide timer, so neither gets the button — any future intent
    wait uses IntentFlow and gets it. Intents for approve / rollback live on the GUI's agent (it coordinates); volume
    intents live on the site holding the volume, so their wait / speed / cancel go through `/sites/{site}/intents/…`
    (forwarded over the mesh; the forwarded session id is `<sid>@<site>`, now also sent on forwarded GETs).
    (c) **UI.** ×2 at once, ×4 after 1.5 s of holding; release / leave / cancel / blur / hidden tab → ×1 at the agent at once,
    the shown speed eases down in 400 ms; badge ×1 / ×2 / ×4 + meter; aria-pressed + aria-live; Space / Enter hold;
    touch-action none, no context menu; the countdown shows the agent's remaining time (re-anchored on every answer).
    (d) **Record.** `FastForwardUse { usedBy, maxSpeed, realSeconds (start → wait complete, 0.1 s), effectiveSeconds }`
    only when speed > 1 was used — in the activity data + summary suffix, `UpdateRun.fastForward`,
    `Proposal.approvedFastForward` (agent-local, omitted when absent: the cloud never sees it), `VolumeOp.fastForward`;
    the Updates proposal detail / list and the update progress show "⏩ fast-forwarded (×4, 18 s real)".
96. **Volume manager in the agent GUI** (user, after emptying a PG16 data volume by hand over SSH because PostgreSQL 17 refused
    it: "you need to give a proper UI for managing the volumes: copy them into a separate path and empty volumes, or view a
    volume, etc.")
    (a) **Where.** Tools → Volumes per site (`*` = every site's volumes, Manage → that site). File operations are tools
    endpoints (`tools/volumes/{v}/file|upload|mkdir|move|delete`, `internal/tools/volumes_write.go`); the long actions are
    per-site agent routes (`volume-ops`, `volumes/{v}/empty|copy|restore|snapshot`, `volume-copies`, `internal/agent/volops.go`)
    because they need intents, snapshots, the driver and the update lock. Contract: `AGENT_API` + `TOOLS_API` + types.ts
    (`VolumeOp` & co.) + `shared/src/volume-ops.ts` (DB images, mismatch patterns, default copy path, path pre-check).
    (b) **Rules.** §7.2 "Volume manager": one action per volume, refused during an update and vice versa, stop users in
    reverse start order / start in start order (also on failure), Empty keeps the folder / file and its owner + mode,
    Empty and Restore need the 60 s wait (⏩) + the volume name typed, Restore always snapshots the volume first, Empty
    optionally (default on), copies only to safe paths, restore archives checked, 6 actions / session / minute, file writes
    120 / minute and locked while the volume is busy; new files take their parent folder's owner. Every write is an
    activity (Appendix B).
    (c) **Stop containers during a copy** defaults ON in the GUI when a user's image is a known database / broker
    (`isDatabaseImage`: postgres, custom-pg, timescale, mysql, mariadb, mongo, redis, influx, clickhouse, elastic, …);
    copying INTO a volume always stops the target's users (their data is replaced).
    (d) **Duplicate as a new data volume = guidance, not an automatic proposal.** Declaring a volume is a configuration
    change (proposal + gate + approval); the data copy is a logged action. The GUI explains: declare it in Config → Volumes,
    approve, then Copy → Another volume. Mixing the copy into an update would need a new update step and a failure story
    for half-copied data — not worth it for a rare action.
    (e) **Copies** live in `<agentHome>/volume-copies/<volume>-<yyyymmdd-hhmmss>[-n]` (or a custom path) and are listed with
    their live size until deleted (deleting removes the data). They are not snapshots: not signed, not in Backups, not
    distributed. Restore uploads (.tar.gz ≤ 2 GiB) wait 1 h in `<agentHome>/tmp/volume-restore/`.
    (f) **Data-directory mismatch helper** on the Status page (§18) with the guided "Copy data volume aside, then empty it";
    the hint is computed by the GUI from the container state + log tail (no new agent endpoint).
    (g) Shipped together with #95 as agent 0.10.2.

97. **Dev tools: service GUIs run by the agent** (user: "capability for service GUIs, like POSTMAN, SQLITE, PG, CSQL, REDIS,
    MQTT. The agent backend will run all the queries on the server, and the agent frontend has the GUIs … Any query run
    from this GUI must be logged to activities … remembering the service cred … preset the dev tool config from template
    and deployment in cloud … But the manual configuration should always be there."). Contract §7.6, `shared/src/devtools.ts`.
    (a) **`SiteConfig.devTools` is omitted when empty.** Agents verify a revision by re-hashing the parsed config; an agent
    < 0.11 drops unknown keys, so a non-empty key changes its hash and the revision is refused. Omitting the empty list
    keeps every existing deployment (no presets) hash-identical; readers use `siteDevTools(sc)` (absent = []). The Go model
    is `DevTools *[]DevToolPreset json:"devTools,omitempty"`; the cloud normalizer deletes an empty `devTools`. A deployment
    that USES presets needs agent ≥ 0.11.0 on that site: the cloud publish dialog warns per site whose last reported
    agent version is lower ("upgrade the agent first, else the site refuses the revision").
    (b) **Presets never hold literal credentials** (SUPERSEDED by §21 #103: a static Value is allowed with a warning).
    password / token are `none` or an expression (normally
    `{{ secret.x }}`); the secret value travels in the revision like every secret and is resolved on the site at use. The
    validator (`validateSiteDevTools`, Go mirror `validate/devtools.go`) checks: slug format + unique per site
    (V_DEVTOOL_SLUG, V_DEVTOOL_DUP), ≤ 50 per site (V_DEVTOOL_MAX), name 1–80, known kind (V_DEVTOOL_KIND), target kind allowed
    for the kind + fields (V_DEVTOOL_TARGET), the container exists on the site (V_DEVTOOL_CONTAINER; disabled container →
    warning), the port name is a port of that container's image (V_DEVTOOL_PORT), sqlite volume declared + safe relative
    path (V_DEVTOOL_VOLUME), no literal credential (V_DEVTOOL_LITERAL), every `secret.x` / `var.y` exists
    (V_DEVTOOL_SECRET / V_DEVTOOL_VAR), expressions parse (E_*), kind-specific auth (V_DEVTOOL_AUTH: postgres username;
    cassandra username and password both or none; redis database "0"–"15"; http basic → username, bearer / api-key → token;
    mqtt clientId ≤ 65535 bytes), option ranges (V_DEVTOOL_OPTION: timeoutSec 1–3600, maxRows 1–10000, keepAliveSec 0–65535,
    basePath starts with "/").
    (c) **Local proposals cannot change presets.** The agent copies `devTools` of the head into a local proposal's
    config; a body whose devTools differ → `V_LOCAL_DEVTOOLS` (checkLocalEdit). Presets are cloud configuration; on the
    site, "Duplicate to edit locally" creates a manual connection — the faster, unlogged-in-config path an operator wants.
    (d) **Diff + Template sync.** diffConfigs emits section `devTools`, path `<site>/devtools/<presetSlug>` (added / removed /
    changed; credentials are expressions, so values are shown). Template sync treats `sites[s].config.devTools` as a keyed
    list by presetSlug (theirs / ours / same / conflict / skipped, like volumes). verify-schema checks the stored shape.
    (e) **Typed confirmation = the connection name** (like the host shell's site slug): destructive SQL / CQL, destructive
    Redis commands, clearing a retained MQTT message. The agent re-classifies on the server (the GUI's warning is advice).
    (f) **Read-only connections** are enforced by the agent: pg `BEGIN READ ONLY`, sqlite `mode=ro` + `query_only`, cassandra
    and redis by the classifier, MQTT no publish, HTTP only GET / HEAD / OPTIONS.
    (g) **History comes from activities** (`devtools.query`, redacted, 4 KiB) — there is no second store with full statements;
    "Saved" is the operator's explicit, unredacted store (per site, shared).
    (h) **HTTP placeholders:** `{{name}}` = environment variable; `{{ <root>.… }}` with an OPM root (var, secret, site, self,
    deployment, containers, container, file) or a filter = OPM expression on the site. Unknown `{{name}}` is sent as is.
    (i) **Dev tool state is not deployment state**: manual connections, saved items, environments and history are not in
    snapshots, `.opmsnap` or `.opmlog`; a rollback does not touch them.
    (j) **Agent reference parity.** The reference (northwind /app/developer) browses the backend's own stores (no connection
    forms, no saved queries, no HTTP / CQL tool). OPM matches its editor depth (CodeMirror 6, schema completion, find /
    replace, grid with type-aware viewers, Redis key tree split on ":" + console, MQTT channel tree + latest payload) and
    exceeds it (connections, presets, tabs, run selection, explain, params, paging, sort / filter / export, saved +
    history, QoS / retain / many subscriptions, HTTP client, Cassandra).
    (k) **Agent GUI placement (nav2, #98):** a DEV TOOLS accordion section after DOCKER with six items (Connections, SQL,
    Redis, MQTT, HTTP client, History) — one SQL workbench for the three SQL kinds keeps the sidebar calm; per-site like every
    module (`*` → choose a site). CodeMirror 6 is a separate embedded vendor file loaded on first use (like xterm), so the
    main bundle carries only the dev-tools UI. `tools.devTools` is also an agent.json SAFE setting (Agent page switch).
    (l) **Logging scope:** refusals that happen before anything runs (403 read-only, 422 confirm / blocked) are not
    activities; connection failures are. Schema introspection and the Redis key browser reads are not logged; every
    command / statement / request / publish / subscription is. The client-chosen queryId and the connection's resolved
    credentials are masked like secrets.
    (m) Shipped as agent 0.11.0.
98. **Agent GUI navigation v2 (nav2)** (user, with a screenshot of the sidebar DEPLOYMENT · RELEASE · PER SITE [picker] ·
    TOOLS · TRANSFERS [picker] · AGENT: "too overwhelming … show the site dropdown on top … the user doesn't care whether
    this tab is site specific or not … every tab in a collapsible accordion … DOCKER: container, volumes, docker, network;
    monitoring, terminal and logs container-specific; on container click open a proper page instead of a side drawer …
    AGENT: agent, debug, send, receive, internet … DEPLOYMENT: sites, variables, secrets, files, diff, backups … SITE:
    status, activities, config"). §18 Layout / §18.T / §18.A rewritten; agent GUI only, no API change; agent 0.10.3.
    (a) **Every module under `/_opm/app/{site|*}/…`, deployment-wide ones too** (Variables, Diff, …): one URL scheme, the
    selection survives every click and a shared link opens the same site. Deployment-wide pages ignore the segment (or
    filter / highlight by it). `/_opm/app` and the old flat URLs redirect to this tab's selection (sessionStorage, per tab
    — two tabs can look at two sites), else this site (`GET /_opm/api/info`). Static old routes (`/_opm/app/variables`, …)
    win over the `[siteSlug]` segment, so a site literally named `variables` / `sites` / `updates` … reaches its pages
    with the module in the path only (`/_opm/app/variables/status` works; the bare `/_opm/app/variables` is the redirect).
    (b) **Accordion default** = only the section of the current page open (calm); the user's open / closed choice is
    remembered in localStorage and wins, except that opening a page of a collapsed section opens it. Collapsed headers
    show the badges of their items (one count, or "•" when several).
    (c) **Host shell** = a tab of Agent › Agent (it is the server of the agent, not a container); **host monitoring** →
    Site › Status (one site; `*` keeps the per-site summary cards); the **agent log** stream → Agent › Debug › Live agent
    log; the **edge access log** → Docker › Network (it is network traffic). The global Logs / Terminal / Monitoring pages
    and the inspect drawer are removed (old URLs redirect, §18 Layout).
    (d) **Container Logs stay mergeable**: the tab is scoped to the container, "Merge with…" adds others (merged by time) so
    the old multi-container view is still reachable. Terminal sessions / transcripts listed on a container tab are that
    container's; host ones are on the Host shell tab.
    (e) **Config sections are tabs of one Config page** (`ConfigTabs`), with the old tree's count / edited / error marks; the
    container editor is one component (`lib/editors/ContainerEditor.svelte`) used by `config/containers/{c}` and the
    container page's Config tab (same working copy).
    (f) **Diff replaces Updates and Sites config.** Diff = the newest open proposal against the head + gate + approve +
    progress + failed containers + history; rollback stays in Backups (snapshot planner). "Change the proposal" cannot
    edit a cloud proposal in place (its parent is the head and cloud-only fields cannot be changed locally): local edits
    become a NEW local proposal from the head that supersedes the open one (§21 #10) — the page says so before Create.
    Sites config's site cards became Config with `*`.
    (g) Tests: `tests/nav2.spec.ts` (accordion mouse + keyboard + remembered, global selector drives modules + URL +
    per tab, `*` "Choose a site" states, Diff, redirects, container Config tab, screenshots light / dark) +
    `nav2-mobile.spec.ts` (390 px drawer); the tools / config / mobile / roundd / roundi specs follow the new IA.
99. **Dev-tool presets read another container's env: `containers.<containerSlug>.env.<NAME>`, field `devtool`** (user, on the
    cloud "New PostgreSQL preset" drawer for container `postgress` port `service`: "in the expression i am not able to select
    the env variable, of container."). Contract: `expr.ts` (`ExprField 'devtool'`, `CONTAINERS_ENV_FIELDS`, `ContainerPeer.env`,
    `PeerEnv`), `validate.ts` (`siteContainersEnv`, `siteDevtoolExprContext`, `DEVTOOL_EXPR_FIELD`, `containerEnvIsSecret`,
    `containerEnvNames`), `devtools.ts` (`suggestDevToolEnv`, `DEVTOOL_ENV_HINTS`, `devToolContainerEnvRefs`,
    `containerEnvExpr`, `containerEnvOfExpr`, `DEVTOOL_CONTAINER_ENV_MIN_AGENT`, `DevToolsInfo.containers[].env`); vectors
    `expr-vectors.json` (field devtool + `site-peers.containers.postgress.env`) and `devtools-validate-vectors.json`; Go
    mirror `expr.FieldDevtool`, `validate.SiteDevtoolExprContext`.
    (a) **Value = the FINAL env of that container** (image default ← container env, expressions resolved with
    `resolveContainerConfig` — the value the container itself receives). Secret-valued (secretRef / `secret.*`, also through
    `container.env.<X>`) → the result is secret (masked, never logged; the agent adds it to the redaction set like any
    resolved credential). A failed value re-raises its code at the reference; no value → E_NULL (use `| default(...)`).
    (b) **A dedicated field `devtool`** (roots var, secret, site, self, deployment, containers; secrets allowed) for every
    preset value, the agent's manual connections and the HTTP client's OPM placeholders. `containers.<c>.env.*` is allowed
    ONLY there: env / imageEnv / file / mountPath / edge … → E_NOT_ALLOWED with "only allowed in dev-tool presets". A
    container value can therefore never read another container's env — no cross-container cycles, no ordering between
    containers. `checkDevToolExpr` = `checkExpr(src, 'devtool')`.
    (c) **Not a literal.** Referencing a container env satisfies "presets never hold a literal password" (§21 #97 b, since
    relaxed by §21 #103): the
    value lives in the container config (secretRef → secret). A password / token from a plain env is allowed with a
    WARNING `V_DEVTOOL_PLAIN_ENV` (the value is readable in the config); unknown env name → `V_DEVTOOL_ENV`, unknown
    container → `V_DEVTOOL_CONTAINER` (errors). Images missing from the catalog skip the name check (manual connections).
    (d) **Cloud editor.** Completion is hierarchical: from an empty `{{` the list shows roots / groups (`containers.` →
    `containers.postgress.` → `env.` → names with their resolved values, `••• secret`); short lists (≤ 14 paths) stay flat
    under their group rows; a non-empty token also matches by substring (`{{ POSTGRES` finds the env). The popup flips above
    the input and stays inside the viewport. With a container target the drawer shows chips "Use env POSTGRES_USER of
    postgress" (PostgreSQL: POSTGRES_USER / PGUSER / POSTGRESQL_USERNAME …, POSTGRES_PASSWORD / PGPASSWORD …, POSTGRES_DB /
    PGDATABASE …; Redis: REDIS_PASSWORD / REDISCLI_AUTH …, REDIS_USERNAME; Cassandra: CASSANDRA_USER / CASSANDRA_PASSWORD /
    CASSANDRA_KEYSPACE; MQTT: MQTT_USERNAME / MOSQUITTO_USERNAME …, MQTT_PASSWORD …, MQTT_CLIENT_ID; HTTP: BASIC_AUTH_* /
    API_TOKEN …; then name patterns) and the password / token control gets a fourth mode **Container env** (picker of
    that container's env, secret / plain badges). Previews are on for username / database / client id / expressions.
    (e) **Old agents.** Agent 0.11.0 rejects the new path (E_UNKNOWN_PATH at staging → the revision is refused). The cloud
    warns per site whose last reported agent is < 0.11.1 when a preset of the site uses `containers.*.env`
    (`devToolsAgentNeed` / `oldAgentSites`, Dev tools page + Diff banner + publish dialog); it never blocks.
    (f) **Agent GUI.** `DevToolsInfo.containers[].env` = names + secret flag (never values) so the manual connection form
    completes `containers.<c>.env.<NAME>` (field devtool); the values resolve on the site.
    (g) Shipped as agent 0.11.1.

100. **Transfers = one module; Send › Browser | Path with a server filesystem picker; store-and-forward both ways**
    (user, screenshot of `/_opm/app/site-01/send` "Send to the cloud" with "Send from server path" + "Send from browser" and
    the subtitle "Uploads wait for a direct or relay route — never through the browser tunnel": "lets make send and receive
    into one module where right side i see receive end and left side send end. when click on send from agent server, then
    ask for 2 options "browser" | "path" on path selection show the complete fs tree of the server and let user pick
    something. (now it dont matter if there is someone active on reciver end or not, clound will regardless store it) on
    send from server its simple always pick from browser. (now it dont matter if there is someone active on reciver end
    or not, agent will regardless store it)"). Agent 0.11.2.
    (a) **One module per GUI, same orientation from the viewer's side.** Agent GUI: Agent › **Transfers**
    (`/_opm/app/{site}/transfers`, one site; `*` → "Choose a site"), left = Send (this site → cloud), right = Receive
    (cloud → this site); the cloud deployment: `…/sites/{site}/transfers`, left = Send (cloud → site), right = Receive
    (from the site). Columns stack on narrow screens, Send first. Old `…/send`, `…/receive` (and nav2's
    `/_opm/app/sites/{s}/send|receive`) redirect; the cloud sites table has one Transfers link.
    (b) **Send = one button → Browser | Path.** Browser = a file from this computer (agent: stored on the site, then
    queued). Path = the server filesystem picker (one level per request, lazy). In the cloud, sending to a site is ALWAYS a
    browser upload (the cloud has no filesystem to pick from).
    (c) **Files only.** `sendFromPath` never supported folders; the picker says "Folders cannot be sent — pick a file (or
    make a .tar.gz on the server first)" and the API answers 422 for a folder. Packing folders server-side would need a
    size estimate, a temp copy up to 2 GiB and a format choice — not worth it while the host shell exists.
    (d) **"Wait for a route" never meant a live receiver.** It meant the site's own way to the cloud (direct, or relay
    through a peer) — uploads cannot use the browser tunnel (tunnel request bodies ≤ 4 MiB; parts are 8 MiB). Nothing ever
    needed a person on the receiving side; what was missing: (1) `sendFromBrowser` / `sendFromPath` waited up to 20 s for
    `POST /v1/agent/transfers` → now they return at once (`local-<id>`, delivery `queued`) and the loop does the rest with
    per-transfer backoff 15 s × 2ⁿ ≤ 5 min (an expired cloud upload restarts as a new cloud transfer from the same file);
    (2) the GUIs showed no queue state → `Transfer.delivery` (agent API only: phase queued | uploading | stored | pending |
    downloading | on-site, bytesDone, attempts, lastError, lastAttemptAt, nextAttemptAt; in memory — a restart shows 0
    bytes until the next attempt re-reads the cloud upload); (3) the cloud could not tell "waiting for the site" from "on
    the site" → the agent reports `fetched` (`AgentTransferStatusRequest.status`) and the cloud sets `Transfer.fetchedAt`
    (no status change, no activity — a machine event; saved / downloaded set it too). Agents < 0.11.2 never report it, so
    their files read "stored · waiting for the site" until saved / downloaded; a 0.11.2 agent reports files it already had.
    (e) **Server filesystem picker (`GET …/transfers/fs`) — reach and safety policy.** The agent is a host binary
    (systemd, root on a standard install), not a container, so `/` is the host's real root; there are no mounts to map.
    Reads: session (like every GET), not logged (like the volume tree); the send is the action: actor token + activity
    `transfer.sent { source: 'path', sourcePath }`. Listing shows everything the agent can see (an operator with the host
    shell can see it anyway) with `readable` (access(2)) and `blocked`; unreadable directories answer 403 "the agent (user
    X) cannot list …" inline instead of breaking navigation. **Never sent** (403, checked on the requested path AND the
    resolved symlink target, shared rule `hostFsBlockReason` TS = Go, vectors `hostfs-vectors.json`): the agent home
    (site key, opm.db, certs, secrets, blobs, transfers), agent.json, the install-token file, `/proc` `/sys` `/dev` `/run`,
    `/etc/shadow` `/etc/gshadow` (+ `-` backups) `/etc/sudoers` `/etc/security/opasswd`, `/etc/ssh/ssh_host_*_key`, and any
    non-regular file. Why a denylist and not an allowlist: the point is "any file on the server" (logs, dumps, configs in
    arbitrary places); the denylist keeps MACHINE secrets out of the cloud, where every deployment.transfers.read user can
    download them. Limits: one level per request, 5000 entries (`truncated`), names read ≤ 50 000, hidden files behind a
    toggle (`hiddenCount`).
    (f) Unchanged: token-only downloads (never public objects), 2 GiB, 30-day retention / expiry, sha256 checks, the
    activities (`transfer.sent` agent data gains `source`, `sourcePath`), permissions (deployment.transfers read / write).
    (g) Tests: shared transfers.test.ts (vectors, state labels, contract); Go TestTransferToCloudStoreAndForward (cloud
    blocked → queued + retries, unblocked → stored with nobody calling the cloud), TestTransferToSiteFetchedAfterOnline
    (agent stopped while the cloud gets a file → started → fetched + fetchedAt), TestHostFsBlockVectors,
    TestHostFsListAndSendPolicy; backend emulator tests for `fetched`; both GUIs' Playwright specs for the merged page,
    Browser | Path choice, picker and redirects.
    (h) Shipped as agent 0.11.2-10090048 (built in a clean worktree: HEAD + #99 + #100, without the 0.12.0 work in progress).
101. **A volume's content is its own file tree; persist-to-cloud volumes; Files are static and read-only** (user: "currently
    i have to first create files in files then create prefilled volume and point all files in the volume. i dont like that
    approch … directly show me a full file editor … file tree … create edit move files … let me set the volume as persist to
    cloude … nodered flow or other things that has client's integreation code … persistent volume + cloud based persistence
    instead of git repo … i dont want to refrence files from "files" into prefilled volumes. Files are always suppose to be
    static and readonly! never to be mounted on writable volumes."). Contract `shared/src/volume-content.ts` (+ types,
    validate, diff, cloud-api), vectors `shared/testdata/volume-content-vectors.json` (Go `internal/volcontent`).
    **Supersedes** #79 (starter content "files" / "file" from Config files), the round-E "Starter content" rule that a seed
    entry's content is a Config file (§2.2), the "Recommended for Node-RED" advice to seed / mount `settings.js` from a Config
    file inside the `/data` volume (§2.2, #83 e), and FILE_VOLUME_RENAME_WARNING's "seeded with the files" wording.
    (a) **Own content.** `SiteVolume.seed` kinds `tree { entries [{ path, sha256, size, mode, render }], dirs }` (folder
        volumes) and `single { sha256, size, mode, render }` (single-file volumes). The bytes are content-addressed blobs
        `files/<hex>` in Storage (POST /v1/blobs or a multi-part upload, purpose blob — the same store as File contents); the
        config holds only the manifest, so revisions, diffs and template sync stay small and hash-stable. Canonical tree: entries
        sorted by path (UTF-16, like JCS), `dirs` = only empty folders; treeHash = "sha256:" + hex(sha256(JCS(tree))). Paths:
        relative, "/"-separated, no "." / ".." / empty segments, no NUL / control characters / "\", ≤ 1024 bytes, names ≤ 255
        bytes, not `.opm-seeded`. Modes "0644" style. Limits: a file ≤ 50 MiB; starter content ≤ 1000 files, ≤ 1000 empty
        folders, ≤ 200 MiB; the editor opens text ≤ 2 MiB (bigger / binary: size + download).
    (b) **render per entry** (`none` default for new files; `expressions` = rendered ONCE PER SITE when written, roots of a
        File): the user's real Node-RED `flows.json` holds `{{ containers.northwind-backend-worker.host }}` … — dropping rendering
        would have broken it. Persisted versions are always literal (`none`): they are what the site really has.
    (c) **Files are static and read-only.** A legacy seed referencing Files → `V_VOLUME_SEED_FILE_REF` (save error in template /
        draft / publish; accepted only in agent mode, i.e. in revisions published before 0.12). A writable File mount →
        `V_READONLY` (unchanged). NEW `V_FILE_IN_WRITABLE_VOLUME`: a File mount whose effective path equals or is inside a
        WRITABLE volume or host mount of the SAME container is an error (save level; publish level when an expression is involved;
        a warning in agent mode) — "Files are static and read-only and are never mounted on a writable volume: put it into the
        volume's own content, mount it outside, or mount the volume read-only". Secrets are not affected.
    (d) **Seeding rules unchanged** (round E): written at APPLY before containers start, only into a new / empty volume never
        seeded on this site, marker `.opm-seeded` + kv, never re-seeded, "Reset to starter content…" re-applies on purpose.
        **Placeholder transition** (agent ≥ 0.12): when the previous head mounted a File inside a writable volume and the new
        config moved it into that volume's content, the 0-byte file Docker created as the bind mountpoint is filled ONCE with
        the entry (activity `volume.placeholder-filled`); a 0-byte former mountpoint is never user data. Without this the
        migration of Node-RED `settings.js` would leave existing sites with an empty settings file.
    (e) **Persist to cloud** (`SiteVolume.persist { exclude, maxBytes, auto }`, folder volumes, OMITTED when off): the agent
        snapshots the live tree (excludes: gitignore-lite, default `node_modules/ .npm/ .cache/ *.log`; ≤ 10000 files; ≤ maxBytes
        1–200 MiB, default 50 MiB; files > 24 MiB / symlinks / special files / bad names skipped and listed) into versioned cloud
        content `deployments/{id}/volumeContent/{site}:{volume}` + `versions/{versionId}` (seq; origin snapshot | edit | restore;
        the tree JSON is the blob `files/<treeHash hex>`). **When:** on demand (agent GUI "Snapshot now"; cloud "Snapshot now" =
        a request in the cloud view, answered once), automatically on change (a cheap fingerprint every 60 s; snapshot after
        120 s without further change; at most one auto snapshot per 15 min per volume), before an apply when live changed, and
        after every apply. Same tree as the last snapshot → nothing new. Every version is kept (the daily job prunes only AUTO
        snapshots beyond the newest 200 versions; blobs stay); every snapshot / edit / send / apply is an activity.
    (f) **Cloud edit** = the same file editor on the latest version → a new version (`baseVersionId` must be the latest, else
        409 "a newer version arrived"; paths under the excludes are refused: they could never come back in a snapshot).
        Restore = an older version copied as the new latest. "Use as starter content" copies a version into the draft.
    (g) **Push back = a dedicated "Send to site", approved on the site — not a revision.** Volume content of a persist volume is
        DATA (like a volume restore), not configuration: a configuration rollback must not revert flows edited later on the
        site, a revision would run the whole multi-site LOCK → SNAPSHOT → APPLY update for one folder, and the conflict check
        needs the live tree at apply time. It keeps the gate model: nothing reaches the live volume until a PERSON on the site
        applies it in the agent GUI (intent `volume-content-apply`, 60 s, fast-forwardable #95), like the volume manager's
        Restore (#96). Send (perm deployment.config.publish) stores a pending push {version, base = the newest SNAPSHOT
        ancestor, restart, note}; a new send supersedes it; it can be cancelled.
    (h) **Conflict safety.** The agent plans base (the snapshot the edit started from) / live / cloud per file: live = base →
        fast-forward; live = cloud → nothing to do; otherwise a 3-way choice — Overwrite (cloud wins), Keep live (nothing written,
        push kept-live, live snapshot uploaded), or Per file (cloud-only changes taken, live-only kept, every conflict decided
        with a per-file diff). An unknown base makes every difference a conflict. The apply refuses when live changed since the
        plan (re-plan), takes a Backups snapshot of the volume first, optionally restarts the containers that mount it (checkbox,
        default from the send), never touches excluded paths or `.opm-seeded`, and uploads an after-apply snapshot.
    (i) **Store-and-forward both ways.** Pending pushes and snapshot requests ride the cloud view (agentEpoch+1), so the site
        can be offline while the cloud edits / sends; snapshots queue in the agent (SQLite + blob store) and upload with
        backoff when the cloud is reachable. Agent downloads stay site-key signed (`getAgentBlob` now also serves seed tree
        blobs and the trees + blobs of a pending push and its base).
    (j) **GUIs.** Cloud: a full-page volume content workspace in template + deployment scope (tree with create / rename / move
        by drag-and-drop and "Move to…" / delete / duplicate, upload of files and folders, CodeMirror by extension, mode +
        render, binary = size + download), persist settings (exclude preview), and for persist volumes a "Cloud copy" tab
        (state badge, history, Save as new version, Restore, Send to site…, Cancel, Snapshot now); Diff lists per-file content
        changes (`<site>/volumes/<v>/content/<path>`, text diff). Agent GUI: Docker › Volumes sync state (in sync / live changed
        / cloud ahead / conflict / no snapshot yet / waiting to upload / error), "Snapshot now", "Apply cloud version" (plan
        dialog + per-file diff + IntentFlow); Config › Volumes shows content read-only (local edits cannot change content or
        persist: `V_LOCAL_VOLUME_CONTENT`).
    (k) **Migration** `backend/scripts/migrate-h.ts` (`migrateVolumeFileSeeds`): templates and deployment drafts (+ the
        srcTemplate snapshot / base), never revisions (immutable; old agents keep applying them; agent 0.12 still decodes legacy
        seeds). Each legacy File seed takes the File's blob (same sha256, size, render; mode 0644); a File mounted inside a
        writable volume moves into that volume's content at the relative path (its image-volume row → Not mounted; an extra
        mount is removed); a File nothing references any more is removed, a File still mounted read-only elsewhere is kept.
        Idempotent; one `system.migration` audit entry.
    (l) **Agent compatibility.** `persist` is omitted when off and legacy kinds keep their bytes, so existing revisions hash
        as before. A revision with `tree` / `single` / `persist` needs agent ≥ 0.12.0 (`VOLUME_CONTENT_MIN_AGENT`); an older
        agent refuses it (unknown seed kind) — the cloud warns per site (publish dialog, Send), like dev-tool presets.
    (m) Shipped as agent 0.12.0.

---------------------------------------------------------------------------------------------------------------------

102. **One form per entity in both GUIs: the shared `ui/` package (`@opm/ui`, alias `$opm`)** (user: "go through other docker
    GUI and reanalize and see if there is any way to make the GUI a bit better, and consistent, i dont want to learn 4 diff
    forms, i should be able to see same form in clound on create/edit and some thing in read/edit in agent."). Analysis,
    matrix, specs and screenshots: `docs-v2/gui-consistency.md`.
    (a) **Research** (Portainer, Docker Desktop, Rancher, Dockge, Yacht, Coolify, CapRover, Komodo, Lazydocker): adopted —
        one form component per entity with a create / edit / read mode, read = the same form disabled (Rancher `mode`,
        Komodo `disabled`); a change bar that shows only with unsaved edits (count · Discard · Review · Save); a diff before
        anything that changes running things; key/value rows with bulk `KEY=VALUE` paste (never a lossy text mode); section
        navigation inside long forms; inline field errors + one summary, Save disabled while errors; searchable pickers;
        ONE "locked here" marker. Avoided — separate create vs "duplicate / edit" forms that drift (Portainer), one-way YAML,
        a diff in one path only, a different read-only layout per screen.
    (b) **Audit** (before): ~53 form / view implementations for the entities both GUIs show (container + env / ports /
        mounts / health check, volume, persist settings, edge ports / host / route / headers / body size / nginx import /
        Test a URL, variable + value input, secret, file definition, site settings, dev-tool preset / connection +
        credential, expression input, diff tables); 6 validation-display styles in the agent, 4 in the cloud; 8 read-only
        techniques; native number inputs / checkboxes left in some forms. Bug found: the agent `DiffTables` omitted the
        `devTools` section (preset changes invisible in review, Diff and rollback) — fixed.
    (c) **Architecture.** `on-premise-manager/ui/` (package `@opm/ui`, Svelte 5 SOURCE, no build): `src/primitives`
        (Field, Toggle, SegmentedControl, NumberStepper, Select, Combobox, MultiSelect, ChipList, SlugInput, ExprInput,
        ValueText, Issues, FieldIssues, FormPage, FormSection, ChangeBar, SharedBadge, InfoTip, Btn, Icon), `src/forms/<entity>`
        and `src/styles/{tokens,base}.css`. Both apps alias `$opm` → the source (`frontend/svelte.config.js` `../ui/src`,
        `agent/gui/svelte.config.js` `../../ui/src`; vite `resolve.dedupe: ['svelte']`, agent `server.fs.allow`, frontend
        `optimizeDeps` the `@opm/shared/*` subpaths); each app's Svelte compiler builds them, so the agent stays one
        embedded bundle. Listbox popovers use the browser top layer (`popover="manual"`), which works inside the agent's
        native `<dialog>`s and the cloud's portaled drawers alike. `ui/tsconfig.json` + `npx svelte-check` type-check the
        package on its own.
    (d) **ConfigHost** (`ui/src/forms/host.ts`, Svelte context): the ONE adapter between a form and an app — working
        config, deploymentId, `image()` / `service()` / `services()` / `versions()`, `validate(candidate)`,
        `secretIsSet()`, not-found pages, `lock(path)`, `readOnly`, file texts. Cloud: `config-editor/configHost.ts` from
        the ConfigScope (TemplateShell / DeploymentShell). Agent: `lib/configHost.ts` from the local-edit draft
        (`routes/app/+layout.svelte`) with `agentLock()`: containerSlug / serviceSlug / version → "Cloud only — containers
        are added, renamed and re-imaged in the cloud console"; variable / secret / file definitions, the site label and
        dev-tool presets → "Cloud only — change it in the cloud console (V_LOCAL_DEFINITION)" (`ui/src/forms/locks.ts`).
    (e) **One form per entity, used by both apps** (`ui/src/forms`): container (`ContainerForm` + `HealthCheckForm`;
        FormPage with sections 1 Service, version and name · 2 Ports · 3 Volumes · 4 Environment · 5 Runtime · 6 Resolved +
        jump links, summary, validation with one-click fixes); edge (`HostForm`, `EdgePortsForm`, `RouteForm`, `NginxImport`,
        `TestUrl`, request / response header editors, `ByteSizeInput`); `VariableForm` + `VariableValueInput`; `SecretForm`
        + `SecretValueInput`; `DevToolForm` (variant preset | connection) + `CredentialField`; `VolumeForm`, `PersistForm`,
        `LocationField`, `SeedView`; `SiteSettingsForm` (+ `HostField`, `NotFoundPagePicker`); `FileForm`;
        `TransferSendForm`. Every form takes `mode: create | edit | read`; read = the same markup with every control
        disabled; edit vs read differ ONLY by the disabled state and the actions. Locked fields keep their control,
        disabled, with a lock icon and the reason (Field `lock`). The cloud's `ui/Field` and `ui/Toggle` render the shared
        ones, so root forms use the same field markup.
    (f) **Same container in both apps** (the §17.0 rule): host = wide drawer; route, container, nginx import = page;
        variable / secret definition, volume, dev-tool preset / connection, file definition = drawer; set secret value,
        transfer send, service = modal; deployment create AND edit = one drawer (one field order: Label, Logo, Source*,
        Template*, Tags, Managers; *create only, shown locked on edit — "changes arrive via Template sync"). Service create
        and edit = one `ServiceForm` modal.
    (g) **Agent read / edit = the cloud form.** Variables and secrets rows open the cloud drawers (definition locked, value
        editable on variables); files get a Details drawer (`FileForm`, locked); dev-tool presets open `DevToolForm` read;
        manual connections use the same form (variant connection: no preset id, extra "Stored value" credential mode — §21
        #103: "Value", in both variants —, and
        the cloud's Container env mode); the volume drawer is `VolumeForm` (starter content + persist cloud only, shown
        read-only with "Edit in the cloud console"); site settings = `SiteSettingsForm` (label locked).
    (h) **Behaviour unified** (the better side won): Published toggle on ports (model unchanged: no host port = not
        published); the cloud's image-env table (Default · This container = value | secret) in both; bulk paste of
        `KEY=VALUE` / `export KEY=VALUE` / `KEY: VALUE` lines into an extra-env name cell adds rows (comments / blank lines
        skipped, quotes stripped; secrets never overwritten); mounts = one grouped searchable picker with
        "+ New data volume…" (each app opens its own volume drawer); depends = multi-select + implicit / suggested chips;
        nginx import default Append (the cloud defaulted to Replace); Uploads streaming preset = 1 GiB body; not-found page
        = thumbnail picker; JSON variables = textarea; folder / file kind problems only as validator issues with fixes.
    (i) **Change bar** (`ChangeBar`): the agent local-edit bar and the cloud site editor (Discard · Save template / Save
        draft moved from the page header to the sticky bar; test ids `discard`, `save-site` kept); the site label is edited
        only in Settings (the header "Edit label" link opens it; the label modal is gone).
    (j) **Design tokens**: `ui/src/styles/tokens.css` is THE token file of both GUIs; `ui/src/styles/base.css` holds the
        shared classes (buttons, fields, inputs, switches, cards, badges, banners — the cloud tone names and the agent's
        ok / warn / bad / info are aliases —, listbox popovers, issue codes). Each app keeps only extras in its own tokens
        file (cloud: z-scale for portaled overlays + ANSI on --code-bg; agent: bundled fonts, in-flow z-scale, dark log /
        ANSI palette). No token value changed.
    (k) **Deleted duplicates**: cloud RouteEditor, NginxImport, ResponseHeadersEditor, SizeInput, TestUrl, edge.ts,
        VariableValueInput, devtools/CredentialField, HostField, LocationField, PersistCard, NotFoundPagePicker and the
        inline markup of ContainerEditor (1400 → ~330 lines); agent EdgeTrace, RequestHeaders, ResponseHeaders,
        ByteSizeInput, VarInput, devtools/CredentialField, SeedView and the inline markup of ContainerEditor (990 → ~150),
        EdgeEditor, VolumeModal, route / import / settings pages. Net −10.5 k lines in the apps, +6.2 k shared form lines.
    (l) **Tests**: ui svelte-check, frontend + agent GUI check 0 / 0; frontend mock Playwright 172 pass / 13 skip; agent GUI
        Playwright 175 / 175; side-by-side evidence specs `frontend/tests/gc-screens.spec.ts` + `agent/gui/tests/
        gc-screens.spec.ts` (OPM_GC_SHOTS=1) → `docs-v2/assets/gui-consistency/{cloud,agent,pair}-<entity>-<light|dark|390>.png`
        for container, volume, edge route, variable, secret, dev-tool preset. Parallel Playwright runs need their own
        `--output`, port (`OPM_E2E_PORT`) and, in the agent, mock dir (`OPM_MOCK_DIR`); concurrent agent builds share
        `.svelte-kit/output`. Agent **0.13.0** ships the new GUI (no Go change).

103. **Static credential Values in dev tools + hash filters (`sha256`, `sha512`, `mosquitto_passwd`)** (user: "in dev tools
    pass for all connecters seems mandatory from secreats or expression etc, also allow static value typing." and "can the
    Mosquitto password file be generated with an expression function?" — the user's File `mqtt-pwfile` of template
    "Single" holds a hand-made line `northwind:$7$101$<salt>$<hash>`). SUPERSEDES §21 #97 b ("presets never hold a literal").
    (a) **Value mode.** The shared `CredentialField` (ONE control, §21 #102) offers None · Value · Secret · Container env ·
        Expression for the password / token of EVERY kind (postgres, cassandra, redis, mqtt, http; sqlite has no
        credential) in cloud presets AND agent manual connections. Value = `{ kind: 'literal', value }`: masked input with a
        show / hide toggle. Presets keep it IN THE CONFIG in plain text → non-blocking warning V_DEVTOOL_LITERAL "stored in
        plain text in the config — anyone who can read the config sees it; use a secret for production" (the drawer shows
        the same text as an amber note; an empty Value → V_DEVTOOL_AUTH). Manual connections keep it on the agent
        (`secret_json`) as before. An expression without `{{ }}` or without a reference is treated the same way (warning).
    (b) **Never in clear outside the form.** Agent: `presetDef` treats a preset Value like a stored literal — every API read
        returns MASK (`maskedAuth`), `maskConfig` masks it in every config view (a local edit that sends it back masked keeps
        the head's preset byte for byte, `unmaskPresetLiterals`), Reveal (`revealCredential`, logged, 60 s) and Duplicate
        work for presets, and the resolved value joins the activity / log redaction set (`NewMasker` with the connection's
        credentials) — verified on real Mosquitto (`devtools.mqtt-published` topic `t/••••••`). Cloud: `diffConfigs` and the
        Template sync plan show it as `••••` (compared raw; a change of only the value gets the note "static password / token
        value changed (hidden)"); the Go diff gained the devTools section with the same masking; the audit log already
        masks everything under a `password` / `token` key.
    (c) **Old agents.** Agents < 0.13.1 refuse a revision with a preset Value (their validator made V_DEVTOOL_LITERAL an
        error at staging, and their resolver refused preset literals) and with the new filters (E_UNKNOWN_FILTER). The cloud
        warns per site, never blocks (`DEVTOOL_LITERAL_MIN_AGENT`, `FILTER_MIN_AGENT`, `siteFilterAgentNeed`; site Dev
        tools page, Diff banner, publish dialog; the Diff page loads rendered File sources to find filters in files).
        verify-schema accepts a preset literal when it is a non-empty string.
    (d) **Hash filters** (`shared/src/hash.ts`, pure synchronous TS — WebCrypto is async; Go crypto/sha256, sha512, hmac,
        pbkdf2): `sha256` / `sha512` = lower-case hex of the UTF-8 bytes (every field). `mosquitto_passwd(format=7,
        iterations=101)` = the Mosquitto password-file hash, verified against the Mosquitto source (2.0.18
        `src/password_mosq.c` / `apps/mosquitto_passwd/mosquitto_passwd.c`: PBKDF2-HMAC-SHA512, 12-byte salt
        `SALT_LEN`, 64-byte key, `PW_DEFAULT_ITERATIONS` 101, `$%d$%d$%s$%s` with OpenSSL base64 without newlines;
        `$6$` = SHA512(password ‖ salt)). Mosquitto 2.1.x (`libcommon/password_common.c`) writes 64-byte salts and 1000
        iterations but still verifies 12-byte salts; the 2.0 broker verifies ONLY 12-byte salts
        (`security_default.c`: `salt_len == 12`) — so the filter writes 12 bytes and 101 iterations by default (the same
        shape as the user's existing line; both brokers accept it). Format 6 for Mosquitto 1.x; iterations 1–10000.
        Only where secrets are allowed (env, file, devtool), else E_NOT_ALLOWED at the filter name; the filter-name lexer
        now accepts `_`.
    (e) **Deterministic salt** = first 12 bytes of HMAC-SHA256(key = password, msg = "opm-mosquitto-salt" NUL deploymentId
        NUL siteSlug). Why: a rendered File is hashed and compared at every staging; a random salt would change the file at
        every render → a new file hash, a recreate / restart of mqtt at every apply, and endless Diff churn. Keyed by the
        password (the salt reveals nothing the hash does not), varied by deployment + site (a table precomputed for one
        deployment / site is useless for another). Tradeoff, accepted and documented: two users with the SAME password on
        the SAME site get the same line (an observer of the file learns they share a password) — use different passwords
        per user. Alternatives rejected: a random salt (churn above), a salt from the username (the filter does not see
        it; arguments are literals), a salt only from the password (identical lines across every OPM install).
    (f) **Secret propagation.** The block of a secret input stays secret after any filter: masked `••••` in cloud previews
        (the cloud hashes its `•••` stand-in or shows the deferred placeholder), rendered with the real secret on the site;
        rendered bytes are never logged / uploaded / snapshotted (§10.6). **File mode:** a File that holds a secret in clear
        stays 0400; one whose secrets ONLY go through `mosquitto_passwd` (`ResolveResult.secretHashed`, `HASHING_FILTERS`;
        Go `driver.RenderedFileMode`) is written 0444 — Mosquitto (2.0 and 2.1) opens `password_file` AFTER dropping to
        uid 1883, so a root-owned 0400 file is "Unable to open pwfile" (verified on both images; the user's current
        hand-made file has no secret and is 0444 already). Mosquitto logs "world readable … owner is not mosquitto" warnings
        for it, as it does today for the user's file; the content is a salted PBKDF2 hash, not the password.
    (g) **Not added (options):** `bcrypt` / `htpasswd` (nginx / Caddy basic auth) — needs a deterministic-salt bcrypt in TS
        and Go (Go's x/crypto/bcrypt only salts randomly); `hmac_sha256(key)` — the key would be a literal in the config;
        `md5` / `sha1` / Apache `$apr1$` — weak. Each can be added like `mosquitto_passwd` (FILTER_MIN_AGENT bump).
    (h) **Tests:** shared `hash.test.ts` (vs node:crypto 0–300 bytes, PBKDF2, the REAL mosquitto_passwd lines of
        `testdata/mosquitto-passwd-vectors.json` — 2.0.18 + 2.1.2, `$7$` + `$6$`), `static-value-103.test.ts`, expr vectors
        (+24 cases, +17 checks, context `mqtt-site`), devtools-validate vectors (warning severity); Go `TestVectors`,
        `TestMosquittoPasswdVectors`, `TestDockerMosquittoPasswd` (live `mosquitto_passwd` in eclipse-mosquitto:2.0.18 and
        :2), `TestDockerMosquittoPasswordFile` (the rendered file is the password_file of a real 2.0.18 / 2.x broker; MQTT
        presets with `{{ secret.mqtt-password }}` and with a static Value connect, a wrong password is refused, the Value is
        MASK in reads, revealable, duplicated, redacted in activities), `TestDevToolsStaticValueMasked` (Go diff),
        `TestPresetStaticValueMaskRoundTrip`, `TestSecretHashed` (0444 rule); backend `26-devtools-presets` (#103 case); frontend
        `devtools-value-103.spec.ts`; agent GUI `devtools.spec.ts` (#103 case).
    (i) **The user's file**, paste-ready (create the secret `mqtt-password` with the plain password first):
        `northwind:{{ secret.mqtt-password | mosquitto_passwd }}`; the dev-tools MQTT preset password = Secret
        `mqtt-password`, so both stay in sync. Shipped as agent **0.13.1-10091124** (agent/latest + deps pin; backend + Hosting deployed; prod verify-schema 0 violations).


104. **Templates, deployments and sites are BRANCHES of commits; compare + cherry-pick between any two; no proposals**
    (user: "think of templates and deployments as branches … i can see diff between 2 entities & cherry pick what changes i
    want to bring, and on commit i can merge those changes … (on evey change you must also give commit msg) … seperate sub
    branches for each deployment called agent branch … you dont have to make any proposal agent is suppose to fetch the
    latest deployment commit … anything not done viya agent [is] uncommit … user is req to first commit the local mannual
    changes before any merge … clound will also maintan the local branch … when anyone choose to merge show the deleted
    services/files/volumes too"). Full design (normative with `shared/src/branch.ts` + `shared/testdata/branch-vectors.json`,
    Go `agent/internal/branch`): **docs-v2/branches-commits.md**.
    **Supersedes:** #10 (one proposal at a time), the publish / proposal / approve model (§6 `publish`, §9.4, proposal staging
    of §9.5, §14.2, §14.4 for updates, §18 Diff + local proposals, §7.2 previewLocalProposal / createLocalProposal), #94
    (Template sync → the general compare + cherry-pick; `srcTemplate` becomes `branch.origin` + a merge-base record), #11 for
    updates (the 60 s approve wait), and the term "Draft" (= the **working copy**). Kept: the apply engine (§14.5 steps,
    pre-update snapshot, start order, readiness, failure diagnostics, rollback), image / file staging (now of the cloud head),
    smart distribution, store-and-forward, hold-to-fast-forward (#95) for data restores, volume content (#101).
    (a) **Branches + commits.** Every template, deployment and (deployment, site) pair is a linear branch. `Commit { commitId
        (ULID), branch, seq, parentId, kind create|change|merge|cherry-pick|revert|reset|drift|rollback|migrated|legacy,
        message (required, ≤ 2000, first line ≤ 200), author, createdAt, source (branch, commit, picks, skipped), config, hash =
        sha256(JCS(config)) (= the revision hash of the same config), catalog (deployment + agent), stats }`. Firestore
        `templates/{id}/commits`, `deployments/{id}/commits`, `deployments/{id}/agentBranches/{site}[/commits]`,
        `…/mergeBases/{sourceKey}`; agent SQLite `commits`, `cloud_commits`, `working_items`, `drift`, `drift_accepts`,
        `merge_bases`, `merge_runs`.
    (b) **Working copy** — SUPERSEDED by #105 (no working copy; pending edits live in the browser). Saving without a message into the working copy IS allowed (multi-page edits; audited, not history);
        nothing reaches history without a message. The uncommitted count (items head ↔ working copy) shows everywhere (D4).
        Commit on a template = template validation; on a deployment = publish-level blockers (a deployment commit IS a
        release, perm deployment.config.publish).
    (c) **Create from** template / deployment / agent branch (all four pairs): first commit "created from X@n", origin link +
        a merge-base record. Deployment ← deployment: generated secrets regenerate, others copied only with
        "copySecretValues" + deployment.secrets.read.
    (d) **Compare + cherry-pick** between ANY two branches (`POST /v1/branches/compare | merge/preview | merge`): field-level
        items (the #94 key grammar + secret values as hashes + per-file starter content); kinds add / remove / change (no base)
        and theirs / ours / conflict / skipped (3-way); **deletions are items** ("remove from target" / "remove locally",
        grouped as "Removed by this merge"); conflicts show base / target / source with Keep · Take · Custom; requirements
        auto-ticked. Merge needs a clean target working copy (409 dirty-working-copy) and a message; kind merge when every
        source change was taken, else cherry-pick. Merge base = per-pair record updated on every merge (#94 skip semantics),
        else the origin, else two-way. Revert / reset-to-commit are new commits; history is never rewritten.
    (e) **Deployment → sites without proposals.** The latest deployment commit is what sites should run (`CloudView.branch`);
        agents fetch + stage it automatically; nothing changes on a site until a person there MERGES (agent GUI Branch page).
        A merge with runtime effect runs the existing engine on that site; the merge commit is written only at COMMIT (a
        failed merge leaves no commit). No 60 s wait for merges; data restores keep IntentFlow (60 s, ⏩, ACCEPT).
        "Fast-forward all sites" = the old coordinated multi-site update for clean, strictly-behind sites.
    (f) **Agent branch** per site: scope = shared sections + its own site; other sites are reference copies refreshed from the
        merged cloud commit. **Drift** (containers created / changed / removed by hand, image, env, ports, mounts, restart,
        command, resources, edited per-site file / secret copies, persist-to-cloud volume content via #101) is read from
        Docker + the filesystem and shown as uncommitted config items; what cannot be expressed is a drift note (discard /
        accept). Merges are refused until every local change is committed or discarded.
    (g) **Cloud mirror**: agents push commits (store-and-forward, verified hash / seq / parent) and report uncommitted drift in
        the heartbeat; the deployment shows each site's branch (behind / ahead / differs / drift) and can cherry-pick from it.
    (h) **Activities / audit**: branch.created / committed / merged / cherry-picked / reverted / reset / merge-failed,
        working.discarded, drift.detected / committed / discarded / accepted, agent-branch.pushed; audit template.* /
        deployment.* committed / merged / reverted / reset / working-discarded.
    (i) **Migration** `backend/scripts/migrate-i.ts`: templates → one migrated commit; deployments: the applied revision chain
        → commits (commitId = revId, message = summary or "migrated revision N"), an open proposal on the head → the new head
        commit (its proposal stays for old agents), drafts → working copies, srcTemplate → origin + merge base (skips kept),
        agentBranches per enrolled site; agents ≥ 0.14 migrate their own revisions + local proposals (→ uncommitted).
    (j) **Compatibility**: while a site runs agent < 0.14 every new deployment head also writes the old revision + proposal
        (revId = commitId); 0.14 agents answer legacy updates as no-op participants; per-site "needs agent ≥ 0.14" warnings
        (`BRANCHES_MIN_AGENT`, `supportsBranches`). Removed routes answer 410 gone with a hint.
    (k) **GUIs**: shared `ui/src/branch` (CommitDialog, MergeView, HistoryView, ChangeCount + ChangeBar Commit…) used by both
        apps through a `BranchHost` adapter (#102 rule). Cloud: History + Compare in template and deployment scope (Diff and
        Template sync redirect), Agent branches page; agent GUI: Branch page (cloud head · this site · uncommitted · merge ·
        history), Diff redirects there.

105. **ONE action: Commit — no server-side working copy; pending changes live in the browser; on the agent "uncommitted" =
    drift only** (user: "again there should not be 2 diff things, "save template" and "commit" is not 2 diff things, one
    thing "commit" … eveything in cloude is always commited, there is never a uncommited changes, uncommited changes are
    always in browser UI until you push it gets commited with the commit message. only on the site agent if someone mannualy
    did something outside of agent UI then it is uncommited changes. and if you change anything viya agent GUI, then again
    those remain uncommited in browser, once you click commit it also updates the container's and all with the changes.").
    Full design: **docs-v2/branches-commits.md §15** (decisions J1–J15). Agent **0.14.1**.
    **Supersedes** the working-copy parts of #104: (b) "saving without a message into the working copy IS allowed", the
    server-side uncommitted count (D4, `BranchState.uncommitted`), D3, the cloud dirty-working-copy 409 on merges (D9 / D13
    for the cloud), §3.2 / §7.1 `discard…Working`, the agent `putWorking` + `working_items` GUI edits (§3.6, §7.2), and the
    agent GUI decisions BR-A1 (Save without a message) and BR-A5 (saved edits survive a reload on the agent).
    (a) **Cloud: no working copy.** The template config fields and `Deployment.draft` are a copy of the HEAD commit config,
        written only by commits (commit, merge, cherry-pick, revert, reset, create); `branch.uncommitted` is removed.
    (b) **Commit is the only persisting action** of the config editors: `POST …/commits { message, baseCommitId, config }`
        (the full resulting ConfigInput: base head + the browser changes; secrets plainValue absent = keep, null = clear,
        string = set, generateNow = generate). `baseCommitId` ≠ head → **409 stale** `{ reason: 'head-moved', head }`; the
        GUI rebases the browser changes with the shared merge engine (`rebaseChanges`: automatic when clean, else the
        Rebase dialog with the conflicts) and the person commits again. A deployment commit is a release (publish
        permission + publish-level blockers, unchanged).
    (c) **Removed (410 gone)**: putDraftSection, putDraftSite, regenerateSecret, discardTemplateWorking,
        discardDeploymentWorking, config fields in patchTemplate; `at: 'working'` in compare / merge → 422. Merges,
        cherry-picks, reverts and resets write their commit directly (no dirty check; the stale check stays).
    (d) **Browser change set**: one per entity, shared by all its pages; inline forms edit it, drawers / modals **Apply**
        into it; persisted in localStorage per entity + base head (`shared/src/pending.ts`, `ui/src/branch/pending.ts`);
        on load: same head → "Restore", moved head → stale → "Review & rebase" or Discard. The ChangeBar on every config
        page: "N changes (not committed)" · Discard · Review · Commit… — no Save button anywhere in the config editors.
    (e) **Secret values** are part of the change set and of the commit (typed values in memory only, never in
        localStorage); the commit records `data.secretValues { set, generated, cleared }` (slugs only) and so do the
        `branch.committed` activity and the audit entry. "Generate new value" = generateNow at commit; Reveal reads the head.
    (f) **Other writes**: ~~blob uploads (File content, seed archives) may happen before Commit (content-addressed, nothing
        references them until the commit lands)~~ — SUPERSEDED by #107 (bytes are pending changes, uploaded by the commit itself); image "Rename everywhere" writes one commit per affected template; entity
        details (labels, icons, descriptions, tags, managers) and catalog entities are not branch config ("Save details");
        persist-to-cloud volume content (#101) is data with its own versions.
    (g) **Agent GUI = the same model**: edits stay in the browser (localStorage); **Commit** (message required) =
        `POST /_opm/api/branch/commit { message, baseCommitId, config }` → the existing apply engine on this site (staging,
        pre-update snapshot, readiness, VERIFY, rollback on failure); the local commit is written only at COMMIT — a failed
        apply writes no commit and the browser keeps the changes. `putWorking` → 410.
    (h) **Agent "uncommitted" = drift only** (changes made outside the agent). Drift must be committed (or discarded =
        re-applied) before a merge AND before a commit of GUI edits (409 conflict `{ reason: 'uncommitted-drift' }`); the
        GUI offers "Commit server changes first", then rebases the browser changes onto the drift commit.
    (i) **Migration**: `backend/scripts/migrate-j.ts` (dry run / --apply, idempotent) turns every cloud config copy that
        differs from its head into a commit "Uncommitted changes from before 0.14.1 (auto-committed)" (`data.autoCommitted`;
        validation reported, not enforced), then deletes `branch.uncommitted`; verify-schema checks copy hash = head hash.
        Agents 0.14.0 → 0.14.1: saved working edits are auto-committed through the apply engine at first start (same
        message, author system) when the site is idle, has a head, no blocking drift and the edits validate; otherwise (or
        if the apply fails) they are dropped to `<dataDir>/backup/working-copy-pre-0.14.1.json` and logged
        (`working.discarded`, reason).
    (j) **Compatibility**: the agent-facing cloud API is unchanged (agents < 0.14.1 keep working; a 0.14.0 heartbeat
        `uncommitted.working` is shown as "saved edits on the agent"); agent 0.14.1 works with the 0.14.0 and the 0.14.1 cloud.

106. **GitHub mirror of every branch + docker compose without the agent** (user: "define a GitHub repo and an access token,
    and mirror every OPM branch into it as git branches, as a backup and a side database for configs … git clone the branch
    onto a VM, cd into the site's directory, run `docker compose up`, and everything comes up equivalently — containers,
    router and edge — except the agent itself … under the ADMIN section"). Contract `shared/src/github-mirror.ts`; full
    design + as built: **docs-v2/github-mirror.md**. Agent **0.15.0** (drift push).
    (a) **Branches** `template/<slug>`, `deployment/<slug>/cloud`, `deployment/<slug>/agent/<site>`,
        `deployment/<slug>/uncommitted/<site>` (`mirrorBranchName`). One git commit per OPM commit (same message, author name
        / email; author = committer, both dates = createdAt) with trailers `OPM-Commit` (idempotency key), `OPM-Branch`,
        `OPM-Seq`; agent branches also one commit per persist-to-cloud snapshot (`OPM-Volume-Version`); uncommitted/<site> =
        ONE drift snapshot commit on top of the agent branch, reset per new drift fingerprint, deleted when cleared. Nothing
        else is pushed; an EMPTY repository first gets one README commit on `main` (the Git Data API refuses empty repos).
    (b) **Entity slugs** `Template.slug` / `Deployment.slug` (RE.slug): `uniqueSlug(slugifyLabel(label))` or the requested
        slug at create (409 conflict reason slug-taken), immutable (PATCH → 422 V_IMMUTABLE), unique per collection (deleted
        deployments included); `backend/scripts/migrate-k.ts` assigned existing ones; verify-schema checks them.
    (c) **Token** stored like Registry.password (#1 / #84): plain field in Firestore `githubMirror/config` (client access
        denied by the rules), masked everywhere (`maskGithubToken`), revealed 60 s by `revealGithubToken` (CREDENTIAL_OPS:
        no-store, 30/min/uid; audit github.token-revealed), replaced / removed by PATCH. Secret Manager was not introduced:
        the existing OPM credential pattern is followed. Admin → GitHub (`/app/github`, perm accounts.admin); every change is
        audited (target type `github`, target `githubMirror/config`; actions github.updated / tested / token-revealed /
        synced / resynced / push-failed).
    (d) **Exporter** = Go `agent/internal/export` (pure), built on the agent's own builders (`driver.Build` per container,
        `edgecfg.InputFromConfig` + `edgecfg.BuildStandard` for the edge, `validate` depends / start order). It runs in Cloud
        Functions as `opm-export.wasm` (wasip1, node:wasi), compiled by the backend build from `agent/`. Deferred inputs
        (secrets, auto hosts, other sites' auto hosts, required template variables) are detected by resolving twice with
        two sentinel sets; filters on them are evaluated symbolically (§21 #115), so every output that depends on them is
        the config text with placeholders: `${NAME}` in the compose file, `@@{NAME}@@` in templates.
    (e) **Tree** and (f) **Compose**: superseded by §21 #115 (the Docker-only tree: site.env + deploy.sh, Caddy edge,
        Portainer, no OPM binary).
    (g) **Queue**: Firestore `githubMirror/config/{queue,branches,blobs}`; enqueue by triggers on new template / deployment /
        agent-branch commits and persist snapshot versions, by the drift push and by heartbeats (agent head not mirrored →
        catch-up); worker = queue trigger + schedule every 5 min + inline Sync now; per-branch lease, ordered, idempotent
        (state + the remote head's OPM-Commit trailer), backoff 30 s → 1 h, 401 / 403 / rate limits pause the mirror.
        GitHub REST Git Data API only (blobs deduplicated by git sha, one full tree, commit, ref; force only for rebuilds).
        Sync now = catch-up; Resync everything = rebuild from history (same shas when unchanged).
    (h) **Drift push** (agent 0.15.0): `POST /v1/agent/branch/drift` when the drift fingerprint changes and once at start
        (outbox, store-and-forward; liveConfig masked; an unmasked secret is refused 422 V_SECRET); stored on the
        AgentBranchDoc (`drift`, `_driftLive`). Older agents: no uncommitted branch.
    (i) **Not equivalent** (README of every branch + docs §6): drift, rollback / snapshots / backups, persist sync,
        approvals / merges, agent GUI / dev tools / transfers / self-upgrade, auto host detection (→ .env), readiness for
        containers without health (started at once) and log health checks, the edge's immediate 503 for a stopped
        container (502 → 503 page), client address (docker gateway), Caddy per-read / per-write deadlines, dns-01 without
        the Dockerfile, loopback ephemeral publications, File locations, the platform OPM imported.
    (j) **Proof**: `TestComposeEquivalenceDocker` (real Docker + Compose: agent apply vs `docker compose up` of the same
        commit, 47 aspects, 0 differences, incl. a mosquitto_passwd pwfile and seeded volumes), `TestStandardEdgeVectorsDocker`
        (57 / 57 edge vectors on the official caddy image), `TestStandardClientParityDocker`, `TestWasmMatchesNative`,
        backend emulator 32-github-mirror (fake GitHub with real git objects; tree sha = `git write-tree`) + 33-slugs.

107. **ONE Commit = ONE transaction, including the bytes; content edits are pending changes like any other** (user, on
    the template volume content page `/app/templates/<id>/sites/site-01/volumes/content?volume=code-shell-code`: "when i
    change any file on approve/apply it seems to upload those files. and then i have to seperately make the commit. this is
    also wrong. keep all the file changes and volume changes all config changes. and single commit button on msg + comit,
    you will first upload file then commit, all done in transaction so everything gets reverted if not done."). Contract
    `shared/src/staging.ts` (+ `pending.ts` blobs, `ui/src/branch/pending-blobs.ts`, `ui/src/branch/stage-commit.ts`);
    design: **docs-v2/branches-commits.md §16**. Agent **0.15.1**.
    **Supersedes** #105 (f) / J8 "blob uploads (File content, seed archives) may happen before Commit" and the page-level
    Apply / Discard of the volume content workspace (#101 (j)).
    (a) **Every content edit is a pending change of the browser change set**: volume starter content (create, edit,
        rename, move, delete, upload files / folders), File content (the "Edit content" drawer has no Save content /
        Apply — edits apply live; picking a file), volume seed archives. The editors put the BlobRef (sha256 + size) into
        the working config at once (texts debounced ~300 ms + on blur) and keep the BYTES in the browser: memory + IndexedDB
        (`opm-pending-blobs`, one record per entity + sha256 with the base head; localStorage is too small) next to the
        localStorage change set (`StoredPending.blobs`). The change bar counts them; Review lists per-file changes with a
        text diff. Reload restore, stale → rebase keep the bytes; Discard and a successful commit clear them. Nothing
        touches the server until Commit. Not-found pages and icons are catalog entities (not config): unchanged.
    (b) **Commit = stage → verify → promote → commit**: (1) `POST /v1/stages { blobs }` with every blob the commit config
        references that the base head does not (`newConfigBlobs`) → `{ stageId, expiresAt, missing, present }`; blobs the
        content-addressed store already has are never re-sent. (2) every missing blob → `PUT /v1/stages/{id}/blobs/{sha}`
        (raw ≤ 24 MiB, verified) or `POST /v1/uploads { …, stageId }` (multi-part) into Storage `staging/<stageId>/<hex>`;
        the dialog shows "Uploading n of m files · x of y MB". (3) `POST …/commits { message, baseCommitId, config, stageId }`:
        stale check → validation → every new blob must be in the store or the stage with the right size AND sha256
        (else **422 invalid `V_BLOB_MISSING`**, `details.missing`) → the staged blobs are **promoted** (copied to
        `files/<hex>`) → the Firestore commit transaction (head check again) → the stage is deleted.
    (c) **Crash safety**: promotion happens BEFORE the commit becomes visible. A crash or failure at any point leaves
        either no commit (promoted blobs are unreferenced content-addressed objects nothing reads) or a commit whose blobs
        are all readable. Readers — the agent's `getAgentBlob`, the GitHub mirror (#106), the console's `getBlob` — read
        only the store, never `staging/`.
    (d) **Failure = nothing visible**: upload error, 422, 409 stale, network, crash → no commit; the server deletes the
        stage (staged blobs) on every failed commit, the browser deletes it too (best effort) and KEEPS every pending
        change including the bytes, so the person retries; 409 offers the rebase of #105. Abandoned stages (tab closed
        mid-upload): Storage lifecycle rule (`staging/` objects older than 1 day are deleted; `backend/storage.lifecycle.json`,
        applied by `npm run deploy`) + the daily job (expired `stages/{id}` documents and their objects).
    (e) **Audit / activities**: one commit entry. Stage operations are never audited (AUDIT_EXEMPT_OPS), multi-part
        uploads into a stage are not audited; there is no separate "blob uploaded" entry for a commit. `POST /v1/blobs`
        stays for compatibility; the console no longer calls it.
    (f) **Persist-to-cloud cloud copies** (#101 (f), data, not config) keep their own "Save as new version", now also as
        one transaction: stage → `createVolumeContentVersion { …, stageId }` (promote, then the version).
    (g) **Agent GUI = the same rule** (agent 0.15.1): File content edits stay in the browser (PendingBlobs 'agent');
        Commit = `POST /_opm/api/stages` → `PUT /_opm/api/stages/{id}/blobs/{sha}` → `POST /_opm/api/branch/commit
        { message, baseCommitId, config, stageId }`; the agent verifies (422 V_BLOB_MISSING), promotes the staged blobs
        into its blob store BEFORE the apply engine stages anything, and deletes the stage on every outcome; a failed
        apply writes no commit (unchanged). Stages older than 24 h are removed at start and hourly. `POST /_opm/api/blobs`
        stays for compatibility.
    (h) **Local drafts in IndexedDB, 1 h after the app was last open** (user follow-up: "even if my website gets refreshed
        or if by mistake went out and then came back in, all pending changes in templates, deployments even in agent UI,
        must be all stored in index db. (auto expire after 1hr if website is not loaded, but if the website is open then
        keep it while the session is live) … on refrsh ask if they want to use the draft or not. and draft is only valied
        if the drafet was made on the commit and the current latest commit is also the same!"). **Supersedes** #105 (d)
        "persisted in localStorage" and the stale "Review & rebase" restore (J5 / J6 for stored sets).
        - ALL pending change sets (config + bytes) of cloud templates, cloud deployments and the agent GUI live in IndexedDB
          `opm-drafts` (stores drafts / blobs / meta; `shared/src/pending.ts` DRAFT_*, `ui/src/branch/drafts.ts`), keyed per
          app + USER + entity, auto-saved on every change (debounced 400 ms, flushed on pagehide). localStorage is no
          longer used; an old `opm.pending.v1.*` entry is migrated once.
        - Expiry per app origin: a heartbeat (`meta alive:<app>`) every 30 s and on visibilitychange / pagehide while any
          tab is open; on load, when the last heartbeat is older than 1 h every draft of the app is purged (BEFORE the first
          heartbeat). An open tab keeps drafts alive however long the session lasts. Logout or another account → that
          user's drafts are deleted; drafts of another user are never shown.
        - On load, a draft is valid ONLY when its base commit is the entity's CURRENT head (agent GUI: the local branch
          head): valid → "You have unsaved changes from <time> (N changes) — Restore or Discard?" (Restore loads config +
          bytes; Discard deletes); head moved → deleted automatically with a one-time notice "Your local draft was based on
          commit X; the latest is Y, so it was discarded." No rebase of a stored draft; the in-session 409 rebase at
          Commit stays.
        - Users: cloud = the signed-in uid; agent GUI = the declared actor email ("Who is working right now?", asked on
          every load; until someone is declared the changes stay in memory only; declaring another person deletes the
          previous person's drafts). Released: backend + Storage lifecycle + hosting deployed, agent 0.15.1-10100226 =
          agent/latest + deps manifest pin.
108. **The cloud runs on a VM without Firebase — storage, auth, events and jobs are adapters** (user: "convert this into
    a proper VM app, so no firebase at all … the code should work in firebase and in VM … local dbs like sqlite multiple
    sqlites small, one for each deployments … Table {id, value: json, ...index cols} … for storage use the fs disk
    directly … db is just an adaptor"). Guide: **docs-v2/self-host.md**; code: `backend/functions/src/ports/` (the
    interfaces), `adapters/firebase/`, `adapters/local/` (+ its README), `app.ts` (router + TRIGGERS + JOBS, shared by
    both entries), `index.ts` (Cloud Functions), `server.ts` + `server/` (opm-cloud), `selfhost/` (Dockerfile, compose,
    Caddyfile, systemd unit).
    (a) **Ports** (narrow, only what OPM uses — not a Firestore clone): **DocStore** (collections / sub-collections as
        paths, collection groups, get / set / set-merge / create / update with dotted paths / delete, transforms increment
        / arrayUnion / arrayRemove, queries == in array-contains < <= > >= + orderBy + limit + startAfter + select,
        optimistic read-then-write transactions, batches, a bulk writer, `Timestamp`, `StoreError` codes);
        **BlobStore** (put / writeStream / get / stream with Range / head with custom metadata / setMeta / delete /
        deletePrefix / list / copy / compose (capability) / signedReadUrl / lifecycle sweep); **AuthAdmin**
        (verifyIdToken with revocation, user admin, reset links — Firebase error codes kept); **Events** (DocTrigger:
        name + created|written + path pattern + handler), **Scheduler** (ScheduledJob: name + schedule), **Queue** = the
        existing durable queue documents (`githubMirror/config/queue`, leases) woken by a created-trigger. Business code
        (core/, routes/, jobs/) imports no Firebase module; `core/fire.ts` `db()` / `auth()` and `core/storage.ts`
        delegate to the installed ports. A future **pg + redis + s3** adapter implements the same three interfaces
        (not built).
    (b) **Firebase adapter = production default** (your-firebase-project): thin wrappers over the Admin SDK; documents, paths,
        indexes, rules, Storage objects and the deployed function names / options are unchanged. Triggers stay Firestore
        triggers (`index.ts` exports `importImage`, `mirror*`, `daily`, `mirrorRetry` from the same `TRIGGERS` / `JOBS`
        declarations), so production behaviour is byte-for-byte the old one.
    (c) **Local adapter** (opm-cloud, `OPM_DATA_DIR`): `db/global.sqlite` (accounts, catalog, templates + their commits,
        audit, mirror config / queue / branches, uploads, stages, nonces, rate limits, built-in auth users / sessions)
        + **one `db/deployments/<deploymentId>.sqlite` per deployment** holding the deployment document itself and every
        path under it (commits, agents, agent branches, activities, transfers, proposals, revisions, install tokens,
        volume content, uptime…), so writes of different deployments never contend. WAL, busy_timeout 5 s. Every
        logical collection is ONE table `{ id TEXT PRIMARY KEY (document path), parent TEXT, value JSON, ix_<field>… }`;
        index columns exist only for fields OPM filters / orders by (registry in `adapters/local/registry.ts`, from
        firestore.indexes.json + every query in the code; `OPM_LOCAL_STRICT=1` makes an unindexed query an error), are
        filled from the JSON on write and back-filled when the registry grows. Documents keep the Firestore JSON
        (Timestamps `{"$timestamp":{seconds,nanos}}`, bytes `{"$bytes":…}`), so data moves 1:1. Root `deployments` and
        collection groups query every file and merge. Documents whose orderBy field is null / missing are included
        (Firestore leaves out missing ones; OPM never relies on it).
        **Transactions** are optimistic like Firestore (reads recorded, re-validated under `BEGIN IMMEDIATE`, retried
        up to 5×). A transaction may span files: deployment files are locked + committed in id order, the global file
        LAST; a crash between the COMMITs keeps only the deployment part. Inventory of the spanning writes: completeUpload
        (transfer) — fixed: a retry finishes an upload whose transfer is already ready (`core/uploads.ts`);
        createTransfer / createAgentTransfer (an orphan `uploading` transfer, expired by the daily job), deleteTransfer
        (an open upload, harmless), deleteTag / daily cleanup / migrations (chunked, idempotent like on Firestore).
        Slug uniqueness across deployments (multi-file queries in a transaction) is serializable (all files read + global
        locked).
        **Events**: every write matching a trigger pattern appends an `_outbox` row in the SAME SQLite transaction
        (change-data capture: no write path can forget to emit); opm-cloud drains the outboxes after each commit and
        every second (rows written by scripts / other processes too), at least once, failures logged and dropped like
        `retry: false`. Migrated / copied documents are written with the outbox off.
        **Blobs**: files under `blobs/<object path>` (the same paths as the bucket), metadata sidecars in `blobmeta/`,
        atomic writes; downloads 302 to `/_blob/<path>?exp&sig` (5-minute HMAC-SHA256 tokens, Range, CORS like the
        bucket); agent downloads stay token-gated (§4) — the blob URL is only handed out by those endpoints. Lifecycle
        (`staging/` 1 day) and the TTL fields (agentNonces, rateLimits) are sweep jobs.
        **Auth** (the hosted console signs in with Firebase email + password; Google only with VITE_OPM_GOOGLE_DOMAIN):
        built-in email + password (argon2id), HS256 ID tokens (1 h) + rotating 30-day refresh tokens (reuse revokes the
        family), `tokensValidAfterTime` semantics (password change / revoke / disable end sessions), reset links
        `<console>/reset-password?token=` (1 h, mailed with OPM_SMTP_URL), optional OIDC / Google sign-in
        (`OPM_OIDC_*`, PKCE, the person still needs an account with that e-mail), endpoints `/auth/*` on opm-cloud.
        **Realtime: not needed** — nothing in OPM listens to the database (agents long-poll the API, the console polls);
        SQLite only, no in-memory + JSON store.
    (d) **opm-cloud** = one Node 22 process on one port: `/v1/*` (the same `handleApi` as the function `api`: mount
        stripping, CORS), `/auth/*`, `/_blob/*`, `/opm-config.json`, `/healthz`, and the built frontend with SPA fallback
        (= Hosting). Jobs: `daily`, `mirrorRetry` + `docTtl` (10 min) + `blobLifecycle` (hourly), each claimed through
        a lease document `opmScheduler/{job}` (a restart catches a missed run up, never doubles one); the outbox
        dispatcher (4 handlers in parallel). Config: env `OPM_*` or `--config <json>` (adapter local | firebase, data
        dir, public URL, port, static dir, SMTP, OIDC, blob redirect). CLI: `serve | create-admin | reset-link |
        release-key | upload-agent | upload-deps | backup`. One process per data dir (scale up; pg+redis+s3 for scale-out).
    (e) **Frontend: one build for both.** It reads `/opm-config.json` at start (`{ auth: { provider, passwordReset,
        oidc } }`; Hosting serves the static `{ provider: 'firebase' }`, opm-cloud its own) and loads Firebase Auth only
        for the firebase provider. New static route `/reset-password?token=`.
    (f) **Agents → any cloud**: the agent has no built-in cloud URL; `apiBase` comes from the install / upgrade command
        of the cloud that issued it (install script, binary, deps manifest, upgrades, relay all go through it), so a VM
        installation is its own single API domain (the one-domain rule holds per installation). Agent binaries, `.sig`
        and the deps manifest live in the BlobStore and are uploaded with `opm-cloud upload-agent / upload-deps`
        (signed with the data dir's release key, `opm-cloud release-key`, or `OPM_RELEASE_PRIVATE_KEY`). No agent change.
    (g) **Migration**: `backend/scripts/migrate-cloud.ts --from firebase --to local` (reverse with `--confirm-project`)
        copies every document (recursive walk incl. sub-collections of missing parents), object and Auth user (same
        uid, no password — Firebase hashes are not exportable: reset links / `create-admin`), then verifies counts +
        a sha256 of each document's canonical encoding + every object's sha256; report `migration-report.json`, exit 1
        on any difference. Reads of Firebase only.
    (h) **Tests**: the whole backend suite runs on both adapters (`npm test` = emulators; `npm run test:local` =
        opm-cloud on a temp data dir, tests open the same files); adapter conformance `test/40` (both) + local-only
        `test/41` (cross-deployment concurrency incl. a second process, outbox, strict mode, blob URLs, sweeps, auth);
        `OPM_TEST_HOOKS=1` enables the emulator-only test hooks on the local stack; the VM e2e `e2e/vm/` drives the
        docker compose stack with a real agent.

109. **Found by the VM template tests (local-test app-cs / ha-2vm, 2026-10-10)** — agent 0.16.x.
    (a) **Completion limit honours the health check** (§15.4): a container may take max(300 s, startPeriodSec + retries ×
        (intervalSec + timeoutSec)) to become completed. Before, the fixed 300 s made a start period above ~300 s useless
        (code-shell's first Deno install: 900 s; a Postgres standby clone waits for the primary's spread checkpoint).
    (b) **Staging is not cancelled by the client**: a merge / commit that stages synchronously (first staging pulls images
        for minutes) runs on a context detached from the HTTP request (still bounded by 15 min); a client / proxy timeout
        no longer aborts the downloads, the run starts and the GUI follows it in the branch state.
    (c) **Merge preview renders files like the apply**: the per-container runtime actions are computed with the files
        rendered (deferred only while a file blob is not on the site), so containers that mount rendered files show
        "keep" when the apply keeps them.

110. **An update never freezes the agent: the lock is advisory; Stop, Stop + roll back, Stop + restart + roll back;
    `opm-agent repair`** (user, after a failed apply left app1 of an HA-4VM deployment "update running" for over an hour
    with every action refused: "disable such freezes. allow agent to work independently! dont ever freeze anything. just
    show the warning but nothing. and a explisit buttons to fix things like stop terminate existing run or a button to
    auto restart the agent and rollback to last backup. go through this agent code and make sure all cases are taken
    care of!" + "also make it so that on mannual agent reinstall viya terminal has all this mechenics to fix the agent if
    its in corrupt state. and revert to last backup state."). Agent **0.16.6**; code `agent/internal/agent/terminate.go`,
    `repair.go`. **Supersedes** every "409 locked while an update runs" of §7, §14.5 step 1 "each site checks no lock" and
    the gate condition `idle` closing a gate.
    (a) **The update lock (`kv.lock`) is advisory.** It is shown (layout banner, ping `lock`, gate condition `idle` with
        `warn: true` — always `ok`), never enforced: container restart / stop / start, prune, volume files, volume actions,
        volume content apply / snapshot, snapshots, drift rescan, local proposals + approve, branch commit / discard /
        merge / fast-forward, snapshot roll back, volume reset, agent restart / upgrade / settings / mesh change all go
        ahead. Still waiting (automatic work only, nobody clicked): the cloud-requested upgrade, the automatic drift scan,
        the automatic volume-content snapshot, the working-copy migration. Still refused: a volume action on a volume
        another volume action is busy with, and starting an update while a volume restore / copy runs (409 conflict;
        an agent restart ends a stuck volume action: it is marked failed at start).
    (b) **Starting an update while one runs stops the running one first** (`supersedeRunning`: terminate on every site of
        its plan, nothing rolled back); the new plan converges the sites. `update.started` carries `superseded: [ids]`. A
        LOCK step for another update than the one a site holds stops the held one there (never `locked by update …`).
    (c) **Stop** = `POST /_opm/api/updates/{updateId|current}/terminate { reason?, rollback?, restartAgent? }` (session +
        actor, never refused; any site of the plan): the asking site sends `POST /_opm/peer/update/{id}/terminate` to every
        other site of the plan (+ the coordinator), then stops it itself; per site: the update's loops are cancelled
        (coordinator run + its own step; Docker waits stop at once), its lock is removed, a running step is marked
        `failed: terminated: <reason>`, the coordinator's run is finished `ok: false`, a merge run is `failed`, an
        approved proposal goes back to `ready`; activity `update.terminated`. Nothing is rolled back: the containers stay
        as they are. A site not reachable stops it when it next sees a ping listing it (`pingWire.terminated`, the last
        50 ids, from ANY site — not only the coordinator). A terminated update is never resumed: a late coordinator's
        step is refused (409 conflict), its late writes never resurrect the run, and its roll back does not run.
        Answer `{ updateId, sites: [{ siteSlug, ok, message }] }`.
    (d) **Stop + roll back** (`rollback: true`): each site then converges to its HEAD config with the volumes of the
        update's pre-update snapshot (when the site has its part; else the head config only, volumes kept) — shown as a
        running lock of kind `rollback`, updateId `recover-<id>`, itself stoppable with the same endpoint; activity
        `update.recovered`. **Stop + restart + roll back** (`rollback` + `restartAgent`): each agent writes
        `kv.recover`, restarts, and runs that roll back right after the start. `restartAgent` alone: every agent restarts
        (a coordinator restarted mid-update still rolls its unfinished run back, §14.5).
    (e) **`opm-agent repair [--check] [--rollback [--snapshot ID]] [--json] [--no-service]`** (offline, the service is
        stopped and started around it unless `--no-service`): `PRAGMA integrity_check` (damaged → report, the install
        script restores a backup with `OPM_RESTORE_DB_BACKUP=1`); every update this site still holds (lock, unfinished
        coordinated run, step marked running, pending `kv.recover`) is stopped as in (c); `--rollback` = (d) with the
        LAST BACKUP: the pre-update snapshot of the stuck update, else the newest snapshot of this site taken at the head
        revision (older snapshots = a real roll back: Backups → Roll back). `--check` changes nothing and exits 3 when an
        update is stuck or the database is damaged.
    (f) **The install command runs the repair on every reinstall / up- / downgrade** (agents with `repair`), while the
        agent is stopped for the binary swap: a stuck update is always stopped; when one was stuck it asks "Also roll
        this site back to its last backup?" (`OPM_ROLLBACK=1` answers yes, also when nothing was stuck).
    (h) **Nothing runs or stays locked forever**: every site step (and the abandon / recovery roll backs) is cancelled at
        its step timeout + 1 min; apply timeout 30 min (was 15: a 900 s start period + retries needs ~17 min, so the
        coordinator timed the apply out before the site reported). **Watchdog** (every minute, every site): a lock or an
        unfinished coordinated run older than its deadline is stopped as in (c) (no roll back; `update.terminated` with
        reason `watchdog: …`). Deadline = lock + snapshot + verify + commit + per site (apply + roll back + 2 min) +
        10 min (2 sites ≈ 2 h 50 min, 4 sites ≈ 5 h — a safety net; normal failures unlock within their roll back); a
        `recover-*` lock: 36 min; a lock without its plan: 15 min; an unreadable lock: at once. A roll back interrupted by
        an agent restart drops its lock at start.
    (i) **A stop never leaves a service down**: after a plain Stop each site starts again the service containers of its
        head config that THIS update touched and that are not running (start only, nothing recreated). An apply (also a
        roll back / repair) starts a KEPT service container that is not running (before #110 a container stopped by the
        pre-update snapshot stayed down after a roll back that did not restore its volumes). A container a person
        stopped is started too by the next apply when the config has it enabled (disable it in the config to keep it
        stopped).
    (j) **The agent itself never crashes on these paths**: a panic in a step fails that step at once; panics in staging,
        readiness waits and the post-update cleanup are recovered; `caddy.Validate` (edge staging check) runs under the
        runtime lock — two at once panicked inside certmagic ("close of closed channel") and took the process down —
        and a panic inside Caddy becomes an error. The service unit restarts a crashed agent after 5 s (containers keep
        running); a coordinator restarted mid-update rolls its unfinished run back.
    (k) **Verified with real Docker** (`cmd/opm-devmesh/stuck_test.go`, OPM_STUCK_E2E=1; two agents): a never-healthy
        update on 2 sites — nothing refused while it runs, Stop & roll back from the non-coordinator, plain Stop, a new
        update superseding it, the unattended failure rolling back + unlocking by itself, both agents killed mid-update
        then `Repair` (= `opm-agent repair --rollback`) clearing it with every service running again.
    (g) **GUI**: the layout banner is a warning ("… is running on all sites (coordinator X). You can keep working —
        starting another update stops this one.") with **Stop update**, **Stop & roll back**, **Stop, restart agents &
        roll back** (each confirmed, results per site); no page disables anything because an update runs.

111. **One site's branch from any site's GUI** (user: "in agent it is not easy to understand how the merge from cloud
    for all sites works, I have to open each site IP's OPM … I can see each site's branch separately and merge them
    individually"). The branch endpoints are also SITE endpoints: `/_opm/api/sites/{site}/branch`, `…/branch/commits`,
    `…/branch/commits/{id}`, `…/branch/cloud-commits/{id}`, `…/branch/uncommitted`, `…/branch/blobs/{sha256}`,
    `…/branch/drift/rescan`, `…/branch/commit-drift`, `…/branch/discard`, `…/branch/compare`, `…/branch/merge/preview`,
    `…/branch/merge`, `…/branch/revert`, `…/branch/reset`, and the run of such a merge `…/updates/current`,
    `…/updates/{id}`, `…/updates/{id}/abandon`. Same handlers as `/_opm/api/branch/*`: site = this agent → served
    locally; another site → relayed signed to its `/_opm/peer/site/…` (like every per-site call: the person is forwarded
    as `X-OPM-Actor` + session, actor-required calls are checked on the forwarding agent, the target records the
    activity with that person); not reachable → 503 unavailable "site X is not reachable". `commit-drift` = commit in
    the drift mode only (keys / accept / takeBytes); a body with `config` or `stageId` → 400 invalid: browser edits are
    committed on the agent the person is logged in to (`POST /_opm/api/branch/commit`, its staged blobs live there).
    Fast-forward all stays on `/_opm/api/branch/fast-forward` of the logged-in agent (one coordinated run).
    GUI: `/_opm/app/{site}/branch` shows THAT site's branch (cloud card, the site's head / merged from / behind /
    ahead / differs, uncommitted server changes, History); Commit, Merge from cloud, Discard, Revert and Reset act on
    that site only; a banner says which site is shown through which agent; the sites table links every site's branch.
    `/_opm/api/branch/*` is unchanged (older agents; the sidebar badge of the logged-in site). An agent older than this
    answers 404 "no such site endpoint" for another site's branch: open that site's own GUI.
112. **The agent's own footprint, liveness and backups** (user: "why would my server take so much resources … is it
    unoptimized … find any point of failure"; "fix and optimize the agent as much as possible"; per-volume automatic
    backups that stay usable). Guarantees kept: merge of the cloud config, diff local vs cloud, a backup before every
    change from the agent GUI, every volume restorable from any kept backup.
    (a) **One shared Docker view.** The Docker client subscribes to `GET /events` (container, image, network; one stream,
    reconnected with backoff 1 → 30 s) and caches container lists (per filter), inspects (per container, by any key) and
    image inspects. An entry is dropped on any event about it (exec_* / top / resize / attach / archive ignored: health
    probes), on any write through the client (writes to /containers/{id}/…, /images, /networks), after 30 s (periodic
    resync) and wholesale on (re)connect or stream loss; while the stream is down nothing is cached. Docker stays the
    only source of truth. The drift loop uses the stream instead of polling `/events` every 10 s; unlabelled containers
    the list shows off the site network are skipped before any inspect; ImageTag only when the tag is missing.
    Measured (docker cache_test, 14 containers, the loops' per-minute pattern): 315 → 92 Docker API calls / min (lists
    21 → 7, inspects 238 → 29; the 56 one-shot stats remain). `headConfigRO` keeps the decoded head config per head
    revision for read-only hot paths (served ping, container counts, uptime sampler); the cloud view is fetched from a
    peer only when its ping shows a newer `cloudFetchedAt`.
    (b) **No call blocks forever.** Every Docker request without a caller deadline gets one (GET 15 s, one-shot stats
    10 s, stop / restart t + 60 s, exec start 10 min, other writes 2 min; none for streams, wait, follow logs, image
    load / pull). Every periodic loop records start / end; the systemd watchdog is fed (sd_notify WATCHDOG=1) only
    while the critical loops (heartbeat, outbox, sampler, watchdog, tunnel) progress — a tick stuck for
    max(10 min, 3 periods) stops the feeding and systemd restarts the agent (containers keep running). The agent turns
    the watchdog on itself (`WATCHDOG_USEC=180 s` under `NotifyAccess=main`; a unit `WatchdogSec` wins), so an older
    binary after a roll back never runs under a watchdog it cannot feed. A panic in any goroutine is recovered and
    logged (`dlog.Recover`); in an update step it fails that step on that site (the update rolls back).
    (c) **Unit.** `Wants=` (not `Requires=`) docker.service: restarting / stopping Docker no longer stops the agent and
    its edge; `NotifyAccess=main`; `MemoryHigh` = 2 × the Go memory limit (≤ half the RAM; throttles, never kills).
    Port 443 is NOT held by opm-agent.socket: agent.json has no HTTPS port setting, and a host where another program
    owns 443 would fail the socket unit and with it the agent (it `Requires=` the socket).
    (d) **Memory.** Go soft limit: GOMEMLIMIT, else min(25 % of RAM, 1 GiB), never below 256 MiB. Hourly prune of the
    in-memory maps (update runs / site states / coordinator contacts finished > 1 day, staging details without an open
    proposal, rate windows, reveal times, peer have-lists > 10 min); the debug-log reader reads files backwards in
    64 KiB chunks and stops at the limit (Export streams); the log's secret set is bounded (4096).
    (e) **Disk.** A free-space floor of min(5 % of the filesystem, 2 GiB) the agent never uses: a snapshot, a volume copy
    into the agent home or a blob download that would go below it is refused (error + activity `disk.low` + status
    warning). Snapshots stay within a size budget (agent.json `snapshotBudgetPct`, default 30 % of the agent-home
    filesystem) on top of the count retention: oldest scheduled → pre-update → volume-auto go first; never removed:
    the newest of each reason (the newest pre-change snapshot), a running update's pre-snapshot, the `keep` newest
    automatic backups of every volume, manual and imported snapshots; when those alone exceed the budget, status warns.
    (f) **Store.** A daily online backup of opm.db (`VACUUM INTO <dataDir>/backup/daily/opm-YYYYMMDD.db`, 3 kept); at
    start a file that fails to open or `PRAGMA quick_check` is moved aside (`opm.db.broken-<unix>`) and the newest daily
    backup restored (no restart loop with the edge down). Every failed write is recorded (status / self-metrics). Every
    6 h: prune of finished updates and terminal proposals (+ staging rows) older than 90 days and outbox dead letters
    older than 30 days, then a WAL checkpoint. Activities (the verified hash chain) and cached cloud commits are kept.
    (g) **Outbox.** An item the cloud keeps failing with 5xx is parked after 20 tries (a dead letter: never due, kept
    30 days, counted in status) so it never blocks every later report; network errors / 401 / 429 keep the FIFO wait
    (they hit every item alike); a 401 shows "cloud key rejected" (local operation goes on).
    (h) **Per-volume automatic backups.** `SiteVolume.backupPolicy { schedule: off | hourly | 6h | daily | weekly,
    keep: 1 … 100 }` — absent = today's behaviour (the daily scheduled site snapshot, its 7 newest kept; older revisions
    keep their hash). A volume with a policy leaves the scheduled site snapshot and gets its own snapshots (reason
    `volume-auto`, note `auto <slug>`): hourly / 6h taken live, daily / weekly with its containers stopped around the
    archive; the `keep` newest are kept. Validation (Go + TS `checkBackupPolicy`): V_ENUM schedule, V_RANGE keep,
    warning V_VOLUME_BACKUP_OFF when the volume's backup is off. `GET /_opm/api/sites/{site}/volumes/{volumeSlug}/backups`
    lists every complete snapshot on the site that holds the volume (any reason, newest first) + the policy and the next
    run; `restoreVolume` takes `source.kind: "snapshot", snapshotId` (stop its containers, snapshot the current data
    first, restore the archive — sha256 checked —, start them). GUIs: "Automatic backups" in the volume form (cloud +
    agent); the agent Volumes page Backups tab (Restore from it…).
    (i) **Self-metrics.** `GET /_opm/api/sites/{site}/agent/process`: RSS, CPU % (of one core), threads, open files, Go
    heap / limit, goroutines, GC, Docker calls / min + whether the shared view is live, store health, the agent's own
    disk (opm.db, metrics.db, blobs, snapshots vs budget, logs, free), warnings, loop states — the Agent page's "Agent
    process" card. Site status carries `agentWarnings` (Status page banners); the heartbeat's `metrics` adds
    `agentRssBytes`, `agentCpuPct`, `agentWarnings` (the cloud stores metrics as sent).

113. **A site is standalone (Portainer-like, with commit history)** (user: "VM agent is also a stand alone, it don't
    require cloud to do everything; I should be able to update image, create new containers, edit files, create files,
    create variables, secrets, delete containers — everything should be allowed … keep docker as single source of truth …
    detect the diff and show it as uncommitted changes or auto commit them as UNKNOWN"). SUPERSEDES the local-edit rules
    of §11 / the validation table (V_LOCAL_ADD_REMOVE, V_LOCAL_DEFINITION, V_LOCAL_CONTAINERS, V_LOCAL_TAG,
    V_LOCAL_DEVTOOLS are gone).
    (a) **Local edits = everything** of the deployment config except the list of sites: variables / secrets / files
        (add, redefine, remove), containers (add, duplicate, rename, remove, every field, the image), the site's volumes,
        edge, settings incl. the label, dev-tool presets. Kept: `V_LOCAL_SITES` (sites are added / removed in the cloud)
        and `V_LOCAL_VOLUME_CONTENT` for a volume's persist-to-cloud setting (it belongs to the cloud content versions,
        §21 #101). Normal validation still blocks a broken result (a removed variable still used → V_REF, port clashes …).
        `checkLocalEdit` TS + Go mirror; agent GUI: Add / Edit / Remove on Variables, Secrets, Files, Config › Containers
        (+ Duplicate), the shared forms unlocked in agent scope (`agentLock` locks only a container's serviceSlug /
        version: picked with "Change image…", LOCK_IMAGE_PICKER). Every change is ONE commit of the agent branch through
        the update engine (LOCK → pre-update SNAPSHOT → APPLY → VERIFY → COMMIT), so History → revert / reset rolls it
        back (containers + volumes + config) exactly like a cloud merge.
    (b) **Site images**: a container may run an image that is not in the cloud catalog. Model (backward compatible): the
        container keeps `serviceSlug` + `version`; the catalog entry is a SITE image — `serviceSlug` = the repository name,
        `version` = `local-<12 hex of the docker image id>`, `digest` "" (no blobs in the cloud), `configDigest` = the
        docker image id, NEW optional `ImageSpec.reference` = the pullable docker reference, pinned
        `<ref>@sha256:<repo digest>` when the registry gave one (absent for an image built on the host). The same shape
        drift-adopted images had since §21 #104 (`localImageSpec`). `POST /_opm/api/images/resolve` { reference | imageId,
        pull?, username?, password? (this pull only, never stored) } → pulls through the host's Docker when missing
        (X-Registry-Auth), tags `opm/cfg:<hex>` + the readable tag, remembers the entry in kv `site-images`;
        `GET /_opm/api/images` = knownCatalog (head ∪ the 10 recent cloud commits' catalogs ∪ drift-adopted ∪ site
        images) + the tagged images of the host; `/config` also answers `knownCatalog`; edits validate against it and
        the commit carries the used entries in its catalog. A host that does not have a site image (another site, or
        removed from Docker) pulls its reference again before the apply (stager + apply: EnsureReadable); a moved tag
        (different image id) fails with "change the container's image". GUI: ImagePicker (Catalog · Docker reference ·
        On this host), "Change image…" on the container editor, "Add container" = pick an image.
    (c) **Cloud**: commits / merges / previews / the diff of a deployment accept the site images of the target head's
        catalog and of the merged source commit (`withSiteImages`; no V_REF), so a site's commit with a site image merges
        into the deployment and later cloud commits keep it. Cloud editor: the head's site images are read from the head
        commit and shown as "site image <reference>". GitHub mirror: `image:` = the site image's reference.
    (d) **Docker = source of truth**: the drift scan (§21 #104 f) keeps reading every change made outside OPM. Per site
        **drift policy** (`GET / PUT /_opm/api/branch/drift-policy` and `/_opm/api/sites/{site}/branch/drift-policy`,
        kv `drift-policy`): `ask` (default — uncommitted, commit or discard on the Branch page) | `auto-commit` (once the
        same drift is stable for 30 s and no update runs, the agent commits it itself: kind `drift`, author
        `{ kind: "unknown", email: "UNKNOWN", name: "UNKNOWN (changed in Docker)", siteSlug }` — `CommitAuthor.kind` gains
        `unknown`; persist-to-cloud volume content and drift notes still wait for a person). A drift commit applies
        nothing (the server already runs it); revert / reset of it runs the engine with its pre-update snapshot.
    (e) **Offline**: all of the above works without the cloud; commits are pushed through the outbox when it is back.
114. **Tunnelled downloads stream, never redirect** (user: "when i give access to internet, then it only makes minimum
    calls … transfer files … I am not able to transfer the file to the server"). Bug: through the browser tunnel (§9.10)
    every agent download (`/v1/agent/blobs/*` images + files, `/v1/agent/transfers/*/download`, `/v1/agent/binary`)
    answered 302 to a signed `/_blob/…` / GCS URL; that target allows only the frontend origins, so the GUI tab
    (`http://<host>:8080`, any address) dropped the response (status 0) and only small JSON calls worked. Fix (cloud only,
    no agent upgrade needed): `noRedirectFor(headers, relayedBy)` = relayed OR the request carries `Origin` (only a browser
    sends it; the agent never does) → 200/206 stream from the API (open CORS). Direct agent downloads keep the redirect.
    Uploads (to-cloud transfers) still never use the tunnel (§9.10).
115. **Docker-only sites: the GitHub branches run without any OPM code** (user: "github repo is not supposed to use the
    agent/on-premise anything … in last resort … fetch the github repo there, then manually docker install, and docker
    compose up … auto setup portainer as our GUI for docker and caddy for routing … in its readme there should be exact
    steps … first pull, then create a new branch, complete everything then push … for each site; then for new changes
    just run the git cmd to merge from cloud and docker compose update"). Replaces the opm-render runtime of #106 (d–f).
    (a) **Branch per machine**: `deployment/<slug>/site/<site>` (templates: `template/<slug>/site/<site>`), created on the
        machine from the cloud branch, merged with new cloud commits, pushed by the machine; the mirror never writes,
        rebuilds or deletes it. The site README gives the exact commands (Docker install, clone, `git switch -c`, site.env,
        deploy, push; update = fetch + merge + deploy + push; local change, roll back, volume backup, troubleshooting).
    (b) **Tree** (export format 2): README.md, .gitignore (`site.env`, `.env`, `.runtime/`, `.data/`), and per site
        README.md, deploy.sh, site.env.example, docker-compose.yml, setup.sh, init.sh, files/ (`<slug>` byte for byte,
        `<slug>.template` with placeholders), templates/{steps,secrets,volumes}/, volumes/<v>/ (starter content; archives
        `<v>.seed.tar.gz`; > 100 MB skipped + `<v>.skipped.txt`), edge/{caddy.json | caddy.template.json, pages/, tls/,
        Dockerfile for dns-01}. No `opm/` folder, no binary, no `opm.*` label. `ExportRequest.tool` is gone,
        `ExportRequest.repository` ("owner/name") puts the clone URL into the READMEs.
    (c) **Symbolic filters**: `expr.Symbolic` (a hook that answers only for strings holding the exporter's sentinels)
        turns `filter(value with a deferred input)` into a derived value `OPM_D_<12 hex of sha256(filter, args, input)>`
        recorded as a step (input = a template with `${NAME}`). Steps are computed by setup.sh with busybox (upper /
        lower ASCII, trim ASCII white space, base64, url, sha256, sha512, int, json) and `mosquitto_passwd` in the
        official `eclipse-mosquitto:2.0.22` image (random salt; recomputed only when the input changes). `default` and
        `join` are not symbolic (a sentinel is never empty; join of texts stays text).
    (d) **deploy.sh** (same for every site, plain sh, official images only): `setup.sh values` (busybox
        `1.37-musl`, the calling user: site.env `NAME=value` as written, surrounding quotes stripped; required values +
        `PORTAINER_ADMIN_PASSWORD` ≥ 12 characters unless `PORTAINER=off`), `setup.sh steps` (+ the mosquitto rounds),
        `setup.sh render` (templates → `.runtime/` with the agent's modes; an unchanged file is not touched, a changed one
        is renamed into place; `.env` = every value double-quoted (`\\`, `\"`, `$$`, `\n`), `FILES_SHA_<SERVICE>` = sha256
        of the files a service mounts, `COMPOSE_PROFILES`), then `docker compose up -d --remove-orphans` (extra arguments
        passed on; `--render-only` stops before it).
    (e) **Compose**: values `${NAME:?run ./deploy.sh}` from `.env` (plain `docker compose ps / logs` work); label
        `files.sha256: ${FILES_SHA_<SERVICE>:-}` on every service that mounts files and on the edge, so a changed file
        recreates exactly those services (the agent restarts them); health http / tcp with the static busybox of the
        `tools` volume at `/.tools` (`wget -S` status line / `nc -z`, the container's own address), exec = CMD (expected
        exit code ≠ 0: busybox sh); `init` (busybox) = seed-once (`.opm-seeded`) + installs the busybox; `edge` =
        `caddy:2.11.7` (TLS files at `/etc/caddy/tls`); `portainer` = `portainer/portainer-ce:2.45.2` (LTS) in profile
        `portainer`, `--admin-password-file`, port `${PORTAINER_HTTPS_PORT:-9443}`, Docker socket, volume `portainer-data`.
        Project name `<slug>-<site>`; helper names `init` / `edge` / `portainer` (suffix `-2` … when a container takes one).
    (f) **Proof**: `TestComposeEquivalenceDocker` (real agent vs `./deploy.sh` on the same Docker): 0 differences, plus
        Portainer login with the site.env password, no re-seed on a second deploy, and a File change that recreates only
        the service mounting it. `TestExportFixture` runs `deploy.sh --render-only` and `docker compose config` on real
        Docker. `agent/cmd/opm-render` is removed; the backend build compiles only `opm-export.wasm`.

116. **The image prune keeps the images of an update in flight** (an update failed on one site with "image
    code-shell:v3: image opm/cfg:<hex>: not found" and rolled back, while the same commit applied on the other site).
    The hourly housekeeping `pruneImages` removes every `opm/cfg:<hex>` image that no container uses and that
    `referencedBlobs` does not list (head, open proposals, kept snapshots). In branch mode (§21 #104) an update holds its
    target revision in no proposal, and it is not the head until it commits; the images it loaded in step 3 are used by
    no container until step 4 creates them. A prune falling in that window removed the image of a container created
    late in the update (a "create", so no older container of that service kept anything in use). Now
    `referencedBlobs` also lists (a) the target revision of every update this site has not finished (`updates` row with
    `finishedAt` NULL, the plan carries the revision) and (b) the cloud commits staged for a merge (`branchRevs`). The
    same set keeps their blobs from `pruneBlobs`. Test: `TestReferencedBlobsKeepsInFlightUpdate`.

117. **Persist-to-cloud volume content is committed and merged like the rest of the config — no Send / Apply** (user:
    "you need to push on commit, and fetch on merge there should be no seperated send/accept idea then commit & merge
    pipeline. and if someone has changed from server something then it shows as non eq volume on merge time so we can
    choose wether we want to merge or not, and on cloude we can always merge from the site to cloud config as before").
    Decisions (user): a change made on the server stays **uncommitted until a person commits it** (no auto-commit); a
    revert / reset rolls the content back too, **with the conflict check**. **Supersedes** #101 (f) cloud copies as the
    editing surface, (g) Send to site, (h) the separate plan / apply, (i) pending pushes and snapshot requests, (j) the
    cloud "Cloud copy" tab and the agent "Apply cloud version" flow, #104 (f) / docs §5 "Leave to volume sync", #105 (f)
    / #107 (f) "persist-to-cloud content is data with its own versions", and the `/live` drift item.
    (a) **Content = the seed tree of a persist volume.** For a volume with `persist`, `seed` (kind `tree`; null = empty)
        is the volume's COMMITTED content, not starter content: the cloud editor (Volume › Content) edits it as pending
        changes, Commit stores it with its bytes (#107), compare / cherry-pick / merge move it per file
        (`sites/<s>/volumes/<v>/content/<path>`, key grammar unchanged). Rules (`V_VOLUME_SEED`): only kind tree;
        entries are literal (render `none`); the persisted-version limits (10000 files, 5000 empty folders, in total at
        most the volume's `persist.maxBytes`); no path under `persist.exclude`. Volumes without persist keep the round-E
        starter-content rules.
    (b) **Fetch on merge.** Every apply on a site (merge, commit of GUI edits, fast-forward, revert, reset, discard)
        whose target tree of a persist volume differs from the tree of the site's head converges the live folder to it
        (`converge`: excluded paths and `.opm-seeded` untouched, atomic writes, nothing through a symlink) at apply step
        2b — after the pre-update snapshot (the volume's backup) and before containers start; the containers that mount
        the volume and would otherwise be kept running are stopped before the write and started again in step 4. The
        blobs are staged with the revision. A brand-new or empty volume is simply written.
    (c) **Server changes = uncommitted items.** The drift scan (§21 #104 f) reads each persist volume's live tree
        (excludes applied; blobs kept in the agent blob store for the diffs and the push) and expresses it as the
        volume's seed in the live config, so the Branch page lists each changed file as an uncommitted item (origin
        `volume-content`). These items never block a merge or a commit of GUI edits; committing them writes a drift
        commit (nothing to apply). A live tree that cannot be expressed (over the limits) is a drift note.
    (d) **Merge time shows the server's side.** A merge from the cloud takes "ours" of a persist volume = the LIVE tree
        (head + its uncommitted content changes): files only the cloud changed are taken, files only the server changed
        are kept (and recorded by the merge commit), files both changed are **conflicts** (Keep this site's / Take the
        cloud's, with the per-file diff). The live tree hash is recorded with the merge run; if the folder changes
        before the apply writes it, the apply fails ("changed on the server since the merge was reviewed") and rolls
        back. Revert / reset / fast-forward / commit of GUI edits: a persist volume with uncommitted server changes
        whose content the target changes refuses with 409 `uncommitted-drift` ("commit or discard the server changes
        of volume X first"); after that commit the 3-way revert decides per file as usual.
    (e) **Site → cloud.** Before an agent-branch push the agent uploads every File / volume-content blob the commits
        reference that the cloud lacks (`agentCheckBlobs` + `agentPutBlob`, now allowed for any enrolled site, ≤ 24 MiB
        per file); the push waits in the outbox until they are up. The deployment takes a site's content from that
        site's branch (Compare / cherry-pick, unchanged).
    (f) **Removed**: cloud `sendVolumeContent`, `cancelVolumeContentPush`, `requestVolumeSnapshot`,
        `createVolumeContentVersion`, `restoreVolumeContentVersion` → 410 gone (hint: commit the content). The history
        of the old cloud copies stays readable (`listVolumeContentVersions`, `getVolumeContentVersion`). Agents ≥ 0.19
        never upload snapshots and answer 410 on `/volume-sync/*/apply…` and `keep-live`; `persist.auto` stays in the
        schema and is ignored. Agent GUI Docker › Volumes: "matches the committed content" / "N files changed on the
        server (not committed)" with a link to the Branch page.
    (g) **Migration** `backend/scripts/migrate-l.ts` (over the API of a running cloud; dry run / --apply, idempotent):
        per deployment, every persist volume that has a cloud copy gets `seed` = the newest version's tree (render
        none; the content the cloud last held, incl. an edit not sent yet), in one commit "Persist-to-cloud volumes:
        the content is committed (§21 #117)"; a version that breaks the rules of (a) is reported and skipped. Pending
        pushes / snapshot requests stay as they are (agents ≥ 0.19 ignore them). The first merge on a site then
        compares its live folder with that content file by file (conflicts where they differ).
    (h) **Compatibility**: agent **0.19.0** (`VOLUME_CONTENT_COMMIT_MIN_AGENT`). An older agent treats the seed as
        starter content (only an empty volume is written): the content page of a persist volume warns for that site.

118. **The agent GUI of a site, opened in the cloud** (user: "we should be able to access the server from cloude. atleast
    the opm GUI should be open able from the cloud … <cloud>/app/deployments/{deploymentId}/remote/{siteId}/* -> this
    will serve the agent's opm site … cloud login … cloud will already know the deployment key … cloud login email will be
    auto used … By default this is enabled, but we can turn it of … server will never ping or make any api calls to site,
    instead site will ask server … similar fation of tunneling system as … browser based internet"). Contract
    `shared/src/remote-gui.ts`. Later (not now): `{siteId}-{deployId}.<cloud domain>` serving the whole site with its ports.
    (a) **URL + session.** `<cloud>/app/deployments/{deploymentId}/remote/{siteSlug}/_opm/…` serves the site's agent GUI
        (`openRemoteGui`, perm deployment.config.publish, console: Status › "Open agent GUI" per site, a new tab). It sets
        an HttpOnly cookie `opm_remote_<site>` (Path = the remote prefix, SameSite Lax, 8 h sliding) naming a remote
        session (person + site) and lists the site in the cloud view `remoteGui { siteSlug, until }` (agentEpoch+1 when
        it was not listed). No cookie → a 401 page that says to open it from the console. Activity `remote-gui.opened`.
    (b) **The site asks; the cloud never calls.** An agent ≥ 0.19 whose site is listed (and whose setting is on)
        long-polls `GET /v1/agent/remote-gui/next?wait=25` (signed; only its own site's requests, ≤ 8 per answer) until
        `until`; each browser request (method, `/_opm` path + query, a whitelist of headers — never the cloud cookie or
        auth —, body ≤ 16 MiB, the person) is handed out once; the agent answers with `POST
        /v1/agent/remote-gui/{reqId}/result` (raw body ≤ 64 MiB, X-OPM-Remote-Status, X-OPM-Remote-Headers base64 JSON).
        A browser request waits ≤ 60 s (504 / 503 "not connected" pages). The hub is in memory: the single-process VM
        cloud (§21 #108) only; `openRemoteGui` answers 503 unavailable on other clouds.
    (c) **As the cloud person, no key, no prompt.** The agent runs each request in process against its own GUI handler
        with one agent session per cloud session (sessions row ip `cloud`, user agent `OPM cloud · <email>`, activity
        `remote-gui.session`) and an actor token for the cloud email: `GET /_opm/api/session` answers
        `remote { email, name, actorToken, expiresAt }`, the GUI adopts it (no login page, no "Who is working?" popup;
        the chip says "through the cloud"; Change person / Log out hidden). Every action is recorded under that email.
    (d) **Paths.** The unchanged agent GUI build runs under the prefix: in text/html, JavaScript, CSS and manifests every
        `/_opm` right after a quote, backtick, `(`, `=` or whitespace and before `/`, a quote, backtick, `?`, `#` or `)`
        becomes `<prefix>/_opm` (`rewriteRemoteText`; ETag dropped, `Cache-Control: private, no-cache`); a `Location`
        under /_opm is re-prefixed; `Set-Cookie` of the agent never reaches the browser; JSON passes unchanged.
    (e) **On by default, switched off on the site.** kv `remote-gui { enabled }` (absent = on); Agent page card "Remote
        GUI through the cloud" (state, who works through the cloud now, Switch off / on), `GET / PUT
        /_opm/api/agent/remote-gui`; PUT through a remote session → 403 (switch it off on the site). Off: no polling,
        the open remote sessions end at once. Activity `agent.remote-gui-changed`.
    (f) **Event streams**: one without `follow=1|true` ends by itself (the lines of the range, then `end`) and is carried
        as one answer (the logs pages with follow off); a follow stream → 501 page. Another site through the mesh works
        as usual (the remote agent relays, §21 #111).
    (g) **WebSockets (terminal, MQTT client) are relayed over the site's HTTP polls** (agent 0.19.5; user: "some client
        might have refused the outbound socket connection only kept the default http connections"). The browser
        upgrades at the cloud; the cloud makes a stream and hands the request out with `ws { streamId }`; the agent
        dials its own GUI port on 127.0.0.1 as the cloud person (session cookie + actor token, same origin) and answers
        the upgrade's result (101 + Sec-WebSocket-Protocol, or the refusal's status). Then the agent only makes HTTP
        calls: `GET /v1/agent/remote-gui/ws/{streamId}/recv?wait≤30` (the browser's messages; `closed` = the browser
        left) and `POST …/send` (its messages, base64 frames `text | binary | close`, those within 15 ms in one call,
        ≤ 8 MiB). The site never opens a socket to the cloud. The terminal's `wsPath` (JSON, not rewritten) gets the
        page's base in the GUI.
    (h) Tests: `shared/src/remote-gui.test.ts`, `backend/test/42-remote-gui.test.ts` (local adapter: cookie, hand-off,
        isolation, rewriting, the WebSocket relay, SSE without follow carried / with follow 501), `agent/internal/agent/remotegui_test.go`,
        `remotegui_ws_test.go` (a host shell through a fake cloud); checked end to end with a real agent and a browser
        (navigation, deep-link reload, an actor POST, the 403 switch).

119. **Storage at a glance + moving the agent home to another disk** (user: an agent upgrade was refused "because there was
    no enough space" (the §21 #112 floor: a download that would leave less than min(5 % of the disk, 2 GiB) free) —
    "in latest GUI also give option to see the available volume size and how much is free and let me move the complete
    opm dir to another place").
    (a) **Agent › Storage** (`GET /_opm/api/agent/storage`): every disk OPM uses — the agent home, Docker's data root
        (`/info` DockerRootDir: images and container layers, usually the biggest), each volume with its own location —
        grouped per filesystem with total / used / free and the floor; "nearly full" below 2 × the floor (downloads,
        agent upgrades, snapshots and volume copies are refused below the floor). The size of the agent home (walked in
        the background, kept 10 min) and the last move.
    (b) **Move agent home…** (`POST …/agent/move-home/check { to }`, then `POST …/agent/move-home { to, typed: MOVE }`):
        refused unless the folder is absolute, outside / not containing the home, empty or absent with an existing
        parent, the target disk has the home's size + 5 % + its floor free (another disk), the agent runs under
        systemd, no update runs, and no volume / File has a fixed location inside the home. The agent stops this site's
        containers, checkpoints opm.db, writes `<home>/move-home.json` (prepared) and starts `opm-agent move-home`
        (systemd-run, hidden command, `internal/movehome`), which stops the service and moves the folder: the same
        filesystem → one rename; another → `cp -a`, every file verified (count + bytes), the old folder renamed
        `<home>.moved-<time>`; then agent.json `dataDir` (a .bak first), the systemd units + boot guard for the new
        folder, and the service started. At start the agent rewrites the absolute paths opm.db keeps (blobs, debug log
        index, transfer paths), re-applies its head (the containers get the new mount paths) and records
        `agent.home-moved`. A failure leaves everything at the old place (agent.json untouched, a partial copy removed),
        restarts the agent there and starts the stopped containers (`agent.home-move-failed`).
    (c) **The old copy** stays until "Delete old copy…" (typed DELETE, `…/move-home/delete-old`, `agent.home-old-deleted`).
    (d) Docker's own data root is not moved (a Docker setting: `data-root` in /etc/docker/daemon.json).
    (e) Tests: `internal/movehome` (copy incl. modes + symlinks, rename, refusals, failure; also on Linux),
        `TestMoveHomeAgentSide` (check, path rewrite, once). Shipped as agent 0.19.1.

120. **`docker image prune -a` is not an image change; the install script asks for the agent home** (a site ran
    `docker image prune -a`; every container then showed as running a new local image, that drift was committed and
    every later merge failed validation: "image redis:local-37aa… has no volume data / port service …").
    (a) **Cause.** On the containerd image store (Docker ≥ 29) an image id is the manifest digest, so the agent finds the
        catalog image of a container through its integrity tag `opm/cfg:<config hex>`. The prune removed those tags (the
        running containers keep the images), the lookup failed and the drift scan adopted every image as a site image
        (`<svc>:local-<id>`, ports `p<number>`, no named volumes).
    (b) **Fix.** A container the agent created FROM `opm/cfg:<config hex>` (its `Config.Image`) still runs that image
        when the tag is gone (a tag that points at another image is still a change): the drift scan matches it and puts
        the tag back on the container's image (log "integrity tag restored"); `ImagePresent` does the same before an
        apply would load the image again (the image's layers are compared with the config when the config blob is
        there). Test `TestRestoreIntegrityTagDocker`.
    (c) **A site that already committed such drift**: revert that drift commit on the site's Branch page (agent ≥
        0.19.2: the tags are restored, nothing is downloaded), then merge the cloud commit.
    (d) **Install script — the agent home.** A fresh install asks for the agent home (default /var/lib/opm, showing the
        free space; `OPM_DATA_DIR=/path` without a terminal) and writes `dataDir` into agent.json. A reinstall / upgrade
        to ≥ 0.19.1 shows the home's size and the free space and offers to move it (`OPM_MOVE_HOME=/path` without a
        terminal): the same checks as §21 #119, the site's containers stopped, `move-home.json` prepared, `opm-agent
        move-home` run; a failure leaves the home where it was and starts the containers again.

121. **Every site is managed from any site** (user: "it is very likely a client might not open the port … to the public
    server for some servers, so one site can let me manage all sites is very important"; the Storage card of app2 was
    missing on app1's agent GUI). The agent page's cards of another site come over the mesh like the rest of the
    per-site API: `GET /_opm/api/sites/{site}/agent/health` (always 200, ok in the body), `…/agent/storage`,
    `POST …/agent/move-home/check | move-home | move-home/delete-old`, `GET | PUT …/agent/remote-gui` (registered on the
    site mux: self in process, another site `/_opm/peer/site/…`). Through the cloud no site's remote GUI is switched
    (the forwarding site refuses: the target cannot see that the person is remote). Volume "Reset to starter content"
    works for another site too (its flow already followed the target's intent). Tests:
    `agent/internal/agent/site_agent_routes_test.go`, `tests/rounde-volumes.spec.ts`.

122. **A multi-site branch plan stages every site before it applies** (a 4-site "Fast-forward all sites" rolled back
    with "apply failed on app2: image <service>:v4: blob … missing"). A proposal is
    staged by every site when it arrives, but a fast-forward of several sites (and any multi-site merge plan) staged
    only the coordinator; another site then applied without the image layers and files. The snapshot step (all sites in
    parallel, before any apply, 30 min) now stages the site's revision unless it is staged already (`ensureStaged`,
    rollbacks excluded): downloads happen before any container stops; a failure (e.g. the free-space floor) rolls back
    before anything changed. Test: `TestTwoSiteBranches` (the peer has a ready staging row after the fast-forward).

123. **Shared files: one bucket per deployment instead of per-site send / receive** (user: "transfer GUI should be more
    like a bucket … you can see any site or cloud's published contents and you can download it to site or browser using
    site or using cloud … you can also publish … nothing gets auto downloaded"). Contract `shared/src/files.ts`.
    (a) **Publish** from the cloud console (browser → the cloud: `publishFile` + an upload, purpose `file`) or from any
        site (a browser upload through that site's agent GUI, or a path on its server — the §21 #100 picker policy). A
        site makes the fileId (ULID) and registers the file through the outbox (`registerAgentFile`, metadata only), so
        it publishes offline too. ≤ 2 GiB, note ≤ 500.
    (b) **Only the listing spreads**: the cloud view carries every file (+ tombstones of 30 days) to every agent; sites
        also merge each other's listing over the mesh (`/_opm/peer/files`, every minute), so sites without the cloud see
        each other's files. The bytes stay where they were published (user choice: "stay on the source, fetched on
        request"). Copies: `cloud.stored` + `sites[]`.
    (c) **Download on request only.** A site gets a copy (`fetchToSite`, a job with progress) from a holding peer over the
        mesh, else from the cloud copy, else it asks the cloud to fetch one (`requestAgentFileFetch` → waiting-cloud).
        The cloud gets a copy by listing the file in `CloudView.fileFetches` for a holding site, which uploads it in
        parts (`startAgentFileUpload`); the cloud never calls a site. Every place that got a copy keeps it and serves it.
        On a site the copy downloads to the browser or is saved to a server path (must not exist; 0640); in the console
        it downloads to the browser ("Get into the cloud" first when only a site holds it).
    (d) **Unpublish** (deployment.transfers write; user choice: anyone with write, no expiry) — everywhere: tombstone,
        every copy deleted (a copy saved to a server path stays). From a site: the cloud now, else the outbox; peers
        learn it from the listing. "Drop this site's copy" frees disk (refused for the only copy).
    (e) Copies on a site: `<agent home>/shared-files/<fileId>` (`files/` holds the configured Files; `shared-files` is
        reserved). SQLite `files(fileId, data, localPath, listedBy, updatedAt)`.
    (f) **Transfers**: the agent GUI and console pages are replaced (agent GUI: same URL, "Files"; console:
        `/app/deployments/{id}/shared-files`, the per-site transfer URLs redirect there). Existing transfers become files
        with fileId = transferId (cloud `scripts/migrate-m.ts`; each agent converts its copies once on start); a new
        agent no longer downloads legacy to-site transfers by itself. The legacy endpoints stay for old agents.
    (g) Activities `file.published`, `file.fetched`, `file.downloaded`, `file.saved`, `file.copy-dropped`,
        `file.unpublished`. Tests: `agent/internal/agent/files_test.go` (two sites without the cloud: publish browser +
        path, listing over the mesh, nothing by itself, copy on request, save + 409, drop + only-copy 409, unpublish
        spreading; transfers → files), `backend/test/43-files.test.ts`, `frontend/tests/files-123.spec.ts`, the agent GUI
        Files specs.

## Appendix B. Activity catalog

| type | origin | written when | data |
|---|---|---|---|
| install.enrolled | agent (+ `_cloud`) | enroll | agentVersion, publicKeyFingerprint, ip, hostname |
| agent.started | system | process start | agentVersion, headRevId |
| agent.port-bind-failed | system | a listener cannot bind (once per port; retries are silent) | port, purpose, error, owner? |
| login.succeeded / login.failed / logout | agent | §4.6 | sessionId?, siteSlug, keyVersion?, ip, userAgent |
| actor.declared | agent | actor popup | email, sessionId, siteSlug, userAgent, ip |
| access-key.rotated | cloud (`_cloud`) | rotate / Regenerate (+ audit deployment.access-key-rotated) | version |
| access-key.revealed | cloud (`_cloud`) | View on Setup (`revealAccessKey`, §21 #84; + audit deployment.access-key-revealed) | version |
| access-key.received | system | a site adopts a newer hash | version |
| deployment.created / deployment.updated | cloud | create / patch | label, templateId? / changed fields |
| draft.template-synced | cloud (`_cloud`) | Template sync applied (§21 #94; + audit deployment.template-synced) | templateId, fromVersion, toVersion, relinked, previousTemplateId?, taken, skipped, conflicts, draftVersion, keys |
| deployment.template-unlinked | cloud (`_cloud`) | Unlink template (§21 #94; + audit deployment.template-unlinked) | templateId, baseVersion |
| install-token.created / install-token.revoked / agent.revoked | cloud | setup actions (created: + audit deployment.install-command-created; Regenerate = revoked per replaced token + created) | siteSlug, tokenHash?, expiresAt? |
| install-token.revealed | cloud | View of an active install command (`revealInstallCommand`, §21 #84; + audit deployment.install-command-revealed) | siteSlug, tokenHash |
| install-command.revealed | cloud | View of an enrolled site's upgrade / reinstall command (`revealUpgradeCommand`, §21 #92; + audit deployment.upgrade-command-revealed) | siteSlug, tokenHash, kind |
| install-command.regenerated | cloud | Regenerate of an enrolled site's upgrade command (`regenerateUpgradeCommand`, §21 #92; + audit deployment.upgrade-command-regenerated) | siteSlug, tokenHash, revoked[] |
| proposal.published | cloud | publish | revId, parentRevId, seq, summary |
| proposal.created | agent | local proposal | revId, parentRevId, seq, summary, sections |
| proposal.staged | agent/system | staging ends on a site | revId, result (ready / failed), error, durationMs |
| proposal.stale | cloud / agent | parent ≠ head | revId, parentRevId, headRevId |
| proposal.superseded | cloud / agent | newer proposal | revId, byRevId |
| proposal.approved | agent | ACCEPT | revId, approver |
| update.started | agent | coordinator starts | updateId, revId, kind, sites |
| update.snapshot | agent | pre-update snapshot done | revId, snapshotId, sizeBytes |
| update.applied | agent | commit on that site | revId, seq, durationMs |
| update.failed | agent | a step failed | revId, step, siteSlug, containerSlug? (the failing container instance), error, failures? (§21 #87: [{containerSlug, error, exitCode, oomKilled, restartCount, ports, keptAs, logTail, hint? (§21 #93)}], ≤ 12 KiB) |
| update.rolled-back | agent | rollback done on that site | revId, snapshotId, durationMs |
| update.abandoned | agent | abandonUpdate | updateId, revId |
| update.terminated | agent | terminateUpdate, peer terminate, a newer update, `opm-agent repair` (§21 #110) | updateId, siteSlug, reason |
| update.recovered | agent | terminateUpdate `rollback`, `opm-agent repair --rollback` (§21 #110) | updateId, siteSlug, snapshotId, reason, durationMs, error? |
| config.local-change | agent | a local proposal applied | revId, sections |
| snapshot.created / downloaded / imported / deleted / rolled-back | agent | backups | snapshotId, reason?, sizeBytes?, includeImages?, newRevId? |
| volume.seeded | agent | starter content written into a new / empty volume (round E) | siteSlug, volumeSlug, source (files / file / archive), entries |
| volume.reset-to-seed | agent | "Reset to starter content…" done (snapshot first) | siteSlug, volumeSlug, snapshotId, entries |
| volume.placeholder-filled | agent | §21 #101: a 0-byte former File mountpoint inside a writable volume got its content (migration transition, once) | siteSlug, volumeSlug, path, sha256, size, formerFileSlug |
| volume-content.snapshot | agent | §21 #101: snapshot of a persist-to-cloud volume taken (queued for upload) | volumeSlug, snapshotId, trigger, treeHash, fileCount, totalBytes, excludedCount, skippedCount, requestId?, pushId? |
| volume-content.snapshot-uploaded | agent | the snapshot became cloud version seq N | volumeSlug, snapshotId, versionId, seq |
| volume-content.snapshot-failed | agent | over the size cap / file count, scan error | volumeSlug, trigger, error |
| volume-content.conflict | agent | a pending push meets a live tree that changed since its base (once per push) | volumeSlug, pushId, versionId, conflicts, liveTreeHash, baseTreeHash |
| volume-content.applied | agent | "Apply cloud version" done | volumeSlug, pushId, versionId, resolution, written, deleted, keptLive, snapshotId, restarted[], fastForward? |
| volume-content.kept-live | agent | the push was declined (Keep live) | volumeSlug, pushId, versionId |
| volume-content.apply-failed | agent | the apply failed (users restarted) | volumeSlug, pushId, versionId, step, error |
| volume-content.version-created | cloud | cloud edit / restore saved as version seq N | siteSlug, volumeSlug, versionId, seq, origin, treeHash, fileCount, totalBytes, baseVersionId, added, removed, modified |
| volume-content.snapshot-requested | cloud | "Snapshot now" from the cloud | siteSlug, volumeSlug, requestId |
| volume-content.sent | cloud | "Send to site" (pending until the site applies / keeps live) | siteSlug, volumeSlug, pushId, versionId, baseVersionId, restart, supersededPushId |
| volume-content.push-cancelled | cloud | pending push cancelled | siteSlug, volumeSlug, pushId, versionId |
| transfer.sent / received / saved | cloud / agent | transfers (cloud → site: sent twice, `stage: started` at createTransfer and `stage: uploaded` at complete; agent: once when the file is queued on the site, §21 #100) | transferId, direction, fileName?, sizeBytes?, savedPath?, stage?, source? (`browser` \| `path`, agent), sourcePath? |
| secret.revealed | cloud / agent | View in RevealValue (cloud: + audit deployment.secret-revealed) | secretSlug, actor, where, siteSlug? |
| secret.regenerated | cloud | Regenerate in RevealValue (`regenerateSecret`, §21 #84; with draft.updated + audit deployment.secret-regenerated) | secretSlug, draftVersion |
| secret.fetched | cloud / agent | revision with secrets sent to a site | siteSlug, secretSlugs, revId |
| internet.tunnel.started / stopped / revoked / expired | agent / system | tunnel | leaseId, actor?, userAgent?, reqs?, bytes?, revokedBy?, previousActor?, lastPollAt? |
| debug.downloaded | agent | bundle with debug log | sites, fromSeq, toSeq |
| edge.reloaded / edge.reload-failed | system | Caddy load | revSeq, hosts, routes, durationMs / error |
| cert.issued / cert.renewed / cert.failed / cert.local-ca-downloaded | system / agent | certificates | hostname, mode, issuer?, notAfter?, error?, actor? |
| site.unreachable / site.reachable | system | peer ping state change | peerSlug, lastSeen |
| draft.reset-to-head | cloud | reset | headRevId |
| draft.updated | cloud | putDraftSection / putDraftSite | section, siteSlug?, draftVersion, paths (≤ 100 changed paths, never values), pathsTotal |
| transfer.deleted | cloud | deleteTransfer | transferId, direction, fileName, sizeBytes, siteSlug, previousStatus |
| activities.uploaded | cloud | manual .opmlog upload | signedBy, inserted, skipped, rejected, sites, bundle, sizeBytes |
| deployment.deleted | cloud | deleteDeployment (soft) | label |
| terminal.opened / terminal.closed | agent | a terminal attaches / ends (§7.4; both sites when forwarded) | terminalId, kind (container / host), target, command?, via? / forwardedTo?, durationSec?, exitCode?, reason?, closedBy?, bytesOut?, sessionId |
| container.restarted / container.stopped / container.started | agent | toolbox container actions (§21 #89 d) | containerSlug, ref, siteSlug, reason?, expectedRunning? (stop) |
| tool.netcheck | agent | a network check ran | from, check, target, ok, durationMs, summary |
| volume.downloaded | agent | a volume file / tar.gz link was issued | volumeSlug, path, kind (file / archive) |
| volume.emptied | agent | §21 #96 Empty finished | volumeSlug, siteSlug, opId, snapshotId, bytesBefore, stopped[], durationMs, fastForward? |
| volume.copied | agent | §21 #96 Copy finished | volumeSlug, siteSlug, opId, target { kind, path, volumeSlug }, bytes, files, preserve, copyId, stopped[], durationMs |
| volume.restored | agent | §21 #96 Restore finished | volumeSlug, siteSlug, opId, source { kind, ref, path }, snapshotId, bytes, files, stopped[], durationMs, fastForward? |
| volume.op-failed | agent / system | §21 #96 an action failed (or was interrupted by an agent stop) | volumeSlug, siteSlug, opId, kind, step, error, stopped[], durationMs |
| volume.copy-deleted | agent | §21 #96 a copy deleted | copyId, volumeSlug, path, sizeBytes |
| volume.action-forwarded | agent | §21 #96 the forwarding site of a mesh volume action | siteSlug, forwardedTo, method, action, status |
| devtools.query | agent | §21 #97 every dev-tools execution (SQL / CQL / Redis / HTTP; never result data) | DevToolQueryActivity: connectionId, connectionName, kind, source, target, op, statement (redacted ≤ 4 KiB), statements, durationMs, rows, rowsAffected, status, ok, error, readOnly, destructive, confirmed, queryId, via? |
| devtools.query-cancelled | agent | §21 #97 cancel | connectionId, connectionName, kind, queryId, wasRunning |
| devtools.connection-created / -updated / -deleted | agent | §21 #97 manual connection changed (duplicate = created) | connectionId, name, kind, target, readOnly, changed[]?, credentialChanged?, duplicatedFrom? |
| devtools.connection-tested | agent | §21 #97 Test | connectionId, name, kind, target, ok, latencyMs, error |
| devtools.credential-revealed | agent | §21 #97 a stored literal password / token / environment secret revealed | connectionId \| environmentId, name, field |
| devtools.mqtt-published | agent | §21 #97 MQTT publish | connectionId, connectionName, topic, qos, retain, bytes, ok, error, durationMs |
| devtools.mqtt-subscribed / -unsubscribed | agent | §21 #97 live subscription session opened / filter added / ended | connectionId, connectionName, sessionId, filters[], messages?, durationSec?, reason? |
| devtools.action-forwarded | agent | §21 #97 the forwarding site of a mesh dev-tools write | siteSlug, forwardedTo, method, action, connectionId, status |
| volume.file-written / -uploaded / -folder-created / -moved / -deleted | agent | §21 #96 file operations in a volume | volumeSlug, path(s) / from, to, sizeBytes, created / overwritten, entries, via? |
| docker.images-pruned | agent | "Prune unused images" | deleted, skipped, reclaimedBytes |
| agent.upgrade-started | agent | an upgrade starts on the OLD agent (GUI, cloud request, mesh run, `opm-agent upgrade`; §7.5, §21 #90) | upgradeId, from, to, via (gui / cloud / cli / mesh), requestId?, runId? |
| agent.upgraded | agent | the NEW agent came up healthy (written by it after the restart) | upgradeId, from, to, durationSec, via |
| agent.upgrade-failed | agent | an upgrade failed in any phase (also before the rollback record) | upgradeId, from, to, phase, error, via |
| agent.rolled-back | agent | the restored old agent read `upgrade/rolled-back.json` | upgradeId, from (failed target), to (restored version), reason, by (watcher / guard) |
| agent.restarted | agent | Restart agent / a config save with restart (upgrades write agent.upgrade-* instead) | reason, via (systemd / exec) |
| agent.config-changed | agent | agent.json safe settings saved from the Agent page | changed, before, after (never secrets) |
| agent.support-bundle-downloaded | agent | the support bundle download starts | sizeBytes, sections |
| agent.upgrade-requested | cloud (`_cloud`) | "Upgrade agent" / each request of "Upgrade all" (+ audit deployment.agent-upgrade-requested) | requestId, siteSlug, version, runId? |
| agent.upgrade-cancelled | cloud (`_cloud`) | a queued request / a run cancelled (+ audit deployment.agent-upgrade-cancelled) | requestId?, runId?, siteSlug? |

---------------------------------------------------------------------------------------------------------------------

## Appendix A. `shared/src/types.ts` (verbatim, normative)

```ts
/**
 * @opm/shared · types.ts
 *
 * NORMATIVE wire format of On-Prem Manager (OPM). Every document stored in Firestore / agent SQLite and
 * every API request / response body uses exactly these names. JSON on the wire. See SPEC-final.md.
 *
 * Conventions
 * - `Ts` is ISO 8601 UTC with "Z" (e.g. "2026-10-07T09:30:00Z"); milliseconds optional on input, the cloud
 *   writes millisecond precision ("2026-10-07T09:30:00.123Z"). Firestore stores Timestamps; the API converts.
 * - Optional fields (`?`) may be absent; `| null` fields are always present.
 * - "View" types are what the API returns (secrets masked). "Input" types are request bodies.
 */

// ───────────────────────────────────────────────────────────── common scalars
export type Id = string; //        Firestore auto id: ^[A-Za-z0-9]{20}$
export type Slug = string; //      ^[a-z][a-z0-9-]{1,39}$ (2–40 chars), immutable after create
export type Uid = string; //       Firebase Auth uid (1–128 chars)
export type Ts = string; //        ISO 8601 UTC
export type Sha256 = string; //    "sha256:" + 64 lowercase hex
export type Ulid = string; //      ^[0-9A-HJKMNP-TV-Z]{26}$
export type RevId = Ulid;
export type Expr = string; //      contains at least one "{{ … }}" block
export type Value = string; //     plain string or Expr, ≤ 4096 chars
/**
 * Icon reference (services, templates, tags; also Account.img and Deployment.image):
 *   null                → the entity type's default icon
 *   "lucide:<name>"     → a Lucide icon (^[a-z0-9-]+$), rendered from the curated set in shared/src/icons.ts
 *   "upload:<iconId>"   → an uploaded icon (icons/{iconId}; bytes at GET /v1/icons/{iconId}/content, public)
 *   "<name>"            → legacy bare Lucide name (read as "lucide:<name>")
 *   "https://…"         → legacy URL (Account.img / Deployment.image only)
 *   "data:image/…"      → only inside Revision.catalog (uploaded icons are embedded so offline agent GUIs can show them)
 */
export type Icon = string | null;

export interface Audit {
  createdAt: Ts;
  createdBy: Uid;
  updatedAt: Ts;
  updatedBy: Uid; // "system" when OPM itself changed it
}

/** The masked placeholder returned instead of a secret / password value. */
export const MASK = '•••' as const;
export type Masked = typeof MASK;

// ───────────────────────────────────────────────────────────── reveal (SPEC-final §21 #84: every credential is viewable + regenerable any time)
/** Seconds a revealed credential stays visible in RevealValue (both GUIs), then it is masked again. */
export const REVEAL_SECONDS = 60 as const;
/** Value-returning (reveal / regenerate) requests per person per minute (cloud: per uid; agent GUI: per session). */
export const REVEAL_RATE_PER_MINUTE = 30 as const;
/**
 * Common shape of every response that carries a credential value (reveal AND regenerate / create responses).
 * `value` = the text RevealValue shows and copies. Served with `Cache-Control: no-store`.
 */
export interface RevealResponse {
  value: string;
  hideAfterSec: typeof REVEAL_SECONDS;
}

// ───────────────────────────────────────────────────────────── errors
export type ErrorCode =
  | 'unauthenticated' // 401
  | 'session-expired' // 401 agent GUI
  | 'invalid-key' // 401 agent GUI login
  | 'actor-required' // 401 agent GUI write without actor token
  | 'forbidden' // 403
  | 'not-found' // 404
  | 'method-not-allowed' // 405
  | 'conflict' // 409
  | 'stale' // 409
  | 'gone' // 410
  | 'too-large' // 413
  | 'invalid' // 422
  | 'gate-closed' // 409 agent: approve / rollback gate is not open
  | 'locked' // 409 agent: another update holds the lock
  | 'no-internet' // 503 agent peer fetch-request
  | 'unavailable' // 503 a peer / the cloud cannot be reached
  | 'rate-limited' // 429 (Retry-After header)
  | 'internal'; // 500 (details.requestId)

export const ERROR_HTTP_STATUS: Record<ErrorCode, number> = {
  unauthenticated: 401,
  'session-expired': 401,
  'invalid-key': 401,
  'actor-required': 401,
  forbidden: 403,
  'not-found': 404,
  'method-not-allowed': 405,
  conflict: 409,
  stale: 409,
  gone: 410,
  'too-large': 413,
  invalid: 422,
  'gate-closed': 409,
  locked: 409,
  'no-internet': 503,
  unavailable: 503,
  'rate-limited': 429,
  internal: 500,
};

export interface ValidationIssue {
  /** JSON-ish path, e.g. "sites[app].config.containers[web].port[http].hostPort" */
  path: string;
  /** Stable code, see VALIDATION_CODES in validate.ts (V_*) or an expression code (E_*) */
  code: string;
  message: string;
  /** info (round E): advice only, never counted as a warning or blocker (e.g. V_VAR_OPTIONAL_NULL in templates) */
  severity: 'error' | 'warning' | 'info';
}

export interface ApiErrorBody {
  error: {
    code: ErrorCode;
    message: string;
    /** invalid → { errors: ValidationIssue[] }; internal → { requestId }; conflict → resource specific */
    details?: Record<string, unknown>;
  };
}

// ───────────────────────────────────────────────────────────── developer tools
export interface Registry extends Audit {
  registryId: Id;
  label: string; //       1–80
  url: string; //         https URL without path; default "https://registry-1.docker.io"
  username: string; //    1–255
  password: string; //    1–8192 (a GCP service-account key JSON for _json_key is ~2.4 KB), stored plain (prototype); NEVER returned except by reveal
  lastCheck: { at: Ts; ok: boolean; message: string } | null;
}
export type RegistryView = Omit<Registry, 'password'> & { password: Masked; isSet: boolean };

export interface Service extends Audit {
  /** THE id (one identifier per entity): document id services/{serviceSlug}; ^[a-z][a-z0-9-]{1,39}$, unique, immutable;
   *  the default containerSlug of its containers on a site (containers are instances: Container.containerSlug is their name) */
  serviceSlug: Slug;
  label: string;
  icon: Icon;
  description: string;
}

export type Platform = 'linux/amd64' | 'linux/arm64';
export type ImageKind = 'service' | 'job';
export type ImportMode = 'copy' | 'gateway';
export type ImportStatus = 'pending' | 'copying' | 'ready' | 'failed';

export interface ImagePort {
  name: Slug; //          unique in the image
  port: number | Expr; // 1–65535 or ONE whole block using var.* / deployment.id
  protocol: 'tcp' | 'udp';
  description: string;
}
export interface ImageEnv {
  name: string; //        ^[A-Z_][A-Z0-9_]*$, unique
  /**
   * The default. Absent = no default. Plain text or an expression (field imageEnv). When `options` is set it must be
   * one of the options (no expression). Legacy documents may hold string[] here = options with the first as default;
   * read them through normalizeImageEnv() (validate.ts).
   */
  value?: Value;
  /** allowed values ("choice"): 1–50 unique strings; containers must resolve to one of them (V_OPTION); null = free text */
  options?: string[] | null;
  required: boolean;
  secret: boolean; //     true → container must use secretRef (or an expression with secret.*); no default allowed
  description: string;
}
export interface ImageVolume {
  name: Slug;
  mountPath: string; //   absolute path, not "/"; may hold {{ }} (field mountPath: var, deployment, self, container incl. container.env.<NAME>) and must start with a literal "/"
  /**
   * What the image keeps at mountPath (volume-kind fix, §21 #83): 'dir' = a folder, 'file' = one file (e.g.
   * /data/flows.json). Always stored explicitly; a request without it (and legacy documents / revision catalogs, where
   * the key is absent) get `inferImageVolumeKind(mountPath)`: a last path segment with an extension → file, else dir.
   * Read it with `imageVolumeKind(v)`. Registry Volumes entries are always 'dir'. Containers: a folder data volume on a
   * 'file' image volume → V_VOLUME_KIND_MISMATCH (error); a single-file data volume on a 'dir' one → V_VOLUME_KIND_IMAGE (warning).
   */
  kind: ImageVolumeKind;
  description: string;
}
export type ImageVolumeKind = 'dir' | 'file';
/** An image volume in a register / patch request: kind optional (absent → inferred from the path and stored explicitly). */
export type ImageVolumeInput = Omit<ImageVolume, 'kind'> & { kind?: ImageVolumeKind };
export interface HealthCheckBase {
  intervalSec: number; // 1–3600, default 10
  timeoutSec: number; //  1–600, default 5
  retries: number; //     1–100, default 3
  startPeriodSec: number; // 0–3600, default 30
}
export type HealthCheck = HealthCheckBase &
  (
    | { type: 'http'; portName: Slug; path: string; expectStatus: number }
    | { type: 'tcp'; portName: Slug }
    | { type: 'exec'; command: string[]; expectExitCode: number }
    | { type: 'log'; pattern: string }
  );

export interface ImageLayer {
  digest: Sha256;
  sizeBytes: number;
  mediaType: string; // e.g. "application/vnd.oci.image.layer.v1.tar+gzip"
}
export interface ImageImport {
  mode: ImportMode;
  status: ImportStatus;
  error: string | null;
  sizeBytes: number | null; // sum of layers
  /** manifest media type of the platform manifest that was stored */
  manifestMediaType: string | null;
  configDigest: Sha256 | null; // image config blob (= docker image id)
  layers: ImageLayer[];
  startedAt: Ts | null;
  finishedAt: Ts | null;
}
/** A parsed docker reference `[host/]repository[:tag][@sha256:digest]` (parseImageReference in validate.ts). */
export interface ImageRefParts {
  host: string; //         "docker.io" for Docker Hub (docker.io / index.docker.io / registry-1.docker.io), else the registry host[:port]
  repository: string; //  Docker Hub single names get "library/" (redis → library/redis)
  tag: string | null; //   null only when a digest is given; "latest" when neither is given
  digest: Sha256 | null;
}
export interface Image extends Audit {
  /**
   * The OPM version of the image: the document id (services/{serviceSlug}/images/{version}), unique per service, immutable.
   * RE.version ^[a-z0-9][a-z0-9._-]{0,63}$ — e.g. v1, v2, 3.1.0. Containers reference serviceSlug + version; on a site the
   * image is tagged `<serviceSlug>:<version>` (e.g. northwind-backend:v1).
   */
  version: string;
  serviceSlug: Slug; //       parent (denormalized)
  /** the full docker reference to import, as entered: e.g. "docker.io/northwind/backend:3.0.9" (≤ 512) */
  reference: string;
  /** parsed + normalized `reference` (stored for queries / display) */
  source: ImageRefParts;
  /** registry whose credentials are used (matched by host; the user may override); null = public, no credentials */
  registryId: Id | null;
  digest: Sha256 | null; // manifest digest, set by import, immutable once set
  platform: Platform; //  immutable (identity)
  import: ImageImport; // identity: set by the import job only
  kind: ImageKind; //     editable (§21 #82: everything but the identity is editable)
  entrypoint: string[] | null; // editable
  command: string[] | null; // editable (containers may override)
  workdir: string | null; // editable
  user: string | null; // editable
  ports: ImagePort[]; //  editable: add / remove / rename (in-use renames + removals → 409 in-use, PatchImageRequest)
  env: ImageEnv[]; //     editable: add / remove / rename
  volumes: ImageVolume[]; // editable: add / remove / rename
  healthcheck: HealthCheck | null; // editable
  stopTimeoutSec: number; // 1–600, default 30, editable
  description: string; // editable
}

/** What an agent needs of an image; copied into Revision.catalog at publish so revisions are self-contained. */
export interface ImageSpec {
  serviceSlug: Slug;
  version: string;
  digest: Sha256;
  configDigest: Sha256;
  platform: Platform;
  manifestMediaType: string;
  layers: ImageLayer[];
  sizeBytes: number;
  kind: ImageKind;
  entrypoint: string[] | null;
  command: string[] | null;
  workdir: string | null;
  user: string | null;
  ports: ImagePort[];
  env: ImageEnv[];
  volumes: ImageVolume[];
  healthcheck: HealthCheck | null;
  stopTimeoutSec: number;
  /**
   * §21 #113 a SITE image (digest "", version `local-<12 hex>`, added on a site or adopted from Docker): the pullable
   * docker reference it came from (pinned `@sha256:` when known). Absent for catalog images / images built on the host.
   */
  reference?: string;
}
/** §21 #104 / #113: a site image has no blobs in the cloud (digest ""), it lives in the site's Docker. */
export const isSiteImage = (s: Pick<ImageSpec, 'digest'>) => !s.digest;

export interface NotFoundPage {
  notFoundPageId: Id;
  label: string; //      1–80
  htmlCode: string; //   ≤ 262144 bytes UTF-8
  createdAt: Ts;
  createdBy: Uid;
}

export interface Tag extends Audit {
  tagSlug: Slug;
  label: string;
  icon: Icon;
  color: string; // ^#[0-9a-f]{6}$
}

// ───────────────────────────────────────────────────────────── access
export type Action = 'read' | 'write' | 'publish' | 'admin';
export type DeveloperResource = 'registries' | 'services' | 'images' | 'notFoundPages' | 'templates' | 'tags' | 'accounts';
export type DeploymentScopedResource =
  | 'deployment.setup'
  | 'deployment.config'
  | 'deployment.secrets'
  | 'deployment.activities'
  | 'deployment.transfers';
export type Resource = DeveloperResource | 'deployments' | DeploymentScopedResource;
export type Scope = { kind: 'all' } | { kind: 'tags'; tagSlugs: Slug[] } | { kind: 'deployments'; deploymentIds: Id[] };
export interface AccessRule {
  resource: Resource;
  actions: Action[]; // 1–4 unique; must be valid for the resource (see access.ts VALID_ACTIONS)
  scope: Scope;
}
/** API view of a stored credential: metadata only (the value comes from its reveal endpoint). */
export interface StoredCredentialMeta {
  setAt: Ts;
  setBy: Uid;
}
export interface Account {
  uid: Uid;
  img: Icon; //         avatar: "upload:<iconId>", "lucide:<name>", legacy https URL, or null (initials)
  name: string; //      1–80
  email: string; //     unique, lower-case, immutable
  phone: string | null; // E.164 ^\+[1-9]\d{6,14}$
  access: AccessRule[];
  disabled: boolean;
  createdAt: Ts;
  createdBy: Uid; // "bootstrap" for the first admin
  lastLoginAt: Ts | null;
  /**
   * §21 #84: the last password an admin set ("Set new password…" or account creation), viewable with
   * revealAccountPassword until the person changes their own password (Auth tokensValidAfterTime moves → cleared).
   * null = none stored. The API returns only { setAt, setBy }; Firestore holds AccountDoc.lastSetPassword.
   */
  lastSetPassword: StoredCredentialMeta | null;
  /** the last password-reset link (Firebase oob link, 1 h), viewable with revealAccountResetLink until it expires / is used */
  lastResetLink: (StoredCredentialMeta & { expiresAt: Ts }) | null;
}
/** Firestore accounts/{uid}: the API Account + the stored values (never returned except by the reveal endpoints). */
export type AccountDoc = Omit<Account, 'lastSetPassword' | 'lastResetLink'> & {
  /** validAfter = Auth tokensValidAfterTime (ISO) right after the set; a later one = the person changed it → cleared */
  lastSetPassword: (StoredCredentialMeta & { value: string; validAfter: Ts | null }) | null;
  lastResetLink: (StoredCredentialMeta & { value: string; expiresAt: Ts; validAfter: Ts | null }) | null;
};
// ───────────────────────────────────────────────────────────── root audit log (SPEC-final §6.2)
/** What an audit entry is about. */
export const AUDIT_TARGET_TYPES = ['registry', 'service', 'image', 'notFoundPage', 'template', 'tag', 'account', 'deployment', 'upload', 'blob', 'icon', 'system', 'github'] as const;
export type AuditTargetType = (typeof AUDIT_TARGET_TYPES)[number];
/**
 * Every root-scope audit action. Human writes (every non-GET human request outside /v1/deployments/{id}/…) and
 * system jobs. Deployment-internal actions are activities (ACTIVITY_TYPES), not audit entries.
 */
export const AUDIT_ACTIONS = [
  'registry.created',
  'registry.updated',
  'registry.deleted',
  'registry.tested',
  'registry.password-revealed',
  'service.created',
  'service.updated',
  'service.deleted',
  'image.inspected',
  'image.registered',
  'image.updated',
  'image.deleted',
  'image.import-retried',
  'image.import-ready', //  system (import job)
  'image.import-failed', // system (import job)
  'not-found-page.created',
  'not-found-page.deleted',
  'template.created',
  'template.updated',
  'template.deleted',
  'template.committed', //          §21 #104 working copy committed
  'template.merged', //             §21 #104 merge / cherry-pick into a template (data.kind)
  'template.reverted',
  'template.reset',
  'template.working-discarded',
  'tag.created',
  'tag.updated',
  'tag.deleted',
  'account.created',
  'account.updated', //        profile only (name / phone / img)
  'account.access-changed', // access rules changed (profile changes in the same request are in `changes`)
  'account.disabled',
  'account.enabled',
  'account.password-reset', //       legacy (before §21 #84): a password-set link was made
  'account.password-set', //         §21 #84 "Set new password…" (also written at account creation)
  'account.password-revealed',
  'account.reset-link-created',
  'account.reset-link-revealed',
  'deployment.created',
  'deployment.updated',
  'deployment.deleted',
  'deployment.access-key-revealed', //      §21 #84: deployment-scoped credential ops are audited AND write a _cloud activity
  'deployment.access-key-rotated',
  'deployment.install-command-created', //  also Regenerate (replaceActive)
  'deployment.install-command-revealed',
  'deployment.upgrade-command-revealed', //    §21 #92: the enrolled site's "Upgrade / reinstall command" viewed
  'deployment.upgrade-command-regenerated', // §21 #92: a new site-bound maintenance token minted (old upgrade command revoked)
  'deployment.secret-revealed',
  'deployment.secret-regenerated',
  'deployment.agent-upgrade-requested', //  §21 #90: "Upgrade agent" / "Upgrade all" in the cloud console (also a _cloud activity)
  'deployment.agent-upgrade-cancelled', //  §21 #90: a queued request / a run cancelled before it started on the site
  'deployment.template-synced', //   §21 #94: Template sync applied to the draft (also a _cloud activity draft.template-synced)
  'deployment.template-unlinked', // §21 #94
  'deployment.committed', //        §21 #104 working copy committed (= a release; also a _cloud activity branch.committed)
  'deployment.merged', //           §21 #104 merge / cherry-pick into a deployment (data.kind)
  'deployment.reverted',
  'deployment.reset',
  'deployment.working-discarded',
  'blob.uploaded',
  'upload.created',
  'upload.completed',
  'icon.uploaded', //          also when the same bytes already existed (data.existing = true)
  'icon.deleted',
  'github.updated', //         §21 #106 Admin → GitHub: owner / repo / enabled / token changed (token "***"; data.tokenReplaced / tokenRemoved)
  'github.tested', //          §21 #106 Test connection
  'github.token-revealed', //  §21 #106 token viewed (60 s)
  'github.synced', //          §21 #106 Sync now
  'github.resynced', //        §21 #106 Resync everything
  'github.push-failed', //     §21 #106 system: a branch stopped pushing (first failure of a run of failures; data { gitBranch, status, message })
  'system.daily-cleanup', //   system (daily job; one summary entry per run)
  'system.migration', //       system (data migration scripts; data = { migration, dryRun, changes })
] as const;
export type AuditAction = (typeof AUDIT_ACTIONS)[number];
/**
 * Default action + target type of every audited CLOUD_API op (the router writes one entry per request, success or
 * failure; handlers may refine the action, e.g. patchAccount → account.access-changed, disableAccount → account.enabled).
 * putUploadPart is the only human write without an entry (logged once at completeUpload with the part count).
 */
export const AUDIT_OPS = {
  createRegistry: { action: 'registry.created', targetType: 'registry' },
  patchRegistry: { action: 'registry.updated', targetType: 'registry' },
  deleteRegistry: { action: 'registry.deleted', targetType: 'registry' },
  testRegistry: { action: 'registry.tested', targetType: 'registry' },
  revealRegistryPassword: { action: 'registry.password-revealed', targetType: 'registry' },
  createService: { action: 'service.created', targetType: 'service' },
  patchService: { action: 'service.updated', targetType: 'service' },
  deleteService: { action: 'service.deleted', targetType: 'service' },
  inspectImage: { action: 'image.inspected', targetType: 'image' },
  registerImage: { action: 'image.registered', targetType: 'image' },
  patchImage: { action: 'image.updated', targetType: 'image' },
  deleteImage: { action: 'image.deleted', targetType: 'image' },
  retryImport: { action: 'image.import-retried', targetType: 'image' },
  createNotFoundPage: { action: 'not-found-page.created', targetType: 'notFoundPage' },
  deleteNotFoundPage: { action: 'not-found-page.deleted', targetType: 'notFoundPage' },
  createTemplate: { action: 'template.created', targetType: 'template' },
  patchTemplate: { action: 'template.updated', targetType: 'template' },
  deleteTemplate: { action: 'template.deleted', targetType: 'template' },
  commitTemplate: { action: 'template.committed', targetType: 'template' },
  discardTemplateWorking: { action: 'template.working-discarded', targetType: 'template' },
  merge: { action: 'deployment.merged', targetType: 'deployment' }, //        handler refines: template.merged for template targets
  revertCommit: { action: 'deployment.reverted', targetType: 'deployment' }, // handler refines: template.reverted
  resetToCommit: { action: 'deployment.reset', targetType: 'deployment' }, //  handler refines: template.reset
  commitDeployment: { action: 'deployment.committed', targetType: 'deployment' },
  discardDeploymentWorking: { action: 'deployment.working-discarded', targetType: 'deployment' },
  createTag: { action: 'tag.created', targetType: 'tag' },
  patchTag: { action: 'tag.updated', targetType: 'tag' },
  deleteTag: { action: 'tag.deleted', targetType: 'tag' },
  createAccount: { action: 'account.created', targetType: 'account' },
  patchAccount: { action: 'account.updated', targetType: 'account' },
  disableAccount: { action: 'account.disabled', targetType: 'account' },
  resetAccountPassword: { action: 'account.reset-link-created', targetType: 'account' },
  setAccountPassword: { action: 'account.password-set', targetType: 'account' },
  revealAccountPassword: { action: 'account.password-revealed', targetType: 'account' },
  revealAccountResetLink: { action: 'account.reset-link-revealed', targetType: 'account' },
  createDeployment: { action: 'deployment.created', targetType: 'deployment' },
  patchDeployment: { action: 'deployment.updated', targetType: 'deployment' },
  deleteDeployment: { action: 'deployment.deleted', targetType: 'deployment' },
  revealAccessKey: { action: 'deployment.access-key-revealed', targetType: 'deployment' },
  rotateAccessKey: { action: 'deployment.access-key-rotated', targetType: 'deployment' },
  createInstallToken: { action: 'deployment.install-command-created', targetType: 'deployment' },
  revealInstallCommand: { action: 'deployment.install-command-revealed', targetType: 'deployment' },
  revealUpgradeCommand: { action: 'deployment.upgrade-command-revealed', targetType: 'deployment' },
  regenerateUpgradeCommand: { action: 'deployment.upgrade-command-regenerated', targetType: 'deployment' },
  revealSecret: { action: 'deployment.secret-revealed', targetType: 'deployment' },
  regenerateSecret: { action: 'deployment.secret-regenerated', targetType: 'deployment' },
  requestAgentUpgrade: { action: 'deployment.agent-upgrade-requested', targetType: 'deployment' },
  upgradeAllAgents: { action: 'deployment.agent-upgrade-requested', targetType: 'deployment' },
  cancelAgentUpgrade: { action: 'deployment.agent-upgrade-cancelled', targetType: 'deployment' },
  cancelAgentUpgradeRun: { action: 'deployment.agent-upgrade-cancelled', targetType: 'deployment' },
  applyTemplateSync: { action: 'deployment.template-synced', targetType: 'deployment' },
  unlinkTemplate: { action: 'deployment.template-unlinked', targetType: 'deployment' },
  uploadBlob: { action: 'blob.uploaded', targetType: 'blob' },
  createUpload: { action: 'upload.created', targetType: 'upload' },
  completeUpload: { action: 'upload.completed', targetType: 'upload' },
  uploadIcon: { action: 'icon.uploaded', targetType: 'icon' },
  deleteIcon: { action: 'icon.deleted', targetType: 'icon' },
  patchGithubMirror: { action: 'github.updated', targetType: 'github' },
  testGithubMirror: { action: 'github.tested', targetType: 'github' },
  revealGithubToken: { action: 'github.token-revealed', targetType: 'github' },
  syncGithubMirror: { action: 'github.synced', targetType: 'github' },
  resyncGithubMirror: { action: 'github.resynced', targetType: 'github' },
} as const satisfies Record<string, { action: AuditAction; targetType: AuditTargetType }>;
/**
 * §21 #84: deployment-scoped ops (/v1/deployments/{id}/…) that ARE written to the root audit log too (credential
 * reveals / regenerations); every other deployment-internal op is only a _cloud activity.
 */
export const AUDIT_DEPLOYMENT_OPS = ['revealAccessKey', 'rotateAccessKey', 'createInstallToken', 'revealInstallCommand', 'revealUpgradeCommand', 'regenerateUpgradeCommand', 'revealSecret', 'regenerateSecret', 'requestAgentUpgrade', 'upgradeAllAgents', 'cancelAgentUpgrade', 'cancelAgentUpgradeRun', 'applyTemplateSync', 'unlinkTemplate', 'commitDeployment', 'discardDeploymentWorking'] as const;
/**
 * §21 #84: every CLOUD_API op whose response carries a credential value. The router sends `Cache-Control: no-store`
 * on them and rate-limits them per uid (REVEAL_RATE_PER_MINUTE, fixed 1-minute window, Firestore counter
 * `rateLimits/reveal_<uid>_<yyyymmddhhmm>`; 429 rate-limited, details.retryAfterSec, Retry-After).
 */
export const CREDENTIAL_OPS = [
  'createDeployment',
  'revealAccessKey',
  'rotateAccessKey',
  'createInstallToken',
  'revealInstallCommand',
  'revealUpgradeCommand',
  'regenerateUpgradeCommand',
  'revealSecret',
  'regenerateSecret',
  'revealRegistryPassword',
  'createAccount',
  'setAccountPassword',
  'revealAccountPassword',
  'resetAccountPassword',
  'revealAccountResetLink',
  'revealGithubToken',
] as const;
/** Human writes that are NOT audited (putUploadPart: logged at completeUpload; previewTemplateSync: a dry run that writes nothing, §21 #94). */
export const AUDIT_EXEMPT_OPS = ['putUploadPart', 'previewTemplateSync', 'compareBranches', 'previewMerge'] as const;
/** One changed leaf. Secret values are "***" (secret = true); values > 2 KB are AuditTruncated. */
export interface AuditChange {
  path: string; //      e.g. "label", "sites[app].containers[redis].env.FOO", "variables[port].value"
  before: unknown; //   null when the field / document did not exist
  after: unknown; //    null when the field / document was removed
  secret?: boolean; //  true: a secret value changed (both sides masked as "***", or null when unset)
}
/** Replaces a value whose canonical JSON is larger than 2 KB. */
export interface AuditTruncated {
  $truncated: true;
  bytes: number; //   size of the full value (JSON)
  preview: string; // first 2000 characters of the (redacted) JSON
}
export interface AuditLogEntry {
  auditId: Id;
  ts: Ts;
  /** the CLOUD_API op name (e.g. "patchTemplate"), or "system:<job>" (system:importImage, system:daily) */
  op: string;
  /** uid of the person, or "system" */
  actor: Uid | 'system';
  actorEmail: string | null;
  action: AuditAction | string; // AUDIT_ACTIONS (string for entries written before the catalog)
  outcome: 'ok' | 'error';
  /** HTTP status of the answer (system entries: 200 ok / 500 error) */
  status: number;
  /** ErrorCode of a failed request (forbidden, invalid, conflict, …), null when ok */
  errorCode: string | null;
  errorMessage: string | null;
  targetType: AuditTargetType;
  /** document path of the target, e.g. "registries/{registryId}", "services/{id}/images/{version}", "blobs/sha256:…" */
  target: string;
  requestId: string | null; // X-Request-Id of the request (null for system entries)
  ip: string | null;
  userAgent: string | null;
  /** deep diff of the target document (before → after); create = the created doc, delete = the deleted doc.
   *  ≤ 200 entries (changesTotal tells how many there were); secrets redacted */
  changes: AuditChange[];
  changesTotal: number;
  /** extra facts (redacted like changes), e.g. { removedFrom } for deleteTag, { partCount } for completeUpload */
  data: Record<string, unknown>;
}
/** GET /v1/audit-log query (one filter runs in Firestore, the others in memory). */
export interface AuditLogQuery {
  action?: string; //     exact action or a prefix ending in "." (e.g. "template.")
  actor?: string; //      uid, "system", or an e-mail (contains "@")
  targetType?: AuditTargetType;
  target?: string; //     exact target path, e.g. "templates/abc"
  outcome?: 'ok' | 'error';
  from?: Ts; //           ts ≥ from
  to?: Ts; //             ts ≤ to
  cursor?: string;
  limit?: number; //      default 100, max 500
}

// ───────────────────────────────────────────────────────────── config (templates + deployments)
export type VariableType = 'string' | 'number' | 'boolean' | 'enum' | 'list' | 'json';
export type VariableValue = string | number | boolean | string[] | Record<string, unknown> | unknown[] | null;
export interface Variable {
  variableSlug: Slug;
  label: string;
  description: string;
  type: VariableType;
  value: VariableValue; // template = default; deployment = actual value
  required: boolean; //   default false
  options: string[] | null; // enum only, 1–100 unique
  validation: { min: number | null; max: number | null; pattern: string | null } | null;
}
export type SecretCharset = 'alnum' | 'hex' | 'base64url';
export interface SecretDef {
  secretSlug: Slug;
  label: string;
  description: string;
  required: boolean; // default true
  generate: { charset: SecretCharset; length: number } | null; // length 8–128
  value: string | null; // plain (prototype); ALWAYS null in templates; ≤ 8192 bytes
  rotatedAt: Ts | null;
}
export type SecretDefView = Omit<SecretDef, 'value'> & { value: Masked | null; isSet: boolean };
/** Request item for PUT draft/secrets and agent local proposals.
 *  plainValue: absent/undefined = keep the current value; null = clear; string = set (rotatedAt = now).
 *  generateNow: true = the server generates a new value with `generate` (overrides plainValue). */
export type SecretDefInput = Omit<SecretDef, 'value' | 'rotatedAt'> & { plainValue?: string | null; generateNow?: boolean };

/**
 * Where something lives on the site server (round D). "agentHome" = the agent data dir (`--data-dir` / OPM_DATA_DIR,
 * default /var/lib/opm). auto = the default location: files `<agentHome>/files/<fileSlug>`, site volumes
 * `<agentHome>/volumes/<volumeSlug>`. path: "./x/y" = relative to agentHome (no ".." segments), or an absolute path used
 * as is (isSafeMountPath, no {{ }}). Locations are unique per site and never nested in each other (V_LOCATION).
 */
export type StorageLocation = { kind: 'auto' } | { kind: 'path'; path: string };
export type FileRender = 'expressions' | 'none';
export interface FileDef {
  fileSlug: Slug;
  label: string;
  description: string;
  mime: string; //     type/subtype
  editor: 'text' | 'binary';
  maxBytes: number; // 1–52428800, default 1048576
  content: { sha256: Sha256; sizeBytes: number } | null; // blob "files/<hex>" (the SOURCE; never the rendered bytes)
  /**
   * 'expressions' (default for editor text): the content is a template; {{ }} blocks resolve PER MOUNTING CONTAINER at
   * staging on the site (field "file": var, secret, file, site, self, deployment, container incl. container.env.*).
   * Literal "{{" is written {{ "{{" }}. Rendered bytes live only on the site (mode 0400, read-only mount).
   * 'none' (default for editor binary; binary files are always 'none'): mounted byte for byte.
   * Missing on legacy documents = the default for the editor (normalizeFileDef).
   * Round D: a file is rendered ONCE PER SITE (roots var, secret, site, self, deployment — container.* and file.* are
   * E_NOT_ALLOWED: the copy is shared by every container of the site).
   */
  render: FileRender;
  /**
   * Where the ONE copy of this file lives on each site (round D; default auto = <agentHome>/files/<fileSlug>). Every
   * container that mounts the file bind-mounts that single host file read-only. Mode 0400 when the rendered content holds
   * a secret in clear, else 0444 (§21 #103: also when every secret only went through mosquitto_passwd — a salted hash the
   * container reads as a non-root user). Missing on legacy documents = auto (normalizeFileDef).
   */
  location: StorageLocation;
}

export interface ContainerEnv {
  name: string; //               ^[A-Z_][A-Z0-9_]*$, unique in the container
  value: Value | null; //        exactly one of value / secretRef
  secretRef: Slug | null;
}
/**
 * A data folder of a site (SPEC §2.2 "Site volumes", round D). Declared on the site; containers mount it by volumeSlug
 * (VolumeSource kind 'volume'). On the agent: its location (auto = `<agentHome>/volumes/<volumeSlug>/`). Several containers (and several mounts) may share
 * one. Snapshots archive exactly the declared volumes with backup = true (host paths are never backed up).
 */
export interface SiteVolume {
  volumeSlug: string; //  the ONLY identifier, unique per site, RE.volumeSlug ^[a-z0-9][a-z0-9_.-]{0,99}$ (never renamed: rename = new volume)
  label: string; //       0–80 (empty = the GUIs show the volumeSlug)
  description: string; // 0–2000
  backup: boolean; //     default true: included in manual / scheduled / pre-update snapshots
  /** where the folder lives on the server: auto = <agentHome>/volumes/<volumeSlug> (default); backed up wherever it is */
  location: StorageLocation;
  /**
   * round E: 'dir' (default) = a folder; 'file' = ONE persistent read-write file at the location (auto: the file
   * <agentHome>/volumes/<volumeSlug>), mounted at a FILE path in the container (mount path must not end with "/").
   * Apps that save by writing a temp file + rename (e.g. Node-RED flows.json) need a 'dir' volume: a single-file bind
   * mount cannot be renamed over (EBUSY). Backed up / restored like any volume.
   */
  kind: SiteVolumeKind;
  /**
   * round E: starter content, applied ONLY when the volume is created or empty (never re-seeded); null = start empty.
   * §21 #101: the volume's OWN content (`tree` / `single`, blobs in cloud Storage, a manifest here); Files are never
   * volume content (legacy `files` / `file` seeds exist only in revisions published before 0.12).
   */
  seed: VolumeSeed | null;
  /**
   * §21 #101 "persist to cloud" (folder volumes only): the agent snapshots the live tree to cloud Storage (versioned),
   * the cloud edits it and sends it back (approved on the site). OMITTED when off (absent = off — keeps revisions
   * hash-compatible with agents < 0.12; read it with `volumePersist(v)`).
   */
  persist?: VolumePersist;
}
export type SiteVolumeKind = 'dir' | 'file';
/**
 * §21 #101 one file of a volume's own content (a manifest entry; the bytes are the content-addressed blob
 * `files/<hex>` in cloud Storage, like FileDef.content). path = relative inside the volume ("/"-separated, no "." /
 * ".." / empty segments, no NUL / control chars / "\", ≤ 1024 UTF-8 bytes, segments ≤ 255 bytes; `contentPathError`).
 * mode = permission bits "0644" (^0[0-7]{3}$). render: 'expressions' = rendered ONCE PER SITE when the starter
 * content is written (like a File with render expressions; only in starter content); persisted versions are always 'none'.
 */
export interface VolumeTreeEntry {
  path: string;
  sha256: Sha256;
  size: number; //   bytes, 0 – VOLUME_CONTENT_LIMITS.fileMaxBytes
  mode: string; //   "0644" (default), "0755", "0600" …
  render: FileRender;
}
/**
 * §21 #101 a volume file tree (manifest). Canonical form (`normalizeVolumeTree`): entries sorted by path (UTF-16 code
 * units, as JCS), `dirs` = only EMPTY folders (folders implied by an entry or a deeper dir are dropped), sorted.
 * treeHash = "sha256:" + hex(sha256(JCS(canonical tree))) — vectors shared/testdata/volume-content-vectors.json (Go mirror).
 */
export interface VolumeTree {
  entries: VolumeTreeEntry[];
  dirs: string[];
}
/** §21 #101 persist-to-cloud settings of a folder volume (SiteVolume.persist; absent = off). */
export interface VolumePersist {
  /** gitignore-lite patterns never snapshotted nor touched by an apply (≤ 50, 1–200 chars; `excludePatternError`) */
  exclude: string[];
  /** size cap of one snapshot (sum of the non-excluded files), 1 MiB – 200 MiB, default 50 MiB */
  maxBytes: number;
  /** snapshot automatically when the live tree changed and has been quiet for 2 min (≤ 1 per 15 min); default true */
  auto: boolean;
}
/**
 * Starter content of a site volume (round E). Applied by the agent at APPLY, before containers start, only when the
 * volume does not exist yet or is empty (dir without entries / file of size 0); then marked seeded (marker
 * `.opm-seeded` in a dir volume, agent DB state for both) so it never seeds again. Later seed changes never touch
 * existing data ("seed changed — applies only to new / empty volumes"); the agent GUI "Reset to starter content…"
 * re-applies it on purpose (snapshot first, 60 s + ACCEPT).
 *   files   — dir volumes: entries[].path relative inside the volume (segments [A-Za-z0-9._-], no "." / "..", unique,
 *             1–100 entries) ← a template file's content (render 'expressions' → rendered once per site)
 *   file    — file volumes: the file's content (rendered like above)
 *   archive — dir volumes: a .tar.gz blob (CreateUploadRequest purpose 'seed-archive', ≤ SEED_ARCHIVE_MAX); never
 *             rendered; regular files + dirs only, relative safe paths, no symlinks / hard links / devices
 */
export type VolumeSeed =
  /** §21 #101 folder volumes: the volume's own file tree (≤ 1000 files, ≤ 1000 empty folders, ≤ 200 MiB in total) */
  | ({ kind: 'tree' } & VolumeTree)
  /** §21 #101 single-file volumes: the file's own content */
  | { kind: 'single'; sha256: Sha256; size: number; mode: string; render: FileRender }
  /** @deprecated §21 #101 — legacy (Files as volume content): accepted only in revisions published before 0.12 */
  | { kind: 'files'; entries: { path: string; fileSlug: Slug }[] }
  /** @deprecated §21 #101 — legacy (a File as volume content): accepted only in revisions published before 0.12 */
  | { kind: 'file'; fileSlug: Slug }
  | { kind: 'archive'; blobSha256: Sha256; name: string; size: number };
export const SEED_ARCHIVE_MAX = 200 * 1024 * 1024;
/** Where a mount gets its content from (SPEC §2.2 "Container instances" + "Site volumes"). */
export type VolumeSource =
  | { kind: 'volume'; volumeSlug: string } // a volume DECLARED in SiteConfig.volumes (else V_VOLUME_UNDECLARED); shareable; readOnly default false
  | { kind: 'host'; path: string } //       advanced: bind of an absolute host path (literal, isSafeMountPath); never backed up; readOnly default false
  | { kind: 'file'; fileSlug: Slug } //     always read-only; ONE copy per site at FileDef.location (rendered once per site)
  | { kind: 'secret'; secretSlug: Slug } // always read-only; ONE copy per site at <agentHome>/secrets/<secretSlug>, mode 0400
  | { kind: 'none' }; //                    image volume not mounted (container-local storage, lost on recreate)
export type VolumeSourceKind = VolumeSource['kind'];
export interface ContainerMount {
  /** the image volume this row maps (one row per image volume); null = an extra mount */
  imageVolume: Slug | null;
  /** extra mounts: required; image-volume rows: null = the image mountPath (or an override). May hold {{ }} (field mountPath). */
  mountPath: string | null;
  source: VolumeSource; //  extra mounts cannot use kind 'none'. An image volume WITHOUT a row is not mounted (warning V_VOLUME_NOT_MOUNTED)
  readOnly: boolean; //     volume + host: default false; file + secret: must be true (V_READONLY)
}
export type HostIp = '0.0.0.0' | '127.0.0.1';
export interface ContainerPort {
  portName: Slug; //             a port name of the image (one row per image port; missing row = not published, edge true)
  hostPort: number | Expr | null; // null = not published; ≠ 80/443/edge/agent ports, unique per site + protocol
  hostIp: HostIp; //             bind address of the published port; default '0.0.0.0'
  edge: boolean; //              the edge may proxy to this port (default true)
}
/**
 * One container INSTANCE on a site. The same service + version may run many times per site with different env / ports /
 * volumes. containerSlug is the ONLY identifier: unique per site, the DNS name on the site network, used in expressions
 * (container.slug, containers.<containerSlug>.host|port.<portName>, site.<site>.port.<containerSlug>.<portName>), edge
 * targets, depends and docker names (the containerSlug; agent.json containerNames "prefixed": opm_<dep>_<site>_<containerSlug>,
 * SPEC §21 #88). The image tag stays `<serviceSlug>:<version>` (shared by instances).
 */
export interface Container {
  containerSlug: Slug; //        ^[a-z][a-z0-9-]{1,39}$; default = serviceSlug, then serviceSlug-2, -3 … (suggestContainerSlug)
  serviceSlug: Slug; //          cloud only (local edits cannot change it)
  version: string; //            Image.version of that service; cloud only
  enabled: boolean;
  env: ContainerEnv[]; //        overrides of the image env + extra vars
  ports: ContainerPort[];
  volumes: ContainerMount[];
  command: string[] | null; //   overrides the image command
  entrypoint: string[] | null; // overrides the image entrypoint
  restart: 'always' | 'on-failure' | 'never';
  resources: { cpus: number | null; memoryMb: number | null }; // cpus 0.1–64, memoryMb 64–1048576
  healthcheck: HealthCheck | null; // null = the image health check
  depends: { containerSlug: Slug }[]; // containers of the same site, no cycles (sites: SiteConfig.depends)
}

export type EdgeTls =
  | { mode: 'none' }
  | { mode: 'internal' }
  | {
      mode: 'acme';
      email: string;
      challenge: 'http-01' | 'tls-alpn-01' | 'dns-01';
      dnsProvider: string | null; // cloudflare | digitalocean | duckdns | godaddy | route53 | googleclouddns | namecheap (SPEC §12.4)
      dnsTokenSecretSlug: Slug | null;
    }
  | { mode: 'files'; certFileSlug: Slug; keySecretSlug: Slug };
/**
 * Response headers (round E, nginx add_header / proxy_hide_header). Order applied to a response:
 * upstream headers → `remove` → `set` (replaces any value) → `add` (appends; duplicates allowed, as nginx add_header).
 * `set` / `add` apply only to statuses 200 201 204 206 301 302 303 304 307 308 unless the name is in `always`
 * (nginx "always": every status, also the edge's own 404 / 413 / 500 / 502 / 503 pages). `remove` applies to every
 * proxied response. Values may hold `{{ }}` (field edge) and the nginx variables of SPEC §12.6.
 */
export interface EdgeResponseHeaders {
  set: Record<string, Value>; //   nginx proxy_hide_header X + add_header X
  add: Record<string, Value>; //   nginx add_header X (alone)
  remove: string[]; //             nginx proxy_hide_header X (alone)
  always: string[]; //             names of set / add entries that apply to every status (nginx add_header … always)
}
export interface EdgeHost {
  hostname: string; // DNS name | "*.x.y" | IPv4 | IPv6 | "*" ; may be Expr (var.*, deployment.id, self.*)
  tls: EdgeTls;
  redirectHttpToHttps: boolean;
  /** round E: host level (nginx server level) response headers; routes with responseHeaders null inherit them. Default empty. */
  responseHeaders: EdgeResponseHeaders;
}
/** nginx `rewrite <regex> <replacement>`: regex (RE2 ∩ JS) against the current path; replacement uses $1…$9 + §12.6 variables. */
export interface EdgeRewrite {
  regex: string;
  replacement: Value;
}
export interface EdgeProxyAction {
  type: 'proxy';
  containerSlug: Slug; //      a container of THIS site
  portName: Slug; //           a port of that container's image whose ContainerPort.edge is true
  stripPrefix: boolean; //     prefix / exact routes only: drop the matched path from the upstream path
  /** round E: nginx `rewrite … break` (any pattern); exclusive with stripPrefix; null = none */
  rewrite: EdgeRewrite | null;
  addPrefix: string | null; // starts with "/"
  setHeaders: Record<string, Value>; // request header name → Value (Expr allowed: var.*, deployment.id, self.*; §12.6 variables)
  removeHeaders: string[];
  websocket: boolean; //       default true (round E)
  connectTimeoutSec: number; // 1–3600, default 60 (nginx proxy_connect_timeout)
  sendTimeoutSec: number; //   1–86400, default 60: idle time between two writes to the upstream (nginx proxy_send_timeout)
  readTimeoutSec: number; //   1–86400, default 60: time to the response header AND idle time between two reads (nginx proxy_read_timeout). Legacy `timeoutSec`
  requestBuffering: boolean; // default false = stream the request body; true = buffer up to 8 MiB in memory first (nginx proxy_request_buffering)
  responseBuffering: boolean; // default false = flush every write (Caddy flush_interval -1); text/event-stream always flushes (nginx proxy_buffering)
  maxBodyBytes: number | null; // null = inherit Edge.maxBodyBytes; 0 = unlimited; else 1–1099511627776 → 413 above (nginx client_max_body_size)
}
export interface EdgeRedirectAction {
  type: 'redirect';
  to: Value; //                absolute URL or path; placeholders {path} {query} {host} + §12.6 variables ($uri $args $1 …); Expr allowed
  status: 301 | 302 | 303 | 307 | 308;
  keepPath: boolean;
}
/** round E: internal rewrite of the path, then the routes are matched again (nginx `rewrite … last`); ≤ 10 per request, else 500. */
export interface EdgeRewriteAction {
  type: 'rewrite';
  regex: string; //            RE2 ∩ JS, against the current path (no query)
  replacement: Value; //       new path (may hold "?query": then it REPLACES the query); $1…$9 + §12.6 variables
}
export type EdgeRouteAction = EdgeProxyAction | EdgeRedirectAction | EdgeRewriteAction;
/**
 * round E (nginx location semantics, SPEC §12.1):
 *   exact  — the path equals `path` (nginx `location = /x`)
 *   prefix — the path starts with `path` (plain string prefix, nginx `location /x/`; `stopRegex` = nginx `^~`)
 *   regex  — `path` is a regular expression (RE2 syntax that JS also understands; a leading `(?i)` = case-insensitive,
 *            nginx `~` / `~*`) matched against the decoded request path without the query
 * Legacy shape `{ pathPrefix, methods }` = kind prefix (normalizeSiteConfig).
 */
export type EdgeMatchKind = 'prefix' | 'exact' | 'regex';
export interface EdgeMatch {
  kind: EdgeMatchKind;
  path: string;
  stopRegex: boolean; //       prefix only; false for exact / regex
  methods: string[] | null; // null = all; a route is invisible to other methods
}
export interface EdgeRoute {
  routeId: Slug; //            ^[a-z0-9][a-z0-9-]{0,62}$, unique in the site
  hosts: string[]; //          hostnames from edge.hosts; [] = all hosts
  match: EdgeMatch;
  action: EdgeRouteAction;
  /** integer ≥ 1; only the RELATIVE order of regex routes matters (exact > longest prefix are order-free); GUIs renumber 1..n */
  order: number;
  /** round E: null = inherit the host's responseHeaders; an object REPLACES them for this route (nginx add_header inheritance). Must be null for rewrite routes. */
  responseHeaders: EdgeResponseHeaders | null;
}
export interface Edge {
  httpPort: number; //         default 80
  httpsPort: number | null; // default 443; null = no TLS listener
  /** round E: request body limit of every proxy route (nginx http/server client_max_body_size); null = unlimited. Over → 413. */
  maxBodyBytes: number | null;
  hosts: EdgeHost[];
  routes: EdgeRoute[];
}
export interface SiteConfig {
  /**
   * Address of the site for containers of other sites and users (IP or DNS); Expr: var.*, deployment.id.
   * null = AUTO (default): the agent's advertised / detected primary address (AgentDoc.advertiseHost, detectedAddresses).
   * A host expression that resolves to null or "" also falls back to auto (warning V_HOST_AUTO in the cloud, never an error).
   */
  host: Value | null;
  agentPort: number; //        default 7080 (mesh + fallback GUI), 1024–65535
  agentGuiAliasPort: number | null; // default 8080, null = off, 1024–65535
  /** the data folders of this site (round D); containers mount them by volumeSlug. Default []. */
  volumes: SiteVolume[];
  containers: Container[];
  depends: { siteSlug: Slug }[];
  notFoundPageId: Id | null;
  edge: Edge;
  /**
   * §21 #97 dev-tool presets of this site (HTTP / PostgreSQL / SQLite / Cassandra / Redis / MQTT connections the agent
   * GUI lists read-only). Credentials only as expressions (`{{ secret.x }}`). OMITTED when empty (absent = [] — keeps
   * revisions of deployments without presets hash-compatible with agents < 0.11; siteDevTools() reads it).
   */
  devTools?: import('./devtools.js').DevToolPreset[];
}
export interface Site {
  siteSlug: Slug; //           not one of RESERVED_SITE_SLUGS
  label: string;
  config: SiteConfig;
}
export interface Config {
  variables: Variable[];
  secrets: SecretDef[];
  files: FileDef[];
  sites: Site[];
}
export type ConfigView = Omit<Config, 'secrets'> & { secrets: SecretDefView[] };
export type ConfigInput = Omit<Config, 'secrets'> & { secrets: SecretDefInput[] };

export interface Template extends Audit {
  templateId: Id;
  /**
   * §21 #106 the stable entity slug (GitHub mirror branch template/<slug>): slugifyLabel(label) made unique among
   * templates at create (or the requested CreateTemplateRequest.slug), IMMUTABLE (PATCH slug → invalid V_IMMUTABLE).
   * Older documents got one from migrate-k. RE.slug.
   */
  slug: Slug;
  label: string;
  icon: Icon;
  description: string;
  version: number; // 1 at create, +1 on every details PATCH and every commit that changes the config
  variables: Variable[]; //   §21 #105: the config fields are a COPY of the head commit config, written only by commits (no working copy)
  secrets: SecretDef[]; // value always null
  files: FileDef[];
  sites: Site[];
  /** §21 #104 branch state (head commit, origin, counts); absent on pre-0.14 documents until migrate-i */
  branch?: import('./branch.js').BranchState;
}
export type TemplateSummary = Pick<Template, 'templateId' | 'slug' | 'label' | 'icon' | 'description' | 'version' | 'updatedAt'> & {
  siteSlugs: Slug[];
  counts: { variables: number; secrets: number; files: number; sites: number };
  usedBy: number;
  /** §21 #104 */
  branch?: import('./branch.js').BranchState | null;
};

// ───────────────────────────────────────────────────────────── deployments
export interface HeadRef {
  revId: RevId;
  seq: number;
  hash: Sha256;
  reportedAt: Ts;
}
export interface Deployment extends Audit {
  deploymentId: Id;
  /** §21 #106 the stable entity slug (GitHub mirror branches deployment/<slug>/…): like Template.slug (unique among deployments, deleted ones too), immutable. */
  slug: Slug;
  label: string;
  image: Icon; //                     logo: "upload:<iconId>", "lucide:<name>", legacy https URL, or null
  tagSlugs: Slug[]; //                ≤ 20
  managerAccountUids: Uid[];
  /**
   * §21 #94: the template this deployment was cloned from (or linked to in Template sync); null = none. The deployment is
   * DETACHED: nothing reads the live template for its config (validation, diff, publish, files, staging); template edits /
   * deletion never change it. Only the manual Template sync (3-way: baseSnapshot ↔ the template now ↔ the draft) moves
   * template changes into the draft.
   */
  srcTemplate: SrcTemplate | null;
  draft: Config; //                    §21 #105: a COPY of the head commit config, written only by commits (the name is kept for storage compat; there is no working copy)
  draftVersion: number; //            +1 on every commit that changes the config
  head: HeadRef | null; //            last head reported by an agent
  accessKeyHash: string; //           argon2id PHC string; never returned by the API (agents get it in the cloud view)
  /** §21 #84: the plaintext key ("XXXX-…", grouped), stored like secrets; returned ONLY by create / rotate / revealAccessKey;
   *  null = deployment created before #84 (not stored → "Regenerate to view"). Never in DeploymentView or the cloud view. */
  accessKey: string | null;
  accessKeyVersion: number; //        1 at create, +1 per rotate
  accessKeyRotatedAt: Ts;
  accessKeyRotatedBy: Uid;
  /** monotonically increasing counter; +1 on every change that agents must see (cloud view). The poll cursor. */
  agentEpoch: number;
  cloudActivitySeq: number; //        last seq of the "_cloud" activity log of this deployment
  deleted: boolean; //                soft delete flag (deleted deployments vanish from lists; agents see deleted:true)
  /** §21 #90 the current / last cloud "Upgrade all" run (absent = never) */
  agentUpgradeRun?: AgentUpgradeRun | null;
  /** §21 #104 branch state: head commit (what the sites should run), origin, counts. `draft` = a copy of the head commit config (§21 #105); `head`
   *  above = the LEGACY head reported by agents < 0.14 (compat proposals, §21 #104 j). Absent until migrate-i. */
  branch?: import('./branch.js').BranchState;
}
export type DeploymentView = Omit<Deployment, 'draft' | 'accessKeyHash' | 'accessKey' | 'srcTemplate'> & {
  draft: ConfigView;
  srcTemplate: SrcTemplateView | null;
};
/** §21 #94 one template change the user did not take in a Template sync (kept as "skipped" until the template changes it again). */
export interface TemplateSyncSkip {
  key: string; //            TemplateSyncChange.key
  theirsHash: string; //     templateSyncHash of the template's value when skipped; a different value = a new change again
  templateVersion: number; // the template version it was skipped at
  skippedAt: Ts;
  skippedBy: Uid;
}
/** §21 #94 Deployment.srcTemplate (stored; the snapshots are internal JSON strings in Firestore). */
export interface SrcTemplate {
  templateId: Id;
  templateVersion: number; // the template version cloned at create (or linked at by Template sync "Change template…")
  snapshot: Template; //      the template as copied at templateVersion (secret values null)
  copiedAt: Ts;
  /** the template version the draft was last synced from (= templateVersion until the first sync) */
  baseVersion: number;
  /** the template's config at baseVersion = the 3-way merge base (self-contained: templates keep no version history) */
  baseSnapshot: Config;
  lastSyncedAt: Ts | null;
  lastSyncedBy: Uid | null;
  skipped: TemplateSyncSkip[];
}
export interface SrcTemplateView {
  templateId: Id;
  templateVersion: number;
  copiedAt: Ts;
  label: string; // the template label at copy / last sync
  baseVersion: number;
  lastSyncedAt: Ts | null;
  lastSyncedBy: Uid | null;
  skippedCount: number;
}
export interface DeploymentSummary {
  deploymentId: Id;
  slug: Slug;
  label: string;
  image: string | null;
  tagSlugs: Slug[];
  siteSlugs: Slug[];
  sitesOnline: number;
  sitesTotal: number;
  head: { revId: RevId; seq: number } | null;
  pending: { revId: RevId; seq: number; status: ProposalStatus } | null;
  updatedAt: Ts;
  /** §21 #104 */
  branch?: import('./branch.js').BranchState | null;
}

/** Item copied into Revision.catalog so that revisions are self-contained (offline sites, rollbacks). */
export interface RevisionCatalog {
  services: Record<Slug, { serviceSlug: Slug; label: string; icon: Icon }>; // icon: "upload:…" is embedded as a data: URL
  /** key = `${serviceSlug}:${version}` */
  images: Record<string, ImageSpec>;
  /** only the pages referenced by the config */
  notFoundPages: Record<Id, { notFoundPageId: Id; label: string; htmlCode: string }>;
}
export type RevisionOrigin = 'cloud' | 'agent';
export interface Revision {
  revId: RevId;
  seq: number; //               parent.seq + 1; first = 1
  parentRevId: RevId | null;
  origin: RevisionOrigin;
  actor: string; //             uid (cloud) or email (agent)
  createdAt: Ts;
  config: Config; //            plain secret values (agents need them); masked in every human read
  hash: Sha256; //              sha256(JCS(config))
  catalog: RevisionCatalog;
  catalogHash: Sha256; //       sha256(JCS(catalog))
  summary: string; //           1–200
  kind: 'change' | 'rollback';
  rollbackOf: { snapshotId: Ulid; seq: number } | null;
}
export type RevisionView = Omit<Revision, 'config'> & { config: ConfigView };
export type RevisionSummary = Pick<Revision, 'revId' | 'seq' | 'parentRevId' | 'origin' | 'actor' | 'createdAt' | 'hash' | 'summary' | 'kind'>;

export type ProposalStatus =
  | 'published'
  | 'staging'
  | 'ready'
  | 'approved'
  | 'applying'
  | 'applied'
  | 'failed-rolled-back'
  | 'stale'
  | 'superseded';
export const PROPOSAL_TERMINAL: readonly ProposalStatus[] = ['applied', 'failed-rolled-back', 'stale', 'superseded'];
export type StagingState = 'pending' | 'downloading' | 'ready' | 'failed';
export interface SiteStaging {
  staging: StagingState;
  progress: number; // 0..1
  error: string | null;
  updatedAt: Ts | null;
}
export interface Proposal {
  revId: RevId;
  parentRevId: RevId | null;
  seq: number;
  summary: string;
  origin: RevisionOrigin;
  kind: 'change' | 'rollback';
  createdAt: Ts;
  createdBy: string; // uid or email
  status: ProposalStatus;
  /** every site of (parent config ∪ proposed config); initialised to pending */
  sites: Record<Slug, SiteStaging>;
  approvedBy: string | null; // declared actor email
  approvedAt: Ts | null;
  /** §21 #95 agent-local: the approval wait was fast-forwarded (absent / null = waited at ×1) */
  approvedFastForward?: FastForwardUse | null;
  updateId: Ulid | null;
  result: { ok: boolean; message: string; snapshotId: Ulid | null } | null;
  supersededBy: RevId | null;
  updatedAt: Ts;
}
export type ProposalMeta = Pick<Proposal, 'revId' | 'parentRevId' | 'seq' | 'summary' | 'status' | 'origin' | 'kind' | 'createdAt' | 'createdBy'>;

export interface PeerInfo {
  siteSlug: Slug;
  publicKey: string; //       Ed25519 raw 32-byte public key, base64 (std, padded)
  advertiseHost: string; //   address other agents use for the mesh (agent.json advertiseHost or detected IP)
  agentPort: number; //       the agentPort the agent listens on now
  enrolledAt: Ts;
}

export interface CertStatus {
  siteSlug: Slug;
  hostname: string;
  mode: EdgeTls['mode'];
  challenge: string | null;
  issuer: string | null;
  notBefore: Ts | null;
  notAfter: Ts | null;
  lastRenewalAt: Ts | null;
  lastError: string | null;
  nextRenewalAt: Ts | null;
}
export interface EdgeReport {
  revSeq: number | null;
  reloadedAt: Ts | null;
  lastReloadError: string | null;
  boundPorts: number[];
  bindErrors: {
    port: number;
    purpose: 'edge-http' | 'edge-https' | 'gui-alias' | 'agent';
    error: string;
    /** §21 #86: name of the process holding the port (Linux /proc, agent runs as root); null = unknown / old agent */
    owner?: string | null;
  }[];
  certs: CertStatus[];
  /** §21 #86: true = the built-in default edge runs (no head yet, or this site is not in the head config): host "*",
   *  no TLS, /_opm/* GUI, / → 302 /_opm/app, everything else → "no configuration applied yet". Absent on old agents. */
  defaultEdge?: boolean;
}
/** §21 #86: one host port on which the agent GUI (/_opm/) answers right now (heartbeat; the cloud Setup page lists URLs). */
export interface GuiPort {
  port: number;
  scheme: 'http' | 'https';
  kind: 'edge' | 'gui-alias' | 'agent-port';
}
export interface HeartbeatStaging {
  revId: RevId;
  state: StagingState;
  progress: number;
}
export interface AgentDoc {
  siteSlug: Slug;
  publicKey: string;
  enrolledAt: Ts;
  agentVersion: string;
  lastSeenAt: Ts;
  online: boolean; //        computed on read: now - lastSeenAt < 90 s
  ip: string;
  hostname: string;
  advertiseHost: string;
  agentPort: number;
  os: string; //            "linux" | "darwin"
  arch: string; //          "amd64" | "arm64"
  relayVia: Slug | null; // set when the last report came through POST /v1/agent/relay
  headRevId: RevId | null;
  staging: HeartbeatStaging | null;
  keyVersion: number; //    accessKeyVersion this site has
  ackSeq: number; //        highest contiguous activity seq stored in the cloud
  metrics: HostMetricsSummary | null;
  edge: EdgeReport | null;
  peersSeen: { siteSlug: Slug; lastSeenAt: Ts }[];
  /** non-loopback IPs of the host (IPv4 first) + the hostname; [] on old agents */
  detectedAddresses: string[];
  /** §21 #86 (absent on agents < 0.8): the host ports the GUI answers on, from the last heartbeat */
  guiPorts?: GuiPort[];
  /** §21 #90: the queued cloud upgrade request of this site (null / absent = none). Shown in CloudView.agentUpgrades. */
  upgradeRequest?: AgentUpgradeRequest | null;
  /** §21 #90: the last upgrade progress the agent reported in a heartbeat (kept after it finished) */
  upgrade?: AgentUpgradeProgress | null;
  /** §21 #92: agent clock − server receive time from the last heartbeat with `agentTime` (null / absent = not measured:
   *  agents before 0.11 do not send it). lastSeenAt is ALWAYS the server receive time, never the agent's clock. */
  clockSkew?: AgentClockSkew | null;
}

// ───────────────────────────────────────────────────────────── §21 #90 agent self-upgrade (GUI + cloud)
/** First agent version that upgrades itself from the agent GUI / the cloud console. Older agents: rerun the install
 *  command (the upgrade-only script, §19.1) once to reach a version ≥ this. */
export const GUI_UPGRADE_MIN_VERSION = '0.10.0';
/** A new agent must report healthy within this many seconds after the restart, else it is rolled back to opm-agent.prev. */
export const AGENT_UPGRADE_HEALTH_SEC = 60;
/** A cloud upgrade request that no agent picked up expires after this many seconds. */
export const AGENT_UPGRADE_REQUEST_TTL_SEC = 24 * 3600;
/** The pinned agent release (deps/manifest.json agent; else OPM_AGENT_VERSION / agent/latest). notes = optional release notes (plain text / light markdown, ≤ 8000 chars). */
export interface AgentReleaseInfo {
  version: string;
  notes: string | null;
}
export type AgentUpgradePhase = 'queued' | 'download' | 'verify' | 'swap' | 'restart' | 'health' | 'done' | 'failed' | 'rolled-back';
export type AgentUpgradeVia = 'gui' | 'cloud' | 'cli' | 'mesh';
/** One upgrade attempt on one site (agent: <agentHome>/upgrade/state.json, survives the restart; heartbeat `upgrade`). */
export interface AgentUpgradeProgress {
  upgradeId: Ulid;
  /** the cloud request this attempt serves (via cloud) */
  requestId: string | null;
  /** the upgrade-all run (cloud run id or agent mesh run id) */
  runId: string | null;
  via: AgentUpgradeVia;
  siteSlug: Slug;
  from: string;
  to: string;
  phase: AgentUpgradePhase;
  /** download fraction 0..1 while phase = download; null otherwise */
  progress: number | null;
  bytesDone: number;
  bytesTotal: number | null;
  /** the phase that failed when phase = failed | rolled-back (rolled-back → usually health); absent / null otherwise */
  failedPhase?: AgentUpgradePhase | null;
  /** filled in phase verify (and kept) */
  verification: ReleaseVerification | null;
  error: string | null;
  actor: string | null; // email
  startedAt: Ts;
  updatedAt: Ts;
  finishedAt: Ts | null;
}
/** Result of checking a release (agent: opm-agent verify-release parity). */
export interface ReleaseVerification {
  version: string;
  sha256: string; //            the pinned sha256 (hex)
  signatureOk: boolean; //      Ed25519 signature of the sha256 hex by the release key
  publicKeySource: 'agent.json' | 'cloud' | 'flag';
  /** "SHA256:" + base64 (no padding) of sha256(raw public key) — the same style as ssh fingerprints */
  publicKeyFingerprint: string;
  /** downloaded file sha256 = pinned sha256; null = not downloaded yet */
  sha256Ok: boolean | null;
  /** the downloaded binary answered `<new> version` with the expected version; null = not run yet */
  selfTestOk: boolean | null;
  error: string | null;
  checkedAt: Ts;
}
/** A person asked the cloud to upgrade one site (never automatic). agents/{site}.upgradeRequest; CloudView.agentUpgrades. */
export interface AgentUpgradeRequest {
  requestId: string; //   ULID
  siteSlug: Slug;
  version: string; //     the pinned version at request time; the agent refuses (upgrade-failed) when the pin moved
  requestedBy: string; // email (the agent uses it as the actor of its activities)
  requestedByUid: Uid;
  requestedAt: Ts;
  expiresAt: Ts; //       requestedAt + AGENT_UPGRADE_REQUEST_TTL_SEC
  runId: string | null;
}
export type AgentUpgradeRunSiteStatus = 'pending' | 'requested' | 'running' | 'done' | 'failed' | 'skipped' | 'cancelled';
/** "Upgrade all": one site at a time, stop on the first failure (cloud: deployments/{id}.agentUpgradeRun; agent mesh:
 *  the coordinating agent's <agentHome>/upgrade/run.json). */
export interface AgentUpgradeRun {
  runId: string;
  origin: 'cloud' | 'agent';
  /** agent origin: the coordinating site (always upgraded LAST, because it restarts) */
  coordinator: Slug | null;
  version: string;
  requestedBy: string;
  requestedAt: Ts;
  status: 'running' | 'done' | 'failed' | 'cancelled';
  sites: { siteSlug: Slug; status: AgentUpgradeRunSiteStatus; from: string | null; requestId: string | null; error: string | null; at: Ts | null }[];
  finishedAt: Ts | null;
}
/** Compare agent versions "MAJOR.MINOR.PATCH[-BUILD]" (BUILD = the MMDDHHMM stamp of make dist): core numerically, then
 *  BUILD (absent = 0; numeric when both are digits, else string order). Unparsable versions sort first. */
export function compareAgentVersions(a: string, b: string): number {
  const p = (v: string): [number, number, number, string] | null => {
    const m = /^v?(\d+)\.(\d+)\.(\d+)(?:[-+](.+))?$/.exec((v ?? '').trim());
    return m ? [Number(m[1]), Number(m[2]), Number(m[3]), m[4] ?? ''] : null;
  };
  const x = p(a);
  const y = p(b);
  if (!x || !y) return x ? 1 : y ? -1 : 0;
  for (let i = 0; i < 3; i++) if (x[i] !== y[i]) return (x[i] as number) < (y[i] as number) ? -1 : 1;
  const [s, t] = [x[3], y[3]];
  if (s === t) return 0;
  if (s === '') return -1;
  if (t === '') return 1;
  if (/^\d+$/.test(s) && /^\d+$/.test(t)) return Number(s) < Number(t) ? -1 : Number(s) > Number(t) ? 1 : 0;
  return s < t ? -1 : 1;
}
/** true when the agent can upgrade itself from the GUI / the cloud (core version ≥ GUI_UPGRADE_MIN_VERSION). */
export function supportsGuiUpgrade(agentVersion: string | null | undefined): boolean {
  if (!agentVersion) return false;
  const core = /^v?(\d+\.\d+\.\d+)/.exec(agentVersion.trim());
  return !!core && compareAgentVersions(core[1], GUI_UPGRADE_MIN_VERSION) >= 0;
}
/** true when `latest` is newer than `installed` (an unknown side → false). */
export function agentUpgradeAvailable(installed: string | null | undefined, latest: string | null | undefined): boolean {
  if (!installed || !latest) return false;
  return compareAgentVersions(installed, latest) < 0;
}
export interface HostMetricsSummary {
  cpu: number; //     0..1
  memPct: number; //  0..1
  diskPct: number; // 0..1
}
/** §21 #92 install token kinds:
 *  - `install` (default; absent on older docs): one fresh-site enrollment within 24 h; after it enrolled the site it stays
 *    valid for upgrade / reinstall / downgrade of THAT enrollment (upgrade-only script, §21 #86), never for a second enroll.
 *  - `maintenance`: minted by "Regenerate" of an enrolled site's upgrade command; bound to the enrollment
 *    (`boundEnrolledAt` = agent.enrolledAt); can only fetch the upgrade-only script / binary / deps of that site, never
 *    enrolls; no expiry — ends when regenerated again (revokedAt) or when the enrollment is revoked. */
export type InstallTokenKind = 'install' | 'maintenance';
export const INSTALL_TOKEN_KINDS = ['install', 'maintenance'] as const satisfies readonly InstallTokenKind[];
export interface InstallToken {
  tokenHash: string; // doc id = sha256 hex of the token string (without "sha256:" prefix)
  siteSlug: Slug;
  /** §21 #92 absent = 'install' (docs before #92) */
  kind?: InstallTokenKind;
  /** §21 #84 / #92: the plaintext "opi_…" token (revealInstallCommand / revealUpgradeCommand). Kept while the token can be
   *  used: an install token while active AND after it enrolled the site (the enrollment's upgrade command, #92); a
   *  maintenance token until it is revoked. Set to null on revoke, on expiry of an unused install token (daily job) and
   *  when the enrollment it belongs to is revoked. null on tokens created before #84 and on install tokens used before #92
   *  ("not stored — Regenerate"). Never in InstallTokenView. */
  token: string | null;
  /** install: createdAt + 24 h (enroll deadline; no expiry for the upgrade use after enrolling). maintenance: null (never). */
  expiresAt: Ts | null;
  /** §21 #92 maintenance only: agent.enrolledAt of the enrollment this token belongs to */
  boundEnrolledAt?: Ts | null;
  usedAt: Ts | null;
  revokedAt: Ts | null;
  createdAt: Ts;
  createdBy: Uid;
}

// ───────────────────────────────────────────────────────────── activities
export type ActivityOrigin = 'agent' | 'cloud' | 'system';
export interface Activity {
  activityId: string; // "<siteSlug>:<seq>"; cloud-origin log uses siteSlug "_cloud"
  deploymentId: Id;
  siteSlug: string; //   Slug or "_cloud"
  seq: number; //        1, 2, 3 … per log, no gaps
  ts: Ts;
  actor: string | null; // email (agent), uid (cloud), null = system
  origin: ActivityOrigin;
  type: string; //       ^[a-z]+(-[a-z]+)*(\.[a-z-]+)*$ — see ACTIVITY_TYPES
  summary: string; //    1–300
  data: Record<string, unknown>; // ≤ 16 KB canonical JSON, never secret values
  prevHash: Sha256 | null; // null only for seq 1
  hash: Sha256; //       see canonical.ts activityHashInput()
  uploadedVia: 'pipe' | 'manual' | null; // set by the cloud
  uploadedAt: Ts | null; //                 set by the cloud
}
export const ACTIVITY_TYPES = [
  'install.enrolled',
  'agent.started',
  'agent.port-bind-failed',
  'login.succeeded',
  'login.failed',
  'logout',
  'actor.declared',
  'access-key.rotated',
  'access-key.received',
  'access-key.revealed', //  §21 #84 (cloud: revealAccessKey)
  'proposal.published',
  'proposal.created',
  'proposal.staged',
  'proposal.stale',
  'proposal.superseded',
  'proposal.approved',
  'update.started',
  'update.snapshot',
  'update.applied',
  'update.failed',
  'update.rolled-back',
  'update.abandoned',
  'update.terminated',
  'update.recovered',
  'config.local-change',
  'snapshot.created',
  'snapshot.downloaded',
  'snapshot.imported',
  'snapshot.deleted',
  'snapshot.rolled-back',
  'volume.seeded',
  'volume.reset-to-seed',
  'transfer.sent',
  'transfer.received',
  'transfer.saved',
  'secret.revealed',
  'secret.regenerated', //   §21 #84 (cloud: regenerateSecret; data { secretSlug, draftVersion })
  'secret.fetched',
  'internet.tunnel.started',
  'internet.tunnel.stopped',
  'internet.tunnel.revoked',
  'internet.tunnel.expired',
  'debug.downloaded',
  'edge.reloaded',
  'edge.reload-failed',
  'cert.issued',
  'cert.renewed',
  'cert.failed',
  'cert.local-ca-downloaded',
  'site.unreachable',
  'site.reachable',
  'deployment.created',
  'deployment.updated',
  'install-token.created',
  'install-token.revoked',
  'install-token.revealed', // §21 #84 (cloud: revealInstallCommand)
  'install-command.revealed', //    §21 #92 (cloud: revealUpgradeCommand) data { siteSlug, tokenHash, kind }
  'install-command.regenerated', // §21 #92 (cloud: regenerateUpgradeCommand) data { siteSlug, tokenHash, revoked: tokenHash[] }
  'agent.revoked',
  'draft.reset-to-head',
  'draft.updated', //       a draft section / site saved in the cloud (data.paths = changed paths, secrets redacted)
  'transfer.deleted', //    a transfer deleted in the cloud
  'activities.uploaded', // a .opmlog uploaded by hand (counts)
  'deployment.deleted', //  soft delete
  // §21 #89 agent operator toolbox (agent/internal/tools)
  'terminal.opened', //     data { terminalId, kind: container|host, target, command, via?, forwardedTo? }
  'terminal.closed', //     data { terminalId, kind, target, durationSec, exitCode, reason, bytesOut }
  'container.restarted', // data { containerSlug, ref, reason? }
  'container.stopped', //   data { containerSlug, ref, reason?, expectedRunning } (manuallyStopped until start / next update)
  'container.started', //   data { containerSlug, ref, reason? }
  'tool.netcheck', //       data { from, check, target, ok, durationMs, summary }
  'volume.downloaded', //   data { volumeSlug, path, kind: file|archive }
  'docker.images-pruned', // data { deleted, reclaimedBytes }
  // §21 #90 agent self-management (agent GUI Agent page, cloud "Upgrade agent", opm-agent upgrade)
  'agent.upgrade-started', //  data { upgradeId, from, to, via: gui|cloud|cli|mesh, requestId?, runId? }
  'agent.upgraded', //         data { upgradeId, from, to, durationSec, via } (written by the NEW agent after it came up healthy)
  'agent.upgrade-failed', //   data { upgradeId, from, to, phase, error, via }
  'agent.rolled-back', //      data { upgradeId, from (failed target), to (restored version), reason, by: watcher|guard }
  'agent.restarted', //        data { reason, via: systemd|exec } (restart from the GUI; upgrades write agent.upgrade-* instead)
  'agent.config-changed', //   data { changed: string[], before, after } (agent.json safe settings; never secrets)
  'agent.support-bundle-downloaded', // data { sizeBytes, sections: string[] }
  'agent.upgrade-requested', // cloud: data { requestId, siteSlug, version, runId? } (+ audit deployment.agent-upgrade-requested)
  'agent.upgrade-cancelled', // cloud: data { requestId?, runId?, siteSlug? }
  // §21 #94 Template sync (cloud)
  'draft.template-synced', //        data { templateId, fromVersion, toVersion, relinked, previousTemplateId?, taken, skipped, conflicts, draftVersion, keys }
  'deployment.template-unlinked', // data { templateId, baseVersion }
  // §21 #96 volume manager (agent GUI Tools → Volumes)
  'volume.emptied', //            data { volumeSlug, siteSlug, opId, snapshotId, stopped[], bytesBefore, durationMs, fastForward? }
  'volume.copied', //             data { volumeSlug, siteSlug, opId, target { kind, path, volumeSlug }, bytes, files, stopped[], preserve, durationMs, copyId }
  'volume.restored', //           data { volumeSlug, siteSlug, opId, source { kind, ref, path }, snapshotId, bytes, files, stopped[], durationMs, fastForward? }
  'volume.op-failed', //          data { volumeSlug, siteSlug, opId, kind, step, error, stopped[], durationMs }
  'volume.copy-deleted', //       data { copyId, volumeSlug, path, sizeBytes }
  'volume.action-forwarded', //   the forwarding site of a mesh volume action: data { siteSlug, forwardedTo, method, action, status }
  'volume.file-written', //       data { volumeSlug, path, sizeBytes, created }
  'volume.file-uploaded', //      data { volumeSlug, path, sizeBytes, overwritten }
  'volume.file-folder-created', // data { volumeSlug, path }
  'volume.file-moved', //         data { volumeSlug, from, to, overwritten }
  'volume.file-deleted', //       data { volumeSlug, paths[], entries }
  // §21 #101 volume content + persist-to-cloud (agent: snapshot / apply; cloud `_cloud`: edits, sends)
  'volume.placeholder-filled', //       agent: data { volumeSlug, path, sha256, size, formerFileSlug } — a 0-byte former File mountpoint got its content (migration)
  'volume-content.snapshot', //         agent: data { volumeSlug, snapshotId, trigger, treeHash, fileCount, totalBytes, excludedCount, skippedCount, requestId?, pushId? }
  'volume-content.snapshot-uploaded', // agent: data { volumeSlug, snapshotId, versionId, seq }
  'volume-content.snapshot-failed', //  agent: data { volumeSlug, trigger, error }
  'volume-content.conflict', //         agent: data { volumeSlug, pushId, versionId, conflicts, liveTreeHash, baseTreeHash }
  'volume-content.applied', //          agent: data { volumeSlug, pushId, versionId, resolution, written, deleted, keptLive, snapshotId, restarted[], fastForward? }
  'volume-content.kept-live', //        agent: data { volumeSlug, pushId, versionId }
  'volume-content.apply-failed', //     agent: data { volumeSlug, pushId, versionId, step, error }
  'volume-content.version-created', //  cloud: data { siteSlug, volumeSlug, versionId, seq, origin, treeHash, fileCount, totalBytes, baseVersionId, added, removed, modified }
  'volume-content.snapshot-requested', // cloud: data { siteSlug, volumeSlug, requestId }
  'volume-content.sent', //             cloud: data { siteSlug, volumeSlug, pushId, versionId, baseVersionId, restart, supersededPushId }
  'volume-content.push-cancelled', //   cloud: data { siteSlug, volumeSlug, pushId, versionId }
  // §21 #97 dev tools (agent GUI Dev tools; data DevToolQueryActivity for devtools.query — never result data)
  'devtools.query', //               data DevToolQueryActivity { connectionId, connectionName, kind, target, op, statement (redacted, ≤ 4 KiB), durationMs, rows, status, ok, error, … }
  'devtools.query-cancelled', //     data { connectionId, connectionName, kind, queryId, wasRunning }
  'devtools.connection-created', //  data { connectionId, name, kind, target, readOnly, duplicatedFrom? }
  'devtools.connection-updated', //  data { connectionId, name, kind, target, readOnly, changed[], credentialChanged }
  'devtools.connection-deleted', //  data { connectionId, name, kind }
  'devtools.connection-tested', //   data { connectionId|null, name, kind, target, ok, latencyMs, error }
  'devtools.credential-revealed', // data { connectionId|environmentId, name, field }
  'devtools.mqtt-published', //      data { connectionId, connectionName, topic, qos, retain, bytes, ok, error, durationMs }
  'devtools.mqtt-subscribed', //     data { connectionId, connectionName, sessionId, filters[] }
  'devtools.mqtt-unsubscribed', //   data { connectionId, connectionName, sessionId, filters[], messages, durationSec, reason }
  'devtools.action-forwarded', //    the forwarding site of a mesh dev-tools write: data { siteSlug, forwardedTo, method, action, connectionId, status }
  // §21 #104 branches + commits (data always { branch, commitId, seq, message (first line), source?, picks, skipped })
  'branch.created', //        cloud: a template / deployment created from another branch (data.from)
  'branch.committed', //      cloud (deployment) / agent: working copy committed
  'branch.merged', //         every source change taken
  'branch.cherry-picked', //  some source changes taken
  'branch.reverted',
  'branch.reset',
  'branch.merge-failed', //   agent: the apply of a merge failed and was rolled back (no commit) — data { step, siteSlug, error, failures? }
  'working.discarded', //     cloud: working copy reset to the head; agent: GUI edits dropped
  'drift.detected', //        agent: data { items, notes, keys[≤50] } (only when the set changes)
  'drift.committed', //       agent: data { commitId, keys }
  'drift.discarded', //       agent: data { keys, updateId }
  'drift.accepted', //        agent: data { fingerprints, commitId }
  'agent-branch.pushed', //   cloud: data { siteSlug, commitIds[≤20], head }
] as const;
export type ActivityType = (typeof ACTIVITY_TYPES)[number];

// ───────────────────────────────────────────────────────────── transfers + uploads
export type TransferDirection = 'to-site' | 'to-cloud';
export type TransferStatus = 'uploading' | 'ready' | 'saved-on-server' | 'downloaded' | 'expired';
export interface Transfer {
  transferId: Id;
  deploymentId: Id;
  siteSlug: Slug;
  direction: TransferDirection;
  fileName: string; //   1–255, no "/" "\\" NUL
  sizeBytes: number; //  ≤ 2147483648
  sha256: Sha256;
  storagePath: string; // "transfers/<deploymentId>/<transferId>"
  uploadId: Id | null;
  createdAt: Ts;
  createdBy: string; //  uid (cloud) or email (agent)
  note: string; //       0–500
  expiresAt: Ts; //      createdAt + 30 days
  status: TransferStatus;
  savedPath: string | null;
  error: string | null;
  /** §21 #100 (cloud, to-site): when the site's agent had the file stored on the site (agents ≥ 0.11.2 report `fetched`);
   *  null / absent = not fetched yet (or an older agent that does not report it) */
  fetchedAt?: Ts | null;
  /** §21 #100 (agent API, to-cloud): the server path the file was sent from; null / absent = a browser upload */
  sourcePath?: string | null;
  /** §21 #100 (agent API only, never stored in the cloud): store-and-forward progress on this site */
  delivery?: TransferDelivery;
}
/**
 * §21 #100 store-and-forward state on the agent:
 * - to-cloud: `queued` (stored on the site, waiting for a cloud route / retry) → `uploading` (parts going up) → `stored` (the cloud has it)
 * - to-site:  `pending` (stored in the cloud, waiting to be fetched) → `downloading` → `on-site` (stored on this site)
 */
export type TransferPhase = 'queued' | 'uploading' | 'stored' | 'pending' | 'downloading' | 'on-site';
export interface TransferDelivery {
  phase: TransferPhase;
  bytesDone: number; //            bytes the receiving side has (to-cloud: parts the cloud acknowledged; to-site: bytes on disk)
  attempts: number; //             failed attempts since the last success (0 = none)
  lastError: string | null;
  lastAttemptAt: Ts | null;
  nextAttemptAt: Ts | null; //     null = now / as soon as a route exists
}
export type TransferMeta = Pick<Transfer, 'transferId' | 'siteSlug' | 'direction' | 'fileName' | 'sizeBytes' | 'sha256' | 'status' | 'createdAt' | 'createdBy' | 'note' | 'expiresAt'>;

export type UploadPurpose = 'blob' | 'transfer' | 'seed-archive'; // seed-archive (round E): .tar.gz volume starter content ≤ SEED_ARCHIVE_MAX
export interface Upload {
  uploadId: Id;
  purpose: UploadPurpose;
  deploymentId: Id | null;
  transferId: Id | null;
  sizeBytes: number;
  sha256: Sha256;
  partSize: number; //   8388608 (8 MiB); last part may be smaller
  partCount: number; //  ceil(sizeBytes / partSize), ≥ 1
  partsReceived: number[];
  status: 'open' | 'complete' | 'failed';
  error: string | null;
  createdAt: Ts;
  createdBy: string;
  expiresAt: Ts; //      createdAt + 24 h
}
export const UPLOAD_PART_SIZE = 8 * 1024 * 1024;
export const SMALL_BLOB_MAX = 24 * 1024 * 1024; // POST /v1/blobs raw body limit
/** GET /v1/blobs/{sha256} for humans: bodies up to this size stream with 200 (no redirect); larger → 302 signed URL */
export const BLOB_STREAM_MAX = 8 * 1024 * 1024;
export const TRANSFER_MAX = 2 * 1024 * 1024 * 1024;

export interface UptimeDay {
  siteSlug: Slug;
  date: string; //     yyyymmdd UTC
  beats: number[]; //  24 items: heartbeats received per hour (expected 120)
  hours: number[]; //  24 items: min(1, beats/120)
  updatedAt: Ts;
}

// ───────────────────────────────────────────────────────────── agent: cloud view, mesh, distribution
export interface CloudView {
  fetchedAt: Ts; //               cloud SERVER time of the poll answer
  source: 'cloud' | `peer:${string}`;
  cursor: string; //              String(deployment.agentEpoch)
  deploymentId: Id;
  deleted: boolean;
  proposals: ProposalMeta[]; //   every proposal whose status is not terminal, plus the 5 newest terminal ones
  transfersToSite: TransferMeta[]; // to-site transfers with status ready (all sites; each agent filters its own)
  draftHead: { revId: RevId; seq: number } | null; // deployment.head as the cloud knows it
  accessKey: { hash: string; version: number };
  peers: PeerInfo[]; //           every enrolled site of the deployment
  /** cloud console origin (env OPM_CONSOLE_URL, else the first https OPM_FRONTEND_ORIGINS entry); absent on old clouds */
  consoleUrl?: string | null;
  /** Deployment.label (the agent GUI shows it); absent on old clouds */
  deploymentLabel?: string | null;
  /** §21 #90 the pinned agent release (version + notes) for the Agent page; absent on old clouds */
  agentRelease?: AgentReleaseInfo | null;
  /** §21 #90 queued, unexpired cloud upgrade requests (each agent runs only its own siteSlug's, once per requestId) */
  agentUpgrades?: AgentUpgradeRequest[];
  /** §21 #101 persist-to-cloud volumes with a PENDING push or an open snapshot request (all sites; each agent filters its own) */
  volumeContent?: import('./volume-content.js').VolumeContentCloudItem[];
  /** §21 #104 the deployment branch head (what the sites should run) + the newest commits; absent on old clouds */
  branch?: import('./branch.js').CloudBranchView;
}
export interface HaveList {
  siteSlug: Slug;
  generatedAt: Ts;
  internet: boolean; // reached the cloud in the last 90 s (directly)
  blobs: Sha256[];
}
export type BlobKind = 'layer' | 'config' | 'manifest' | 'file';
export interface FetchRequest {
  sha256: Sha256;
  sizeBytes: number;
  kind: BlobKind;
  ref: { serviceSlug: Slug; version: string } | { fileSlug: Slug; revId: RevId };
}
export interface FetchRequestResponse {
  fetcher: Slug;
  status: 'queued' | 'downloading' | 'have' | 'failed';
  error: string | null;
}

export interface UpdateLock {
  updateId: Ulid;
  kind: 'update' | 'rollback' | 'merge';
  revId: RevId; //          target revision
  coordinator: Slug;
  startedAt: Ts;
  lastContactAt: Ts;
}
/** GET /_opm/peer/ping response. Also used by the GUI for the site tiles. */
export interface PeerPing {
  deploymentId: Id;
  siteSlug: Slug;
  agentVersion: string;
  serverTime: Ts;
  head: { revId: RevId; seq: number; hash: Sha256 } | null;
  lock: UpdateLock | null;
  proposals: { revId: RevId; status: ProposalStatus; staging: SiteStaging }[];
  internet: boolean;
  cloudRoute: 'direct' | 'relay' | 'tunnel' | 'none';
  cloudFetchedAt: Ts | null;
  keyVersion: number;
  agentPort: number;
  guiAliasPort: number | null;
  edge: { running: boolean; httpPort: number | null; httpsPort: number | null; lastError: string | null };
  inConfig: boolean; //       this site is in the head config
  /** §21 #104 agents ≥ 0.14: the site's branch (head, merged from, uncommitted) */
  branch?: import('./branch.js').AgentBranchReport | null;
}

export type UpdateStepName = 'lock' | 'snapshot' | 'apply' | 'verify' | 'commit' | 'rollback' | 'unlock';
export interface UpdatePlan {
  updateId: Ulid;
  /** §21 #104 'merge' = a branch merge / commit-with-apply / discard / revert / reset / fast-forward (per-site targets) */
  kind: 'update' | 'rollback' | 'merge';
  revision: Revision; //        target revision (rollback: the new "rollback to …" revision built by the coordinator)
  fromRevId: RevId | null; //   head before the update
  preSnapshotId: Ulid; //       id of the pre-update snapshot (same on every site)
  targetSnapshotId: Ulid | null; // rollback only
  sites: Slug[]; //             participating sites (head config ∪ target config)
  order: Slug[]; //             apply order (topological by site depends; removed sites first)
  coordinator: Slug;
  approvedBy: string;
  approvedAt: Ts;
  /** §21 #104 per-site targets (0.14 plans): each site moves from ITS head to ITS new commit; `revision` = the
   *  coordinator's own target (or the first site's) for old readers */
  targets?: Record<Slug, { revision: Revision; fromRevId: RevId | null }>;
  /** §21 #104 the commit each site writes at COMMIT (message, kind, source …; config = targets[site].revision.config) */
  commits?: Record<Slug, Omit<import('./branch.js').Commit, 'config' | 'catalog'>>;
}
export interface UpdateStepRequest {
  step: UpdateStepName;
  plan?: UpdatePlan; //         required for "lock"
}
export type ContainerAction = 'keep' | 'recreate' | 'create' | 'remove' | 'stop';
export interface UpdateSiteState {
  updateId: Ulid;
  siteSlug: Slug;
  step: UpdateStepName | null;
  state: 'idle' | 'running' | 'ok' | 'failed';
  error: string | null;
  preSnapshotId: Ulid | null;
  startedAt: Ts | null;
  finishedAt: Ts | null;
  containers: {
    containerSlug: Slug;
    serviceSlug: Slug;
    action: ContainerAction;
    state: 'pending' | 'running' | 'ok' | 'failed';
    error: string | null;
    /** §21 #87: diagnostics captured before the rollback (absent on old agents / containers that did not fail) */
    failure?: ContainerFailure | null;
  }[];
}
/** §21 #87: one readiness / health check of a container while an update waited for it */
export interface HealthCheckRecord {
  at: Ts;
  ok: boolean;
  /** false = inside the image's startPeriodSec (or "ready after 10 s" for type none): it does not count towards retries */
  counted: boolean;
  message: string;
}
/** §21 #87: what OPM saw when a container failed APPLY / VERIFY (captured BEFORE the rollback removes it) */
export interface ContainerFailure {
  containerSlug: Slug;
  dockerName: string;
  /** the stopped failed container, renamed `<dockerName>-failed-<unix>` for `docker logs`; removed after the next
   *  successful update of the site or after 24 h; null = not kept (e.g. it never got created) */
  keptAs: string | null;
  error: string;
  state: { status: string; exitCode: number; oomKilled: boolean; restartCount: number; error: string; startedAt: string; finishedAt: string } | null;
  /** docker inspect port map, e.g. "8080/tcp=0.0.0.0:6007,127.0.0.1:32768" ("none" while Docker restarts it) */
  ports: string;
  /** last ≤ 200 log lines (stdout + stderr, "<ts> <stream> <text>"), deployment secrets redacted */
  logTail: string[];
  healthHistory: HealthCheckRecord[];
  capturedAt: Ts;
  /** §21 #93: a log-pattern hint, e.g. "northwind-backend-admin could not resolve `mqtt` (EAI_AGAIN) — is mqtt
   *  running/healthy? add it to Depends on" (DNS failure naming a container of this site); null / absent = none */
  hint?: string | null;
}
export interface UpdateRun {
  updateId: Ulid;
  kind: 'update' | 'rollback' | 'merge';
  revId: RevId;
  coordinator: Slug;
  approvedBy: string;
  approvedAt: Ts;
  phase: UpdateStepName | 'done' | 'failed';
  result: { ok: boolean; message: string; snapshotId: Ulid | null } | null;
  sites: Record<Slug, UpdateSiteState>;
  startedAt: Ts;
  finishedAt: Ts | null;
  /** §21 #95: the approval / rollback wait was fast-forwarded (absent / null = waited at ×1) */
  fastForward?: FastForwardUse | null;
}

export interface ApproveIntent {
  intentId: Ulid;
  kind: 'update' | 'rollback' | 'volume-reset' | 'volume-empty' | 'volume-restore' | 'volume-content-apply';
  revId: RevId | null; //       update
  snapshotId: Ulid | null; //   rollback
  /** volume-reset (round E): the volume to reset to its starter content (this site) */
  volume?: { siteSlug: Slug; volumeSlug: string } | null;
  actor: string;
  sessionId: Ulid;
  notBefore: Ts; //             createdAt + 60 s (OPM_APPROVE_WAIT_SEC) = the LATEST ready time (all at ×1)
  expiresAt: Ts; //             notBefore + 600 s
  /** §21 #95: the effective wait the agent requires (60, or OPM_APPROVE_WAIT_SEC); absent on agents before 0.10.2 */
  waitSec?: number;
}

// ───────────────────────────────────────────────────────────── §21 #95 hold-to-fast-forward of an intent wait
/**
 * The 60 s intent wait (approve, rollback to a snapshot, reset a volume to its starter content) counts EFFECTIVE seconds:
 * effective = Σ speed × real dt, integrated by the agent on ITS OWN clock. While the person holds "Hold to fast-forward"
 * the GUI sends `intentSpeed {speed}` on every change plus a heartbeat every INTENT_SPEED_HEARTBEAT_MS while speed > 1. A
 * speed applies from the moment the agent received it until the next speed message, or until INTENT_SPEED_DECAY_MS pass
 * without one — then it decays to ×1 server-side (a lost release never keeps it fast). ACCEPT needs effective ≥ waitSec, so
 * the minimum real wait is waitSec / INTENT_MAX_SPEED (15 s for 60 s).
 */
export type IntentSpeed = 1 | 2 | 4;
export const INTENT_SPEEDS: readonly IntentSpeed[] = [1, 2, 4];
export const INTENT_MAX_SPEED: IntentSpeed = 4;
/** GUI heartbeat while speed > 1 */
export const INTENT_SPEED_HEARTBEAT_MS = 500;
/** agent: a speed > 1 without a heartbeat for this long decays to ×1 */
export const INTENT_SPEED_DECAY_MS = 1500;
/** GUI: hold → ×2 at once, ×4 after this much continuous holding */
export const INTENT_SPEED_X4_AFTER_MS = 1500;
/** GUI: release → the shown speed eases back to ×1 over this long (the agent gets ×1 at once) */
export const INTENT_SPEED_EASE_MS = 400;

export interface IntentSpeedRequest {
  speed: IntentSpeed;
}
/** The agent's authoritative view of an intent wait (answer of getIntent / intentSpeed). */
export interface IntentProgress {
  intentId: Ulid;
  kind: ApproveIntent['kind'];
  waitSec: number; //           the effective seconds required
  effectiveSec: number; //      accumulated Σ speed × dt up to serverTime
  remainingSec: number; //      max(0, waitSec − effectiveSec)
  speed: IntentSpeed; //        the speed the agent applies now (1 after a decay)
  /** a speed > 1 decays to ×1 at this time unless a heartbeat arrives (null at ×1) */
  speedUntil: Ts | null;
  maxSpeed: IntentSpeed; //     the highest speed used so far
  ready: boolean; //            effectiveSec ≥ waitSec (ACCEPT allowed)
  createdAt: Ts;
  serverTime: Ts; //            the agent clock when this answer was made
}
/** Recorded in the approve / rollback / volume-reset activity data (and run / proposal) when speed > 1 was used. */
export interface FastForwardUse {
  usedBy: string; //            the actor who held the button (the intent actor)
  maxSpeed: IntentSpeed; //     2 | 4
  realSeconds: number; //       real seconds from the start of the wait until it was complete (1 decimal)
  effectiveSeconds: number; //  the effective seconds counted (= waitSec)
}

export interface GateCheck {
  open: boolean;
  /** (a) parent = head everywhere · (b) every site reachable · (c) staging ready · (d) no update running */
  conditions: { key: 'parent' | 'reachable' | 'staged' | 'idle' | 'status'; ok: boolean; detail: string }[];
  sites: Slug[];
}

// ───────────────────────────────────────────────────────────── agent: snapshots, logs, tunnel, actors
export type SnapshotReason = 'pre-update' | 'manual' | 'scheduled' | 'imported';
export interface SnapshotVolume {
  /** the declared site volume (SiteVolume.volumeSlug); restore is keyed by it. Snapshots written before round D carry `name` (read as volumeSlug). */
  volumeSlug: string;
  /** containerSlugs that mount it at snapshot time (informational; a restore does not depend on them) */
  containers: Slug[];
  sizeBytes: number; // archive size
  sha256: Sha256; //    archive sha256
  path: string; //      relative to the snapshot dir: "sites/<siteSlug>/volumes/<volumeSlug>.tar.zst"
}
export interface SnapshotSite {
  config: Config;
  images: { serviceSlug: Slug; version: string; digest: Sha256; configDigest: Sha256 }[];
  volumes: SnapshotVolume[];
  sizeBytes: number;
}
export interface Snapshot {
  snapshotId: Ulid;
  createdAt: Ts;
  reason: SnapshotReason;
  revId: RevId | null; //  head at snapshot time (null = empty head)
  seq: number; //          0 when the head was empty
  sites: Record<Slug, SnapshotSite>;
  createdBy: string; //    email or "system"
  note: string; //         0–500, the only editable field
  /** revision + catalog at snapshot time (needed to restore offline) */
  revision: Revision | null;
}
export interface SnapshotSummary {
  snapshotId: Ulid;
  createdAt: Ts;
  reason: SnapshotReason;
  revId: RevId | null;
  seq: number;
  createdBy: string;
  note: string;
  sizeBytes: number;
  sites: Slug[]; //        sites that hold their part
  missingSites: Slug[]; // sites of the snapshot config whose part is missing (cannot roll back)
  hasImages: boolean;
}
export interface OpmsnapManifest {
  format: 'opmsnap/1';
  deploymentId: Id;
  snapshot: Snapshot;
  parts: { path: string; sizeBytes: number; sha256: Sha256 }[];
  includeImages: boolean;
  images: { serviceSlug: Slug; version: string; digest: Sha256; configDigest: Sha256; blobs: Sha256[] }[];
  createdAt: Ts;
  createdBy: string;
  signedBy: { siteSlug: Slug; publicKey: string };
}
export interface OpmlogManifest {
  format: 'opmlog/1';
  deploymentId: Id;
  createdAt: Ts;
  createdBy: string;
  signedBy: { siteSlug: Slug; publicKey: string };
  sites: { siteSlug: Slug; fromSeq: number; toSeq: number; count: number }[];
  activities: { path: 'activities.ndjson'; sizeBytes: number; sha256: Sha256 };
  debug: { siteSlug: Slug; path: string; sizeBytes: number; sha256: Sha256 }[];
}

export type TunnelLeaseStatus = 'active' | 'stopped' | 'revoked' | 'expired';
export interface TunnelLease {
  leaseId: Ulid;
  actor: string;
  siteSlug: Slug;
  userAgent: string;
  startedAt: Ts;
  lastPollAt: Ts;
  status: TunnelLeaseStatus;
  reqs: number;
  bytes: number;
  lastError: string | null;
}
export interface TunnelRequest {
  reqId: Ulid;
  method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
  url: string; //                 absolute https URL (cloud API) — the agent never queues other hosts
  headers: Record<string, string>;
  bodyB64: string | null; //      ≤ 4 MiB decoded
  createdAt: Ts;
  timeoutSec: number; //          5–600, default 60
}
export interface TunnelResult {
  leaseId: Ulid;
  reqId: Ulid;
  status: number; //              0 = network error in the browser
  headers: Record<string, string>;
  bodyB64: string | null;
  chunked: boolean;
  error: string | null;
}

export interface ActorTokenClaims {
  email: string;
  sid: Ulid; //    session id
  dep: Id;
  site: Slug;
  iat: number;
  exp: number; //  = session expiry (unix seconds)
}
export interface ActorRow {
  email: string;
  lastUsedAt: Ts;
  count: number;
}
export interface AgentSession {
  sessionId: Ulid;
  createdAt: Ts;
  expiresAt: Ts; // createdAt + 6 h
  keyVersion: number;
  ip: string;
  userAgent: string;
}

// ───────────────────────────────────────────────────────────── agent: status + logs
export interface ContainerStatus {
  siteSlug: Slug;
  /** managed containers: the containerSlug; unmanaged ones (managed=false): the docker container name */
  containerSlug: Slug;
  /** managed containers: the service slug; unmanaged ones: "" (version "" and image = its docker image) */
  serviceSlug: Slug;
  version: string;
  /** the readable docker reference on the site: `<serviceSlug>:<version>` */
  image: string;
  kind: ImageKind;
  enabled: boolean;
  managed: boolean; //   false = a container without the opm.deployment label (shown as "not managed")
  state: 'running' | 'exited' | 'restarting' | 'created' | 'paused' | 'dead' | 'missing';
  exitCode: number | null;
  health: 'healthy' | 'unhealthy' | 'starting' | 'completed' | 'none';
  ready: boolean; //     "completed" rule
  restarts: number;
  cpuPct: number | null;
  memMb: number | null;
  startedAt: Ts | null;
  ports: { portName: Slug; containerPort: number; hostPort: number | null; protocol: 'tcp' | 'udp' }[];
  specHash: string | null;
  revSeq: number | null;
}
export interface HostMetrics {
  cpuPct: number; //  0..100
  memUsedMb: number;
  memTotalMb: number;
  diskUsedPct: number; // 0..100 of the dataDir filesystem
  diskFreeMb: number;
  loadAvg1: number | null;
}
export interface UptimeCell {
  t: Ts; //           bucket start
  up: number; //      0..1 fraction up (null samples excluded)
  samples: number;
}
export interface SiteStatus {
  siteSlug: Slug;
  reachable: boolean;
  lastSeenAt: Ts | null; // for a peer: last answer
  agentVersion: string | null;
  host: HostMetrics | null;
  containers: ContainerStatus[];
  uptime24h: { subject: string; cells: UptimeCell[] }[]; //  144 cells × 10 min (subject = "site" or containerSlug)
  uptime30d: { subject: string; cells: UptimeCell[] }[]; //  720 cells × 1 h
  certs: CertStatus[];
  edge: EdgeReport | null;
}
export interface DebugLine {
  ts: Ts;
  level: 'debug' | 'info' | 'warn' | 'error';
  module: string; // cloudlink | mesh | stager | expr | driver | edge | tunnel | health | coordinator | snapshot | pipe | transfer | auth | gui | distrib
  msg: string;
  fields: Record<string, unknown>;
  site?: Slug; //  set when merged from several sites
}

/** /etc/opm/agent.json (written by the install script). */
export interface AgentConfigFile {
  apiBase: string; //          e.g. "https://api.opm.example" — requests go to apiBase + "/v1/…"
  deploymentId: Id;
  siteSlug: Slug;
  dataDir?: string; //         default "/var/lib/opm"
  agentPort?: number; //       listen port before the first head (default 7080)
  guiAliasPort?: number | null; // default 8080 before the first head
  advertiseHost?: string; //   mesh address announced to peers (default: first non-loopback IPv4)
  dockerHost?: string; //      default "unix:///var/run/docker.sock" (or $DOCKER_HOST)
  proxy?: string | null; //    outbound HTTPS proxy
  installTokenFile?: string; // default "/etc/opm/install-token" (deleted after enroll)
  /** §21 #86 HTTP port of the built-in default edge (before the first head / site not in the head): default 80;
   *  null = no default edge (GUI only on guiAliasPort / agentPort). With a head the edge uses SiteConfig.edge.httpPort. */
  edgeHttpPort?: number | null;
  /** release signing public key (base64), written by the install script; `opm-agent upgrade` verifies with it */
  releasePublicKey?: string;
  /** cloud console origin for the agent GUI links (fallback when the cloud does not send consoleUrl) */
  consoleUrl?: string;
  /** §21 #88 docker container names: "plain" (default; the docker name = the containerSlug) | "prefixed"
   *  (opm_<deploymentId>_<siteSlug>_<containerSlug>, for several sites on one Docker host) */
  containerNames?: 'plain' | 'prefixed';
  /** §21 #89 operator toolbox switches (defaults true) */
  tools?: { hostShell?: boolean; containerExec?: boolean };
  hostShell?: { user?: string; shell?: string };
}

// ═════════════════════════════════════════════════════════════ API DTOs — cloud API (/v1/*)
export interface ListResponse<T> {
  items: T[];
}
export interface PageResponse<T> {
  items: T[];
  cursor: string | null; // opaque; pass back as ?cursor=
}
export interface Deleted {
  deleted: true;
}
export interface ValidationReport {
  ok: boolean; //                 true when there is no issue with severity "error"
  issues: ValidationIssue[];
}
export interface UsageItem {
  kind: 'deployment' | 'template';
  id: Id;
  label: string;
  siteSlug: Slug;
  containerSlug: Slug;
  serviceSlug: Slug;
  version: string;
}

export interface MeResponse {
  account: Account;
  /** convenience flags for the GUI (the API checks again) */
  isAdmin: boolean;
}

// ───────────────────────────────────────────────────────────── icons library (icons/{iconId})
export const ICON_CONTENT_TYPES = ['image/png', 'image/svg+xml', 'image/webp'] as const;
export type IconContentType = (typeof ICON_CONTENT_TYPES)[number];
export const ICON_MAX_BYTES = 65536;
export interface IconDoc {
  iconId: Id;
  name: string; //          1–80 (file name without extension by default)
  contentType: IconContentType;
  sizeBytes: number; //     1–65536 (after SVG sanitizing)
  sha256: Sha256; //        of the stored bytes; uploads are de-duplicated by it
  createdBy: Uid;
  createdAt: Ts;
}
/** Where an icon is used (DELETE /v1/icons/{id} answers 409 conflict with details.users = IconUser[]). */
export interface IconUser {
  type: 'service' | 'template' | 'tag' | 'deployment' | 'account';
  id: string;
  label: string;
}
export type IconListItem = IconDoc & { usedBy: number };
/** POST /v1/icons (raw body = the file bytes, Content-Type = one of ICON_CONTENT_TYPES, query ?name=). */
export interface UploadIconResponse {
  icon: IconDoc;
  /** true = these bytes were uploaded before; the existing icon is returned (nothing new stored) */
  existing: boolean;
}

// ───────────────────────────────────────────────────────────── registry browsing (register-image pickers; GET, not audited)
export interface RegistryRepository {
  name: string; //               e.g. "library/redis", "acme/api", "example/opm-test/hello-api"
  description: string | null;
  official: boolean; //          Docker Hub official image
  stars: number | null;
  pullCount: number | null;
  updatedAt: Ts | null;
}
export interface RegistryRepositoriesResponse {
  items: RegistryRepository[];
  /** how the list was made; "none" = this registry cannot list repositories (type the name) */
  source: 'docker-hub' | 'artifact-registry' | 'catalog' | 'none';
  /** false = listing is not supported or failed; the form keeps free text */
  supported: boolean;
  message: string | null;
}
export interface RegistryTag {
  tag: string;
  pushedAt: Ts | null;
  digest: Sha256 | null;
  sizeBytes: number | null;
}
export interface RegistryTagsResponse {
  items: RegistryTag[]; //       newest first when pushedAt is known, else by name
  source: 'docker-hub' | 'artifact-registry' | 'registry-v2' | 'none';
  supported: boolean;
  message: string | null;
}

export interface CreateRegistryRequest {
  label: string;
  url?: string;
  /** "" + "" = anonymous (public images, e.g. Docker Hub library/redis); otherwise both 1–255 / 1–8192 */
  username: string;
  password: string;
}
export interface PatchRegistryRequest {
  label?: string;
  url?: string;
  username?: string;
  password?: string;
}
export interface RevealPasswordResponse extends RevealResponse {
  /** = value (kept for older clients) */
  password: string;
}
export interface RegistryTestResponse {
  ok: boolean;
  message: string;
  at: Ts;
}

export type ServiceListItem = Service & { imageCount: number; usedBy: number };
export interface CreateServiceRequest {
  label: string;
  serviceSlug: Slug; //        "Name (id)": auto-derived from the label in the form, fixed after create
  icon?: Icon; //              default null
  description?: string;
}
export interface PatchServiceRequest {
  label?: string;
  icon?: Icon;
  description?: string;
}
export interface RegisterImageRequest {
  version: string; //       RE.version; the document id; exists → 409 conflict (details.reason "version-exists")
  reference: string; //     full docker reference, e.g. docker.io/northwind/backend:3.0.9
  /** omitted → the registry whose URL host matches the reference host (first by label; none → null = public); null = public */
  registryId?: Id | null;
  platform?: Platform; //   default linux/amd64
  mode: ImportMode;
  kind: ImageKind;
  entrypoint?: string[] | null;
  command?: string[] | null;
  workdir?: string | null;
  user?: string | null;
  ports?: ImagePort[];
  env?: ImageEnv[];
  volumes?: ImageVolumeInput[]; // kind optional: absent → inferred from the path (§21 #83)
  healthcheck?: HealthCheck | null;
  stopTimeoutSec?: number;
  description?: string;
}
export interface InspectImageRequest {
  reference: string;
  registryId?: Id | null; // same rule as RegisterImageRequest
  platform?: Platform;
}
/** Read from the registry (manifest + image config) to prefill the register form. Nothing is stored. */
export interface InspectImageResponse {
  digest: Sha256;
  platform: Platform;
  sizeBytes: number;
  layers: number;
  entrypoint: string[] | null;
  command: string[] | null;
  workdir: string | null;
  user: string | null;
  ports: ImagePort[]; //     from ExposedPorts, names "p<port>" (e.g. "p8080"), protocol from the key
  env: ImageEnv[]; //        from Env (value = default), required false, secret false
  volumes: ImageVolume[]; // from Volumes, names from the last path segment ("/data" → "data", unique, "volume-<n>" fallback); kind always 'dir'
}
/**
 * PATCH /v1/services/{serviceSlug}/images/{version} — everything except the image IDENTITY is editable (SPEC §2.2 Image,
 * §6.1 patchImage, §21 #82). Identity (never editable → invalid V_IMMUTABLE): version, serviceSlug, source, digest,
 * platform, import (mode, status, layers …), createdAt / createdBy. reference + registryId: only while
 * import.status = failed (else 409 conflict); there is no re-fetch of a ready image ("Clone as new version" instead).
 *
 * `ports` / `env` / `volumes` = the FULL new list (rows added / removed / edited). Legacy partial form (still accepted):
 * every port item only `{ name, description }`, every env item without `required` / `secret` → merged by name into the
 * existing rows (unknown name → invalid V_REF). Mixing full and partial items → invalid V_TYPE.
 *
 * Renames: a row whose name changed is listed in `renames` ({ kind, from: old name, to: new name }); a name of the old
 * list that is neither kept nor a rename source is REMOVED. A renamed / removed name still used by a template or a
 * deployment DRAFT (container of this serviceSlug + version: port rows, health check / edge proxy portName, env rows,
 * `container.env.<NAME>` / `site.<s>.port.<containerSlug>.<port>` expressions, volume rows) → 409 conflict with
 * details `ImageInUseDetails` (reason "in-use"). `renameEverywhere: true` rewrites those references of RENAMED names in
 * every TEMPLATE in the same transaction. §21 #94: deployment drafts are detached and NEVER rewritten — they keep the old
 * names (listed in the 409 details + the audit entry's deploymentsNotRewritten) until a Template sync or a manual edit;
 * heads, revisions and proposals never change (own ImageSpec copy); removals stay blocked until the containers stop using the name.
 */
export interface PatchImageRequest {
  /** reference / registryId: only while import.status is "failed" (fix a wrong source, then retry-import); else 409 conflict */
  reference?: string;
  registryId?: Id | null;
  kind?: ImageKind;
  entrypoint?: string[] | null;
  command?: string[] | null;
  workdir?: string | null;
  user?: string | null;
  ports?: ImagePort[] | { name: Slug; description: string }[];
  env?: ImageEnv[] | { name: string; value?: Value | null; options?: string[] | null; description?: string }[]; // legacy partial: value null = remove the default; options null = free text
  volumes?: ImageVolumeInput[]; // kind optional: absent → inferred from the path (§21 #83)
  healthcheck?: HealthCheck | null;
  stopTimeoutSec?: number;
  description?: string;
  /** rows of the new lists whose name changed (old → new); every other old name missing from the new list is removed */
  renames?: ImageRename[];
  /** rewrite in-use RENAMED names in templates + deployment drafts (one transaction); default false → 409 in-use */
  renameEverywhere?: boolean;
}
/** The three kinds of image names that containers reference. */
export type ImageNameKind = 'port' | 'env' | 'volume';
export interface ImageRename {
  kind: ImageNameKind;
  from: string; // the name in the stored image
  to: string; //   the name in the new list
}
/** One reference of an image name in a template / deployment draft (shared/src/image-edit.ts imageNameUses). */
export interface ImageNameUse {
  kind: 'template' | 'deployment';
  templateId?: Id; //   kind template
  deploymentId?: Id; // kind deployment
  label: string; //     template / deployment label
  source: 'template' | 'draft';
  siteSlug: Slug;
  containerSlug: Slug; // the container (of this image) whose name is referenced
  /** where: "ports[http]", "healthcheck.portName", "edge.routes[api].portName", "env[NODE_ENV]", "env[X].value",
   *  "volumes[0].mountPath", "volumes[data]", "env[URL].value (site.<s>.port.<c>.<p>)" (then containerSlug = the REFERENCING container) */
  field: string;
}
export interface ImageImpactItem {
  kind: ImageNameKind;
  name: string; //               the stored name
  change: 'renamed' | 'removed';
  to: string | null; //          the new name (renamed) or null
  usedBy: ImageNameUse[];
}
/** 409 conflict details of patchImage when renamed / removed names are in use. */
export interface ImageInUseDetails {
  reason: 'in-use';
  items: ImageImpactItem[];
}

export type NotFoundPageListItem = Omit<NotFoundPage, 'htmlCode'> & { usedBy: number; sizeBytes: number };
export interface CreateNotFoundPageRequest {
  label: string;
  htmlCode: string;
}
export type NotFoundPageDetail = NotFoundPage & { usedBy: UsageItem[] };

/** §21 #104 create from another branch (template / deployment / agent branch) */
type CreateFromFields = import('./branch.js').CreateFrom;
export interface CreateTemplateRequest extends CreateFromFields {
  label: string;
  /** §21 #106 optional: the entity slug (RE.slug, unique among templates → 409 conflict details.reason slug-taken); default uniqueSlug(slugifyLabel(label)) */
  slug?: Slug;
  icon?: Icon; // default null
  description?: string;
}
export interface PatchTemplateRequest {
  ifVersion: number;
  label?: string;
  icon?: Icon;
  description?: string;
  /** @deprecated §21 #105 — config fields answer 410 gone (commit them: POST /v1/templates/{id}/commits) */
  variables?: Variable[];
  /** @deprecated §21 #105 */
  secrets?: SecretDef[];
  /** @deprecated §21 #105 */
  files?: FileDef[];
  /** @deprecated §21 #105 */
  sites?: Site[];
}
export interface TemplateWriteResponse {
  template: Template;
  validation: ValidationReport;
}

export interface BlobRef {
  sha256: Sha256;
  sizeBytes: number;
}
export interface CreateUploadRequest {
  /** 'blob' ≤ 50 MiB (files); 'seed-archive' ≤ SEED_ARCHIVE_MAX (200 MiB, .tar.gz volume starter content; round E) */
  purpose: 'blob' | 'seed-archive';
  sizeBytes: number;
  sha256: Sha256;
}
export interface UploadPartResponse {
  received: number[];
}
export interface CompleteUploadResponse {
  upload: Upload;
  blob: BlobRef | null;
  transfer: Transfer | null;
}

export type TagListItem = Tag & { deployments: number };
export interface CreateTagRequest {
  tagSlug: Slug;
  label: string;
  icon?: Icon; // default null
  color: string;
}
export interface PatchTagRequest {
  label?: string;
  icon?: Icon;
  color?: string;
}
export interface DeleteTagResponse {
  deleted: true;
  removedFrom: { deployments: number; accessRules: number };
}

export interface CreateAccountRequest {
  name: string;
  email: string;
  phone?: string | null;
  img?: Icon;
  access?: AccessRule[]; // needs accounts.admin when non-empty
}
/**
 * §21 #84: creation sets a generated password (stored like setAccountPassword → viewable with revealAccountPassword until
 * the person changes it). value = that password, shown in RevealValue for 60 s; no set-password link any more.
 */
export interface CreateAccountResponse extends RevealResponse {
  account: Account;
}
/** POST /v1/accounts/{uid}/password ("Set new password…"): a generated strong password (20 chars, alnum) set via the Admin SDK. */
export interface SetAccountPasswordResponse extends RevealResponse {
  setAt: Ts;
  setBy: Uid;
}
/** POST /v1/accounts/{uid}/password/reveal */
export type RevealAccountPasswordResponse = SetAccountPasswordResponse;
/** POST /v1/accounts/{uid}/password-reset (new link, stored) and …/password-reset/reveal: value = the link. */
export interface ResetLinkResponse extends RevealResponse {
  /** = value (kept for older clients) */
  passwordSetLink: string;
  createdAt: Ts;
  createdBy: Uid;
  expiresAt: Ts; // createdAt + 1 h (Firebase oob code lifetime)
}
export interface PatchAccountRequest {
  name?: string;
  phone?: string | null;
  img?: Icon;
  access?: AccessRule[]; // needs accounts.admin
}
export interface DisableAccountRequest {
  disabled: boolean;
}

export interface CreateDeploymentRequest extends CreateFromFields {
  label: string;
  /** §21 #106 optional: the entity slug (unique among deployments → 409 conflict details.reason slug-taken); default from the label */
  slug?: Slug;
  /** legacy shortcut for from = { branch: template, at: head }; ignored when `from` is given */
  templateId: Id | null;
  tagSlugs: Slug[];
  managerAccountUids: Uid[];
  image?: Icon;
}
export interface CreateDeploymentResponse {
  deployment: DeploymentView;
  /** the agent GUI access key ("XXXX-XXXX-XXXX-XXXX-XXXX-XXXX"); stored (revealAccessKey shows it again any time) */
  accessKey: string;
  hideAfterSec: typeof REVEAL_SECONDS;
}
export interface PatchDeploymentRequest {
  label?: string;
  image?: Icon;
  tagSlugs?: Slug[];
  managerAccountUids?: Uid[];
}
export interface DeleteDeploymentRequest {
  confirm: string; // must equal the label
}
export type DraftSection = 'variables' | 'secrets' | 'files' | 'sites';
export interface PutDraftSectionRequest {
  ifRevision: number;
  items: Variable[] | SecretDefInput[] | FileDef[] | Site[];
}
export interface PutDraftSiteRequest {
  ifRevision: number;
  site: Site;
}
export interface DraftWriteResponse {
  draftVersion: number;
  validation: ValidationReport;
}
/** cloud revealSecret / regenerate + agent GUI revealSecret (hideAfterSec 60, §21 #84) */
export type RevealSecretResponse = RevealResponse;
/** POST /v1/deployments/{id}/secrets/{secretSlug}/regenerate — Regenerate in RevealValue (secret must have a generate rule). */
export interface RegenerateSecretRequest {
  ifRevision: number; // must equal draftVersion (409 conflict otherwise)
}
export interface RegenerateSecretResponse extends RevealResponse {
  draftVersion: number;
  rotatedAt: Ts;
}
/** devTools (§21 #97 d): path `<site>/devtools/<presetSlug>`; Template sync key `sites/<site>/devtools/<presetSlug>`. */
export type DiffSection = 'variables' | 'secrets' | 'files' | 'sites' | 'volumes' | 'containers' | 'edge' | 'devTools';
export interface DiffChange {
  section: DiffSection;
  siteSlug: Slug | null;
  /** e.g. "api-workers", "app/web/env/LOG_LEVEL", "app/edge/routes/api" */
  path: string;
  kind: 'added' | 'removed' | 'changed';
  before: unknown; // secrets: "set" | "not set"
  after: unknown; //  secrets: "changed" | "not changed" | "set" | "not set"
  /** round E: a hint the review shows next to the change (e.g. "seed changed — applies only to new / empty volumes") */
  note?: string;
  /** §21 #101 one file of a volume's content (path `<site>/volumes/<v>/content/<path>`): the blobs to fetch for a text diff */
  blob?: { volumeSlug: string; path: string; before: Sha256 | null; after: Sha256 | null };
}
export interface DiffResponse {
  head: { revId: RevId; seq: number; reportedAt: Ts } | null;
  draftVersion: number;
  changes: DiffChange[];
  publishable: boolean;
  blockers: ValidationIssue[]; //  errors that block publish
  warnings: ValidationIssue[];
  /** proposals that are not terminal (to show "pending" / "stale" banners) */
  pending: ProposalMeta[];
}
export interface PublishRequest {
  ifRevision: number;
  summary: string;
}
export interface PublishResponse {
  proposal: Proposal;
  revision: RevisionSummary;
}
export interface ResetToHeadRequest {
  confirm: true;
}

// ───────────────────────────────────────────────────────────── template sync (§21 #94, shared/src/template-sync.ts)
/**
 * Classification of one item of the 3-way Template sync (base = template@baseVersion, theirs = the template now,
 * ours = the deployment draft):
 *   theirs   — only the template changed it (clean; take it with the checkbox)
 *   ours     — only the draft changed it ("your change"; kept, shown greyed)
 *   same     — both changed it the same way (no-op)
 *   conflict — both changed it differently: take template / keep mine / (scalars) a merged value
 *   skipped  — a template change not taken in an earlier sync (unchanged since); can still be taken
 *   default  — variables only: the template changed a variable's DEFAULT value; values belong to the deployment and are
 *              never overwritten unless the user ticks "use template default"
 */
export type TemplateSyncKind = 'theirs' | 'ours' | 'same' | 'conflict' | 'skipped' | 'default';
export type TemplateSyncOp = 'added' | 'removed' | 'changed' | 'none';
export interface TemplateSyncChange {
  /** stable identity: "variables/<slug>", "variables/<slug>/value", "secrets/<slug>", "files/<slug>", "sites/<site>",
   *  "sites/<site>/settings/<field>", "sites/<site>/edge/<field>", "sites/<site>/edge/hosts/<hostname>",
   *  "sites/<site>/edge/routes/<routeId>", "sites/<site>/volumes/<volumeSlug>", "sites/<site>/devtools/<presetSlug>", "sites/<site>/containers/<slug>",
   *  "sites/<site>/containers/<slug>/<field>", "sites/<site>/containers/<slug>/env/<NAME>" */
  key: string;
  section: DiffSection;
  siteSlug: Slug | null;
  /** human path, e.g. "app / api / env PORT" */
  label: string;
  kind: TemplateSyncKind;
  theirsOp: TemplateSyncOp; // what the template did since the base
  oursOp: TemplateSyncOp; //   what the draft did since the base
  /** values (null when absent — see present); secrets: the definition only (never a value); variables: the definition without value */
  base: unknown;
  theirs: unknown;
  ours: unknown;
  present: { base: boolean; theirs: boolean; ours: boolean };
  /** both sides are scalars (string / number / boolean / null): a conflict may be resolved with a merged value */
  scalar: boolean;
  theirsHash: string;
  /** set when kind = skipped */
  skippedAt?: Ts;
}
export interface TemplateSyncCounts {
  theirs: number;
  ours: number;
  same: number;
  conflict: number;
  skipped: number;
  default: number;
  /** template changes since the last sync = theirs + conflict + same + default */
  templateChanges: number;
}
export interface TemplateSyncStatus {
  linked: boolean;
  link: SrcTemplateView | null;
  templateLabel: string | null;
  /** null = not linked or the template was deleted */
  currentVersion: number | null;
  templateDeleted: boolean;
  /** currentVersion − baseVersion (0 when not linked / deleted) */
  newerVersions: number;
  /** actionable template changes since the last sync = counts theirs + conflict + default (0 when not linked / deleted) */
  pendingChanges: number;
  conflicts: number;
}
export interface TemplateSyncPlan {
  link: SrcTemplateView | null;
  template: { templateId: Id; label: string; icon: Icon; version: number; updatedAt: Ts; updatedBy: Uid };
  /** true when the template differs from the linked one ("Change template…" / "Link a template…") */
  relink: boolean;
  /** the base: template@baseVersion of the linked template; null = no base (link: everything is an addition) */
  baseVersion: number | null;
  draftVersion: number;
  base: ConfigView;
  theirs: ConfigView;
  ours: ConfigView;
  changes: TemplateSyncChange[];
  counts: TemplateSyncCounts;
}
export type TemplateSyncTake = 'theirs' | 'ours' | 'custom';
export interface TemplateSyncChoice {
  key: string;
  take: TemplateSyncTake;
  /** take = custom (scalar items only): the merged value */
  value?: unknown;
}
export interface TemplateSyncApplyRequest {
  ifRevision: number; //       the draftVersion that was reviewed
  templateId: Id; //           the template reviewed (≠ the linked one = relink)
  templateVersion: number; //  the template version reviewed (changed meanwhile → 409 conflict)
  choices: TemplateSyncChoice[]; // missing = not taken (conflicts MUST have a choice)
  /** variable values entered in the review (e.g. required variables the template added) */
  values?: Record<Slug, VariableValue>;
}
export interface TemplateSyncMissing {
  variables: { variableSlug: Slug; label: string; type: VariableType; options: string[] | null; required: boolean }[];
  secrets: { secretSlug: Slug; label: string }[];
}
export interface TemplateSyncPreviewResponse {
  /** the draft as it would be after Apply */
  draft: ConfigView;
  /** draft before → after */
  changes: DiffChange[];
  validation: { ok: boolean; issues: ValidationIssue[] };
  unresolved: string[]; // keys of conflicts without a choice
  taken: number;
  skipped: number;
  missing: TemplateSyncMissing;
}
export interface TemplateSyncApplyResponse {
  draftVersion: number;
  validation: { ok: true; issues: ValidationIssue[] };
  link: SrcTemplateView;
  taken: number;
  skipped: number;
  missing: TemplateSyncMissing;
}
export interface UnlinkTemplateRequest {
  confirm: true;
}

export interface InstallTokenView {
  tokenHash: string;
  siteSlug: Slug;
  /** §21 #92 ('install' for docs without kind) */
  kind: InstallTokenKind;
  /** null = no expiry (maintenance) */
  expiresAt: Ts | null;
  usedAt: Ts | null;
  revokedAt: Ts | null;
  createdAt: Ts;
  createdBy: Uid;
  /** maintenance tokens are 'active' until revoked (they never expire and are never "used") */
  state: 'active' | 'used' | 'expired' | 'revoked';
  /** §21 #84: true = active AND the token is stored → "View" (revealInstallCommand); false on active pre-#84 tokens
   *  ("not stored — regenerate to view") and on used / expired / revoked ones (their state is shown, not the value).
   *  Always false for maintenance tokens (they are viewed through the site's upgrade command). */
  stored: boolean;
  /** §21 #92: this token is the enrolled site's current upgrade command (works for the upgrade-only script now) */
  upgradeCommand: boolean;
}
/** §21 #92 the enrolled site's "Upgrade / reinstall command" (Setup site card). The token is the enrollment's own install
 *  token (kind install, used for the current enrollment) or the newest unrevoked maintenance token bound to it. */
export interface SiteUpgradeCommandView {
  /** null = no usable token for the current enrollment (e.g. its token doc is gone) → Regenerate */
  tokenHash: string | null;
  kind: InstallTokenKind | null;
  /** the plaintext is stored → View (revealUpgradeCommand); false → "not stored — Regenerate" */
  stored: boolean;
  createdAt: Ts | null;
  createdBy: Uid | null;
}
/** §21 #92 revealUpgradeCommand / regenerateUpgradeCommand; value = command (the same `curl … | sudo sh` form as the
 *  install command; on the server it runs the upgrade-only flow of §19.1 / §21 #91) */
export interface UpgradeCommandResponse extends RevealResponse {
  command: string;
  token: string;
  tokenHash: string;
  siteSlug: Slug;
  kind: InstallTokenKind;
  /** agent.enrolledAt of the enrollment the command belongs to */
  enrolledAt: Ts;
}
/** §21 #92 skew of an agent's clock: |agent − server| above this many seconds → warning on Setup / Status */
export const CLOCK_SKEW_WARN_SEC = 120;
/** §21 #92 "5 h 30 min" / "3 min" / "2 d 4 h" for a number of seconds (≥ 0) */
export function formatSkewDuration(sec: number): string {
  const s = Math.round(Math.abs(sec));
  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.round(s / 60)} min`;
  if (s < 86400) {
    const h = Math.floor(s / 3600);
    const m = Math.round((s % 3600) / 60);
    return m === 0 ? `${h} h` : m === 60 ? `${h + 1} h` : `${h} h ${m} min`;
  }
  const d = Math.floor(s / 86400);
  const h = Math.round((s % 86400) / 3600);
  return h === 0 ? `${d} d` : `${d} d ${h} h`;
}
/** §21 #92 the warning text for an agent clock skew (agent − server, seconds); null when |skew| ≤ CLOCK_SKEW_WARN_SEC */
export function clockSkewWarning(skewSec: number | null | undefined): string | null {
  if (typeof skewSec !== 'number' || !Number.isFinite(skewSec) || Math.abs(skewSec) <= CLOCK_SKEW_WARN_SEC) return null;
  return `agent clock is ${formatSkewDuration(skewSec)} ${skewSec > 0 ? 'ahead' : 'behind'} — fix NTP on the server (e.g. sudo timedatectl set-ntp true)`;
}
/** §21 #92 state of an install token: maintenance tokens are active until revoked (never used / expired) */
export function installTokenState(t: Pick<InstallToken, 'kind' | 'usedAt' | 'revokedAt' | 'expiresAt'>, nowMs: number = Date.now()): InstallTokenView['state'] {
  // revoked wins: a used install token can be revoked later (Regenerate of the site's upgrade command, §21 #92)
  if (t.revokedAt) return 'revoked';
  if (t.kind === 'maintenance') return 'active';
  if (t.usedAt) return 'used';
  if (t.expiresAt && Date.parse(t.expiresAt) <= nowMs) return 'expired';
  return 'active';
}
/** §21 #92 agent clock vs the cloud (from the last heartbeat that carried `agentTime`) */
export interface AgentClockSkew {
  /** agent clock − server receive time, whole seconds (positive = the agent clock is ahead) */
  skewSec: number;
  /** server time of the heartbeat that measured it */
  measuredAt: Ts;
}
/** Storage deps/manifest.json — pinned install dependencies (written by backend/scripts/upload-deps.ts). The install script
 *  never installs "latest": versions + sha256 are embedded at script generation. */
export interface DepsFile {
  path: string; //   Storage path, e.g. deps/docker/27.5.1/amd64/docker-27.5.1.tgz
  sha256: string; // hex
  size: number;
}
export interface DepsManifest {
  docker: { version: string; files: { amd64: DepsFile; arm64: DepsFile } };
  /** minimum Docker Engine API version the agent accepts (an existing Docker below it is never touched → script fails) */
  minDockerApi: string;
  /** notes (§21 #90, optional): release notes shown on the agent GUI Agent page + the cloud Setup page;
   *  schema / minSchema (§21 #91) from agent/<v>/release.json */
  agent: { version: string; notes?: string | null; schema?: number | null; minSchema?: number | null } | null;
  updatedAt: Ts;
}
export interface SetupResponse {
  /** stored = the plaintext is kept (§21 #84) → View; false = pre-#84 deployment → "not stored — regenerate to view" */
  accessKey: { version: number; rotatedAt: Ts; rotatedBy: Uid; stored: boolean };
  apiBase: string;
  /** what the install script installs when missing (deps/manifest.json); null = no manifest uploaded yet */
  installDeps: { dockerVersion: string; minDockerApi: string; agentVersion: string | null } | null;
  /** §21 #90 the pinned agent release (= what "Upgrade agent" installs); null = none pinned */
  agentRelease: AgentReleaseInfo | null;
  /** §21 #90 the current / last cloud "Upgrade all" run; null = never */
  agentUpgradeRun: AgentUpgradeRun | null;
  /** §21 #92 the cloud's clock when it answered (the console corrects relative times for a wrong browser clock) */
  serverTime: Ts;
  sites: {
    siteSlug: Slug;
    label: string;
    inDraft: boolean;
    inHead: boolean;
    agent: (Pick<AgentDoc, 'agentVersion' | 'enrolledAt' | 'lastSeenAt' | 'online' | 'hostname' | 'ip' | 'keyVersion' | 'os' | 'arch' | 'advertiseHost' | 'detectedAddresses'> & { clockSkew: AgentClockSkew | null }) | null;
    /** §21 #92 the "Upgrade / reinstall command" of an enrolled site; null when not enrolled */
    upgradeCommand: SiteUpgradeCommandView | null;
    /** §21 #86: agent GUI URLs from the last heartbeat (detectedAddresses × guiPorts; port 80 / the edge first); [] when
     *  not enrolled or for agents < 0.8 (then the frontend falls back to :8080 / :7080) */
    guiUrls: { url: string; kind: GuiPort['kind'] }[];
    /** §21 #86: true = the site runs the built-in default edge (no applied configuration yet); null = unknown / old agent */
    defaultEdge: boolean | null;
    /** §21 #86: edge ports the agent cannot bind (from heartbeat edge.bindErrors, purpose edge-http / edge-https), e.g.
     *  message "port 80 is used by nginx — stop it or change edge.httpPort" */
    portAlerts: { port: number; purpose: 'edge-http' | 'edge-https'; owner: string | null; message: string }[];
    /** §21 #90 null when not enrolled */
    upgrade: SiteAgentUpgradeView | null;
    tokens: InstallTokenView[];
  }[];
}
/** §21 #90 per-site agent upgrade state for the Setup / Status pages */
export interface SiteAgentUpgradeView {
  installed: string;
  latest: string | null;
  /** latest is newer than installed */
  available: boolean;
  /** installed ≥ GUI_UPGRADE_MIN_VERSION (else: rerun the install command once — `hint`) */
  supported: boolean;
  /** "Rerun the install command of this site to reach agent ≥ 0.10.0; later upgrades are one click here." when !supported */
  hint: string | null;
  request: AgentUpgradeRequest | null;
  /** the last progress the agent reported (heartbeat) */
  progress: AgentUpgradeProgress | null;
}
/** rotate (= Regenerate, typed ACCEPT in the GUI) and revealAccessKey; value = accessKey */
export interface RotateAccessKeyResponse extends RevealResponse {
  accessKey: string;
  version: number;
  rotatedAt: Ts;
}
export type RevealAccessKeyResponse = RotateAccessKeyResponse;
export interface CreateInstallTokenRequest {
  siteSlug: Slug;
  /** §21 #84 Regenerate: revoke this site's active tokens (install-token.revoked each) in the same transaction */
  replaceActive?: boolean;
}
/** create / Regenerate and revealInstallCommand; value = command */
export interface CreateInstallTokenResponse extends RevealResponse {
  /** the full shell command (token inside, header form) */
  command: string;
  /** "opi_" + base64url(32 random bytes) — stored while usable (revealInstallCommand; after enrolling: revealUpgradeCommand) */
  token: string;
  tokenHash: string;
  siteSlug: Slug;
  expiresAt: Ts;
}
export type RevealInstallCommandResponse = CreateInstallTokenResponse;
export interface RevokeAgentRequest {
  confirm: Slug; // must equal the siteSlug
}

export interface SiteCloudStatus {
  siteSlug: Slug;
  label: string;
  inHead: boolean;
  enrolled: boolean;
  online: boolean;
  lastSeenAt: Ts | null;
  relayVia: Slug | null;
  agentVersion: string | null;
  headRevId: RevId | null;
  headSeq: number | null;
  staging: HeartbeatStaging | null;
  keyVersion: number | null;
  metrics: HostMetricsSummary | null;
  edge: EdgeReport | null;
  /** auto host of the site (null until enrolled) */
  advertiseHost: string | null;
  detectedAddresses: string[];
  /** §21 #90 null when not enrolled */
  upgrade: SiteAgentUpgradeView | null;
  /** §21 #92 agent clock skew (null = not measured / not enrolled) */
  clockSkew: AgentClockSkew | null;
}
export interface StatusResponse {
  serverTime: Ts;
  sites: SiteCloudStatus[];
  uptime: { siteSlug: Slug; days: { date: string; onlineFraction: number; hours: number[] }[] }[]; // 30 days, oldest first
}
export interface ActivitiesQuery {
  site?: string;
  origin?: ActivityOrigin;
  type?: string; //   exact type or a prefix ending in "." (e.g. "update.")
  actor?: string;
  from?: Ts;
  to?: Ts;
  cursor?: string;
  limit?: number; //  default 100, max 500
  via?: 'pipe' | 'manual';
}
export interface UploadActivitiesResponse {
  inserted: number;
  skipped: number;
  rejected: number;
  errors: { activityId: string; reason: string }[];
  sites: { siteSlug: Slug; fromSeq: number; toSeq: number }[];
  signatureOk: boolean;
}
export interface CreateTransferRequest {
  fileName: string;
  sizeBytes: number;
  sha256: Sha256;
  note?: string;
}
export interface CreateTransferResponse {
  transfer: Transfer;
  upload: Upload;
}

// ───────────────────────────────────────────────────────────── install + agent ↔ cloud
export interface EnrollRequest {
  publicKey: string; //   Ed25519 raw public key, base64
  agentVersion: string;
  hostname: string;
  ip: string;
  advertiseHost: string;
  agentPort: number;
  os: string;
  arch: string;
  /** non-loopback IPs (IPv4 first) + hostname; optional for old agents */
  detectedAddresses?: string[];
}
export interface EnrollResponse {
  deploymentId: Id;
  siteSlug: Slug;
  peers: PeerInfo[];
  accessKey: { hash: string; version: number };
  serverTime: Ts;
  /** same as CloudView.consoleUrl / deploymentLabel (optional; new installs know them before the first poll) */
  consoleUrl?: string | null;
  deploymentLabel?: string | null;
}
export interface PollQuery {
  cursor?: string;
  wait?: number; // seconds to hold the request when nothing changed: default 25, max 50, 0 = answer at once
}
export type PollResponse = CloudView;
export interface HeartbeatRequest {
  siteSlug: Slug;
  agentVersion: string;
  headRevId: RevId | null;
  staging: HeartbeatStaging | null;
  peersSeen: { siteSlug: Slug; lastSeenAt: Ts }[];
  metrics: HostMetricsSummary;
  edge: EdgeReport | null;
  keyVersion: number;
  advertiseHost: string;
  agentPort: number;
  ip: string;
  hostname: string;
  /** non-loopback IPs (IPv4 first) + hostname; optional for old agents */
  detectedAddresses?: string[];
  /** §21 #86 (optional for old agents) */
  guiPorts?: GuiPort[];
  /** §21 #90 current / last upgrade attempt (sent at once on every phase change, then with every heartbeat until 24 h after it finished) */
  upgrade?: AgentUpgradeProgress | null;
  /** §21 #92 the agent's own clock when it sent this heartbeat (raw, NOT corrected by the X-OPM-Server-Time offset);
   *  the cloud stores clockSkew = agentTime − receive time. Optional (agents before 0.11 omit it). */
  agentTime?: Ts;
  /** §21 #101 sync state of every persist-to-cloud volume of this site (agents ≥ 0.12; absent on older agents) */
  volumeSync?: import('./volume-content.js').VolumeSyncReport[];
  /** §21 #104 the agent branch of this site (agents ≥ 0.14) */
  branch?: import('./branch.js').AgentBranchReport | null;
}
export interface HeartbeatResponse {
  ok: true;
  serverTime: Ts;
}
/** §21 #86 `opm-agent upgrade` (enrolled site, signed by the site key): the pinned agent release for this os/arch =
 *  deps/manifest.json agent.version, else OPM_AGENT_VERSION / agent/latest (the same pin the install script embeds). */
export interface AgentReleaseResponse {
  version: string;
  os: 'linux' | 'darwin';
  arch: 'amd64' | 'arm64';
  sha256: string; //           hex of the binary
  signature: string; //        Ed25519 (release key) of the sha256 hex, base64url
  releasePublicKey: string; // base64; agent.json releasePublicKey wins when set (must match)
  /** GET path of the binary (signed agent request): /v1/agent/binary?os=&arch=&version= */
  binaryPath: string;
  /** §21 #90 release notes (deps/manifest.json agent.notes); null / absent = none */
  notes?: string | null;
  /** §21 #90 binary size in bytes (download progress); absent on old clouds */
  sizeBytes?: number | null;
  /** §21 #91 SQLite schema of this build (opm.db PRAGMA user_version it migrates to) and the oldest DB schema it opens;
   *  null = unknown (builds before 0.10.0 = AGENT_LEGACY_SCHEMA) */
  schema?: number | null;
  minSchema?: number | null;
}
/** §21 #91 build metadata (`opm-agent version --json`; make dist writes dist/release.json; upload-agent.ts stores
 *  agent/<v>/release.json; served as AgentReleaseResponse.schema/minSchema + headers X-OPM-Agent-Schema / -Min-Schema). */
export interface AgentBuildMeta {
  version: string;
  schema: number;
  minSchema: number;
  commit: string | null;
  builtAt: string | null;
  os?: string;
  arch?: string;
}
/** §21 #91 agents before 0.10.0 have no schema metadata and no user_version: they count as schema 1. */
export const AGENT_LEGACY_SCHEMA = 1;
/** §21 #91 install-script decision for an installed agent vs the target (also used by tests + the cloud). */
export type AgentInstallAction = 'fresh' | 'upgrade' | 'downgrade' | 'same';
export function agentInstallAction(installed: string | null, target: string): AgentInstallAction {
  if (!installed) return 'fresh';
  const c = compareAgentVersions(installed, target);
  return c < 0 ? 'upgrade' : c > 0 ? 'downgrade' : 'same';
}
/** §21 #91 a target build can open the installed DB when dbSchema ≤ target.schema (and ≥ target.minSchema). Unknown
 *  numbers count as AGENT_LEGACY_SCHEMA. false → the downgrade is refused unless a DB backup with a compatible schema is restored. */
export function agentSchemaCompatible(dbSchema: number | null | undefined, target: { schema?: number | null; minSchema?: number | null }): boolean {
  const db = dbSchema ?? AGENT_LEGACY_SCHEMA;
  const max = target.schema ?? AGENT_LEGACY_SCHEMA;
  const min = target.minSchema ?? 0;
  return db <= max && db >= min;
}
/** §21 #90 cloud console "Upgrade agent" (one site) */
export interface RequestAgentUpgradeRequest {
  /** must equal the pinned version (else 409 stale: the pin moved, reload) */
  version: string;
  /** typed siteSlug */
  confirm: Slug;
}
export interface RequestAgentUpgradeResponse {
  request: AgentUpgradeRequest;
}
/** §21 #90 cloud console "Upgrade all" (sequential, stops on the first failure) */
export interface UpgradeAllAgentsRequest {
  version: string;
  /** typed "UPGRADE" */
  confirm: 'UPGRADE';
}
export interface AgentUpgradeRunResponse {
  run: AgentUpgradeRun;
}
export interface ReportHeadRequest {
  revision: Revision;
}
export interface ReportHeadResponse {
  stored: boolean; //      false = the revision was already known
  headChanged: boolean;
}
export interface AgentProposalResponse {
  proposal: Proposal;
  revision: Revision;
}
export interface RegisterLocalProposalRequest {
  proposal: Proposal;
  revision: Revision;
}
export interface ProposalStatusReport {
  siteSlug: Slug; //       the reporting site
  staging?: StagingState;
  progress?: number;
  error?: string | null;
  /** coordinator only */
  status?: 'approved' | 'applying' | 'applied' | 'failed-rolled-back' | 'stale';
  approvedBy?: string;
  approvedAt?: Ts;
  updateId?: Ulid;
  result?: { ok: boolean; message: string; snapshotId: Ulid | null };
}
export interface ProposalStatusResponse {
  ok: true;
  proposal: Proposal;
}
export interface ImageManifestResponse {
  serviceSlug: Slug;
  version: string;
  digest: Sha256;
  platform: Platform;
  mediaType: string;
  config: { digest: Sha256; sizeBytes: number };
  layers: ImageLayer[];
}
export interface AgentActivitiesRequest {
  siteSlug: Slug;
  entries: Activity[]; //  ≤ 500, ascending seq, contiguous
}
export interface AgentActivitiesResponse {
  ackSeq: number;
  inserted: number;
  skipped: number;
  rejected: number;
  errors: { activityId: string; reason: string }[];
}
export interface AgentCreateTransferRequest {
  fileName: string;
  sizeBytes: number;
  sha256: Sha256;
  note?: string;
  actor: string;
}
export interface AgentTransferStatusRequest {
  /** §21 #100: `fetched` = a to-site file is stored on the site (sets Transfer.fetchedAt; no status change) */
  status: 'ready' | 'saved-on-server' | 'downloaded' | 'fetched';
  savedPath?: string;
  actor: string;
}
export interface RelayRequest {
  forSite: Slug;
  request: { method: string; path: string; headers: Record<string, string>; bodyB64: string | null };
}
export interface RelayResponse {
  status: number;
  headers: Record<string, string>;
  bodyB64: string | null;
}

// ═════════════════════════════════════════════════════════════ API DTOs — agent local API (/_opm/api/*)
export interface AgentInfoResponse {
  deploymentId: Id;
  siteSlug: Slug;
  agentVersion: string;
  enrolled: boolean;
  hasAccessKey: boolean;
  keyVersion: number;
  serverTime: Ts;
  surface: 'edge' | 'gui-alias' | 'agent-port';
  /** cloud console origin (cloud view / enroll answer, else agent.json consoleUrl); null when unknown */
  consoleUrl?: string | null;
  /** agent.json apiBase */
  apiBase?: string | null;
  /** Deployment.label from the cloud view; null when unknown */
  deploymentLabel?: string | null;
  /** §21 #91 SQLite schema of this build (absent before 0.10.0) */
  schema?: number;
}
/** §21 #91 GET /_opm/api/health (public, cheap): the install script + the upgrade engine wait for ok after a restart. */
export interface AgentHealthResponse {
  ok: boolean; //          store open + listeners bound + startup finished
  version: string;
  schema: number;
  pid: number;
  startedAt: Ts;
  /** reached the cloud since start (direct or relay or tunnel) */
  cloud: { connected: boolean; route: 'direct' | 'relay' | 'tunnel' | 'none'; lastContactAt: Ts | null };
  docker: { ok: boolean };
  /** inherited systemd sockets in use (socket activation) */
  socketActivated: boolean;
  checks: { name: string; ok: boolean; message: string | null }[];
}
export interface LoginRequest {
  accessKey: string; // 24 base32 chars; dashes, spaces and case are ignored
}
export interface LoginResponse {
  sessionId: Ulid;
  expiresAt: Ts;
  keyVersion: number;
}
export interface SessionResponse {
  sessionId: Ulid;
  expiresAt: Ts;
  keyVersion: number;
}
export interface ActorRequest {
  email: string;
}
export interface ActorResponse {
  actorToken: string; // JWT (EdDSA) with ActorTokenClaims
  expiresAt: Ts;
  email: string;
}
export interface RecentActorsResponse {
  actors: ActorRow[];
}
export interface SiteTile {
  siteSlug: Slug;
  label: string | null; //     from the head config (null when the site is not in it)
  self: boolean;
  enrolled: boolean;
  reachable: boolean;
  lastSeenAt: Ts | null;
  host: string | null; //      resolved SiteConfig.host
  advertiseHost: string | null;
  agentPort: number | null;
  agentVersion: string | null;
  head: { revId: RevId; seq: number } | null;
  staging: { revId: RevId; staging: SiteStaging } | null;
  cloudRoute: PeerPing['cloudRoute'] | null;
  containers: { total: number; ready: number };
  inConfig: boolean;
  lock: UpdateLock | null;
}
export interface OverviewResponse {
  deploymentId: Id;
  self: Slug;
  serverTime: Ts;
  head: { revId: RevId; seq: number; hash: Sha256; summary: string; createdAt: Ts } | null;
  sites: SiteTile[];
  /** links[a][b] = rtt ms when a reached b in the last 30 s, null = no link, "self" on the diagonal */
  links: Record<Slug, Record<Slug, number | null | 'self'>>;
  cloud: { route: PeerPing['cloudRoute']; lastOkAt: Ts | null; fetchedAt: Ts | null; source: CloudView['source'] | null };
  lock: UpdateLock | null;
  deleted: boolean;
}
export interface AgentConfigResponse {
  head: { revId: RevId; seq: number; hash: Sha256 } | null;
  config: ConfigView; //       empty config when there is no head
  catalog: RevisionCatalog | null;
  /** §21 #104 agents ≥ 0.14: the working copy = head ⊕ GUI edits (masked); absent = no edits / old agent */
  working?: ConfigView | null;
  /** §21 #113: every image a local edit can use (head + recent cloud commits + drift-adopted + site images ⊇ catalog) */
  knownCatalog?: RevisionCatalog;
}
/** §21 #113 POST /_opm/api/images/resolve — a docker reference (pulled when missing) or an image on the host. */
export interface SiteImageResolveRequest {
  reference?: string;
  imageId?: string;
  /** pull even when the host has the reference (a moved tag) */
  pull?: boolean;
  /** registry credentials for this pull only (never stored) */
  username?: string;
  password?: string;
}
export interface SiteImageResolveResponse {
  key: string; // <serviceSlug>:<version> (version local-<12 hex>)
  image: ImageSpec;
  service: { serviceSlug: Slug; label: string; icon: string | null };
  pulled: boolean;
}
/** §21 #113 GET /_opm/api/images */
export interface SiteImagesResponse {
  catalog: RevisionCatalog;
  host: { id: string; repoTags: string[]; sizeBytes: number; created: number }[];
}
export interface LocalProposalRequest {
  baseRevId: RevId; //         must be the current head
  summary: string; //          1–200
  config: ConfigInput; //      full config; secrets: plainValue semantics (absent = keep)
}
export interface LocalProposalPreviewResponse {
  changes: DiffChange[];
  validation: ValidationReport; // includes local-edit violations (V_LOCAL_*)
}
export interface LocalProposalResponse {
  proposal: Proposal;
  changes: DiffChange[];
}
export interface StagingDetail {
  siteSlug: Slug;
  staging: SiteStaging;
  layers: { total: number; have: number };
  files: { total: number; have: number };
  blobs: { sha256: Sha256; kind: BlobKind; sizeBytes: number; source: 'cloud' | `peer:${string}` | 'local' | null; state: 'have' | 'downloading' | 'waiting' | 'failed' }[];
  issues: ValidationIssue[];
}
export interface ProposalListItem {
  proposal: Proposal;
  isHeadParent: boolean;
}
export interface ProposalDetailResponse {
  proposal: Proposal;
  revision: RevisionView;
  changes: DiffChange[]; //   head → proposal
  staging: StagingDetail[]; // one per site of the proposal (peers over the mesh; stale ones marked)
  gate: GateCheck;
}
export interface ApproveStartResponse {
  intent: ApproveIntent;
  gate: GateCheck;
}
export interface ApproveRequest {
  intentId: Ulid;
  typed: string; // must be exactly "ACCEPT"
  /** §21 #104 branch mode: the commit message of a snapshot rollback (required there, 422 V_COMMIT_MESSAGE) */
  message?: string;
}
export interface ApproveResponse {
  updateId: Ulid;
  run: UpdateRun;
}
export interface CreateSnapshotRequest {
  note?: string;
}
export interface CreateSnapshotResponse {
  snapshotId: Ulid;
  /** the snapshot is taken on every reachable site of the head config; poll GET /_opm/api/snapshots/{id} */
  sites: Slug[];
}
export interface SnapshotDetailResponse {
  summary: SnapshotSummary;
  snapshot: Snapshot; // config secrets masked
}
export interface RollbackPlanResponse {
  snapshot: SnapshotSummary;
  images: { siteSlug: Slug; containerSlug: Slug; serviceSlug: Slug; from: string | null; to: string | null }[];
  changes: DiffChange[];
  /** volumes in the snapshot (restored by volumeSlug, wherever they are mounted now) */
  volumesRestored: { siteSlug: Slug; volumeSlug: string; sizeBytes: number }[];
  /** declared volumes of the current head that are not in the snapshot (untouched: backup off, new since, or no data then) */
  volumesKept: { siteSlug: Slug; volumeSlug: string; backup: boolean }[];
  dataLossSince: Ts;
  newSeq: number;
  gate: GateCheck;
}
/** One volume of a site on the agent Backups page (GET /_opm/api/sites/{siteSlug}/volumes). */
export interface SiteVolumeStatus {
  volumeSlug: string;
  label: string;
  description: string;
  backup: boolean; //    false for orphans
  /** false = a directory under <agentHome>/volumes/ that the head config no longer declares (kept on disk, never deleted automatically, never backed up) */
  declared: boolean;
  /** the absolute directory (file volumes: file) on this server (location resolved against agentHome) */
  path: string;
  location: StorageLocation; // as configured (orphans: auto)
  /** round E: as configured (orphans: dir) */
  kind: SiteVolumeKind;
  seed: VolumeSeed | null;
  /** when the starter content was applied on this site (null = never: started empty or created before round E) */
  seeded: { at: Ts; source: VolumeSeed['kind']; entries: number } | null;
  usedBy: { containerSlug: Slug; imageVolume: Slug | null; mountPath: string; readOnly: boolean }[];
  /** bytes on disk of the volume directory (apparent size, measured at most every 60 s); null = no directory yet */
  sizeBytes: number | null;
  /** newest snapshot of this site that contains the volume */
  lastSnapshot: { snapshotId: Ulid; createdAt: Ts; sizeBytes: number } | null;
}
export interface SiteVolumesResponse {
  siteSlug: Slug;
  /** the agent data dir of this site ("./" locations are relative to it) */
  agentHome: string;
  measuredAt: Ts;
  volumes: SiteVolumeStatus[];
  /** host-path mounts of the head config: NEVER backed up (the GUI says so) */
  hostPaths: { containerSlug: Slug; mountPath: string; path: string; readOnly: boolean }[];
}
/** POST /_opm/api/sites/{siteSlug}/volumes/{volumeSlug}/reset (round E): typed ACCEPT after the 60 s wait. */
export interface ResetVolumeResponse {
  siteSlug: Slug;
  volumeSlug: string;
  /** the manual snapshot of this volume taken first (note "before reset of <volumeSlug>") */
  snapshotId: Ulid;
  /** entries written (files seed: entries; file seed: 1; archive: regular files) */
  entries: number;
}
// ───────────────────────────────────────────────────────────── §21 #96 volume manager (agent GUI Tools → Volumes)
/**
 * Long volume actions run as an operation on the site that holds the volume (one at a time per volume; refused while an
 * update runs and vice versa). The GUI polls `getVolumeOp`. Containers that mount the volume and run are stopped in
 * REVERSE start order and started again in start order (§15.5) — also when the action fails.
 */
export type VolumeOpKind = 'empty' | 'copy' | 'restore';
export type VolumeOpStepName = 'stop' | 'snapshot' | 'copy' | 'empty' | 'restore' | 'start';
export interface VolumeOpStep {
  name: VolumeOpStepName;
  state: 'pending' | 'running' | 'done' | 'skipped' | 'failed';
  detail: string | null;
  startedAt: Ts | null;
  finishedAt: Ts | null;
}
export interface VolumeOpTarget {
  kind: 'path' | 'volume';
  /** absolute path on the server (the volume directory / file for kind volume) */
  path: string;
  volumeSlug: string | null;
}
export interface VolumeOpSource {
  kind: 'copy' | 'upload';
  /** copyId | uploadId */
  ref: string;
  path: string;
}
export interface VolumeOp {
  opId: Ulid;
  siteSlug: Slug;
  volumeSlug: string;
  volumeKind: 'dir' | 'file';
  kind: VolumeOpKind;
  actor: string;
  startedAt: Ts;
  finishedAt: Ts | null;
  /** the running step, or done | failed */
  phase: VolumeOpStepName | 'queued' | 'done' | 'failed';
  ok: boolean | null;
  error: string | null;
  steps: VolumeOpStep[];
  progress: { bytesDone: number; bytesTotal: number | null; files: number };
  /** containers stopped for the action (started again at the end) */
  stopped: string[];
  target: VolumeOpTarget | null; //  copy
  source: VolumeOpSource | null; //  restore
  snapshotId: Ulid | null; //        empty (optional) / restore (always)
  copyId: string | null; //          copy → the entry in the copies list
  resultBytes: number | null;
  durationMs: number | null;
  /** warnings (ownership could not be kept, xattrs unsupported, …) */
  warnings: string[];
  fastForward?: FastForwardUse | null;
}
export interface VolumeEmptyRequest {
  intentId: Ulid;
  /** the volume slug, typed */
  typed: string;
  /** take a snapshot of this volume first (default true in the GUI) */
  snapshot: boolean;
}
export type VolumeCopyTargetInput =
  | { kind: 'path'; /** absolute, or "./…" relative to agentHome; empty/absent = <agentHome>/volume-copies/<volume>-<yyyymmdd-hhmmss> */ path?: string | null }
  | { kind: 'volume'; volumeSlug: string; /** required when the target volume is not empty: its slug, typed */ overwrite?: string | null };
export interface VolumeCopyRequest {
  target: VolumeCopyTargetInput;
  /** stop the containers that mount the SOURCE (and a target volume) during the copy (GUI default: on for databases) */
  stopContainers: boolean;
  /** keep owner / group / mode / mtime / xattrs (default true) */
  preserve: boolean;
}
export interface VolumeRestoreRequest {
  intentId: Ulid;
  typed: string; //                 the volume slug
  source: { kind: 'copy'; copyId: string } | { kind: 'upload'; uploadId: string };
}
export interface VolumeRestoreUpload {
  uploadId: string;
  sizeBytes: number;
  /** regular files + dirs + symlinks in the archive (checked: no absolute paths, no "..", no devices) */
  entries: number;
  expiresAt: Ts; //                 1 h
}
export interface VolumeSnapshotRequest {
  note?: string;
}
export interface VolumeSnapshotResponse {
  snapshotId: Ulid;
  sizeBytes: number;
}
/** A copy made by "Copy to path" (listed until deleted; the data itself lives at `path`). */
export interface VolumeCopy {
  copyId: string;
  siteSlug: Slug;
  volumeSlug: string;
  volumeKind: 'dir' | 'file';
  path: string;
  /** inside <agentHome>/volume-copies/ (else a custom path the operator chose) */
  inCopiesDir: boolean;
  exists: boolean;
  sizeBytes: number | null;
  files: number | null;
  createdAt: Ts;
  createdBy: string;
  opId: Ulid | null;
}
export interface DeleteVolumeCopyRequest {
  /** the copyId, typed */
  confirm: string;
}
export const VOLUME_COPIES_DIR = 'volume-copies';
/** at most this many volume actions (empty / copy / restore / snapshot / restore upload) per session per minute */
export const VOLUME_OP_RATE_PER_MINUTE = 6;
/** restore upload (.tar.gz) limit — bigger data: copy it to the server and restore from a copy path */
export const VOLUME_RESTORE_UPLOAD_MAX = 2 * 1024 * 1024 * 1024;

export interface PatchSnapshotRequest {
  note: string;
}
export interface ContainerLogsResponse {
  siteSlug: Slug;
  containerSlug: Slug;
  serviceSlug: Slug;
  lines: { ts: Ts; stream: 'stdout' | 'stderr'; text: string }[];
}
export interface DownloadActivitiesQuery {
  from?: number; // seq (inclusive), default 1
  to?: number; //   seq (inclusive), default last
  includeDebug?: boolean;
}
export interface SaveTransferRequest {
  path: string; // absolute path on the server; must not exist
}
export interface SendFromPathRequest {
  path: string; // absolute path of a regular file on the server (§21 #100 safety policy: hostFsBlockReason)
  note?: string;
}
/** §21 #100: one entry of the server filesystem picker (GET …/transfers/fs) */
export interface HostFsEntry {
  name: string;
  path: string; //                 absolute
  type: 'dir' | 'file' | 'symlink' | 'other';
  /** symlinks: what the target is (`missing` = dangling); null for other types */
  targetType: 'dir' | 'file' | 'other' | 'missing' | null;
  linkTarget: string | null;
  sizeBytes: number | null; //     files (and symlinks to files): size; dirs / other: null
  mtime: Ts | null; //             null when lstat failed
  mode: string; //                 -rw-r--r-- ("" when lstat failed)
  hidden: boolean; //              name starts with "."
  /** the agent can read the file / list the directory (false = permission denied) */
  readable: boolean;
  /** why this entry cannot be sent (safety policy), null = allowed */
  blocked: string | null;
}
export interface HostFsListResponse {
  path: string; //                 cleaned absolute directory
  parent: string | null; //        null at "/"
  entries: HostFsEntry[]; //       directories first, then by name (case-insensitive)
  truncated: boolean; //           more than HOST_FS_MAX_ENTRIES entries
  hiddenCount: number; //          hidden entries left out (hidden=false)
  agentUser: string; //            the user the agent reads as (root on a standard install)
  /** the directory itself lies in a blocked area (its files cannot be sent) */
  blocked: string | null;
}
export const HOST_FS_MAX_ENTRIES = 5000;
export interface TunnelStartRequest {
  userAgent: string;
}
export interface TunnelLeaseResponse {
  lease: TunnelLease;
}
export interface TunnelStateResponse {
  lease: TunnelLease | null; // the active lease of the deployment (any site), if any
  holderSite: Slug | null;
  pending: number; //          queued requests on this site
}
export interface TunnelNextResponse {
  requests: TunnelRequest[];
}
export interface TunnelCompleteRequest {
  leaseId: Ulid;
  parts: number;
  sha256: Sha256;
}
export interface CertRenewResponse {
  status: 'renewed' | 'failed';
  cert: CertStatus;
}

// ═════════════════════════════════════════════════════════════ peer API (/_opm/peer/*)
export interface PeerRevisionResponse {
  revision: Revision;
}
export interface PeerProposalPush {
  proposal: Proposal;
  revision: Revision;
}
export interface PeerRelayRequest {
  /** the inner, already signed cloud request of the asking site */
  request: { method: string; path: string; headers: Record<string, string>; bodyB64: string | null };
}
export interface PeerActorsResponse {
  actors: ActorRow[];
}
export interface PeerSnapshotPartQuery {
  includeImages?: boolean;
}
```
