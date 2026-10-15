/**
 * §21 #104 mock of the agent branch API (BRANCH_AGENT_API): this site's branch (local commits = the migrated revision
 * chain), the deployment branch as the cloud knows it (2 commits ahead), uncommitted = ONLY drift (§21 #105: a
 * container created by hand on the site network, an edited per-site file copy) + one drift note (an edited rendered
 * file). GUI edits live in the browser: commitLocal { message, baseCommitId, config } runs the simulated update
 * (lib/mock/index.ts startRun) and the commit is written when the run finishes ok (like the real agent: only at COMMIT);
 * 409 uncommitted-drift while drift / notes are uncommitted, 409 stale when the head moved. putWorking → 410 gone.
 *
 * Knobs (localStorage):
 *   opm.mock.branch.clean=1      → no drift, no note (a site that can merge / commit at once).
 *   opm.mock.branch.failApply=1  → the next commit of GUI edits fails in APPLY (rollback, no commit; = opm.mock.fail=apply).
 */
import {
  applyMerge,
  applyRevert,
  secretValueChanges,
  checkCommitMessage,
  commitRef,
  commitSummaryOf,
  configHash,
  isFastForward,
  countMerge,
  makeCommit,
  nextSkips,
  planMerge,
  revertPlan,
  uncommittedItems,
  withReferenceSites,
  type AgentBranchState,
  type BranchRef,
  type Commit,
  type CommitAuthor,
  type CommitDetail,
  type DriftItem,
  type DriftNote,
  type LocalCommitRequest,
  type LocalCompareRequest,
  type LocalMergeRequest,
  type LocalRevertRequest,
  type MergeChoice,
  type MergeItem,
  type MergePlan,
  type MergePreview,
  type MergeSkip,
  type UncommittedResponse,
} from '@opm/shared/branch';
import { diffConfigs } from '$shared/diff.js';
import { newDevToolPreset } from '@opm/shared/devtools';
import type { Config, ConfigInput, ConfigView, ErrorCode, Revision, UpdateRun, ValidationIssue } from '@opm/shared/types';
import * as D from './data';

type Json = Record<string, unknown>;
export interface BranchDeps {
  head: () => Revision | null;
  revisions: () => Revision[];
  /** make a revision the head without a run (drift commits: nothing changes on the server) */
  adopt: (rev: Revision) => void;
  startRun: (rev: Revision, actor: string) => UpdateRun;
  getRun: (updateId: string) => UpdateRun | null;
  runningRun: () => UpdateRun | null;
  addActivity: (site: string, type: string, summary: string, actor: string | null, data?: Json) => void;
  maskConfig: (c: Config) => ConfigView;
  buildConfig: (input: ConfigInput, base: Config) => { config: Config };
  validate: (c: Config, before: Config, localEdit?: boolean) => ValidationIssue[];
  fail: (code: ErrorCode, message: string, details?: Json) => never;
  reachable: (site: string) => boolean;
}
type Ctx = { params: Record<string, string>; query: URLSearchParams; body: unknown };
type Handler = (c: Ctx, s: unknown, actor: string | null) => unknown;

const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T;
const ls = (k: string) => {
  try {
    return localStorage.getItem(k);
  } catch {
    return null;
  }
};
const LOCAL: BranchRef = { kind: 'agent', deploymentId: D.DEP, siteSlug: D.SELF };
const CLOUD: BranchRef = { kind: 'deployment', deploymentId: D.DEP };
const OPTS = { secretValues: true, scope: D.SELF };
const person = (email: string): CommitAuthor => ({ kind: 'person', email, uid: null, name: null, siteSlug: D.SELF });
const cloudPerson = (email: string, name: string): CommitAuthor => ({ kind: 'person', email, uid: 'uidOps0000000000000001', name, siteSlug: null });

export function createBranchMock(deps: BranchDeps): { handlers: Partial<Record<string, Handler>>; onRollback: (run: UpdateRun, message: string, snapshotId: string, actor: string) => void } {
  const now = () => Date.now();
  const local: Commit[] = [];
  const cloud: Commit[] = [];
  // the migrated revision chain = commits of both branches (same ids)
  for (const r of deps.revisions()) {
    const mk = (b: BranchRef, list: Commit[]) =>
      list.push({
        ...makeCommit({ commitId: r.revId, branch: b, parent: list.at(-1) ?? null, kind: 'migrated', message: r.summary || `migrated revision ${r.seq}`, author: r.origin === 'agent' ? person(r.actor) : cloudPerson('mira@acme.example', 'Mira'), createdAt: r.createdAt, config: clone(r.config), catalog: r.catalog, data: { migratedFrom: { revId: r.revId, seq: r.seq } } }),
        seq: r.seq,
      });
    mk(LOCAL, local);
    mk(CLOUD, cloud);
  }
  const head = () => local.at(-1) ?? null;
  const headCfg = (): Config => clone(head()?.config ?? { variables: [], secrets: [], files: [], sites: [] });
  // two newer cloud commits (theme yellow; greeting + api memory + a removed route)
  if (head()) {
    const c8 = clone(head()!.config);
    c8.variables.find((v) => v.variableSlug === 'theme')!.value = 'yellow';
    const c9 = clone(c8);
    c9.variables.find((v) => v.variableSlug === 'greeting')!.value = 'Hello from the cloud';
    const app = c9.sites.find((s) => s.siteSlug === D.SELF)!;
    app.config.containers[0].resources = { ...app.config.containers[0].resources, memoryMb: 192 };
    app.config.edge.routes = app.config.edge.routes.slice(0, Math.max(1, app.config.edge.routes.length - 1));
    // knob opm.mock.presetProposal=1 (§21 #102): the cloud commit also adds a dev-tool preset on this site
    if (ls('opm.mock.presetProposal') === '1') {
      const pg = newDevToolPreset('postgres', []);
      pg.presetSlug = 'plant-db';
      pg.name = 'Plant database';
      pg.target = { kind: 'host', host: '10.0.0.12', port: 5432 };
      pg.auth.username = 'postgres';
      app.config.devTools = [pg];
    }
    for (const [cfg, msg, ago] of [
      [c8, 'Theme green → yellow\n\nRequested by the plant manager for the night shift screens.', 3600 * 5],
      [c9, 'Greeting from the cloud, api memory 192 MB, drop the old /legacy route', 1800],
    ] as [Config, string, number][])
      cloud.push(makeCommit({ commitId: D.mockUlid(now() - ago * 1000), branch: CLOUD, parent: cloud.at(-1)!, kind: 'change', message: msg, author: cloudPerson('mira@acme.example', 'Mira'), createdAt: D.iso(now() - ago * 1000), config: cfg, catalog: D.catalog }));
  }
  let mergedFrom: { commitId: string; seq: number } | null = head() ? { commitId: head()!.commitId, seq: head()!.seq } : null;
  let base: { config: Config; sourceCommitId: string | null; skipped: MergeSkip[] } | null = head() ? { config: clone(head()!.config), sourceCommitId: head()!.commitId, skipped: [] } : null;
  /** the live state of the server as the drift scan sees it; null = equal to the head */
  let live: Config | null = null;
  let notes: DriftNote[] = [];
  let scannedAt = D.iso(now() - 40_000);
  const observed: Record<string, { detail: string; at: string; origin: DriftItem['origin'] }> = {};
  if (head() && ls('opm.mock.branch.clean') !== '1') {
    live = headCfg();
    const app = live.sites.find((s) => s.siteSlug === D.SELF)!;
    const web = app.config.containers.find((c) => c.containerSlug === 'hello-web') ?? app.config.containers[0];
    app.config.containers.push({ ...clone(web), containerSlug: 'adminer', serviceSlug: 'adminer', version: 'local-1f2e3d4c5b6a', ports: [], volumes: [], depends: [], env: [{ name: 'ADMINER_DEFAULT_SERVER', value: 'redis', secretRef: null }] });
    observed[`sites/${D.SELF}/containers/adminer`] = { detail: 'created by hand on the site network (docker run --network opm_…_app --name adminer adminer:4.8.1) · image only on this server', at: D.iso(now() - 3600_000), origin: 'docker' };
    const banner = live.files.find((f) => f.fileSlug === 'client-cert') ?? live.files[0];
    const was = D.fileBlobs[(banner.content as { sha256: string } | null)?.sha256 ?? ''] ?? '';
    const edited = was.replace(/-----END CERTIFICATE-----\n?$/, '-----END CERTIFICATE-----\n# renewed by hand on the server (intermediate added)\n-----BEGIN CERTIFICATE-----\nMIIBINTERMEDIATE\n-----END CERTIFICATE-----\n');
    banner.content = { sha256: D.fakeSha('edited-on-server'), sizeBytes: edited.length };
    D.fileBlobs[D.fakeSha('edited-on-server')] = edited;
    observed[`files/${banner.fileSlug}`] = { detail: `the per-site copy at ${banner.location?.kind === 'path' ? banner.location.path : `<agentHome>/files/${banner.fileSlug}`} was edited on the server (vim, 12 min ago)`, at: D.iso(now() - 720_000), origin: 'file' };
    notes = [{ fingerprint: 'file:api-config:sha256:9a1f', key: 'files/api-config', label: 'Rendered file api-config edited on the server', detail: 'config/api.json is rendered from expressions (render: expressions) and cannot be turned back into a template. Discard re-renders it; Accept keeps the edit until it changes again; Take the bytes makes the edited bytes the new File content (render none).', actions: ['discard', 'accept', 'take-bytes'] }];
  }
  const pending = new Map<string, { commit: Omit<Commit, 'config' | 'hash' | 'stats' | 'seq' | 'parentId'>; config: Config; merge?: { source: Commit; notTaken: MergeItem[] }; editKeys: string[]; driftKeys: string[] }>();
  /** other sites for Fast-forward all (mock: db strictly behind, northwind has drift) */
  const others: Record<string, { merged: { commitId: string; seq: number } | null; drift: number }> = { db: { merged: mergedFrom, drift: 0 }, northwind: { merged: mergedFrom, drift: 2 } };

  const toRev = (cfg: Config, msg: string, actor: string): Revision => D.makeRevision((head()?.seq ?? 0) + 1, cfg, { createdMs: now(), parentRevId: head()?.commitId ?? null, origin: 'agent', actor, summary: msg.split('\n')[0].slice(0, 200) });
  function settle() {
    for (const [id, p] of pending) {
      const run = deps.getRun(id);
      if (!run?.finishedAt) continue;
      pending.delete(id);
      if (!run.result?.ok) {
        deps.addActivity(D.SELF, 'branch.merge-failed', `Merge failed and was rolled back: ${run.result?.message ?? ''}`, run.approvedBy, { updateId: id });
        continue;
      }
      const c = { ...makeCommit({ ...p.commit, branch: LOCAL, parent: head(), config: p.config, catalog: D.catalog, scope: D.SELF }), commitId: run.revId, createdAt: run.finishedAt, data: { ...p.commit.data, updateId: id } };
      local.push(c);
      if (p.merge) {
        mergedFrom = { commitId: p.merge.source.commitId, seq: p.merge.source.seq };
        base = { config: clone(p.merge.source.config), sourceCommitId: p.merge.source.commitId, skipped: nextSkips(p.merge.notTaken, base?.skipped ?? [], p.merge.source.seq, c.createdAt, run.approvedBy) };
      }
      if (live) live = applyMerge(null, c.config, live, p.driftKeys.map((key) => ({ key, take: 'source' as const })), OPTS).config;
      // a merge also replaces what runs: drift on keys the merge changed is gone
      if (p.merge && live) live = null;
      deps.addActivity(D.SELF, c.kind === 'merge' ? 'branch.merged' : c.kind === 'cherry-pick' ? 'branch.cherry-picked' : c.kind === 'revert' ? 'branch.reverted' : c.kind === 'reset' ? 'branch.reset' : 'branch.committed', `${c.kind} seq ${c.seq}: ${c.message.split('\n')[0]}`, run.approvedBy, { commitId: c.commitId, seq: c.seq });
    }
  }
  const liveCfg = () => live ?? headCfg();
  const origin = (key: string): DriftItem['origin'] => observed[key]?.origin ?? (key.startsWith('files/') ? 'file' : key.startsWith('secrets/') ? 'secret-file' : key.includes('/volumes/') ? 'volume-content' : 'docker');
  function uncommitted(): { items: DriftItem[]; editKeys: Set<string>; driftKeys: Set<string>; combined: Config } {
    const h = headCfg();
    const driftItems = uncommittedItems(h, liveCfg(), OPTS);
    const editKeys = new Set<string>();
    const driftKeys = new Set(driftItems.map((i) => i.key));
    const items: DriftItem[] = driftItems.map((i) => ({ ...i, origin: origin(i.key), detail: observed[i.key]?.detail ?? 'changed on the server outside the agent', observedAt: observed[i.key]?.at ?? scannedAt }));
    const combined = liveCfg();
    return { items, editKeys, driftKeys, combined };
  }
  const counts = () => {
    const u = uncommitted();
    return { working: 0, drift: u.driftKeys.size, notes: notes.length, scannedAt };
  };
  const dirtyCount = () => {
    const c = counts();
    return c.drift + c.notes;
  };
  const cloudHead = () => cloud.at(-1) ?? null;
  const cloudById = (id: string) => cloud.find((c) => c.commitId === id) ?? deps.fail('not-found', `no cloud commit ${id}`);
  const localById = (id: string) => local.find((c) => c.commitId === id) ?? deps.fail('not-found', `no local commit ${id}`);
  const behind = () => (cloudHead() && mergedFrom ? cloud.filter((c) => c.seq > mergedFrom!.seq).length : cloud.length);
  const detail = (c: Commit, list: Commit[]): CommitDetail => {
    const parent = list.find((x) => x.commitId === c.parentId) ?? null;
    return { commit: { ...c, config: deps.maskConfig(c.config) }, changes: uncommittedItems(parent?.config ?? null, c.config, { secretValues: true }) };
  };
  const pageOf = <T>(list: T[], q: URLSearchParams) => {
    const off = Number(q.get('cursor') ?? 0) || 0;
    const lim = Math.min(100, Number(q.get('limit') ?? 30) || 30);
    return { items: list.slice(off, off + lim), cursor: off + lim < list.length ? String(off + lim) : null };
  };
  const requireMessage = (m: unknown) => {
    const issues = checkCommitMessage(m);
    if (issues.length) deps.fail('invalid', issues[0].message, { errors: issues });
    return String(m).trim();
  };
  /** §21 #110 a running update never blocks: the new run stops it (mock startRun) */
  const requireIdle = () => {};
  function plan(source: Commit, diffOnly = false): MergePlan {
    const items = planMerge(base?.config ?? null, source.config, headCfg(), { ...OPTS, skipped: base?.skipped ?? [] });
    const refs = withReferenceSites(headCfg(), source.config, D.SELF);
    const n = dirtyCount();
    return {
      source: { point: { branch: CLOUD, at: { commitId: source.commitId } }, label: 'cloud', commit: commitRef(source) },
      target: { branch: LOCAL, label: `this site (${D.SELF})`, head: head() ? commitRef(head()!) : null, workingClean: n === 0, uncommitted: n },
      base: base ? { kind: 'record', sourceCommitId: base.sourceCommitId, label: `cloud@${cloud.find((c) => c.commitId === base!.sourceCommitId)?.seq ?? '?'} (last merge)` } : { kind: 'none', sourceCommitId: null, label: 'no common history' },
      items,
      counts: countMerge(items),
      fastForward: isFastForward(items),
      references: { updatedSites: refs.updatedSites },
      diffOnly,
    };
  }
  function mergeResult(b: LocalMergeRequest) {
    const source = cloudById(b.sourceCommitId);
    const r = applyMerge(base?.config ?? null, source.config, headCfg(), b.choices ?? [], { ...OPTS, skipped: base?.skipped ?? [], values: b.values });
    const config = withReferenceSites(r.config, source.config, D.SELF).config;
    return { source, r, config };
  }
  function runtime(cfg: Config) {
    const a = headCfg().sites.find((s) => s.siteSlug === D.SELF)?.config.containers ?? [];
    const b = cfg.sites.find((s) => s.siteSlug === D.SELF)?.config.containers ?? [];
    const out: { containerSlug: string; action: string }[] = [];
    for (const c of b) {
      const o = a.find((x) => x.containerSlug === c.containerSlug);
      if (!o) out.push({ containerSlug: c.containerSlug, action: 'create' });
      else if (JSON.stringify(o) !== JSON.stringify(c)) out.push({ containerSlug: c.containerSlug, action: 'recreate' });
    }
    for (const o of a) if (!b.some((x) => x.containerSlug === o.containerSlug)) out.push({ containerSlug: o.containerSlug, action: 'remove' });
    return out;
  }
  function runApply(cfg: Config, message: string, actor: string, commit: Partial<Commit>, extra: { merge?: { source: Commit; notTaken: MergeItem[] }; editKeys?: string[]; driftKeys?: string[] } = {}): UpdateRun {
    requireIdle();
    const run = deps.startRun(toRev(cfg, message, actor), actor);
    run.kind = 'merge';
    pending.set(run.updateId, {
      commit: { commitId: run.revId, branch: LOCAL, kind: 'change', message, author: person(actor), createdAt: D.iso(now()), source: null, revertOf: null, resetTo: null, rollbackOf: null, catalog: D.catalog, catalogHash: null, data: {}, ...commit } as Pick<Commit, 'commitId'> as never,
      config: cfg,
      merge: extra.merge,
      editKeys: extra.editKeys ?? [],
      driftKeys: extra.driftKeys ?? [],
    });
    return run;
  }

  const H: Partial<Record<string, Handler>> = {
    branchState: (): AgentBranchState => {
      settle();
      const ch = cloudHead();
      const c = counts();
      const run = deps.runningRun();
      const strict = (m: { commitId: string; seq: number } | null, drift: number) => !!ch && !!m && m.seq < ch.seq && drift === 0;
      const selfStrict = strict(mergedFrom, c.drift + c.notes) && local.filter((x) => x.seq > (mergedFrom?.seq ?? 0)).length === 0;
      return {
        siteSlug: D.SELF,
        cloud: { head: ch ? { ...commitRef(ch), catalogHash: ch.catalogHash } : null, recent: [...cloud].reverse().slice(0, 10).map(commitRef), staged: Object.fromEntries(['app', 'db', 'northwind'].map((s) => [s, D.stagingOf(s === 'northwind' ? 'downloading' : 'ready', s === 'northwind' ? 0.62 : 1, now() - 60_000)])) },
        local: { head: head() ? commitRef(head()!) : null, commits: [...local].reverse().slice(0, 10).map(commitSummaryOf), mergedFrom, ahead: local.filter((x) => mergedFrom && x.seq > (local.find((l) => l.source?.commitId === mergedFrom!.commitId || l.commitId === mergedFrom!.commitId)?.seq ?? 0)).length, behind: behind(), differs: ch ? planMerge(null, ch.config, headCfg(), OPTS).length : 0 },
        uncommitted: c,
        run: run && pending.has(run.updateId) ? run : null,
        sites: [
          { siteSlug: D.SELF, reachable: true, agentVersion: '0.14.0', head: head() ? commitRef(head()!) : null, mergedFrom, uncommitted: c, strictlyBehind: selfStrict, canFastForward: selfStrict, reason: selfStrict ? null : c.drift + c.notes ? `${c.drift + c.notes} uncommitted local changes` : behind() === 0 ? 'up to date' : 'has local commits — merge it on its own Branch page' },
          ...Object.entries(others).map(([s, o]) => {
            const reach = deps.reachable(s);
            const ok = reach && strict(o.merged, o.drift);
            return { siteSlug: s, reachable: reach, agentVersion: '0.14.0', head: o.merged ? commitRef(cloud.find((x) => x.commitId === o.merged!.commitId) ?? head()!) : null, mergedFrom: o.merged, uncommitted: { working: 0, drift: o.drift, notes: 0, scannedAt }, strictlyBehind: ok, canFastForward: ok, reason: !reach ? 'unreachable' : o.drift ? `${o.drift} uncommitted server changes — commit them in its agent GUI` : !ch || (o.merged?.seq ?? 0) >= ch.seq ? 'up to date' : null };
          }),
        ],
      };
    },
    getBranchBlob: (c) => {
      const t = D.fileBlobs[c.params.sha256];
      if (t === undefined) deps.fail('not-found', `no blob ${c.params.sha256}`);
      return new Response(t, { headers: { 'Content-Type': 'application/octet-stream' } });
    },
    listBranchCommits: (c) => (settle(), pageOf([...local].reverse().map(commitSummaryOf), c.query)),
    getBranchCommit: (c) => detail(localById(c.params.commitId), local),
    getCloudCommit: (c) => detail(cloudById(c.params.commitId), cloud),
    getUncommitted: (): UncommittedResponse => {
      settle();
      return { items: uncommitted().items, notes, scannedAt, headCommitId: head()?.commitId ?? null };
    },
    putWorking: () => deps.fail('gone', 'GUI edits live in the browser until Commit (§21 #105): POST /_opm/api/branch/commit { message, baseCommitId, config }.', { hint: 'POST /_opm/api/branch/commit' }),
    rescanDrift: () => {
      settle();
      scannedAt = D.iso(now());
      deps.addActivity(D.SELF, 'drift.detected', `Drift scan: ${counts().drift} server changes, ${notes.length} notes`, null, { items: counts().drift, notes: notes.length });
      return { scannedAt, headCommitId: head()?.commitId ?? null, items: uncommitted().items.filter((i) => i.origin !== 'edit'), notes, localImages: live ? ['adminer:local-1f2e3d4c5b6a'] : [] };
    },
    commitLocal: (c, _s, actor) => {
      settle();
      const b = c.body as LocalCommitRequest;
      const message = requireMessage(b?.message);
      requireIdle();
      // §21 #105 GUI edits: the browser change set → apply, commit only when the apply succeeds
      if (b.config) {
        if ((b.keys ?? []).length || (b.accept ?? []).length || (b.takeBytes ?? []).length) deps.fail('invalid', 'Commit GUI edits (config) and server changes (keys / accept / takeBytes) separately.');
        if ((b.baseCommitId ?? null) !== (head()?.commitId ?? null)) deps.fail('stale', 'This site moved on since you started editing — rebase your changes.', { reason: 'head-moved', head: head() ? commitRef(head()!) : null });
        const k = counts();
        if (k.drift + k.notes) deps.fail('conflict', `${k.drift + k.notes} change(s) made on the server are not committed — commit or discard them first.`, { reason: 'uncommitted-drift', drift: k.drift, notes: k.notes });
        const cfg = deps.buildConfig(b.config, headCfg()).config;
        const issues = deps.validate(cfg, headCfg(), true).filter((i) => i.severity === 'error');
        if (issues.length) deps.fail('invalid', 'The changes have errors.', { errors: issues });
        if (!uncommittedItems(headCfg(), cfg, OPTS).length) deps.fail('invalid', 'Nothing to commit: the changes equal the head.', { errors: [{ path: 'config', code: 'V_NO_CHANGE', message: 'nothing to commit', severity: 'error' }] });
        if (ls('opm.mock.branch.failApply') === '1') {
          try {
            localStorage.setItem('opm.mock.fail', 'apply');
            localStorage.removeItem('opm.mock.branch.failApply');
          } catch {
            /* */
          }
        }
        const sv = secretValueChanges(headCfg().secrets, cfg.secrets, (b.config.secrets ?? []).filter((x) => x.generateNow).map((x) => x.secretSlug));
        const keys = uncommittedItems(headCfg(), cfg, OPTS).map((i) => i.key);
        const run = runApply(cfg, message, actor!, { kind: 'change', data: sv.set.length + sv.generated.length + sv.cleared.length ? { secretValues: sv } : {} }, { editKeys: keys });
        return { commit: null, run };
      }
      // drift: record what was changed on the server (nothing restarts), take-bytes: edited bytes → File content (applied)
      const after = uncommitted();
      let cfg = applyMerge(null, after.combined, headCfg(), (b.keys ?? after.items.map((i) => i.key)).map((key) => ({ key, take: 'source' as const })), OPTS).config;
      const keys = b.keys ?? after.items.map((i) => i.key);
      for (const k of keys) if (!after.items.some((i) => i.key === k)) deps.fail('invalid', `no uncommitted change ${k} (rescan / reload)`, { errors: [{ path: 'keys', code: 'V_MERGE_CHOICE', message: `unknown key ${k}`, severity: 'error' }] });
      const tb: string[] = [];
      for (const fp of b.takeBytes ?? []) {
        const n = notes.find((x) => x.fingerprint === fp);
        if (!n?.key) deps.fail('invalid', `no drift note ${fp} with take-bytes`);
        const slug = n!.key!.split('/')[1];
        cfg = clone(cfg);
        const f = cfg.files.find((x) => x.fileSlug === slug);
        if (f) Object.assign(f, { render: 'none', content: { sha256: D.fakeSha(`bytes-${fp}`), sizeBytes: 211 } });
        tb.push(`files/${slug}`);
      }
      const accepted = notes.filter((n) => (b.accept ?? []).includes(n.fingerprint) || (b.takeBytes ?? []).includes(n.fingerprint));
      if (!keys.length && !accepted.length) deps.fail('invalid', 'Nothing to commit.', { errors: [{ path: 'keys', code: 'V_NO_CHANGE', message: 'nothing selected', severity: 'error' }] });
      const driftKeys = keys.filter((k) => after.driftKeys.has(k));
      notes = notes.filter((n) => !accepted.includes(n));
      const data = accepted.length ? { acceptedDrift: accepted.map((n) => ({ fingerprint: n.fingerprint, label: n.label, detail: n.detail })) } : {};
      if (tb.length) {
        const run = runApply(cfg, message, actor!, { kind: 'change', data }, { editKeys: tb, driftKeys });
        return { commit: null, run };
      }
      // drift only: recorded at once (it already runs on the server)
      const rev = toRev(cfg, message, actor!);
      const commit = makeCommit({ commitId: rev.revId, branch: LOCAL, parent: head(), kind: 'drift', message, author: person(actor!), createdAt: D.iso(now()), config: cfg, catalog: D.catalog, data: { ...data, updateId: null }, scope: D.SELF });
      local.push(commit);
      deps.adopt(rev);
      if (live && configHash(live) === configHash(cfg)) live = null;
      else if (live) live = applyMerge(null, cfg, live, driftKeys.map((key) => ({ key, take: 'source' as const })), OPTS).config;
      if (live && !uncommittedItems(headCfg(), live, OPTS).length) live = null;
      deps.addActivity(D.SELF, 'drift.committed', `Drift committed (seq ${commit.seq}): ${message.split('\n')[0]}`, actor, { commitId: commit.commitId, keys: driftKeys });
      if (accepted.length) deps.addActivity(D.SELF, 'drift.accepted', `${accepted.length} drift note(s) accepted`, actor, { fingerprints: accepted.map((n) => n.fingerprint), commitId: commit.commitId });
      return { commit, run: null };
    },
    discardLocal: (c, _s, actor) => {
      settle();
      const b = c.body as { keys?: string[]; notes?: string[] };
      const u = uncommitted();
      const h = headCfg();
      const dk = (b.keys ?? []).filter((k) => u.driftKeys.has(k));
      for (const k of b.keys ?? []) if (!u.driftKeys.has(k)) deps.fail('invalid', `no uncommitted change ${k}`);
      if (dk.length && live) live = applyMerge(null, h, live, dk.map((key) => ({ key, take: 'source' as const })), OPTS).config;
      if (live && !uncommittedItems(h, live, OPTS).length) live = null;
      notes = notes.filter((n) => !(b.notes ?? []).includes(n.fingerprint));
      if (dk.length || (b.notes ?? []).length) deps.addActivity(D.SELF, 'drift.discarded', `${dk.length + (b.notes ?? []).length} server change(s) re-applied from the branch`, actor, { keys: dk, notes: b.notes ?? [] });
      return { run: null };
    },
    compareLocal: (c) => {
      settle();
      const b = c.body as LocalCompareRequest;
      if (b?.source && typeof b.source === 'object' && 'localCommitId' in b.source) {
        const lc = localById(b.source.localCommitId);
        const items = planMerge(null, lc.config, headCfg(), OPTS);
        return { source: { point: { branch: LOCAL, at: { commitId: lc.commitId } }, label: `this site@${lc.seq}`, commit: commitRef(lc) }, target: { branch: LOCAL, label: 'this site', head: head() ? commitRef(head()!) : null, workingClean: true, uncommitted: 0 }, base: { kind: 'none', sourceCommitId: null, label: 'two commits' }, items, counts: countMerge(items), fastForward: false, diffOnly: true } satisfies MergePlan;
      }
      const src = b?.source && typeof b.source === 'object' && 'cloudCommitId' in b.source ? cloudById(b.source.cloudCommitId) : cloudHead();
      if (!src) deps.fail('not-found', 'the cloud has no commit yet');
      return plan(src!, !!b?.diffOnly);
    },
    previewLocalMerge: (c): MergePreview => {
      settle();
      const { r, config } = mergeResult(c.body as LocalMergeRequest);
      const issues = deps.validate(config, headCfg());
      return {
        result: deps.maskConfig(config),
        changes: diffConfigs(headCfg(), config),
        validation: { errors: issues.filter((i) => i.severity === 'error'), warnings: issues.filter((i) => i.severity === 'warning') },
        unresolved: r.unresolved,
        missing: { variables: [], secrets: [] },
        choiceErrors: r.errors,
        kind: r.kind,
        taken: r.taken.length,
        skipped: r.notTaken.length,
        runtime: runtime(config),
        warnings: [],
      };
    },
    mergeLocal: (c, _s, actor) => {
      settle();
      const b = c.body as LocalMergeRequest;
      const message = requireMessage(b?.message);
      const n = dirtyCount();
      if (n) deps.fail('conflict', `Commit or discard the ${n} change(s) made on the server first.`, { reason: 'uncommitted-drift', drift: counts().drift, notes: notes.length, uncommitted: n });
      if ((b.targetHead ?? null) !== (head()?.commitId ?? null)) deps.fail('stale', 'This site moved on since the plan was made. Refresh.', { head: head()?.commitId });
      const { source, r, config } = mergeResult(b);
      if (r.unresolved.length) deps.fail('invalid', `${r.unresolved.length} conflicts need a decision.`, { errors: r.unresolved.map((k) => ({ path: k, code: 'V_MERGE_UNRESOLVED', message: `conflict ${k} needs a decision`, severity: 'error' })) });
      if (r.errors.length) deps.fail('invalid', r.errors[0].message, { errors: r.errors.map((e) => ({ path: e.key, code: 'V_MERGE_CHOICE', message: e.message, severity: 'error' })) });
      return runApply(config, message, actor!, { kind: r.kind, source: { branch: CLOUD, commitId: source.commitId, seq: source.seq, label: 'cloud', picks: r.kind === 'merge' ? 'all' : r.taken, skipped: r.notTaken.length } }, { merge: { source, notTaken: r.notTaken } });
    },
    fastForwardAll: (c, _s, actor) => {
      settle();
      const b = c.body as { sourceCommitId: string; sites: string[]; message: string };
      const message = requireMessage(b?.message);
      if (!b.sites?.length) deps.fail('invalid', 'pick at least one site');
      const source = cloudById(b.sourceCommitId);
      for (const s of b.sites) if (s !== D.SELF && others[s]) others[s].merged = { commitId: source.commitId, seq: source.seq };
      if (b.sites.includes(D.SELF)) {
        if (dirtyCount()) deps.fail('conflict', 'This site has uncommitted changes.', { reason: 'uncommitted-drift', drift: counts().drift, notes: notes.length });
        const items = planMerge(base?.config ?? null, source.config, headCfg(), OPTS);
        const r = applyMerge(base?.config ?? null, source.config, headCfg(), items.map((i) => ({ key: i.key, take: 'source' as const })), OPTS);
        const cfg = withReferenceSites(r.config, source.config, D.SELF).config;
        return runApply(cfg, message, actor!, { kind: 'merge', source: { branch: CLOUD, commitId: source.commitId, seq: source.seq, label: 'cloud', picks: 'all', skipped: 0 } }, { merge: { source, notTaken: [] } });
      }
      const sim = deps.startRun(toRev(headCfg(), message, actor!), actor!);
      return sim;
    },
    revertLocal: (c, _s, actor) => {
      settle();
      const b = c.body as LocalRevertRequest;
      const message = requireMessage(b?.message);
      if (dirtyCount()) deps.fail('conflict', 'Commit or discard the local changes first.', { reason: 'uncommitted-drift', drift: counts().drift, notes: notes.length });
      const cm = localById(b.commitId);
      const parent = local.find((x) => x.commitId === cm.parentId) ?? null;
      if (!parent) deps.fail('invalid', 'the first commit cannot be reverted');
      const r = applyRevert(cm.config, parent!.config, headCfg(), (b.choices ?? []) as MergeChoice[], OPTS);
      if (r.unresolved.length) deps.fail('invalid', `${r.unresolved.length} conflicts need a decision.`);
      return runApply(r.config, message, actor!, { kind: 'revert', revertOf: { commitId: cm.commitId, seq: cm.seq } });
    },
    resetLocal: (c, _s, actor) => {
      settle();
      const b = c.body as LocalRevertRequest;
      const message = requireMessage(b?.message);
      if (dirtyCount()) deps.fail('conflict', 'Commit or discard the local changes first.', { reason: 'uncommitted-drift', drift: counts().drift, notes: notes.length });
      const cm = localById(b.commitId);
      return runApply(clone(cm.config), message, actor!, { kind: 'reset', resetTo: { commitId: cm.commitId, seq: cm.seq } });
    },
  };
  /** Backups → Roll back (IntentFlow): the rollback commit is written when the run finishes ok (kind rollback) */
  function onRollback(run: UpdateRun, message: string, snapshotId: string, actor: string) {
    const cfg = deps.revisions().find((r) => r.revId === run.revId)?.config ?? headCfg();
    pending.set(run.updateId, { commit: { commitId: run.revId, branch: LOCAL, kind: 'rollback', message, author: person(actor), createdAt: D.iso(now()), source: null, revertOf: null, resetTo: null, rollbackOf: { snapshotId }, catalog: D.catalog, catalogHash: null, data: {} } as never, config: clone(cfg), editKeys: [], driftKeys: [] });
  }
  return { handlers: H, onRollback };
}
/** client-side revert plan (the GUI computes it from two masked commits; kept here for tests) */
export const revertItems = revertPlan;
