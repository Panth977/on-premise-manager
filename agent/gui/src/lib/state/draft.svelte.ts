/**
 * §21 #105 the BROWSER change set of this site (docs-v2/branches-commits.md §15 J4–J6, J11): every edit on Variables,
 * Secrets, Files and Config pages adds up in ONE pending set; nothing reaches the agent until **Commit** (message
 * required) sends `{ message, baseCommitId, config }` — the agent applies it with the update engine and writes the commit
 * only when the apply succeeds (a failed apply: no commit, the changes stay here). There is no agent working copy.
 * `base` = the head (GET /_opm/api/config), `work` = what the pages edit. Secrets use plainValue semantics (absent = keep).
 * §21 #107 local DRAFTS: the set (config + the BYTES of new File content) auto-saves to IndexedDB (ui drafts.ts /
 * PendingBlobs, database opm-drafts) per USER = the declared actor email, debounced; typed secret values never touch
 * storage. After a refresh / coming back, once the person is declared, a draft made on the CURRENT head is offered
 * (Restore / Discard); a draft made on another head is discarded automatically with a one-time notice (no stale
 * rebase). Drafts expire 1 h after the agent GUI was last open (heartbeat while a tab is open), are deleted at logout
 * and when another person is declared in this browser. In-session, a head that moves under pending changes rebases
 * them (shared merge engine; conflicts → the Rebase dialog), and a 409 stale at Commit does the same.
 * Nothing is uploaded before Commit; Commit stages the new blobs (POST /_opm/api/stages → PUT …/blobs/{sha}) and sends
 * { message, baseCommitId, config, stageId } (commitWithStage).
 * §21 #126 ONE change set PER SITE: every site of the sidebar tree edits its OWN branch (variables, secrets and files
 * included — an agent branch holds the shared sections + its site). `draft` is the set of the page's site; another
 * site's set loads that site's head and commits on that site (/_opm/api/sites/{site}/… relayed over the mesh).
 */
import type { AgentConfigResponse, Config, ConfigInput, ConfigView, DiffChange, ImageSpec, SecretDefInput, ValidationIssue } from '@opm/shared/types';
import { MASK } from '@opm/shared/types';
import { diffConfigs } from '$shared/diff.js';
import { rebaseChanges, uncommittedItems, type MergeChoice, type MergeItem } from '@opm/shared/branch';
import { toStoredPending, type StoredPending } from '@opm/shared/pending';
import { autosaver, clearDraft, endDrafts, initDrafts, readDraft, watchDraft, writeDraft, type DraftsSession } from '$opm/branch/drafts';
import { PendingBlobs } from '$opm/branch/pending-blobs';
import { configBlobs } from '@opm/shared/staging';
import { checkLocalEdit, normalizeEdge, validateConfig, type CatalogImage, type CatalogService } from '@opm/shared/validate';
import { api, apiPath } from '../api';
import type { AGENT_API } from '@opm/shared/agent-api';
import { page } from '$app/state';
import { session } from './session.svelte';

const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T;

export function viewToInput(v: ConfigView): ConfigInput {
  return {
    variables: clone(v.variables),
    files: clone(v.files),
    // round E: legacy edge shapes (pathPrefix, timeoutSec, missing headers / body limits) → current shape (pure, idempotent)
    sites: clone(v.sites).map((s) => ({ ...s, config: { ...s.config, edge: normalizeEdge(s.config.edge as Parameters<typeof normalizeEdge>[0]) } })),
    secrets: v.secrets.map((s) => ({ secretSlug: s.secretSlug, label: s.label, description: s.description, required: s.required, generate: s.generate ? { ...s.generate } : null })),
  };
}
/** a ConfigInput as a masked Config (secret value MASK when set: a typed / generated value, else the reference's isSet) */
export function inputToConfig(input: ConfigInput, ref: ConfigView | null): Config {
  return {
    variables: input.variables,
    files: input.files,
    sites: input.sites,
    secrets: input.secrets.map((s) => {
      const b = ref?.secrets.find((x) => x.secretSlug === s.secretSlug);
      const set = s.generateNow ? true : s.plainValue !== undefined ? s.plainValue !== null && s.plainValue !== '' : !!b?.isSet;
      return { secretSlug: s.secretSlug, label: s.label, description: s.description, required: s.required, generate: s.generate, value: set ? MASK : null, rotatedAt: b?.rotatedAt ?? null };
    }),
  };
}
/** back from a (rebased) Config to an input: secret definitions from `cfg`, value operations from `ops` (same slug) */
function configToInput(cfg: Config, ops: readonly SecretDefInput[]): ConfigInput {
  return {
    variables: clone(cfg.variables),
    files: clone(cfg.files),
    sites: clone(cfg.sites),
    secrets: cfg.secrets.map((s) => {
      const o = ops.find((x) => x.secretSlug === s.secretSlug);
      const def: SecretDefInput = { secretSlug: s.secretSlug, label: s.label, description: s.description, required: s.required, generate: s.generate ? { ...s.generate } : null };
      if (o?.plainValue !== undefined) def.plainValue = o.plainValue;
      if (o?.generateNow) def.generateNow = true;
      return def;
    }),
  };
}
export const viewToConfig = (v: ConfigView): Config => inputToConfig(viewToInput(v), v);

/** §21 #113: edits may use every image the site knows (head ∪ recent cloud commits ∪ drift-adopted ∪ site images) */
export function withKnownCatalog(c0: AgentConfigResponse): AgentConfigResponse {
  return c0.knownCatalog && c0.catalog ? { ...c0, catalog: { services: { ...c0.knownCatalog.services, ...c0.catalog.services }, images: { ...c0.knownCatalog.images, ...c0.catalog.images }, notFoundPages: { ...c0.knownCatalog.notFoundPages, ...c0.catalog.notFoundPages } } } : c0;
}

/** shared by every site's set: the agent the person is logged in to + the detected site addresses */
const shared = $state<{ self: string; detectedHosts: Record<string, string>; made: number }>({ self: '', detectedHosts: {}, made: 0 });
/**
 * A per-site store is made on first use — often while a page renders. Svelte does not track state made inside the
 * reaction that reads it, so that page would never see the store change: every proxy read also depends on `made`,
 * bumped right after a store is made, and the page renders once more (now tracking the new store).
 */
export function storeMade() {
  queueMicrotask(() => shared.made++);
}
export const madeVersion = () => shared.made;
/** the agent the person is logged in to (its sets use the plain /_opm/api/… endpoints) */
export const selfSlug = () => shared.self || session.info?.siteSlug || '';
export function setSelfSite(s: string) {
  if (s && shared.self !== s) shared.self = s;
}

export interface RebaseUi {
  conflicts: MergeItem[];
  clean: number;
  headLabel: string;
  resolve: (choices: MergeChoice[]) => void;
  cancel: () => void;
}

class DraftStore {
  constructor(siteSlug: string) {
    this.siteSlug = siteSlug;
  }
  /** §21 #126 the site this set edits when it is not the agent the person is logged in to (null = this agent) */
  get remote(): string | null {
    const self = selfSlug();
    return this.siteSlug && self && this.siteSlug !== self ? this.siteSlug : null;
  }
  /** the head config of this set's site (+ the images it knows) */
  async loadHead(): Promise<AgentConfigResponse> {
    // this agent not known yet: ask by site name (this agent answers its own /sites/{self}/config too)
    const r = this.remote ?? (selfSlug() ? null : this.siteSlug || null);
    return withKnownCatalog(r ? await api.siteConfig({ siteSlug: r }) : await api.config({}));
  }
  /** [op, path] of a head file's content on this set's site (fetchText / download) */
  fileContentPath(fileSlug: string): [keyof typeof AGENT_API, string] {
    const r = this.remote;
    return r ? ['siteGetFileContent', apiPath('siteGetFileContent', { siteSlug: r, fileSlug })] : ['getFileContent', apiPath('getFileContent', { fileSlug })];
  }
  revealSecret(secretSlug: string) {
    const r = this.remote;
    return r ? api.siteRevealSecret({ siteSlug: r, secretSlug }) : api.revealSecret({ secretSlug });
  }
  getUpdate(updateId: string) {
    const r = this.remote;
    return r ? api.siteGetUpdate({ siteSlug: r, updateId }) : api.getUpdate({ updateId });
  }
  /** §21 #107 the draft owner = the declared actor email (null until declared: memory only) */
  user = $state<string | null>(null);
  #drafts: DraftsSession | null = null;
  /** pending bytes of this site's change set (memory + IndexedDB), per user + branch key */
  #pb: PendingBlobs | null = null;
  /** bumps when pending bytes change (reactive consumers) */
  blobsTick = $state(0);
  /** sha256s whose bytes a restored draft needed but this browser no longer has */
  lostBytes = $state<string[]>([]);
  get blobs(): PendingBlobs {
    const user = this.user ?? '~';
    if (!this.#pb || this.#pb.branchKey !== this.branchKey || this.#pb.user !== user) {
      const old = this.#pb;
      this.#pb = new PendingBlobs('agent', user, this.branchKey);
      this.#pb.onchange = () => this.blobsTick++;
      // carry the bytes over (a person declared / the site known later): they are part of the in-memory set
      if (old && old.branchKey === this.branchKey) for (const sha of old.shas()) void this.#pb.putKnown(sha, old.get(sha)!, this.headId);
    }
    return this.#pb;
  }
  /** keep new content bytes in the change set (nothing is uploaded before Commit) → the BlobRef for FileDef.content */
  async putBytes(bytes: Uint8Array): Promise<{ sha256: string; sizeBytes: number }> {
    return this.blobs.put(bytes, this.headId);
  }
  /** pending bytes of a blob (undefined = not pending: the agent has it) */
  bytesOf(sha: string): Uint8Array | undefined {
    return this.blobs.get(sha);
  }
  /** pending sha256s the working set references */
  pendingShas(): string[] {
    if (!this.work) return [];
    const pb = this.blobs;
    return configBlobs(this.work).map((b) => b.sha256).filter((s) => pb.has(s));
  }
  /** the head the pages edit on */
  base = $state<AgentConfigResponse | null>(null);
  /** the browser change set (head + every pending edit) */
  work = $state<ConfigInput | null>(null);
  deploymentId = $state('');
  siteSlug = $state('');
  /** a stored draft from an earlier visit / another tab ('same' → Restore / Discard) or the notice of an auto-discarded one ('stale') */
  offer = $state<{ stored: StoredPending; state: 'same' | 'stale'; headSeq?: number | null } | null>(null);
  /** Commit sent: the agent applies it (the commit exists only when the run succeeds) */
  applying = $state<{ updateId: string; message: string } | null>(null);
  /** a rebase / drift-first flow is moving the base: head reloads must not turn the set into a stale offer */
  holding = $state(false);
  /** conflicts of a running rebase (the layout shows the Rebase dialog) */
  rebaseUi = $state<RebaseUi | null>(null);
  /** fileSlug → local text kept for the editor after "Use this content" (display only) */
  fileText = $state<Record<string, string>>({});
  /**
   * siteSlug → the agent's advertised / detected address (auto hosts, SPEC §21 #51): this agent + its peers, from
   * OverviewResponse.sites[].advertiseHost. Sites without a known address stay out (agent mode: E_NULL when used).
   * Shared by every site's set.
   */
  get detectedHosts(): Record<string, string> {
    return shared.detectedHosts;
  }
  set detectedHosts(v: Record<string, string>) {
    shared.detectedHosts = v;
  }
  /**
   * §21 #93: head text of files loaded on demand (container editor / containers list: implicit + suggested depends in
   * mounted files), keyed by fileSlug with the head content ref it belongs to (a new head invalidates it).
   */
  headFileText = $state<Record<string, { key: string; text: string }>>({});
  /** fileSlug → current text (local edit, else the loaded head text) — only files whose text is known */
  fileContents = $derived.by((): Record<string, string> => {
    const out: Record<string, string> = {};
    for (const f of this.work?.files ?? []) {
      const head = this.base?.config.files.find((x) => x.fileSlug === f.fileSlug);
      const key = JSON.stringify(head?.content ?? null);
      const changed = JSON.stringify(f.content ?? null) !== key;
      if (changed) {
        if (this.fileText[f.fileSlug] !== undefined) out[f.fileSlug] = this.fileText[f.fileSlug];
      } else if (this.headFileText[f.fileSlug]?.key === key) out[f.fileSlug] = this.headFileText[f.fileSlug].text;
    }
    return out;
  });

  #baseInputJson = $derived(this.base ? JSON.stringify(viewToInput(this.base.config)) : '');
  /** the browser holds changes (not committed) */
  dirty = $derived(!!this.work && JSON.stringify(this.work) !== this.#baseInputJson);
  /** alias (the set differs from the head) */
  get edited() {
    return this.dirty;
  }
  get branchKey(): string {
    return `agent:${this.deploymentId}/${this.siteSlug}`;
  }
  get headId(): string | null {
    return this.base?.head?.revId ?? null;
  }

  /** the pending items vs the head (+ secret value changes, which masked configs cannot show) */
  pendingItems = $derived.by((): MergeItem[] => {
    if (!this.base || !this.work || !this.dirty) return [];
    const before = this.asConfig('base');
    const after = this.asConfig('work');
    if (!before || !after) return [];
    let items: MergeItem[] = [];
    try {
      items = uncommittedItems(before, after, { secretValues: false });
    } catch {
      items = [];
    }
    const keys = new Set(items.map((i) => i.key));
    for (const s of this.work.secrets) {
      if (keys.has(`secrets/${s.secretSlug}`)) continue;
      if (s.plainValue === undefined && !s.generateNow) continue;
      const was = !!this.base.config.secrets.find((x) => x.secretSlug === s.secretSlug)?.isSet;
      const now = s.generateNow || (s.plainValue !== null && s.plainValue !== '');
      if (!was && !now) continue;
      items.push({ key: `secrets/${s.secretSlug}/value`, section: 'secrets', siteSlug: null, label: `Secret ${s.secretSlug} value${s.generateNow ? ' (generate a new one)' : ''}`, kind: 'change', effect: !now ? 'remove' : was ? 'change' : 'add', base: null, target: { has: was, value: was ? MASK : null }, source: { has: now, value: now ? MASK : null }, preselect: true, scalar: true, requires: [], locked: null, sourceHash: '' });
    }
    return items;
  });
  /** number of pending changes (the change bar) */
  get pendingCount(): number {
    return this.dirty ? Math.max(1, this.pendingItems.length) : 0;
  }

  /**
   * Adopt a fresh head config. With pending changes made on another head (an update / merge / someone's commit on this
   * site while the tab is open) the changes are rebased onto the new head (clean → applied; conflicts → the Rebase
   * dialog; cancelled → discarded with a notice).
   */
  adopt(cfg: AgentConfigResponse, deploymentId: string, siteSlug = this.siteSlug, force = false) {
    if (!force && (this.applying || this.holding) && this.base) return;
    this.deploymentId = deploymentId;
    if (siteSlug) this.siteSlug = siteSlug;
    const sameHead = this.base?.head?.revId === cfg.head?.revId;
    if (!force && this.work && this.dirty) {
      if (sameHead) {
        this.base = cfg;
        return;
      }
      void this.#rebaseInSession(cfg);
      return;
    }
    const firstLoad = !this.base;
    this.base = cfg;
    this.work = viewToInput(cfg.config);
    this.fileText = {};
    if (firstLoad || !sameHead) void this.checkStored();
    this.#watch();
  }
  /** §21 #113: a site image just resolved (docker reference / host image) is usable by the edits at once */
  addSiteImage(key: string, image: ImageSpec, service: { serviceSlug: string; label: string; icon: string | null }) {
    if (!this.base) return;
    const cat = this.base.catalog ?? { services: {}, images: {}, notFoundPages: {} };
    this.base = { ...this.base, catalog: { ...cat, images: { ...cat.images, [key]: image }, services: { ...cat.services, [service.serviceSlug]: cat.services[service.serviceSlug] ?? service } } };
  }
  async #rebaseInSession(cfg: AgentConfigResponse) {
    if (!this.base || !this.work) return;
    this.holding = true;
    try {
      const oldBase = this.base.config;
      const mine = $state.snapshot(this.work) as ConfigInput;
      const before = this.#toStored();
      const next = await this.rebaseInput(oldBase, mine, cfg, `commit ${cfg.head?.seq ?? '—'}`);
      this.base = cfg;
      if (next) {
        this.work = next;
        void this.blobs.rebase(cfg.head?.revId ?? null);
      } else {
        this.work = viewToInput(cfg.config);
        this.fileText = {};
        if (before) this.offer = { stored: before, state: 'stale', headSeq: cfg.head?.seq ?? null }; // notice: discarded
        void this.blobs.clear();
      }
      this.persist();
    } finally {
      this.holding = false;
    }
  }
  /**
   * Look for a stored draft of this site (the person is declared and the head is known): made on the current head →
   * offered (Restore / Discard) unless this tab already has changes; made on another head → deleted + one-time notice.
   */
  async checkStored() {
    if (!this.user || !this.base || !this.deploymentId || !this.siteSlug) return;
    const user = this.user;
    const branch = this.branchKey;
    const r = await readDraft('agent', user, branch, this.headId);
    if (!r || this.user !== user || this.branchKey !== branch) return;
    if (r.validity === 'stale') {
      await clearDraft('agent', user, branch);
      this.offer = { stored: r.draft, state: 'stale', headSeq: this.base.head?.seq ?? null };
      return;
    }
    if (!this.dirty) this.offer = { stored: r.draft, state: 'same' };
  }
  /**
   * The declared person changed (layout): the previous person's drafts are deleted (endDrafts), the new person's
   * drafts session starts (expiry check + heartbeat) and their stored draft is offered.
   */
  async setUser(email: string | null) {
    if (email === this.user) return;
    const prev = this.user;
    if (prev) await endDrafts('agent', prev, this.#drafts).catch(() => undefined);
    this.#drafts = null;
    this.user = email;
    if (!email) return;
    try {
      this.#drafts = await initDrafts('agent', email);
    } catch {
      this.#drafts = null;
    }
    this.#unwatch?.();
    this.#watchedKey = '';
    this.#watch();
    if (this.dirty) this.persist(); // edits made before the person was declared are kept for them
    else await this.checkStored();
  }
  /** logout: stop the heartbeat, delete this person's drafts (memory set dropped by discard()) */
  async endSession() {
    const u = this.user;
    this.#saver.cancel();
    if (u) await endDrafts('agent', u, this.#drafts).catch(() => undefined);
    this.#drafts = null;
    this.user = null;
    this.offer = null;
  }
  #unwatch: (() => void) | null = null;
  #watchedKey = '';
  #watch() {
    if (!this.user || !this.deploymentId || !this.siteSlug) return;
    const key = `${this.user}|${this.branchKey}`;
    if (this.#watchedKey === key) return;
    this.#unwatch?.();
    this.#watchedKey = key;
    // another tab wrote / cleared this site's draft: offer it (never overwrite edits made in this tab)
    this.#unwatch = watchDraft('agent', this.user, this.branchKey, (st) => {
      if (this.dirty) return;
      this.offer = st && st.baseCommitId === this.headId && st.count > 0 ? { stored: st, state: 'same' } : null;
    });
  }
  #toStored(): StoredPending | null {
    if (!this.work || !this.base) return null;
    return toStoredPending({ branch: this.branchKey, baseCommitId: this.headId, baseSeq: this.base.head?.seq ?? null, config: $state.snapshot(this.work) as ConfigInput, count: this.pendingCount, now: new Date().toISOString(), extra: Object.keys(this.fileText).length ? { fileText: $state.snapshot(this.fileText) } : undefined, blobs: this.pendingShas() });
  }
  #saver = autosaver(() => this.#save());
  async #save() {
    const user = this.user;
    if (!user || !this.deploymentId || !this.siteSlug || !this.base) return;
    if (this.dirty) {
      const st = this.#toStored();
      if (!st) return;
      await writeDraft('agent', user, st);
      void this.blobs.retain(configBlobs(this.work!).map((b) => b.sha256)); // bytes nothing references any more
    } else if (this.offer?.state !== 'same') {
      await clearDraft('agent', user, this.branchKey);
      if (this.blobs.size) void this.blobs.clear();
    }
  }
  /** called on every change of the set (layout effect): auto-save the draft (debounced) */
  persist() {
    if (!this.deploymentId || !this.siteSlug || !this.base) return;
    if (this.dirty && this.offer?.state === 'same') this.offer = null; // editing replaces an offered draft (the banner asked first)
    this.#saver.schedule();
  }
  /** write a pending auto-save now (page hide) */
  flush() {
    this.#saver.flush();
  }

  /** drop the pending changes (back to the head) — memory, the stored draft and its bytes */
  discard(cfg?: AgentConfigResponse) {
    if (cfg) this.base = cfg;
    if (this.base) this.work = viewToInput(this.base.config);
    this.fileText = {};
    this.lostBytes = [];
    this.#saver.cancel();
    if (this.user && this.deploymentId && this.siteSlug) void clearDraft('agent', this.user, this.branchKey);
    void this.blobs.clear();
  }
  /** Restore the offered draft (made on the current head): config + the file bytes */
  async restoreOffer(): Promise<boolean> {
    const o = this.offer;
    if (!o || !this.base || o.state !== 'same') return false;
    if ((o.stored.baseCommitId ?? null) !== this.headId) {
      // the head moved since the offer: the draft is stale now
      if (this.user) await clearDraft('agent', this.user, this.branchKey);
      this.offer = { stored: o.stored, state: 'stale', headSeq: this.base.head?.seq ?? null };
      return false;
    }
    const text = (o.stored.extra?.fileText ?? {}) as Record<string, string>;
    this.lostBytes = await this.blobs.restore(o.stored.blobs ?? []);
    this.work = clone(o.stored.config);
    this.fileText = { ...text };
    this.offer = null;
    return true;
  }
  /** Discard the offered draft (and its bytes) / dismiss the notice of an auto-discarded one */
  dropOffer() {
    const o = this.offer;
    this.offer = null;
    if (o?.state === 'same' && this.user) {
      void clearDraft('agent', this.user, this.branchKey);
      if (!this.dirty) void this.blobs.clear();
    }
  }
  /**
   * Rebase `mine` (made on `oldBase`) onto `head` with the shared merge engine; conflicts open the Rebase dialog
   * (rebaseUi). Returns the rebased input, or null when the person cancelled.
   */
  async rebaseInput(oldBase: ConfigView | null, mine: ConfigInput, head: AgentConfigResponse, headLabel: string): Promise<ConfigInput | null> {
    const b = oldBase ? viewToConfig(oldBase) : null;
    const m = inputToConfig(mine, oldBase ?? head.config);
    const h = viewToConfig(head.config);
    let r = rebaseChanges(b, m, h, [], { secretValues: false });
    if (!r.clean) {
      const choices = await new Promise<MergeChoice[] | null>((res) => {
        this.rebaseUi = { conflicts: r.conflicts, clean: r.taken.length, headLabel, resolve: (c) => ((this.rebaseUi = null), res(c)), cancel: () => ((this.rebaseUi = null), res(null)) };
      });
      if (!choices) return null;
      r = rebaseChanges(b, m, h, choices, { secretValues: false });
      if (!r.clean) return null;
    }
    return configToInput(r.config, mine.secrets);
  }
  /**
   * The head moved while this tab holds changes (409 stale at commit, a drift commit first): load the new head and
   * rebase the set onto it. Returns the new head seq, or null when cancelled / failed.
   */
  async moveToNewHead(): Promise<number | null> {
    if (!this.base || !this.work) return null;
    this.holding = true;
    try {
      const oldBase = this.base.config;
      const mine = $state.snapshot(this.work) as ConfigInput;
      const cfg = await this.loadHead();
      const next = await this.rebaseInput(oldBase, mine, cfg, `commit ${cfg.head?.seq ?? '—'}`);
      if (!next) return null;
      this.base = cfg;
      this.work = next;
      void this.blobs.rebase(cfg.head?.revId ?? null);
      this.persist();
      return cfg.head?.seq ?? null;
    } finally {
      this.holding = false;
    }
  }
  /** follow a committed run: success → the set is gone (it IS the head now); failure → nothing committed, keep it */
  followRun(updateId: string, message: string, done: (ok: boolean, msg: string) => void) {
    this.applying = { updateId, message };
    const tick = async () => {
      if (this.applying?.updateId !== updateId) return;
      let run: Awaited<ReturnType<typeof api.getUpdate>> | null = null;
      try {
        run = await this.getUpdate(updateId);
      } catch {
        run = null;
      }
      if (run?.finishedAt) {
        const ok = !!run.result?.ok;
        if (ok) {
          this.#saver.cancel();
          if (this.user) void clearDraft('agent', this.user, this.branchKey); // §21 #107 committed: draft + bytes gone
          void this.blobs.clear();
          this.offer = null;
          try {
            const cfg = await this.loadHead();
            this.applying = null;
            this.adopt(cfg, this.deploymentId, this.siteSlug, true);
          } catch {
            this.applying = null;
          }
        } else this.applying = null;
        done(ok, run.result?.message ?? '');
        return;
      }
      setTimeout(tick, 1500);
    };
    setTimeout(tick, 800);
  }

  secretInput(slug: string): SecretDefInput | undefined {
    return this.work?.secrets.find((s) => s.secretSlug === slug);
  }
  secretIsSet(slug: string): boolean {
    const w = this.secretInput(slug);
    const b = this.base?.config.secrets.find((s) => s.secretSlug === slug);
    if (w?.generateNow) return true;
    if (w && w.plainValue !== undefined) return w.plainValue !== null && w.plainValue !== '';
    return !!b?.isSet;
  }

  /** a Config shape for the shared validators (masked secret values) */
  asConfig(which: 'base' | 'work'): Config | null {
    if (!this.base || !this.work) return null;
    const src = which === 'base' ? viewToInput(this.base.config) : this.work;
    return {
      variables: src.variables,
      files: src.files,
      sites: src.sites,
      secrets: src.secrets.map((s) => {
        const b = this.base!.config.secrets.find((x) => x.secretSlug === s.secretSlug);
        const set = which === 'base' ? !!b?.isSet : this.secretIsSet(s.secretSlug);
        return { secretSlug: s.secretSlug, label: s.label, description: s.description, required: s.required, generate: s.generate, value: set ? MASK : null, rotatedAt: b?.rotatedAt ?? null };
      }),
    };
  }

  serviceSlug = (slug: string) => slug;

  /** client-side diff head → working copy */
  changes = $derived.by((): DiffChange[] => {
    if (!this.base || !this.work || !this.edited) return [];
    const after = this.asConfig('work');
    if (!after) return [];
    return diffConfigs(this.base.config, after, this.serviceSlug, (slug) => {
      const w = this.work!.secrets.find((s) => s.secretSlug === slug);
      return !!w && (w.generateNow === true || w.plainValue !== undefined);
    });
  });

  /** the head catalog in the shape of the shared ValidationContext (services by slug, images by `<service>:<version>`) */
  catalogCtx = $derived.by((): { services: Record<string, CatalogService>; images: Record<string, CatalogImage> } => {
    const services: Record<string, CatalogService> = {};
    const images: Record<string, CatalogImage> = {};
    const cat = this.base?.catalog;
    if (cat) {
      for (const [slug, s] of Object.entries(cat.services)) services[slug] = { label: s.label };
      for (const [k, im] of Object.entries(cat.images)) images[k] = { kind: im.kind, ports: im.ports, env: im.env, volumes: im.volumes, healthcheck: im.healthcheck };
    }
    return { services, images };
  });

  /** client-side validation (same functions as the agent): local-edit rules + validateConfig(mode agent) */
  issues = $derived.by((): ValidationIssue[] => {
    const after = this.asConfig('work');
    return after ? this.issuesFor(after) : [];
  });
  /** the same validation for a modified working copy (route editor: the edge with the edited route substituted) */
  issuesFor(after: Config): ValidationIssue[] {
    if (!this.base || !this.work) return [];
    const before = this.asConfig('base');
    if (!before) return [];
    const cat = this.base.catalog;
    const { services, images } = this.catalogCtx;
    const secretsSet = after.secrets.filter((s) => s.value !== null).map((s) => s.secretSlug);
    let list: ValidationIssue[] = [];
    const fc = this.fileContents;
    try {
      list = validateConfig(after, { mode: 'agent', deploymentId: this.deploymentId || 'deployment', services, images, notFoundPageIds: cat ? Object.keys(cat.notFoundPages) : undefined, secretsSet, detectedHosts: this.detectedHosts, fileContents: Object.keys(fc).length ? fc : undefined }).list;
    } catch (e) {
      list = [{ path: '', code: 'V_TYPE', message: e instanceof Error ? e.message : String(e), severity: 'error' }];
    }
    return [...checkLocalEdit(before, after), ...list];
  }
  errorCount = $derived(this.issues.filter((i) => i.severity === 'error').length);

  issuesAt(prefix: string): ValidationIssue[] {
    return this.issues.filter((i) => i.path === prefix || i.path.startsWith(prefix + '.') || i.path.startsWith(prefix + '['));
  }
}

/** §21 #126 one set per site, made on first use (the declared person carried over) */
const stores = new Map<string, DraftStore>();
let currentUser: string | null = null;
/**
 * A store must be made OUTSIDE any effect: its deriveds belong to the effect that is running when it is made and die
 * with it (a page that is left). A missing store is made in a microtask and the placeholder answers meanwhile (empty:
 * "loading"); the `made` bump renders the readers again.
 * The overview load makes every site's store up front (after its await: no effect running).
 */
const placeholder = new DraftStore('');
function makeStore(site: string): DraftStore {
  let d = stores.get(site);
  if (d) return d;
  d = new DraftStore(site);
  stores.set(site, d);
  storeMade();
  if (currentUser) void d.setUser(currentUser);
  return d;
}
export function draftFor(site: string): DraftStore {
  const d = stores.get(site);
  if (d) return d;
  // a component's setup is not "tracking" but still inside its effect: always make it in a microtask (no effect runs)
  queueMicrotask(() => makeStore(site));
  return placeholder;
}
/** make the stores of these sites now (call outside any effect) */
export function ensureDrafts(sites: string[]) {
  for (const s of sites) if (s) makeStore(s);
}
/** the set of a site if one was made (sidebar markers; never creates one) */
export const peekDraft = (site: string): DraftStore | undefined => (void shared.made, stores.get(site));
export const allDrafts = (): DraftStore[] => [...stores.values()];
/** the person declared / changed / logged out: every set follows */
export async function setDraftUser(email: string | null) {
  currentUser = email;
  await Promise.all(allDrafts().map((d) => d.setUser(email)));
}
export async function endDraftSessions() {
  currentUser = null;
  await Promise.all(allDrafts().map((d) => d.endSession()));
}
/** some site's set holds changes that are not committed */
export const anyDirty = (): boolean => allDrafts().some((d) => d.dirty);
/** the site of the page (a site of the tree), else the agent the person is logged in to */
export function pageSite(): string {
  const p = page.params.siteSlug;
  return p && p !== '*' ? p : selfSlug();
}

/** the change set of the page's site (every config page edits through this) */
export const draft: DraftStore = new Proxy({} as DraftStore, {
  get(_, k) {
    void shared.made;
    const d = draftFor(pageSite());
    const v = Reflect.get(d, k, d);
    return typeof v === 'function' ? v.bind(d) : v;
  },
  set(_, k, v) {
    const d = draftFor(pageSite());
    return Reflect.set(d, k, v, d);
  },
  has(_, k) {
    return k in draftFor(pageSite());
  },
});

/** Summary prefill from changes, e.g. "theme green → yellow" */
export function summarize(changes: DiffChange[]): string {
  const parts: string[] = [];
  for (const c of changes) {
    if (parts.length >= 3) break;
    const short = c.path.split('/').slice(-2).join(' ');
    if (c.section === 'variables' && c.kind === 'changed' && (typeof c.before !== 'object' || c.before === null)) parts.push(`${c.path} ${fmt(c.before)} → ${fmt(c.after)}`);
    else if (c.section === 'secrets') parts.push(`secret ${c.path} changed`);
    else if (c.section === 'files') parts.push(`file ${c.path} changed`);
    else if (c.section === 'volumes') parts.push(`volume ${c.path.split('/').pop()} ${c.kind}`);
    else parts.push(`${short} ${c.kind}`);
  }
  const more = changes.length - parts.length;
  const s = parts.join(', ') + (more > 0 ? ` (+${more} more)` : '');
  return s.slice(0, 200);
}
function fmt(v: unknown): string {
  if (v === null || v === undefined) return '—';
  if (typeof v === 'string') return v.length > 30 ? v.slice(0, 27) + '…' : v;
  return JSON.stringify(v).slice(0, 30);
}
