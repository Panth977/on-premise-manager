/**
 * A config "scope" = the thing the shared editors edit: a template or a deployment (SPEC §21 #105).
 *
 * There is NO server-side working copy. The server state is the HEAD (the template config fields / the deployment
 * `draft` are a copy of the head commit config). Every edit of the entity lives in ONE in-memory pending change set
 * shared by all its pages (the scope object is provided by the shell's layout context):
 *   - `config`      = the working config (the base head + every change applied by a drawer / modal / page)
 *   - `siteDrafts`  = the sites of `config` by slug (the site editor, container editor, routes … edit them in place)
 *   - `secretOps`   = deployment secret VALUE changes (typed values: memory only, never localStorage)
 * Nothing calls the server until `commit(message)`, which writes ONE commit from the base head + the changes
 * (409 stale when the head moved → `rebase()` with the shared merge engine). The set is persisted in localStorage per
 * entity (shared/src/pending.ts) by the PendingBar; on load a stored set is offered for restore (same head) or for a
 * rebase (stale). Template / deployment DETAILS (label, icon, …) are not config: saved directly ("Save details").
 *
 * §21 #107 BYTES are pending changes too: File contents, volume starter content and seed archives are kept by
 * `putBytes` in a PendingBlobs (memory + IndexedDB, per entity; the localStorage set lists their sha256s in `blobs`);
 * the config points at their BlobRef at once. Nothing is uploaded before Commit: `commit()` runs ONE transaction —
 * stage the NEW blobs (only those the store lacks), then the commit request with the stageId (the server verifies +
 * promotes, then commits). Any failure keeps every change, bytes included; success / Discard clear the bytes.
 */
import type { BranchRef, BranchState, CertStatus, Commit, CommitRef, Config, ConfigInput, ConfigView, DeploymentView, MergeChoice, MergeItem, RevisionCatalog, SecretDef, SecretDefInput, Site, StatusResponse, Template, ValidationIssue, Account } from '@opm/shared';
import { MASK, branchKey, emptyConfig, normalizeConfig, rebaseChanges, resolveSiteHosts, uncommittedItems, validateConfig, type Issues, type SiteHost, type ValidationContext } from '@opm/shared';
import { toStoredPending, type StoredPending } from '@opm/shared/pending';
import { configBlobs, newConfigBlobs, totalBytes, type BlobNeed } from '@opm/shared/staging';
import { PendingBlobs } from '$opm/branch/pending-blobs';
import { commitWithStage, LostBytesError } from '$opm/branch/stage-commit';
import { registerPendingBytes, stageTransport } from './files';
import { bytes as fmtBytes } from '../util/format';
import { autosaver, clearDraft, readDraft, watchDraft, writeDraft } from '$opm/branch/drafts';
import { draftsReady, draftUser, noteDraftCount } from '../branch/drafts.svelte';
import { api, conflictVersion, errIssues, isApiError } from '../api';
import { toast } from '../toast.svelte';
import { stripEmptyDevTools } from './devtools/devtools';
import { catalog } from '../stores/catalog.svelte';
import { session } from '../session.svelte';

export type SectionKey = 'variables' | 'secrets' | 'files' | 'sites';
export interface SecretOp {
  /** string = a new typed value (memory only), null = clear */
  plainValue?: string | null;
  generateNow?: true;
}

export interface ConflictState {
  current: number | null;
  reload: () => Promise<void>;
  keep: () => Promise<void>;
}
/** a stored draft offered on load (§21 #107: only a draft made on the CURRENT head is offered) */
export interface RestoreOffer {
  stored: StoredPending;
  state: 'same';
}
/** a draft made on an older head: discarded at load, the notice tells (one time) */
export interface StaleNotice {
  baseSeq: number | null;
  headSeq: number | null;
  count: number;
  savedAt: string;
}
/** conflicts of a rebase waiting for the person (RebaseDialog) */
export interface RebaseState {
  conflicts: MergeItem[];
  clean: number;
  headLabel: string;
  /** called after the choices were applied (e.g. reopen the commit dialog) */
  then?: () => void;
}

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);
const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T;
const snap = <T>(v: T): T => clone($state.snapshot(v) as T);

export abstract class ConfigScope {
  abstract readonly kind: 'template' | 'deployment';
  readonly id: string;
  /** the WORKING config: base head + the applied pending changes */
  config: Config = $state(emptyConfig());
  /** the current server head config (masked secret values) */
  headConfig: Config = $state(emptyConfig());
  /** the current server head commit */
  headCommit: CommitRef | null = $state(null);
  /** the head the pending set was made on (≠ headCommit = stale) */
  baseCommitId: string | null = $state(null);
  baseSeq: number | null = $state(null);
  baseConfig: Config = $state(emptyConfig());
  version = $state(0);
  loaded = $state(false);
  loading = $state(false);
  error: string | null = $state(null);
  notFound = $state(false);
  saving = $state(false);
  committing = $state(false);
  /** issues of the last refused save / commit (API `invalid`) */
  saveIssues: ValidationIssue[] = $state([]);
  lastReport: ValidationIssue[] = $state([]);
  conflict: ConflictState | null = $state(null);
  /** the working sites by slug (edited in place: they ARE part of the pending set) */
  get siteDrafts(): Record<string, Site> {
    return Object.fromEntries(this.config.sites.map((s) => [s.siteSlug, s]));
  }
  /** deployment secret value changes (slug → op) */
  secretOps: Record<string, SecretOp> = $state({});
  /** a stored set from an earlier visit / reload, offered for restore */
  restoreOffer: RestoreOffer | null = $state(null);
  /** §21 #107 a stale draft was discarded at load */
  staleNotice: StaleNotice | null = $state(null);
  /** debounced auto-save of the draft (IndexedDB) */
  #autosave = autosaver(() => this.#writeDraft(), 150);
  #pagehide: (() => void) | null = null;
  /** a rebase waiting for conflict choices */
  rebaseState: RebaseState | null = $state(null);
  /** siteSlug → detected / advertised address of enrolled sites (auto hosts; deployments only) */
  detected: Record<string, string> = $state({});
  #unwatch: (() => void) | null = null;
  /** §21 #107 pending bytes (lazy: branchKey needs the subclass) + a reactive counter bumped on every change */
  #pb: PendingBlobs | null = null;
  #unreg: (() => void) | null = null;
  blobsVersion = $state(0);
  /** sha256 → time put (kept by retain() for a few seconds: a text edit is hashed before the config points at it) */
  #fresh = new Map<string, number>();
  /** progress of a running commit ("Uploading 2 of 5 files · …" / "Committing…") */
  commitProgress: string | null = $state(null);
  /** files whose pending bytes could not be restored (IndexedDB cleared) — banner */
  lostBytes: string[] = $state([]);

  constructor(id: string) {
    this.id = id;
  }
  get blobs(): PendingBlobs {
    if (!this.#pb || this.#pb.user !== draftUser()) {
      this.#unreg?.();
      this.#pb = new PendingBlobs('cloud', draftUser(), this.branchKey);
      this.#pb.onchange = () => this.blobsVersion++;
      this.#unreg = registerPendingBytes((sha) => this.#pb?.get(sha));
    }
    return this.#pb;
  }
  /** keep bytes as a pending change (memory + IndexedDB) → the BlobRef the config points at. Nothing is uploaded. */
  async putBytes(bytes: Uint8Array): Promise<{ sha256: string; sizeBytes: number }> {
    const r = await this.blobs.put(bytes, this.baseCommitId);
    this.#fresh.set(r.sha256, Date.now());
    return r;
  }
  /** keep bytes whose sha256 is known (volume content workspace) */
  async keepBytes(sha: string, bytes: Uint8Array): Promise<void> {
    this.#fresh.set(sha, Date.now());
    await this.blobs.putKnown(sha, bytes, this.baseCommitId);
  }
  /** pending bytes of a blob (undefined = not pending: it is in the store) */
  bytesOf(sha: string): Uint8Array | undefined {
    void this.blobsVersion;
    return this.blobs.get(sha);
  }
  /** new blobs of the working config that this browser holds (what Commit uploads, unless the store has them) */
  get pendingUploads(): BlobNeed[] {
    void this.blobsVersion;
    if (!this.loaded) return [];
    return newConfigBlobs(this.baseConfig, this.working()).filter((b) => this.blobs.has(b.sha256));
  }
  /** CommitDialog note: "2 files · 1.2 MB are uploaded with this commit" */
  get uploadNote(): string | null {
    const l = this.pendingUploads;
    if (!l.length) return null;
    return `${l.length} file${l.length === 1 ? '' : 's'} · ${fmtBytes(totalBytes(l))} ${l.length === 1 ? 'is' : 'are'} uploaded with this commit (only content the cloud does not have yet).`;
  }
  /** labels of the config places that use a sha256 (lost-bytes banner) */
  #blobUsers(shas: Set<string>, cfg: Config = this.working()): string[] {
    const out: string[] = [];
    for (const f of cfg.files) if (f.content && shas.has(f.content.sha256)) out.push(`File ${f.fileSlug}`);
    for (const site of cfg.sites)
      for (const v of site.config.volumes ?? []) {
        const seed = v.seed;
        if (seed?.kind === 'tree') for (const e of seed.entries) if (shas.has(e.sha256)) out.push(`${site.siteSlug} › ${v.volumeSlug}/${e.path}`);
        if (seed?.kind === 'single' && shas.has(seed.sha256)) out.push(`${site.siteSlug} › ${v.volumeSlug}`);
        if (seed?.kind === 'archive' && shas.has(seed.blobSha256)) out.push(`${site.siteSlug} › ${v.volumeSlug} (${seed.name})`);
      }
    return out;
  }
  async #restoreBytes(st: StoredPending) {
    // the stored list, else every new blob of the restored config (sets written before #107 have none)
    const want = st.blobs ?? newConfigBlobs(this.baseConfig, this.working()).map((b) => b.sha256);
    const lost = await this.blobs.restore(want);
    // a lost blob only matters when the store does not have it either; the commit stage tells — warn now anyway
    this.lostBytes = lost.length ? this.#blobUsers(new Set(lost)) : [];
    if (this.lostBytes.length) toast.push('error', 'Some pending content is no longer in this browser', `${this.lostBytes.slice(0, 4).join(', ')}${this.lostBytes.length > 4 ? ` and ${this.lostBytes.length - 4} more` : ''} — upload or edit ${this.lostBytes.length === 1 ? 'it' : 'them'} again, or Discard.`);
  }
  abstract get base(): string;
  abstract get label(): string;
  /** "template" for templates (deployment.id in expressions) */
  abstract get deploymentId(): string;
  abstract get canWrite(): boolean;
  abstract get canWriteSecrets(): boolean;
  /** may commit (templates.write / deployment.config.publish) */
  abstract get canCommit(): boolean;
  abstract get branchRef(): BranchRef;
  abstract get branchState(): BranchState | null;
  /** fetch the server entity (head copy); calls adoptHead */
  protected abstract fetch(): Promise<void>;
  protected abstract postCommit(body: { message: string; baseCommitId: string | null; config: ConfigInput; stageId?: string }): Promise<Commit>;
  /** config of one commit of this branch (masked) — the base of a stale stored set */
  protected abstract commitConfig(commitId: string): Promise<Config>;

  get mode(): 'template' | 'draft' {
    return this.kind === 'template' ? 'template' : 'draft';
  }
  get branchKey(): string {
    return branchKey(this.branchRef);
  }
  /** secrets with a value in the WORKING config (validation context) */
  get secretsSet(): string[] | undefined {
    if (this.kind === 'template') return undefined;
    return this.working().secrets.filter((s) => s.value !== null && s.value !== undefined && s.value !== '').map((s) => s.secretSlug);
  }

  async load(): Promise<void> {
    this.loading = true;
    try {
      await Promise.all([this.fetch(), catalog.ensure()]);
      this.config = this.normalize(this.config);
      this.loaded = true;
      this.error = null;
      this.notFound = false;
      await draftsReady();
      await this.#offerStored();
      this.#unwatch?.();
      this.#unwatch = watchDraft('cloud', draftUser(), this.branchKey, (p) => this.#otherTab(p));
      if (typeof window !== 'undefined' && !this.#pagehide) {
        this.#pagehide = () => this.#autosave.flush();
        window.addEventListener('pagehide', this.#pagehide);
      }
    } catch (e) {
      this.error = e instanceof Error ? e.message : String(e);
      this.notFound = isApiError(e) && e.code === 'not-found';
    } finally {
      this.loading = false;
    }
  }
  dispose() {
    this.#autosave.flush();
    if (this.#pagehide && typeof window !== 'undefined') window.removeEventListener('pagehide', this.#pagehide);
    this.#pagehide = null;
    this.#unwatch?.();
    this.#unwatch = null;
    this.#unreg?.();
    this.#unreg = null;
  }

  /** API writes are normalized; reads of older documents may still hold the legacy container shape (round C) */
  protected normalize(c: Config): Config {
    try {
      return normalizeConfig(c, catalog.imageVolumes());
    } catch {
      return c;
    }
  }
  /**
   * The server head arrived (load / refresh / after a commit). Without pending changes the working config follows
   * the head; with pending changes on another head they become STALE (banner → rebase).
   */
  protected adoptHead(cfg: Config, head: CommitRef | null) {
    const n = this.normalize(cfg);
    const hadPending = this.loaded && this.pendingCount > 0;
    this.headConfig = n;
    this.headCommit = head;
    if (!hadPending) {
      this.config = clone(n);
      this.secretOps = {};
      this.baseConfig = clone(n);
      this.baseCommitId = head?.commitId ?? null;
      this.baseSeq = head?.seq ?? null;
    }
  }
  /** the pending set was made on an older head */
  get stale(): boolean {
    return this.pendingCount > 0 && (this.baseCommitId ?? null) !== (this.headCommit?.commitId ?? null);
  }

  /** ValidationContext of this scope (catalog + secrets set + detected hosts) */
  /** §21 #113 site images (digest "", added on a site or adopted from its Docker) the deployment head runs */
  siteImages = $state<RevisionCatalog | null>(null);
  vctx(mode: ValidationContext['mode'] = this.mode): ValidationContext {
    const v = catalog.vctx(mode, this.deploymentId, this.secretsSet, Object.keys(this.detected).length ? this.detected : undefined);
    const si = this.siteImages;
    if (!si) return v;
    const services = { ...v.services };
    const images = { ...v.images };
    for (const [slug, s] of Object.entries(si.services)) services[slug] ??= { label: s.label };
    for (const [k, im] of Object.entries(si.images)) images[k] ??= { kind: im.kind, ports: im.ports, env: im.env, volumes: im.volumes, healthcheck: im.healthcheck, importStatus: 'ready' };
    return { ...v, services, images };
  }
  hosts(config: Config = this.working()): Record<string, SiteHost> {
    return resolveSiteHosts(config, this.deploymentId, { detected: this.detected, placeholder: true });
  }
  validate(config: Config = this.working()): Issues {
    return validateConfig(config, this.vctx());
  }

  /** the working config (base head + every applied change) */
  working(): Config {
    return this.config;
  }

  // ───── pending change set
  /** items between the base head and the working config (+ secret value changes) */
  pendingItems = $derived.by((): MergeItem[] => {
    if (!this.loaded) return [];
    let items: MergeItem[] = [];
    try {
      items = uncommittedItems(this.baseConfig, this.working(), { secretValues: false });
    } catch {
      items = [];
    }
    const keys = new Set(items.map((i) => i.key));
    for (const [slug, op] of Object.entries(this.secretOps)) {
      if (keys.has(`secrets/${slug}`) && !this.baseConfig.secrets.some((s) => s.secretSlug === slug)) continue; // a new secret: one item
      const had = !!this.baseConfig.secrets.find((s) => s.secretSlug === slug)?.value;
      const effect: MergeItem['effect'] = op.generateNow || typeof op.plainValue === 'string' ? (had ? 'change' : 'add') : 'remove';
      if (effect === 'remove' && !had) continue;
      items.push({ key: `secrets/${slug}/value`, section: 'secrets', siteSlug: null, label: `Secret ${slug} value`, kind: effect === 'add' ? 'add' : effect === 'remove' ? 'remove' : 'change', effect, base: null, target: { has: had, value: had ? MASK : null }, source: { has: effect !== 'remove', value: effect === 'remove' ? null : op.generateNow ? 'generated at commit' : MASK }, preselect: false, scalar: false, requires: [], locked: null, sourceHash: '', note: op.generateNow ? 'a new value is generated by the commit' : undefined });
    }
    return items;
  });
  get pendingCount(): number {
    return this.pendingItems.length;
  }
  /** the commit body config (ConfigInput: secret ops as plainValue / generateNow) */
  commitInput(): ConfigInput {
    const w = snap(this.working());
    return {
      variables: w.variables,
      files: w.files,
      sites: w.sites.map((s) => stripEmptyDevTools(s)),
      secrets: w.secrets.map((s) => {
        const { value: _v, rotatedAt: _r, ...def } = s;
        void _v;
        void _r;
        const op = this.kind === 'deployment' ? this.secretOps[s.secretSlug] : undefined;
        const out: SecretDefInput = { ...def };
        if (op?.generateNow) out.generateNow = true;
        else if (op && op.plainValue !== undefined) out.plainValue = op.plainValue;
        return out;
      }),
    };
  }
  /** §21 #107 auto-save the draft (IndexedDB, debounced; flushed on pagehide / dispose); none while a draft is offered */
  persist() {
    if (!this.loaded || this.restoreOffer) return;
    this.#autosave.schedule();
  }
  async #writeDraft() {
    if (!this.loaded || this.restoreOffer) return;
    const user = draftUser();
    if (this.pendingCount === 0) {
      noteDraftCount(this.branchKey, 0);
      await clearDraft('cloud', user, this.branchKey, { keepBlobs: true });
      if (this.blobs.size) await this.#retain(new Set());
      return;
    }
    const input = this.commitInput();
    const used = new Set(configBlobs(input).map((b) => b.sha256));
    const blobs = newConfigBlobs(this.baseConfig, input).map((b) => b.sha256).filter((s) => this.blobs.has(s));
    noteDraftCount(this.branchKey, this.pendingCount);
    await writeDraft('cloud', user, toStoredPending({ branch: this.branchKey, baseCommitId: this.baseCommitId, baseSeq: this.baseSeq, config: input, count: this.pendingCount, now: new Date().toISOString(), blobs }));
    await this.#retain(used);
  }
  /** drop the stored draft + its bytes (memory too) */
  #dropDraft() {
    this.#autosave.cancel();
    noteDraftCount(this.branchKey, 0);
    void this.blobs.clear().then(() => clearDraft('cloud', draftUser(), this.branchKey));
  }
  /** drop pending bytes nothing references (except ones put in the last 10 s: the config may not point at them yet) */
  async #retain(used: Set<string>) {
    const now = Date.now();
    for (const [k, t] of this.#fresh) if (now - t > 10_000) this.#fresh.delete(k);
    await this.blobs.retain([...used, ...this.#fresh.keys()]);
  }
  async #offerStored() {
    const r = await readDraft('cloud', draftUser(), this.branchKey, this.headCommit?.commitId ?? null);
    if (!r) return;
    if (r.validity === 'valid') {
      this.restoreOffer = { stored: r.draft, state: 'same' };
      return;
    }
    // made on an older head: never offered — discarded at once, with a one-time notice
    this.staleNotice = { baseSeq: r.draft.baseSeq, headSeq: this.headCommit?.seq ?? null, count: r.draft.count, savedAt: r.draft.savedAt };
    noteDraftCount(this.branchKey, 0);
    await clearDraft('cloud', draftUser(), this.branchKey);
  }
  #otherTab(p: StoredPending | null) {
    if (!p || this.pendingCount > 0 || this.restoreOffer) return;
    if ((p.baseCommitId ?? null) === (this.headCommit?.commitId ?? null)) {
      this.#adoptStored(p, this.headConfig, this.headCommit?.commitId ?? null, this.headCommit?.seq ?? null);
      void this.#restoreBytes(p);
    }
  }
  /** a stored ConfigInput on top of `base` → working config + secret ops (values: base unless an op says otherwise) */
  #adoptStored(st: StoredPending, base: Config, baseId: string | null, baseSeq: number | null) {
    const ops: Record<string, SecretOp> = {};
    const secrets: SecretDef[] = st.config.secrets.map((s) => {
      const { plainValue, generateNow, ...def } = s;
      const b = base.secrets.find((x) => x.secretSlug === s.secretSlug);
      let value: string | null = b?.value ?? null;
      if (this.kind === 'deployment') {
        if (generateNow) {
          ops[s.secretSlug] = { generateNow: true };
          value = MASK;
        } else if (plainValue === null) {
          ops[s.secretSlug] = { plainValue: null };
          value = null;
        }
      } else value = null;
      return { ...def, value, rotatedAt: b?.rotatedAt ?? null } as SecretDef;
    });
    this.baseConfig = clone(base);
    this.baseCommitId = baseId;
    this.baseSeq = baseSeq;
    this.secretOps = ops;
    this.config = this.normalize({ variables: clone(st.config.variables), files: clone(st.config.files), sites: clone(st.config.sites), secrets });
  }
  /** Restore the offered draft (made on the current head: config + bytes) */
  async acceptRestore(): Promise<void> {
    const o = this.restoreOffer;
    if (!o) return;
    const st = o.stored;
    try {
      this.restoreOffer = null;
      this.#adoptStored(st, this.headConfig, this.headCommit?.commitId ?? null, this.headCommit?.seq ?? null);
      await this.#restoreBytes(st);
      toast.success(`${this.pendingCount} change${this.pendingCount === 1 ? '' : 's'} restored`, st.droppedValues.length ? `Re-enter the value of ${st.droppedValues.join(', ')} (secret values are never stored in the browser).` : 'Not committed yet — commit them with a message.');
    } catch (e) {
      toast.error(e, 'Could not restore the changes');
    } finally {
      this.persist();
    }
  }
  discardRestore() {
    this.restoreOffer = null;
    this.#dropDraft();
    this.lostBytes = [];
  }
  /** drop every pending change (memory + storage) */
  /** bumped by every Discard: editors with in-flight (debounced) edits drop them (VolumeContentPage → ws.abandon) */
  discards = $state(0);
  discardPending() {
    this.discards++;
    this.config = clone(this.headConfig);
    this.baseConfig = clone(this.headConfig);
    this.baseCommitId = this.headCommit?.commitId ?? null;
    this.baseSeq = this.headCommit?.seq ?? null;
    this.secretOps = {};
    this.saveIssues = [];
    this.rebaseState = null;
    this.#dropDraft();
    this.#fresh.clear();
    this.lostBytes = [];
  }

  /**
   * Rebase the pending set onto the current head (J6). Clean → applied (returns true). Conflicts → `rebaseState`
   * (the RebaseDialog asks; `then` runs after the choices were applied) and returns false.
   */
  rebase(choices?: MergeChoice[], then?: () => void): boolean {
    const mine = snap(this.working());
    const head = clone(this.headConfig);
    const r = rebaseChanges(this.baseConfig, mine, head, choices ?? []);
    if (!r.clean && !choices) {
      this.rebaseState = { conflicts: r.conflicts, clean: r.taken.length, headLabel: `commit ${this.headCommit?.seq ?? '—'}`, then };
      return false;
    }
    const slugs = new Set(r.config.secrets.map((s) => s.secretSlug));
    const ops: Record<string, SecretOp> = {};
    for (const [k, v] of Object.entries(this.secretOps)) if (slugs.has(k)) ops[k] = v;
    // the result holds the head's masked values; re-mark the pending value ops
    for (const s of r.config.secrets) {
      const op = ops[s.secretSlug];
      if (op) s.value = op.generateNow || typeof op.plainValue === 'string' ? MASK : null;
    }
    this.config = this.normalize(r.config);
    this.secretOps = ops;
    this.baseConfig = clone(head);
    this.baseCommitId = this.headCommit?.commitId ?? null;
    this.baseSeq = this.headCommit?.seq ?? null;
    this.rebaseState = null;
    void this.blobs.rebase(this.baseCommitId);
    toast.success(`Rebased onto commit ${this.headCommit?.seq ?? '—'}`, `${r.taken.length} of your change${r.taken.length === 1 ? '' : 's'} re-applied — not committed yet.`);
    this.persist();
    return true;
  }
  /** RebaseDialog → apply the choices */
  resolveRebase(choices: MergeChoice[]) {
    const then = this.rebaseState?.then;
    this.rebase(choices);
    then?.();
  }

  /**
   * Commit the pending set (message required). Throws the API error (409 stale is handled by the caller via
   * `refreshAndRebase`).
   */
  async commit(message: string): Promise<Commit> {
    this.committing = true;
    this.saveIssues = [];
    try {
      const input = this.commitInput();
      const baseCommitId = this.baseCommitId;
      // §21 #107 one transaction: stage the new blobs (only those the store lacks), then commit with the stageId
      const needs = newConfigBlobs(this.baseConfig, input);
      const archives = new Set<string>();
      for (const s of input.sites) for (const v of s.config.volumes ?? []) if (v.seed?.kind === 'archive') archives.add(v.seed.blobSha256);
      const c = await commitWithStage({
        needs,
        bytesOf: (sha) => this.blobs.get(sha),
        transport: stageTransport(archives),
        commit: (stageId) => this.postCommit({ message, baseCommitId, config: input, ...(stageId ? { stageId } : {}) }),
        onProgress: (_p, text) => (this.commitProgress = text),
      });
      this.secretOps = {};
      this.#dropDraft();
      this.#fresh.clear();
      this.lostBytes = [];
      // the head moved: with no pending changes the working config follows it
      this.config = clone(this.baseConfig); // pendingCount → 0 before the head arrives
      await this.fetch();
      this.afterCommit();
      return c;
    } catch (e) {
      if (e instanceof LostBytesError) this.lostBytes = this.#blobUsers(new Set(e.missing.map((m) => m.sha256)));
      this.saveIssues = errIssues(e);
      throw e;
    } finally {
      this.committing = false;
      this.commitProgress = null;
    }
  }
  protected afterCommit() {}
  /** after a 409 stale: fetch the new head, rebase. true = clean (commit again), false = conflicts (rebaseState) */
  async refreshAndRebase(then?: () => void): Promise<boolean> {
    await this.fetch();
    return this.stale ? this.rebase(undefined, then) : true;
  }

  // ───── sites (edited in place inside the pending set)
  /** the working site */
  serverSite(slug: string): Site | null {
    return this.config.sites.find((s) => s.siteSlug === slug) ?? null;
  }
  headSite(slug: string): Site | null {
    return this.baseConfig.sites.find((s) => s.siteSlug === slug) ?? null;
  }
  /** kept for the editors: the working sites are edited in place */
  openSite(_slug: string) {
    void _slug;
  }
  /** the site differs from the base head (changed, not committed) */
  siteDirty(slug: string): boolean {
    const d = this.serverSite(slug);
    const h = this.headSite(slug);
    if (!d) return false;
    return !h || !same(stripEmptyDevTools(snap(d)), stripEmptyDevTools(snap(h)));
  }
  /** back to the base head's version of the site (drops its pending changes) */
  discardSite(slug: string) {
    const h = this.headSite(slug);
    const i = this.config.sites.findIndex((s) => s.siteSlug === slug);
    if (h && i >= 0) this.config.sites[i] = clone(h);
    this.saveIssues = [];
  }
  /** the site is already in the working config (edited in place) — kept for the editors' "Apply" */
  async saveSite(slug: string): Promise<boolean> {
    return !!this.serverSite(slug);
  }

  // ───── sections (applied into the pending set — nothing is sent until Commit)
  async saveSection(section: SectionKey, items: unknown[], success = 'Applied'): Promise<boolean> {
    this.saveIssues = [];
    const list = clone(items);
    if (section === 'secrets') this.#applySecrets(list as SecretDefInput[]);
    else if (section === 'sites') this.config.sites = this.normalize({ ...emptyConfig(), sites: list as Site[] }).sites; else if (section === 'files') this.config.files = this.normalize({ ...emptyConfig(), files: list as Config['files'] }).files;
    else this.config.variables = list as Config['variables'];
    toast.success(success, 'Not committed yet — Commit… records it with a message.');
    return true;
  }
  #applySecrets(items: SecretDefInput[]) {
    const ops = { ...this.secretOps };
    const prev = this.config.secrets;
    this.config.secrets = items.map((s) => {
      const { plainValue, generateNow, ...def } = s;
      const old = prev.find((x) => x.secretSlug === s.secretSlug);
      const baseHas = this.baseConfig.secrets.find((x) => x.secretSlug === s.secretSlug);
      let value: string | null = old?.value ?? null;
      if (this.kind === 'deployment') {
        if (generateNow) {
          ops[s.secretSlug] = { generateNow: true };
          value = MASK;
        } else if (plainValue !== undefined) {
          ops[s.secretSlug] = { plainValue };
          value = plainValue === null || plainValue === '' ? null : MASK;
        } else if (!old && !baseHas && def.generate) {
          // a new secret with a generate rule gets its value at the commit (as at create)
          ops[s.secretSlug] = { generateNow: true };
          value = MASK;
        }
      } else value = null;
      return { ...def, value, rotatedAt: old?.rotatedAt ?? null } as SecretDef;
    });
    for (const k of Object.keys(ops)) if (!items.some((s) => s.secretSlug === k)) delete ops[k];
    this.secretOps = ops;
  }
  /** pending value change of a secret ("new value — not committed") */
  secretPending(slug: string): SecretOp | null {
    return this.secretOps[slug] ?? null;
  }

  // ───── helpers for editors
  /** secrets as SecretDefInput (keep values) for saveSection('secrets') */
  secretInputs(list: SecretDef[] = this.config.secrets): SecretDefInput[] {
    return list.map((s) => {
      const { value: _v, rotatedAt: _r, ...def } = s;
      void _v;
      void _r;
      return { ...def };
    });
  }
}

// ───────────────────────────────────────────── template
export class TemplateScope extends ConfigScope {
  readonly kind = 'template' as const;
  template: Template | null = $state(null);
  get base() {
    return `/app/templates/${this.id}`;
  }
  get label() {
    return this.template?.label ?? '';
  }
  get deploymentId() {
    return 'template';
  }
  get canWrite() {
    return session.can('templates', 'write');
  }
  get canWriteSecrets() {
    return this.canWrite;
  }
  get canCommit() {
    return this.canWrite;
  }
  get branchRef(): BranchRef {
    return { kind: 'template', templateId: this.id };
  }
  get branchState(): BranchState | null {
    return this.template?.branch ?? null;
  }
  async refresh() {
    await this.fetch().catch((e) => toast.error(e));
  }
  protected postCommit(body: { message: string; baseCommitId: string | null; config: ConfigInput; stageId?: string }) {
    return api.commitTemplate({ templateId: this.id, body });
  }
  protected async commitConfig(commitId: string): Promise<Config> {
    const r = await api.getTemplateCommit({ templateId: this.id, commitId });
    return configFromView(r.commit.config);
  }
  protected async fetch() {
    const t = await api.getTemplate({ templateId: this.id });
    this.template = t;
    this.version = t.version;
    this.adoptHead({ variables: t.variables, secrets: t.secrets.map((s) => ({ ...s, value: null })), files: t.files, sites: t.sites }, t.branch?.head ?? null);
  }
  /** template DETAILS (label / icon / description) — not config, saved directly */
  async saveMeta(meta: { label?: string; icon?: string | null; description?: string }): Promise<boolean> {
    this.saving = true;
    this.saveIssues = [];
    try {
      const r = await api.patchTemplate({ templateId: this.id, body: { ifVersion: this.version, ...meta } });
      this.template = r.template;
      this.version = r.template.version;
      this.lastReport = r.validation.issues;
      toast.success('Details saved');
      return true;
    } catch (e) {
      const cur = conflictVersion(e);
      if (cur !== null) {
        this.conflict = {
          current: cur,
          reload: async () => {
            await this.fetch();
            this.conflict = null;
          },
          keep: async () => {
            await this.fetch();
            this.conflict = null;
            await this.saveMeta(meta);
          },
        };
        return false;
      }
      this.saveIssues = errIssues(e);
      toast.error(e, 'Not saved');
      return false;
    } finally {
      this.saving = false;
    }
  }
}

// ───────────────────────────────────────────── deployment
export function configFromView(v: ConfigView): Config {
  return { ...v, secrets: v.secrets.map(({ isSet, ...s }) => ({ ...s, value: isSet ? MASK : null })) } as Config;
}

export class DeploymentScope extends ConfigScope {
  readonly kind = 'deployment' as const;
  deployment: DeploymentView | null = $state(null);
  /** head config + seq for "changed since head" markers (= headConfig) */
  head: { config: Config; seq: number } | null = $state(null);
  headLoaded = $state(true);
  status: StatusResponse | null = $state(null);
  get base() {
    return `/app/deployments/${this.id}`;
  }
  get label() {
    return this.deployment?.label ?? '';
  }
  get deploymentId() {
    return this.id;
  }
  get target() {
    const d = this.deployment;
    return d ? { deploymentId: d.deploymentId, tagSlugs: d.tagSlugs, managerAccountUids: d.managerAccountUids } : null;
  }
  can(resource: Parameters<typeof session.can>[0], action: Parameters<typeof session.can>[1]) {
    return session.can(resource, action, this.target);
  }
  get canWrite() {
    return this.can('deployment.config', 'write') || this.can('deployment.config', 'publish');
  }
  get canWriteSecrets() {
    return this.can('deployment.secrets', 'write');
  }
  get canCommit() {
    return this.can('deployment.config', 'publish');
  }
  get branchRef(): BranchRef {
    return { kind: 'deployment', deploymentId: this.id };
  }
  get branchState(): BranchState | null {
    return this.deployment?.branch ?? null;
  }
  /** the pending set changes secret definitions or values (needs deployment.secrets.write) */
  get secretsChanged(): boolean {
    return this.pendingItems.some((i) => i.section === 'secrets');
  }
  protected postCommit(body: { message: string; baseCommitId: string | null; config: ConfigInput; stageId?: string }) {
    return api.commitDeployment({ deploymentId: this.id, body });
  }
  protected async commitConfig(commitId: string): Promise<Config> {
    const r = await api.getDeploymentCommit({ deploymentId: this.id, commitId });
    return configFromView(r.commit.config);
  }
  protected async fetch() {
    const d = await api.getDeployment({ deploymentId: this.id });
    this.deployment = d;
    this.version = d.draftVersion;
    const head = d.branch?.head ?? null;
    this.adoptHead(configFromView(d.draft), head);
    this.head = head ? { config: this.headConfig, seq: head.seq } : null;
    void this.#loadSiteImages(head?.commitId ?? null);
  }
  /** §21 #113: the site images (digest "") the head runs — from the head commit's catalog (not in the cloud catalog) */
  async #loadSiteImages(commitId: string | null) {
    const missing = (this.headConfig?.sites ?? []).some((s) => s.config.containers.some((c) => !catalog.image(c.serviceSlug, c.version)));
    if (!commitId || !missing) {
      this.siteImages = null;
      return;
    }
    try {
      const r = await api.getDeploymentCommit({ deploymentId: this.id, commitId });
      const cat = r.commit.catalog;
      if (!cat) return;
      const images = Object.fromEntries(Object.entries(cat.images).filter(([, im]) => !im.digest));
      this.siteImages = Object.keys(images).length ? { services: cat.services, images, notFoundPages: {} } : null;
    } catch {
      /* the editor then shows the image as unknown */
    }
  }
  async refresh() {
    await this.fetch().catch((e) => toast.error(e));
  }
  /** kept for callers: the head config is part of the deployment read now */
  async loadHead(_force = false) {
    void _force;
  }
  async loadDetected() {
    if (!this.can('deployment.setup', 'read')) {
      await this.loadStatus();
      this.#detectedFromStatus();
      return;
    }
    try {
      const r = await api.getSetup({ deploymentId: this.id });
      const out: Record<string, string> = {};
      for (const s of r.sites) {
        const addr = s.agent?.advertiseHost || s.agent?.detectedAddresses?.[0];
        if (addr) out[s.siteSlug] = addr;
      }
      this.detected = out;
    } catch {
      /* optional */
    }
  }
  statusError = $state(false);
  async loadStatus() {
    try {
      this.status = await api.getStatus({ deploymentId: this.id });
      this.statusError = false;
      this.#detectedFromStatus();
    } catch {
      this.statusError = true;
    }
  }
  #detectedFromStatus() {
    const out: Record<string, string> = {};
    for (const s of this.status?.sites ?? []) {
      const addr = s.advertiseHost || s.detectedAddresses?.[0];
      if (s.enrolled && addr) out[s.siteSlug] = addr;
    }
    if (Object.keys(out).length) this.detected = { ...this.detected, ...out };
  }
  certsOf(siteSlug: string): CertStatus[] {
    return this.status?.sites.find((s) => s.siteSlug === siteSlug)?.edge?.certs ?? [];
  }
}

export type AnyScope = TemplateScope | DeploymentScope;
export type { Account };
