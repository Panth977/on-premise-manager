# Branches and commits — templates, deployments and agent branches (SPEC §21 #104, #105)

> **§21 #105 (agent 0.14.1) — ONE action: Commit.** There is no server-side working copy and no draft any more: edits
> live in the browser until **Commit** (message required) writes a commit directly; on the agent "uncommitted" means
> ONLY drift. **§15 below supersedes** every working-copy part of this document: D3 (save without a message), D4 (the
> server-side uncommitted count), D9 / D13 for GUI edits (dirty-working-copy refusals of merges in the cloud), §3.2
> (save / discard of the working copy), §3.6 GUI edits (`putWorking`, `working_items`), §2.2 `branch.uncommitted`,
> §7.1 `discard…Working`, §7.2 `putWorking`, and the GUI decisions BR-A1 (Save without a message) and BR-A5 (saved
> edits survive a reload on the agent). Read §15 first.

Status: **BUILT (phase B)**, 2026-10-09 — design below, as-built deviations in §14; **#105 (§15) supersedes the working copy**. Implementation (phase B) starts only after agent 0.13.1 (§21 #103) is
released (ORCH.md completion entry + `agent/latest` = 0.13.1). Target release: agent **0.14.0**.

User request (verbatim):

> "lets rethink the diff in the deployment and agent side.
> first. think of templates and deployments as branches.
> i can create a deployment from existing template/deployment
> i can create a template from exiting deployment/template
> i can see diff between 2 entities & cherry pick what changes i want to bring, and on commit i can merge those changes.
> i can see the commit history in entity.
> (on evey change you must also give commit msg)
> then there are seperate sub branches for each deployment called agent branch.
> you dont have to make any proposal agent is suppose to fetch the latest deployment commit. (latest commit is the proposal)
> in agent UI
> i can see the cloud branch/commit
> i can see my local branch/commit (if there are any kind of change made mannually like file changes or docker img etc,
> then all of them must be set as uncommit, anything not done viya agent. and user is req to first commit the local mannual
> changes before any merge)
> then user can similar to clound merge cloud branch <> local cherrypick no need for proposal and no need for full exact
> merge.
> finally clound will also maintan the local branch, and try to show them in deployments. and then clound can charry pick
> what it wants to take.
> when anyone choose to merge show the deleted services/files/volumes too, so if cloud branch dont have somethings, and
> local one has those thing then loclly on merge it should show the option to remove the extra things it has locally."

**Supersedes** (SPEC §21): #10 (one proposal at a time), the proposal / publish / approve model of §6 `publish`, §9.4, §9.5
(staging of proposals), §14.2, §14.4 (the gate for updates), §18 Diff + local proposals (§7.2 `previewLocalProposal` /
`createLocalProposal`), #94 (Template sync → general compare + cherry-pick), #11 for updates (the 60 s approve wait; it
stays for data restores), and the "Draft" term (§0). Kept: the apply engine (§14.5 steps, snapshot, rollback, readiness,
failure diagnostics), staging of images / files (§9.5 steps 2–6, now for the cloud head), smart distribution (§9.7),
store-and-forward, hold-to-fast-forward (#95) for the intents that remain, volume content + persist-to-cloud (#101).

---------------------------------------------------------------------------------------------------------------------

## 0. Decisions in one page

| # | decision | why |
|---|---|---|
| D1 | Every template, every deployment and every (deployment, site) pair is a **branch** = a linear list of immutable **commits**. | the user's model; linear = no merge commits with two parents to render or reason about |
| D2 | A **commit** = `{ commitId (ULID), branch, seq, parentId, kind, message, author, createdAt, source?, config, hash, catalog?, stats }`. `hash = sha256(JCS(config))` — the existing canonical hashing (`canonical.ts`, Go `internal/canonical`). | reuse; the commit hash = the old revision hash for the same config |
| D3 | Edits go into an uncommitted **working copy** (replaces "draft"). A save WITHOUT a message is allowed into the working copy (it is not history; it is audited). NOTHING reaches history without a message: commit, merge, cherry-pick, revert, reset, drift commit, rollback — each needs a message (1–2000 chars, first line 1–200). | multi-page editing needs a scratch area (a container page + its volume + an edge route are one change); "on every change you must give a commit msg" = every history entry has one |
| D4 | The uncommitted change count = the number of merge ITEMS (§4.1) between the head commit and the working copy. It shows in the sidebar scope header, the change bar, the lists (Templates / Deployments), Overview and the agent GUI. | one counting rule everywhere |
| D5 | A deployment commit IS the release: the **latest commit of the deployment branch is what the sites should run**. No proposal object. Commits on a deployment need `deployment.config.publish` and publish-level validation (the old publish blockers). | "latest commit is the proposal" |
| D6 | Each **site** has its own **agent branch** (a sub-branch of the deployment) on the agent, mirrored to the cloud. Scope of an agent branch = the shared sections (variables, secrets, files) + its own site. Other sites' sections are carried as **reference copies** (for cross-site expressions), refreshed from the merged cloud commit, never shown as items. | per-site merges ("no need for full exact merge") with a well-defined content per site |
| D7 | Nothing changes on a site until a **person on the site merges** in the agent GUI. A merge with runtime effect runs the existing apply engine (pre-update snapshot, start order, readiness, VERIFY, rollback on failure) and creates the merge commit ONLY at COMMIT. A failed merge leaves no commit. | the gate model survives without proposals |
| D8 | No 60 s wait for merges (the commit message + plan review are the deliberate act). The 60 s wait + hold-to-fast-forward (#95) + typed ACCEPT stay for **data restores**: roll back to a snapshot, volume reset / empty / restore, volume content apply. | the user removed proposals; restoring data is still the dangerous act |
| D9 | **Drift** = anything changed on the site NOT via the agent (containers created / changed / removed by hand, image, env, ports, mounts, restart, command, resources, per-site file + secret copies edited, persist-to-cloud volume content). The agent reads Docker + the filesystem, expresses it as config changes = uncommitted items. **Merges are refused while drift or GUI edits are uncommitted** (409 `dirty-working-copy`). | "user is req to first commit the local manual changes before any merge" |
| D10 | Drift that cannot be expressed as config (an edited rendered file, a running edge that differs) is a **drift note**: Discard (re-apply) or Accept (recorded in the next commit, ignored until it changes). | honest: never invent config |
| D11 | **Compare + cherry-pick between ANY two branches** (template ↔ template ↔ deployment ↔ agent branch) is ONE engine (`shared/src/branch.ts`, Go `internal/branch`, shared vectors). Items are field-level (the #94 key grammar, extended). **Deletions** are items: target-only things are offered as "remove from target". | one flow replaces Template sync, Diff/publish, local proposals |
| D12 | **Merge base** = a per-pair record `mergeBases/{sourceKey}` on the target (base config + skipped items), updated on every merge — the #94 `baseSnapshot` / `skipped` mechanism generalised. Fallback: the origin link (created-from). No base → two-way compare (no conflict detection, said so in the UI). | proven by #94, self-contained, no graph search across collections |
| D13 | Merges are computed against the target **head commit** and require a clean working copy (cloud too). Apply picks → validate → commit (one step). | no mixing of half-done edits into a merge commit |
| D14 | Rollback / history repair never rewrites history: **revert a commit** (inverse changes as a new commit, conflicts if later commits touched the same items) and **reset to commit** (a new commit whose config = an older commit's). | rule 7 |
| D15 | Agents < 0.14 keep working: while a deployment has an enrolled site < 0.14 the cloud writes a **compat revision + proposal** (the old publish) for every deployment commit (revId = commitId); old sites approve as before; 0.14 agents answer legacy updates as no-op participants. Per-site "needs agent ≥ 0.14" warnings. | rule 8 |
| D16 | Multi-site: cherry-pick merges are per site. "**Fast-forward all sites**" (agent GUI) = the old coordinated multi-site update (LOCK all → SNAPSHOT all → APPLY in order → VERIFY → COMMIT, rollback all), allowed only for sites that are clean and strictly behind. | keeps the atomic multi-site update for the common "release everything" case |
| D17 | The shared `ui/` package gets `ui/src/branch/` — CommitDialog, MergeView (diff + cherry-pick + conflicts + deletions), HistoryView, ChangeBar count — used by BOTH GUIs through a `BranchHost` adapter (#102 rule). | rule 9 |

---------------------------------------------------------------------------------------------------------------------

## 1. Terms (replace SPEC §0 Draft / Revision / Proposal / Staging / Gate / Update)

- **Branch** — a linear list of commits. `BranchRef` = `{ kind: 'template', templateId }` | `{ kind: 'deployment',
  deploymentId }` | `{ kind: 'agent', deploymentId, siteSlug }`. String key (`branchKey`): `template:<id>`,
  `deployment:<id>`, `agent:<deploymentId>/<siteSlug>`.
- **Commit** — an immutable config snapshot with a message (§2.1). **Head** = the newest commit of a branch.
- **Working copy** — the uncommitted, editable config of a branch (cloud: the template's config fields / the deployment's
  `draft` field — same storage as before, new name; agent: GUI edits + drift).
- **Uncommitted changes** — the items (§4.1) between the head and the working copy (agent: + drift + drift notes).
- **Origin** — the branch + commit a branch was created from ("created from Single@c7").
- **Source / target** — in a compare: the branch whose changes are brought (source) into the branch that gets a new
  commit (target).
- **Agent branch** — the branch of ONE site of a deployment (on the agent; mirrored in the cloud).
- **Cloud head** (agent GUI) — the head commit of the deployment branch, as the site last learned it.
- **Drift** — live changes on the site not made through the agent (§6).
- **Merge** — apply picked items of a source into a target and commit (kinds `merge` when every source change was
  taken, `cherry-pick` otherwise).

---------------------------------------------------------------------------------------------------------------------

## 2. Data model

### 2.1 Shared types (`shared/src/types.ts` + new `shared/src/branch.ts`)

```ts
export type BranchKind = 'template' | 'deployment' | 'agent';
export type BranchRef =
  | { kind: 'template'; templateId: Id }
  | { kind: 'deployment'; deploymentId: Id }
  | { kind: 'agent'; deploymentId: Id; siteSlug: Slug };
/** A point to compare from: a commit of a branch, its head, or (cloud/agent only for its own branch) its working copy. */
export interface BranchPoint { branch: BranchRef; at: 'head' | 'working' | { commitId: Ulid } }

export type CommitKind =
  | 'create'       // first commit of a new branch ("created from X@c" or "empty")
  | 'change'       // working copy committed
  | 'merge'        // every change of the source taken
  | 'cherry-pick'  // some changes of the source taken
  | 'revert'       // inverse of one commit
  | 'reset'        // config = an older commit's config
  | 'drift'        // agent: live changes made outside the agent recorded as config
  | 'rollback'     // agent: roll back to a snapshot (data restore, §14.6)
  | 'migrated'     // created by migrate-i from a pre-0.14 revision / template state
  | 'legacy';      // cloud mirror of a pre-0.14 agent's local proposal / head report

export interface CommitAuthor {
  kind: 'person' | 'system';
  email: string;          // account email (cloud) / declared actor (agent) / "system"
  uid: Uid | null;        // cloud account uid; null on the agent
  name: string | null;    // account name at commit time
  siteSlug: Slug | null;  // agent commits: the site whose GUI was used
}
export interface CommitSource {
  branch: BranchRef;
  commitId: Ulid | null;  // null: a pre-0.14 template state without a commit (migrated base)
  seq: number | null;
  label: string;          // "Single", "Test 02", "Test 02 · site-01"
  picks: string[] | 'all';// item keys taken (≤ 500; more → 'all' or truncated flag)
  skipped: number;        // source changes not taken
  truncated?: boolean;
}
export interface CommitStats { added: number; changed: number; removed: number; keys: string[] /* ≤ 200 */; truncated: boolean }

export interface Commit {
  commitId: Ulid;
  branch: BranchRef;
  seq: number;                  // parent.seq + 1; first = 1
  parentId: Ulid | null;
  kind: CommitKind;
  message: string;              // 1–2000; first line 1–200 = the summary
  author: CommitAuthor;
  createdAt: Ts;
  source: CommitSource | null;  // create (origin), merge, cherry-pick
  revertOf: { commitId: Ulid; seq: number } | null;
  resetTo: { commitId: Ulid; seq: number } | null;
  rollbackOf: { snapshotId: Ulid } | null;
  config: Config;               // templates: secret values null; deployment + agent: plain values (masked in every human read)
  hash: Sha256;                 // sha256(JCS(config))
  catalog: RevisionCatalog | null;  // deployment + agent commits (agents need ImageSpecs offline); null on templates
  catalogHash: Sha256 | null;
  stats: CommitStats;           // vs the parent (items, §4.1)
  data: CommitData;             // extra facts, never config
}
export interface CommitData {
  /** agent drift commits: the drift notes accepted (D10) */
  acceptedDrift?: { fingerprint: string; label: string; detail: string }[];
  /** agent: persist-to-cloud content recorded by this commit (#101 snapshot taken at commit time) */
  volumeContent?: { siteSlug: Slug; volumeSlug: string; treeHash: Sha256; versionId: Ulid | null }[];
  /** migrated: the revision / template version it came from */
  migratedFrom?: { revId?: RevId; seq?: number; proposalStatus?: ProposalStatus; templateVersion?: number };
  /** agent commits: the update run that applied it (null = no runtime change, e.g. drift) */
  updateId?: Ulid | null;
  /** §21 #95 rollback commits: fast-forward use of the restore wait */
  fastForward?: FastForwardUse | null;
}
export type CommitView = Omit<Commit, 'config'> & { config: ConfigView };
export type CommitSummary = Pick<Commit, 'commitId' | 'branch' | 'seq' | 'parentId' | 'kind' | 'message' | 'author' | 'createdAt' | 'source' | 'revertOf' | 'resetTo' | 'hash' | 'stats'>;
export type CommitRef = Pick<Commit, 'commitId' | 'seq' | 'hash' | 'message' | 'author' | 'createdAt' | 'kind'>;

/** Template.branch / Deployment.branch */
export interface BranchState {
  head: CommitRef | null;       // null only between create and its first commit (never visible: create commits at once)
  origin: { branch: BranchRef; commitId: Ulid | null; seq: number | null; label: string; templateVersion?: number } | null;
  commitCount: number;
  uncommitted: number;          // D4, recomputed on every working-copy write and commit
}
```

`Template` keeps its config fields (`variables`, `secrets`, `files`, `sites`) — they ARE the working copy — and its
`version` (= working-copy version, the `ifVersion` cursor). New field `branch: BranchState`. `Deployment.draft` /
`draftVersion` are the working copy / its cursor (API and GUI call it "working copy"); new field `branch: BranchState`.
`Deployment.head` (the legacy reported head) stays, renamed in docs to **legacy head** (compat, §9). `srcTemplate` is
frozen after the migration (read by nothing; kept for one release as rollback safety, removed in 0.15).

**Merge base record** (`MergeBase`, stored per target branch, keyed by the source branch key):

```ts
export interface MergeBase {
  sourceKey: string;            // branchKey of the source
  source: BranchRef;
  sourceCommitId: Ulid | null;  // the source commit the base was taken at (null: migrated template state)
  sourceSeq: number | null;
  targetCommitId: Ulid;         // the target commit written by that merge (or the create / migration commit)
  base: Config;                 // = source config at sourceCommitId (secret VALUES replaced by their sha256, never stored twice)
  skipped: MergeSkip[];         // source changes not taken (still takeable until the source changes them again)
  updatedAt: Ts;
  updatedBy: string;
}
export interface MergeSkip { key: string; theirsHash: string; sourceSeq: number | null; skippedAt: Ts; skippedBy: string }
```

### 2.2 Documents (cloud)

| path | type | doc id | notes |
|---|---|---|---|
| `templates/{templateId}` | `Template` + `branch` | auto | config fields = working copy |
| `templates/{templateId}/commits/{commitId}` | `Commit` | ULID | `_config` JSON string (secret values null); `catalog` null; immutable (`create()`) |
| `templates/{templateId}/mergeBases/{sourceKey}` | `MergeBase` | branchKey with `/` → `~` | `_base`, `_skipped` JSON strings |
| `deployments/{id}` | `Deployment` + `branch` | auto | `draft` = working copy |
| `deployments/{id}/commits/{commitId}` | `Commit` | ULID | `_config` (plain secrets), `_catalog` JSON strings; immutable |
| `deployments/{id}/mergeBases/{sourceKey}` | `MergeBase` | | incl. `agent:<id>~<site>` for picks from an agent branch, `template:<id>` from the migrated srcTemplate |
| `deployments/{id}/agentBranches/{siteSlug}` | `AgentBranchDoc` | slug | the cloud mirror of the site's branch (§2.4) |
| `deployments/{id}/agentBranches/{siteSlug}/commits/{commitId}` | `Commit` | ULID | pushed by the agent (verified), `_config` + `_catalog` |
| `deployments/{id}/revisions/{revId}` · `proposals/{revId}` | unchanged | | legacy + compat only (§9); never written for 0.14 sites |

Indexes: `commits` (seq desc) on the three commit collections (collection-group index `commits` (seq desc) too, for the
compare pickers); `mergeBases` none (point reads). A commit document > 850 KB (config + catalog JSON) stores the JSON in
Storage `commits/<hash hex>.json` (content-addressed) and keeps `_configRef` / `_catalogRef` instead (never seen in prod
data — Single ≈ 120 KB — but the 900 KB limit is hard).

`AgentBranchDoc`:
```ts
export interface AgentBranchDoc {
  siteSlug: Slug;
  head: CommitRef | null;        // newest pushed commit (0.14) or the legacy head (pre-0.14, legacy: true)
  legacy: boolean;               // the site runs agent < 0.14 (head mirrors AgentDoc.headRevId)
  mergedFrom: { commitId: Ulid; seq: number } | null;  // newest deployment commit merged into this site (source of its newest merge/cherry-pick with source = the deployment)
  uncommitted: { working: number; drift: number; notes: number; scannedAt: Ts | null } | null; // heartbeat
  pushedAt: Ts | null;
  agentVersion: string | null;
}
export interface AgentBranchView extends AgentBranchDoc {
  behind: number;     // deployment commits with seq > mergedFrom.seq (0 when mergedFrom = head)
  ahead: number;      // agent commits after the newest merge from the deployment whose items are not in the deployment head (§5.4)
  differs: number;    // items in scope that differ between deployment head and agent head (§4.1)
  needsAgent014: boolean;
}
```

### 2.3 Agent SQLite (schema +1; `backup/` copy before the migration, §19.1 rule)

| table | columns | rules |
|---|---|---|
| `commits` | commitId PK, seq UNIQUE, parentId, kind, message, author JSON, createdAt TEXT, source JSON, revertOf JSON, resetTo JSON, rollbackOf JSON, config JSON, hash, catalog JSON, catalogHash, stats JSON, data JSON, pushedAt TEXT | the site's branch; linear; never deleted while a snapshot refers to it (commitId is the snapshot `revId`) |
| `cloud_commits` | commitId PK, seq, parentId, kind, message, author JSON, createdAt, source JSON, config JSON, hash, catalog JSON, catalogHash, fetchedAt, via (cloud / peer:<slug>) | deployment-branch commits fetched for compare / staging / peers (head + the 20 newest metas; configs only of fetched ones; pruned to 50) |
| `working_items` | key PK, has INTEGER, value JSON, updatedAt TEXT, updatedBy | GUI form edits as items (§4.1 keys) on top of the head; cleared per key on commit / discard |
| `drift` | id PK (=1), scannedAt TEXT, headCommitId, report JSON | the latest drift scan (§6.4) |
| `drift_accepts` | fingerprint PK, commitId, acceptedAt, acceptedBy | accepted drift notes (D10) |
| `merge_bases` | sourceKey PK, sourceCommitId, sourceSeq, targetCommitId, base JSON, skipped JSON, updatedAt, updatedBy | as the cloud `MergeBase` (the agent's base vs the deployment branch) |
| `merge_runs` | runId PK (=updateId), plan JSON (picks, source, message, target commit draft), state, startedAt, finishedAt, error | a merge / commit-with-apply in progress; the commit row is written only at COMMIT |

kv: `branch` = `{ headCommitId, seq, hash }` (= the applied head; kv `head` stays as an alias for the engine and
snapshots), `cloudHead` = the deployment head meta from the cloud view.
Kept read-only: `revisions` (old history, migrated into `commits`, §8.3), `proposals`, `staging` (legacy participant mode,
§9.3).

### 2.4 What each branch contains

| branch | config | scope (items shown in compare / counted) | catalog |
|---|---|---|---|
| template | full `Config`, secret values null | everything except secret values | null (validation uses the live catalog) |
| deployment | full `Config` with plain secrets | everything | built at commit (as publish did: services, ImageSpecs, 404 pages) |
| agent `<site>` | full `Config`: shared sections + own site = **owned**; other sites = **reference copies** (D6) | variables, secrets, files, `sites/<own site>/…` | the merged catalog (source catalog entries for picked containers ∪ own; local images, §6.2) |

Reference copies: when the agent merges deployment commit C, every OTHER site section in the agent commit is replaced by
C's (no item, listed as one info line "references to other sites updated from cloud commit 12"). A site removed from the
deployment is offered as ONE item `sites/<own>` "remove this site from the branch" (stops and removes every container
of the site, edge back to the default edge; data folders stay).

---------------------------------------------------------------------------------------------------------------------

## 3. Flows

### 3.1 Create from

`POST /v1/templates { label, icon?, description?, from?: BranchPoint }` and `POST /v1/deployments { …, from?: BranchPoint }`
(`from` absent = empty). The new branch gets commit seq 1, kind `create`, message (editable, required, default prefilled
by the dialog) `created from <label>@<seq>` (or `created empty`), `source` = the origin with `picks: 'all'`,
`branch.origin` = the same, and a `mergeBases/<sourceKey>` record (base = the origin config) so the first compare with the
origin is 3-way.

| new ← from | config rule |
|---|---|
| template ← template | deep copy (secret values null) |
| template ← deployment / agent branch | deep copy; secret values dropped (templates hold none), generate rules kept; variable values become the template defaults |
| deployment ← template | today's create: deep copy, secrets with `generate` get a value, access key generated |
| deployment ← deployment / agent branch | deep copy; secrets with `generate` get a NEW value; secrets without `generate` copy the source value only when "Copy secret values without a generate rule" is ticked (default off) AND the caller has `deployment.secrets.read` on the source, else value null (required → shown "not set"); access key generated; sites are not enrolled (new install commands) |

Permissions: templates.write / deployments.write (as today) + read on the source (templates.read,
deployment.config.read). Activities / audit: `branch.created { from, commitId }` (`_cloud` on deployments) +
`template.created` / `deployment.created` with `data.from`.

### 3.2 Edit → working copy → commit (cloud)

- The existing editors keep writing the working copy (`PATCH /v1/templates/{id}` with ifVersion; `PUT …/draft/{section}`
  and `…/draft/sites/{site}` with ifRevision) — no message, no history, audited as today. Responses gain
  `uncommitted: number`.
- **Commit…** (change bar / Overview / History): `POST /v1/templates/{id}/commits { message, ifVersion }` /
  `POST /v1/deployments/{id}/commits { message, ifRevision }`. Checks: working copy differs from head (else 422
  `V_NO_CHANGE`); template: `validateConfig(mode 'template')` errors block; deployment: `mode 'publish'` blockers block
  (D5; the old publish blockers incl. agent-version needs as warnings) → commit (kind `change`), catalog built
  (deployment), `branch.head` updated, `uncommitted` 0, deployment `agentEpoch+1` (the sites see the new cloud head),
  compat revision + proposal when needed (§9.1). Permissions: templates.write / deployment.config.publish (+ the
  secrets permission is NOT needed: values in the working copy were already written by someone allowed to).
- **Discard** working copy: `POST …/working/discard { ifVersion | ifRevision }` → working copy := head config (the old
  "Copy head into draft"). Per-item discard is done in the GUI by editing back.
- Image **Rename everywhere** (§6.1 patchImage) rewrites template WORKING COPIES (no commit; their uncommitted count goes
  up; the impact dialog says "templates get uncommitted changes — commit them"). Deployments: unchanged rule (not
  rewritten).

### 3.3 Compare, cherry-pick, merge (cloud, any two branches)

1. **Compare** `POST /v1/branches/compare { source: BranchPoint, target: BranchRef }` → `MergePlan` (§4.3). Source `at`
   may be `head` or a commit; `working` only when source = target's own branch is not allowed (use Commit). Target is
   always its head.
2. The user ticks items (field-level), resolves conflicts (take source / keep target / custom scalar), and may fill
   `values` (required variables a new item brings without a value, as #94) and, for a deployment target,
   `secretValues` (plain values for required secrets the merge adds without one and without a generate rule — a
   deployment commit is checked at `publish` level, so such a merge cannot go through without them; needs
   deployment.secrets.write; never echoed back; recorded as `CommitData.secretValues.set`; refused for a template).
3. **Preview** `POST /v1/branches/merge/preview { source, target, targetHead, picks, values?, secretValues? }` → resulting config
   (masked), `diffConfigs(head → result)`, validation of the target kind (template: `template`, deployment: `publish`
   blockers + warnings, plus agent-version warnings), unresolved conflicts, missing values, `requires` hints (§4.4).
   Dry run, not audited (AUDIT_EXEMPT).
4. **Merge** `POST /v1/branches/merge { …, message }`: refused when the target working copy is dirty (409
   `dirty-working-copy { uncommitted }`), the target head moved (409 `stale { head }`), the source commit moved for a
   `head` point (409 `source-changed`), conflicts without a choice (422 `V_MERGE_UNRESOLVED`), bad picks (422
   `V_MERGE_CHOICE`), validation errors (422). Writes in one transaction: commit (kind `merge` when every source change
   — clean, conflict and skipped — was taken, else `cherry-pick`; `source.picks` = keys), working copy := result,
   `mergeBases/<sourceKey>` := `{ base = source config, skipped = source changes not taken }` (§4.5), `branch.head`,
   deployment `agentEpoch+1` + compat proposal. Activities `branch.merged` / `branch.cherry-picked` + audit.

Permissions: read on both; write on the target = templates.write | deployment.config.publish; secret VALUE items need
deployment.secrets.read on the source and deployment.secrets.write on the target (403 otherwise; the plan shows them
locked with the reason).

### 3.4 History, revert, reset (every branch)

- `GET …/commits?cursor&limit` (newest first, `CommitSummary`), `GET …/commits/{commitId}` (`CommitView` + `changes` =
  items vs its parent), compare any two commits of any branches = `POST /v1/branches/compare` with two commit points
  (read-only mode of the MergeView: `diffOnly: true`, no target writes).
- **Revert** `POST /v1/branches/revert { branch, commitId, message, ifHead, choices? }`: plan(base = commit.config, theirs =
  parent config, ours = head) → the clean items are taken, conflicts (a later commit touched the same item) need choices
  → commit kind `revert`, `revertOf`. Reverting a `create` commit → 422 (nothing before it).
- **Reset to commit** `POST /v1/branches/reset { branch, commitId, message, ifHead }`: commit kind `reset` with config =
  that commit's config (secrets of the older commit's values; the dialog lists every item that changes, incl. removals).
- Both need a clean working copy; deployment ones raise agentEpoch (the sites see a new cloud head and merge it like any
  other: D7).

### 3.5 Deployment → sites (no proposals)

- `CloudView.branch = { head: CommitRef & { catalogHash }, recent: CommitRef[] (10 newest) }` replaces `proposals` for
  0.14 agents (`proposals` stays filled for old agents, §9). Every head change = `agentEpoch+1`.
- The agent fetches the head commit (`GET /v1/agent/commits/{commitId}`, plain secrets, `_cloud` activity
  `secret.fetched`), or from a peer (`GET /_opm/peer/cloud-commits/{commitId}`) when it has no cloud route, stores it in
  `cloud_commits`, and **stages** it automatically for its own site (§9.5 steps 1–6 unchanged: validate, images, docker
  load, files, 404 page, dry run) → the GUI shows "cloud commit 14 · staged ✓" so a merge applies fast. Staging is
  advisory: the merge itself stages its RESULT again (validate + dry run; blobs are already there).
- Store-and-forward: the cloud head reaches offline sites through peers (view exchange + peer commit fetch) and the
  browser tunnel exactly as revisions did.

### 3.6 Agent: commit, merge, fast-forward all (agent GUI, §7)

- **Uncommitted** = `working_items` (GUI edits) + drift items + drift notes (§6). The local change bar shows the count;
  **Review** lists them (MergeView in `diffOnly` mode, sources "edited here" / "changed on the server"), **Commit…** opens
  CommitDialog: tick items (default all), message → `POST /_opm/api/branch/commit { message, keys, accept: fingerprints }`.
  Drift items commit without touching the runtime (they ARE live already); edited items commit through a single-site
  apply (§5.3) — the commit row is written at COMMIT; drift-only commits are written at once (kind `drift`; mixed =
  kind `change` with `data.updateId`). **Discard** per item: an edit → removed from `working_items`; a drift item / note
  → re-apply the branch value (single-site apply of the head config: recreates that container / rewrites that file).
- **Merge from cloud** (`/_opm/app/{site}/branch/merge`): refused with 409 `dirty-working-copy` while anything is
  uncommitted ("Commit or discard the N local changes first", link to Review). Plan = compare(source = cloud head or any
  cached cloud commit, target = own head) restricted to the scope (D6). Deletions: everything the local branch has that
  the cloud commit lacks (containers committed from drift, local volumes, files, routes, env rows, presets …) is an item
  "remove locally" (D11). Pick → preview (agent validation `mode 'agent'` + staging dry run of the result) → message →
  **Merge & apply** → single-site update run (§5.3) → commit kind `merge` / `cherry-pick` at COMMIT + merge base updated.
- **Fast-forward all sites** (Branch page, shown when ≥ 2 sites are behind the cloud head): lists every site with its
  state; selectable only when the site is reachable, ≥ 0.14, has no uncommitted changes and no local commits since its
  last merge (strictly behind). One message → coordinated multi-site update (§5.3, `plan.targets` per site) → each site
  commits kind `merge` (source = the cloud commit, picks 'all') with the same message.
- `checkLocalEdit` keeps restricting GUI EDITS (no add / remove / rename of containers, definitions cloud only); drift
  commits and merges are not edits and may add / remove containers.

### 3.7 Cloud mirror of agent branches (store-and-forward)

- Every agent commit is queued in the outbox and pushed `POST /v1/agent/branch/commits { commits: Commit[] }` (≤ 20 per
  call, oldest first; direct → relay → tunnel; idempotent by commitId). The cloud verifies: signer = branch site,
  `hash` = sha256(JCS(config)), `catalogHash`, `seq = parent.seq + 1` with the parent stored (or seq 1 with parentId
  null / the first pushed commit after a migration, which carries `data.migratedFrom`), no fork (same seq, other id → 409
  `conflict`, never happens: one writer). Stores `agentBranches/{site}/commits/{id}` + updates the AgentBranchDoc;
  `_cloud` activity `agent-branch.pushed { siteSlug, commitIds[≤20], head }`.
- Heartbeat `branch: { head: CommitRef | null, mergedFrom, uncommitted: { working, drift, notes, scannedAt } }` → the
  AgentBranchDoc (`uncommitted`, live even when nothing is committed).
- Deployment → **Sites** page and a new **Agent branches** card (Overview) + page `…/branches`: per site head (seq,
  message, author, time), behind / ahead / differs, uncommitted drift ("3 changes made on the server, not committed —
  commit them in the agent GUI"), agent version + "needs agent ≥ 0.14", buttons **Compare** (→ compare with source =
  `agent:<site>` head, target = deployment) and **History** (the mirrored commits).
- Cherry-picking FROM an agent branch into the deployment = the normal merge (§3.3); its base record is
  `mergeBases/agent:<id>~<site>`. Uncommitted drift is not pickable (it is not on the branch yet).

---------------------------------------------------------------------------------------------------------------------

## 4. The compare / merge engine (`shared/src/branch.ts`; Go `agent/internal/branch`)

### 4.1 Items (keys)

The #94 key grammar (template-sync.ts), extended. Lists are keyed by slug / name, never position. A site / container
present on both sides is compared field by field; present on one side only = ONE whole item (a rename = remove + add).

| key | value compared (canonical JSON) | notes |
|---|---|---|
| `variables/<slug>` | definition without the value | |
| `variables/<slug>/value` | the value | a normal item (no more "default" kind); template ↔ deployment labels it "template default"; never pre-ticked unless fast-forward |
| `secrets/<slug>` | definition + generate rule | |
| `secrets/<slug>/value` | `{ sha256 }` of the plain value / null | only when BOTH sides hold values (deployment, agent); masked everywhere ("differs" / "same" / "not set"); taking it copies the plain value server-side |
| `files/<slug>` | metadata + content sha256 | content diff = TextDiff of the two blobs |
| `sites/<s>` | whole site | one side lacks it |
| `sites/<s>/settings/<label\|host\|agentPort\|agentGuiAliasPort\|notFoundPageId\|depends>` | scalar / list | |
| `sites/<s>/edge/<httpPort\|httpsPort\|maxBodyBytes>` | scalar | |
| `sites/<s>/edge/hosts/<hostname>` · `…/edge/routes/<routeId>` | object | |
| `sites/<s>/volumes/<v>` | the volume without its own content (`seed` of kind tree/single → `"content"` marker) | |
| `sites/<s>/volumes/<v>/content/<path>` · `…/content/<dir>/` | tree entry `{ sha256, size, mode, render }` / empty folder | NEW: per-file cherry-pick of starter content (#101 trees) |
| `sites/<s>/volumes/<v>/seed` | non-tree seeds (archive, legacy files/file) whole | |
| `sites/<s>/devtools/<presetSlug>` | preset (static values masked for display, compared raw, #103) | |
| `sites/<s>/containers/<c>` | whole container | one side lacks it |
| `sites/<s>/containers/<c>/<image\|enabled\|command\|entrypoint\|restart\|resources\|healthcheck\|depends\|ports\|volumes>` | field (`image` = serviceSlug + version) | |
| `sites/<s>/containers/<c>/env/<NAME>` | value string / null / `{ secretRef }` | |

`itemize(config, { scope?: siteSlug, secretValues: 'hash' | 'none' })`; the agent scope = shared sections + `sites/<own>`.
`uncommittedCount(head, working)` = number of keys whose slots differ.

### 4.2 Kinds (3-way when a base exists)

Slots B (base), T (source = theirs), O (target = ours); `has` + canonical value. Items with T = O are omitted.

| condition | kind | default | UI label (+ action when ticked) |
|---|---|---|---|
| no base, T only | `add` | off | "only in source" → add to target |
| no base, O only | `remove` | off | "only in target" → **remove from target** |
| no base, both, differ | `change` | off | "differs" → take source |
| O = B, T ≠ B | `theirs` (clean) | off (on in fast-forward) | "changed in source" (added / changed / removed) → take |
| T = B, O ≠ B | `ours` | off | "changed in target only" → take source = revert the target's change; if T lacks it: **remove from target** ("added in target") |
| T ≠ B, O ≠ B, T ≠ O | `conflict` | — (choice required) | base / target / source side by side: Keep target · Take source · Custom (scalars) |
| `theirs` / `conflict` whose `templateSyncHash(T)` (renamed `itemHash`) equals a `MergeSkip.theirsHash` | `skipped` | off | "skipped earlier" → still takeable |

**Fast-forward** = every item is `theirs` / `add` (no `ours`, `conflict`, `remove`, `change`): the MergeView pre-ticks all
and offers "Merge all". Deletions (`remove`, and `ours` / `theirs` items whose taken side lacks the thing) are listed in
their own group **"Removed by this merge"** with the exact effect (container stopped + removed, data folder kept; file
copy deleted; route removed …) so nobody removes something by accident.

### 4.3 MergePlan

```ts
export interface MergeItem {
  key: string; section: DiffSection | 'settings'; siteSlug: Slug | null; label: string;
  kind: 'add' | 'remove' | 'change' | 'theirs' | 'ours' | 'conflict' | 'skipped';
  effect: 'add' | 'change' | 'remove';          // what taking the source does to the target
  base: MaskedSlot | null; target: MaskedSlot; source: MaskedSlot;   // MaskedSlot { has, value } (secrets masked)
  preselect: boolean; scalar: boolean;
  requires: string[];                          // keys this item depends on in the result (§4.4)
  locked: string | null;                       // permission / agent-scope reason; null = pickable
  blob?: { before: Sha256 | null; after: Sha256 | null; base?: Sha256 | null }; // files + volume content text diffs
  note?: string;                               // e.g. VOLUME_CONTENT_CHANGED_NOTE, DEVTOOL_VALUE_CHANGED_NOTE, "data folder is kept"
}
export interface MergePlan {
  source: { point: BranchPoint; label: string; commit: CommitRef };
  target: { branch: BranchRef; label: string; head: CommitRef; workingClean: boolean; uncommitted: number };
  base: { kind: 'record' | 'origin' | 'none'; sourceCommitId: Ulid | null; label: string };
  items: MergeItem[]; counts: Record<MergeItem['kind'], number>; fastForward: boolean;
  references?: { updatedSites: Slug[] };       // agent target: reference copies that the merge refreshes (D6)
  diffOnly: boolean;                           // compare of two commits / a read-only target
}
export interface MergeChoice { key: string; take: 'source' | 'target' | 'custom'; value?: unknown }
```

### 4.4 Apply + dependencies

`applyMerge(target: Config, plan, choices, values?) → Config`: for every `take: source` set the source slot (remove when
absent; list additions go after their predecessor in the SOURCE order, else at the end), `custom` = the scalar.
Secret value items copy the plain value (server side / agent side only). Variables NEW to the target with no value take
`values[slug]` (missing required → listed in preview `missing`). `requires` (computed from the RESULT's references):
a route → its container (+ port row), a container → its image in the catalog + its depends + declared volumes it mounts
+ files / secrets it mounts / references (`secretRef`, `{{ secret.x }}`, `{{ var.x }}`), a volume content entry → its
volume. The MergeView ticks requirements automatically with an undo toast ("also took sites/app/containers/api — the
route needs it"); unticking a requirement unticks its dependants. Anything left inconsistent is caught by validation.

### 4.5 Base selection and update

For target T and source S (both `BranchRef`), in order:
1. `T/mergeBases/<key(S)>` exists → base = its `base`, skipped = its `skipped`.
2. T.origin is S → base = S's config at the origin commit (stored as the mergeBase record at create, so this is case 1;
   kept as a rule for migrated origins without a commit: base = the migrated `_srcBase`).
3. S.origin is T (S was created from T@c) → base = T's config at c.
4. Agent branch ↔ its deployment: the agent keeps `merge_bases['deployment']`; the cloud keeps
   `deployments/{id}/mergeBases/agent:…` for the reverse direction; first time: the agent branch's create / migrated
   commit (= a deployment commit) is the base.
5. Else no base (two-way; the MergeView says "no common history — every difference is shown, conflicts cannot be told
   apart").

After a merge from S@c into T: `T/mergeBases/<key(S)>` := `{ base: S@c config, sourceCommitId: c, skipped: every
theirs / conflict / skipped item NOT taken (key + itemHash(T)) plus the existing records whose hash still matches }`
(the #94 rule (e)). `ours` / `remove` items are not recorded (they are the target's own state).

### 4.6 Revert / reset

`revertPlan(commit, parent, head)` = plan with B = commit.config, T = parent.config, O = head.config (clean = undo,
conflict = a later commit touched it). `resetPlan(older, head)` = two-way plan with every item taken. Both reuse
MergeView (target = own branch).

### 4.7 Vectors

`shared/testdata/branch-vectors.json` (generator `shared/scripts/gen-branch-vectors.mjs`): itemize (keys + canonical
values per section, agent scope), plan kinds (every row of §4.2 × every key family, skip records, fast-forward, no
base), applyMerge results (+ list order, removals, custom scalars, secret value copy, requires), revert / reset, base
update (skipped rules), uncommittedCount, commit hash. Go `internal/branch` passes the same file (`TestVectors`).

---------------------------------------------------------------------------------------------------------------------

## 5. Agent engine changes

### 5.1 Branch state + migration at start (0.14 first start)

- `revisions` → `commits` (same ids: commitId = revId; seq, parentId, config, catalog, hash, createdAt, author = actor,
  message = summary or `migrated revision <seq>`, kind `migrated`, `data.migratedFrom { revId, seq }`), only the applied
  chain (walk from kv `head` via parentRevId). kv `branch` := head. `merge_bases['deployment']` := `{ sourceCommitId: head
  revId, base: head config }` (the cloud migration gives the deployment branch the same ids, §8, so the agent is exactly
  "merged up to" its head).
- Open LOCAL proposals (origin agent, not terminal, parent = head) → `working_items` = items(head → proposal config)
  (author = the proposal's actor), proposal status `superseded` locally ("became uncommitted changes in 0.14").
  Other open proposals are left as they are (legacy participant, §9.3).
- The migrated commits are pushed to the cloud mirror (first push carries `data.migratedFrom`).

### 5.2 Drift scan (§6) — loop every 60 s, on Docker events (container create / destroy / rename / die with a non-OPM
actor, debounced 5 s), on GUI "Rescan", and before every merge / commit. Skipped while the site holds an update lock.

### 5.3 Apply = the existing engine, per site

`UpdatePlan` gains `targets: Record<Slug, { revision: Revision; fromRevId: RevId | null }>` (0.14 plans; `revision` +
`fromRevId` stay for legacy plans) and `kind: 'update' | 'rollback' | 'merge'`. A commit is turned into a `Revision`
for the engine (`revId = commitId`). Single-site merge / commit-with-edits / discard = a plan with `sites = [self]`,
coordinator = self; steps LOCK → SNAPSHOT (pre-update, §13.2 rules) → APPLY → VERIFY → COMMIT (writes the commit row, kv
`branch`, merge base, outbox push, activity) or ROLLBACK (no commit, `merge_runs.state = failed`, activity
`branch.merge-failed` with the failure diagnostics of §15.4). Fast-forward all = `sites` = the selected sites, each with its
own target. LOCK checks `head = targets[site].fromRevId` per site. Mixed meshes: a 0.14 coordinator never includes a
< 0.14 site (the GUI does not offer it).

### 5.4 Ahead / behind / differs (agent + cloud, same rules)

- behind = number of deployment commits with seq > `mergedFrom.seq` (the newest deployment commit used as the source of a
  merge / cherry-pick / fast-forward / create / migration on the agent branch).
- ahead = agent commits after that merge whose items are not equal in the deployment head (computed on the cloud mirror
  from `stats.keys`; on the agent from the cached cloud head).
- differs = items in scope between the deployment head and the agent head (= what a full merge would change).

---------------------------------------------------------------------------------------------------------------------

## 6. Drift detection (Go `agent/internal/agent/drift.go` + `internal/driver` helpers)

### 6.1 Inputs

Head commit config + catalog, the expected container specs (§15.2 — the SAME builder the apply uses: resolved env,
mounts with file sha256, ports, restart, resources, command / entrypoint, image config digest), `GET /containers/json?all=1`
(every container, not only labelled ones) + inspect of each candidate, the site network members, the per-site file and
secret copies (sha256 of the bytes on disk), the running edge config hash (`edge.Runtime` holds the last loaded config),
and the #101 sync state of persist-to-cloud volumes (live fingerprint / tree vs the last snapshot).

### 6.2 Matching and mapping (live → config)

Candidates: containers labelled `opm.deployment=<dep>` + `opm.site=<site>` (managed), plus UNLABELLED containers attached
to the site network `opm_<dep>_<site>` or named exactly like a config container (hand-made); `-failed-` copies, containers
of other deployments and containers listed in agent.json `driftIgnoreContainers` are never candidates.
A managed container whose recomputed live spec hash = `opm.spec_hash` = the expected hash is clean (fast path).

| live fact | config change (item) |
|---|---|
| config container missing from Docker (no labelled and no same-named container) | `sites/<s>/containers/<c>` removed ("removed by hand") |
| unlabelled container named `<c>` replacing config container `<c>` | field items of `<c>` below ("recreated by hand at <time>") |
| unknown container on the site network | `sites/<s>/containers/<slug(name)>` added: image (below), env (non-image-default values), ports (bindings mapped to image port names, `p<port>` for unknown), mounts (binds under `<agentHome>/volumes/<v>` → volume source; others → host source), restart, command / entrypoint when ≠ image |
| image id ≠ expected | `…/image`: config digest found in the head catalog / `cloud_commits` catalogs → that `serviceSlug:version`; else a **local image** catalog entry `<serviceSlug>:local-<12 hex of the config digest>` (ImageSpec from `docker image inspect`: config digest, layers' diff ids, env, exposed ports, volumes, entrypoint, cmd, workdir, user; `source.host` "local", RepoTags / RepoDigests as `reference` hint). A local image cannot be picked into the cloud until a cloud image with the same config digest exists ("register <reference> in the cloud first") |
| env NAME differs from the expected resolved value | `…/env/<NAME>` → literal live value (an expression / variable reference becomes a literal: the review says "was {{ var.x }} (= a), now a literal") |
| env NAME whose source is a secret differs | NOT a literal (never write a secret into config): `secrets/<slug>/value` changed (masked) when every container using the secret shows the same new value; else a drift note |
| extra / missing env NAME | `…/env/<NAME>` added / removed (an image default that disappeared → note: cannot unset an image default) |
| port bindings | `…/ports` (rows rebuilt: published host port / host ip; an unknown container port → note) |
| binds / mounts | `…/volumes` (rows rebuilt by mount target: volume / host / file / secret by source path) |
| restart policy, nano cpus / memory, cmd / entrypoint | `…/restart`, `…/resources`, `…/command`, `…/entrypoint` |
| per-site File copy edited (render none) | `files/<slug>` content := new blob (bytes stored as a local blob of kind file, smart-distributed) |
| per-site File copy edited (render expressions) | **note** (cannot be un-rendered): Discard (re-render) / Accept, or "take the edited bytes as a new File content with render none" (explicit choice in the review: becomes the `files/<slug>` item) |
| secret file `<agentHome>/secrets/<slug>` edited | `secrets/<slug>/value` (masked) |
| running edge ≠ built from the head | **note** (the edge is owned by the agent; only a crash recovery can cause it): Discard (reload) / Accept |
| persist-to-cloud volume: live ≠ committed content | **§21 #117** (supersedes the `/live` item): one content item per changed file (`sites/<s>/volumes/<v>/content/<path>`, origin `volume-content`) — the live tree is the volume's seed in the live config; committing them writes a drift commit. Never a merge blocker: a merge takes the live tree as "ours" (files both sides changed are conflicts). Auto-commit never takes them |
| container stopped / restarting, restart count, health | NOT drift (runtime state; the Status page shows it) |
| non-persist volume content, logs, network aliases, labels | NOT drift |

### 6.3 Output

```ts
export interface DriftItem extends MergeItem { origin: 'docker' | 'file' | 'secret-file' | 'volume-content'; detail: string; observedAt: Ts }
export interface DriftNote { fingerprint: string; key: string | null; label: string; detail: string; actions: ('discard' | 'accept' | 'take-bytes')[] }
export interface DriftReport { scannedAt: Ts; headCommitId: Ulid; items: DriftItem[]; notes: DriftNote[]; liveConfig: ConfigView; localImages: string[] }
```
`liveConfig` = head config with every item applied; `uncommitted` (agent) = items(head → liveConfig ⊕ working_items),
an item both drifted and edited shows "edited here (the server differs)" and the edit wins on commit.

### 6.4 Activities

`drift.detected { siteSlug, items, notes, keys[≤50] }` (only when the set of keys / fingerprints changes),
`drift.committed { commitId, keys }`, `drift.discarded { keys }`, `drift.accepted { fingerprints, commitId }`.

---------------------------------------------------------------------------------------------------------------------

## 7. APIs

### 7.1 Cloud (`CLOUD_API`, new rows; § = audit)

| op | method | path | permission | body → response | activities / audit |
|---|---|---|---|---|---|
| `listTemplateCommits` | GET | `/v1/templates/{templateId}/commits` ?cursor&limit | templates.read | → `PageResponse<CommitSummary>` | — |
| `getTemplateCommit` | GET | `/v1/templates/{templateId}/commits/{commitId}` | templates.read | → `CommitDetail { commit: CommitView, changes: MergeItem[] }` | — |
| `commitTemplate` | POST | `/v1/templates/{templateId}/commits` | templates.write | `{ message, ifVersion }` → `Commit` (201) | audit template.committed |
| `discardTemplateWorking` | POST | `/v1/templates/{templateId}/working/discard` | templates.write | `{ ifVersion }` → `{ version }` | audit template.working-discarded |
| `listDeploymentCommits` / `getDeploymentCommit` | GET | `/v1/deployments/{id}/commits[/{commitId}]` | deployment.config.read | as templates (masked) | — |
| `commitDeployment` | POST | `/v1/deployments/{id}/commits` | deployment.config.publish | `{ message, ifRevision }` → `Commit` (201) | branch.committed, audit deployment.committed |
| `discardDeploymentWorking` | POST | `/v1/deployments/{id}/working/discard` | deployment.config.write | `{ ifRevision }` → `{ draftVersion }` | working.discarded, audit |
| `listAgentBranches` | GET | `/v1/deployments/{id}/agent-branches` | deployment.config.read | → `ListResponse<AgentBranchView>` | — |
| `listAgentBranchCommits` / `getAgentBranchCommit` | GET | `/v1/deployments/{id}/agent-branches/{siteSlug}/commits[/{commitId}]` | deployment.config.read | masked | — |
| `compareBranches` | POST | `/v1/branches/compare` | read on both | `{ source: BranchPoint, target: BranchRef \| BranchPoint, diffOnly? }` → `MergePlan` | — (AUDIT_EXEMPT) |
| `previewMerge` | POST | `/v1/branches/merge/preview` | read on both | `MergeRequest` → `MergePreview { result: ConfigView, changes, validation, unresolved, missing, warnings }` | — (AUDIT_EXEMPT) |
| `merge` | POST | `/v1/branches/merge` | read source + write target (+ secrets perms) | `MergeRequest & { message }` → `Commit` (201) | branch.merged / branch.cherry-picked, audit {template\|deployment}.merged |
| `revertCommit` | POST | `/v1/branches/revert` | write on the branch | `{ branch, commitId, message, ifHead, choices? }` → `Commit` | branch.reverted, audit |
| `resetToCommit` | POST | `/v1/branches/reset` | write on the branch | `{ branch, commitId, message, ifHead }` → `Commit` | branch.reset, audit |
| `createTemplate` / `createDeployment` | POST | (existing) | (existing) + read on `from` | `+ from?: BranchPoint, message?, copySecretValues?` | branch.created |
| `getAgentCommit` | GET | `/v1/agent/commits/{commitId}` | site key | → `{ commit: Commit }` (plain) | secret.fetched |
| `listAgentCloudCommits` | GET | `/v1/agent/commits` ?limit | site key | → `ListResponse<CommitRef>` | — |
| `pushAgentBranch` | POST | `/v1/agent/branch/commits` | site key (branch site = signer, or relayed) | `{ commits: Commit[] }` → `{ stored: Ulid[], head }` | agent-branch.pushed |

`MergeRequest = { source: BranchPoint, target: BranchRef, targetHead: Ulid, sourceCommit: Ulid, choices: MergeChoice[], values?: Record<slug, VariableValue> }`.
`write on target` = templates.write (template), deployment.config.publish (deployment). Agent branches are written only by
their agent. Removed (410 `gone` + `details.hint` "branches: use Commit / Compare"): `publish`, `resetDraftToHead`,
`getTemplateSyncStatus`, `getTemplateSyncPlan`, `previewTemplateSync`, `applyTemplateSync`, `unlinkTemplate`,
`registerLocalProposal` stays (old agents, §9), `getDiff` becomes `uncommitted vs head` (`DiffResponse` keeps its shape:
`changes`, `blockers` = commit blockers, `pending` = []), `listProposals` / `listRevisions` / `getRevision` stay read-only
(legacy history). `getImageManifest` / `getAgentBlob` access rule: "in the deployment head commit, one of its 10 newest
commits, the signer's agent-branch head, an open compat proposal or a legacy head" (was: head or open proposal).

### 7.2 Agent local API (`AGENT_API`, new rows; all session+actor for writes)

| op | method | path | → | activities |
|---|---|---|---|---|
| `branchState` | GET | `/_opm/api/branch` (`?site=*` merges peers) | `AgentBranchState { cloud: { head, recent, staged: Record<site, SiteStaging> }, local: { head, commits (10), mergedFrom, ahead, behind, differs }, uncommitted: { working, drift, notes, scannedAt }, sites: per-site summary for Fast-forward all }` | — |
| `listBranchCommits` / `getBranchCommit` | GET | `/_opm/api/branch/commits[/{commitId}]` | `PageResponse<CommitSummary>` / `CommitDetail` (masked) | — |
| `getCloudCommit` | GET | `/_opm/api/branch/cloud-commits/{commitId}` | `CommitDetail` (masked; fetched + cached on demand) | — |
| `getUncommitted` | GET | `/_opm/api/branch/uncommitted` | `{ items: (MergeItem & { origin: 'edit' \| DriftItem['origin'] })[], notes: DriftNote[], scannedAt }` | — |
| `putWorking` | PUT | `/_opm/api/branch/working` | body `ConfigInput` of the edited sections (the forms' config) → stored as items; `{ uncommitted }` | — (edits are not activities; commits are) |
| `rescanDrift` | POST | `/_opm/api/branch/drift/rescan` | `DriftReport` | drift.detected |
| `commitLocal` | POST | `/_opm/api/branch/commit` | `{ message, keys, accept: string[], takeBytes?: string[] }` → `{ commit?: Commit, run?: UpdateRun }` (202 when an apply runs) | branch.committed, drift.committed, drift.accepted |
| `discardLocal` | POST | `/_opm/api/branch/discard` | `{ keys, notes }` → `{ run?: UpdateRun }` | drift.discarded / working.discarded |
| `compareLocal` | POST | `/_opm/api/branch/compare` | `{ source: 'cloud' \| { cloudCommitId } \| { localCommitId }, diffOnly? }` → `MergePlan` | — |
| `previewLocalMerge` | POST | `/_opm/api/branch/merge/preview` | `{ sourceCommitId, choices, values? }` → `MergePreview` (+ staging dry-run issues) | — |
| `mergeLocal` | POST | `/_opm/api/branch/merge` | `{ sourceCommitId, targetHead, choices, message }` → `UpdateRun` (202) | branch.merged / branch.cherry-picked (at COMMIT), branch.merge-failed |
| `fastForwardAll` | POST | `/_opm/api/branch/fast-forward` | `{ sourceCommitId, sites, message }` → `UpdateRun` (202) | branch.merged per site |
| `revertLocal` / `resetLocal` | POST | `/_opm/api/branch/revert` · `/reset` | `{ commitId, message, targetHead, choices? }` → `UpdateRun` | branch.reverted / branch.reset |

Removed: `previewLocalProposal`, `createLocalProposal`, `restage`, `approveStart`, `approve` (410 gone, hint "Branch
page"); `listProposals` / `getProposal` stay read-only for history. `rollbackStart` / `rollback` (snapshot restore) stay with
IntentFlow; the rollback COMMIT writes a branch commit (kind `rollback`, message from the dialog, required).
Peer API: `GET /_opm/peer/cloud-commits/{commitId}` (plain, for sites without a cloud route), `GET /_opm/peer/branch`
(own branch state for `*` and Fast-forward all), `PeerPing` + `branch { head, mergedFrom, uncommitted }`, update steps
with `plan.targets`.

---------------------------------------------------------------------------------------------------------------------

## 8. Migration (`backend/scripts/migrate-i.ts`, `--dry-run` / `--apply`, idempotent, whole database)

### 8.1 Templates

For each template without `branch`: commit seq 1, kind `migrated`, commitId = new ULID, message `Migrated: template
version <v> (templates kept no history before 0.14)`, author = `updatedBy` account (else system), createdAt = updatedAt,
config = the current config fields, catalog null; `branch = { head, origin: null, commitCount: 1, uncommitted: 0 }`.

### 8.2 Deployments (every deployment, deleted ones too — they stay hidden)

1. **Revisions → commits.** Walk the applied chain from `deployment.head.revId` via `parentRevId` (all revisions of the
   chain must exist; a hole → the deployment is reported and skipped). Each revision → `deployments/{id}/commits/{revId}`
   (commitId = revId, seq = revision seq, parentId = parentRevId, kind `migrated`, message = `summary` if non-empty else
   `migrated revision <seq>`, author = actor (uid → account email/name; agent-origin: the email, `siteSlug` null), config,
   catalog, hash (verified = revision.hash), `data.migratedFrom { revId, seq }`). Revisions NOT on the chain (superseded /
   stale proposals) are not commits; they stay readable as legacy history.
2. **Pending proposals.** An open proposal (published / staging / ready) whose parent = the head → one more commit
   (commitId = its revId, seq head+1, message = its summary + ` (was proposal seq <n>, <status>)`, `data.migratedFrom {
   revId, seq, proposalStatus }`) = the new deployment head (the sites will see it as the cloud head to merge). The
   proposal document is NOT changed: old agents can still approve it (it is exactly the compat proposal of that commit).
   `approved` / `applying` → the deployment is skipped ("an update is running — run again after it"). Stale / superseded
   → left as they are (listed in the report).
3. **Draft** → working copy (unchanged bytes); `branch.uncommitted` = items(head → draft).
4. **srcTemplate** → `branch.origin = { branch: template, commitId: null, seq: null, label, templateVersion }` and
   `mergeBases/template:<templateId>` = `{ base: _srcBase, sourceCommitId: null, skipped: _srcSkipped mapped 1:1 (same
   key grammar; `theirsHash` kept), targetCommitId: head }` — Template sync state survives as the compare base.
5. **agentBranches/{site}** for every enrolled site: `{ head: the commit of AgentDoc.headRevId (legacy: true while < 0.14),
   mergedFrom: same, agentVersion, uncommitted: null }`; open LOCAL proposals of that site are listed in the report (the
   0.14 agent turns them into uncommitted changes, §5.1).
6. No head yet (never applied): an open proposal with parent null → commit seq 1 (step 2); otherwise NO commit is
   invented (a deployment commit is a release): `branch.head` stays null, the draft is the working copy and the GUI
   shows "No commit yet — commit to release". `branch.head` null is allowed only for such migrated, never-published
   deployments (new deployments always start with their create commit).
7. One `system.migration` audit entry with the counts; `verify-schema.ts` extended (branch fields, commit hashes, chain
   seq/parent, merge base shapes); rerun → 0 changes.

### 8.3 Agents (0.14 first start, §5.1) — no cloud action needed.

### 8.4 Expected prod result (to be confirmed by the dry run)

Template "Single": 1 migrated commit (version v at run time). Deployment "Test 02" (EByKdKkGRHOdkm5emrg4): its applied
revision chain → commits; draftVersion 11 working copy vs head = the uncommitted count; srcTemplate → origin Single +
merge base. "Dev Server": same; its #94 state (base v51, 26 pending, 1 conflict) shows in Compare (target Dev Server,
source Single) as 26 changes incl. 1 conflict. Exact numbers go to ORCH.md and the report.

---------------------------------------------------------------------------------------------------------------------

## 9. Compatibility (agents < 0.14)

### 9.1 Cloud compat proposals

While ANY enrolled site of the deployment reports `agentVersion` < 0.14.0 (or has not reported one), every new
deployment head (commit, merge, revert, reset) ALSO runs the old publish internally: revision `{ revId = commitId, seq =
(legacy head seq) + 1, parentRevId = deployment.head?.revId, origin 'cloud', summary = first line of the message }` +
proposal `published` (older open ones superseded). Old agents stage / approve / apply / report as before
(`reportHead`, `reportProposalStatus`, `registerLocalProposal` unchanged); `reportHead` also updates the site's
AgentBranchDoc (legacy head). A legacy local proposal is mirrored as an agent-branch commit kind `legacy`
(message = summary). When no site < 0.14 remains, no compat proposals are written (existing ones stay).

### 9.2 Warnings

Per site, wherever a deployment commit is made or shown: "site-01 runs agent 0.13.1 — needs agent ≥ 0.14 for agent
branches; it still receives this commit as a proposal (approve it in its agent GUI, whole commit only)" (Commit dialog,
Agent branches page, Sites, Setup upgrade row). `BRANCHES_MIN_AGENT = '0.14.0'` + `supportsBranches(version)`.

### 9.3 Mixed meshes (0.14 agent as a peer of an old one)

- Legacy proposals in the cloud view are not staged by 0.14 agents; their ping reports `proposals: [{ revId, status,
  staging: { staging: 'ready', progress: 1 } }]` for every open compat proposal ("legacy participant: not applying"), so
  the old gate can open.
- A legacy `updateStep` from an old coordinator: LOCK = take the lock (the engine's lock rules), SNAPSHOT / APPLY /
  VERIFY = no-op ok, COMMIT = unlock + record `kv legacyHead` (not the branch), ROLLBACK = unlock. The 0.14 site's
  containers never change through a legacy update — it merges on its own Branch page.
- A 0.14 coordinator never sends steps to an old site (Fast-forward all lists it as "needs agent ≥ 0.14").

---------------------------------------------------------------------------------------------------------------------

## 10. GUIs

### 10.1 Shared components (`ui/src/branch/`, BranchHost adapter)

- `BranchHost` (Svelte context, like ConfigHost): `compare(source, opts)`, `preview(req)`, `merge(req)`, `commits(cursor)`,
  `commit(id)`, `commitWorking(msg)`, `revert / reset`, `blobText(sha)`, `canWrite`, `labelOf(branch)`, `actor()`.
  Cloud: `frontend/src/lib/branch/branchHost.ts`; agent: `agent/gui/src/lib/branch/branchHost.ts`.
- `ChangeCount.svelte` (badge "N uncommitted"), and `ChangeBar` gains `count` + `onCommit` (Discard · Review · Commit…).
- `CommitDialog.svelte` (modal ≤ 4 fields): message textarea (first line counter 200, total 2000; **Suggest** inserts
  an auto summary — never prefilled silently), the change list (grouped, collapsible), author line, validation summary /
  blockers, agent-version warnings, for the agent: the runtime effect ("recreates api, web · no restart for drift items")
  → **Commit** (or **Commit & apply**).
- `MergeView.svelte` (full page): header source → target (pickers for the source: branch + commit), base line, counts +
  filter chips (All · Source changes · Conflicts · Target only · Removals · Skipped), groups Variables · Secrets · Files ·
  per site Settings / Edge / Volumes (+ content) / Containers / Dev tools, a **"Removed by this merge"** group, row =
  checkbox · label · kind badge · effect · requires chips; expand = Base | Target | Source (diff colours; TextDiff for
  files / content; secrets masked); conflicts: SegmentedControl Keep target · Take source · Custom (scalar input);
  Select all source changes · Select all removals · Clear; preview panel (bottom bar, drawer at 390 px) with validation,
  missing values (inline inputs: variables, and password inputs for required secrets on a deployment target; the
  matching `V_VAR_REQUIRED` / `V_SECRET_REQUIRED` issues show as these inputs, not as errors), unresolved conflicts → **Merge…** (CommitDialog). `diffOnly` mode = no checkboxes
  (History compare, Review).
- `HistoryView.svelte`: commit list (seq · kind badge · message first line · author · time · source chip "from Single@7"
  · stats +a ~c −r), select two → Compare, row → commit detail (message, author, source / revert / reset links, changes
  via MergeView diffOnly) with **Revert…** and **Reset to this commit…** (write only).

### 10.2 Cloud (frontend)

- Template scope + deployment scope sidebars: **History** (`…/history`, replaces deployment History of proposals),
  **Compare** (`…/compare?source=<branchKey>[@commitId]`, replaces Diff + Template sync; old routes redirect: `…/diff` →
  `…/compare?review=working`, `…/template-sync` → `…/compare?source=template:<origin>`), the scope header shows "head
  seq n · N uncommitted"; every config page shows the ChangeBar with Commit… when N > 0.
- Templates list / Deployments list: columns Head (seq · message) and Uncommitted; **New template** and **New
  deployment** dialogs: Source Empty · Template · Deployment (+ agent branch for templates/deployments) + commit picker +
  "Copy secret values without a generate rule" (deployment ← deployment).
- Deployment Overview: cards Head commit, Uncommitted, **Agent branches** (per site: head, behind / ahead, drift,
  version, Compare / History); page `…/branches` (table + per-site History drawer + Compare button).
- Deployment `…/compare` with source = an agent branch shows a banner "Changes made on site-01 — pick what the
  deployment should take".

### 10.3 Agent GUI

- Sidebar Deployment section: **Branch** (replaces Diff; badge = uncommitted + behind), `…/{site}/branch` with tabs
  Overview · Uncommitted · History; `…/{site}/branch/merge?source=<commitId>`; `…/diff` and `?rev=` redirect to Branch.
- Overview: **Cloud** card (head seq, message, author, time, "staged ✓ / downloading 40 % / failed"), **This site** card
  (head, merged from cloud seq, ahead / behind / differs), Uncommitted card (N edits · M server changes · K notes,
  last scan, Rescan, Review · Commit…), **Merge from cloud** (disabled with the reason while uncommitted), **Fast-forward
  all sites** (when applicable), the update progress panel (existing) while a merge runs, failed-containers panel.
- Variables / Secrets / Files / Config pages: edits go to the working copy (`putWorking`); the existing local-edit bar
  becomes the ChangeBar with Commit…; "Create local proposal" is gone.
- Backups → Roll back keeps IntentFlow (60 s, ⏩, ACCEPT) + a required commit message field in the final step.

---------------------------------------------------------------------------------------------------------------------

## 11. Activities and audit (Appendix B / AUDIT_ACTIONS)

New activity types (also appended to the Go mirror): `branch.created`, `branch.committed`, `branch.merged`,
`branch.cherry-picked`, `branch.reverted`, `branch.reset`, `branch.merge-failed`, `working.discarded`, `drift.detected`,
`drift.committed`, `drift.discarded`, `drift.accepted`, `agent-branch.pushed`. Data always carries `{ branch, commitId,
seq, message (first line), source?, picks count, skipped count }`. Agent activities go through the pipe (site logs);
cloud ones into `_cloud`. Audit actions: `template.committed`, `template.merged`, `template.reverted`,
`template.reset`, `template.working-discarded`, `deployment.committed`, `deployment.merged`, `deployment.reverted`,
`deployment.reset`, `deployment.working-discarded` (merge covers cherry-pick with `data.kind`). The old proposal / Template
sync types stay in the catalog (history) but are no longer written (except compat proposals, §9).

---------------------------------------------------------------------------------------------------------------------

## 12. Tests

- **shared**: `branch.test.ts` + `branch-vectors.json` (§4.7), commit hash = revision hash for the same config, uncommitted
  counts, MergePlan for every BranchRef pair shape (template has no secret values; agent scope), deletions everywhere,
  `requires`, revert / reset, migrate helpers (srcTemplate → MergeBase key mapping). The template-sync module stays for one
  release (its skip records are read by migrate-i; nothing else calls it) and is removed in 0.15.
- **Go**: `go vet`, `go test ./...`: `internal/branch` vectors; store migration (revisions → commits, local proposal →
  working items); `TestDriftDocker` (real Docker): hand-edit a container (`docker rm` + `docker run` same name other env),
  a per-site file, an image (`docker tag` another image + recreate), plus an unknown container on the site network →
  all uncommitted; merge refused 409; commit (drift) → clean; cloud commit adds a change → merge with cherry-pick (one
  item NOT taken) + a deletion removing the local-only container → container gone, data kept, commit kinds and
  merge base correct, push reaches the fake cloud mirror; `TestTwoSiteMesh` updated (cloud commit → per-site merge on
  db, fast-forward all, failing health → rollback leaves no commit, legacy participant with a fake old peer).
- **backend emulator**: `28-branches` (create from all four pairs, commit + validation levels, working discard, compare
  kinds incl. conflicts + removals + skipped, merge / cherry-pick / revert / reset, 409 dirty / stale, permissions incl.
  secret values, agent push verify / fork refusal, agent branch views, compat proposals while an old site exists,
  removed routes 410), `29-migrate-i` (fixtures shaped like Single / Test 02 / Dev Server incl. pending proposal, local
  proposal, srcTemplate with skipped → dry run report, apply, rerun 0, verify-schema 0).
- **frontend**: `npm run check` 0 errors; mock Playwright `branches.spec.ts`: create-from (template ← deployment,
  deployment ← deployment), history (open, compare two, revert), compare + cherry-pick with a conflict, deletion
  ("Removed by this merge"), commit dialog (message required), uncommitted counts; screenshots light / dark / 390.
- **agent GUI**: check 0 errors; Playwright `branch.spec.ts` on the mock: cloud vs local cards, uncommitted (drift +
  edits), commit, merge blocked while dirty, merge with cherry-pick + remove locally, fast-forward all, history;
  screenshots light / dark / 390; real-agent smoke vs `opm-devmesh -branches`.
- e2e harness updated (publish → commit; approve → merge).

---------------------------------------------------------------------------------------------------------------------

## 13. Implementation plan (phase B, after 0.13.1)

1. **Contract (lead)**: `shared/src/branch.ts` (+ types, cloud-api, agent-api, validate hooks, ACTIVITY_TYPES /
   AUDIT_ACTIONS, vectors + generator), SPEC §21 #104 + Appendix A/B; shared tests green.
2. In parallel, disjoint dirs (forks): **GO** (`agent/` except `agent/gui`), **BACKEND** (`backend/`, migrate-i dry run
   only), **UI** (`ui/src/branch/`), then **FRONTEND** (`frontend/`) and **AGENT GUI** (`agent/gui/`) once the UI
   components exist (they may stub them meanwhile). Parallel Playwright runs use their own `--output`, `OPM_E2E_PORT`,
   `OPM_MOCK_DIR`; one agent `vite build` at a time.
3. Lead: integration, docs (SPEC body, docs-v2, TEAM.md, ORCH.md), migration dry run → apply on prod → verify, `make dist
   VERSION=0.14.0-<MMDDHHMM>` from the main tree, upload + `--latest`, deps pin + notes, deploy functions + firestore +
   hosting, prod schema check, screenshots review.

---------------------------------------------------------------------------------------------------------------------

## 14. As built (phase B) — deviations and team decisions

- **409 shape**: "dirty working copy" is `409 conflict` with `details { reason: 'dirty-working-copy', uncommitted }` (cloud and
  agent; `dirty-working-copy` is not a separate ErrorCode). A stale `targetHead` answers `stale` before the dirty check (agent).
- **Skipped rule**: an item whose 3-way kind is `ours`, `theirs` or `conflict` becomes `skipped` when a skip record matches the
  source hash — after a merge the base IS the source, so a change not taken comes back as `ours` (#94 rule). Vectors: 16 cases.
- **Every branch starts with a `create` commit** (also deployments created from a template); its compat proposal is written
  only when the config is publish-valid (BR-B2). The legacy `templateId` shortcut copies the template working copy (BR-B4).
- **Compat proposals** are written unless at least one site is enrolled and EVERY enrolled site is ≥ 0.14 (BR-B3).
- **Branch mode on the agent** starts only when the cloud view carries `branch`; against an old cloud the agent keeps the
  proposal flow unchanged (BR-G2).
- **Edge drift is not detected** (the agent owns the running edge, Caddy admin off); edge changes come only from GUI edits.
- **Docker 29 / containerd image ids**: an image counts as expected when its id equals the config digest OR the id of
  `opm/cfg:<hex>` (BR-G9). Unknown images become `<svc>:local-<12 hex>` catalog entries.
- **Hand-made containers** adopted by a drift commit are replaced / removed by later applies (`adoptedContainers`).
- **Push**: one commit per outbox entry; `pushedAt` is not tracked.
- **Agent blob endpoint** `GET /_opm/api/branch/blobs/{sha256}` (local → peer → cloud, ≤ 8 MiB) feeds the merge view text diffs.
- **Cloud GUI**: one working-copy bar per scope page (BR-F1); Compare source in the URL (BR-F4).
- `srcTemplate` is still written for new deployments from a template (older migrations read it; BR-B5).
- Removed / 410: publish, reset-to-head, template-sync (5 ops), agent local proposals / restage / approve (in branch mode).

---------------------------------------------------------------------------------------------------------------------

## 15. §21 #105 — one action: Commit (no working copy; pending changes live in the browser) · agent 0.14.1

User request (verbatim):

> "again there should not be 2 diff things, "save template" and "commit" is not 2 diff things, one thing "commit" and
> commit will same template, this means eveything in cloude is always commited, there is never a uncommited changes,
> uncommited changes are always in browser UI until you push it gets commited with the commit message.
> only on the site agent if someone mannualy did something outside of agent UI then it is uncommited changes. and if
> you change anything viya agent GUI, then again those remain uncommited in browser, once you click commit it also
> updates the container's and all with the changes.
> this keeps it easy to track instead of save to draft then commit, etc."

### 15.1 Decisions

| # | decision | why |
|---|---|---|
| J1 | **No server-side working copy.** The template config fields and `Deployment.draft` stay in Firestore as a **copy of the head commit config**, written ONLY by the commit writer (`commitTo`: commit, merge, cherry-pick, revert, reset, create). `branch.uncommitted` is removed (migrate-j deletes it). The field name `draft` is kept for storage compatibility (every reader — setup, install, status, transfers, agent upgrade — now reads the head). `draftVersion` / template `version` still move when the config changes (a cursor for nothing but history and compat). | "everything in cloud is always committed"; renaming a field read by 12 modules buys nothing |
| J2 | **Commit = the only persisting action of the config editors**: `POST /v1/templates/{id}/commits` · `POST /v1/deployments/{id}/commits` with `{ message, baseCommitId, config: ConfigInput }`. The server checks `baseCommitId` = head (else **409 `stale` `{ reason: 'head-moved', head }`**), merges secrets (SecretDefInput: `plainValue` absent = keep the base head value, `null` = clear, string = set, `generateNow` = generate), normalizes exactly as the old section writes did, validates (template level / publish level = a release, D5), refuses `V_NO_CHANGE`, and writes one commit kind `change`. Permissions: templates.write; deployment.config.publish (+ deployment.secrets.write when secret definitions or values change). | one write path; optimistic concurrency without a server copy |
| J3 | **Removed — 410 `gone` with a hint**: `putDraftSection`, `putDraftSite`, `regenerateSecret`, `discardTemplateWorking`, `discardDeploymentWorking`, config fields in `patchTemplate`; `compareBranches` with `at: 'working'` → 422 (the browser compares its own changes with the shared engine). Merges / cherry-picks / revert / reset no longer check a dirty working copy (there is none); the stale check stays. | nothing may write config outside a commit |
| J4 | **The browser change set**: ONE pending change set per entity (template / deployment in the cloud; the site in the agent GUI), shared by every page / form / drawer of that entity. Inline page forms (site editor, container editor) edit it directly; modals and drawers **Apply** into it. Nothing calls the server until Commit. It is persisted in **localStorage** per entity (`shared/src/pending.ts`: key `opm.pending.v1.<app>.<branchKey>`, value = base head + the edited `ConfigInput`) on every change, so a reload or an accidental close loses nothing; other tabs of the same browser adopt it (storage event). | "uncommitted changes are always in browser UI until you push" |
| J5 | **Restore**: on load, a stored set made on the current head → banner "N unsaved changes from <time> · Restore · Discard"; a set made on an older head → **stale** banner "made on seq X, the head is now seq Y · Review & rebase · Discard". | reload-safe, honest about a moved head |
| J6 | **Rebase** (`rebaseChanges` in `shared/src/branch.ts` = the merge engine with source = my changes, target = the new head, base = the head I edited): clean → applied automatically (toast "rebased onto seq Y"); conflicts → the Rebase dialog (Keep the head · Take mine · Custom for scalars). Used for the stale restore and for **409 at Commit** (the dialog stays open on the rebased set: review and Commit again). | the shared merge engine, no second algorithm |
| J7 | **Secret VALUES** (deployments, agent): setting / clearing / generating a value is part of the change set and of the commit like any other change. Typed values live in memory only — never in localStorage (`stripSecretValues`; the restore banner lists "re-enter the value of X"). The commit stores the value in the commit config (plain, masked on every human read, as since #104) and records **`CommitData.secretValues { set, generated, cleared }` (slugs only)**; the same slugs go into the `branch.committed` activity and the audit entry (`secretValues`). "Generate new value" = `generateNow` (the server generates at commit; Reveal after the commit). Templates hold no values. | "everything committed" + audited, never a value in storage, activity or audit |
| J8 | **Other writes**: ~~blob uploads (File content, seed archives) happen BEFORE the commit~~ — **superseded by §16 (#107)**: bytes are pending changes uploaded by the commit itself; "Rename everywhere" of an image writes **one commit per affected template** (message "Rename everywhere: <svc>:<old> → <svc>:<new>", the person as author); catalog entities (services, images, registries, icons, 404 pages, tags, accounts) and entity **details** (template label / icon / description, deployment label / image / tags / managers) are not branch config and keep their own Save (labelled "Save details"); persist-to-cloud volume content (#101 cloud copies) is DATA, versioned on its own (not config). | everything that is config is committed; the rest is not config |
| J9 | **ChangeBar** (`ui/src/primitives/ChangeBar.svelte`, both GUIs): "**N changes (not committed)**" · **Discard** · **Review** · **Commit…** — no Save button anywhere in the config editors. Cloud: one bar for the scope on every config page (template / deployment); agent: on every config page of the site. Review = the shared MergeView in diffOnly mode on a plan computed in the browser (`uncommittedItems(head, pending)`). | one bar, one action |
| J10 | **Merge / cherry-pick / revert / reset**: pick, then Commit — a merge commit is written directly (no dirty check). If the browser holds pending changes of the target, they become stale and are rebased (J6). | the user's "pick, then Commit" |
| J11 | **Agent GUI = the same model.** Edits stay in the browser (localStorage, same module). **Commit** (message required) = `POST /_opm/api/branch/commit { message, baseCommitId, config }` → the agent validates (local-edit rules + agent validation), stages, and runs the EXISTING apply engine on this site (LOCK → pre-update SNAPSHOT → APPLY → VERIFY → COMMIT, rollback on failure); the local commit (kind `change`) is written only at COMMIT. A failed apply writes **no commit**; the browser keeps the changes and shows the failure. `putWorking` answers 410; the `working_items` table / kv `workingCopy` are no longer used; there are no "edit" uncommitted items. | "once you click commit it also updates the containers" |
| J12 | **Agent "uncommitted" = drift only** (changes made outside the agent, §6). Drift must be committed (or discarded = re-applied) before a merge **and before a commit of GUI edits**: the edits commit answers **409 `conflict` `{ reason: 'uncommitted-drift', drift, notes }`**. GUI flow: the Commit dialog shows "N changes made on the server are not committed — commit them first" with **Commit server changes…**; after the drift commit the browser changes are rebased onto it (J6, usually clean) and the Commit dialog opens again. | one rule: the branch head must describe what runs before anything new is applied |
| J13 | **Agent start-up migration (0.14.0 → 0.14.1)**: saved working edits (kv `workingCopy`) are **auto-committed through the apply engine** (author system, message "Uncommitted changes from before 0.14.1 (auto-committed)", `data.autoCommitted`) once the site is idle with a head, no blocking drift, and the edits pass validation. If any condition fails, or the apply fails (engine rollback), the edits are **dropped**: written to `<dataDir>/backup/working-copy-pre-0.14.1.json` (0600; it may hold secret values like the agent DB) and logged (`working.discarded` with `reason`). Either way kv `workingCopy` / `working_items` are cleared once. | nothing silently lost; nothing half-applied |
| J14 | **Cloud migration** `backend/scripts/migrate-j.ts` (`--dry-run` default, `--apply`, idempotent): every template / deployment whose config copy differs from its head (items > 0, secret values compared by hash) gets one commit kind `change`, author system, message "Uncommitted changes from before 0.14.1 (auto-committed)", `data.autoCommitted { from: 'cloud-working-copy', items }` — validation is reported, NOT enforced (a migration must not lose data); deployments: agentEpoch+1 (+ compat proposal rule as any commit). A deployment without a head and with a non-empty config gets its first commit the same way. Then `branch.uncommitted` is deleted everywhere. One `system.migration` audit entry; rerun = 0 changes; verify-schema checks J1 (copy hash = head hash, no `uncommitted`). | "so nothing is lost" |
| J15 | **Compatibility**: the agent-facing cloud API is unchanged (agents < 0.14.1 keep working; their heartbeat `uncommitted.working` is stored and shown as "N saved edits on the agent (0.14.0)"). Agent 0.14.1 works with this cloud and with a 0.14.0 cloud (it never calls a removed cloud route). The 0.14.1 agent GUI is embedded in the binary, so no GUI ↔ agent skew. | rule 8 |

### 15.2 API (changed rows)

| op | before (#104) | now (#105) |
|---|---|---|
| `commitTemplate` / `commitDeployment` | `{ message, ifVersion \| ifRevision }` — commit the server working copy | `{ message, baseCommitId, config }` — commit the browser change set; 409 `stale { reason: 'head-moved', head }` |
| `putDraftSection`, `putDraftSite`, `regenerateSecret`, `discard…Working`, `patchTemplate` config fields | working-copy writes | **410 gone** + hint |
| `merge`, `revertCommit`, `resetToCommit` | 409 dirty-working-copy | no dirty check (stale only) |
| `compareBranches` `at: 'working'` | the server working copy | 422 (browser-side compare) |
| agent `putWorking` | stores GUI edits | **410 gone** |
| agent `commitLocal` | `{ message, keys, accept, takeBytes }` (edits + drift) | edits: `{ message, baseCommitId, config }` (apply, then commit; 409 uncommitted-drift / stale); drift: `{ message, keys, accept, takeBytes }` |
| `BranchState.uncommitted` | count | removed |
| `CommitData` | — | `+ secretValues { set, generated, cleared }`, `+ autoCommitted` |

### 15.3 Tests (#105)

shared: `pending.test.ts` (storage form without secret values, restore state, rebase, secret value changes) + rebase
vectors in `branch-vectors.json` (Go `internal/branch` TestVectors); Go: commit of GUI edits applies (real Docker), a
failed apply writes no commit, 409 uncommitted-drift / stale, putWorking 410, start-up migration of a saved working
copy; backend emulator: direct commit (secrets merge, permissions, V_NO_CHANGE), 409 stale + rebase, removed routes 410,
rename everywhere → commits, migrate-j (dry run, apply, rerun 0, verify-schema 0); frontend mock Playwright: no Save
button in any config editor, several forms then ONE commit, reload restores, a stale head rebases (clean + conflict);
agent GUI Playwright: the same on the agent + drift-first flow; real-agent smoke on `opm-devmesh -branches`.


## 16. §21 #107 — one Commit = one transaction, including the bytes · agent 0.15.1

User request (verbatim, on the template volume content page with its page-level Apply button):

> "in here also when i change any file on approve/apply it seems to upload those files. and then i have to seperately
> make the commit. this is also wrong. keep all the file changes and volume changes all config changes. and single commit
> button on msg + comit, you will first upload file then commit, all done in transaction so everything gets reverted if
> not done."

### 16.1 Decisions

| # | decision | why |
|---|---|---|
| S1 | **Content edits are pending changes** like any other config edit: volume starter content (create / edit / rename / move / delete / upload files and folders), File content (the "Edit content" drawer applies live — no Save content), seed archives. The editor puts the BlobRef into the working config at once (texts debounced ~300 ms + on blur); the change bar counts them; Review shows a per-file text diff. No page-level Apply / Discard on the content workspace. | "keep all the file changes and volume changes all config changes" |
| S2 | **Bytes stay in the browser** until Commit: memory + **IndexedDB** `opm-pending-blobs` (store `blobs`, key `opm.pending.v1.<app>.<branchKey>\|<sha256>`, record = owner, sha256, size, base head, savedAt, bytes); `StoredPending.blobs` (localStorage) lists the sha256s the set needs. Restore loads them (lost bytes → a warning naming the files); rebase only re-labels the base; Discard and a successful commit delete them; unreferenced bytes are dropped on every persist. IndexedDB unavailable → memory only (like localStorage). | localStorage is ~5 MB; reload-safe like #105 J4 |
| S3 | **Commit = stage → verify → promote → commit** (`ui/src/branch/stage-commit.ts` `commitWithStage`): `POST /v1/stages { blobs: newConfigBlobs(head, config) }` → `{ stageId, missing }` (the store's blobs are never re-sent) → each missing blob `PUT /v1/stages/{id}/blobs/{sha}` (≤ 24 MiB) or a multi-part upload with `stageId` → `POST …/commits { message, baseCommitId, config, stageId }`. The dialog shows "Uploading n of m files · x of y MB", then "Committing…". | one button, one transaction |
| S4 | **Server**: stale check → validation → every NEW blob (in the config, not in the head) is in the store or in the stage with the right size and sha256, else **422 `V_BLOB_MISSING`** (`details.missing`) → **promote** (copy `staging/<id>/<hex>` → `files/<hex>`, skip existing) → Firestore transaction (head check again) → delete the stage. | verified bytes only |
| S5 | **Crash safety = promote before visible.** Crash before the transaction: no commit; promoted blobs are unreferenced content-addressed objects (nothing reads them; identical bytes are reused by the retry). Crash after: the commit's blobs are all in the store. Readers (agent `getAgentBlob`, GitHub mirror, console `getBlob`) never read `staging/`. | "everything gets reverted if not done" |
| S6 | **Failure = nothing visible**: the server deletes the stage on every failed commit (422, 409, fault), the browser deletes it too (best effort) and keeps all pending changes including the bytes; 409 → the #105 rebase. Abandoned stages: Storage lifecycle `staging/` age ≥ 1 day → delete (`backend/storage.lifecycle.json`) + the daily job (expired `stages/{id}` docs + objects). | no leftovers |
| S7 | **Audit**: one commit entry; stage ops are not audited; multi-part uploads into a stage are not audited. `POST /v1/blobs` kept for compatibility, unused by the console. | "single commit" — no blob noise |
| S8 | **Cloud copies of persist volumes** (data, #101 f) keep "Save as new version", also staged: `createVolumeContentVersion { …, stageId }`. | same guarantee for data |
| S9 | **Agent GUI** (0.15.1): File content edits stay in the browser (`PendingBlobs('agent', …)`); Commit = `POST /_opm/api/stages` → `PUT …/blobs/{sha}` → `POST /_opm/api/branch/commit { …, stageId }`; the agent verifies, promotes into its blob store before the apply engine stages, deletes the stage on every outcome; stages > 24 h removed at start + hourly. | the same rule on the site |

| S10 | **Drafts in IndexedDB** (user follow-up): every pending change set (config + bytes) of cloud templates, deployments and the agent GUI is auto-saved (debounced 400 ms, flushed on pagehide) to IndexedDB `opm-drafts` — stores `drafts` (key `<app>\|<user>\|<branchKey>`), `blobs` (`…\|<sha256>`, index owner), `meta` (`alive:<app>`, `user:<app>`). localStorage is no longer used (old `opm.pending.v1.*` entries migrated once). | "all pending changes … must be all stored in index db" |
| S11 | **Expiry = 1 h after the app was last open**: heartbeat every 30 s + visibilitychange / pagehide while a tab is open; on load, last heartbeat > 1 h → purge every draft of the app (before the first heartbeat). A long-open tab keeps its drafts. Logout / another account → the user's drafts are deleted; drafts are keyed per user. | "auto expire after 1hr if website is not loaded, but if the website is open then keep it" |
| S12 | **Valid only on the current head**: on load, base commit = current head → "You have unsaved changes from <time> (N changes) — Restore or Discard?"; otherwise deleted automatically + one-time notice "Your local draft was based on commit X; the latest is Y, so it was discarded." Replaces the #105 J5 stale "Review & rebase" offer; the in-session 409 rebase at Commit (J6) stays. | "draft is only valid if … the current latest commit is also the same" |

### 16.1a As built

Cloud user of a draft = the signed-in uid; agent GUI user = the declared actor email (until declared: memory only).
Agent stage blobs ≤ 200 MiB (archives). The cloud content page shows a header badge "N files not committed"; the Files
drawer footer = status · Revert to committed · Done. Released 2026-10-10: backend (functions, rules, CORS, lifecycle
`staging/` 1 day), hosting, agent **0.15.1-10100226** = agent/latest + deps pin; prod verify-schema 0 violations.

### 16.2 API

| op | path | notes |
|---|---|---|
| `createStage` | `POST /v1/stages` | `{ blobs: BlobNeed[] }` → `Stage { stageId, expiresAt, missing, present }` (≤ 5000 blobs, ≤ 1 GiB) |
| `putStageBlob` | `PUT /v1/stages/{stageId}/blobs/{sha256}` | raw ≤ 24 MiB, verified; creator only; expired → 410; in the store → `staged: false` |
| `deleteStage` | `DELETE /v1/stages/{stageId}` | best effort after a failure |
| `createUpload` | `POST /v1/uploads` | `+ stageId` → joined into the stage |
| `commitTemplate` / `commitDeployment` | `POST …/commits` | `+ stageId`; 422 `V_BLOB_MISSING` |
| `createVolumeContentVersion` | `POST …/volume-content/…/versions` | `+ stageId` |
| agent `createLocalStage` / `putLocalStageBlob` / `deleteLocalStage` | `/_opm/api/stages…` | ≤ 50 MiB per blob |
| agent `commitLocal` | `POST /_opm/api/branch/commit` | `+ stageId` |

### 16.3 Tests

shared `staging.test.ts`; backend emulator `34-commit-staging` (new blobs commit, only missing uploaded, sha mismatch,
failure after staging deletes the stage + no commit, 409 stale with staged blobs, crash point after promotion = no
visible commit + retry, missing staged blob 422, other user's stage 404, audit = one entry); Go stage + commit tests;
frontend + agent GUI mock Playwright (no Apply / Save, change bar counts both, one Commit uploads then commits, reload
restores the bytes, failed commit keeps the changes, Discard clears IndexedDB; drafts: refresh → Restore incl. bytes,
Discard, moved head → auto-discard + notice, expiry > 1 h without an open tab (fake clock), long-open tab keeps its
draft, per-user isolation).
