# OPM — On-Prem Manager · canonical spec v2 (generic product; source = user prompt; this file fills every "...")

Product name in docs: **On-Prem Manager (OPM)**. It is generic: no knowledge of any specific application. Never mention a specific application or vendor in the docs. Example data uses neutral services: `web` (nginx), `api` (a Node.js API), `postgres`, `redis`, `worker` (a job), `cache`. Example deployment: `acme-plant` (client "ACME Corp"), sites `app` and `db`, template "Two-site web + database".

## 0. Terms
- **Deployment**: one on-prem installation for one client. Has 1..N **sites**.
- **Site**: one server (one machine) that runs one **agent** and a set of **containers**. Identified by `siteSlug` inside its deployment.
- **Service**: a named kind of container (e.g. `postgres`). Has **images** (docker tags).
- **Image**: one docker tag of a service, imported into the cloud catalog. Immutable content.
- **Template**: a reusable deployment blueprint (variables, secrets, files, sites). A deployment copies a template (or starts empty).
- **Config**: the deployment-wide document { variables, secrets, files, sites }. Same on every site.
- **Revision**: an immutable version of the config. Linear chain on the sites.
- **Proposal**: a revision that waits for approval in the agent GUI (/app/updates).
- **Snapshot**: a backup taken by the agent (before every update, on demand, or imported).
- **Activity**: one audit record (who did what, where).
- **Expression**: a string with `{{ … }}` blocks, resolved by the agent on the site.

## 1. Three codebases
1. **cloud backend** — Firebase project: Cloud Functions (HTTP API, Node 22 + TypeScript, Express-style router), Firestore (data), Cloud Storage (blobs, files, transfers), Firebase Auth (human login only), Secret Manager (registry passwords), Cloud KMS (secret encryption keys). Clients never access Firestore/Storage directly: Firestore + Storage security rules deny all client access; everything goes through the HTTP API `https://api.opm.example/v1/...` (one domain). This keeps a later migration away from Firebase simple.
2. **cloud frontend** — SvelteKit SPA on Firebase Hosting at `https://app.opm.example`. Calls the API with the Firebase ID token.
3. **on-prem agent** — one Go binary per site (systemd service `opm-agent`), with an embedded GUI (Svelte SPA), SQLite store, built-in edge proxy, Docker Engine API driver. Runs as root (needs the Docker socket).

## 2. Common types (TypeScript notation; JSON on the wire)
```ts
type Id = string;        // Firestore auto id: 20 chars [A-Za-z0-9]
type Slug = string;      // ^[a-z][a-z0-9-]{1,39}$  user-defined, unique in its parent, immutable after create
type Uid = string;       // Firebase Auth uid
type Ts = string;        // ISO 8601 UTC, e.g. "2026-10-07T09:30:00Z" (stored as Firestore Timestamp)
type Sha256 = string;    // "sha256:" + 64 lowercase hex
type RevId = string;     // ULID (26 chars, sortable)
type Expr = string;      // a string that contains at least one "{{ … }}" block (see §9)
type Value = string;     // plain string or Expr
type Icon = string;      // a Lucide icon name (e.g. "database") or an https URL to an image
interface Audit { createdAt: Ts; createdBy: Uid; updatedAt: Ts; updatedBy: Uid; }
```
Limits: label ≤ 80 chars, description ≤ 2000 chars, any Firestore document ≤ 900 KB (file contents never inline).

## 3. Cloud data model (Firestore paths)
### 3.1 registries — `registries/{registryId}`
```ts
interface Registry extends Audit {
  registryId: Id;              // auto
  label: string;               // "Docker Hub · team account"
  url: string;                 // default "https://registry-1.docker.io" when the prompt's url? is omitted
  username: string;
  passwordRef: string;         // Secret Manager version name; WRITE-ONLY: the API accepts `password` on create/update, never returns it
  lastCheck: { at: Ts; ok: boolean; message: string } | null;   // POST /v1/registries/{id}/test
}
```
Delete allowed only if no image uses the registry.

### 3.2 services — `services/{serviceId}`
```ts
interface Service extends Audit {
  serviceId: Id;               // auto
  label: string;               // "PostgreSQL"
  icon: Icon;
  repository: string;          // image repository in the registry, e.g. "library/postgres", "acme/api"
  description: string;         // may be ""
}
```
### 3.3 images — `services/{serviceId}/images/{tag}` (route param imageId == tag)
```ts
interface Image extends Audit {
  tag: string;                 // identifier, = docker tag, ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$, IMMUTABLE
  registryId: Id;              // IMMUTABLE
  digest: Sha256 | null;       // manifest digest, set by the import, IMMUTABLE once set
  platform: 'linux/amd64' | 'linux/arm64';   // IMMUTABLE
  import: {
    mode: 'copy' | 'gateway';  // copy = cloud copies all layers to Storage now; gateway = layers are fetched from the registry on the first agent request and cached in Storage
    status: 'pending' | 'copying' | 'ready' | 'failed';
    error: string | null; sizeBytes: number | null;
    layers: { digest: Sha256; sizeBytes: number }[];
    finishedAt: Ts | null;
  };
  kind: 'service' | 'job';     // job = runs to completion; "completed" = exit code 0
  entrypoint: string[] | null; // null = use the image default
  command: string[] | null;    // "startup cmd"; null = image default
  workdir: string | null;
  user: string | null;         // "1000:1000"
  ports: ImagePort[];
  env: ImageEnv[];
  volumes: ImageVolume[];      // data mount points the image needs
  healthcheck: HealthCheck | null;
  stopTimeoutSec: number;      // default 30
  description: string;
}
interface ImagePort { name: Slug; port: number | Expr; protocol: 'tcp' | 'udp'; description: string; }  // container-side port
interface ImageEnv {
  name: string;                // ^[A-Z_][A-Z0-9_]*$
  value?: Value | string[];    // Value = default (plain or expression). string[] = list of allowed values; the FIRST item is the default
  required: boolean;           // a container must give a value if there is no default
  secret: boolean;             // true = the container must take the value from a secret (§3.7 secretRef)
  description: string;
}
interface ImageVolume { name: Slug; mountPath: string; description: string; }
type HealthCheck = {
  intervalSec: number; timeoutSec: number; retries: number; startPeriodSec: number;   // defaults 10, 5, 3, 30
} & (
  | { type: 'http'; portName: Slug; path: string; expectStatus: number }      // 200
  | { type: 'tcp'; portName: Slug }
  | { type: 'exec'; command: string[]; expectExitCode: number }              // 0
  | { type: 'log'; pattern: string }                                         // RE2, matched on stdout/stderr since the container started
);
```
Rules: "update is not allowed — create a new image with a new tag": the immutable fields above never change; re-importing a tag is not possible. Editable later (edit form on /image/{imageId}): description, ports[].description, env[].description, env[].value (defaults), healthcheck, stopTimeoutSec. Editing an image does NOT change existing deployments (they store their own container values). Delete allowed only if no template / deployment uses the tag.

### 3.4 notFoundPages — `notFoundPages/{notFoundPageId}` — create-only, never edited
```ts
interface NotFoundPage { notFoundPageId: Id; label: string; htmlCode: string; createdAt: Ts; createdBy: Uid; }
```
htmlCode ≤ 256 KB, one self-contained HTML document (inline CSS/JS/images as data URIs). The edge proxy of a site serves it with status 404 for any request that matches no edge route, and with status 503 + the same page when the routed container is not ready. No update endpoint exists. Delete only if unused.

### 3.5 tags — `tags/{tagSlug}`
```ts
interface Tag extends Audit { tagSlug: Slug; label: string; icon: Icon; color: string /* #rrggbb */; }
```
Tags are a queryable index (deployments.tagSlugs, array-contains), a bundler (filter / bulk view), and an access scope (§6).

### 3.6 accounts — `accounts/{uid}`
```ts
interface Account {
  uid: Uid; img: string | null; name: string; email: string; phone: string | null;   // phone in E.164
  access: AccessRule[]; disabled: boolean;
  createdAt: Ts; createdBy: Uid; lastLoginAt: Ts | null;
}
interface AccessRule { resource: Resource; actions: Action[]; scope: Scope; }
type Action = 'read' | 'write' | 'publish' | 'admin';
type Resource =
  | 'registries' | 'services' | 'images' | 'notFoundPages' | 'templates' | 'tags' | 'accounts'   // developer / admin tools (scope must be all)
  | 'deployments'                                   // list, create, delete, edit label/image/tags/managers
  | 'deployment.setup'                              // install commands, credential
  | 'deployment.config'                             // variables, files, sites (cloud draft)
  | 'deployment.secrets'                            // secret values (read = reveal)
  | 'deployment.activities'                         // read, upload (write)
  | 'deployment.transfers';                         // send / receive
type Scope = { kind: 'all' } | { kind: 'tags'; tagSlugs: Slug[] } | { kind: 'deployments'; deploymentIds: Id[] };
```
Evaluation: deny by default. Allowed if (a) any rule has the resource, the action, and a scope that contains the target deployment (tags: the deployment has at least one of the tags), or (b) for deployment.* resources, the user is in that deployment's managerAccountUids (all actions incl. publish). `publish` = create a proposal from the cloud draft (/diff). `admin` on accounts = change other accounts' access. Accounts are created only by an admin (POST /v1/accounts creates the Firebase Auth user and sends a password-set email). There is no signup.

### 3.7 templates — `templates/{templateId}`
```ts
interface Template extends Audit {
  templateId: Id; label: string; icon: Icon; description: string;
  version: number;             // +1 on every save
  variables: Variable[]; secrets: SecretDef[]; files: FileDef[]; sites: Site[];
}
interface Variable {
  variableSlug: Slug; label: string; description: string;
  type: 'string' | 'number' | 'boolean' | 'enum' | 'list' | 'json';
  value: string | number | boolean | string[] | object | null;   // template = default, deployment = actual value
  required: boolean;
  options: string[] | null;    // enum only
  validation: { min: number | null; max: number | null; pattern: string | null } | null;   // number: min/max; string: pattern (RE2)
}
interface SecretDef {
  secretSlug: Slug; label: string; description: string; required: boolean;
  generate: { charset: 'alnum' | 'hex' | 'base64url'; length: number } | null;   // length 8..128; null = a person must enter the value
  value: EncryptedValue | null;   // ALWAYS null in templates; set in deployments
  rotatedAt: Ts | null;
}
type EncryptedValue = string;    // "enc:v1:<base64url(nonce|ciphertext|tag)>" — AES-256-GCM with the deployment data key (§7.5)
interface FileDef {
  fileSlug: Slug; label: string; description: string;
  mime: string;                // "text/plain", "application/json", "application/octet-stream" …
  editor: 'text' | 'binary';   // text = edited in the code editor; binary = upload / download only
  maxBytes: number;            // default 1 MiB, max 50 MiB
  content: { sha256: Sha256; sizeBytes: number } | null;   // blob in Storage files/<sha256>; template = default content
}
interface Site { siteSlug: Slug; label: string; config: SiteConfig; }
interface SiteConfig {
  host: Value;                 // the address other sites and users use: an IP or a DNS name; may be an expression of variables
  containers: Container[];
  depends: { siteSlug: Slug }[];   // this site starts its containers only after every listed site is ready (§10)
  notFoundPageId: Id | null;   // null = a plain built-in 404
  edge: Edge;
}
interface Edge {
  httpPort: number;            // default 80
  httpsPort: number | null;    // null = no TLS listener
  tls: { mode: 'none' } | { mode: 'self-signed' } | { mode: 'files'; certFileSlug: Slug; keySecretSlug: Slug };
  routes: EdgeRoute[];         // longest path prefix wins; path "/_opm/" is reserved for the agent
}
interface EdgeRoute { path: string /* starts with "/" */; serviceId: Id; portName: Slug; stripPrefix: boolean; }
interface Container {
  serviceId: Id;               // ONE container per service per site; the serviceId is also its DNS name in the site network
  tag: string;                 // image tag; must be import.status = 'ready' before a proposal can be staged
  enabled: boolean;
  env: { name: string; value: Value | null; secretRef: Slug | null }[];   // exactly one of value / secretRef
  volume: (
    | { kind: 'data'; name: Slug; mountPath: string }                     // persistent dir /var/lib/opm/volumes/<serviceId>/<name>
    | { kind: 'file'; fileSlug: Slug; mountPath: string }                 // read-only mount of a deployment file
    | { kind: 'secret'; secretSlug: Slug; mountPath: string }             // read-only file with the decrypted secret (mode 0400)
  )[];
  port: { portName: Slug; hostPort: number | Expr | null; protocol: 'tcp' | 'udp' }[];   // hostPort null = reachable only inside the site network
  command: string[] | null;    // overrides the image command
  restart: 'always' | 'on-failure' | 'never';
  resources: { cpus: number | null; memoryMb: number | null };
  depends: { serviceId: Id }[];  // same site; this container starts after each listed container is "completed"
}
```
Network: each site has ONE Docker bridge network `opm_<deploymentId>`; containers reach each other by serviceId. Sites reach each other through `site.<slug>.host` + host ports (expressions). Nothing else is special.
"completed" (for both container depends and site depends): kind service → health check passes (or, without a health check, the container runs for 10 s with no restart); kind job → exit code 0. Timeout 300 s per container → failure.

### 3.8 deployments — `deployments/{deploymentId}` (+ subcollections)
```ts
interface Deployment extends Audit {
  deploymentId: Id; label: string;
  image: string | null;        // logo URL (Storage logos/<id>) shown on cards
  tagSlugs: Slug[]; managerAccountUids: Uid[];
  srcTemplate: { templateId: Id; templateVersion: number; snapshot: Template; copiedAt: Ts } | null;   // null = created empty
  draft: Config;               // the cloud draft (edited on /variables, /secrets, /files, /sites)
  head: { revId: RevId; seq: number; hash: Sha256; reportedAt: Ts } | null;   // last head reported by the agents (all sites share one head)
  credential: { version: number; status: 'none' | 'active' | 'renewing'; renewal: CredentialRenewal | null };
}
interface Config { variables: Variable[]; secrets: SecretDef[]; files: FileDef[]; sites: Site[]; }
```
Subcollections:
- `revisions/{revId}`: `{ revId, seq, parentRevId: RevId | null, origin: 'cloud' | 'agent', actor: string /* uid or email */, createdAt, config: Config, hash: Sha256, summary: string }` — hash = sha256 of the RFC 8785 canonical JSON of config.
- `proposals/{revId}`: `{ revId, parentRevId, origin, createdAt, createdBy, status: ProposalStatus, sites: { [siteSlug]: { staging: 'pending'|'downloading'|'ready'|'failed'; progress: number /*0..1*/; error: string|null } }, approvedBy: string|null, approvedAt: Ts|null, result: { ok: boolean; message: string; snapshotId: Id|null } | null }`
  `type ProposalStatus = 'published'|'staging'|'ready'|'approved'|'applying'|'applied'|'failed-rolled-back'|'stale'|'superseded'`
- `agents/{siteSlug}`: `{ siteSlug, publicKey /* Ed25519 base64 */, enrolledAt, agentVersion, lastSeenAt, online /* lastSeen < 90 s */, ip, hostname, relayVia: Slug | null, headRevId, staging: string | null }`
- `installTokens/{sha256(token)}`: `{ siteSlug, expiresAt /* +24 h */, usedAt: Ts|null, createdBy }`
- `activities/{siteSlug}:{seq}` (§11) · `transfers/{transferId}` (§12) · `uptime/{siteSlug}_{yyyymmdd}` (24 hourly online fractions)

## 4. Cloud frontend routes (SvelteKit; path params as in the prompt)
Root scope sidebar: Deployments · Templates · Services · Registries · Not-found pages · Tags · Accounts.
- `/login` — Firebase Auth email + password (or Google SSO for the org domain). No signup link. Disabled accounts are refused.
- `/app` — redirects to `/app/deployments`.
- `/app/registries` — list; create (label, url, username, password); "Test login"; delete if unused.
- `/app/services` — list (icon, label, repository, number of images, used by N deployments); create.
- `/app/services/{serviceId}` — service header + image list (tag, digest, size, import status, mode, kind); "Register image" (tag, registry, mode copy|gateway, kind) → import job; no update (new tag instead).
- `/app/services/{serviceId}/image/{imageId}` — full image detail (digest, layers, ports, env, volumes, entrypoint, command, workdir, user, health check) + edit form for the editable fields (§3.3).
- `/app/services/{serviceId}/image/{imageId}/used` — deployments + templates that use this tag (site, container).
- `/app/services/{serviceId}/used` — deployments + templates that use any tag of this service.
- `/app/not-found-pages` — list; create (label, htmlCode). No edit.
- `/app/not-found-pages/{notFoundPageId}` — sandboxed iframe preview + read-only code; "Create a copy" (prefilled create form).
- `/app/templates` — list; create (label, icon, description).
- `/app/templates/{templateId}` — overview + full JSON preview + edit label/icon/description; site dependency graph.
- `/app/templates/{templateId}/variables` — table editor for Variable[] (the prompt's `/add/...` typo is `/app/...`).
- `/app/templates/{templateId}/secrets` — SecretDef[] (no values in templates).
- `/app/templates/{templateId}/files` — FileDef[] with default content (code editor for text files).
- `/app/templates/{templateId}/sites` — Site[] list + add site (siteSlug, label, host).
- `/app/templates/{templateId}/sites/{siteSlug}` — site editor: containers (pick service + tag, env, volume, port, command, restart, resources, depends), site depends, notFoundPageId, edge (ports, TLS, routes); live validation + expression preview.
- `/app/accounts` — list; create (name, email, phone, img); edit access rules; disable.
- `/app/tags` — list; create / edit (label, icon, color).
- `/app/deployments` — cards (logo, label, tags, sites online, head seq, pending proposal); filter by tag; create (from a template or empty).
- `/app/deployments/{deploymentId}` — enters the DEPLOYMENT SCOPE: new sidebar (Setup · Status · Activities · Variables · Secrets · Files · Sites · Diff) and a navbar with the deployment name and an "Exit" button (back to root scope).
- `.../setup` — install command per site (generate, 24 h, single use, revoke); deployment credential (create, show once, renew — §7.4); offline install bundle.
- `.../status` — per site: online, last seen, relay, agent version, head, staging; 30-day online heatmap.
- `.../activities` — table (filters: site, origin, type, actor, date); `.../activities/upload` — upload an activity bundle file; upsert by (siteSlug, seq): existing records are never changed; shows imported / skipped counts.
- `.../variables`, `.../secrets`, `.../files`, `.../sites`, `.../sites/{siteSlug}` — edit the cloud DRAFT (same editors as templates; secrets show "set / not set", reveal needs deployment.secrets read).
- `.../sites/{siteSlug}/send` — upload files for that site (→ agent `/app/sites/{siteSlug}/receive`).
- `.../sites/{siteSlug}/receive` — files the site sent (from agent `/app/sites/{siteSlug}/send`); download.
- `.../diff` — draft vs last reported head, per section (variables, secrets (changed / not changed only), files (hash), sites, containers, edge); buttons "Publish proposal" (needs publish) and "Copy head into draft" (discard cloud edits).

## 5. Agent GUI routes (served by every agent at `http://<site host>:<edge httpPort>/_opm/...`)
- `/_opm/login` — shows deploymentId + this siteSlug; fields: actor email + deployment credential (§7.3).
- `/_opm/app` — all sites of the deployment: online, mesh link matrix (who can reach whom), head, staging state.
- `/_opm/app/variables`, `/secrets`, `/files`, `/sites` — the deployment-wide config, identical on all sites. Edits create a LOCAL proposal (§8.5). Allowed: change values, file contents, env values, volume / port / restart / resources / depends / edge / notFoundPageId. NOT allowed locally: add or remove a container, change a container's serviceId or tag, add or remove a site (cloud only).
- `/_opm/app/updates` — proposals (cloud and local) with diff and staging per site; ONE "Approve" button (no schedule, no deny) under the gate (§8.3).
- `/_opm/app/backups` — snapshots (§10): list, download, import, roll back.
- `/_opm/app/{siteSlug}/debug` — the agent's own debug log (siteSlug `*` = all sites merged, sorted by time).
- `/_opm/app/{siteSlug}/status` — containers (state, health, restarts, CPU, RAM, ports), host (CPU, RAM, disk), uptime heatmaps (24 h of 30 s samples, 30 days of 1 h buckets), container logs (tail + search). `*` = all sites.
- `/_opm/app/{siteSlug}/activities` — activity log (`*` = all); `/_opm/app/{siteSlug}/activities/download` — signed bundle download (§11).
- `/_opm/app/{siteSlug}/config` — the site's part of the config (containers, edge, depends) with the same local-edit rules.
- `/_opm/app/sites/{siteSlug}/send` — upload a file from this server (choose a path on the server or from the browser) → cloud `.../receive`.
- `/_opm/app/sites/{siteSlug}/receive` — files from the cloud: download to the browser, or "Save on server" to a path you type.
Every page of any agent shows ALL sites (the agent fetches peers over the mesh); a site that does not answer shows "stale · last seen hh:mm".

## 6. (see 3.6 access) — permission matrix per frontend route is in the docs.

## 7. Auth
### 7.1 Humans → cloud: Firebase Auth. The frontend sends `Authorization: Bearer <Firebase ID token>`. The API verifies it, loads accounts/{uid}, refuses disabled accounts, evaluates access rules.
### 7.2 Install (24 h token)
1. Setup page → "Create install command" for siteSlug X → API makes a random 32-byte token, stores sha256 in installTokens (expires +24 h, single use).
2. Command: `curl -fsSL -H "Authorization: Bearer <token>" https://api.opm.example/v1/install/script | sudo sh`
3. The script downloads the agent binary (signed URL), checks sha256 + Ed25519 signature (public key in the script), writes `/etc/opm/agent.json` `{ apiBase, deploymentId, siteSlug }`, installs the systemd unit, starts the agent.
4. The agent makes an Ed25519 **site key** (private key `/var/lib/opm/site.key`, 0600) and calls `POST /v1/agent/enroll` with the token + public key → cloud marks the token used, stores agents/{siteSlug}.publicKey, returns `{ peers: [{siteSlug, host, httpPort, publicKey}] }`.
5. The site config is EMPTY. Nothing runs until a person logs in and approves the first proposal.
A site without internet: install with the command from another site's agent GUI (`http://<site>/_opm/install/<siteSlug>` script, same token rules, binary from that agent's cache); its cloud traffic is relayed by a peer with internet.
### 7.3 Agent login (custom auth — no Firebase on the site)
- Deployment credential = one random secret per deployment (shown ONCE in the cloud Setup page; format `opm_` + 32 base62 chars). The cloud stores only an argon2id hash (m=64 MiB, t=3, p=1, 16-byte salt) + version.
- /_opm/login: actor email + credential. First login on a site: the agent sends the credential to `POST /v1/agent/credential/verify` (signed request, §7.6); on success the cloud returns the verifier (hash + params + version); the agent stores it, so later logins work offline. The email is the actor name in activities (audit; it is not verified — see open questions).
- Session: the agent issues its own JWT (EdDSA, signed with the site key; claims { sub: email, dep, site, credVersion, iat, exp }) in an HttpOnly cookie; TTL 12 h, idle timeout 60 min. Any agent accepts a session from any site of the same deployment (it knows all site public keys). A credential version change ends all sessions.
- 5 failed logins per IP per 10 min → 15 min lock.
### 7.4 Credential renewal — all sites or nothing (two-phase)
1. Cloud Setup → "Renew credential" → API creates `renewal = { newVersion: v+1, newHash, status: 'preparing', createdAt }`; the new plaintext is shown once.
2. An agent with internet (the **coordinator**, chosen as the first site in slug order that is online) gets the renewal on its next poll and sends PREPARE (sealed new verifier) to every site over the mesh. Each site stores it as `pending` and answers 200.
3. If every site answered 200 within 60 s → coordinator calls `POST /v1/agent/credential/prepared` → API sets `status: 'committed'`, `credential.version = v+1`, answers 200.
4. Coordinator sends COMMIT to every site → each swaps pending → active and ends old sessions.
5. If any site does not answer PREPARE → coordinator sends ABORT, API sets `status: 'aborted'` with the missing sites; the old credential stays valid everywhere. A site that has `pending` but missed COMMIT asks a peer or the cloud on start: committed → swap; aborted → drop. There is never a mix.
### 7.5 Secrets at rest
- Cloud: each deployment has a 256-bit data key (DEK) wrapped by Cloud KMS; secret values are `enc:v1:` AES-256-GCM with the DEK.
- Site: the DEK is sealed to each site's public key (X25519 from the Ed25519 key, libsodium sealed box) and sent at enroll; the agent decrypts only in memory when it starts a container or writes a secret mount.
### 7.6 Signed requests (agent → cloud and agent → agent)
Headers: `X-OPM-Site: <deploymentId>/<siteSlug>`, `X-OPM-Ts: <unix seconds>`, `X-OPM-Nonce: <16 random bytes b64url>`, `X-OPM-Sig: <Ed25519 signature b64url over "METHOD\nPATH\nTS\nNONCE\nsha256(body)">`. Reject if |now - ts| > 60 s or the nonce was seen in the last 5 min.

## 8. Config, revisions, proposals, updates
### 8.1 Revisions are linear on the sites: head → child. Every agent stores the same head. revId = ULID, seq = parent.seq + 1.
### 8.2 Cloud publish: /diff → "Publish proposal" → revision { parentRevId: deployment.head.revId, origin: 'cloud', config: draft } + proposal status 'published'. Agents get it on poll. If the sites' head is not the parent anymore (a local change happened) → the agents mark it 'stale'; the cloud shows "rebase needed": Diff page → "Copy head into draft", edit again, publish again. Nothing is merged silently. A newer cloud proposal marks older unapproved ones 'superseded'.
### 8.3 Staging (every site, automatic, no approval needed): validate the config (schema + every expression resolves) → download missing image layers (from the cloud catalog; agent never sees registry credentials) → `docker load` (do not run) → download missing file blobs → build container specs and dry-run validate (ports free, mounts possible). Status per site goes to the proposal.
Approve button is VISIBLE only when ALL: (a) proposal.parentRevId == head on every site; (b) every site of the current AND of the proposed config is enrolled and reachable over the mesh now (last answer < 30 s); (c) every such site has staging 'ready'; (d) no other update is running.
### 8.4 Apply: click Approve → 60-second wait (shown as a countdown, can be cancelled) → a dialog asks to type ACCEPT → the approving agent becomes the coordinator:
1. LOCK all sites (no other change until done). 2. SNAPSHOT on every site (§10, reason 'pre-update'): config + image refs of all containers + data volumes of the containers that will change (they are stopped first, in reverse dependency order). 3. APPLY in order: sites in `depends` order (topological; cycles are rejected at validation), and inside a site containers in `depends` order: recreate changed containers, start new ones, remove deleted ones; edge config reloads last. 4. VERIFY: every container "completed" (§3.7) within 300 s. 5. COMMIT: head = new revision on every site; proposal 'applied'.
On ANY failure in 2–4 → ROLLBACK on every site from the pre-update snapshot (previous config, previous images, restore the snapshot volumes, restart) → proposal 'failed-rolled-back' with the message. Every step is an activity.
New site in the proposal: it must be installed (enrolled) and reachable before the gate opens.
### 8.5 Local changes (agent GUI): the edit page shows a diff → "Create local proposal" → revision { origin: 'agent', actor: email } → same /app/updates gate and apply. After apply, the agents report the new head; the cloud shows it in /diff (head ≠ draft).

## 9. Expressions
Syntax: `{{ path | filter(args) | filter }}` inside any Value string; several blocks allowed; text outside blocks is kept. Literals: "text", 123, true.
Paths: `var.<variableSlug>` · `secret.<secretSlug>` (only in env with secretRef or file mounts; never in edge or host) · `file.<fileSlug>.path` (mount path inside the container when mounted) · `site.<siteSlug>.host` · `site.<siteSlug>.port.<serviceId>.<portName>` (host port) · `self.slug` · `self.host` · `deployment.id` · `container.serviceId`.
Filters: `default(x)` · `join(sep)` (lists) · `upper` · `lower` · `trim` · `base64` · `url` (URL-encode) · `int` · `json`.
Evaluation: on the site, at staging and at container start. An unknown path or a type error = staging error (the proposal cannot be approved). Result type: always string, except a whole-value expression in a number field (hostPort, ImagePort.port) which must parse as an integer.
Example: `DATABASE_URL = "postgres://app:{{ secret.db-password | url }}@{{ site.db.host }}:{{ site.db.port.postgres.sql }}/{{ var.db-name }}"`.

## 10. Backups / snapshots (agent; /app/backups)
```ts
interface Snapshot { snapshotId: string /* ULID */; createdAt: Ts; reason: 'pre-update' | 'manual' | 'scheduled' | 'imported'; revId: RevId; seq: number;
  sites: { [siteSlug]: { config: Config; images: { serviceId: Id; tag: string; digest: Sha256 }[]; volumes: { serviceId: Id; name: Slug; sizeBytes: number; sha256: Sha256; path: string }[]; sizeBytes: number } };
  createdBy: string; note: string; }
```
- Stored on each site under `/var/lib/opm/snapshots/<snapshotId>/` (volume archives `.tar.zst`, `manifest.json`). Images referenced by a kept snapshot are never pruned.
- Retention: keep the last 10 pre-update + 7 daily scheduled + all manual (configurable later); a free-space check (needs 1.2 × estimate) runs before every snapshot.
- Roll back: pick a snapshot → same gate as §8.3 (all sites reachable) → 60 s → type ACCEPT → restore config + images + volumes on every site → a new revision (origin 'agent', summary "rollback to …") becomes head. Data written after the snapshot is lost (the dialog says so).
- Download: one `.opmsnap` file (tar: manifest.json + per-site volume archives; optional "include images"). Import: upload an `.opmsnap` → verified (sha256 of every part) → listed with reason 'imported' → can be rolled back to.

## 11. Activities and debug logs
```ts
interface Activity { activityId: string /* "<siteSlug>:<seq>" */; deploymentId: Id; siteSlug: Slug; seq: number; ts: Ts;
  actor: string | null /* email, uid, or null = system */; origin: 'agent' | 'cloud' | 'system';
  type: string /* e.g. login, proposal.created, proposal.approved, update.applied, update.rolled-back, config.local-change, snapshot.created, transfer.sent, credential.renewed */;
  summary: string; data: object; prevHash: Sha256 | null; hash: Sha256; uploadedVia: 'pipe' | 'manual' | null; uploadedAt: Ts | null; }
```
- One log per site, append-only, hash chain (hash = sha256(prevHash + canonical JSON without hash fields)).
- Upload pipe (separate agent component): when the cloud is reachable (directly or via a relay peer), it sends all entries after the last acknowledged seq in batches of 500; the API upserts by activityId and NEVER changes an existing record; ack = highest stored seq.
- Download (/activities/download): `.opmlog` = NDJSON + manifest { deploymentId, sites, seq ranges } + Ed25519 signature of the site. Cloud /activities/upload verifies signature + chain, inserts missing records with uploadedVia 'manual', skips existing ones, reports counts.
- Debug log: the agent's own JSON-lines log (level, ts, module, msg, fields), rotated daily, kept 7 days / 200 MB; shown on /debug; can be included in a download.

## 12. Transfers (send / receive)
```ts
interface Transfer { transferId: Id; deploymentId: Id; siteSlug: Slug; direction: 'to-site' | 'to-cloud'; fileName: string; sizeBytes: number; sha256: Sha256;
  storagePath: string /* transfers/<deploymentId>/<transferId> */; createdAt: Ts; createdBy: string; note: string; expiresAt: Ts | null /* null: transfers never expire; only Delete removes one */;
  status: 'uploading' | 'ready' | 'saved-on-server' | 'downloaded' | 'expired'; savedPath: string | null; }
```
Max 2 GiB per file (resumable upload). Cloud → site: cloud `.../send` uploads → agent `/receive` lists → "Save on server" (path) or browser download. Site → cloud: agent `/send` uploads (from a server path or the browser) → cloud `.../receive`. Every transfer is an activity.

## 13. Status and uptime
Agent heartbeat to cloud every 30 s (poll). Cloud: online = lastSeen < 90 s; 30-day online heatmap from `uptime/` docs. Agent: samples every 30 s per site and per container; keeps 24 h raw + 30 days hourly; shown on /status.

## 14. Cloud HTTP API (all JSON; errors `{ "error": { "code": string, "message": string, "details": object } }`; codes: unauthenticated, forbidden, not-found, conflict, invalid, stale, gone, rate-limited, internal)
Human (Firebase ID token):
- registries: GET/POST `/v1/registries`, GET/PATCH/DELETE `/v1/registries/{id}`, POST `/v1/registries/{id}/test`
- services: GET/POST `/v1/services`, GET/PATCH/DELETE `/v1/services/{id}`, GET `/v1/services/{id}/used`
- images: GET/POST `/v1/services/{id}/images`, GET/PATCH/DELETE `/v1/services/{id}/images/{tag}`, GET `/v1/services/{id}/images/{tag}/used`
- not-found pages: GET/POST `/v1/not-found-pages`, GET `/v1/not-found-pages/{id}`, DELETE (unused only)
- templates: GET/POST `/v1/templates`, GET/PATCH/DELETE `/v1/templates/{id}` (PATCH accepts any of label, icon, description, variables, secrets, files, sites; checks `ifVersion`)
- files (blobs): POST `/v1/blobs` (multipart, returns { sha256, sizeBytes }), GET `/v1/blobs/{sha256}`
- tags: GET/POST `/v1/tags`, PATCH/DELETE `/v1/tags/{slug}`
- accounts: GET/POST `/v1/accounts`, GET/PATCH `/v1/accounts/{uid}` (access needs accounts.admin), POST `/v1/accounts/{uid}/disable`
- deployments: GET/POST `/v1/deployments` (POST { label, templateId | null, tagSlugs, managerAccountUids }), GET/PATCH/DELETE `/v1/deployments/{id}`
- PUT `/v1/deployments/{id}/draft/{variables|secrets|files|sites}` (whole array, `ifRevision` check) · PUT `/v1/deployments/{id}/draft/sites/{slug}`
- POST `/v1/deployments/{id}/secrets/{slug}/reveal` (needs deployment.secrets read; logged)
- GET `/v1/deployments/{id}/diff` · POST `/v1/deployments/{id}/publish` · POST `/v1/deployments/{id}/draft/reset-to-head`
- setup: POST `/v1/deployments/{id}/install-tokens` { siteSlug } → { command, expiresAt } · DELETE `/v1/deployments/{id}/install-tokens/{tokenHash}` · POST `/v1/deployments/{id}/credential` (create, returns plaintext once) · POST `/v1/deployments/{id}/credential/renew` · GET `/v1/deployments/{id}/credential`
- status: GET `/v1/deployments/{id}/status` · activities: GET `/v1/deployments/{id}/activities?site&origin&type&from&to&cursor` · POST `/v1/deployments/{id}/activities/upload` (multipart .opmlog) → { inserted, skipped, rejected }
- transfers: GET `/v1/deployments/{id}/sites/{slug}/transfers?direction=` · POST `/v1/deployments/{id}/sites/{slug}/transfers` (to-site, resumable) · GET `/v1/transfers/{transferId}/download`
Agent (signed requests §7.6; install token only for script + enroll):
- GET `/v1/install/script` · GET `/v1/install/binary?os&arch` (302 to a 5-min signed URL)
- POST `/v1/agent/enroll` · GET `/v1/agent/poll?cursor` (long-poll ≤ 55 s; returns proposals, renewal, transfers, config draft events) · POST `/v1/agent/heartbeat` { sites seen, head, staging, metrics summary }
- POST `/v1/agent/head` { revision } (reports a new head incl. local revisions) · GET `/v1/agent/proposals/{revId}` · POST `/v1/agent/proposals/{revId}/status` { siteSlug, staging | status, progress, error }
- GET `/v1/agent/images/{serviceId}/{tag}/manifest` · GET `/v1/agent/blobs/{sha256}` (302 signed URL; gateway mode fetches from the registry first)
- POST `/v1/agent/activities` { entries[] } → { ackSeq } · POST `/v1/agent/credential/verify` · POST `/v1/agent/credential/prepared` · POST `/v1/agent/credential/aborted`
- GET `/v1/agent/transfers?direction=to-site` · POST `/v1/agent/transfers` (to-cloud upload, resumable) · POST `/v1/agent/transfers/{id}/status`
- POST `/v1/agent/relay` { forSite, request } (a site with internet forwards a signed request of a peer without internet)

## 15. Agent internals (Go)
Modules: config store (SQLite: revisions, proposals, snapshots index, activities, debug index, uptime_raw, uptime_hourly, transfers, sessions, credential, peers) · cloud link (poll, heartbeat, relay) · mesh (peer HTTP over edge port `/_opm/peer/*`, signed) · stager · expression engine · container driver (Docker Engine API; labels opm.deployment, opm.site, opm.service, opm.rev, opm.spec_hash) · edge proxy (HTTP/HTTPS listener, routes, not-found page, `/_opm/` reserved) · health/readiness checker · update coordinator (lock, snapshot, apply, verify, commit, rollback) · snapshot manager · upload pipe · transfers · auth (login, sessions, credential 2PC) · GUI server.
Disk: `/etc/opm/agent.json`, `/var/lib/opm/{site.key, opm.db, volumes/, snapshots/, images-cache/, files/, transfers/, logs/}`.
Ports: one edge port (default 80, optional 443) for users, the agent GUI (`/_opm/`) and peer traffic (`/_opm/peer/`). Outbound: `api.opm.example:443` only.

## 16. Prototype tomorrow (scope)
Must work end-to-end: login (cloud) → registry → service → import image (copy mode) → template with 2 sites (app: web + api; db: postgres) → deployment from template → install command → agent enroll on 2 VMs → agent login → first proposal staged → gate → Approve + 60 s + ACCEPT → snapshot → apply in depends order → status + activities in both GUIs → a local env change → diff in cloud → publish → rollback from backups. Stub for the prototype: gateway mode, credential renewal 2PC (show UI, implement later), TLS files mode, relay, uptime 30-day view, transfers (optional), access rules (single admin role first).
Open questions (state in docs): actor identity on agents (shared credential + typed email = audit only; option: per-person agent accounts or TOTP later); multi-container-per-service (v1: one per service per site); Windows hosts (no); offline install bundle (later); image signing (later).

## Addenda (decided while writing docs)
- Scheduled snapshots: daily at 02:00 site local time (prototype default). Manual and scheduled snapshots include ALL data volumes; pre-update snapshots include only the volumes of containers that change.
- Service slug: `Service` gets `slug: Slug` (user-defined at create, unique across services, IMMUTABLE). serviceId stays the auto id (prompt) and is what containers/edge routes store; the slug is the container DNS name in the site network and the name used in expressions: `site.<siteSlug>.port.<serviceSlug>.<portName>`. Examples use slugs web, api, postgres, redis, worker.
- `CredentialRenewal = { newVersion: number; newHash: string; status: 'preparing'|'committed'|'aborted'; coordinator: Slug|null; preparedSites: Slug[]; missingSites: Slug[]; createdAt: Ts; createdBy: Uid; finishedAt: Ts|null }`.
- `UptimeDay` (`deployments/{id}/uptime/{siteSlug}_{yyyymmdd}`) = `{ siteSlug; date: 'yyyymmdd'; hours: number[24] /* online fraction 0..1 */; updatedAt: Ts }`.
- Secret rule (§9 fix): `secret.*` may appear ONLY in `Container.env[].value`; an env value that uses `secret.*` is treated as secret (masked in GUIs, never logged). `secretRef` remains for a whole-value secret. Secret file mounts use `volume.kind = 'secret'`.
- Expression limits: `SiteConfig.host` and every hostPort/port expression may use only `var.*` and `deployment.id` (no site/secret refs → no cycles between sites); number fields must be exactly one whole `{{ }}` block; path segments may start with a digit.
- Draft edit counter: `Deployment.draftVersion: number` (+1 on every draft write). Every PUT `/draft/*` and POST `/publish` sends `ifRevision = draftVersion`; a mismatch returns `conflict` (two people editing the same draft).
- `POST /v1/agent/enroll` for a site that is already enrolled returns `conflict` (re-install = revoke the site in Setup first, then a new token).
- `GET /v1/agent/images/{serviceId}/{tag}/manifest` (and its blobs) is refused (`forbidden`) unless that tag is used in the site's current head or in a published, not-yet-applied proposal for that deployment.
- SECRETS SIMPLIFIED (user decision, overrides §3.1 passwordRef, §3.7 EncryptedValue, §7.5): secrets are stored like variables — plain values in the same documents (Firestore: `SecretDef.value: string | null`; registries: `password: string`; agent SQLite: same tables as variables). No Cloud KMS, no data key, no Secret Manager, no sealing to site keys for now. Database security is out of scope for the prototype; protection is in the GUIs and the API:
  - The API never returns secret values in normal reads: `value` is replaced by `"•••"` and `isSet: boolean`. Registry `password` likewise.
  - Cloud frontend and agent GUI show `•••` with an eye button. Eye → `POST /v1/deployments/{id}/secrets/{slug}/reveal` (cloud, needs deployment.secrets read) or `POST /_opm/api/secrets/{slug}/reveal` (agent) → returns the value for that view only, hides again after 30 s.
  - Every reveal writes an activity `secret.revealed` { secretSlug, actor, where: 'cloud'|'agent', siteSlug? }. Registry password reveal writes `registry.password-revealed` (cloud audit log in `auditLog/{id}`).
  - Every time an agent fetches secret values (proposal download that contains secrets, or a peer copy during staging), the cloud / the sending agent writes `secret.fetched` { siteSlug, secretSlugs[], revId } (values never in logs).
  - Values never appear in logs, diffs (diff shows "changed / not changed"), debug logs or downloaded activity bundles. Snapshots contain the config incl. secret values (warn on download).
- SMART DISTRIBUTION (user decision; extends §7.2 relay, §8.3 staging, §15):
  - Every agent with internet talks to the cloud INDEPENDENTLY: its own poll, its own downloads (config / proposals, image layers, file blobs, transfers) and its own reports.
  - Cloud view: each agent keeps `cloudView = { fetchedAt: Ts /* the cloud server time of the poll response */, cursor, proposals[], renewal, transfersToSite[], draftHead }`. Agents exchange cloudView with peers every 10 s over the mesh. LATEST FETCH WINS: an agent adopts a peer's cloudView when the peer's `fetchedAt` is newer than its own (equal → keep own). So a site without internet always has the newest cloud view that any peer has seen.
  - Blobs (image layers, file blobs) are content-addressed (sha256), so any copy is valid after the hash check. Each agent advertises a `have` list (sha256 set) to peers.
  - Getting a missing blob: (1) from a peer that has it (LAN first), else (2) from the cloud if this site has internet, else (3) ask an internet peer to fetch it. To avoid double downloads, each missing blob has ONE fetcher: the internet-connected site chosen by rendezvous hashing over (sha256, siteSlug) among sites that currently have internet; the others wait for it and then copy from it. If the fetcher goes away, the next site in the hash order takes over.
  - Reports (status, activities, staging, heartbeat) from a site without internet go through any internet peer (relay); a peer never changes the content (signed by the origin site).
  - Staging shows per blob where it came from: cloud · peer <slug>.
- NO SITE LOGIN — "WHO IS DOING THIS?" (user decision; overrides §7.3, §7.4 and the prompt's credential login):
  - The agent GUI has NO email/password or credential login (the site may not know that a new operations person took over).
  - On EVERY page load (refresh) the GUI shows a blocking popup "Who is doing this?" with one field: email. Below it: suggestions = the 10 most recently used emails on this deployment (from activities, newest first; click to fill). This is NOT authentication; it only names the actor.
  - `POST /_opm/api/actor { email }` → `{ actorToken, expiresAt }` (agent-signed JWT, claims { email, dep, site, iat, exp }); kept only in page memory (a refresh asks again). Every write call sends `Authorization: Actor <actorToken>`; the agent writes the email into the activity (`actor`). The token expires 6 h after it was issued → the popup comes back ("logout every 6 h").
  - Activity `actor.declared` { email, siteSlug, userAgent, ip } on every popup submit.
  - Consequences: the DEPLOYMENT CREDENTIAL is REMOVED (no `Deployment.credential`, no CredentialRenewal, no `/credential` endpoints, no `/v1/agent/credential/*`, no 2-phase renewal, no credential panel on the Setup page). Machine trust stays: 24 h install token → site Ed25519 key → signed requests (cloud and peers).
  - Risk (state it): anyone who can open the agent GUI can act; mitigations: keep the edge port / GUI on the client admin network, the 60 s wait + typed ACCEPT for updates and rollbacks, every action logged with the declared email + IP + user agent. Optional later: an allow-list of emails per deployment, or a site PIN.
- NO EDGE / NO ROUTING IN OPM (user decision; removes §3.7 Edge + EdgeRoute, the edge proxy in §15 and every "edge" mention):
  - Routing and custom integrations are NOT part of OPM. They are containers (e.g. an nginx container with its own config file).
  - The agent listens on ONE port of its own: `SiteConfig.agentPort` (default 7080) for the agent GUI `/_opm/...`, the agent API `/_opm/api/...` and peer traffic `/_opm/peer/...`. It never binds 80/443 for the user's applications. Containers publish their own host ports (`Container.port[].hostPort`).
  - `SiteConfig` = { host, agentPort, containers, depends, notFoundPageId }. Remove `edge`.
  - Not-found pages stay (catalog, create-only). Delivery: new container volume kind `{ kind: 'notFoundPage'; mountPath }` → the agent writes the site's selected page (notFoundPageId) as a read-only file at mountPath (e.g. `/usr/share/nginx/html/404.html`). The routing container serves it however it wants. If notFoundPageId is null, the mount gets a plain built-in 404 page.
- BROWSER INTERNET TUNNEL (user idea; adds a 4th route to the cloud):
  - Route order for cloud traffic of a site: own internet → a peer site with internet (relay / smart distribution) → the browser tunnel.
  - Agent GUI route `/_opm/app/internet`. It shows the tunnel state for the DEPLOYMENT (one tunnel per deployment, all sites can use it through the mesh). If a lease is active (another tab / person), the page shows who and since when, and the only actions are "Revoke the other tab" (confirm; logged) or leave. With no active lease: "Give internet from this browser" → the tab holds the lease.
  - `TunnelLease = { leaseId: ULID; actor: string /* declared email */; siteSlug: Slug /* the site whose GUI the tab opened */; userAgent: string; startedAt: Ts; lastPollAt: Ts; status: 'active'|'stopped'|'revoked'|'expired' }`. Expires when the tab does not poll for 30 s (tab closed). Kept on the site whose GUI holds it and shared with peers.
  - Queue: the agent puts outbound cloud requests into `TunnelRequest = { reqId: ULID; method; url /* https://api.opm.example/... or a signed Storage URL */; headers /* incl. X-OPM-* signature */; bodyB64: string|null; createdAt; timeoutSec }`.
  - Tab loop: `GET /_opm/api/tunnel/next?leaseId=…` (long-poll ≤ 25 s, returns up to 4 requests) → the browser does `fetch(url, { method, headers, body })` → `POST /_opm/api/tunnel/result { leaseId, reqId, status, headers, bodyB64 }` (large bodies: `POST /_opm/api/tunnel/result/{reqId}/chunk?offset=` binary chunks, then `/complete`). Max 4 requests in flight. The agent matches the result by reqId; unanswered requests time out and are retried by the normal retry logic.
  - Start / stop / revoke: `POST /_opm/api/tunnel/start` (409 `conflict` with the active lease if one exists), `POST /_opm/api/tunnel/stop`, `POST /_opm/api/tunnel/revoke { leaseId }`.
  - Cloud side: the agent API (`/v1/agent/*`, `/v1/install/*`) and the Storage bucket allow CORS from any origin (requests are authenticated by the site signature, not by cookies), so a browser can make these calls.
  - The tab shows a live counter (requests, bytes, last error) and must stay open; closing it ends the tunnel.
  - Risk (state it): payloads pass through the browser (proposals include secret values) — the tab is same-origin with the agent GUI only; every secret fetch is still logged (`secret.fetched`); encrypting agent↔cloud payloads end to end is a later item.
  - Activities: `internet.tunnel.started` { leaseId, actor, siteSlug }, `internet.tunnel.stopped`, `internet.tunnel.revoked` { leaseId, by }, `internet.tunnel.expired`.
- PORT 80 TAKEOVER + NOT-FOUND PAGES (user clarification; REPLACES the "notFoundPage volume kind" idea above — there is NO notFoundPage volume kind):
  - The agent always listens on `agentPort` (default 7080).
  - If NO container of the site publishes host port 80/tcp (e.g. the db site has no nginx), the agent ALSO listens on port 80 and serves there: `/_opm/...` = the agent GUI / API (same as on agentPort), and EVERY OTHER PATH = the site's not-found page (`notFoundPageId`, HTTP 404; built-in plain page if null).
  - Inside the agent GUI, an unknown `/_opm/...` path shows the AGENT's own 404 page, never the site's page.
  - If a container publishes 80 (e.g. the app site has nginx with hostPort 80), the agent does not use 80; the not-found page is then not served by the agent.
  - Changes are handled in the update apply step: if a proposal adds a container on hostPort 80, the agent releases 80 just before it starts that container; if a proposal removes it, the agent takes 80 after the container stopped. Staging validates that only one owner claims 80.
- EDGE IS BACK — IN THE AGENT, POWERED BY EMBEDDED CADDY (user decision; REPLACES "NO EDGE / NO ROUTING IN OPM" and "PORT 80 TAKEOVER"; nginx/caddy containers are no longer used for routing):
  - The agent embeds Caddy v2 as a Go library (in-process, NOT a child process / container). The agent turns the site's `edge` config into Caddy JSON and loads it with `caddy.Load` (graceful reload, no dropped connections). Certificates are handled by Caddy's CertMagic: storage `/var/lib/opm/certs/`.
  - Ports: the edge owns `httpPort` (default 80) and `httpsPort` (default 443, null = no TLS). Containers may NOT publish 80/443 (validation error). Peer traffic and a fallback GUI stay on `agentPort` (default 7080), so a broken edge config never breaks the mesh. The agent GUI is ALSO on the edge at the reserved prefix `/_opm/` on every host.
  - Types:
    ```ts
    interface SiteConfig { host: Value; agentPort: number; containers: Container[]; depends: { siteSlug: Slug }[]; notFoundPageId: Id | null; edge: Edge; }
    interface Edge { httpPort: number /*80*/; httpsPort: number | null /*443*/; hosts: EdgeHost[]; routes: EdgeRoute[]; }
    interface EdgeHost {
      hostname: string;            // DNS name ("plant.acme.com"), wildcard ("*.acme.com"), an IP ("10.0.1.10"), or "*" = any host. May use {{ var.* }}.
      tls: { mode: 'none' }        // HTTP only
         | { mode: 'internal' }    // Caddy local CA (default for IPs / private names; clients install the CA once, downloadable in the GUI)
         | { mode: 'acme'; email: string; challenge: 'http-01' | 'tls-alpn-01' | 'dns-01'; dnsProvider: string | null; dnsTokenSecretSlug: Slug | null }   // Let's Encrypt; http/tls challenges need the site reachable from the internet; dns-01 needs the provider API token secret
         | { mode: 'files'; certFileSlug: Slug; keySecretSlug: Slug };   // the client's own certificate
      redirectHttpToHttps: boolean;
    }
    interface EdgeRoute {
      routeId: Slug;               // unique in the site
      hosts: string[];             // hostnames from edge.hosts this route applies to; [] = all hosts
      match: { pathPrefix: string /* starts with "/" */; methods: string[] | null };
      action:
        | { type: 'proxy'; serviceId: Id; portName: Slug; stripPrefix: boolean; addPrefix: string | null; setHeaders: Record<string, Value>; removeHeaders: string[]; websocket: boolean; timeoutSec: number /*60*/ }
        | { type: 'redirect'; to: Value /* absolute URL or path; placeholders {path} {query} {host} */; status: 301 | 302 | 307 | 308; keepPath: boolean };
      order: number;               // explicit order shown in the GUI; ties are broken by the longest pathPrefix
    }
    ```
  - Matching: host first (exact > wildcard > "*"), then the routes for that host in `order`, first match wins. `/_opm/` is reserved on every host (agent GUI/API); unknown `/_opm/…` paths get the agent's own 404. No match → the site's not-found page (404). A proxy target that is not ready → the same page with status 503.
  - TLS in the GUI: per host the certificate status (issuer, valid until, last renewal, last error); "Download local CA" for internal mode; ACME errors surface as alerts. Activities: `cert.issued`, `cert.renewed`, `cert.failed`, `edge.reloaded`, `edge.reload-failed` (a failed reload keeps the previous config running and fails the update step → automatic rollback).
  - GUI: cloud site editor and agent `/_opm/app/{siteSlug}/config` get an **Edge** tab: Hosts table (add hostname, TLS mode, cert status), Routes table (drag to reorder; host filter; path prefix; action proxy (service + port, strip prefix, add prefix, headers, websocket, timeout) or redirect (target, status, keep path)), and a **Test a URL** box (enter `https://plant.acme.com/api/v1/x` → shows the matched host, route, action and the upstream URL after strip/add prefix). Local edits of the edge are allowed (they become local proposals).
  - Expressions: `edge.hosts[].hostname`, `redirect.to` and `setHeaders` values may use `var.*`, `deployment.id`, `self.host` (never secrets).
  - Prototype scope: proxy + strip prefix + redirect + multiple hosts + `internal` and `none` TLS; ACME and `files` are stretch.
- AGENT GUI ALIAS PORT (user decision): `SiteConfig.agentGuiAliasPort: number | null` (default 8080; null = off). The agent serves ONLY the agent GUI + its API (`/_opm/...`; `/` redirects to `/_opm/app`) on this host port, directly from the agent's own HTTP server (NOT through the Caddy edge). Purpose: the GUI stays reachable when port 80/443 cannot be bound (port in use, no root, broken edge config). Rules: must differ from agentPort, edge.httpPort, edge.httpsPort and every container hostPort; 1024–65535. Access paths to the GUI: (1) `http(s)://<any edge host>/_opm/` (2) `http://<host>:<agentGuiAliasPort>/_opm/` (3) `http://<host>:<agentPort>/_opm/` (agentPort also carries peer traffic). Changing it is a local-editable field (local proposal). Bind failure → alert `gui-alias-port-bind-failed` + activity `agent.port-bind-failed`.
- LOGIN ≠ ACTOR POPUP (user clarification; REFINES "NO SITE LOGIN / WHO IS DOING THIS"): two separate layers in the agent GUI.
  1. **Login** — `/_opm/login`. The person enters the deployment **access key** (the "login cred (common)" of the prompt). The cloud generates it on `/app/deployments/{id}/setup` (shown once, with a "Rotate" button; 24 chars, base32). The cloud keeps only `accessKeyHash` (argon2id) in the deployment doc; agents get the hash with the cloud view (smart distribution, latest fetch wins), and the install token seeds the first hash. `POST /_opm/api/login {accessKey}` → sets the cookie `opm_session` (HttpOnly, SameSite=Strict, Secure on https, **expires 6 h after login**, no sliding renewal). When it expires, every API call returns 401 `session-expired` and the GUI goes to `/_opm/login`. Rate limit: 5 failures / 5 min per IP. Activities: `login.succeeded`, `login.failed` (ip, userAgent), `logout`. Rotation: the old key stops working at a site as soon as that site receives the new hash; sessions that are already open stay valid until their 6 h expiry.
  2. **Actor popup** — after login, on EVERY page load / refresh / new tab / new session the blocking popup asks only for the email ("who is working right now?"), with recently used emails (localStorage) suggested under the input. `POST /_opm/api/actor {email}` (needs a valid `opm_session`) → `actorToken` held in page memory only (lost on refresh → popup again); its expiry = the session expiry. Every write needs both the cookie and `Authorization: Actor <token>`; activities record `actor.email` + sessionId. Activity `actor.declared`. The popup is not authentication.
  - The chip in the header shows the email + "session ends in 4 h 12 m" + "Change person" (opens the popup) + "Log out".
  - agentGuiAliasPort and agentPort GUIs use the same login + popup.
